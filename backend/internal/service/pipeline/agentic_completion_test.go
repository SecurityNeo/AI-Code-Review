package pipeline

import (
	"testing"

	"github.com/ai-optimizer/backend/internal/engine"
	"github.com/ai-optimizer/backend/internal/model"
	"github.com/ai-optimizer/backend/pkg/llm"
)

func TestValidateAgenticCompletion(t *testing.T) {
	cases := []struct {
		name string
		r    *llm.BatchReviewResult
		ok   bool
	}{
		{"has issues", &llm.BatchReviewResult{Issues: []llm.AIReviewIssue{{Severity: "high"}}}, true},
		{"empty + complete + reason", &llm.BatchReviewResult{Completion: "complete", NoIssueReason: "已核对无问题", Issues: []llm.AIReviewIssue{}}, true},
		{"empty + complete without reason", &llm.BatchReviewResult{Completion: "complete", Issues: []llm.AIReviewIssue{}}, false},
		{"empty + need_more_analysis", &llm.BatchReviewResult{Completion: "need_more_analysis", NoIssueReason: "x", Issues: []llm.AIReviewIssue{}}, false},
		{"empty + no completion", &llm.BatchReviewResult{Issues: []llm.AIReviewIssue{}}, false},
		{"nil", nil, false},
	}
	for _, c := range cases {
		ok, reason := validateAgenticCompletion(c.r)
		if ok != c.ok {
			t.Errorf("%s: ok=%v want %v (reason=%q)", c.name, ok, c.ok, reason)
		}
	}
}

func TestParseSubmittedReview(t *testing.T) {
	tc := &llm.ToolCall{Function: llm.ToolCallFunction{
		Name:      submitReviewToolName,
		Arguments: `{"batch_notes":"ok","issues":[],"recommendations":[],"completion":"complete","no_issue_reason":"已核对"}`,
	}}
	r, err := parseSubmittedReview(tc)
	if err != nil {
		t.Fatalf("parseSubmittedReview: %v", err)
	}
	if ok, reason := validateAgenticCompletion(r); !ok {
		t.Fatalf("submitted result should be valid, reason=%q", reason)
	}
	if r.Issues == nil || r.Recommendations == nil {
		t.Fatal("nil slices should be normalized to empty")
	}
}

func TestNudgeToSubmitLimit(t *testing.T) {
	st := &AgenticReviewState{}
	for i := 0; i < maxSubmitNudges; i++ {
		if err := nudgeToSubmit(st, "x"); err != nil {
			t.Fatalf("nudge #%d unexpected err: %v", i+1, err)
		}
	}
	if err := nudgeToSubmit(st, "x"); err == nil {
		t.Fatal("expected limit error after maxSubmitNudges")
	}
}

// 任务 2276 回归：模型输出“还要继续验证”+ 空 issues 的 JSON，必须判定为未完成
func TestRegression2276Incomplete(t *testing.T) {
	content := "{\n\n  \"batch_notes\": \"Let me verify a few more details about the store getters and the media/XSS concerns.\"\n\n  ,\"issues\": []\n\n  ,\"recommendations\": []\n\n  }"
	r, err := engine.ParseBatchReviewResult(content, engine.DefaultDeductScoreConfig())
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if ok, reason := validateAgenticCompletion(r); ok {
		t.Fatalf("2276 output must be judged incomplete, got ok (reason=%q)", reason)
	}
}

func TestTryHandleSubmitReview_AcceptedRecordsAudit(t *testing.T) {
	st := &AgenticReviewState{CurrentRound: 2}
	task := &model.Task{ID: 1}
	valid := &llm.ToolCall{ID: "call1", Function: llm.ToolCallFunction{
		Name:      submitReviewToolName,
		Arguments: `{"batch_notes":"ok","issues":[{"severity":"high","category":"security","file":"a.go","message":"m","suggestion":"s"}],"recommendations":[],"completion":"complete"}`,
	}}
	res, err := tryHandleSubmitReview(st, valid, task)
	if err != nil || res == nil {
		t.Fatalf("valid submit should be accepted: res=%v err=%v", res, err)
	}
	if len(st.SubmitAttempts) != 1 || !st.SubmitAttempts[0].OK {
		t.Fatalf("audit not recorded as ok: %+v", st.SubmitAttempts)
	}
	// 必须记录一条 submit_review 的 tool_execution 事件
	found := false
	for _, r := range st.Rounds {
		if r.Role == "tool_execution" && len(r.ToolResults) > 0 && r.ToolResults[0].ToolName == submitReviewToolName {
			found = true
		}
	}
	if !found {
		t.Fatal("submit tool_execution metric missing")
	}
	// 对话历史应追加 assistant(tool_call) + tool 结果
	if len(st.Messages) != 2 || len(st.Messages[0].ToolCalls) != 1 || st.Messages[1].Role != "tool" {
		t.Fatalf("messages not appended correctly: %+v", st.Messages)
	}
}

func TestTryHandleSubmitReview_InvalidNudgesAndAudits(t *testing.T) {
	st := &AgenticReviewState{CurrentRound: 2}
	task := &model.Task{ID: 1}
	invalid := &llm.ToolCall{ID: "call1", Function: llm.ToolCallFunction{
		Name:      submitReviewToolName,
		Arguments: `{"batch_notes":"x","issues":[],"recommendations":[],"completion":"need_more_analysis"}`,
	}}
	res, err := tryHandleSubmitReview(st, invalid, task)
	if err != nil || res != nil {
		t.Fatalf("invalid submit should nudge (no result, no error): res=%v err=%v", res, err)
	}
	if len(st.SubmitAttempts) != 1 || st.SubmitAttempts[0].OK {
		t.Fatalf("audit should record failure: %+v", st.SubmitAttempts)
	}
	if st.SubmitNudges != 1 {
		t.Fatalf("SubmitNudges = %d, want 1", st.SubmitNudges)
	}
}

func TestRecordSkippedToolCalls(t *testing.T) {
	st := &AgenticReviewState{CurrentRound: 4}
	calls := []llm.ToolCall{
		{ID: "a", Function: llm.ToolCallFunction{Name: "get_diff", Arguments: "{}"}},
		{ID: "b", Function: llm.ToolCallFunction{Name: "get_function_callers", Arguments: "{}"}},
	}
	recordSkippedToolCalls(st, calls, "工具预算已用尽，未执行")

	if len(st.Rounds) != 1 || st.Rounds[0].Role != "tool_execution" || len(st.Rounds[0].ToolResults) != 2 {
		t.Fatalf("skipped metric wrong: %+v", st.Rounds)
	}
	if len(st.Messages) != 3 {
		t.Fatalf("messages = %d, want 3 (assistant + 2 tool)", len(st.Messages))
	}
	if st.Messages[0].Role != "assistant" || len(st.Messages[0].ToolCalls) != 2 {
		t.Fatalf("assistant tool_calls missing: %+v", st.Messages[0])
	}
	if st.Messages[1].Role != "tool" || st.Messages[1].ToolCallID != "a" {
		t.Fatalf("tool result missing: %+v", st.Messages[1])
	}
}
