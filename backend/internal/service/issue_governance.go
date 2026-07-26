package service

import (
	"fmt"
	"time"

	"github.com/ai-optimizer/backend/internal/model"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// IssueGovernanceService Issue 治理核心服务：任务重试时的覆盖与继承逻辑
type IssueGovernanceService struct {
	fpSvc *FingerprintService
}

func NewIssueGovernanceService() *IssueGovernanceService {
	return &IssueGovernanceService{
		fpSvc: NewFingerprintService(),
	}
}

// ProcessTaskCompletion 任务完成时的事务处理
// 在同一数据库事务内完成：软删除旧 Issue → 插入新 Issue → 指纹匹配 → 状态继承
func (s *IssueGovernanceService) ProcessTaskCompletion(taskID uint, newIssues []model.ReviewIssue) error {
	return model.DB.Transaction(func(tx *gorm.DB) error {
		// 1. 查询当前任务
		var task model.Task
		if err := tx.First(&task, taskID).Error; err != nil {
			return fmt.Errorf("task not found: %w", err)
		}

		// 2. execution_count += 1（首次也会走到这里，从 1 开始；重试时累加）
		if err := tx.Model(&task).UpdateColumn("execution_count", gorm.Expr("execution_count + 1")).Error; err != nil {
			return fmt.Errorf("update execution_count failed: %w", err)
		}
		task.ExecutionCount++ // 本地值也要更新

		// 3. 软删除该任务此前所有当前可见的 Issue
		if err := tx.Model(&model.ReviewIssue{}).
			Where("task_id = ? AND deleted_at IS NULL", taskID).
			UpdateColumn("deleted_at", time.Now()).Error; err != nil {
			return fmt.Errorf("soft delete old issues failed: %w", err)
		}

		// 4. 获取 MR 信息用于 owner 解析
		mrID := task.MRMergeID
		ownerID := uint(0)
		// 尝试根据 MRAuthor(git 用户名) 匹配平台用户
		if task.MRAuthor != "" {
			var user model.User
			if err := tx.Where("gitlab_username = ? OR gitlab_username = ?",
				task.MRAuthor, task.MRAuthor).First(&user).Error; err == nil {
				ownerID = user.ID
			}
		}

		// 5. 逐条处理新 Issue
		for i := range newIssues {
			issue := &newIssues[i]
			issue.TaskID = taskID
			// 计算语义指纹
			issue.Fingerprint = s.fpSvc.Compute(issue)

			// 解析 owner
			if ownerID > 0 {
				issue.OwnerID = &ownerID
				issue.CurrentOwnerID = &ownerID
			}

			// 指纹匹配与状态继承
			if mrID > 0 {
				hist := s.fpSvc.MatchBest(uint(mrID), issue.Fingerprint)
				if hist != nil {
					issue.InheritedFromIssueID = &hist.ID
					switch hist.Status {
					case model.IssueStatusRejected, model.IssueStatusDismissed, model.IssueStatusAutoFiltered:
						// 历史为误报/忽略/自动过滤 → 直接继承 auto_filtered
						issue.Status = model.IssueStatusAutoFiltered
						issue.OriginalCreatedAt = hist.OriginalCreatedAt
						if issue.OriginalCreatedAt == nil || issue.OriginalCreatedAt.IsZero() {
							issue.OriginalCreatedAt = &hist.CreatedAt
						}
					case model.IssueStatusAccepted:
						// 历史为已修复 → 新出现保持 pending（代码可能回退或不同语境）
						issue.Status = model.IssueStatusPending
						issue.OriginalCreatedAt = hist.OriginalCreatedAt
						if issue.OriginalCreatedAt == nil || issue.OriginalCreatedAt.IsZero() {
							issue.OriginalCreatedAt = &hist.CreatedAt
						}
					default:
						// 历史上也是 pending → 继承首次发现时间，防止重试刷时间
						if !hist.CreatedAt.IsZero() {
							issue.OriginalCreatedAt = &hist.CreatedAt
						}
					}
				}
			}

			if issue.OriginalCreatedAt == nil || issue.OriginalCreatedAt.IsZero() {
				now := time.Now()
				issue.OriginalCreatedAt = &now
			}

			// 插入新记录
			if err := tx.Create(issue).Error; err != nil {
				return fmt.Errorf("create issue failed: %w", err)
			}
		}

		// 6. 更新任务 issue_count
		pendingCount := 0
		for _, issue := range newIssues {
			if issue.Status == model.IssueStatusPending {
				pendingCount++
			}
		}
		if err := tx.Model(&task).UpdateColumns(map[string]interface{}{
			"issue_count":         len(newIssues),
			"pending_issue_count": pendingCount,
		}).Error; err != nil {
			return fmt.Errorf("update task counts failed: %w", err)
		}

		zap.L().Info("task completion processed",
			zap.Uint("task_id", taskID),
			zap.Int("new_issues", len(newIssues)),
			zap.Int("pending", pendingCount),
			zap.Int("execution_count", task.ExecutionCount))

		return nil
	})
}

// ResolveIssue 用户处理 Issue（前端调用）
func (s *IssueGovernanceService) ResolveIssue(issueID uint, userID uint, status string, reason string) error {
	if status != model.IssueStatusAccepted && status != model.IssueStatusRejected && status != model.IssueStatusDismissed {
		return fmt.Errorf("invalid status: %s", status)
	}
	now := time.Now()
	updates := map[string]interface{}{
		"status":       status,
		"resolved_by":  userID,
		"resolved_at":  &now,
		"is_resolved":  true,
		"reject_reason": reason,
	}
	if err := model.DB.Model(&model.ReviewIssue{}).Where("id = ?", issueID).Updates(updates).Error; err != nil {
		return err
	}
	return nil
}
