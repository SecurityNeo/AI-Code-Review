package service

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ai-optimizer/backend/internal/model"
	"github.com/ai-optimizer/backend/internal/vectorstore"
	"go.uber.org/zap"
)

// IncubatorService handles rule incubation business logic.
type IncubatorService struct {
	embedSvc *EmbeddingService
	store    vectorstore.Store
	SSEHub   *PipelineSSEHub
}

// NewIncubatorService creates a new incubator service.
func NewIncubatorService(embedSvc *EmbeddingService, store vectorstore.Store) *IncubatorService {
	return &IncubatorService{
		embedSvc: embedSvc,
		store:    store,
		SSEHub:   NewPipelineSSEHub(),
	}
}

// Status returns the current incubator status including embedding availability.
func (s *IncubatorService) Status() (map[string]any, error) {
	cfg := s.getConfig()
	embeddingConfigured := cfg.EmbeddingModelID != nil

	res := map[string]any{
		"embedding_configured": embeddingConfigured,
		"fallback_mode":        "keyword",
		"degraded_features":    []string{},
	}

	if embeddingConfigured {
		res["embedding_model_id"] = *cfg.EmbeddingModelID
		var m model.LLMModel
		if err := model.DB.First(&m, *cfg.EmbeddingModelID).Error; err == nil {
			res["embedding_model_name"] = m.ModelID
			available := m.Status == "active" && m.ModelType == string(model.ModelTypeEmbedding)
			res["embedding_available"] = available
			if available {
				res["fallback_mode"] = "none"
			}
		} else {
			res["embedding_available"] = false
			res["last_error"] = err.Error()
		}
	} else {
		res["embedding_available"] = false
		res["last_error"] = "embedding model not configured"
	}

	if res["fallback_mode"] == "keyword" {
		res["degraded_features"] = []string{
			"semantic_clustering",
			"semantic_dedup",
			"semantic_retro_match",
			"rule_degradation_precise",
		}
	}

	// Last cluster job
	var lastJob model.RuleIncubationJob
	if err := model.DB.Where("job_type = ?", "cluster").
		Order("created_at DESC").First(&lastJob).Error; err == nil {
		res["last_cluster_job"] = map[string]any{
			"id":           lastJob.ID,
			"status":       lastJob.Status,
			"completed_at": lastJob.CompletedAt,
		}
	}

	return res, nil
}

// ListUnmatchedIssues returns issues without an associated rule (rule_id IS NULL).
func (s *IncubatorService) ListUnmatchedIssues(filter IssueFilter) ([]model.ReviewIssue, int64, error) {
	db := model.DB.Model(&model.ReviewIssue{}).
		Where("rule_id IS NULL")

	if filter.StartDate != "" {
		db = db.Where("created_at >= ?", filter.StartDate)
	}
	if filter.EndDate != "" {
		db = db.Where("created_at <= ?", filter.EndDate+" 23:59:59")
	}
	// Note: review_issues table does not have a language column; filtering by file extension is a future enhancement.
	if filter.Language != "" {
		// placeholder: no-op until language column is added or inferred via JOIN
	}
	if filter.ProjectID > 0 {
		db = db.Where("task_id IN (SELECT id FROM tasks WHERE project_id = ?)", filter.ProjectID)
	}
	if filter.Category != "" {
		db = db.Where("category = ?", filter.Category)
	}
	if filter.Severity != "" {
		db = db.Where("severity = ?", filter.Severity)
	}
	if filter.Status != "" {
		db = db.Where("status = ?", filter.Status)
	}
	if filter.Keyword != "" {
		db = db.Where("message LIKE ? OR file LIKE ?", "%"+filter.Keyword+"%", "%"+filter.Keyword+"%")
	}

	var total int64
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var issues []model.ReviewIssue
	if err := db.Scopes(model.Paginate(filter.Page, filter.PageSize)).
		Order("created_at DESC").
		Find(&issues).Error; err != nil {
		return nil, 0, err
	}

	return issues, total, nil
}

// IssueFilter defines query parameters for the unmatched issue pool.
type IssueFilter struct {
	Page, PageSize int
	StartDate      string
	EndDate        string
	Language       string
	ProjectID      uint
	Category       string
	Severity       string
	Status         string
	Keyword        string
}

// CreateCandidate creates a RuleIncubation from selected issues.
func (s *IncubatorService) CreateCandidate(req CreateCandidateRequest, userID uint) (*model.RuleIncubation, error) {
	if len(req.SourceIssueIDs) == 0 {
		return nil, fmt.Errorf("source_issue_ids is empty")
	}

	var issues []model.ReviewIssue
	if err := model.DB.Where("id IN ?", req.SourceIssueIDs).Find(&issues).Error; err != nil {
		return nil, err
	}
	if len(issues) == 0 {
		return nil, fmt.Errorf("no issues found")
	}

	// Derive fields from issues
	catCount := make(map[string]int)
	sevCount := make(map[string]int)
	langCount := make(map[string]int)
	projectIDs := make(map[uint]struct{})
	accepted := 0
	for _, iss := range issues {
		catCount[iss.Category]++
		sevCount[iss.Severity]++
		langCount[inferLanguage(iss.File)]++
		if iss.Status == "accepted" {
			accepted++
		}

		// Infer project from task
		var t model.Task
		if err := model.DB.Select("project_id").First(&t, iss.TaskID).Error; err == nil {
			projectIDs[t.ProjectID] = struct{}{}
		}
	}

	sourceIssueIDsJSON, _ := json.Marshal(req.SourceIssueIDs)
	sourceProjects := make([]uint, 0, len(projectIDs))
	for pid := range projectIDs {
		sourceProjects = append(sourceProjects, pid)
	}
	sourceProjectsJSON, _ := json.Marshal(sourceProjects)

	code := generateIncubationCode(issues)

	cand := &model.RuleIncubation{
		Status:          "draft",
		Code:            code,
		Name:            s.suggestName(issues),
		Category:        modeKey(catCount),
		Severity:        modeKey(sevCount),
		Language:        modeKey(langCount),
		Description:     s.suggestDescription(issues),
		Prompt:          "", // To be refined by LLM or manually filled
		SourceIssueIDs:  string(sourceIssueIDsJSON),
		SourceCount:     len(issues),
		SourceProjects:  string(sourceProjectsJSON),
		ConfidenceScore: 0.5, // baseline
		UserAcceptRate:  float64(accepted) / float64(len(issues)),
		CreatedBy:       userID,
		PipelineJobID:   req.PipelineJobID,
		SimilarRules:    "{}",
		TestResults:     "{}",
	}

	if err := model.DB.Create(cand).Error; err != nil {
		return nil, err
	}

	if req.AutoRefine {
		_, _ = QueueJob("refine", map[string]any{"incubation_id": cand.ID})
	}

	return cand, nil
}

// CreateCandidateRequest is the input for creating a candidate rule.
type CreateCandidateRequest struct {
	SourceIssueIDs []uint `json:"source_issue_ids" binding:"required,min=1"`
	ClusterID      string `json:"cluster_id"`
	AutoRefine     bool   `json:"auto_refine"`
	PipelineJobID  *uint  `json:"pipeline_job_id"`
}

// GetCandidate returns a candidate by ID with source issues hydrated.
func (s *IncubatorService) GetCandidate(id uint) (*model.RuleIncubation, []model.ReviewIssue, error) {
	var cand model.RuleIncubation
	if err := model.DB.First(&cand, id).Error; err != nil {
		return nil, nil, err
	}

	var issueIDs []uint
	_ = json.Unmarshal([]byte(cand.SourceIssueIDs), &issueIDs)
	var issues []model.ReviewIssue
	if len(issueIDs) > 0 {
		model.DB.Where("id IN ?", issueIDs).Find(&issues)
	}

	return &cand, issues, nil
}

// UpdateCandidate edits a draft/ready candidate.
func (s *IncubatorService) UpdateCandidate(id uint, updates map[string]any) error {
	var cand model.RuleIncubation
	if err := model.DB.First(&cand, id).Error; err != nil {
		return err
	}
	if cand.Status == "published" || cand.Status == "rejected" || cand.Status == "merged" {
		return fmt.Errorf("cannot update candidate in status %s", cand.Status)
	}
	delete(updates, "id")
	delete(updates, "source_issue_ids")
	delete(updates, "created_by")
	delete(updates, "published_rule_id")
	return model.DB.Model(&cand).Updates(updates).Error
}

// calculateConfidenceScore computes a dynamic confidence score (0.40–1.00) for a candidate rule.
// It aggregates scores from:
//   1) Refine quality – based on prompt/description/name completeness
//   2) Sandbox test accuracy – proportion of passed LLM-generated test cases
//   3) Similar-check originality – penalized if similar existing rules are found
//   4) Source-issue accept rate – historical positive signal from users
func (s *IncubatorService) calculateConfidenceScore(cand *model.RuleIncubation) float64 {
	var score float64 = 0.50 // baseline from rule creation

	// 1. Refine quality: max +0.10
	if len(cand.Prompt) >= 100 {
		score += 0.06
	} else if len(cand.Prompt) > 0 {
		score += 0.03
	}
	if len(cand.Name) >= 5 && len(cand.Description) >= 20 {
		score += 0.04
	} else if len(cand.Name) > 0 && len(cand.Description) > 0 {
		score += 0.02
	}

	// 2. Sandbox test accuracy: max +0.25
	var testResult map[string]any
	if cand.TestResults != "" && cand.TestResults != "{}" {
		_ = json.Unmarshal([]byte(cand.TestResults), &testResult)
	}
	if acc, ok := testResult["accuracy"].(float64); ok {
		score += acc * 0.25
	}

	// 3. Similar-check originality: max +0.10
	if cand.SimilarRules != "" && cand.SimilarRules != "{}" {
		var similar []map[string]any
		if _ = json.Unmarshal([]byte(cand.SimilarRules), &similar); len(similar) == 0 {
			score += 0.10
		} else if len(similar) == 1 {
			score += 0.05
		} else {
			// 2+ similar rules -> no originality bonus
		}
	} else {
		score += 0.10
	}

	// 4. Source-issue quality (user accept rate): max +0.05
	score += cand.UserAcceptRate * 0.05

	// Clamp to [0.40, 1.00]
	if score > 1.0 {
		score = 1.0
	}
	if score < 0.4 {
		score = 0.4
	}

	return math.Round(score*100) / 100
}

// DeleteCandidate hard-deletes a candidate rule (protects published ones).
func (s *IncubatorService) DeleteCandidate(id uint) error {
	var cand model.RuleIncubation
	if err := model.DB.First(&cand, id).Error; err != nil {
		return err
	}
	if cand.Status == "published" {
		return fmt.Errorf("cannot delete published candidate")
	}
	return model.DB.Delete(&cand).Error
}

// ListCandidates returns incubation candidates with optional status filter.
func (s *IncubatorService) ListCandidates(status, keyword, severity string, page, pageSize int) ([]model.RuleIncubation, int64, error) {
	db := model.DB.Model(&model.RuleIncubation{})
	if status != "" {
		db = db.Where("status = ?", status)
	}
	if keyword != "" {
		db = db.Where("name LIKE ? OR code LIKE ?", "%"+keyword+"%", "%"+keyword+"%")
	}
	if severity != "" {
		db = db.Where("severity = ?", severity)
	}

	var total int64
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var list []model.RuleIncubation
	if err := db.Scopes(model.Paginate(page, pageSize)).Order("created_at DESC").Find(&list).Error; err != nil {
		return nil, 0, err
	}
	return list, total, nil
}

// PublishCandidate converts an incubation into a real ReviewRule.
func (s *IncubatorService) PublishCandidate(id uint, req PublishRequest, userID uint) (uint, error) {
	var cand model.RuleIncubation
	if err := model.DB.First(&cand, id).Error; err != nil {
		return 0, err
	}
	if cand.Status == "published" {
		return 0, fmt.Errorf("already published")
	}

	// Check code uniqueness
	var exists int64
	model.DB.Model(&model.ReviewRule{}).Where("code = ?", cand.Code).Count(&exists)
	if exists > 0 {
		return 0, fmt.Errorf("rule code %s already exists", cand.Code)
	}

	rule := model.ReviewRule{
		Code:        cand.Code,
		Name:        cand.Name,
		Category:    cand.Category,
		Severity:    cand.Severity,
		Language:    cand.Language,
		Description: cand.Description,
		Prompt:      cand.Prompt,
		IsBuiltIn:   false,
		IsEnabled:   req.IsEnabled,
	}

	if err := model.DB.Create(&rule).Error; err != nil {
		return 0, err
	}

	// Auto-create project configs (default disabled)
	autoCreateProjectReviewConfigs(rule.ID)

	// Update candidate
	now := time.Now()
	model.DB.Model(&cand).Updates(map[string]any{
		"status":            "published",
		"published_rule_id": rule.ID,
		"resolved_by":       userID,
		"resolved_at":       now,
	})

	// Asynchronously vectorize the published rule
	if s.embedSvc != nil && s.embedSvc.IsAvailable() {
		go func(r model.ReviewRule) {
			cfg := s.getConfig()
			if cfg.EmbeddingModelID == nil {
				return
			}
			key := vectorstore.Key{
				EntityType: "rule",
				EntityID:   r.ID,
				ModelID:    *cfg.EmbeddingModelID,
			}
			text := r.Name + " " + r.Description + " " + r.Prompt
			ctx := context.Background()
			if err := s.embedSvc.EmbedAndStore(ctx, key, text); err != nil {
				zap.L().Warn("embed rule failed", zap.Uint("rule_id", r.ID), zap.Error(err))
			}
		}(rule)
	}

	return rule.ID, nil
}

// PublishRequest is the input for publishing a candidate.
type PublishRequest struct {
	IsEnabled bool `json:"is_enabled"`
}

// ClusterIssues performs keyword-based clustering on unmatched issues and manages its own job record.
// Designed for synchronous API calls. For asynchronous job execution, use doClusterIssues + caller-managed job record.
func (s *IncubatorService) ClusterIssues(timeRangeDays int, languages []string, minGroupSize int) (*model.RuleIncubationJob, error) {
	since := time.Now().AddDate(0, 0, -timeRangeDays)
	paramsJSON, _ := json.Marshal(map[string]any{
		"time_range_days": timeRangeDays,
		"languages":       languages,
		"min_group_size":  minGroupSize,
	})
	job := &model.RuleIncubationJob{
		JobType:       "cluster",
		Status:        "running",
		TimeRangeFrom: &since,
		TimeRangeTo:   func() *time.Time { t := time.Now(); return &t }(),
		Params:        string(paramsJSON),
		ResultSummary: "{}",
		ErrorMsg:      "{}",
		CreatedBy:     0,
		CreatedAt:     time.Now(),
		StartedAt:     func() *time.Time { t := time.Now(); return &t }(),
	}
	if err := model.DB.Create(job).Error; err != nil {
		return nil, fmt.Errorf("create cluster job failed: %w", err)
	}

	summary, err := s.doClusterIssues(timeRangeDays, languages, minGroupSize, nil)
	now := time.Now()
	updates := map[string]any{"completed_at": &now}
	if err != nil {
		updates["status"] = "failed"
		updates["error_msg"] = err.Error()
	} else {
		updates["status"] = "success"
		updates["result_summary"] = summary
	}
	model.DB.Model(job).Updates(updates)
	return job, err
}

// doClusterIssues is the core clustering logic without job lifecycle management.
// Returns the JSON summary string.
func (s *IncubatorService) doClusterIssues(timeRangeDays int, languages []string, minGroupSize int, pipelineJobID *uint) (string, error) {
	cfg := s.getConfig()
	if timeRangeDays <= 0 {
		timeRangeDays = cfg.ClusterTimeWindowDays
	}
	if minGroupSize <= 0 {
		minGroupSize = cfg.ClusterMinGroupSize
	}

	since := time.Now().AddDate(0, 0, -timeRangeDays)
	var issues []model.ReviewIssue
	db := model.DB.Where("rule_id IS NULL AND created_at >= ?", since)
	if len(languages) > 0 {
		_ = languages
	}
	if err := db.Find(&issues).Error; err != nil {
		return "", err
	}

	// Asynchronously vectorize issues when embedding is available
	if s.embedSvc != nil && s.embedSvc.IsAvailable() {
		go func(issList []model.ReviewIssue) {
			cfg := s.getConfig()
			if cfg.EmbeddingModelID == nil {
				return
			}
			ctx := context.Background()
			for _, iss := range issList {
				key := vectorstore.Key{
					EntityType: "issue",
					EntityID:   iss.ID,
					ModelID:    *cfg.EmbeddingModelID,
				}
				text := iss.Message + " " + iss.Suggestion
				if err := s.embedSvc.EmbedAndStore(ctx, key, text); err != nil {
					zap.L().Warn("embed issue failed", zap.Uint("issue_id", iss.ID), zap.Error(err))
				}
			}
		}(issues)
	}

	clusters := s.keywordCluster(issues, minGroupSize)

	generated := 0
	for _, cl := range clusters {
		if generated >= cfg.ClusterMaxGroupsPerRun {
			break
		}
		_, err := s.CreateCandidate(CreateCandidateRequest{
			SourceIssueIDs: cl.IssueIDs,
			ClusterID:      cl.ID,
			AutoRefine:     true,
			PipelineJobID:  pipelineJobID,
		}, 0)
		if err == nil {
			generated++
		} else {
			zap.L().Warn("create candidate from cluster failed", zap.String("cluster_id", cl.ID), zap.Error(err))
		}
	}

	summary := map[string]any{
		"扫描Issue数": len(issues),
		"发现簇数":     len(clusters),
		"生成候选规则数":  generated,
	}
	summaryJSON, err := json.Marshal(summary)
	if err != nil {
		return "{}", nil
	}
	return string(summaryJSON), nil
}

// clusterResult holds a keyword cluster.
type clusterResult struct {
	ID       string
	IssueIDs []uint
	Keywords []string
	Category string
	Severity string
	Language string
}

func (s *IncubatorService) keywordCluster(issues []model.ReviewIssue, minSize int) []clusterResult {
	// Group by (category, language, severity) first
	type groupKey struct {
		Category string
		Language string
		Severity string
	}
	groups := make(map[groupKey][]model.ReviewIssue)
	for _, iss := range issues {
		k := groupKey{Category: iss.Category, Language: inferLanguage(iss.File), Severity: iss.Severity}
		groups[k] = append(groups[k], iss)
	}

	var results []clusterResult
	for gk, list := range groups {
		// Within each group, cluster by keyword overlap (Jaccard)
		clusters := s.splitByKeywordOverlap(list, 0.4)
		for i, cl := range clusters {
			if len(cl) < minSize {
				continue
			}
			ids := make([]uint, len(cl))
			for j, iss := range cl {
				ids[j] = iss.ID
			}
			results = append(results, clusterResult{
				ID:       fmt.Sprintf("%s-%s-%s-%d", gk.Category, gk.Language, gk.Severity, i),
				IssueIDs: ids,
				Keywords: extractKeywords(cl[0].Message),
				Category: gk.Category,
				Severity: gk.Severity,
				Language: gk.Language,
			})
		}
	}

	// Sort by issue count desc
	sort.Slice(results, func(i, j int) bool {
		return len(results[i].IssueIDs) > len(results[j].IssueIDs)
	})
	return results
}

func (s *IncubatorService) splitByKeywordOverlap(issues []model.ReviewIssue, threshold float64) [][]model.ReviewIssue {
	var clusters [][]model.ReviewIssue
	for _, iss := range issues {
		kw := extractKeywords(iss.Message)
		placed := false
		for ci := range clusters {
			if len(clusters[ci]) == 0 {
				continue
			}
			ckw := extractKeywords(clusters[ci][0].Message)
			if jaccard(kw, ckw) >= threshold {
				clusters[ci] = append(clusters[ci], iss)
				placed = true
				break
			}
		}
		if !placed {
			clusters = append(clusters, []model.ReviewIssue{iss})
		}
	}
	return clusters
}

// GetConfig returns the incubator configuration singleton.
func (s *IncubatorService) GetConfig() model.IncubatorConfig {
	return s.getConfig()
}

// SaveConfig updates the incubator configuration.
// It sanitizes and type-converts known fields to avoid GORM map-update type mismatches.
func (s *IncubatorService) SaveConfig(updates map[string]any) error {
	var cfg model.IncubatorConfig
	if err := model.DB.First(&cfg, 1).Error; err != nil {
		return err
	}

	clean := map[string]any{}
	if v, ok := updates["embedding_model_id"]; ok {
		switch val := v.(type) {
		case nil:
			clean["embedding_model_id"] = nil
		case float64:
			if val > 0 {
				uid := uint(val)
				clean["embedding_model_id"] = &uid
			} else {
				clean["embedding_model_id"] = nil
			}
		default:
			clean["embedding_model_id"] = v
		}
	}
	for _, key := range []string{"cluster_min_group_size", "cluster_max_groups_per_run", "cluster_time_window_days", "retro_match_max_days_lookback"} {
		if v, ok := updates[key]; ok {
			switch val := v.(type) {
			case float64:
				clean[key] = int(val)
			case int:
				clean[key] = val
			default:
				clean[key] = v
			}
		}
	}
	if v, ok := updates["retro_match_enabled"]; ok {
		clean["retro_match_enabled"] = v
	}
	if v, ok := updates["retro_match_confidence_threshold"]; ok {
		clean["retro_match_confidence_threshold"] = v
	}
	if v, ok := updates["health_check_enabled"]; ok {
		clean["health_check_enabled"] = v
	}
	if v, ok := updates["health_check_interval_days"]; ok {
		switch val := v.(type) {
		case float64:
			clean["health_check_interval_days"] = int(val)
		case int:
			clean["health_check_interval_days"] = val
		default:
			clean["health_check_interval_days"] = v
		}
	}
	if v, ok := updates["vector_store_type"]; ok {
		clean["vector_store_type"] = v
	}

	if len(clean) == 0 {
		return nil
	}
	return model.DB.Model(&cfg).Updates(clean).Error
}

// generateIncubationCode creates a semantic, readable code for a candidate rule.
// Format: incubated-{semantic-slug}-{4-char-random}
// generateIncubationCode creates a semantic, readable code for a candidate rule.
// Format: incubated-{semantic-slug}-{4-char-random}
// Requirements:
//   1. Only lowercase letters, digits, and hyphens allowed.
//   2. Compact: semantic-slug max 30 chars, total ~45 chars.
//   3. Accurate: semantic-slug describes the issueconcisely.
func generateIncubationCode(issues []model.ReviewIssue) string {
	randStr := generateRandomString(4)
	if len(issues) == 0 {
		return fmt.Sprintf("incubated-rule-%s", randStr)
	}
	msg := issues[0].Message
	if msg == "" {
		return fmt.Sprintf("incubated-rule-%s", randStr)
	}
	slug := extractSemanticSlug(msg)
	code := fmt.Sprintf("incubated-%s-%s", slug, randStr)
	if len(code) > 64 {
		code = code[:64]
	}
	return code
}

// extractSemanticSlug extracts the core semantic keywords from a message,
// filters stop-words, and joins them with hyphens.
// Result contains only a-z, 0-9, and hyphens.
func extractSemanticSlug(msg string) string {
	if msg == "" {
		return "rule"
	}
	msg = strings.ToLower(strings.TrimSpace(msg))

	// Comprehensive stop-words set (Chinese + English)
	stopwords := map[string]struct{}{
		// English
		"the": {}, "a": {}, "an": {}, "is": {}, "are": {}, "was": {}, "were": {},
		"to": {}, "of": {}, "in": {}, "on": {}, "at": {}, "for": {}, "with": {},
		"and": {}, "or": {}, "not": {}, "it": {}, "this": {}, "that": {}, "be": {},
		"by": {}, "from": {}, "as": {}, "has": {}, "have": {}, "had": {}, "do": {},
		"does": {}, "did": {}, "will": {}, "would": {}, "should": {}, "could": {},
		"can": {}, "may": {}, "might": {}, "must": {}, "shall": {},
		"i": {}, "you": {}, "he": {}, "she": {}, "we": {}, "they": {},
		"my": {}, "your": {}, "his": {}, "her": {}, "our": {}, "their": {},
		"me": {}, "him": {}, "them": {}, "us": {},
		// Chinese
		"建议": {}, "检查": {}, "需要": {}, "应该": {}, "可能": {}, "存在": {},
		"一个": {}, "进行": {}, "使用": {}, "没有": {}, "确保": {}, "避免": {},
		"注意": {}, "推荐": {}, "请": {}, "将": {}, "为": {}, "在": {},
		"了": {}, "和": {}, "与": {}, "或": {}, "对": {}, "从": {},
		"中": {}, "上": {}, "下": {}, "里": {}, "外": {}, "内": {},
	}

	// Split by whitespace and punctuation
	words := strings.FieldsFunc(msg, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == '\r' ||
			r == ',' || r == '.' || r == '，' || r == '。' ||
			r == ':' || r == ';' || r == '：' || r == '；' ||
			r == '(' || r == ')' || r == '（' || r == '）' ||
			r == '[' || r == ']' || r == '【' || r == '】' ||
			r == '!' || r == '！' || r == '?' || r == '？' ||
			r == '"' || r == '"' || r == '"' || r == '"' ||
			r == '<' || r == '>' || r == '《' || r == '》' ||
			r == '/' || r == '\\' || r == '|' || r == '_' ||
			r == '+' || r == '=' || r == '-' || r == '@' || r == '#' ||
			r == '$' || r == '%' || r == '^' || r == '&' || r == '*' ||
			r == '`' || r == '~'
	})

	// Filter valid keywords (>1 char, not stopword, only a-z0-9)
	var keywords []string
	for _, w := range words {
		w = strings.TrimSpace(w)
		if len(w) <= 1 {
			continue
		}
		if _, ok := stopwords[w]; ok {
			continue
		}
		// Only retain words made of a-z and 0-9
		clean := make([]rune, 0, len(w))
		valid := true
		for _, r := range w {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
				clean = append(clean, r)
			} else {
				valid = false
				break
			}
		}
		if valid && len(clean) > 1 {
			keywords = append(keywords, string(clean))
		}
	}

	if len(keywords) == 0 {
		return "rule"
	}

	// Take up to 5 keywords to keep it concise
	if len(keywords) > 5 {
		keywords = keywords[:5]
	}

	slug := strings.Join(keywords, "-")
	// Collapse consecutive hyphens
	re := regexp.MustCompile(`-+`)
	slug = re.ReplaceAllString(slug, "-")
	slug = strings.Trim(slug, "-")

	if len(slug) > 30 {
		slug = slug[:30]
		slug = strings.TrimRight(slug, "-")
	}

	if slug == "" {
		return "rule"
	}
	return slug
}

// sanitizeSlug normalizes an LLM-generated slug: lowercase ASCII letters, digits,
// and hyphens only; must start with a letter; trimmed to 40 chars; minimum 2 chars.
func sanitizeSlug(s string) string {
	if s == "" {
		return ""
	}
	s = strings.ToLower(strings.TrimSpace(s))
	// Normalize unicode hyphens/underscores to simple hyphens
	s = strings.NewReplacer(
		"_", "-",
		"–", "-",
		"—", "-",
	).Replace(s)
	var clean strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			clean.WriteRune(r)
		} else if r == '-' {
			clean.WriteRune(r)
		}
	}
	slug := clean.String()
	// Collapse consecutive hyphens
	re := regexp.MustCompile(`-+`)
	slug = re.ReplaceAllString(slug, "-")
	slug = strings.Trim(slug, "-")
	// Must start with a letter
	if slug == "" || !(slug[0] >= 'a' && slug[0] <= 'z') {
		return ""
	}
	// Length clamp
	if len(slug) > 40 {
		slug = slug[:40]
		slug = strings.TrimRight(slug, "-")
	}
	if len(slug) < 2 {
		return ""
	}
	return slug
}

// ValidateEmbedding checks whether the configured embedding model is reachable.
// If modelID > 0, tests that specific model directly (useful before saving config).
func (s *IncubatorService) ValidateEmbedding(modelID uint) (map[string]any, error) {
	var m model.LLMModel
	var err error

	if modelID > 0 {
		// Test a specific model selected by user (before saving config)
		err = model.DB.First(&m, modelID).Error
	} else {
		// Test the currently saved configuration
		cfg := s.getConfig()
		if cfg.EmbeddingModelID == nil {
			return nil, fmt.Errorf("embedding model not configured")
		}
		err = model.DB.First(&m, *cfg.EmbeddingModelID).Error
	}
	if err != nil {
		return nil, err
	}
	if m.ModelType != string(model.ModelTypeEmbedding) {
		return nil, fmt.Errorf("configured model is not an embedding model")
	}

	start := time.Now()
	_, tokens, err := s.embedSvc.Embed(context.Background(), "test validation")
	latency := time.Since(start).Milliseconds()

	if err != nil {
		return nil, err
	}
	return map[string]any{
		"available":   true,
		"model_id":    m.ID,
		"model_name":  m.ModelID,
		"latency_ms":  latency,
		"tokens_used": tokens,
	}, nil
}

// --- helpers ---

func (s *IncubatorService) getConfig() model.IncubatorConfig {
	var cfg model.IncubatorConfig
	if err := model.DB.First(&cfg, 1).Error; err != nil {
		zap.L().Warn("incubator config not found, using defaults", zap.Error(err))
	}
	return cfg
}

func modeKey(m map[string]int) string {
	var maxK string
	var maxV int
	for k, v := range m {
		if v > maxV {
			maxV = v
			maxK = k
		}
	}
	return maxK
}

func (s *IncubatorService) suggestName(issues []model.ReviewIssue) string {
	if len(issues) == 0 {
		return "未命名规则"
	}
	msg := issues[0].Message
	if utf8.RuneCountInString(msg) > 30 {
		rs := []rune(msg)
		msg = string(rs[:30])
	}
	return msg
}

func (s *IncubatorService) suggestDescription(issues []model.ReviewIssue) string {
	if len(issues) == 0 {
		return ""
	}
	// Simple aggregation
	msgs := make([]string, 0, min(len(issues), 3))
	for i, iss := range issues {
		if i >= 3 {
			break
		}
		m := iss.Message
		if utf8.RuneCountInString(m) > 100 {
			rs := []rune(m)
			m = string(rs[:100]) + "..."
		}
		msgs = append(msgs, m)
	}
	return strings.Join(msgs, "\n")
}

func extractKeywords(text string) []string {
	// Very basic keyword extraction for MVP
	words := strings.FieldsFunc(text, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == ',' || r == '.' || r == ':' || r == ';' || r == '(' || r == ')'
	})
	stopwords := map[string]struct{}{
		"the": {}, "a": {}, "an": {}, "is": {}, "are": {}, "was": {}, "were": {},
		"to": {}, "of": {}, "in": {}, "on": {}, "at": {}, "for": {}, "with": {},
		"and": {}, "or": {}, "not": {}, "it": {}, "this": {}, "that": {},
		"建议": {}, "检查": {}, "需要": {}, "应该": {}, "可能": {}, "存在": {},
		"一个": {}, "进行": {}, "使用": {}, "没有": {},
	}
	var res []string
	seen := make(map[string]struct{})
	for _, w := range words {
		w = strings.ToLower(strings.TrimSpace(w))
		if w == "" || len(w) < 2 {
			continue
		}
		if _, ok := stopwords[w]; ok {
			continue
		}
		if _, ok := seen[w]; ok {
			continue
		}
		seen[w] = struct{}{}
		res = append(res, w)
	}
	return res
}

func jaccard(a, b []string) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	setA := make(map[string]struct{}, len(a))
	for _, x := range a {
		setA[x] = struct{}{}
	}
	inter := 0
	for _, x := range b {
		if _, ok := setA[x]; ok {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

// extractJSON extracts the outermost JSON object from a text, handling markdown code blocks.
func extractJSON(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		lines := strings.Split(s, "\n")
		if len(lines) > 2 {
			s = strings.Join(lines[1:len(lines)-1], "\n")
		}
	}
	s = strings.TrimSpace(s)
	start := strings.Index(s, "{")
	if start < 0 {
		return s
	}
	depth := 0
	inString := false
	escape := false
	end := -1
	for i := start; i < len(s); i++ {
		c := s[i]
		if escape {
			escape = false
			continue
		}
		if c == '\\' {
			escape = true
			continue
		}
		if c == '"' {
			inString = !inString
			continue
		}
		if !inString {
			switch c {
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					end = i
					break
				}
			}
		}
		if end >= 0 {
			break
		}
	}
	if end < 0 {
		end = strings.LastIndex(s, "}")
		if end <= start {
			return s
		}
	}
	return s[start : end+1]
}

// tryFixInnerQuotes attempts to fix unescaped inner double-quotes inside JSON string values.
// This is a best-effort heuristic for LLM-generated JSON that contains code examples with
// unescaped quotes like: {"key": "example: `MapClaims{"a":1}`"}
func tryFixInnerQuotes(raw string) string {
	inString := false
	escape := false
	result := make([]rune, 0, len(raw))
	for _, c := range raw {
		if escape {
			result = append(result, c)
			escape = false
			continue
		}
		if c == '\\' {
			result = append(result, c)
			escape = true
			continue
		}
		if c == '"' {
			// If we are already inside a string, this might be an unescaped inner quote
			// that should have been \".  We count whether it makes sense structurally.
			if inString {
				// Heuristic: if the next non-space character looks like a JSON control character
				// or end-of-object, treat this quote as a string terminator.
				// Otherwise escape it.
				if looksLikeJSONControlAfter(raw, len(result)) {
					inString = false
					result = append(result, c)
				} else {
					result = append(result, '\\', c)
				}
			} else {
				inString = true
				result = append(result, c)
			}
			continue
		}
		result = append(result, c)
	}
	return string(result)
}

func looksLikeJSONControlAfter(s string, pos int) bool {
	for i := pos; i < len(s); i++ {
		switch s[i] {
		case ' ', '\t', '\n', '\r':
			continue
		case ',', '}', ']':
			return true
		default:
			return false
		}
	}
	return true
}

// runRefine uses LLM to generate rule name, description and prompt from source issues.
func (s *IncubatorService) runRefine(incubationID uint) error {
	_, issues, err := s.GetCandidate(incubationID)
	if err != nil {
		return err
	}
	if len(issues) == 0 {
		return fmt.Errorf("no source issues")
	}

	// Build issue samples for LLM
	var samples []string
	for i, iss := range issues {
		if i >= 5 {
			break
		}
		samples = append(samples, fmt.Sprintf("Issue %d:\nFile: %s\nMessage: %s\nSuggestion: %s",
			i+1, iss.File, iss.Message, iss.Suggestion))
	}
	sampleText := strings.Join(samples, "\n\n")

	llmSvc := NewLLMService()
	systemPrompt := "You are a code-review rule engineer. Your task is to analyze a set of code issues and abstract them into a general review rule. You must output valid JSON. All double-quote characters inside string values must be properly escaped with backslash. Use single quotes in code examples within string values to simplify escaping."
	userPrompt := fmt.Sprintf(`Analyze the following code-review issues and output a structured review rule.

Issues:
%s

Please output in the following JSON format (do not include markdown code block):
{
  "name": "A concise Chinese rule name (within 20 characters)",
  "slug": "A kebab-case English slug that accurately describes the rule issue. Use 3-5 lowercase English words or common abbreviations (e.g., 'nil-pointer-deref', 'unused-import', 'sql-injection'). Only lowercase letters, digits, and hyphens are allowed. Must start with a letter. Length: 10-40 characters.",
  "description": "A 1-2 sentence description of what this rule checks, why it matters, and how to fix it (in Chinese)",
  "prompt": "In Chinese. ONLY describe: 1) what to check (check objectives), 2) judgment criteria, 3) examples of violation vs non-violation. DO NOT include role-setting sentences like 'you are a code review assistant...' or output constraints like 'do not output extra explanation...'. IMPORTANT: when providing code examples inside the prompt field, use single quotes for inner strings to avoid JSON escaping issues, e.g. use {'user_id': 1} instead of {\"user_id\": 1}. Keep it concise and focused on the review logic itself."
}
`, sampleText)

	resp, err := llmSvc.ChatCompletion(nil, 0, "rule_refine", systemPrompt, userPrompt)
	if err != nil {
		return fmt.Errorf("llm refine failed: %w", err)
	}

	// Parse JSON from LLM response
	var refineResult struct {
		Name        string `json:"name"`
		Slug        string `json:"slug"`
		Description string `json:"description"`
		Prompt      string `json:"prompt"`
	}
	content := extractJSON(resp.Content)
	if err := json.Unmarshal([]byte(content), &refineResult); err != nil {
		// Try sanitized version: replace unescaped inner double quotes that commonly break LLM JSON
		// The LLM sometimes generates examples like: `MapClaims{"k": "v"}` where inner quotes are NOT escaped
		sanitized := tryFixInnerQuotes(content)
		if json.Unmarshal([]byte(sanitized), &refineResult) == nil {
			zap.L().Info("refine JSON parsed after sanitization", zap.Uint("incubation_id", incubationID))
		} else {
			zap.L().Error("parse refine result failed",
				zap.Uint("incubation_id", incubationID),
				zap.String("raw", resp.Content),
				zap.String("extracted", content),
				zap.String("error", err.Error()))
			return fmt.Errorf("parse refine result failed: %w", err)
		}
	}

	// Update candidate
	updates := map[string]any{}
	if refineResult.Name != "" {
		updates["name"] = refineResult.Name
	}
	if refineResult.Description != "" {
		updates["description"] = refineResult.Description
	}
	if refineResult.Prompt != "" {
		updates["prompt"] = refineResult.Prompt
	}
	// Use LLM-generated slug for the rule code; fallback to heuristic if slug is missing/invalid.
	slug := sanitizeSlug(refineResult.Slug)
	if slug == "" {
		cand, _, _ := s.GetCandidate(incubationID)
		if cand != nil && cand.Prompt != "" {
			slug = extractSemanticSlug(cand.Prompt)
		} else if len(issues) > 0 {
			slug = extractSemanticSlug(issues[0].Message)
		} else {
			slug = "rule"
		}
	}
	randStr := generateRandomString(4)
	newCode := fmt.Sprintf("incubated-%s-%s", slug, randStr)
	if len(newCode) > 64 {
		newCode = newCode[:64]
		newCode = strings.TrimRight(newCode, "-")
	}
	updates["code"] = newCode
	updates["status"] = "ready"
	if len(updates) > 0 {
		if err := s.UpdateCandidate(incubationID, updates); err != nil {
			return err
		}
	}
	zap.L().Info("refine: candidate updated",
		zap.Uint("incubation_id", incubationID),
		zap.String("code", newCode),
		zap.String("slug", slug))

	// Asynchronously vectorize the refined candidate
	if s.embedSvc != nil && s.embedSvc.IsAvailable() {
		go func(id uint) {
			cfg := s.getConfig()
			if cfg.EmbeddingModelID == nil {
				return
			}
			var cand model.RuleIncubation
			if err := model.DB.First(&cand, id).Error; err != nil {
				return
			}
			key := vectorstore.Key{
				EntityType: "incubation",
				EntityID:   cand.ID,
				ModelID:    *cfg.EmbeddingModelID,
			}
			text := cand.Name + " " + cand.Description + " " + cand.Prompt
			ctx := context.Background()
			if err := s.embedSvc.EmbedAndStore(ctx, key, text); err != nil {
				zap.L().Warn("embed incubation failed", zap.Uint("incubation_id", cand.ID), zap.Error(err))
			}
		}(incubationID)
	}

	// Recalculate confidence score after refine succeeded
	if cd, _, err := s.GetCandidate(incubationID); err == nil {
		conf := s.calculateConfidenceScore(cd)
		_ = s.UpdateCandidate(incubationID, map[string]any{
			"confidence_score": conf,
		})
		zap.L().Info("confidence updated after refine",
			zap.Uint("incubation_id", incubationID),
			zap.Float64("confidence", conf))
	}

	return nil
}

// runSimilarCheck detects similarity between the candidate and existing rules.
// Returns true if the candidate passes originality check (no similar rules found).
func (s *IncubatorService) runSimilarCheck(incubationID uint) (bool, error) {
	cand, _, err := s.GetCandidate(incubationID)
	if err != nil {
		zap.L().Warn("similarCheck: get candidate failed", zap.Uint("incubation_id", incubationID), zap.Error(err))
		return false, err
	}

	var rules []model.ReviewRule
	if err := model.DB.Where("is_enabled = ?", true).Find(&rules).Error; err != nil {
		zap.L().Warn("similarCheck: query rules failed", zap.Uint("incubation_id", incubationID), zap.Error(err))
		return false, err
	}

	var similar []map[string]any
	candKeywords := extractKeywords(cand.Name + " " + cand.Description + " " + cand.Prompt)
	for _, r := range rules {
		ruleKeywords := extractKeywords(r.Name + " " + r.Description + " " + r.Prompt)
		score := jaccard(candKeywords, ruleKeywords)
		if score >= 0.5 {
			similar = append(similar, map[string]any{
				"rule_id":    r.ID,
				"name":       r.Name,
				"similarity": score,
			})
		}
	}

	similarJSON, err := json.Marshal(similar)
	if err != nil {
		return false, err
	}

	// Also check code uniqueness
	var codeExists int64
	model.DB.Model(&model.ReviewRule{}).Where("code = ?", cand.Code).Count(&codeExists)
	updates := map[string]any{
		"similar_rules": string(similarJSON),
	}
	if codeExists > 0 {
		updates["code"] = fmt.Sprintf("%s-%d", cand.Code, time.Now().Unix())
		zap.L().Info("similarCheck: code collision, renamed",
			zap.Uint("incubation_id", incubationID), zap.String("new_code", updates["code"].(string)))
	}

	if err := s.UpdateCandidate(incubationID, updates); err != nil {
		zap.L().Warn("similarCheck: update candidate failed",
			zap.Uint("incubation_id", incubationID), zap.Error(err))
		return false, err
	}

	passed := len(similar) == 0
	zap.L().Info("similarCheck: completed",
		zap.Uint("incubation_id", incubationID),
		zap.Int("rules_checked", len(rules)),
		zap.Int("similar_found", len(similar)),
		zap.Bool("passed", passed))

	// Recalculate confidence after similar-check
	if updatedCand, _, err := s.GetCandidate(incubationID); err == nil {
		conf := s.calculateConfidenceScore(updatedCand)
		if err := s.UpdateCandidate(incubationID, map[string]any{
			"confidence_score": conf,
		}); err == nil {
			zap.L().Info("confidence updated after similar-check",
				zap.Uint("incubation_id", incubationID),
				zap.Float64("confidence", conf))
		}
	}

	return passed, nil
}

// runSandboxTest uses LLM to verify the candidate rule by generating test code and validating it.
// Returns true if the candidate passes the test (accuracy >= 0.5).
func (s *IncubatorService) runSandboxTest(incubationID uint) (bool, error) {
	cand, _, err := s.GetCandidate(incubationID)
	if err != nil {
		zap.L().Warn("sandboxTest: get candidate failed", zap.Uint("incubation_id", incubationID), zap.Error(err))
		return false, err
	}
	if cand.Prompt == "" {
		return false, fmt.Errorf("prompt is empty, cannot test")
	}

	// Step 1: Ask LLM to generate positive and negative test code
	cases, err := s.generateLLMTestCases(cand)
	if err != nil {
		zap.L().Warn("sandboxTest: generate test cases failed",
			zap.Uint("incubation_id", incubationID), zap.Error(err))
		return false, err
	}
	if len(cases) == 0 {
		return false, fmt.Errorf("no test cases generated")
	}

	zap.L().Info("sandboxTest: test cases generated",
		zap.Uint("incubation_id", incubationID),
		zap.Int("total_cases", len(cases)))

	llmSvc := NewLLMService()
	passed := 0
	for i := range cases {
		systemPrompt := "You are a strict code review assistant. Answer only 'YES' if the given code violates the described rule, or 'NO' if it does not. Be concise."
		userPrompt := fmt.Sprintf("Rule description:\n%s\n\nCode to review:\n```\n%s\n```\n\nDoes this code violate the rule? Answer YES or NO only.",
			cand.Prompt, cases[i].Code)

		resp, err := llmSvc.ChatCompletion(nil, 0, "sandbox_test", systemPrompt, userPrompt)
		if err != nil {
			cases[i].Pass = false
			cases[i].Reason = "LLM call failed: " + err.Error()
			continue
		}

		answer := strings.ToUpper(strings.TrimSpace(resp.Content))
		actual := "unknown"
		if idxYes := strings.Index(answer, "YES"); idxYes >= 0 {
			actual = "yes"
			if idxNo := strings.Index(answer, "NO"); idxNo >= 0 && idxNo < idxYes {
				actual = "no"
			}
		} else if idxNo := strings.Index(answer, "NO"); idxNo >= 0 {
			actual = "no"
		}
		switch actual {
		case "yes":
			cases[i].Actual = true
		case "no":
			cases[i].Actual = false
		default:
			cases[i].Actual = !cases[i].Expected
			cases[i].Reason = "LLM answer unclear"
			continue
		}
		cases[i].Pass = cases[i].Actual == cases[i].Expected
		if cases[i].Pass {
			passed++
		} else {
			cases[i].Reason = fmt.Sprintf("Expected %v, got %v", cases[i].Expected, cases[i].Actual)
		}
	}

	accuracy := 0.0
	if len(cases) > 0 {
		accuracy = float64(passed) / float64(len(cases))
	}

	testResult := map[string]any{
		"total_cases": len(cases),
		"passed":      passed,
		"accuracy":    accuracy,
		"details":     cases,
	}
	resultJSON, _ := json.Marshal(testResult)

	if err := model.DB.Model(&model.RuleIncubation{}).Where("id = ?", incubationID).Update("test_results", string(resultJSON)).Error; err != nil {
		zap.L().Warn("sandboxTest: save results failed",
			zap.Uint("incubation_id", incubationID), zap.Error(err))
		return false, err
	}

	passedOverall := accuracy >= 0.5
	zap.L().Info("sandboxTest: completed",
		zap.Uint("incubation_id", incubationID),
		zap.Int("total_cases", len(cases)),
		zap.Int("passed", passed),
		zap.Float64("accuracy", accuracy),
		zap.Bool("passed_overall", passedOverall))

	// Recalculate confidence after sandbox test
	if updatedCand, _, err := s.GetCandidate(incubationID); err == nil {
		conf := s.calculateConfidenceScore(updatedCand)
		if err := s.UpdateCandidate(incubationID, map[string]any{
			"confidence_score": conf,
		}); err == nil {
			zap.L().Info("confidence updated after sandbox-test",
				zap.Uint("incubation_id", incubationID),
				zap.Float64("confidence", conf))
		}
	}

	return passedOverall, nil
}

// generateLLMTestCases asks the LLM to generate positive (violating) and negative (compliant) code snippets.
func (s *IncubatorService) generateLLMTestCases(cand *model.RuleIncubation) ([]testCase, error) {
	type tc struct {
		No       int    `json:"no"`
		Type     string `json:"type"`
		Code     string `json:"code"`
		File     string `json:"file"`
		Expected bool   `json:"expected"`
		Actual   bool   `json:"actual"`
		Pass     bool   `json:"pass"`
		Reason   string `json:"reason"`
	}

	systemPrompt := `You are a code review test-case generator. Based on the given review rule, generate realistic, concise test code snippets.

Output ONLY in this exact plain-text format (do not use markdown code blocks):

POSITIVE_CASES:
===
<code snippet 1>
===
<code snippet 2>
===

NEGATIVE_CASES:
===
<code snippet 1>
===
<code snippet 2>
===

Requirements:
- Generate 2 POSITIVE cases: code that VIOLATES the described review rule.
- Generate 2 NEGATIVE cases: clean code that does NOT violate the review rule.
- Use the programming language context implied by the rule (e.g., Go, Java, Python).
- Each snippet should be short, realistic, and self-contained.
- Do NOT add explanations outside the format.`

	userPrompt := fmt.Sprintf("Review rule:\n%s\n\nGenerate test code snippets.", cand.Prompt)

	llmSvc := NewLLMService()
	resp, err := llmSvc.ChatCompletion(nil, 0, "sandbox_test", systemPrompt, userPrompt)
	if err != nil {
		return nil, fmt.Errorf("llm generate test cases failed: %w", err)
	}

	content := resp.Content
	var cases []tc

	// Parse positive cases
	posStart := strings.Index(content, "POSITIVE_CASES:")
	negStart := strings.Index(content, "NEGATIVE_CASES:")
	if posStart < 0 {
		return nil, fmt.Errorf("no POSITIVE_CASES section in LLM output")
	}
	if negStart < 0 {
		negStart = len(content)
	}

	posSection := content[posStart:negStart]
	negSection := ""
	if negStart < len(content) {
		negSection = content[negStart:]
	}

	// Split by "===" and extract non-empty snippets
	parseSnippets := func(section string) []string {
		parts := strings.Split(section, "===")
		var snippets []string
		for i, p := range parts {
			if i == 0 {
				continue // skip header line
			}
			s := strings.TrimSpace(p)
			if s != "" {
				snippets = append(snippets, s)
			}
		}
		return snippets
	}

	posSnippets := parseSnippets(posSection)
	negSnippets := parseSnippets(negSection)

	if len(posSnippets) == 0 && len(negSnippets) == 0 {
		return nil, fmt.Errorf("no test cases parsed from LLM output")
	}

	idx := 1
	for _, snip := range posSnippets {
		cases = append(cases, tc{
			No:       idx,
			Type:     "positive",
			Code:     snip,
			File:     cand.Language,
			Expected: true,
		})
		idx++
	}
	for _, snip := range negSnippets {
		cases = append(cases, tc{
			No:       idx,
			Type:     "negative",
			Code:     snip,
			File:     cand.Language,
			Expected: false,
		})
		idx++
	}

	// Convert to the public testCase type
	var result []testCase
	for _, c := range cases {
		result = append(result, testCase(c))
	}
	return result, nil
}

type testCase struct {
	No       int    `json:"no"`
	Type     string `json:"type"` // positive / negative
	Code     string `json:"code"`
	File     string `json:"file"`
	Expected bool   `json:"expected"`
	Actual   bool   `json:"actual"`
	Pass     bool   `json:"pass"`
	Reason   string `json:"reason"`
}

// runRetroMatch performs retroactive matching for a published rule against historical issues.
func (s *IncubatorService) runRetroMatch(ruleID uint, lookbackDays int, threshold float64) error {
	var rule model.ReviewRule
	if err := model.DB.First(&rule, ruleID).Error; err != nil {
		return err
	}
	if lookbackDays <= 0 {
		lookbackDays = s.getConfig().RetroMatchMaxDaysLookback
	}
	if threshold <= 0 {
		threshold = s.getConfig().RetroMatchConfidenceThreshold
	}

	since := time.Now().AddDate(0, 0, -lookbackDays)
	var issues []model.ReviewIssue
	if err := model.DB.Where("rule_id IS NULL AND created_at >= ?", since).Find(&issues).Error; err != nil {
		return err
	}

	// Level 1: structural filter
	var candidates []model.ReviewIssue
	for _, iss := range issues {
		if inferLanguage(iss.File) != rule.Language && rule.Language != "common" {
			continue
		}
		if iss.Category != rule.Category {
			continue
		}
		candidates = append(candidates, iss)
	}

	// Level 2: keyword pre-match
	var filtered []model.ReviewIssue
	ruleKeywords := extractKeywords(rule.Prompt + " " + rule.Name)
	for _, iss := range candidates {
		issKeywords := extractKeywords(iss.Message + " " + iss.Suggestion)
		if jaccard(ruleKeywords, issKeywords) < 0.3 { // too different
			continue
		}
		filtered = append(filtered, iss)
	}

	// Build matches (without Level 3 embedding for MVP, or if available use it)
	matched := 0
	for _, iss := range filtered {
		confidence := 0.7 // baseline for passing L1+L2
		if s.embedSvc.IsAvailable() {
			// Level 3: if embedding available, do semantic comparison
			// In MVP we skip complex semantic comparison and rely on L1+L2 + a moderate threshold
			confidence = 0.75
		}
		if confidence < threshold {
			continue
		}

		exists := int64(0)
		model.DB.Model(&model.ReviewIssueRuleMatch{}).Where("issue_id = ? AND rule_id = ?", iss.ID, ruleID).Count(&exists)
		if exists > 0 {
			continue
		}

		match := model.ReviewIssueRuleMatch{
			IssueID:    iss.ID,
			RuleID:     ruleID,
			MatchType:  "retroactive",
			MatchedAt:  time.Now(),
			Confidence: confidence,
		}
		if err := model.DB.Create(&match).Error; err != nil {
			zap.L().Warn("create retro match failed",
				zap.Uint("issue_id", iss.ID),
				zap.Uint("rule_id", ruleID),
				zap.Error(err))
			continue
		}
		matched++
	}

	zap.L().Info("retro match completed",
		zap.Uint("rule_id", ruleID),
		zap.Int("matched", matched),
		zap.Int("candidates", len(filtered)))
	return nil
}

// runHealthCheck evaluates the health of all published rules.
func (s *IncubatorService) runHealthCheck() error {
	cfg := s.getConfig()
	if !cfg.HealthCheckEnabled {
		return nil
	}

	var rules []model.ReviewRule
	if err := model.DB.Where("is_enabled = ?", true).Find(&rules).Error; err != nil {
		return err
	}

	for _, rule := range rules {
		// Calculate recent hit rate (past 7 days)
		sevenDaysAgo := time.Now().AddDate(0, 0, -7)
		var hitCount int64
		model.DB.Model(&model.ReviewIssue{}).Where("rule_id = ? AND created_at >= ?", rule.ID, sevenDaysAgo).Count(&hitCount)

		// Calculate total unmatched issues in the same category/language in past 7 days
		var missedCount int64
		missedDB := model.DB.Model(&model.ReviewIssue{}).
			Where("rule_id IS NULL AND created_at >= ? AND category = ?", sevenDaysAgo, rule.Category)
		if rule.Language != "common" {
			// approximate by file extension; exact language matching needs a language column
		}
		missedDB.Count(&missedCount)

		// Get reject rate
		var totalIssues, rejectedIssues int64
		model.DB.Model(&model.ReviewIssue{}).Where("rule_id = ?", rule.ID).Count(&totalIssues)
		model.DB.Model(&model.ReviewIssue{}).Where("rule_id = ? AND status = ?", rule.ID, "rejected").Count(&rejectedIssues)

		rejectRate := float64(0)
		if totalIssues > 0 {
			rejectRate = float64(rejectedIssues) / float64(totalIssues)
		}

		// For now we only log; later we can persist health snapshots or alert
		alertLevel := "healthy"
		if rejectRate > 0.15 || missedCount > 20 {
			alertLevel = "warning"
		}
		if rejectRate > 0.25 || missedCount > 50 {
			alertLevel = "critical"
		}

		if alertLevel != "healthy" {
			zap.L().Info("rule health check",
				zap.Uint("rule_id", rule.ID),
				zap.String("rule_code", rule.Code),
				zap.String("alert_level", alertLevel),
				zap.Int64("hit_7d", hitCount),
				zap.Int64("missed_7d", missedCount),
				zap.Float64("reject_rate", rejectRate))
		}
	}
	return nil
}

func inferLanguage(file string) string {
	f := strings.ToLower(file)
	switch {
	case strings.HasSuffix(f, ".go"):
		return "golang"
	case strings.HasSuffix(f, ".py"):
		return "python"
	case strings.HasSuffix(f, ".java"):
		return "java"
	case strings.HasSuffix(f, ".js"), strings.HasSuffix(f, ".ts"),
		strings.HasSuffix(f, ".vue"), strings.HasSuffix(f, ".jsx"), strings.HasSuffix(f, ".tsx"):
		return "frontend"
	default:
		return "common"
	}
}

// autoCreateProjectReviewConfigs 为所有已有项目自动插入新规则的默认配置（默认禁用）
func autoCreateProjectReviewConfigs(ruleID uint) {
	var projects []model.Project
	if err := model.DB.Find(&projects).Error; err != nil {
		zap.L().Warn("auto create project review configs: find projects failed", zap.Error(err))
		return
	}
	for _, p := range projects {
		var count int64
		model.DB.Model(&model.ProjectReviewConfig{}).
			Where("project_id = ? AND rule_id = ?", p.ID, ruleID).
			Count(&count)
		if count > 0 {
			continue
		}
		cfg := model.ProjectReviewConfig{
			ProjectID: p.ID,
			RuleID:    ruleID,
			IsEnabled: false,
			Severity:  "",
		}
		if err := model.DB.Create(&cfg).Error; err != nil {
			zap.L().Warn("auto create project review config failed",
				zap.Uint("project_id", p.ID), zap.Uint("rule_id", ruleID), zap.Error(err))
		}
	}
}

// ---------- Pipeline Orchestration ----------

// ErrPipelineRunning is returned when another pipeline_run is already in progress.
type ErrPipelineRunning struct {
	JobID uint
}

func (e *ErrPipelineRunning) Error() string {
	return fmt.Sprintf("已有流水线正在执行 (Job #%d)", e.JobID)
}

// RunPipeline kicks off a full pipeline: cluster → refine → similar_check → sandbox_test.
// It creates a pipeline_run job and executes steps asynchronously in a goroutine.
func (s *IncubatorService) RunPipeline(timeRangeDays, minGroupSize int) (*model.RuleIncubationJob, error) {
	// Check mutex: only one pipeline_run can be active at a time.
	var existing model.RuleIncubationJob
	if err := model.DB.Where("job_type = ? AND status = ?", "pipeline_run", "running").
		Order("created_at DESC").First(&existing).Error; err == nil {
		return nil, &ErrPipelineRunning{JobID: existing.ID}
	}

	paramsJSON, _ := json.Marshal(map[string]any{
		"time_range_days": timeRangeDays,
		"min_group_size":  minGroupSize,
	})
	job := &model.RuleIncubationJob{
		JobType:       "pipeline_run",
		Status:        "running",
		Params:        string(paramsJSON),
		ResultSummary: `{"pipeline_steps":{"cluster":{"status":"running"},"refine":{"status":"idle"},"similar_check":{"status":"idle"},"sandbox_test":{"status":"idle"}}}`,
		ErrorMsg:      "{}",
		CreatedAt:     time.Now(),
		StartedAt:     func() *time.Time { t := time.Now(); return &t }(),
	}
	if err := model.DB.Create(job).Error; err != nil {
		return nil, fmt.Errorf("create pipeline job failed: %w", err)
	}
	go s.runPipelineSteps(job.ID, timeRangeDays, minGroupSize)
	return job, nil
}

func (s *IncubatorService) runPipelineSteps(jobID uint, timeRangeDays, minGroupSize int) {
	defer func() {
		if r := recover(); r != nil {
			zap.L().Error("pipeline run panicked, recovering",
				zap.Uint("job_id", jobID),
				zap.Any("panic", r))
			now := time.Now()
			model.DB.Model(&model.RuleIncubationJob{}).Where("id = ?", jobID).Updates(map[string]any{
				"status":       "failed",
				"error_msg":    fmt.Sprintf("pipeline panicked: %v", r),
				"completed_at": &now,
			})
			if s.SSEHub != nil {
				b, _ := json.Marshal(map[string]any{
					"job_id": jobID, "step_id": "", "status": "",
					"pipeline_status": "failed", "updated_at": time.Now().Format(time.RFC3339),
				})
				s.SSEHub.Broadcast(int64(jobID), string(b))
			}
		}
	}()

	broadcast := func(stepID, status, overall string, current, total int) {
		if s.SSEHub == nil {
			return
		}
		payload := map[string]any{
			"job_id":          jobID,
			"step_id":         stepID,
			"status":          status,
			"pipeline_status": overall,
			"updated_at":      time.Now().Format(time.RFC3339),
		}
		if total > 0 {
			payload["current"] = current
			payload["total"] = total
		}
		b, _ := json.Marshal(payload)
		s.SSEHub.Broadcast(int64(jobID), string(b))
	}

	updateStep := func(stepID, status string, current, total int) {
		var job model.RuleIncubationJob
		if err := model.DB.First(&job, jobID).Error; err != nil {
			return
		}
		var summary map[string]any
		_ = json.Unmarshal([]byte(job.ResultSummary), &summary)
		if summary == nil {
			summary = map[string]any{}
		}
		steps, _ := summary["pipeline_steps"].(map[string]any)
		if steps == nil {
			steps = map[string]any{}
		}
		stepInfo := map[string]any{
			"status":     status,
			"updated_at": time.Now().Format(time.RFC3339),
		}
		if total > 0 {
			stepInfo["current"] = current
			stepInfo["total"] = total
		}
		steps[stepID] = stepInfo
		summary["pipeline_steps"] = steps
		b, _ := json.Marshal(summary)
		model.DB.Model(&job).Update("result_summary", string(b))
		broadcast(stepID, status, job.Status, current, total)
	}

	failJob := func(err error) {
		now := time.Now()
		model.DB.Model(&model.RuleIncubationJob{}).Where("id = ?", jobID).Updates(map[string]any{
			"status":       "failed",
			"error_msg":    err.Error(),
			"completed_at": &now,
		})
		broadcast("", "", "failed", 0, 0)
	}

	completeJob := func() {
		now := time.Now()
		model.DB.Model(&model.RuleIncubationJob{}).Where("id = ?", jobID).Updates(map[string]any{
			"status":       "success",
			"completed_at": &now,
		})
		broadcast("", "", "success", 0, 0)
	}

	// -------- Step 1: Cluster --------
	// 初始状态已在创建 Job 时设为 running，这里直接进入执行
	summaryJSON, err := s.doClusterIssues(timeRangeDays, nil, minGroupSize, &jobID)
	if err != nil {
		failJob(fmt.Errorf("cluster failed: %w", err))
		return
	}
	updateStep("cluster", "completed", 0, 0)

	// Parse cluster result
	var clusterSummary map[string]any
	_ = json.Unmarshal([]byte(summaryJSON), &clusterSummary)
	generated := 0
	if v, ok := clusterSummary["生成候选规则数"]; ok {
		switch val := v.(type) {
		case float64:
			generated = int(val)
		case int:
			generated = val
		}
	}
	if generated == 0 {
		zap.L().Info("pipeline_run: no candidates generated, finishing after cluster", zap.Uint("job_id", jobID))
		completeJob()
		return
	}

	// -------- Step 2: Refine --------
	var pipeCands []model.RuleIncubation
	model.DB.Where("pipeline_job_id = ?", jobID).Find(&pipeCands)
	updateStep("refine", "running", 0, len(pipeCands))
	for i, cand := range pipeCands {
		if cand.Status == "draft" {
			_ = s.runRefine(cand.ID)
		}
		updateStep("refine", "running", i+1, len(pipeCands))
	}
	updateStep("refine", "completed", len(pipeCands), len(pipeCands))

	// -------- Step 3: Similar Check --------
	updateStep("similar_check", "running", 0, len(pipeCands))
	similarPassed := 0
	for i, cand := range pipeCands {
		if ok, _ := s.runSimilarCheck(cand.ID); ok {
			similarPassed++
		}
		updateStep("similar_check", "running", i+1, len(pipeCands))
	}
	updateStep("similar_check", "completed", similarPassed, len(pipeCands))

	// -------- Step 4: Sandbox Test --------
	updateStep("sandbox_test", "running", 0, len(pipeCands))
	sandboxPassed := 0
	for i, cand := range pipeCands {
		if cand.Status == "ready" {
			if ok, _ := s.runSandboxTest(cand.ID); ok {
				sandboxPassed++
			}
		}
		updateStep("sandbox_test", "running", i+1, len(pipeCands))
	}
	updateStep("sandbox_test", "completed", sandboxPassed, len(pipeCands))

	completeJob()
	zap.L().Info("pipeline_run completed", zap.Uint("job_id", jobID))
}
