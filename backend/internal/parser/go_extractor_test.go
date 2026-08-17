package parser

import (
	"testing"
)

func TestGoExtractor_ParseFile(t *testing.T) {
	src := []byte(`package main

import "fmt"

// Foo is a sample struct
type Foo struct {
	Name string ` + "`json:\"name\"`" + `
}

// Hello says hello
func (f *Foo) Hello(a int, b string) error {
	fmt.Println("hello")
	return nil
}

func main() {
	f := &Foo{Name: "world"}
	_ = f.Hello(1, "x")
}
`)

	tests := []struct {
		name        string
		filePath    string
		wantFuncs   int
		wantTypes   int
		wantImports int
	}{
		{
			name:        "simple go file",
			filePath:    "test.go",
			wantFuncs:   2,
			wantTypes:   1,
			wantImports: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extractor, ok := GlobalRegistry.GetExtractorByLanguage("golang")
			if !ok {
				t.Fatal("go extractor not registered")
			}
			ast, err := extractor.ParseFile(tt.filePath, src)
			if err != nil {
				t.Fatalf("ParseFile failed: %v", err)
			}
			if ast == nil {
				t.Fatal("expected non-nil ast")
			}
			if ast.Language != "golang" {
				t.Errorf("language = %q, want golang", ast.Language)
			}
			if len(ast.Functions) != tt.wantFuncs {
				t.Errorf("functions = %d, want %d", len(ast.Functions), tt.wantFuncs)
			}
			if len(ast.Types) != tt.wantTypes {
				t.Errorf("types = %d, want %d", len(ast.Types), tt.wantTypes)
			}
			if len(ast.Imports) != tt.wantImports {
				t.Errorf("imports = %d, want %d", len(ast.Imports), tt.wantImports)
			}
			// Verify function details
			var foundHello bool
			for _, fn := range ast.Functions {
				if fn.Name == "Hello" {
					foundHello = true
					if fn.Receiver == "" {
						t.Error("expected receiver for method Hello")
					}
					if len(fn.Params) != 2 {
						t.Errorf("Hello params = %d, want 2", len(fn.Params))
					}
				}
			}
			if !foundHello {
				t.Error("expected Hello function to be found")
			}
			// Verify type details
			if len(ast.Types) > 0 {
				typ := ast.Types[0]
				if typ.Name != "Foo" {
					t.Errorf("type name = %q, want Foo", typ.Name)
				}
				if typ.Kind != "struct" {
					t.Errorf("type kind = %q, want struct", typ.Kind)
				}
				if len(typ.Fields) != 1 {
					t.Errorf("type fields = %d, want 1", len(typ.Fields))
				}
			}
			// Verify call sites
			if len(ast.CallSites) == 0 {
				t.Error("expected at least one call site")
			}
		})
	}
}
