package dataflow

import (
	"testing"

	"github.com/ai-optimizer/backend/internal/graph"
	"github.com/ai-optimizer/backend/internal/parser"
)

type mockReviewView struct {
	g *graph.MemorySymbolGraph
}

func (m *mockReviewView) GetGraph() *graph.MemorySymbolGraph {
	return m.g
}

func (m *mockReviewView) FindEndpoints() []graph.MemorySymbolNode {
	var eps []graph.MemorySymbolNode
	for _, n := range m.g.AllNodes() {
		if n.Type == graph.NodeEndpoint {
			eps = append(eps, *n)
		}
	}
	return eps
}

func TestEngine_Analyze_SimpleTaintFlow(t *testing.T) {
	g := graph.NewMemorySymbolGraph(1)

	// Source function: reads user input
	g.AddNode(&graph.MemorySymbolNode{
		ID:       "func:handler.go:handleRequest",
		Type:     graph.NodeFunc,
		Name:     "handleRequest",
		File:     "handler.go",
		Location: parser.SourceLocation{File: "handler.go", LineStart: 10},
		Properties: map[string]interface{}{
			"body_snippet": `user := c.Query("name")
							db.Exec("SELECT * FROM users WHERE name = " + user)`,
		},
	})

	// Sink function: executes SQL
	g.AddNode(&graph.MemorySymbolNode{
		ID:       "func:db.go:execQuery",
		Type:     graph.NodeFunc,
		Name:     "execQuery",
		File:     "db.go",
		Location: parser.SourceLocation{File: "db.go", LineStart: 20},
		Properties: map[string]interface{}{
			"body_snippet": `db.Exec("SELECT * FROM users")`,
		},
	})

	g.AddRelation(graph.MemoryRelation{
		From: "func:handler.go:handleRequest",
		To:   "func:db.go:execQuery",
		Type: graph.RelCalls,
	})

	view := &mockReviewView{g: g}
	engine := NewEngine()
	result := engine.Analyze(view)

	if len(result.Flows) == 0 {
		t.Fatal("expected at least one taint flow")
	}

	flow := result.Flows[0]
	if flow.Source.Function != "handleRequest" {
		t.Errorf("source function = %q, want handleRequest", flow.Source.Function)
	}
	if flow.Sink.Function != "Exec" {
		t.Errorf("sink function = %q, want Exec", flow.Sink.Function)
	}
	if flow.Category != SinkSQLInjection {
		t.Errorf("category = %q, want %q", flow.Category, SinkSQLInjection)
	}
}

func TestEngine_Analyze_WithSanitizer(t *testing.T) {
	g := graph.NewMemorySymbolGraph(1)

	g.AddNode(&graph.MemorySymbolNode{
		ID:       "func:handler.go:handleRequest",
		Type:     graph.NodeFunc,
		Name:     "handleRequest",
		File:     "handler.go",
		Location: parser.SourceLocation{File: "handler.go", LineStart: 10},
		Properties: map[string]interface{}{
			"body_snippet": `user := c.Query("name")
							sanitized := html.EscapeString(user)
							db.Exec("SELECT * FROM users WHERE name = " + sanitized)`,
		},
	})

	view := &mockReviewView{g: g}
	engine := NewEngine()
	result := engine.Analyze(view)

	// Should NOT have flows because of sanitizer
	for _, flow := range result.Flows {
		if flow.Source.Function == "handleRequest" && flow.Sink.Kind == SinkSQLInjection {
			t.Error("expected sanitizer to prevent sql injection flow")
		}
	}
}
