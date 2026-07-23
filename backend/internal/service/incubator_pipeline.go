package service

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/ai-optimizer/backend/internal/model"
)

// PipelineStatus represents the full incubation pipeline state with every step's input/output.
type PipelineStatus struct {
	Steps []PipelineStep `json:"steps"`
}

// PipelineStep represents one node in the pipeline.
type PipelineStep struct {
	StepID        string         `json:"step_id"`
	Label         string         `json:"label"`
	Status        string         `json:"status"` // active / idle / running / error / completed
	Icon          string         `json:"icon"`
	Color         string         `json:"color"`
	Stats         map[string]any `json:"stats"`
	InputSummary  map[string]any `json:"input_summary"`
	OutputSummary map[string]any `json:"output_summary"`
	NextSteps     []string       `json:"next_steps"`
}

// GetPipelineStatus aggregates the entire incubation pipeline data.
func (s *IncubatorService) GetPipelineStatus() (*PipelineStatus, error) {
	now := time.Now()
	cfg := s.getConfig()

	// --- Step 1: Issue Pool ---
	var totalUnmatched int64
	model.DB.Model(&model.ReviewIssue{}).Where("rule_id IS NULL").Count(&totalUnmatched)
	var newToday int64
	todayStart := now.Truncate(24 * time.Hour)
	model.DB.Model(&model.ReviewIssue{}).Where("rule_id IS NULL AND created_at >= ?", todayStart).Count(&newToday)

	stepIssuePool := PipelineStep{
		StepID: "issue_pool",
		Label:  "原始Issue池",
		Status: "active",
		Icon:   "fa-bug",
		Color:  "gray",
		Stats: map[string]any{
			"total":     totalUnmatched,
			"new_today": newToday,
		},
		OutputSummary: map[string]any{
			"未命中规则Issue": fmt.Sprintf("%d 条", totalUnmatched),
		},
		NextSteps: []string{"cluster"},
	}

	// --- Step 2: Cluster ---
	var latestCluster model.RuleIncubationJob
	clusterStatus := "idle"
	clusterOut := map[string]any{}
	if err := model.DB.Where("job_type = ?", "cluster").Order("created_at DESC").First(&latestCluster).Error; err == nil {
		if latestCluster.Status == "running" {
			clusterStatus = "running"
		} else if latestCluster.Status == "success" {
			clusterStatus = "completed"
		} else if latestCluster.Status == "failed" {
			clusterStatus = "error"
		}
		clusterOut = map[string]any{
			"任务ID":   latestCluster.ID,
			"上次执行": latestCluster.CreatedAt,
			"状态":     latestCluster.Status,
			"结果":     latestCluster.ResultSummary,
		}
	}
	// Parse actual params from latest job if available
	clusterInput := map[string]any{
		"回溯时间窗口(天)": cfg.ClusterTimeWindowDays,
		"最小组大小":       cfg.ClusterMinGroupSize,
	}
	if latestCluster.ID > 0 && latestCluster.Params != "" && latestCluster.Params != "{}" {
		var p map[string]any
		if err := json.Unmarshal([]byte(latestCluster.Params), &p); err == nil {
			if v, ok := p["time_range_days"]; ok {
				clusterInput["回溯时间窗口(天)"] = v
			}
			if v, ok := p["min_group_size"]; ok {
				clusterInput["最小组大小"] = v
			}
		}
	}
	stepCluster := PipelineStep{
		StepID: "cluster",
		Label:  "聚类分析",
		Status: clusterStatus,
		Icon:   "fa-project-diagram",
		Color:  "purple",
		Stats: map[string]any{
			"latest_job_id": latestCluster.ID,
		},
		InputSummary:  clusterInput,
		OutputSummary: clusterOut,
		NextSteps:     []string{"candidate"},
	}

	// --- Step 3: Candidate Rules ---
	var candTotal int64
	model.DB.Model(&model.RuleIncubation{}).Count(&candTotal)
	var candDraft, candReady, candPublished, candRejected int64
	model.DB.Model(&model.RuleIncubation{}).Where("status = ?", "draft").Count(&candDraft)
	model.DB.Model(&model.RuleIncubation{}).Where("status = ?", "ready").Count(&candReady)
	model.DB.Model(&model.RuleIncubation{}).Where("status = ?", "published").Count(&candPublished)
	model.DB.Model(&model.RuleIncubation{}).Where("status = ?", "rejected").Count(&candRejected)

	stepCandidate := PipelineStep{
		StepID: "candidate",
		Label:  "候选规则",
		Status: "active",
		Icon:   "fa-lightbulb",
		Color:  "blue",
		Stats: map[string]any{
			"total":     candTotal,
			"draft":     candDraft,
			"ready":     candReady,
			"published": candPublished,
			"rejected":  candRejected,
		},
		InputSummary: map[string]any{
			"来源": "聚类分析生成的候选规则",
		},
		OutputSummary: map[string]any{
			"候选总数":   candTotal,
			"待审核":     candDraft + candReady,
			"可发布":     candReady,
		},
		NextSteps: []string{"refine", "similar_check", "sandbox_test"},
	}

	// --- Step 4: Refine (LLM Prompt enhancement) ---
	var latestRefine model.RuleIncubationJob
	refineStatus := "idle"
	if err := model.DB.Where("job_type = ?", "refine").Order("created_at DESC").First(&latestRefine).Error; err == nil {
		if latestRefine.Status == "pending" || latestRefine.Status == "running" {
			refineStatus = "running"
		} else if latestRefine.Status == "success" {
			refineStatus = "completed"
		} else if latestRefine.Status == "failed" {
			refineStatus = "error"
		}
	}
	stepRefine := PipelineStep{
		StepID: "refine",
		Label:  "智能提炼",
		Status: refineStatus,
		Icon:   "fa-magic",
		Color:  "indigo",
		Stats: map[string]any{
			"latest_job_id": latestRefine.ID,
		},
		InputSummary: map[string]any{
			"来源": "候选规则 + 源Issue样本",
		},
		OutputSummary: map[string]any{
			"说明": "由LLM自动生成规则名称、描述和Prompt",
		},
		NextSteps: []string{"similar_check", "sandbox_test"},
	}

	// --- Step 5: Similar Check ---
	var latestSimilar model.RuleIncubationJob
	similarStatus := "idle"
	if err := model.DB.Where("job_type = ?", "similar_check").Order("created_at DESC").First(&latestSimilar).Error; err == nil {
		if latestSimilar.Status == "pending" || latestSimilar.Status == "running" {
			similarStatus = "running"
		} else if latestSimilar.Status == "success" {
			similarStatus = "completed"
		} else if latestSimilar.Status == "failed" {
			similarStatus = "error"
		}
	}
	stepSimilar := PipelineStep{
		StepID: "similar_check",
		Label:  "相似检测",
		Status: similarStatus,
		Icon:   "fa-search",
		Color:  "orange",
		Stats: map[string]any{
			"latest_job_id": latestSimilar.ID,
		},
		InputSummary: map[string]any{
			"来源": "候选规则与现有规则库对比",
		},
		OutputSummary: map[string]any{
			"说明": "检测候选规则与现有规则的重复/相似性",
		},
		NextSteps: []string{"sandbox_test", "publish"},
	}

	// --- Step 6: Sandbox Test ---
	var latestTest model.RuleIncubationJob
	testStatus := "idle"
	if err := model.DB.Where("job_type = ?", "sandbox_test").Order("created_at DESC").First(&latestTest).Error; err == nil {
		if latestTest.Status == "pending" || latestTest.Status == "running" {
			testStatus = "running"
		} else if latestTest.Status == "success" {
			testStatus = "completed"
		} else if latestTest.Status == "failed" {
			testStatus = "error"
		}
	}
	var totalTestRuns int64
	model.DB.Model(&model.RuleIncubation{}).Where("test_results IS NOT NULL AND test_results != '{}' AND test_results != ''").Count(&totalTestRuns)
	stepSandbox := PipelineStep{
		StepID: "sandbox_test",
		Label:  "模拟测试",
		Status: testStatus,
		Icon:   "fa-vial",
		Color:  "teal",
		Stats: map[string]any{
			"latest_job_id": latestTest.ID,
			"total_runs":    totalTestRuns,
		},
		InputSummary: map[string]any{
			"来源": "候选规则 + 正例/反例代码片段",
		},
		OutputSummary: map[string]any{
			"说明": "使用LLM验证规则对正例/反例的判定准确率",
		},
		NextSteps: []string{"publish"},
	}

	// --- Step 7: Publish ---
	var totalPublishedRules int64
	model.DB.Model(&model.ReviewRule{}).Where("is_built_in = ?", false).Count(&totalPublishedRules)
	stepPublish := PipelineStep{
		StepID: "publish",
		Label:  "发布规则",
		Status: "active",
		Icon:   "fa-rocket",
		Color:  "green",
		Stats: map[string]any{
			"total_published": totalPublishedRules,
		},
		InputSummary: map[string]any{
			"来源": "审核通过且测试达标的候选规则",
		},
		OutputSummary: map[string]any{
			"已发布规则数": totalPublishedRules,
			"说明":          "正式发布到评审规则库",
		},
		NextSteps: []string{"retro_match"},
	}

	// --- Step 8: Retro Match ---
	var retroTotal int64
	model.DB.Model(&model.ReviewIssueRuleMatch{}).Count(&retroTotal)
	var latestRetro model.RuleIncubationJob
	retroStatus := "idle"
	if cfg.RetroMatchEnabled {
		retroStatus = "active"
	}
	if err := model.DB.Where("job_type = ?", "retro_match").Order("created_at DESC").First(&latestRetro).Error; err == nil {
		if latestRetro.Status == "pending" || latestRetro.Status == "running" {
			retroStatus = "running"
		} else if latestRetro.Status == "success" {
			retroStatus = "completed"
		}
	}
	stepRetro := PipelineStep{
		StepID: "retro_match",
		Label:  "回溯匹配",
		Status: retroStatus,
		Icon:   "fa-history",
		Color:  "cyan",
		Stats: map[string]any{
			"total_retro_matched": retroTotal,
			"latest_job_id":       latestRetro.ID,
		},
		InputSummary: map[string]any{
			"来源":          "已发布的新规则",
			"回溯天数":      cfg.RetroMatchMaxDaysLookback,
			"置信度阈值":    cfg.RetroMatchConfidenceThreshold,
		},
		OutputSummary: map[string]any{
			"匹配总数": retroTotal,
			"说明":     "在历史Issue中寻找可被新规则命中的记录",
		},
		NextSteps: []string{"health_check"},
	}

	// --- Step 9: Health Check ---
	var enabledRulesCount int64
	model.DB.Model(&model.ReviewRule{}).Where("is_enabled = ?", true).Count(&enabledRulesCount)

	stepHealth := PipelineStep{
		StepID: "health_check",
		Label:  "健康监控",
		Status: func() string {
			if cfg.HealthCheckEnabled {
				return "active"
			}
			return "disabled"
		}(),
		Icon:  "fa-heartbeat",
		Color: "red",
		Stats: map[string]any{
			"check_interval_days": cfg.HealthCheckIntervalDays,
			"enabled_rules":       enabledRulesCount,
		},
		InputSummary: map[string]any{
			"来源": "所有已启用规则的命中/拒绝统计数据",
		},
		OutputSummary: map[string]any{
			"说明": "定期评估规则质量，发现退化或误报过高的规则",
		},
		NextSteps: []string{},
	}

	// --- Embedding status affects all semantic steps ---
	embeddingAvailable := s.embedSvc.IsAvailable()
	if !embeddingAvailable {
		for i := range []int{1, 3, 4, 5, 7} {
			// cluster, refine, similar, retro
			_ = i
		}
	}

	steps := []PipelineStep{
		stepIssuePool,
		stepCluster,
		stepCandidate,
		stepRefine,
		stepSimilar,
		stepSandbox,
		stepPublish,
		stepRetro,
		stepHealth,
	}

	// Merge active pipeline_run execution state into steps
	var pipeJob model.RuleIncubationJob
	if err := model.DB.Where("job_type = ?", "pipeline_run").Order("created_at DESC").First(&pipeJob).Error; err == nil {
		if pipeJob.Status == "running" || pipeJob.Status == "success" || pipeJob.Status == "failed" {
			var jr map[string]any
			_ = json.Unmarshal([]byte(pipeJob.ResultSummary), &jr)
			if stepsMap, ok := jr["pipeline_steps"].(map[string]any); ok {
				for i := range steps {
					if s, ok := stepsMap[steps[i].StepID]; ok {
						if m, ok := s.(map[string]any); ok {
							if st, ok := m["status"].(string); ok {
								steps[i].Status = st
							}
						}
					}
				}
			}
		}
	}

	return &PipelineStatus{Steps: steps}, nil
}

// GetCandidateTrace returns the full bloodline (upstream + downstream) of a candidate rule.
func (s *IncubatorService) GetCandidateTrace(incubationID uint) (map[string]any, error) {
	// Fetch candidate with source issues
	cand, issues, err := s.GetCandidate(incubationID)
	if err != nil {
		return nil, err
	}

	// Upstream: source issues
	var upstreamIssues []map[string]any
	for _, iss := range issues {
		upstreamIssues = append(upstreamIssues, map[string]any{
			"id":           iss.ID,
			"file":         iss.File,
			"message":      iss.Message,
			"category":     iss.Category,
			"severity":     iss.Severity,
			"created_at":   iss.CreatedAt,
			"code_snippet": iss.CodeSnippet,
		})
	}

	// Downstream: published rule
	var downstreamRule map[string]any
	if cand.PublishedRuleID != nil && *cand.PublishedRuleID > 0 {
		var rule model.ReviewRule
		if err := model.DB.First(&rule, *cand.PublishedRuleID).Error; err == nil {
			downstreamRule = map[string]any{
				"id":         rule.ID,
				"code":       rule.Code,
				"name":       rule.Name,
				"is_enabled": rule.IsEnabled,
			}
		}
	}

	// Downstream: retro matches
	var retroMatches int64
	if cand.PublishedRuleID != nil {
		model.DB.Model(&model.ReviewIssueRuleMatch{}).Where("rule_id = ?", *cand.PublishedRuleID).Count(&retroMatches)
	}

	// Similar rules (parsed from JSON string)
	var similar []map[string]any
	if cand.SimilarRules != "" && cand.SimilarRules != "{}" {
		_ = json.Unmarshal([]byte(cand.SimilarRules), &similar)
	}

	// Test results (parsed from JSON string)
	var testResults map[string]any
	if cand.TestResults != "" && cand.TestResults != "{}" {
		_ = json.Unmarshal([]byte(cand.TestResults), &testResults)
	}

	return map[string]any{
		"candidate": map[string]any{
			"id":         cand.ID,
			"name":       cand.Name,
			"code":       cand.Code,
			"status":     cand.Status,
			"category":   cand.Category,
			"severity":   cand.Severity,
			"prompt":     cand.Prompt,
			"confidence": cand.ConfidenceScore,
		},
		"upstream": map[string]any{
			"source_issues": upstreamIssues,
			"source_count":  len(upstreamIssues),
		},
		"downstream": map[string]any{
			"published_rule": downstreamRule,
			"retro_matches":  retroMatches,
		},
		"similar_rules": similar,
		"test_results":  testResults,
	}, nil
}

// GetIssueTrace returns the full lifecycle trace of a single review issue.
func (s *IncubatorService) GetIssueTrace(issueID uint) (map[string]any, error) {
	var issue model.ReviewIssue
	if err := model.DB.First(&issue, issueID).Error; err != nil {
		return nil, err
	}

	// Find which incubation candidates include this issue
	var incubations []model.RuleIncubation
	model.DB.Where("JSON_CONTAINS(source_issue_ids, ?, '$')", fmt.Sprintf("%d", issueID)).Find(&incubations)
	// Note: JSON_CONTAINS is MySQL-specific. Fallback for generic:
	// In a real MySQL environment the above works; for compatibility we can also scan all.
	if len(incubations) == 0 {
		var allCandidates []model.RuleIncubation
		model.DB.Find(&allCandidates)
		for _, c := range allCandidates {
			var ids []uint
			if err := json.Unmarshal([]byte(c.SourceIssueIDs), &ids); err == nil {
				for _, id := range ids {
					if id == issueID {
						incubations = append(incubations, c)
						break
					}
				}
			}
		}
	}

	var candidates []map[string]any
	for _, inc := range incubations {
		candidates = append(candidates, map[string]any{
			"id":     inc.ID,
			"name":   inc.Name,
			"status": inc.Status,
		})
	}

	// Find published rule (if issue has rule_id)
	var hitRule map[string]any
	if issue.RuleID != nil {
		var rule model.ReviewRule
		if err := model.DB.First(&rule, *issue.RuleID).Error; err == nil {
			hitRule = map[string]any{
				"id":   rule.ID,
				"code": rule.Code,
				"name": rule.Name,
			}
		}
	}

	// Find retroactive matches
	var retroMatches []map[string]any
	var matches []model.ReviewIssueRuleMatch
	model.DB.Where("issue_id = ?", issueID).Find(&matches)
	for _, m := range matches {
		var rule model.ReviewRule
		model.DB.First(&rule, m.RuleID)
		retroMatches = append(retroMatches, map[string]any{
			"rule_id":    m.RuleID,
			"rule_name":  rule.Name,
			"confidence": m.Confidence,
			"matched_at": m.MatchedAt,
		})
	}

	return map[string]any{
		"issue": map[string]any{
			"id":           issue.ID,
			"file":         issue.File,
			"message":      issue.Message,
			"category":     issue.Category,
			"severity":     issue.Severity,
			"status":       issue.Status,
			"code_snippet": issue.CodeSnippet,
			"created_at":   issue.CreatedAt,
		},
		"hit_rule":      hitRule,
		"candidates":    candidates,
		"retro_matches": retroMatches,
	}, nil
}
