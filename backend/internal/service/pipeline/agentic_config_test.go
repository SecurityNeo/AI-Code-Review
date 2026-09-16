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
				Enabled:           false,
				MaxRounds:         5,
				MaxTools:          8,
				Temperature:       0.1,
				BudgetMultiplier:  1.2,
				MaxMessages:       12, // max_tools(8) + 4
				HistoryTokenLimit: 80000,
			},
		},
		{
			name: "basic agentic enabled",
			cfg:  makeCfg(`{"batch_review_frame":{"agentic_mode":true,"agentic_max_rounds":3,"agentic_max_tools":5}}`),
			want: AgenticConfig{
				Enabled:           true,
				MaxRounds:         3,
				MaxTools:          5,
				Temperature:       0.1,
				BudgetMultiplier:  1.2,
				MaxMessages:       9, // max_tools(5) + 4
				HistoryTokenLimit: 80000,
			},
		},
		{
			name: "all fields custom",
			cfg:  makeCfg(`{"batch_review_frame":{"agentic_mode":true,"agentic_max_rounds":10,"agentic_max_tools":15,"agentic_temperature":0.7,"agentic_budget_multiplier":2.0}}`),
			want: AgenticConfig{
				Enabled:           true,
				MaxRounds:         10,
				MaxTools:          15,
				Temperature:       0.7,
				BudgetMultiplier:  2.0,
				MaxMessages:       19, // max_tools(15) + 4
				HistoryTokenLimit: 80000,
			},
		},
		{
			name: "explicit max_messages and history_token_limit",
			cfg:  makeCfg(`{"batch_review_frame":{"agentic_mode":true,"agentic_max_tools":8,"agentic_max_messages":30,"agentic_history_token_limit":120000}}`),
			want: AgenticConfig{
				Enabled:           true,
				MaxRounds:         5,
				MaxTools:          8,
				Temperature:       0.1,
				BudgetMultiplier:  1.2,
				MaxMessages:       30,
				HistoryTokenLimit: 120000,
			},
		},
		{
			name: "zero values ignored (use defaults)",
			cfg:  makeCfg(`{"batch_review_frame":{"agentic_mode":false,"agentic_max_rounds":0,"agentic_max_tools":0,"agentic_temperature":0,"agentic_budget_multiplier":0,"agentic_max_messages":0,"agentic_history_token_limit":0}}`),
			want: AgenticConfig{
				Enabled:           false,
				MaxRounds:         5,
				MaxTools:          8,
				Temperature:       0.0, // 0 is a valid temperature, not treated as "zero => default"
				BudgetMultiplier:  1.2, // 0 triggers >0 check, fallback to default
				MaxMessages:       12,
				HistoryTokenLimit: 80000,
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
			if got.MaxMessages != tt.want.MaxMessages {
				t.Errorf("MaxMessages = %d, want %d", got.MaxMessages, tt.want.MaxMessages)
			}
			if got.HistoryTokenLimit != tt.want.HistoryTokenLimit {
				t.Errorf("HistoryTokenLimit = %d, want %d", got.HistoryTokenLimit, tt.want.HistoryTokenLimit)
			}
		})
	}
}

// ========== defaultAgenticMaxMessages ==========

func TestDefaultAgenticMaxMessages(t *testing.T) {
	tests := []struct {
		maxTools int
		want     int
	}{
		{8, 12},
		{15, 19},
		{3, 7},
		{1, 6}, // 下限保护
		{0, 6}, // 下限保护
	}
	for _, tt := range tests {
		if got := defaultAgenticMaxMessages(tt.maxTools); got != tt.want {
			t.Errorf("defaultAgenticMaxMessages(%d) = %d, want %d", tt.maxTools, got, tt.want)
		}
	}
}
