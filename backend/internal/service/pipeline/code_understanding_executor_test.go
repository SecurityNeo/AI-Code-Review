package pipeline

import (
	"testing"

	"github.com/ai-optimizer/backend/internal/graph"
	"github.com/ai-optimizer/backend/internal/parser"
)

func TestCodeUnderstandingExecutor_buildReport(t *testing.T) {
	exec := NewCodeUnderstandingExecutor("/tmp/test")

	asts := []*parser.UnifiedAST{
		{
			Language: "golang",
			FilePath: "main.go",
			Functions: []parser.UnifiedFunction{
				{Name: "main", Location: parser.SourceLocation{File: "main.go", LineStart: 5}},
				{Name: "helper", Location: parser.SourceLocation{File: "main.go", LineStart: 10}},
			},
			Types: []parser.UnifiedType{
				{Name: "Config", Kind: "struct", Location: parser.SourceLocation{File: "main.go", LineStart: 1}},
			},
		},
	}

	graphResult := &graph.SymbolGraphResult{
		NodeCount: 5,
		RelCount:  3,
		Endpoints: []parser.UnifiedEndpoint{
			{Path: "/health", Method: "GET", File: "main.go", Line: 20, Framework: "gin"},
		},
	}

	report := exec.buildReport(asts, graphResult, nil, 1, 1, "golang", "")

	if report.Mode != "full" {
		t.Errorf("mode = %q, want full", report.Mode)
	}
	if report.ParsedFiles != 1 {
		t.Errorf("parsed files = %d, want 1", report.ParsedFiles)
	}
	if report.TotalFunctions != 2 {
		t.Errorf("total functions = %d, want 2", report.TotalFunctions)
	}
	if report.TotalTypes != 1 {
		t.Errorf("total types = %d, want 1", report.TotalTypes)
	}
	if report.EndpointCount != 1 {
		t.Errorf("endpoint count = %d, want 1", report.EndpointCount)
	}
	if len(report.Endpoints) != 1 || report.Endpoints[0].Path != "/health" {
		t.Errorf("unexpected endpoints: %+v", report.Endpoints)
	}
	if report.ReportText == "" {
		t.Error("expected non-empty report text")
	}
}

func TestCodeUnderstandingExecutor_buildReport_nilGraphResult(t *testing.T) {
	exec := NewCodeUnderstandingExecutor("/tmp/test")

	asts := []*parser.UnifiedAST{
		{
			Language:  "golang",
			FilePath:  "main.go",
			Functions: []parser.UnifiedFunction{{Name: "main"}},
		},
	}

	report := exec.buildReport(asts, nil, nil, 1, 1, "golang", "")

	if report.Mode != "ast_only" {
		t.Errorf("mode = %q, want ast_only", report.Mode)
	}
	if report.SymbolGraphSummary.NodeCount != 0 {
		t.Errorf("node count = %d, want 0", report.SymbolGraphSummary.NodeCount)
	}
	if report.EndpointCount != 0 {
		t.Errorf("endpoint count = %d, want 0", report.EndpointCount)
	}
}

func TestCodeUnderstandingExecutor_buildReport_emptyASTs(t *testing.T) {
	exec := NewCodeUnderstandingExecutor("/tmp/test")
	report := exec.buildReport(nil, nil, nil, 0, 0, "golang", "")

	if report.ParsedFiles != 0 {
		t.Errorf("parsed files = %d, want 0", report.ParsedFiles)
	}
	if report.TotalFiles != 0 {
		t.Errorf("total files = %d, want 0", report.TotalFiles)
	}
}
