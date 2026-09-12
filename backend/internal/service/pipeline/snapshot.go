package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ai-optimizer/backend/internal/model"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// SnapshotStorage 快照存储接口（由上层注入，避免 import cycle）
type SnapshotStorage interface {
	Put(ctx context.Context, key string, data []byte) (string, error)
	Get(ctx context.Context, key string) ([]byte, error)
}

// defaultSnapshotStorage 全局存储实例（由 service.InitObjectStorageProvider 设置）
var defaultSnapshotStorage SnapshotStorage

// SetSnapshotStorage 设置全局快照存储
func SetSnapshotStorage(s SnapshotStorage) {
	defaultSnapshotStorage = s
}

func getSnapshotStorage() SnapshotStorage {
	if defaultSnapshotStorage != nil {
		return defaultSnapshotStorage
	}
	return nil
}

// IsSnapshotStorageEnabled 判断对象存储是否已配置
func IsSnapshotStorageEnabled() bool {
	return defaultSnapshotStorage != nil
}

// SnapshotRef 对象存储引用结构（存入 OutputSnapshot / InputSnapshot）
type SnapshotRef struct {
	StorageType string `json:"_storage_type"`
	URL         string `json:"_url"`
	Key         string `json:"_key"`
	Size        int    `json:"_size"`
	Summary     string `json:"_summary"`
}

// SnapshotManager Pipeline 快照管理器
type SnapshotManager struct {
	storage SnapshotStorage
}

// NewSnapshotManager 创建快照管理器
func NewSnapshotManager() *SnapshotManager {
	return &SnapshotManager{
		storage: getSnapshotStorage(),
	}
}

// SaveInputSnapshot 保存阶段输入快照
func (m *SnapshotManager) SaveInputSnapshot(db *gorm.DB, execID uint, data map[string]interface{}) (string, error) {
	jsonBytes, err := json.Marshal(data)
	if err != nil {
		return "", err
	}

	if m.storage != nil {
		key := fmt.Sprintf("pipeline/executions/%d/input_%d.json", execID, time.Now().Unix())
		url, err := m.storage.Put(context.Background(), key, jsonBytes)
		if err == nil {
			ref := SnapshotRef{
				StorageType: "object_storage",
				URL:         url,
				Key:         key,
				Size:        len(jsonBytes),
				Summary:     generateSummary(data),
			}
			refBytes, _ := json.Marshal(ref)
			db.Model(&model.TaskPipelineExecution{}).
				Where("id = ?", execID).
				Update("input_snapshot", string(refBytes))
			return url, nil
		}
		zap.L().Warn("对象存储写入 input snapshot 失败，回退到数据库",
			zap.Uint("exec_id", execID), zap.Error(err))
	}

	// 对象存储未配置或写入失败，存入数据库
	err = db.Model(&model.TaskPipelineExecution{}).
		Where("id = ?", execID).
		Update("input_snapshot", string(jsonBytes)).Error
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("db://%d", execID), nil
}

// SaveOutputSnapshot 保存阶段输出快照
func (m *SnapshotManager) SaveOutputSnapshot(db *gorm.DB, execID uint, data map[string]interface{}) (string, error) {
	jsonBytes, err := json.Marshal(data)
	if err != nil {
		return "", err
	}

	if m.storage != nil {
		key := fmt.Sprintf("pipeline/executions/%d/output_%d.json", execID, time.Now().Unix())
		url, err := m.storage.Put(context.Background(), key, jsonBytes)
		if err == nil {
			ref := SnapshotRef{
				StorageType: "object_storage",
				URL:         url,
				Key:         key,
				Size:        len(jsonBytes),
				Summary:     generateSummary(data),
			}
			refBytes, _ := json.Marshal(ref)
			db.Model(&model.TaskPipelineExecution{}).
				Where("id = ?", execID).
				Update("output_snapshot", string(refBytes))
			return url, nil
		}
		zap.L().Warn("对象存储写入 output snapshot 失败，回退到数据库",
			zap.Uint("exec_id", execID), zap.Error(err))
	}

	// 对象存储未配置或写入失败，存入数据库
	err = db.Model(&model.TaskPipelineExecution{}).
		Where("id = ?", execID).
		Update("output_snapshot", string(jsonBytes)).Error
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("db://%d", execID), nil
}

// GetOutputSnapshot 读取阶段输出快照
func (m *SnapshotManager) GetOutputSnapshot(exec *model.TaskPipelineExecution) (map[string]interface{}, error) {
	if exec.OutputSnapshot == "" {
		return nil, nil
	}

	// 判断是否是对象存储引用
	var ref SnapshotRef
	if err := json.Unmarshal([]byte(exec.OutputSnapshot), &ref); err == nil && ref.StorageType == "object_storage" {
		data, err := m.storage.Get(context.Background(), ref.Key)
		if err != nil {
			return nil, fmt.Errorf("从对象存储读取快照失败: %w", err)
		}
		var result map[string]interface{}
		if err := json.Unmarshal(data, &result); err != nil {
			return nil, err
		}
		return result, nil
	}

	// 直接从数据库解析
	var result map[string]interface{}
	if err := json.Unmarshal([]byte(exec.OutputSnapshot), &result); err != nil {
		return nil, err
	}
	return result, nil
}

// GetInputSnapshot 读取阶段输入快照
func (m *SnapshotManager) GetInputSnapshot(exec *model.TaskPipelineExecution) (map[string]interface{}, error) {
	if exec.InputSnapshot == "" {
		return nil, nil
	}

	// 判断是否是对象存储引用
	var ref SnapshotRef
	if err := json.Unmarshal([]byte(exec.InputSnapshot), &ref); err == nil && ref.StorageType == "object_storage" {
		data, err := m.storage.Get(context.Background(), ref.Key)
		if err != nil {
			return nil, fmt.Errorf("从对象存储读取 input snapshot 失败: %w", err)
		}
		var result map[string]interface{}
		if err := json.Unmarshal(data, &result); err != nil {
			return nil, err
		}
		return result, nil
	}

	// 直接从数据库解析
	var result map[string]interface{}
	if err := json.Unmarshal([]byte(exec.InputSnapshot), &result); err != nil {
		return nil, err
	}
	return result, nil
}

func generateSummary(data map[string]interface{}) string {
	if status, ok := data["status"].(string); ok {
		return "status: " + status
	}
	return "snapshot"
}
