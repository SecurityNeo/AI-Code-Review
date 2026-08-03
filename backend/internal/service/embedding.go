package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/ai-optimizer/backend/internal/model"
	"github.com/ai-optimizer/backend/internal/vectorstore"
	"go.uber.org/zap"
)

const (
	embeddingBatchSize   = 16
	embeddingMaxRetries  = 3
	embeddingRetryBaseMs = 500
)

// EmbeddingService handles text-to-vector conversion.
type EmbeddingService struct {
	store  vectorstore.Store
	client *http.Client
}

// NewEmbeddingService creates a new embedding service.
func NewEmbeddingService(store vectorstore.Store) *EmbeddingService {
	return &EmbeddingService{
		store: store,
		client: &http.Client{
			Transport: &http.Transport{
				MaxIdleConns:        20,
				MaxIdleConnsPerHost: 10,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	}
}

// IsAvailable checks whether an embedding model is configured and active.
func (s *EmbeddingService) IsAvailable() bool {
	cfg := s.getConfig()
	if cfg.EmbeddingModelID == nil {
		return false
	}
	var m model.LLMModel
	if err := model.DB.First(&m, *cfg.EmbeddingModelID).Error; err != nil {
		return false
	}
	return m.Status == "active" && m.ModelType == string(model.ModelTypeEmbedding)
}

// Embed converts a single text into a vector.
func (s *EmbeddingService) Embed(ctx context.Context, text string) (vectorstore.Vector, int, error) {
	cfg := s.getConfig()
	if cfg.EmbeddingModelID == nil {
		return nil, 0, fmt.Errorf("embedding model not configured")
	}

	var m model.LLMModel
	if err := model.DB.First(&m, *cfg.EmbeddingModelID).Error; err != nil {
		return nil, 0, fmt.Errorf("embedding model not found: %w", err)
	}
	if m.Status != "active" {
		return nil, 0, fmt.Errorf("embedding model is not active")
	}

	vec, tokens, err := s.callEmbeddingAPI(ctx, m, text)
	if err != nil {
		return nil, 0, err
	}

	s.recordCallLog(&m, tokens)
	return vec, tokens, nil
}

// EmbedAndStore converts text and persists the vector.
func (s *EmbeddingService) EmbedAndStore(ctx context.Context, key vectorstore.Key, text string) error {
	vec, _, err := s.Embed(ctx, text)
	if err != nil {
		return err
	}
	return s.store.Save(ctx, vectorstore.Item{
		Key:       key,
		Vector:    vec,
		Dimension: len(vec),
	})
}

// EmbedBatch converts multiple texts into vectors in batches.
// It uses the configured embedding model's batch API when possible,
// with retry and linear backoff. Falls back to single requests
// if the batch API is unsupported.
func (s *EmbeddingService) EmbedBatch(ctx context.Context, texts []string) ([]vectorstore.Vector, int, error) {
	if len(texts) == 0 {
		return nil, 0, nil
	}

	cfg := s.getConfig()
	if cfg.EmbeddingModelID == nil {
		return nil, 0, fmt.Errorf("embedding model not configured")
	}

	var m model.LLMModel
	if err := model.DB.First(&m, *cfg.EmbeddingModelID).Error; err != nil {
		return nil, 0, fmt.Errorf("embedding model not found: %w", err)
	}
	if m.Status != "active" {
		return nil, 0, fmt.Errorf("embedding model is not active")
	}

	var allVecs []vectorstore.Vector
	var totalTokens int

	for i := 0; i < len(texts); i += embeddingBatchSize {
		end := i + embeddingBatchSize
		if end > len(texts) {
			end = len(texts)
		}
		batch := texts[i:end]

		vecs, tokens, err := s.callEmbeddingBatchAPIWithRetry(ctx, m, batch)
		if err != nil {
			// Fallback: sequential single embedding requests
			zap.L().Warn("embedding batch failed, falling back to single requests",
				zap.Uint("model_id", m.ID),
				zap.Int("batch_start", i),
				zap.Int("batch_size", len(batch)),
				zap.Error(err))
			for _, t := range batch {
				vec, tk, err2 := s.callEmbeddingAPI(ctx, m, t)
				if err2 != nil {
					return nil, 0, fmt.Errorf("embed batch fallback failed at index %d: %w", i, err2)
				}
				allVecs = append(allVecs, vec)
				totalTokens += tk
			}
			continue
		}
		allVecs = append(allVecs, vecs...)
		totalTokens += tokens
	}

	s.recordCallLog(&m, totalTokens)
	return allVecs, totalTokens, nil
}

// callEmbeddingAPI calls the provider's embedding endpoint for a single text.
func (s *EmbeddingService) callEmbeddingAPI(ctx context.Context, m model.LLMModel, text string) (vectorstore.Vector, int, error) {
	reqBody := map[string]any{
		"model": m.ModelID,
		"input": text,
	}
	body, _ := json.Marshal(reqBody)

	reqCtx, cancel := context.WithTimeout(ctx, time.Duration(m.TimeoutSec)*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, "POST", m.BaseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+m.APIKey)

	resp, err := s.client.Do(req)
	if err != nil {
		zap.L().Error("[Embedding] request failed", zap.Error(err))
		return nil, 0, fmt.Errorf("embedding request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		zap.L().Error("[Embedding] API returned non-OK status",
			zap.Int("status", resp.StatusCode),
			zap.String("body", string(respBody)))
		return nil, 0, fmt.Errorf("embedding API error: status=%d, body=%s", resp.StatusCode, string(respBody))
	}

	result, err := parseEmbeddingResponse(respBody)
	if err != nil {
		return nil, 0, err
	}
	if len(result) == 0 {
		return nil, 0, fmt.Errorf("embedding response contains no data")
	}

	return result[0].Vector, result[0].Tokens, nil
}

// callEmbeddingBatchAPI sends a batch request to the embedding endpoint.
// Returns vectors aligned with input order, or an error.
func (s *EmbeddingService) callEmbeddingBatchAPI(ctx context.Context, m model.LLMModel, texts []string) ([]vectorstore.Vector, int, error) {
	if len(texts) == 0 {
		return nil, 0, nil
	}

	reqBody := map[string]any{
		"model": m.ModelID,
		"input": texts,
	}
	body, _ := json.Marshal(reqBody)

	reqCtx, cancel := context.WithTimeout(ctx, time.Duration(m.TimeoutSec)*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, "POST", m.BaseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+m.APIKey)

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("embedding batch request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("embedding batch API error: status=%d, body=%s", resp.StatusCode, string(respBody))
	}

	result, err := parseEmbeddingResponse(respBody)
	if err != nil {
		return nil, 0, err
	}
	if len(result) != len(texts) {
		return nil, 0, fmt.Errorf("embedding batch returned %d results, expected %d", len(result), len(texts))
	}

	vecs := make([]vectorstore.Vector, len(result))
	totalTokens := 0
	for i, r := range result {
		vecs[i] = r.Vector
		totalTokens += r.Tokens
	}
	return vecs, totalTokens, nil
}

// callEmbeddingBatchAPIWithRetry wraps batch API with linear backoff retry.
func (s *EmbeddingService) callEmbeddingBatchAPIWithRetry(ctx context.Context, m model.LLMModel, texts []string) ([]vectorstore.Vector, int, error) {
	var lastErr error
	for attempt := 0; attempt <= embeddingMaxRetries; attempt++ {
		if attempt > 0 {
			delay := time.Duration(attempt*embeddingRetryBaseMs) * time.Millisecond
			zap.L().Warn("[Embedding] batch retry",
				zap.Int("attempt", attempt),
				zap.Duration("delay", delay),
				zap.Error(lastErr))
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return nil, 0, ctx.Err()
			}
		}
		vecs, tokens, err := s.callEmbeddingBatchAPI(ctx, m, texts)
		if err == nil {
			return vecs, tokens, nil
		}
		lastErr = err
		// Only retry on network errors or server-side errors; 4xx client errors are fatal.
		if !isRetryableEmbeddingError(err) {
			break
		}
	}
	return nil, 0, lastErr
}

// parseEmbeddingResponse parses the common OpenAI-compatible embedding response.
// It normalizes both single-object and array-input responses into a flat slice.
func parseEmbeddingResponse(body []byte) ([]struct {
	Vector vectorstore.Vector
	Tokens int
}, error) {
	var raw struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
			Index     int       `json:"index"`
		} `json:"data"`
		Usage struct {
			PromptTokens int `json:"prompt_tokens"`
			TotalTokens  int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parse embedding response failed: %w", err)
	}
	if len(raw.Data) == 0 {
		return nil, fmt.Errorf("embedding response contains no data")
	}

	// Sort by index to guarantee input order.
	sort.Slice(raw.Data, func(i, j int) bool {
		return raw.Data[i].Index < raw.Data[j].Index
	})

	// Distribute total token usage evenly across items (API usually returns aggregate only).
	tokensPerItem := raw.Usage.TotalTokens / len(raw.Data)
	if tokensPerItem == 0 {
		tokensPerItem = raw.Usage.TotalTokens
	}

	out := make([]struct {
		Vector vectorstore.Vector
		Tokens int
	}, len(raw.Data))
	for i, d := range raw.Data {
		vec := make(vectorstore.Vector, len(d.Embedding))
		for j, v := range d.Embedding {
			vec[j] = float32(v)
		}
		out[i].Vector = vec
		out[i].Tokens = tokensPerItem
	}
	return out, nil
}

func isRetryableEmbeddingError(err error) bool {
	if err == nil {
		return false
	}
	errStr := strings.ToLower(err.Error())
	// Retry on transient network/server errors.
	for _, keyword := range []string{"timeout", "connection refused", "no such host", "temporary", "too many requests", "status=5", "status=429"} {
		if strings.Contains(errStr, keyword) {
			return true
		}
	}
	return false
}

func (s *EmbeddingService) getConfig() model.IncubatorConfig {
	var cfg model.IncubatorConfig
	if err := model.DB.First(&cfg, 1).Error; err != nil {
		zap.L().Warn("incubator config not found, using defaults", zap.Error(err))
	}
	return cfg
}

func (s *EmbeddingService) recordCallLog(m *model.LLMModel, tokens int) {
	costCents := calcCostCents(m, tokens, 0, 0) // embedding has no completion tokens
	log := &model.LLMCallLog{
		Provider:         m.Provider,
		ModelName:        m.ModelID,
		CallType:         "embedding",
		Caller:           "rule_incubator",
		PromptTokens:     tokens,
		CompletionTokens: 0,
		TotalTokens:      tokens,
		CostCents:        costCents,
		Status:           "success",
	}
	if err := model.DB.Create(log).Error; err != nil {
		zap.L().Warn("record embedding call log failed", zap.Error(err))
	}
}
