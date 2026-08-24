package pipeline

import "testing"

func TestDetectLanguageFromPath(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"main.go", "golang"},
		{"Foo.java", "java"},
		{"utils.py", "python"},
		{"chat.js", "javascript"},
		{"chat.jsx", "javascript"},
		{"chat.ts", "typescript"},
		{"chat.tsx", "typescript"},
		{"Component.vue", "javascript"},
		{"app.php", "php"},
		{"app.rb", "ruby"},
		{"main.cpp", "cpp"},
		{"main.cc", "cpp"},
		{"main.h", "cpp"},
		{"readme.md", "unknown"},
		{"", "unknown"},
	}
	for _, c := range cases {
		got := detectLanguageFromPath(c.path)
		if got != c.want {
			t.Errorf("detectLanguageFromPath(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}

func TestParseASTSimple_LanguageDetection(t *testing.T) {
	// JS 文件：export function（小写函数名）
	jsContent := "export function normalizeMessage(raw) { return raw; }\n"
	jsAst := parseASTSimple(jsContent, "src/chat.js")
	if len(jsAst.Functions) == 0 {
		t.Fatal("JS file should be parsed by JavaScript extractor")
	}
	found := false
	for _, fn := range jsAst.Functions {
		if fn.Name == "normalizeMessage" {
			found = true
			if !fn.IsExported {
				t.Fatal("export function should be marked IsExported=true")
			}
		}
	}
	if !found {
		t.Fatalf("expected normalizeMessage in JS AST, got %+v", jsAst.Functions)
	}

	// Go 文件：大写导出 + 小写内部
	goContent := "package main\n\nfunc main() {}\nfunc Exported() {}\n"
	goAst := parseASTSimple(goContent, "main.go")
	var exportedFound, internalFound bool
	for _, fn := range goAst.Functions {
		if fn.Name == "Exported" {
			exportedFound = true
			if !fn.IsExported {
				t.Fatal("Go Exported function should be IsExported=true")
			}
		}
		if fn.Name == "main" {
			internalFound = true
			if fn.IsExported {
				t.Fatal("Go main function should be IsExported=false")
			}
		}
	}
	if !exportedFound {
		t.Fatal("Go file should parse Exported function")
	}
	if !internalFound {
		t.Fatal("Go file should parse main function")
	}
}

func TestImpactAnalysis_UsesIsExportedField(t *testing.T) {
	astCtx := &ASTContext{
		Functions: []FunctionSignature{
			// JS export 函数，名字小写，IsExported=true（来自上游 ASTExtractor）
			{
				Name:       "normalizeMessage",
				IsExported: true,
				FilePath:   "src/chat.js",
				Params:     []string{"raw"},
			},
			// 私有函数，IsExported=false
			{
				Name:       "helper",
				IsExported: false,
				FilePath:   "src/chat.js",
			},
		},
	}
	// crossFileText 必须按 buildCrossFileCallChainText 格式构造，
	// 否则 extractAffectedFiles 无法解析出调用者文件。
	crossFileText := "### 文件: src/chat.js\n**被以下文件调用：**\n- src/other.js:45 callerFunc -> normalizeMessage(raw)\n"
	executor := &ImpactAnalysisExecutor{}
	findings := executor.analyzeFunctionSignatureChanges(
		astCtx,
		crossFileText,
		map[string]string{"src/chat.js": "export function normalizeMessage(raw) {}"},
		map[string]string{"src/chat.js": "export function normalizeMessage() {}"},
	)
	if len(findings) == 0 {
		t.Fatal("exported JS function with lowercase name should generate finding")
	}
	if findings[0].SymbolName != "normalizeMessage" {
		t.Fatalf("expected normalizeMessage, got %s", findings[0].SymbolName)
	}
	if findings[0].BeforeSig == "" {
		t.Fatal("baseline signature should not be empty when parseASTSimple works correctly")
	}
}
