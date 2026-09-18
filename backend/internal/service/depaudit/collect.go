package depaudit

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ai-optimizer/backend/internal/model"
	"go.uber.org/zap"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// CollectService 依赖坐标收集
type CollectService struct {
	db *gorm.DB
}

func NewCollectService(db *gorm.DB) *CollectService {
	return &CollectService{db: db}
}

// CollectProject 收集单个项目的依赖坐标并写入快照。
// force=true 时无论 commit 是否变化都重新解析。
// 返回：依赖数、commit、实际分支、error
func (s *CollectService) CollectProject(project *model.Project, cfg *model.DependencyAuditConfig, force bool) (int, string, string, error) {
	branch := project.GraphBaseBranch
	if branch == "" {
		branch = "main"
	}

	lock := lockProject(project.ID)
	lock.Lock()
	defer lock.Unlock()

	repoDir, usedBranch, err := ensureRepoWithFallback(*project, branch)
	if err != nil {
		s.upsertState(project, branch, "", 0, "failed", err.Error())
		return 0, "", branch, err
	}
	commit := readCommitSHA(repoDir)

	// commit 未变化且已有快照 → 跳过解析
	if !force {
		var st model.DependencyAuditCollectState
		if err := s.db.Where("project_id = ?", project.ID).First(&st).Error; err == nil {
			var cnt int64
			s.db.Model(&model.DependencyAuditProjectDep{}).Where("project_id = ?", project.ID).Count(&cnt)
			if st.LastCommit != "" && st.LastCommit == commit && cnt > 0 {
				now := time.Now()
				s.db.Model(&model.DependencyAuditCollectState{}).Where("project_id = ?", project.ID).
					Updates(map[string]interface{}{"branch": usedBranch, "last_collected_at": now, "status": "success", "error": ""})
				return int(cnt), commit, usedBranch, nil
			}
		}
	}

	deps, rep, err := ParseRepoDetailed(repoDir, cfg)
	if err != nil {
		s.upsertState(project, usedBranch, commit, 0, "failed", err.Error())
		return 0, commit, usedBranch, err
	}

	rows := make([]*model.DependencyAuditProjectDep, 0, len(deps))
	for _, d := range deps {
		rows = append(rows, &model.DependencyAuditProjectDep{
			OrgID:           project.OrgID,
			ProjectID:       project.ID,
			Ecosystem:       d.Ecosystem,
			PackageName:     d.Name,
			PackageVersion:  d.Version,
			VersionResolved: d.VersionResolved,
			Direct:          d.Direct,
			Scope:           d.Scope,
			SourceFiles:     toJSONArray(d.SourceFiles),
		})
	}

	err = s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("project_id = ?", project.ID).Delete(&model.DependencyAuditProjectDep{}).Error; err != nil {
			return err
		}
		if len(rows) > 0 {
			if err := tx.CreateInBatches(rows, 500).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		s.upsertState(project, usedBranch, commit, 0, "failed", err.Error())
		return 0, commit, usedBranch, err
	}

	if len(rows) == 0 {
		// 记录可诊断的跳过原因（无受支持清单 / 清单未解析出依赖）
		var reason string
		if len(rep.ManifestFiles) == 0 {
			reason = "未发现受支持的依赖清单（go.mod/package.json/requirements.txt/pom.xml/build.gradle/pyproject.toml）"
		} else {
			reason = "清单未解析出依赖：" + strings.Join(rep.ManifestFiles, ", ")
		}
		s.upsertState(project, usedBranch, commit, 0, "skipped", reason)
		return 0, commit, usedBranch, nil
	}
	s.upsertState(project, usedBranch, commit, len(rows), "success", "")
	return len(rows), commit, usedBranch, nil
}

func (s *CollectService) upsertState(project *model.Project, branch, commit string, count int, status, errMsg string) {
	now := time.Now()
	rec := &model.DependencyAuditCollectState{
		OrgID: project.OrgID, ProjectID: project.ID, Branch: branch, LastCommit: commit,
		LastCollectedAt: &now, DepCount: count, Status: status, Error: truncateStr(sanitizeMessage(errMsg), 500),
	}
	if err := s.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "project_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"branch", "last_commit", "last_collected_at", "dep_count", "status", "error", "updated_at",
		}),
	}).Create(rec).Error; err != nil {
		zap.L().Warn("upsert collect state failed", zap.Uint("project_id", project.ID), zap.Error(err))
	}
}

// CollectOrg 收集组织内项目坐标，并记录作业日志
func (s *CollectService) CollectOrg(orgID uint, projectIDs []uint) {
	q := s.db.Where("org_id = ?", orgID)
	if len(projectIDs) > 0 {
		q = q.Where("id IN ?", projectIDs)
	}
	var projects []model.Project
	if err := q.Find(&projects).Error; err != nil {
		zap.L().Error("collect: load projects failed", zap.Uint("org_id", orgID), zap.Error(err))
		return
	}
	cfg, _ := GetConfig(s.db, orgID)
	tracker := startJobLog(s.db, orgID, JobCollectCoordinates, len(projects))

	sem := make(chan struct{}, defaultConcurrency)
	var wg sync.WaitGroup
	for i := range projects {
		p := projects[i]
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			defer func() {
				if r := recover(); r != nil {
					zap.L().Error("collect project panic", zap.Uint("project_id", p.ID), zap.Any("panic", r))
					tracker.add(0, 1)
				}
			}()
			if _, _, _, err := s.CollectProject(&p, cfg, false); err != nil {
				tracker.add(0, 1)
			} else {
				tracker.add(1, 0)
			}
		}()
	}
	wg.Wait()
	tracker.finish(fmt.Sprintf("项目 %d 个", len(projects)))
}

// CollectStatus 各项目快照状态
func CollectStatus(db *gorm.DB, orgID uint) []model.DependencyAuditCollectState {
	var rows []model.DependencyAuditCollectState
	db.Where("org_id = ?", orgID).Order("updated_at desc").Limit(1000).Find(&rows)
	return rows
}
