package diff

import (
	"strconv"
	"strings"
	"testing"
)

// 构造一个"新增文件"，其中同一 3 行代码块出现两次（第 7-9 行与第 12-14 行），
// 用于验证块匹配 + 就近消歧，以及"标记行匹配"不再命中重复行。
func makeDuplicateBlockFile() *ParsedDiffFile {
	content := []string{
		"<template>",   // 1
		"  <div/>",     // 2
		"</template>",  // 3
		"<script>",     // 4
		"  methods: {", // 5
		"    save() {", // 6
		"      }).catch(() => {}).finally(() => {", // 7
		"        this.confirmLoading = false;",     // 8
		"      });",                                // 9
		"    },",                                   // 10
		"    other() {",                            // 11
		"      }).catch(() => {}).finally(() => {", // 12
		"        this.confirmLoading = false;",     // 13
		"      });",                                // 14
		"    },",                                   // 15
		"  }",                                      // 16
	}
	var sb strings.Builder
	sb.WriteString("diff --git a/p.vue b/p.vue\n--- /dev/null\n+++ b/p.vue\n")
	sb.WriteString("@@ -0,0 +1," + strconv.Itoa(len(content)) + " @@\n")
	for _, l := range content {
		sb.WriteString("+" + l + "\n")
	}
	parser := NewParser()
	files, _ := parser.Parse(sb.String())
	if len(files) != 1 {
		panic("expected 1 file")
	}
	return files[0]
}

const dupSnippetProblem = "      }).catch(() => {}).finally(() => {\n        this.confirmLoading = false;\n      }); <<< 问题所在"
const dupSnippetRegion = "      }).catch(() => {}).finally(() => { <<< 问题区域开始\n        this.confirmLoading = false;\n      }); <<< 问题区域结束"

// 锚点落在第一个匹配块内 → 保留模型行号（不漂移）
func TestCorrectIssue_BlockAnchorFirst(t *testing.T) {
	file := makeDuplicateBlockFile()
	lc := NewLineCorrelator(map[string]*ParsedDiffFile{"p.vue": file})
	res := lc.CorrectIssue("p.vue", 7, 9, dupSnippetProblem)
	if res.NewStart != 7 || res.NewEnd != 7 {
		t.Fatalf("got %d-%d, want 7-7", res.NewStart, res.NewEnd)
	}
	if res.MatchType != MatchTypeExact {
		t.Errorf("MatchType = %v, want exact block match", res.MatchType)
	}
}

// 锚点落在第二个匹配块内 → 就近消歧到第二个块
func TestCorrectIssue_BlockAnchorSecond(t *testing.T) {
	file := makeDuplicateBlockFile()
	lc := NewLineCorrelator(map[string]*ParsedDiffFile{"p.vue": file})
	res := lc.CorrectIssue("p.vue", 12, 12, dupSnippetProblem)
	if res.NewStart != 12 {
		t.Fatalf("got %d, want 12", res.NewStart)
	}
}

// 模型行号偏离匹配块 → 以块起始行为准
func TestCorrectIssue_AnchorOutOfBlock(t *testing.T) {
	file := makeDuplicateBlockFile()
	lc := NewLineCorrelator(map[string]*ParsedDiffFile{"p.vue": file})
	res := lc.CorrectIssue("p.vue", 3, 3, dupSnippetProblem)
	if res.NewStart != 7 {
		t.Fatalf("got %d, want 7 (nearest block)", res.NewStart)
	}
}

// 区域标记 → 保留多行区间
func TestCorrectIssue_RegionMarkers(t *testing.T) {
	file := makeDuplicateBlockFile()
	lc := NewLineCorrelator(map[string]*ParsedDiffFile{"p.vue": file})
	res := lc.CorrectIssue("p.vue", 7, 9, dupSnippetRegion)
	if res.MarkerKind != MarkerKindRegion {
		t.Fatalf("MarkerKind = %v, want region", res.MarkerKind)
	}
	if res.NewStart != 7 || res.NewEnd != 9 {
		t.Fatalf("got %d-%d, want 7-9", res.NewStart, res.NewEnd)
	}
}

// S1：跨 hunk 间隙的"伪连续块"应被拒绝
func TestFindBlockNearest_RejectsCrossHunkGap(t *testing.T) {
	raw := "diff --git a/x.go b/x.go\n--- a/x.go\n+++ b/x.go\n" +
		"@@ -1,2 +1,2 @@\n alpha\n-beta\n+beta2\n" +
		"@@ -50,2 +50,2 @@\n gamma\n-delta\n+delta2\n"
	parser := NewParser()
	files, _ := parser.Parse(raw)
	if len(files) != 1 {
		t.Fatal("expected 1 file")
	}
	lc := NewLineCorrelator(map[string]*ParsedDiffFile{"x.go": files[0]})
	// beta2(新2) 与 gamma(新50) 在 cand 中相邻，但新文件行号不连续 → 必须拒绝
	if _, _, ok := lc.findBlockNearest(files[0], []string{"beta2", "gamma"}, 2); ok {
		t.Fatal("cross-hunk non-contiguous block should be rejected")
	}
}

// S2：区域结束标记之后还有上下文行时，NewEnd 应取标记行而非块末行
func TestCorrectIssue_RegionEndFromMarkerNotBlockEnd(t *testing.T) {
	file := makeDuplicateBlockFile()
	lc := NewLineCorrelator(map[string]*ParsedDiffFile{"p.vue": file})
	// 第 7-10 行为 4 行，其中区域结束标记在第 9 行，第 10 行是 trailing context
	snippet := "      }).catch(() => {}).finally(() => { <<< 问题区域开始\n" +
		"        this.confirmLoading = false;\n" +
		"      }); <<< 问题区域结束\n" +
		"    },"
	res := lc.CorrectIssue("p.vue", 7, 9, snippet)
	if res.NewStart != 7 || res.NewEnd != 9 {
		t.Fatalf("got %d-%d, want 7-9 (end from marker, not block end 10)", res.NewStart, res.NewEnd)
	}
}

// 无标记的多行 snippet：保留模型给出的区间（不折叠为单行）
func TestCorrectIssue_NoMarkerPreservesRange(t *testing.T) {
	file := makeDuplicateBlockFile()
	lc := NewLineCorrelator(map[string]*ParsedDiffFile{"p.vue": file})
	snippet := "      }).catch(() => {}).finally(() => {\n        this.confirmLoading = false;\n      });"
	res := lc.CorrectIssue("p.vue", 7, 9, snippet)
	if res.NewStart != 7 || res.NewEnd != 9 {
		t.Fatalf("got %d-%d, want 7-9", res.NewStart, res.NewEnd)
	}
}

// 单行问题：去标记后可精确匹配（标记不再污染 exact）
func TestCorrectIssue_SingleLineExactAfterMarkerStrip(t *testing.T) {
	file := makeDuplicateBlockFile()
	lc := NewLineCorrelator(map[string]*ParsedDiffFile{"p.vue": file})
	res := lc.CorrectIssue("p.vue", 99, 99, "  }); <<< 问题所在")
	if res.MatchType != MatchTypeExact {
		t.Fatalf("MatchType = %v, want exact", res.MatchType)
	}
	if res.NewStart != res.NewEnd {
		t.Fatalf("single-line issue should collapse, got %d-%d", res.NewStart, res.NewEnd)
	}
}
