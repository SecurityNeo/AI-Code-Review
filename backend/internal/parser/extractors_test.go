package parser

import (
	"testing"
)

func TestExtractorRegistry(t *testing.T) {
	tests := []struct {
		name         string
		language     string
		wantLanguage string
		wantOK       bool
	}{
		{"go by ext", "", "golang", true},
		{"java by ext", "", "java", true},
		{"python by ext", "", "python", true},
		{"javascript by ext", "", "javascript", true},
		{"typescript by ext", "", "typescript", true},
		{"unknown by ext", "", "", false},
	}

	extMap := map[string]string{
		"go by ext":         ".go",
		"java by ext":       ".java",
		"python by ext":     ".py",
		"javascript by ext": ".js",
		"typescript by ext": ".ts",
		"unknown by ext":    ".unknown",
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ext := extMap[tt.name]
			extractor, ok := GlobalRegistry.GetExtractorByExt(ext)
			if ok != tt.wantOK {
				t.Errorf("GetExtractorByExt(%q) ok = %v, want %v", ext, ok, tt.wantOK)
			}
			if ok && extractor.Language() != tt.wantLanguage {
				t.Errorf("language = %q, want %q", extractor.Language(), tt.wantLanguage)
			}
		})
	}

	// Test language lookup
	t.Run("language lookup", func(t *testing.T) {
		langs := GlobalRegistry.GetAllLanguages()
		if len(langs) == 0 {
			t.Error("expected at least one registered language")
		}

		for _, lang := range []string{"golang", "java", "python", "javascript", "typescript"} {
			_, ok := GlobalRegistry.GetExtractorByLanguage(lang)
			if !ok {
				t.Errorf("expected language %q to be registered", lang)
			}
		}
	})
}
