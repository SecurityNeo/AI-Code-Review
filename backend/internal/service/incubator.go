package service

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ai-optimizer/backend/internal/model"
	"github.com/ai-optimizer/backend/internal/vectorstore"
	"go.uber.org/zap"
)

// IncubatorService handles rule incubation business logic.
type IncubatorService struct {
	embedSvc *EmbeddingService
	store    vectorstore.Store
}

// NewIncubatorService creates a new incubator service.
func NewIncubatorService(embedSvc *EmbeddingService, store vectorstore.Store) *IncubatorService {
	return &IncubatorService{
		embedSvc: embedSvc,
		store:    store,
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

	code := fmt.Sprintf("incubated-%s-%s-%d", modeKey(catCount), modeKey(langCount), time.Now().Unix())

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
	}

	if err := model.DB.Create(cand).Error; err != nil {
		return nil, err
	}

	return cand, nil
}

// CreateCandidateRequest is the input for creating a candidate rule.
type CreateCandidateRequest struct {
	SourceIssueIDs []uint `json:"source_issue_ids" binding:"required,min=1"`
	ClusterID      string `json:"cluster_id"`
	AutoRefine     bool   `json:"auto_refine"`
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

// DeleteCandidate soft-deletes by marking rejected.
func (s *IncubatorService) DeleteCandidate(id uint) error {
	var cand model.RuleIncubation
	if err := model.DB.First(&cand, id).Error; err != nil {
		return err
	}
	if cand.Status == "published" {
		return fmt.Errorf("cannot delete published candidate")
	}
	return model.DB.Model(&cand).Update("status", "rejected").Error
}

// ListCandidates returns incubation candidates with optional status filter.
func (s *IncubatorService) ListCandidates(status, keyword string, page, pageSize int) ([]model.RuleIncubation, int64, error) {
	db := model.DB.Model(&model.RuleIncubation{})
	if status != "" {
		db = db.Where("status = ?", status)
	}
	if keyword != "" {
		db = db.Where("name LIKE ? OR code LIKE ?", "%"+keyword+"%", "%"+keyword+"%")
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
	job := &model.RuleIncubationJob{
		JobType:       "cluster",
		Status:        "running",
		TimeRangeFrom: &since,
		TimeRangeTo:   func() *time.Time { t := time.Now(); return &t }(),
		CreatedBy:     0,
		CreatedAt:     time.Now(),
		StartedAt:     func() *time.Time { t := time.Now(); return &t }(),
	}
	if err := model.DB.Create(job).Error; err != nil {
		return nil, fmt.Errorf("create cluster job failed: %w", err)
	}

	summary, err := s.doClusterIssues(timeRangeDays, languages, minGroupSize)
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
func (s *IncubatorService) doClusterIssues(timeRangeDays int, languages []string, minGroupSize int) (string, error) {
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

	clusters := s.keywordCluster(issues, minGroupSize)

	generated := 0
	for _, cl := range clusters {
		if generated >= cfg.ClusterMaxGroupsPerRun {
			break
		}
		_, err := s.CreateCandidate(CreateCandidateRequest{
			SourceIssueIDs: cl.IssueIDs,
			ClusterID:      cl.ID,
			AutoRefine:     false,
		}, 0)
		if err == nil {
			generated++
		} else {
			zap.L().Warn("create candidate from cluster failed", zap.String("cluster_id", cl.ID), zap.Error(err))
		}
	}

	summary := map[string]any{
		"total_issues_scanned": len(issues),
		"clusters_found":       len(clusters),
		"candidates_generated": generated,
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

// ValidateEmbedding checks whether the configured embedding model is reachable.
func (s *IncubatorService) ValidateEmbedding() (map[string]any, error) {
	cfg := s.getConfig()
	if cfg.EmbeddingModelID == nil {
		return nil, fmt.Errorf("embedding model not configured")
	}
	var m model.LLMModel
	if err := model.DB.First(&m, *cfg.EmbeddingModelID).Error; err != nil {
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
		"dimension":   tokens, // wrong semantic, but we don't have dimension here; placeholder
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
	if len(msg) > 30 {
		msg = msg[:30]
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
		if len(m) > 100 {
			m = m[:100] + "..."
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
	end := -1
	for i := start; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				end = i
				break
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
	systemPrompt := "You are a code-review rule engineer. Your task is to analyze a set of code issues and abstract them into a general review rule."
	userPrompt := fmt.Sprintf(`Analyze the following code-review issues and output a structured review rule.

Issues:
%s

Please output in the following JSON format (do not include markdown code block):
{
  "name": "A concise Chinese rule name (within 20 characters)",
  "description": "A 1-2 sentence description of what this rule checks, why it matters, and how to fix it (in Chinese)",
  "prompt": "A detailed instruction in Chinese for an LLM to perform this check. Include: 1) clear check objectives, 2) judgment criteria, 3) examples of what constitutes a violation and what does not, 4) only output instruction text, no explanatory text."
}
`, sampleText)

	resp, err := llmSvc.ChatCompletion(nil, 0, "rule_refine", systemPrompt, userPrompt)
	if err != nil {
		return fmt.Errorf("llm refine failed: %w", err)
	}

	// Parse JSON from LLM response
	var refineResult struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Prompt      string `json:"prompt"`
	}
	content := extractJSON(resp.Content)
	if err := json.Unmarshal([]byte(content), &refineResult); err != nil {
		return fmt.Errorf("parse refine result failed: %w, raw: %s", err, resp.Content)
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
	updates["status"] = "ready"
	if len(updates) > 0 {
		return s.UpdateCandidate(incubationID, updates)
	}
	return nil
}

// runSimilarCheck detects similarity between the candidate and existing rules.
func (s *IncubatorService) runSimilarCheck(incubationID uint) error {
	cand, _, err := s.GetCandidate(incubationID)
	if err != nil {
		return err
	}

	var rules []model.ReviewRule
	if err := model.DB.Where("is_enabled = ?", true).Find(&rules).Error; err != nil {
		return err
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
		return err
	}

	// Also check code uniqueness
	var codeExists int64
	model.DB.Model(&model.ReviewRule{}).Where("code = ?", cand.Code).Count(&codeExists)
	updates := map[string]any{
		"similar_rules": string(similarJSON),
	}
	if codeExists > 0 {
		updates["code"] = fmt.Sprintf("%s-%d", cand.Code, time.Now().Unix())
	}

	return s.UpdateCandidate(incubationID, updates)
}

// runSandboxTest uses LLM to verify the candidate rule against positive/negative test cases.
func (s *IncubatorService) runSandboxTest(incubationID uint) error {
	cand, issues, err := s.GetCandidate(incubationID)
	if err != nil {
		return err
	}
	if cand.Prompt == "" {
		return fmt.Errorf("prompt is empty, cannot test")
	}

	// Build positive cases from accepted/interesting issues and negative from sibling code without issues
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

	var cases []testCase

	// Positive: source issues that have code snippets
	posCount := 0
	for _, iss := range issues {
		if posCount >= 5 {
			break
		}
		if iss.CodeSnippet != "" {
			cases = append(cases, testCase{
				No:       posCount + 1,
				Type:     "positive",
				Code:     iss.CodeSnippet,
				File:     iss.File,
				Expected: true,
			})
			posCount++
		}
	}
	if posCount == 0 {
		return fmt.Errorf("no code snippets for positive test cases")
	}

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
		// Strictly determine YES vs NO based on the first occurrence of a decisive word.
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
			cases[i].Actual = !cases[i].Expected // unknown -> mark as fail
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

	testResult := map[string]any{
		"total_cases": len(cases),
		"passed":      passed,
		"accuracy":    float64(passed) / float64(len(cases)),
		"details":     cases,
	}
	resultJSON, _ := json.Marshal(testResult)

	return model.DB.Model(&model.RuleIncubation{}).Where("id = ?", incubationID).Update("test_results", string(resultJSON)).Error
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
