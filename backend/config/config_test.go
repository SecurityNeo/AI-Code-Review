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
}
