package pipeline

import (
	"context"
	"strings"
	"testing"

	"github.com/ai-optimizer/backend/internal/model"
)

// 简单的 mock ObjectStorage 用于测试
type mockStorage struct {
	putCalled    bool
	getCalled    bool
	deleteCalled bool
	lastKey      string
	lastData     []byte
}

func (m *mockStorage) Put(ctx context.Context, key string, data []byte) (string, error) {
	m.putCalled = true
	m.lastKey = key
	m.lastData = data
	return "http://mock-url/" + key, nil
}

func (m *mockStorage) Get(ctx context.Context, key string) ([]byte, error) {
	m.getCalled = true
	m.lastKey = key
	return m.lastData, nil
}

func (m *mockStorage) Delete(ctx context.Context, key string) error {
	m.deleteCalled = true
	m.lastKey = key
	return nil
}

func TestDiffStoreBuildObjectKey(t *testing.T) {
	ds := NewDiffStore(DefaultDiffStoreConfig, nil)
	task := &model.Task{
		ID:        1234,
		ProjectID: 42,
		MRMergeID: 100,
	}
	key := ds.buildObjectKey(task)
	if key == "" {
		t.Fatal("buildObjectKey returned empty")
	}
	// 验证 key 格式包含必要字段
	if !strings.Contains(key, "project_42") {
		t.Error("key should contain project_id")
	}
	if !strings.Contains(key, "mr_100") {
		t.Error("key should contain mr_iid")
	}
	if !strings.Contains(key, "task_1234") {
		t.Error("key should contain task_id")
	}
	if !strings.HasSuffix(key, ".diff") {
		t.Error("key should end with .diff")
	}
}
