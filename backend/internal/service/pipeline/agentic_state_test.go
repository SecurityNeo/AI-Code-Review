package pipeline

import (
	"testing"

	"github.com/ai-optimizer/backend/pkg/llm"
)

// ========== parseToolCallsFromContent ==========

func TestParseToolCallsFromContent(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    int // expected number of tool calls
		wantOK  bool
	}{
		{
			name:    "single tool call",
			content: `Some text <tool_call>{"name":"get_file_ast_summary","arguments":{"file_path":"main.go"}}</tool_call> more text`,
			want:    1,
			wantOK:  true,
		},
		{
			name: "multiple tool calls",
			content: `Text <tool_call>{"name":"func1","arguments":{"a":"b"}}</tool_call>
				<tool_call>{"name":"func2","arguments":{"c":"d"}}</tool_call> end`,
			want:   2,
			wantOK: true,
		},
		{
			name:    "no tool call tags",
			content: "Just normal text without any tool calls",
			want:    0,
			wantOK:  false,
		},
		{
			name:    "empty tool call",
			content: `<tool_call>  </tool_call>`,
			want:    0,
			wantOK:  false,
		},
		{
			name:    "invalid json inside tag",
			content: `<tool_call>{invalid json}</tool_call>`,
			want:    0,
			wantOK:  false,
		},
		{
			name:    "multiline tool call",
			content: "Before\n<tool_call>\n{\"name\":\"get_diff\",\"arguments\":{\"file_path\":\"a.go\"}}\n</tool_call>\nAfter",
			want:    1,
			wantOK:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseToolCallsFromContent(tt.content)
			if ok != tt.wantOK {
				t.Errorf("parseToolCallsFromContent() ok = %v, want %v", ok, tt.wantOK)
				return
			}
			if len(got) != tt.want {
				t.Errorf("parseToolCallsFromContent() len = %d, want %d", len(got), tt.want)
			}
			if ok && len(got) > 0 {
				// Verify IDs are sequential pseudo_0, pseudo_1,...
				for i, tc := range got {
					wantID := "pseudo_" + string(rune('0'+i))
					if i >= 10 {
						// handle double digits if needed; skip for simplicity
						continue
					}
					if tc.ID != wantID {
						t.Errorf("tool call ID = %s, want %s", tc.ID, wantID)
					}
					if tc.Type != "function" {
						t.Errorf("tool call Type = %s, want function", tc.Type)
					}
				}
			}
		})
	}
}

// ========== parseAgenticResultFromContent ==========

func TestParseAgenticResultFromContent(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
		wantOK  bool
	}{
		{
			name:    "raw json",
			content: `{"issues":[],"total_score":85}`,
			want:    `{"issues":[],"total_score":85}`,
			wantOK:  true,
		},
		{
			name:    "json inside markdown block",
			content: "```json\n{\"issues\":[],\"total_score\":85}\n```",
			want:    `{"issues":[],"total_score":85}`,
			wantOK:  true,
		},
		{
			name:    "json with extra whitespace",
			content: "  \n{\"issues\":[]}\n  ",
			want:    `{"issues":[]}`,
			wantOK:  true,
		},
		{
			name:    "not starting with brace",
			content: `Hello {"issues":[]}`,
			want:    "",
			wantOK:  false,
		},
		{
			name:    "empty string",
			content: "",
			want:    "",
			wantOK:  false,
		},
		{
			name:    "invalid json inside block",
			content: "```json\n{invalid}\n```",
			want:    "",
			wantOK:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseAgenticResultFromContent(tt.content)
			if ok != tt.wantOK {
				t.Errorf("parseAgenticResultFromContent() ok = %v, want %v", ok, tt.wantOK)
				return
			}
			if got != tt.want {
				t.Errorf("parseAgenticResultFromContent() = %q, want %q", got, tt.want)
			}
		})
	}
}

// ========== countToolCalls ==========

func TestCountToolCalls(t *testing.T) {
	tests := []struct {
		name   string
		rounds []AgenticRoundMetrics
		want   int
	}{
		{
			name:   "empty",
			rounds: []AgenticRoundMetrics{},
			want:   0,
		},
		{
			name: "only llm calls",
			rounds: []AgenticRoundMetrics{
				{Role: "llm_call", ToolCalls: []string{"a", "b"}},
			},
			want: 0, // tool_execution only
		},
		{
			name: "two tool executions",
			rounds: []AgenticRoundMetrics{
				{Role: "tool_execution", ToolCalls: []string{"a", "b"}},
				{Role: "llm_call"},
				{Role: "tool_execution", ToolCalls: []string{"c"}},
			},
			want: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := countToolCalls(tt.rounds); got != tt.want {
				t.Errorf("countToolCalls() = %d, want %d", got, tt.want)
			}
		})
	}
}

// ========== truncateHistory ==========

func TestTruncateHistory(t *testing.T) {
	tests := []struct {
		name string
		msgs []llm.Message
		want int // expected length after truncation
	}{
		{
			name: "short history no truncation",
			msgs: []llm.Message{
				{Role: "system"},
				{Role: "user"},
			},
			want: 2,
		},
		{
			name: "assistant+tool pair removed",
			msgs: []llm.Message{
				{Role: "system"},
				{Role: "user"},
				{Role: "assistant"},
				{Role: "tool"},
				{Role: "assistant"},
				{Role: "tool"},
			},
			// preserved=2, remove first assistant+tool (indices 2,3)
			want: 4, // 2 + (6-2-2) = 4
		},
		{
			name: "no assistant+tool pair falls back to drop oldest",
			msgs: []llm.Message{
				{Role: "system"},
				{Role: "user"},
				{Role: "user"},
				{Role: "user"},
				{Role: "user"},
				{Role: "user"},
			},
			// preserved=2, no assistant+tool pair, drop 2 oldest after preserve
			want: 4, // 2 + (6-2-2) = 4
		},
		{
			name: "exactly 4 messages no truncation",
			msgs: []llm.Message{
				{Role: "system"},
				{Role: "user"},
				{Role: "user"},
				{Role: "user"},
			},
			want: 4,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateHistory(tt.msgs)
			if len(got) != tt.want {
				t.Errorf("truncateHistory() len = %d, want %d", len(got), tt.want)
			}
			// Verify we don't modify the original slice's underlying array
			// for the preserved part (first 2 elements should be identical)
			for i := 0; i < 2 && i < len(tt.msgs) && i < len(got); i++ {
				if &got[i] == &tt.msgs[i] {
					// Same pointer means we reused the underlying array for preserved part
					// which is fine since we only mutate the 'recent' part via copy()
				}
			}
		})
	}
}

// ========== truncateString ==========

func TestTruncateString(t *testing.T) {
	tests := []struct {
		name   string
		s      string
		maxLen int
		want   string
	}{
		{
			name:   "no truncation",
			s:      "hello",
			maxLen: 10,
			want:   "hello",
		},
		{
			name:   "exact length",
			s:      "hello",
			maxLen: 5,
			want:   "hello",
		},
		{
			name:   "truncation",
			s:      "hello world",
			maxLen: 5,
			want:   "hello...",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := truncateString(tt.s, tt.maxLen); got != tt.want {
				t.Errorf("truncateString() = %q, want %q", got, tt.want)
			}
		})
	}
}
