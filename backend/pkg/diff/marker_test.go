package diff

import "testing"

func TestParseSnippetMarkers_Problem(t *testing.T) {
	mk := ParseSnippetMarkers("        doThing(); <<< 问题所在")
	if mk.Kind != MarkerKindProblem {
		t.Fatalf("Kind = %v, want problem", mk.Kind)
	}
	if mk.StartText != "doThing();" {
		t.Errorf("StartText = %q, want %q", mk.StartText, "doThing();")
	}
	if mk.EndText != "" {
		t.Errorf("EndText = %q, want empty", mk.EndText)
	}
}

func TestParseSnippetMarkers_Region(t *testing.T) {
	s := "a(); <<< 问题区域开始\nb();\nc(); <<< 问题区域结束"
	mk := ParseSnippetMarkers(s)
	if mk.Kind != MarkerKindRegion {
		t.Fatalf("Kind = %v, want region", mk.Kind)
	}
	if mk.StartText != "a();" {
		t.Errorf("StartText = %q, want %q", mk.StartText, "a();")
	}
	if mk.EndText != "c();" {
		t.Errorf("EndText = %q, want %q", mk.EndText, "c();")
	}
}

func TestParseSnippetMarkers_None(t *testing.T) {
	mk := ParseSnippetMarkers("a();\nb();")
	if mk.Kind != MarkerKindNone {
		t.Fatalf("Kind = %v, want none", mk.Kind)
	}
}

func TestParseSnippetMarkers_Empty(t *testing.T) {
	mk := ParseSnippetMarkers("")
	if mk.Kind != MarkerKindNone {
		t.Fatalf("Kind = %v, want none", mk.Kind)
	}
}

func TestParseSnippetMarkers_IndicesAndCodeLines(t *testing.T) {
	s := "ctx1\n" + // 0
		"a(); <<< 问题区域开始\n" + // 1
		"b();\n" + // 2
		"c(); <<< 问题区域结束\n" + // 3
		"ctx2" // 4
	mk := ParseSnippetMarkers(s)
	if len(mk.CodeLines) != 5 {
		t.Fatalf("CodeLines len = %d, want 5", len(mk.CodeLines))
	}
	if mk.StartIdx != 1 {
		t.Errorf("StartIdx = %d, want 1", mk.StartIdx)
	}
	if mk.EndIdx != 3 {
		t.Errorf("EndIdx = %d, want 3", mk.EndIdx)
	}
	// 空行不登记，下标仍需正确
	mk2 := ParseSnippetMarkers("x(); <<< 问题所在\n\n\ny();")
	if mk2.StartIdx != 0 || len(mk2.CodeLines) != 2 {
		t.Errorf("blank lines should be skipped: StartIdx=%d codeLines=%v", mk2.StartIdx, mk2.CodeLines)
	}
}

func TestFirstCodeLine(t *testing.T) {
	got := FirstCodeLine("\n   \n  x(); <<< 问题所在\ny();")
	if got != "x();" {
		t.Errorf("FirstCodeLine = %q, want %q", got, "x();")
	}
}
