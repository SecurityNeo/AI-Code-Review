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

func TestDiffStoreStoreDiff_NilTask(t *testing.T) {
	ds := NewDiffStore(DefaultDiffStoreConfig, nil)
	meta, err := ds.StoreDiff(context.Background(), nil, "some diff", nil)
	if meta != nil || err == nil {
		t.Fatalf("expected error for nil task, got meta=%v err=%v", meta, err)
	}
	if !strings.Contains(err.Error(), "task is nil") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestDiffStoreStoreDiff_EmptyDiff(t *testing.T) {
	ds := NewDiffStore(DefaultDiffStoreConfig, nil)
	task := &model.Task{ID: 1, ProjectID: 1, MRMergeID: 1}
	meta, err := ds.StoreDiff(context.Background(), task, "   \n\t   ", nil)
	if err != nil {
		t.Fatalf("expected no error for empty diff, got %v", err)
	}
	if meta != nil {
		t.Error("expected nil meta for empty diff")
	}
}

func TestDiffStoreStoreDiff_Disabled(t *testing.T) {
	ds := NewDiffStore(DiffStoreConfig{Enabled: false}, nil)
	task := &model.Task{ID: 1, ProjectID: 1, MRMergeID: 1}
	meta, err := ds.StoreDiff(context.Background(), task, "some diff", nil)
	if err != nil {
		t.Fatalf("expected no error when disabled, got %v", err)
	}
	if meta != nil {
		t.Error("expected nil meta when disabled")
	}
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
