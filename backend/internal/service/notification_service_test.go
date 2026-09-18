package service

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateBytesRune(t *testing.T) {
	// 不超限：原样返回
	if got := truncateBytesRune("hello", 100, "..."); got != "hello" {
		t.Errorf("got %q, want hello", got)
	}
	// maxBytes<=0：原样返回
	if got := truncateBytesRune("hello", 0, "..."); got != "hello" {
		t.Errorf("maxBytes<=0 should keep original, got %q", got)
	}
	// 超限：截断后长度不超过上限，追加 suffix，且为合法 UTF-8
	s := strings.Repeat("中", 100) // 300 bytes
	got := truncateBytesRune(s, 100, "…")
	if len(got) > 100+len("…") {
		t.Errorf("truncated length = %d, should be <= %d", len(got), 100+len("…"))
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("missing suffix: %q", got)
	}
	if !utf8.ValidString(got) {
		t.Errorf("truncated string is not valid UTF-8")
	}
}

func TestBuildEscalationContent(t *testing.T) {
	// 单条：原样
	if got := buildEscalationContent([]string{"only"}); got != "only" {
		t.Errorf("single item should be returned as-is, got %q", got)
	}
	// 多条（未超上限）：含全部条目，无“其余”提示
	items := []string{"a", "b", "c"}
	got := buildEscalationContent(items)
	if !strings.Contains(got, "您有 3 条") {
		t.Errorf("missing count header: %q", got)
	}
	for _, it := range items {
		if !strings.Contains(got, it) {
			t.Errorf("missing item %q in %q", it, got)
		}
	}
	if strings.Contains(got, "其余") {
		t.Errorf("should not contain 其余 when under limit: %q", got)
	}
}

func TestBuildEscalationContent_Capped(t *testing.T) {
	items := make([]string, 25)
	for i := range items {
		items[i] = "item"
	}
	got := buildEscalationContent(items)
	if !strings.Contains(got, "您有 25 条") {
		t.Errorf("missing total count: %q", got)
	}
	// 最多展示 20 条：分隔符出现 19 次
	if n := strings.Count(got, "\n---\n"); n != maxItemsPerNotification-1 {
		t.Errorf("separator count = %d, want %d", n, maxItemsPerNotification-1)
	}
	if !strings.Contains(got, "其余 5 条") {
		t.Errorf("missing overflow hint: %q", got)
	}
}
