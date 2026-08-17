package framework

import (
	"testing"

	"github.com/ai-optimizer/backend/internal/graph"
	"github.com/ai-optimizer/backend/internal/parser"
)

func TestGinAdapter_Detect(t *testing.T) {
	adapter := NewGinAdapter()

	tests := []struct {
		name string
		ast  *parser.UnifiedAST
		want bool
	}{
		{
			name: "detects gin import",
			ast: &parser.UnifiedAST{
				Language: "golang",
				Imports: []parser.UnifiedImport{
					{Path: "github.com/gin-gonic/gin"},
				},
			},
			want: true,
		},
		{
			name: "no gin import",
			ast: &parser.UnifiedAST{
				Language: "golang",
				Imports: []parser.UnifiedImport{
					{Path: "net/http"},
				},
			},
			want: false,
		},
		{
			name: "wrong language",
			ast: &parser.UnifiedAST{
				Language: "java",
				Imports: []parser.UnifiedImport{
					{Path: "github.com/gin-gonic/gin"},
				},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := adapter.Detect(tt.ast)
			if got != tt.want {
				t.Errorf("Detect() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGinAdapter_Enrich(t *testing.T) {
	adapter := NewGinAdapter()
	ast := &parser.UnifiedAST{
		Language: "golang",
		FilePath: "handler.go",
		Functions: []parser.UnifiedFunction{
			{
				Name:        "SetupRoutes",
				BodySnippet: `r.GET("/users", handler.GetUsers)`,
				Location:    parser.SourceLocation{File: "handler.go", LineStart: 10},
			},
		},
	}
	g := graph.NewMemorySymbolGraph(1)

	adapter.Enrich(ast, g)

	// Check that endpoint node was added
	found := false
	for _, node := range g.AllNodes() {
		if node.Type == graph.NodeEndpoint {
			found = true
			if node.Name != "/users" {
				t.Errorf("endpoint path = %q, want /users", node.Name)
			}
			if props, ok := node.Properties["method"]; !ok || props != "GET" {
				t.Errorf("endpoint method = %v, want GET", props)
			}
		}
	}
	if !found {
		t.Error("expected endpoint node to be added")
	}

	// Check that sink node was NOT added because the snippet doesn't match sink patterns
	sinkFound := false
	for _, node := range g.AllNodes() {
		if node.Type == graph.NodeSink {
			sinkFound = true
		}
	}
	if sinkFound {
		t.Error("expected no sink nodes for this snippet")
	}
}
