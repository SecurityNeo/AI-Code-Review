package engine

import (
	"encoding/json"
	"fmt"

	"github.com/ai-optimizer/backend/internal/model"
	"github.com/ai-optimizer/backend/pkg/llm"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// PersistStructuredReview 将结构化评审结果幂等持久化到数据库
// 每次调用会先清理该任务的历史 Issue，再写入最新结果，确保 Retry 后数据一致。
// 注意：ReviewIssue 已加 gorm.DeletedAt 字段，这里的 Delete 实际执行 soft delete
//（UPDATE deleted_at = NOW()），保证历史命中数据可用于规则命中统计。
func PersistStructuredReview(taskID uint, result *llm.AIReviewResult) error {
	// 事务开始前：加载 task 信息和 MR 历史 Issue（用于指纹匹配）
	var task model.Task
	if err := model.DB.First(&task, taskID).Error; err != nil {
		return fmt.Errorf("task not found: %w", err)
	}

	// 解析 MR 提交者对应的平台用户ID
	ownerID := uint(0)
	if task.MRAuthor != "" {
		var user model.User
		if err := model.DB.Where("gitlab_username = ?", task.MRAuthor).First(&user).Error; err == nil {
			ownerID = user.ID
		}
	}

	// 构建历史 Issue 指纹映射（必须在 soft delete 之前查，且跨事务）
	historyMap := BuildIssueHistoryMap(model.DB, task.MRMergeID, taskID)

	return model.DB.Transaction(func(tx *gorm.DB) error {
		// 1. 更新 Task 表
		taskUpdates := map[string]interface{}{
			"ai_response_json": marshalJSON(result),
			"dimension_scores": marshalJSON(result.Dimensions),
			"issue_count":      len(result.Issues),
			"score_value":      result.TotalScore,         // 后置校验后的最终评分
			"raw_ai_score":     result.OriginalTotalScore, // LLM 原始评分（用于对比）
			"execution_count":  gorm.Expr("execution_count + 1"), // 首次创建为0，首次保存后变为1
		}

		if err := tx.Model(&model.Task{}).Where("id = ?", taskID).Updates(taskUpdates).Error; err != nil {
			return fmt.Errorf("update task structured review failed: %w", err)
		}

		// 2. 清理旧 Issue（soft delete，幂等关键：每次重试前先 soft delete 历史记录）
		//    ReviewIssue.DeletedAt 字段使 gorm 自动执行 UPDATE 而非 DELETE。
		if err := tx.Where("task_id = ?", taskID).Delete(&model.ReviewIssue{}).Error; err != nil {
			return fmt.Errorf("soft delete old review issues failed: %w", err)
		}

		// 3. 预加载本次 Issue 引用的 ReviewRule，避免逐条 First 触发 GORM "record not found" Warn 日志
		//    AI 自助发现的 Issue（RuleCode 为空）不参与；规则已删除时静默忽略（RuleID 留空即可）
		ruleByCode := loadReviewRulesByCode(tx, result.Issues)

		// 4. 插入最新 Issue（含指纹计算与状态继承）
		for _, issue := range result.Issues {
			reviewIssue := model.ReviewIssue{
				TaskID:      taskID,
				RuleCode:    issue.RuleCode,
				Category:    issue.Category,
				Severity:    issue.Severity,
				DeductScore: issue.DeductScore,
				File:        issue.File,
				LineStart:   issue.LineStart,
				LineEnd:     issue.LineEnd,
				CodeSnippet: issue.CodeSnippet,
				Message:     issue.Message,
				Suggestion:  issue.Suggestion,
				MRID:        task.MRMergeID,
			}
			if rule, ok := ruleByCode[issue.RuleCode]; ok {
				reviewIssue.RuleID = &rule.ID
			}

			// 指纹计算 + 状态继承
			ApplyFingerprintAndInheritance(&reviewIssue, historyMap, ownerID)

			if err := tx.Create(&reviewIssue).Error; err != nil {
				zap.L().Warn("create review issue failed",
					zap.Uint("task_id", taskID),
					zap.String("rule_code", issue.RuleCode),
					zap.Error(err))
				// 单条失败不影响其他
			}
		}

		// 5. 更新 task_review_rules.IssueCount（只更新本次命中的规则）
		// 先清零该任务所有规则的 issue_count，再写入新计数
		if err := tx.Model(&model.TaskReviewRule{}).
			Where("task_id = ?", taskID).
			UpdateColumn("issue_count", 0).Error; err != nil {
			zap.L().Warn("reset task review rule issue_count failed",
				zap.Uint("task_id", taskID), zap.Error(err))
		}

		ruleHitCount := make(map[string]int)
		for _, issue := range result.Issues {
			ruleHitCount[issue.RuleCode]++
		}
		for ruleCode, count := range ruleHitCount {
			if err := tx.Model(&model.TaskReviewRule{}).
				Where("task_id = ? AND rule_code = ?", taskID, ruleCode).
				UpdateColumn("issue_count", count).Error; err != nil {
				zap.L().Warn("update task review rule issue_count failed",
					zap.Uint("task_id", taskID),
					zap.String("rule_code", ruleCode),
					zap.Error(err))
			}
		}

		zap.L().Info("structured review persisted",
			zap.Uint("task_id", taskID),
			zap.Int("score", result.TotalScore),
			zap.Int("issue_count", len(result.Issues)))

		// Step 6: 刷新项目 pending issue 快照计数（方案设计 Step 4）
		var pendingCount int64
		if err := tx.Model(&model.ReviewIssue{}).
			Where("deleted_at IS NULL AND status IN (?) AND task_id IN (SELECT id FROM tasks WHERE project_id = ?)",
				[]string{model.IssueStatusPending, model.IssueStatusPendingInherited}, task.ProjectID).
			Count(&pendingCount).Error; err == nil {
			tx.Model(&model.Project{}).Where("id = ?", task.ProjectID).UpdateColumn("pending_issue_count", pendingCount)
		}

		return nil
	})
}

func marshalJSON(v interface{}) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// loadReviewRulesByCode 一次性查回 issues 引用的所有 ReviewRule，避免逐条 First
// 在 GORM Warn 模式下产生"record not found"噪音日志；规则已删除/AI 自主发现（code 为空）静默忽略。
func loadReviewRulesByCode(tx *gorm.DB, issues []llm.AIReviewIssue) map[string]model.ReviewRule {
	codes := make([]string, 0, len(issues))
	seen := make(map[string]struct{}, len(issues))
	for _, issue := range issues {
		if issue.RuleCode == "" {
			continue
		}
		if _, ok := seen[issue.RuleCode]; ok {
			continue
		}
		seen[issue.RuleCode] = struct{}{}
		codes = append(codes, issue.RuleCode)
	}
	if len(codes) == 0 {
		return map[string]model.ReviewRule{}
	}
	var rules []model.ReviewRule
	// 单条 SELECT IN 查询替代 N 条 First，未命中的 code 自然不返回，不会触发 ErrRecordNotFound
	if err := tx.Where("code IN ?", codes).Find(&rules).Error; err != nil {
		zap.L().Warn("preload review rules failed", zap.Error(err))
		return map[string]model.ReviewRule{}
	}
	out := make(map[string]model.ReviewRule, len(rules))
	for i := range rules {
		out[rules[i].Code] = rules[i]
	}
	return out
}
