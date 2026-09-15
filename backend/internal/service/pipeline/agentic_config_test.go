package pipeline

import (
	"testing"

	"github.com/ai-optimizer/backend/internal/model"
)

// ========== ParseAgenticConfig ==========

func TestParseAgenticConfig(t *testing.T) {
	// Helper to build a ReviewAgentConfig with raw stage_configs JSON
	makeCfg := func(raw string) model.ReviewAgentConfig {
		return model.ReviewAgentConfig{
			StageConfigs: raw,
		}
	}

	tests := []struct {
		name string
		cfg  model.ReviewAgentConfig
		want AgenticConfig
	}{
		{
			name: "empty config uses defaults",
			cfg:  model.ReviewAgentConfig{},
			want: AgenticConfig{
				Enabled:          false,
				MaxRounds:        5,
				MaxTools:         8,
				Temperature:      0.1,
				BudgetMultiplier: 1.2,
			},
		},
		{
			name: "basic agentic enabled",
			cfg: makeCfg(`{"batch_review_frame":{"agentic_mode":true,"agentic_max_rounds":3,"agentic_max_tools":5}}`),
			want: AgenticConfig{
				Enabled:          true,
				MaxRounds:        3,
				MaxTools:         5,
				Temperature:      0.1,
				BudgetMultiplier: 1.2,
			},
		},
		{
			name: "all fields custom",
			cfg: makeCfg(`{"batch_review_frame":{"agentic_mode":true,"agentic_max_rounds":10,"agentic_max_tools":15,"agentic_temperature":0.7,"agentic_budget_multiplier":2.0}}`),
			want: AgenticConfig{
				Enabled:          true,
				MaxRounds:        10,
				MaxTools:         15,
				Temperature:      0.7,
				BudgetMultiplier: 2.0,
			},
		},
		{
			name: "zero values ignored (use defaults)",
			cfg: makeCfg(`{"batch_review_frame":{"agentic_mode":false,"agentic_max_rounds":0,"agentic_max_tools":0,"agentic_temperature":0,"agentic_budget_multiplier":0}}`),
			want: AgenticConfig{
				Enabled:          false,
				MaxRounds:        5,
				MaxTools:         8,
				Temperature:      0.0, // 0 is a valid temperature, not treated as "zero => default"
				BudgetMultiplier: 1.2, // 0 triggers >0 check, fallback to default
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseAgenticConfig(tt.cfg)
			if got.Enabled != tt.want.Enabled {
				t.Errorf("Enabled = %v, want %v", got.Enabled, tt.want.Enabled)
			}
			if got.MaxRounds != tt.want.MaxRounds {
				t.Errorf("MaxRounds = %d, want %d", got.MaxRounds, tt.want.MaxRounds)
			}
			if got.MaxTools != tt.want.MaxTools {
				t.Errorf("MaxTools = %d, want %d", got.MaxTools, tt.want.MaxTools)
			}
			if got.Temperature != tt.want.Temperature {
				t.Errorf("Temperature = %f, want %f", got.Temperature, tt.want.Temperature)
			}
			if got.BudgetMultiplier != tt.want.BudgetMultiplier {
				t.Errorf("BudgetMultiplier = %f, want %f", got.BudgetMultiplier, tt.want.BudgetMultiplier)
			}
		})
	}
}
