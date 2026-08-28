package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ai-optimizer/backend/internal/model"
	"github.com/ai-optimizer/backend/pkg/diff"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// ObjectStorage 接口别名（避免导入 service 包产生循环依赖）
type ObjectStorage interface {
	Put(ctx context.Context, key string, data []byte) (string, error)
	Get(ctx context.Context, key string) ([]byte, error)
	Delete(ctx context.Context, key string) error
}

// DiffStoreConfig diff 存储配置
type DiffStoreConfig struct {
	// 是否启用对象存储
	Enabled bool
	// 对象存储 key 前缀
	Prefix string
}

// DefaultDiffStoreConfig 默认配置
var DefaultDiffStoreConfig = DiffStoreConfig{
	Enabled: true,
	Prefix:  "diffs/",
}

// DiffStore 封装 diff 的存储逻辑（对象存储 + 数据库索引）
type DiffStore struct {
	config  DiffStoreConfig
	storage ObjectStorage // 对象存储实例（由外部传入，避免循环依赖）
}

// NewDiffStore 创建 diff 存储器
func NewDiffStore(cfg DiffStoreConfig, storage ObjectStorage) *DiffStore {
	return &DiffStore{config: cfg, storage: storage}
}

// StoreDiff 将原始 diff 存储到对象存储，并写入 mr_diff_meta 索引
// 若提供了 diff_refs（baseSha/headSha/startSha），一并写入
// 返回 mr_diff_meta ID 和可能的 error
func (ds *DiffStore) StoreDiff(ctx context.Context, task *model.Task, rawDiff string, parsedFiles []*diff.ParsedDiffFile, baseSha, headSha, startSha string) (*model.MRDiffMeta, error) {
	if !ds.config.Enabled {
		zap.L().Info("diff store disabled, skip storing raw diff")
		return nil, nil
	}
	if task == nil {
		return nil, fmt.Errorf("task is nil")
	}
	if len(strings.TrimSpace(rawDiff)) == 0 {
		zap.L().Info("raw diff is empty, skip storing", zap.Uint("task_id", task.ID))
		return nil, nil
	}

	meta := &model.MRDiffMeta{
		TaskID:    task.ID,
		ProjectID: task.ProjectID,
		MRIID:     task.MRMergeID,
		BaseSha:   baseSha,
		HeadSha:   headSha,
		StartSha:  startSha,
	}

	// 1. 上传对象存储（如果可用）
	if ds.storage != nil {
		key := ds.buildObjectKey(task)
		_, err := ds.storage.Put(ctx, key, []byte(rawDiff))
		if err != nil {
			zap.L().Warn("upload diff to object storage failed, fallback to DB only",
				zap.Error(err),
				zap.Uint("task_id", task.ID),
			)
			// fallback：不上传对象存储，仅保存 line_map 到数据库
		} else {
			meta.ObjectKey = key
			zap.L().Info("raw diff stored to object storage",
				zap.String("key", key),
				zap.Uint("task_id", task.ID),
			)
		}
	} else {
		zap.L().Info("object storage not available, skip upload",
			zap.Uint("task_id", task.ID),
		)
	}

	// 2. 构建 line_map（轻量索引，按文件路径分组）
	if len(parsedFiles) > 0 {
		lineMapByFile := make(map[string][]diff.DiffLineInfo)
		for _, pf := range parsedFiles {
			var fileLines []diff.DiffLineInfo
			for _, line := range pf.Lines {
				t := ""
				switch line.Type {
				case diff.LineTypeContext:
					t = "c"
				case diff.LineTypeAddition:
					t = "+"
				case diff.LineTypeDeletion:
					t = "-"
				default:
					continue
				}
				fileLines = append(fileLines, diff.DiffLineInfo{
					N: line.NewLineNo,
					O: line.OldLineNo,
					T: t,
				})
			}
			if len(fileLines) > 0 {
				lineMapByFile[pf.NewPath] = fileLines
			}
		}
		lineMapJSON, err := json.Marshal(lineMapByFile)
		if err != nil {
			zap.L().Warn("marshal line_map failed",
				zap.Error(err),
				zap.Uint("task_id", task.ID),
			)
		} else {
			meta.LineMapJSON = string(lineMapJSON)
		}
	}

	// 3. 写入数据库（使用 FirstOrCreate 避免并发写入或重试时产生重复记录）
	if model.DB == nil {
		return nil, fmt.Errorf("model.DB is nil, cannot persist mr_diff_meta")
	}
	// 先尝试查找是否已存在记录
	var existing model.MRDiffMeta
	res := model.DB.Where("task_id = ?", task.ID).First(&existing)
	if res.Error == nil {
		// 已存在：执行更新（幂等覆盖）
		updates := map[string]interface{}{
			"project_id":    meta.ProjectID,
			"mri_id":        meta.MRIID,
			"object_key":    meta.ObjectKey,
			"base_sha":      meta.BaseSha,
			"head_sha":      meta.HeadSha,
			"start_sha":     meta.StartSha,
			"line_map_json": meta.LineMapJSON,
		}
		if err := model.DB.Model(&existing).Updates(updates).Error; err != nil {
			return nil, fmt.Errorf("update existing mr_diff_meta failed: %w", err)
		}
		meta.ID = existing.ID
		return meta, nil
	}
	// 不存在：创建新记录
	if err := model.DB.Create(meta).Error; err != nil {
		return nil, fmt.Errorf("create mr_diff_meta failed: %w", err)
	}

	return meta, nil
}

// FetchDiff 根据 task_id 获取 diff（优先从对象存储拉取，fallback 到数据库原始 diff）
func (ds *DiffStore) FetchDiff(ctx context.Context, taskID uint) (string, error) {
	var meta model.MRDiffMeta
	if err := model.DB.Where("task_id = ?", taskID).First(&meta).Error; err != nil {
		return "", fmt.Errorf("mr_diff_meta not found for task %d: %w", taskID, err)
	}

	if meta.ObjectKey != "" && ds.storage != nil {
		data, err := ds.storage.Get(ctx, meta.ObjectKey)
		if err != nil {
			zap.L().Warn("fetch diff from object storage failed",
				zap.Error(err),
				zap.String("key", meta.ObjectKey),
			)
			// fallback：返回空，由调用方处理
			return "", fmt.Errorf("get diff from object storage: %w", err)
		}
		return string(data), nil
	}

	return "", fmt.Errorf("no object key or storage provider for task %d", taskID)
}

// FetchMeta 仅获取 mr_diff_meta 记录（不拉取原始 diff）
func (ds *DiffStore) FetchMeta(taskID uint) (*model.MRDiffMeta, error) {
	var meta model.MRDiffMeta
	if err := model.DB.Where("task_id = ?", taskID).First(&meta).Error; err != nil {
		return nil, fmt.Errorf("mr_diff_meta not found for task %d: %w", taskID, err)
	}
	return &meta, nil
}

// DeleteDiff 删除对象存储中的 diff 和数据库索引（幂等：记录不存在不报错）
func (ds *DiffStore) DeleteDiff(ctx context.Context, taskID uint) error {
	var meta model.MRDiffMeta
	result := model.DB.Where("task_id = ?", taskID).First(&meta)
	if result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			zap.L().Info("mr_diff_meta not found for delete, treating as success",
				zap.Uint("task_id", taskID),
			)
			return nil
		}
		return fmt.Errorf("fetch mr_diff_meta failed for task %d: %w", taskID, result.Error)
	}

	if meta.ObjectKey != "" && ds.storage != nil {
		if err := ds.storage.Delete(ctx, meta.ObjectKey); err != nil {
			zap.L().Warn("delete diff from object storage failed",
				zap.Error(err),
				zap.String("key", meta.ObjectKey),
			)
		}
	}

	if err := model.DB.Delete(&meta).Error; err != nil {
		return fmt.Errorf("delete mr_diff_meta failed: %w", err)
	}
	return nil
}

func (ds *DiffStore) buildObjectKey(task *model.Task) string {
	return fmt.Sprintf("%sproject_%d/mr_%d/task_%d.diff",
		ds.config.Prefix,
		task.ProjectID,
		task.MRMergeID,
		task.ID,
	)
}
