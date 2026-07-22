package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/ai-optimizer/backend/internal/model"
	"github.com/ai-optimizer/backend/internal/vectorstore"
	"go.uber.org/zap"
)

// EmbeddingService handles text-to-vector conversion.
type EmbeddingService struct {
	store vectorstore.Store
}

// NewEmbeddingService creates a new embedding service.
func NewEmbeddingService(store vectorstore.Store) *EmbeddingService {
	return &EmbeddingService{store: store}
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

// Embed converts a single text into a vector and optionally stores it.
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

	// Record usage
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

// callEmbeddingAPI calls the provider's embedding endpoint.
func (s *EmbeddingService) callEmbeddingAPI(ctx context.Context, m model.LLMModel, text string) (vectorstore.Vector, int, error) {
	client := &http.Client{Timeout: time.Duration(m.TimeoutSec) * time.Second}

	reqBody := map[string]any{
		"model": m.ModelID,
		"input": text,
	}
	body, _ := json.Marshal(reqBody)

	req, err := http.NewRequestWithContext(ctx, "POST", m.BaseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+m.APIKey)

	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("embedding request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("embedding API error: status=%d, body=%s", resp.StatusCode, string(respBody))
	}

	var result struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
		Usage struct {
			PromptTokens int `json:"prompt_tokens"`
			TotalTokens  int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, 0, fmt.Errorf("parse embedding response failed: %w", err)
	}
	if len(result.Data) == 0 {
		return nil, 0, fmt.Errorf("embedding response contains no data")
	}

	emb := result.Data[0].Embedding
	vec := make(vectorstore.Vector, len(emb))
	for i, v := range emb {
		vec[i] = float32(v)
	}
	return vec, result.Usage.TotalTokens, nil
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
