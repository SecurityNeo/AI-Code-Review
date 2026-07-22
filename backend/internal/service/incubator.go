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

// ClusterIssues performs keyword-based clustering on unmatched issues.
func (s *IncubatorService) ClusterIssues(timeRangeDays int, languages []string, minGroupSize int) (*model.RuleIncubationJob, error) {
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
		// placeholder: review_issues lacks language column; file-extension inference is a future enhancement
		_ = languages
	}
	if err := db.Find(&issues).Error; err != nil {
		return nil, err
	}

	job := &model.RuleIncubationJob{
		JobType:       "cluster",
		Status:        "running",
		TimeRangeFrom: &since,
		TimeRangeTo:   func() *time.Time { t := time.Now(); return &t }(),
		CreatedBy:     0,
		CreatedAt:     time.Now(),
		StartedAt:     func() *time.Time { t := time.Now(); return &t }(),
	}
	model.DB.Create(job)

	// Keyword clustering (level 1 + 2, no embedding)
	clusters := s.keywordCluster(issues, minGroupSize)

	// Generate candidates from top clusters
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
		}
	}

	summary := map[string]any{
		"total_issues_scanned": len(issues),
		"clusters_found":       len(clusters),
		"candidates_generated": generated,
	}
	summaryJSON, _ := json.Marshal(summary)

	now := time.Now()
	model.DB.Model(job).Updates(map[string]any{
		"status":         "success",
		"result_summary": string(summaryJSON),
		"completed_at":   now,
	})

	return job, nil
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
		k := groupKey{Category: iss.Category, Language: iss.Language, Severity: iss.Severity}
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
func (s *IncubatorService) SaveConfig(updates map[string]any) error {
	var cfg model.IncubatorConfig
	if err := model.DB.First(&cfg, 1).Error; err != nil {
		return err
	}
	return model.DB.Model(&cfg).Updates(updates).Error
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

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
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
