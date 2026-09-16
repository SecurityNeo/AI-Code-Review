package engine

import (
	"fmt"
	"strings"
	"testing"

	"github.com/ai-optimizer/backend/config"
	"github.com/ai-optimizer/backend/pkg/llm"
)

// ========== NormalizeIssueRanges ==========

func TestNormalizeIssueRanges_SwapInvalid(t *testing.T) {
	// 带区域标记的多行区间被反转 → 交换后保留
	issues := []llm.AIReviewIssue{{
		File: "a.vue", LineStart: 147, LineEnd: 145,
		CodeSnippet: "a(); <<< 问题区域开始\nb(); <<< 问题区域结束",
	}}
	out := NormalizeIssueRanges(issues)
	if out[0].LineStart != 145 || out[0].LineEnd != 147 {
		t.Fatalf("got %d-%d, want 145-147", out[0].LineStart, out[0].LineEnd)
	}
}

func TestNormalizeIssueRanges_AlwaysLegal(t *testing.T) {
	// 无标记的非法区间：至少保证 start<=end
	issues := []llm.AIReviewIssue{{File: "a.vue", LineStart: 147, LineEnd: 145}}
	out := NormalizeIssueRanges(issues)
	if out[0].LineStart > out[0].LineEnd {
		t.Fatalf("invalid range after normalize: %d-%d", out[0].LineStart, out[0].LineEnd)
	}
}

func TestNormalizeIssueRanges_CollapseWithoutRegionMarker(t *testing.T) {
	issues := []llm.AIReviewIssue{{
		File: "a.vue", LineStart: 522, LineEnd: 524, CodeSnippet: "x(); <<< 问题所在",
	}}
	out := NormalizeIssueRanges(issues)
	if out[0].LineStart != 522 || out[0].LineEnd != 522 {
		t.Fatalf("got %d-%d, want 522-522", out[0].LineStart, out[0].LineEnd)
	}
}

func TestNormalizeIssueRanges_KeepNoMarkerMultiLine(t *testing.T) {
	// 无标记的多行 snippet：保留区间（避免误折叠）
	issues := []llm.AIReviewIssue{{
		File: "a.vue", LineStart: 30, LineEnd: 32,
		CodeSnippet: "func A() {\n\tx()\n}",
	}}
	out := NormalizeIssueRanges(issues)
	if out[0].LineStart != 30 || out[0].LineEnd != 32 {
		t.Fatalf("got %d-%d, want 30-32", out[0].LineStart, out[0].LineEnd)
	}
}

func TestNormalizeIssueRanges_KeepRegion(t *testing.T) {
	issues := []llm.AIReviewIssue{{
		File: "a.vue", LineStart: 7, LineEnd: 9,
		CodeSnippet: "a(); <<< 问题区域开始\nb();\nc(); <<< 问题区域结束",
	}}
	out := NormalizeIssueRanges(issues)
	if out[0].LineStart != 7 || out[0].LineEnd != 9 {
		t.Fatalf("got %d-%d, want 7-9", out[0].LineStart, out[0].LineEnd)
	}
}

// ========== ApplyLineCorrections 回归（任务 2272：误校正到重复行） ==========

// buildNewFileDiff 构造新增文件 diff，第 522 行与第 528 行为重复的
// `this.confirmLoading = false;`，问题块位于 527-529。
func buildNewFileDiff() string {
	lines := make([]string, 0, 530)
	for i := 1; i <= 519; i++ {
		lines = append(lines, fmt.Sprintf("filler%d", i))
	}
	lines = append(lines,
		"        request.then((res) => {",          // 520
		"          if (isOk(res)) {",               // 521
		"            this.confirmLoading = false;", // 522
		"          }",                 // 523
		"        }).catch(() => {});", // 524
		"      });",                   // 525
		"    },",                      // 526
		"        }).catch(() => {}).finally(() => {", // 527
		"          this.confirmLoading = false;",     // 528
		"        });",                                // 529
		"      });",                                  // 530
	)
	var sb strings.Builder
	sb.WriteString("diff --git a/p.vue b/p.vue\n--- /dev/null\n+++ b/p.vue\n")
	sb.WriteString(fmt.Sprintf("@@ -0,0 +1,%d @@\n", len(lines)))
	for _, l := range lines {
		sb.WriteString("+" + l + "\n")
	}
	return sb.String()
}

func markerCfg() *config.DiffLineMapConfig {
	return &config.DiffLineMapConfig{
		Enabled:            true,
		InjectLineNumbers:  true,
		UseCorrelator:      true,
		GrayPercent:        0,
		MinFuzzyConfidence: 0.3,
	}
}

func TestApplyLineCorrections_2272Regression(t *testing.T) {
	rawDiff := buildNewFileDiff()
	issues := []llm.AIReviewIssue{{
		File:        "p.vue",
		LineStart:   527,
		LineEnd:     529,
		CodeSnippet: "      }).catch(() => {}).finally(() => {\n        this.confirmLoading = false;\n      }); <<< 问题所在",
	}}

	out := ApplyLineCorrections(issues, rawDiff, markerCfg(), 15)
	if len(out) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(out))
	}
	// 修复前误校正为 522-524；修复后应保持问题块首行并收敛为单行
	if out[0].LineStart != 527 || out[0].LineEnd != 527 {
		t.Fatalf("got %d-%d, want 527-527", out[0].LineStart, out[0].LineEnd)
	}
}

func TestApplyLineCorrections_NoMarkerMultiLinePreserved(t *testing.T) {
	rawDiff := buildNewFileDiff()
	issues := []llm.AIReviewIssue{{
		File: "p.vue", LineStart: 527, LineEnd: 529,
		CodeSnippet: "      }).catch(() => {}).finally(() => {\n        this.confirmLoading = false;\n      });",
	}}
	out := ApplyLineCorrections(issues, rawDiff, markerCfg(), 15)
	if out[0].LineStart != 527 || out[0].LineEnd != 529 {
		t.Fatalf("got %d-%d, want 527-529 (multi-line preserved)", out[0].LineStart, out[0].LineEnd)
	}
}

func TestApplyLineCorrections_SingleLineUnchanged(t *testing.T) {
	rawDiff := buildNewFileDiff()
	issues := []llm.AIReviewIssue{{
		File:        "p.vue",
		LineStart:   522,
		LineEnd:     522,
		CodeSnippet: "            this.confirmLoading = false; <<< 问题所在",
	}}

	out := ApplyLineCorrections(issues, rawDiff, markerCfg(), 15)
	if out[0].LineStart != 522 || out[0].LineEnd != 522 {
		t.Fatalf("got %d-%d, want 522-522", out[0].LineStart, out[0].LineEnd)
	}
}

func TestApplyLineCorrections_NeverInvalidRange(t *testing.T) {
	rawDiff := buildNewFileDiff()
	issues := []llm.AIReviewIssue{{
		File: "p.vue", LineStart: 529, LineEnd: 527, // 非法输入
		CodeSnippet: "      }).catch(() => {}).finally(() => {\n        this.confirmLoading = false;\n      }); <<< 问题所在",
	}}
	out := ApplyLineCorrections(issues, rawDiff, markerCfg(), 15)
	if out[0].LineStart > out[0].LineEnd {
		t.Fatalf("invalid range after correction: %d-%d", out[0].LineStart, out[0].LineEnd)
	}
}
