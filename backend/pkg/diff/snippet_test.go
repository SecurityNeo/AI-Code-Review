package diff

import (
	"strings"
	"testing"
)

func TestStripLineAnnotations_Context(t *testing.T) {
	// 注入格式：注解 + 分隔空格 + 上下文空格 + 代码
	in := "[new94|old74]  // comment"
	if got, want := StripLineAnnotations(in), "// comment"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestStripLineAnnotations_Addition(t *testing.T) {
	in := "[new96|old-] +x := 1"
	if got, want := StripLineAnnotations(in), "x := 1"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestStripLineAnnotations_Deletion(t *testing.T) {
	in := "[new-|old76] -old := 1"
	if got, want := StripLineAnnotations(in), "old := 1"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestStripLineAnnotations_NoAnnotationUnchanged(t *testing.T) {
	in := "  x := 1\n  y := 2"
	if got := StripLineAnnotations(in); got != in {
		t.Fatalf("got %q, want unchanged %q", got, in)
	}
}

func TestStripLineAnnotations_PrefixAlreadyRemoved(t *testing.T) {
	// 注解存在、但模型已去掉 diff 前缀：只去注解，不误删首字符
	in := "[new96|old-] x := 1"
	if got, want := StripLineAnnotations(in), "x := 1"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestStripLineAnnotations_MultiLineKeepsProblemMarker(t *testing.T) {
	in := "[new94|old74]  a();\n[new95|old75]  b(); <<< 问题所在\n[new96|old-] +  c();"
	got := StripLineAnnotations(in)
	want := "a();\nb(); <<< 问题所在\n  c();"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if !strings.Contains(got, "问题所在") {
		t.Error("<<< 问题所在 marker must be preserved")
	}
	if strings.Contains(got, "[new") {
		t.Error("annotation should be removed")
	}
}

func TestStripLineAnnotations_Idempotent(t *testing.T) {
	in := "[new94|old74]  a();\n[new-|old76] -b();"
	once := StripLineAnnotations(in)
	twice := StripLineAnnotations(once)
	if once != twice {
		t.Fatalf("not idempotent: once=%q twice=%q", once, twice)
	}
}

func TestStripLineAnnotations_IndentationPreserved(t *testing.T) {
	// 上下文行：去掉注解与注解分隔空格、再去除上下文前导空格，保留代码缩进
	in := "[new10|old10]         if (x) {"
	got := StripLineAnnotations(in)
	if !strings.HasPrefix(got, "       if (x) {") {
		t.Fatalf("indentation not as expected: %q", got)
	}
}
