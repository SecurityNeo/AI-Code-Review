package engine

import (
	"testing"

	"github.com/ai-optimizer/backend/pkg/gitlab"
)

func TestFileMatcherShouldSkip_Go(t *testing.T) {
	cfg := FileFilterConfig{
		EnableDefaultFilters: true,
		ExcludePatterns:      []string{},
		IncludePatterns:      []string{"vendor/mycompany/**"},
	}
	m := NewFileMatcher(cfg, "golang")

	tests := []struct {
		path     string
		expected bool
	}{
		// 默认 Go 规则（排除所有依赖文件，dependency_scan 直接从 repo_dir 读取）
		{"go.mod", true},            // 依赖清单，排除
		{"go.sum", true},            // 锁文件，排除
		{"go.work", true},
		{"vendor/github.com/foo/bar.go", true},
		{"vendor/github.com/foo/bar/baz.go", true},
		// 白名单覆盖
		{"vendor/mycompany/lib.go", false},
		{"vendor/mycompany/pkg/util.go", false},
		// 通用规则
		{".gitignore", true},
		{".idea/workspace.xml", true},
		{"app.log", true},
		// 应保留的文件
		{"main.go", false},
		{"internal/service/user.go", false},
		{"cmd/main.go", false},
		{"go.mod.bak", false}, // 不是精确匹配 go.mod
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := m.ShouldSkip(tt.path)
			if got != tt.expected {
				t.Errorf("ShouldSkip(%q) = %v, want %v", tt.path, got, tt.expected)
			}
		})
	}
}

func TestFileMatcherShouldSkip_NodeJS(t *testing.T) {
	cfg := FileFilterConfig{
		EnableDefaultFilters: true,
	}
	m := NewFileMatcher(cfg, "nodejs")

	tests := []struct {
		path     string
		expected bool
	}{
		{"node_modules/lodash/index.js", true},
		{"node_modules/react/package.json", true},
		{"package-lock.json", true},
		{"yarn.lock", true},
		{"dist/bundle.js", true},
		{".next/server/pages/index.js", true},
		{"src/index.js", false},
		{"README.md", false},      // *.md 不在默认排除规则中
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := m.ShouldSkip(tt.path)
			if got != tt.expected {
				t.Errorf("ShouldSkip(%q) = %v, want %v", tt.path, got, tt.expected)
			}
		})
	}
}

func TestFileMatcherShouldSkip_Disabled(t *testing.T) {
	cfg := FileFilterConfig{
		EnableDefaultFilters: false,
		ExcludePatterns:      []string{"*.log"},
	}
	m := NewFileMatcher(cfg, "golang")

	// 默认规则关闭，go.mod 不应被过滤
	if m.ShouldSkip("go.mod") {
		t.Error("默认规则关闭时不应过滤 go.mod")
	}

	// 但自定义排除规则仍应生效
	if !m.ShouldSkip("app.log") {
		t.Error("自定义排除规则 *.log 应生效")
	}
}

func TestFilterDiffFiles(t *testing.T) {
	files := []gitlab.DiffFile{
		{NewPath: "main.go"},
		{NewPath: "go.mod"},
		{NewPath: "vendor/github.com/foo/bar.go"},
		{NewPath: "README.md"},
		{NewPath: "internal/service/user.go"},
		{NewPath: "go.sum"},
	}

	cfg := DefaultFileFilterConfig()
	cfg.EnableDefaultFilters = true
	m := NewFileMatcher(cfg, "golang")
	filtered, stats := FilterDiffFiles(files, m)

	if stats.Total != 6 {
		t.Errorf("Total = %d, want 6", stats.Total)
	}
	// go.mod 和 go.sum 均在默认排除规则中
	// 保留：main.go, README.md, internal/service/user.go (3个)
	// 过滤：go.mod, vendor/... , go.sum (3个)
	if stats.Kept != 3 {
		t.Errorf("Kept = %d, want 3", stats.Kept)
	}
	if stats.Filtered != 3 {
		t.Errorf("Filtered = %d, want 3", stats.Filtered)
	}
	if len(filtered) != 3 {
		t.Errorf("len(filtered) = %d, want 3", len(filtered))
	}

	// 检查保留的文件
	expectedKept := map[string]bool{
		"main.go":                false,
		"internal/service/user.go": false,
		"README.md":              false,
	}
	for _, f := range filtered {
		expectedKept[f.NewPath] = true
	}
	for path, found := range expectedKept {
		if !found {
			t.Errorf("应保留文件 %q 被过滤了", path)
		}
	}
}

func TestFilterDiffFiles_Empty(t *testing.T) {
	cfg := DefaultFileFilterConfig()
	m := NewFileMatcher(cfg, "golang")
	filtered, stats := FilterDiffFiles(nil, m)

	if stats.Total != 0 || stats.Kept != 0 || stats.Filtered != 0 {
		t.Errorf("空列表应返回零值统计，got: %+v", stats)
	}
	if len(filtered) != 0 {
		t.Errorf("空列表应返回空切片，got: %v", filtered)
	}
}

func TestFilterDiffFiles_OldPath(t *testing.T) {
	// 测试 NewPath 为空时回退到 OldPath 的场景
	files := []gitlab.DiffFile{
		{NewPath: "", OldPath: "go.sum"},
		{NewPath: "", OldPath: "main.go"},
	}

	cfg := DefaultFileFilterConfig()
	cfg.EnableDefaultFilters = true
	m := NewFileMatcher(cfg, "golang")
	_, stats := FilterDiffFiles(files, m)

	if stats.Total != 2 {
		t.Errorf("Total = %d, want 2", stats.Total)
	}
	// go.sum 被过滤，main.go 保留
	if stats.Kept != 1 || stats.Filtered != 1 {
		t.Errorf("Kept=%d Filtered=%d, want Kept=1 Filtered=1", stats.Kept, stats.Filtered)
	}
}

func TestMarshalFilterStats(t *testing.T) {
	stats := FilterStats{
		Total:         50,
		Kept:          35,
		Filtered:      15,
		FilteredPaths: []string{"go.mod", "go.sum"},
	}

	jsonStr := MarshalFilterStats(stats)
	if jsonStr == "" || jsonStr == "{}" {
		t.Error("MarshalFilterStats 应返回非空 JSON")
	}

	// 简单检查包含关键字段
	if !contains(jsonStr, "50") || !contains(jsonStr, "go.mod") {
		t.Errorf("JSON 应包含统计信息，got: %s", jsonStr)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsAt(s, substr))
}

func containsAt(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
