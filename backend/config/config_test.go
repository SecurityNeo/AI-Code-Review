package config

import "testing"

func TestDiffLineMapConfig_Validate(t *testing.T) {
	// 总开关关闭但子功能开启 -> 应有警告
	cfg := DiffLineMapConfig{
		Enabled:           false,
		InjectLineNumbers: true,
		UseCorrelator:     true,
		StoreDiffRefs:     true,
	}
	warnings := cfg.Validate()
	if len(warnings) != 3 {
		t.Fatalf("expected 3 warnings, got %d: %v", len(warnings), warnings)
	}

	// 总开关开启 -> 无警告
	cfg2 := DiffLineMapConfig{
		Enabled:           true,
		InjectLineNumbers: true,
		UseCorrelator:     true,
		StoreDiffRefs:     true,
	}
	warnings2 := cfg2.Validate()
	if len(warnings2) != 0 {
		t.Errorf("expected no warnings when enabled, got %v", warnings2)
	}

	// 全部关闭 -> 无警告
	cfg3 := DiffLineMapConfig{}
	warnings3 := cfg3.Validate()
	if len(warnings3) != 0 {
		t.Errorf("expected no warnings when all false, got %v", warnings3)
	}

	// 灰度百分比越界 -> 应有警告（GrayMode 合法，不额外报错）
	cfg4 := DiffLineMapConfig{Enabled: true, GrayPercent: 150, GrayMode: "project_hash"}
	warnings4 := cfg4.Validate()
	if len(warnings4) != 1 {
		t.Fatalf("expected 1 warning for invalid gray_percent, got %d: %v", len(warnings4), warnings4)
	}

	// 灰度模式无效 -> 应有警告
	cfg5 := DiffLineMapConfig{Enabled: true, GrayPercent: 20, GrayMode: "invalid"}
	warnings5 := cfg5.Validate()
	if len(warnings5) != 1 {
		t.Fatalf("expected 1 warning for invalid gray_mode, got %d: %v", len(warnings5), warnings5)
	}
}

func TestDiffLineMapConfig_IsGrayEnabled(t *testing.T) {
	// GrayPercent <= 0 → 全部命中
	cfg0 := DiffLineMapConfig{GrayPercent: 0, GrayMode: "project_hash"}
	if !cfg0.IsGrayEnabled(42) {
		t.Error("gray_percent=0 should always return true")
	}

	// GrayPercent >= 100 → 全部命中
	cfg100 := DiffLineMapConfig{GrayPercent: 100, GrayMode: "project_hash"}
	if !cfg100.IsGrayEnabled(42) {
		t.Error("gray_percent=100 should always return true")
	}

	// project_hash 模式下同一项目始终命中状态一致
	cfg50 := DiffLineMapConfig{GrayPercent: 50, GrayMode: "project_hash"}
	pid := uint(12345)
	first := cfg50.IsGrayEnabled(pid)
	for i := 0; i < 10; i++ {
		if cfg50.IsGrayEnabled(pid) != first {
			t.Error("project_hash mode should be deterministic for same project")
			break
		}
	}

	// 统计：大量随机项目命中比例应在 45%-55% 之间（50% 灰度）
	hit := 0
	total := 1000
	for i := uint(1); i <= uint(total); i++ {
		if cfg50.IsGrayEnabled(i) {
			hit++
		}
	}
	ratio := float64(hit) / float64(total) * 100
	if ratio < 45 || ratio > 55 {
		t.Errorf("gray 50%% hit ratio=%.1f%%, expected 45%%-55%%", ratio)
	}
}
