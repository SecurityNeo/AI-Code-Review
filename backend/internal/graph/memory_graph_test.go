package graph

import (
	"testing"

	"github.com/ai-optimizer/backend/internal/parser"
)

func TestMemorySymbolGraph_AddNode_GetNode(t *testing.T) {
	g := NewMemorySymbolGraph(1)

	tests := []struct {
		name string
		node *MemorySymbolNode
		id   string
	}{
		{
			name: "add and get function node",
			node: &MemorySymbolNode{
				ID:   "func:main.go:hello",
				Type: NodeFunc,
				Name: "hello",
				File: "main.go",
				Location: parser.SourceLocation{
					File:      "main.go",
					LineStart: 10,
				},
			},
			id: "func:main.go:hello",
		},
		{
			name: "add type node",
			node: &MemorySymbolNode{
				ID:   "type:main.go:Foo",
				Type: NodeType,
				Name: "Foo",
				File: "main.go",
			},
			id: "type:main.go:Foo",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g.AddNode(tt.node)
			got, ok := g.GetNode(tt.id)
			if !ok {
				t.Fatalf("GetNode(%q) not found", tt.id)
			}
			if got.Name != tt.node.Name {
				t.Errorf("Name = %q, want %q", got.Name, tt.node.Name)
			}
			if got.Type != tt.node.Type {
				t.Errorf("Type = %q, want %q", got.Type, tt.node.Type)
			}
		})
	}
}

func TestMemorySymbolGraph_Clone(t *testing.T) {
	g := NewMemorySymbolGraph(1)
	g.AddNode(&MemorySymbolNode{
		ID:   "func:main.go:hello",
		Type: NodeFunc,
		Name: "hello",
		Properties: map[string]interface{}{
			"framework": "gin",
		},
	})
	g.AddRelation(MemoryRelation{
		From: "func:main.go:hello",
		To:   "func:main.go:world",
		Type: RelCalls,
	})

	clone := g.Clone()

	// Verify nodes copied
	node, ok := clone.GetNode("func:main.go:hello")
	if !ok {
		t.Fatal("cloned node not found")
	}
	if node.Name != "hello" {
		t.Errorf("cloned node Name = %q, want hello", node.Name)
	}

	// Verify properties deep copied
	node.Properties["framework"] = "echo"
	orig, _ := g.GetNode("func:main.go:hello")
	if orig.Properties["framework"] != "gin" {
		t.Error("expected original Properties to be unchanged after clone modification")
	}

	// Verify relations copied
	if clone.RelationCount() != g.RelationCount() {
		t.Errorf("cloned relations count = %d, want %d", clone.RelationCount(), g.RelationCount())
	}
}

func TestMemorySymbolGraph_OverlayFileChanges(t *testing.T) {
	g := NewMemorySymbolGraph(1)
	g.AddNode(&MemorySymbolNode{
		ID:   "func:main.go:OldFunc",
		Type: NodeFunc,
		Name: "OldFunc",
		File: "main.go",
	})

	src := []byte(`package main

func NewFunc() {}
`)

	extractor := &mockExtractor{}
	if err := g.OverlayFileChanges("main.go", src, extractor); err != nil {
		t.Fatalf("OverlayFileChanges failed: %v", err)
	}

	// Old node should be removed
	if _, ok := g.GetNode("func:main.go:OldFunc"); ok {
		t.Error("expected old node to be removed")
	}

	// New node should be added
	if _, ok := g.GetNode("func:main.go:NewFunc"); !ok {
		t.Error("expected new node to be added")
	}
}

type mockExtractor struct{}

func (m *mockExtractor) Language() string   { return "golang" }
func (m *mockExtractor) FileExts() []string { return []string{".go"} }
func (m *mockExtractor) ParseFile(filePath string, content []byte) (*parser.UnifiedAST, error) {
	return &parser.UnifiedAST{
		Language: "golang",
		FilePath: filePath,
		Functions: []parser.UnifiedFunction{
			{Name: "NewFunc", Location: parser.SourceLocation{File: filePath, LineStart: 3}},
		},
	}, nil
}
