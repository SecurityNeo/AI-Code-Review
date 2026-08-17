package dataflow

import (
	"testing"

	"github.com/ai-optimizer/backend/internal/graph"
	"github.com/ai-optimizer/backend/internal/parser"
)

func TestTaintAnalyzer_Go_SQLInjection(t *testing.T) {
	ast := &parser.UnifiedAST{
		Language: "golang",
		FilePath: "handler.go",
		Functions: []parser.UnifiedFunction{
			{Name: "handleRequest", Location: parser.SourceLocation{File: "handler.go", LineStart: 1}},
		},
		CallSites: []parser.UnifiedCallSite{
			{CallerFunc: "handleRequest", TargetFunc: "Query", ReceiverVar: "c", Arguments: []string{`"name"`}, Location: parser.SourceLocation{LineStart: 5}},
			{CallerFunc: "handleRequest", TargetFunc: "Exec", ReceiverVar: "db", Arguments: []string{`"SELECT * FROM users WHERE name = " + user`}, Location: parser.SourceLocation{LineStart: 10}},
		},
		VarBindings: []parser.UnifiedVarBinding{
			{VarName: "user", CallSite: &parser.UnifiedCallSite{TargetFunc: "Query", ReceiverVar: "c"}},
		},
	}

	analyzer := NewTaintAnalyzer("golang", []*parser.UnifiedAST{ast}, nil)
	result := analyzer.Analyze()

	if len(result.Flows) == 0 {
		t.Fatal("expected at least one taint flow for SQL injection")
	}

	flow := result.Flows[0]
	if flow.Category != SinkSQLInjection {
		t.Errorf("category = %q, want %q", flow.Category, SinkSQLInjection)
	}
	if flow.RiskLevel != "high" {
		t.Errorf("risk level = %q, want high", flow.RiskLevel)
	}
}

func TestTaintAnalyzer_Go_SQLInjection_WithSanitizer(t *testing.T) {
	ast := &parser.UnifiedAST{
		Language: "golang",
		FilePath: "handler.go",
		Functions: []parser.UnifiedFunction{
			{Name: "handleRequest", Location: parser.SourceLocation{File: "handler.go", LineStart: 1}},
		},
		CallSites: []parser.UnifiedCallSite{
			{CallerFunc: "handleRequest", TargetFunc: "Query", ReceiverVar: "c", Arguments: []string{`"name"`}, Location: parser.SourceLocation{LineStart: 5}},
			{CallerFunc: "handleRequest", TargetFunc: "EscapeString", ReceiverVar: "html", Arguments: []string{"user"}, Location: parser.SourceLocation{LineStart: 7}},
			{CallerFunc: "handleRequest", TargetFunc: "Exec", ReceiverVar: "db", Arguments: []string{`"SELECT * FROM users WHERE name = " + sanitized`}, Location: parser.SourceLocation{LineStart: 10}},
		},
		VarBindings: []parser.UnifiedVarBinding{
			{VarName: "user", CallSite: &parser.UnifiedCallSite{TargetFunc: "Query", ReceiverVar: "c"}},
			{VarName: "sanitized", CallSite: &parser.UnifiedCallSite{TargetFunc: "EscapeString", ReceiverVar: "html"}},
		},
	}

	analyzer := NewTaintAnalyzer("golang", []*parser.UnifiedAST{ast}, nil)
	result := analyzer.Analyze()

	for _, flow := range result.Flows {
		if flow.Category == SinkSQLInjection {
			t.Error("expected sanitizer to prevent sql injection flow")
		}
	}
}

func TestTaintAnalyzer_Go_CommandInjection(t *testing.T) {
	ast := &parser.UnifiedAST{
		Language: "golang",
		FilePath: "cmd.go",
		Functions: []parser.UnifiedFunction{
			{Name: "runCommand", Location: parser.SourceLocation{File: "cmd.go", LineStart: 1}},
		},
		CallSites: []parser.UnifiedCallSite{
			{CallerFunc: "runCommand", TargetFunc: "Query", ReceiverVar: "c", Arguments: []string{`"cmd"`}, Location: parser.SourceLocation{LineStart: 3}},
			{CallerFunc: "runCommand", TargetFunc: "Command", ReceiverVar: "exec", Arguments: []string{"userInput"}, Location: parser.SourceLocation{LineStart: 5}},
		},
		VarBindings: []parser.UnifiedVarBinding{
			{VarName: "userInput", CallSite: &parser.UnifiedCallSite{TargetFunc: "Query", ReceiverVar: "c"}},
		},
	}

	analyzer := NewTaintAnalyzer("golang", []*parser.UnifiedAST{ast}, nil)
	result := analyzer.Analyze()

	if len(result.Flows) == 0 {
		t.Fatal("expected at least one taint flow for command injection")
	}

	flow := result.Flows[0]
	if flow.Category != SinkCommandInjection {
		t.Errorf("category = %q, want %q", flow.Category, SinkCommandInjection)
	}
}

func TestTaintAnalyzer_Python_Flask_SQLInjection(t *testing.T) {
	ast := &parser.UnifiedAST{
		Language: "python",
		FilePath: "app.py",
		Functions: []parser.UnifiedFunction{
			{Name: "get_user", Location: parser.SourceLocation{File: "app.py", LineStart: 1}},
		},
		CallSites: []parser.UnifiedCallSite{
			// Python extractor style: TargetPkg=first_id, TargetFunc=last_id
			{CallerFunc: "get_user", TargetFunc: "get", TargetPkg: "request", Arguments: []string{`"id"`}, Location: parser.SourceLocation{LineStart: 3}},
			{CallerFunc: "get_user", TargetFunc: "execute", TargetPkg: "cursor", Arguments: []string{`"SELECT * FROM users WHERE id = " + user_id`}, Location: parser.SourceLocation{LineStart: 5}},
		},
		VarBindings: []parser.UnifiedVarBinding{
			{VarName: "user_id", CallSite: &parser.UnifiedCallSite{TargetFunc: "get", TargetPkg: "request"}},
		},
	}

	analyzer := NewTaintAnalyzer("python", []*parser.UnifiedAST{ast}, nil)
	result := analyzer.Analyze()

	if len(result.Flows) == 0 {
		t.Fatal("expected at least one taint flow for python sql injection")
	}

	flow := result.Flows[0]
	if flow.Category != SinkSQLInjection {
		t.Errorf("category = %q, want %q", flow.Category, SinkSQLInjection)
	}
	if flow.Source.Kind != SourceHTTPParam {
		t.Errorf("source kind = %q, want %q", flow.Source.Kind, SourceHTTPParam)
	}
}

func TestTaintAnalyzer_Java_Spring_SQLInjection(t *testing.T) {
	ast := &parser.UnifiedAST{
		Language: "java",
		FilePath: "UserController.java",
		Functions: []parser.UnifiedFunction{
			{Name: "getUser", Annotations: []string{"@RequestParam"}, Params: []parser.UnifiedParam{{Name: "userId", Type: "String"}}, Location: parser.SourceLocation{File: "UserController.java", LineStart: 1}},
		},
		CallSites: []parser.UnifiedCallSite{
			// Java extractor style: TargetPkg=conn, TargetFunc=createStatement
			{CallerFunc: "getUser", TargetFunc: "createStatement", TargetPkg: "conn", Arguments: []string{`"SELECT * FROM users WHERE id = " + userId`}, Location: parser.SourceLocation{LineStart: 5}},
		},
		VarBindings: []parser.UnifiedVarBinding{
			// Spring 参数注入：userId 从 @RequestParam 来（由 annotateSources 自动识别）
			{VarName: "userId"},
		},
	}

	analyzer := NewTaintAnalyzer("java", []*parser.UnifiedAST{ast}, nil)
	result := analyzer.Analyze()

	if len(result.Flows) == 0 {
		t.Fatal("expected at least one taint flow for java sql injection")
	}

	flow := result.Flows[0]
	if flow.Category != SinkSQLInjection {
		t.Errorf("category = %q, want %q", flow.Category, SinkSQLInjection)
	}
}

func TestTaintAnalyzer_JavaScript_Express_CommandInjection(t *testing.T) {
	ast := &parser.UnifiedAST{
		Language: "javascript",
		FilePath: "server.js",
		Functions: []parser.UnifiedFunction{
			{Name: "execCmd", Location: parser.SourceLocation{File: "server.js", LineStart: 1}},
		},
		CallSites: []parser.UnifiedCallSite{
			{CallerFunc: "execCmd", TargetFunc: "params", ReceiverVar: "req", Arguments: []string{}, Location: parser.SourceLocation{LineStart: 3}},
			{CallerFunc: "execCmd", TargetFunc: "exec", ReceiverVar: "child_process", Arguments: []string{"cmd"}, Location: parser.SourceLocation{LineStart: 5}},
		},
		VarBindings: []parser.UnifiedVarBinding{
			{VarName: "cmd", CallSite: &parser.UnifiedCallSite{TargetFunc: "params", ReceiverVar: "req"}},
		},
	}

	analyzer := NewTaintAnalyzer("javascript", []*parser.UnifiedAST{ast}, nil)
	result := analyzer.Analyze()

	if len(result.Flows) == 0 {
		t.Fatal("expected at least one taint flow for js command injection")
	}

	flow := result.Flows[0]
	if flow.Category != SinkCommandInjection {
		t.Errorf("category = %q, want %q", flow.Category, SinkCommandInjection)
	}
}

func TestTaintAnalyzer_Go_TaintPropagation(t *testing.T) {
	// 测试污点传播路径：user → sql → db.Exec
	ast := &parser.UnifiedAST{
		Language: "golang",
		FilePath: "handler.go",
		Functions: []parser.UnifiedFunction{
			{Name: "handleRequest", Location: parser.SourceLocation{File: "handler.go", LineStart: 1}},
		},
		CallSites: []parser.UnifiedCallSite{
			{CallerFunc: "handleRequest", TargetFunc: "Query", ReceiverVar: "c", Arguments: []string{`"name"`}, Location: parser.SourceLocation{LineStart: 3}},
			{CallerFunc: "handleRequest", TargetFunc: "Exec", ReceiverVar: "db", Arguments: []string{"sql"}, Location: parser.SourceLocation{LineStart: 7}},
		},
		VarBindings: []parser.UnifiedVarBinding{
			{VarName: "user", CallSite: &parser.UnifiedCallSite{TargetFunc: "Query", ReceiverVar: "c"}},
			{VarName: "sql", Literal: `"SELECT * FROM users WHERE name = " + user`},
		},
	}

	analyzer := NewTaintAnalyzer("golang", []*parser.UnifiedAST{ast}, nil)
	result := analyzer.Analyze()

	if len(result.Flows) == 0 {
		t.Fatal("expected taint flow for propagation chain")
	}

	flow := result.Flows[0]
	// 传播路径应包含中间变量
	if len(flow.Path) == 0 {
		t.Error("expected non-empty propagation path")
	}
}

func TestTaintAnalyzer_Go_SQLInjection_Parameterized(t *testing.T) {
	// 参数化查询应标记为已净化（IsSanitized=true）并降低风险等级
	ast := &parser.UnifiedAST{
		Language: "golang",
		FilePath: "handler.go",
		Functions: []parser.UnifiedFunction{
			{Name: "handleRequest", Location: parser.SourceLocation{File: "handler.go", LineStart: 1}},
		},
		CallSites: []parser.UnifiedCallSite{
			{CallerFunc: "handleRequest", TargetFunc: "Query", ReceiverVar: "c", Arguments: []string{`"id"`}, Location: parser.SourceLocation{LineStart: 3}},
			// 参数化查询："SELECT... WHERE id = ?" + userId 参数
			{CallerFunc: "handleRequest", TargetFunc: "Exec", ReceiverVar: "db", Arguments: []string{`"SELECT * FROM users WHERE id = ?"`, "userId"}, Location: parser.SourceLocation{LineStart: 5}},
		},
		VarBindings: []parser.UnifiedVarBinding{
			{VarName: "userId", CallSite: &parser.UnifiedCallSite{TargetFunc: "Query", ReceiverVar: "c"}},
		},
	}

	analyzer := NewTaintAnalyzer("golang", []*parser.UnifiedAST{ast}, nil)
	result := analyzer.Analyze()

	if len(result.Flows) == 0 {
		t.Fatal("expected taint flow for parameterized query")
	}

	flow := result.Flows[0]
	if flow.RiskLevel != "low" {
		t.Errorf("parameterized query risk level = %q, want low", flow.RiskLevel)
	}
	if !flow.IsSanitized {
		t.Error("parameterized query should be marked as sanitized")
	}
}

func TestTaintAnalyzer_Java_ParamAnnotation(t *testing.T) {
	// 测试参数级 @RequestParam 注解（UnifiedParam.Annotations）
	ast := &parser.UnifiedAST{
		Language: "java",
		FilePath: "UserController.java",
		Functions: []parser.UnifiedFunction{
			{
				Name: "getUser",
				Params: []parser.UnifiedParam{
					{Name: "userId", Type: "Long", Annotations: []string{"@RequestParam"}},
					{Name: "response", Type: "HttpServletResponse"}, // 非用户输入，不应被标记
				},
				Location: parser.SourceLocation{File: "UserController.java", LineStart: 1},
			},
		},
		CallSites: []parser.UnifiedCallSite{
			// Java extractor style
			{CallerFunc: "getUser", TargetFunc: "executeQuery", TargetPkg: "stmt", Arguments: []string{`"SELECT * FROM users WHERE id = " + userId`}, Location: parser.SourceLocation{LineStart: 5}},
		},
	}

	analyzer := NewTaintAnalyzer("java", []*parser.UnifiedAST{ast}, nil)
	result := analyzer.Analyze()

	if len(result.Flows) == 0 {
		t.Fatal("expected taint flow for Java param annotation")
	}

	flow := result.Flows[0]
	if flow.Source.Kind != SourceHTTPParam {
		t.Errorf("source kind = %q, want %q", flow.Source.Kind, SourceHTTPParam)
	}
}

func TestTaintAnalyzer_CrossFileTaint(t *testing.T) {
	// 跨文件污点传播：service/user.go:GetUser (Source) → dao/db.go:QueryUser (Sink)
	ast := &parser.UnifiedAST{
		Language: "golang",
		FilePath: "service/user.go",
		Functions: []parser.UnifiedFunction{
			{Name: "GetUser", Location: parser.SourceLocation{File: "service/user.go", LineStart: 10}},
		},
		CallSites: []parser.UnifiedCallSite{
			// GetUser 先调用 Query 获取 Source
			{CallerFunc: "GetUser", TargetFunc: "Query", ReceiverVar: "c", Arguments: []string{`"id"`}, Location: parser.SourceLocation{LineStart: 12}},
			// 然后调用 dao/db.go 中的 QueryUser
			{CallerFunc: "GetUser", TargetFunc: "QueryUser", ReceiverVar: "", Arguments: []string{"userId"}, Location: parser.SourceLocation{LineStart: 15}},
		},
		VarBindings: []parser.UnifiedVarBinding{
			{VarName: "userId", CallSite: &parser.UnifiedCallSite{CallerFunc: "GetUser", TargetFunc: "Query", ReceiverVar: "c"}},
		},
	}

	g := graph.NewMemorySymbolGraph(1)
	// 变更文件中的 Source 函数
	g.AddNode(&graph.MemorySymbolNode{
		ID:       "func:service/user.go:GetUser",
		Type:     graph.NodeFunc,
		Name:     "GetUser",
		File:     "service/user.go",
		Location: parser.SourceLocation{File: "service/user.go", LineStart: 10},
	})
	// 非变更文件中的 Sink 函数
	g.AddNode(&graph.MemorySymbolNode{
		ID:       "func:dao/db.go:QueryUser",
		Type:     graph.NodeFunc,
		Name:     "QueryUser",
		File:     "dao/db.go",
		Location: parser.SourceLocation{File: "dao/db.go", LineStart: 20},
		Properties: map[string]interface{}{
			"body_snippet": `db.Exec("SELECT * FROM users WHERE id = " + userId)`,
		},
	})
	// 调用关系：service/user.go:GetUser → dao/db.go:QueryUser
	g.AddRelation(graph.MemoryRelation{
		From: "func:service/user.go:GetUser",
		To:   "func:dao/db.go:QueryUser",
		Type: graph.RelCalls,
	})

	analyzer := NewTaintAnalyzer("golang", []*parser.UnifiedAST{ast}, g)
	result := analyzer.Analyze()

	if len(result.Flows) == 0 {
		t.Fatal("expected at least one flow")
	}

	var hasCrossFile bool
	for _, f := range result.Flows {
		if f.Sink.File == "dao/db.go" {
			hasCrossFile = true
			if f.Source.File != "service/user.go" {
				t.Errorf("cross-file flow source file = %q, want service/user.go", f.Source.File)
			}
		}
	}
	if !hasCrossFile {
		t.Error("expected cross-file taint flow from service/user.go to dao/db.go")
	}
}

func TestTaintAnalyzer_NoFlow(t *testing.T) {
	ast := &parser.UnifiedAST{
		Language: "golang",
		FilePath: "safe.go",
		Functions: []parser.UnifiedFunction{
			{Name: "safeFunc", Location: parser.SourceLocation{File: "safe.go", LineStart: 1}},
		},
		CallSites: []parser.UnifiedCallSite{
			// 没有 Source，只有 Sink
			{CallerFunc: "safeFunc", TargetFunc: "Exec", ReceiverVar: "db", Arguments: []string{`"SELECT 1"`}, Location: parser.SourceLocation{LineStart: 3}},
		},
	}

	analyzer := NewTaintAnalyzer("golang", []*parser.UnifiedAST{ast}, nil)
	result := analyzer.Analyze()

	if len(result.Flows) != 0 {
		t.Errorf("expected 0 flows, got %d", len(result.Flows))
	}
}
