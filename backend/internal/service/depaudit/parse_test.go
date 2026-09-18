package depaudit

import "testing"

func findByKey(deps []*Dep, eco, name, version string) *Dep {
	for _, d := range deps {
		if d.Ecosystem == eco && d.Name == name && d.Version == version {
			return d
		}
	}
	return nil
}

func TestParseGoSum(t *testing.T) {
	content := "github.com/gin-gonic/gin v1.9.1 h1:abc=\n" +
		"github.com/gin-gonic/gin v1.9.1/go.mod h1:def=\n" +
		"golang.org/x/text v0.3.7 h1:xyz=\n"
	deps := parseGoSum(content)
	if len(deps) != 2 {
		t.Fatalf("want 2, got %d", len(deps))
	}
	if findByKey(deps, "Go", "github.com/gin-gonic/gin", "v1.9.1") == nil {
		t.Fatal("gin not parsed")
	}
}

func TestParsePackageLockV3(t *testing.T) {
	content := `{
	  "lockfileVersion": 3,
	  "packages": {
	    "": {"name": "root"},
	    "node_modules/lodash": {"version": "4.17.21"},
	    "node_modules/@scope/pkg": {"version": "1.0.0", "dev": true}
	  }
	}`
	deps := parsePackageLock(content)
	if findByKey(deps, "npm", "lodash", "4.17.21") == nil {
		t.Fatal("lodash not parsed")
	}
	scoped := findByKey(deps, "npm", "@scope/pkg", "1.0.0")
	if scoped == nil {
		t.Fatal("scoped pkg not parsed")
	}
	if scoped.Scope != "dev" {
		t.Fatalf("want dev scope, got %s", scoped.Scope)
	}
}

func TestParsePoetryLock(t *testing.T) {
	content := `[[package]]
name = "requests"
version = "2.31.0"
description = "x"

[[package]]
name = "urllib3"
version = "2.0.0"
`
	deps := parsePoetryLock(content)
	if len(deps) != 2 {
		t.Fatalf("want 2, got %d", len(deps))
	}
	if findByKey(deps, "pypi", "requests", "2.31.0") == nil {
		t.Fatal("requests not parsed")
	}
}

func TestParsePipfileLock(t *testing.T) {
	content := `{"default": {"flask": {"version": "==2.3.0"}}, "develop": {"pytest": {"version": "==7.0.0"}}}`
	deps := parsePipfileLock(content)
	if findByKey(deps, "pypi", "flask", "2.3.0") == nil {
		t.Fatal("flask not parsed")
	}
	if d := findByKey(deps, "pypi", "pytest", "7.0.0"); d == nil || d.Scope != "dev" {
		t.Fatal("pytest dev scope mismatch")
	}
}

func TestNormalizeNamePEP503(t *testing.T) {
	if normalizeName("pypi", "My_Package.Name") != "my-package-name" {
		t.Fatalf("pep503 normalize failed: %s", normalizeName("pypi", "My_Package.Name"))
	}
}

func TestBuildPURL(t *testing.T) {
	cases := map[string]string{
		BuildPURL("Go", "github.com/gin-gonic/gin", "v1.9.1"):            "pkg:golang/github.com/gin-gonic/gin@v1.9.1",
		BuildPURL("npm", "@scope/pkg", "1.0.0"):                          "pkg:npm/%40scope/pkg@1.0.0",
		BuildPURL("pypi", "My_Package", "1.0.0"):                         "pkg:pypi/my-package@1.0.0",
		BuildPURL("maven", "org.apache.commons:commons-lang3", "3.12.0"): "pkg:maven/org.apache.commons/commons-lang3@3.12.0",
	}
	for got, want := range cases {
		if got != want {
			t.Fatalf("purl mismatch:\n got %s\nwant %s", got, want)
		}
	}
}

func TestSpecLooksExact_ScopedNpm(t *testing.T) {
	content := `{"dependencies": {"@scope/pkg": "1.2.3", "lodash": "^4.17.21"}}`
	if !specLooksExact("npm", content, "@scope/pkg") {
		t.Fatal("scoped exact version should be resolved")
	}
	if specLooksExact("npm", content, "lodash") {
		t.Fatal("caret range should not be resolved")
	}
}

func TestPackageLookupNamesPEP503(t *testing.T) {
	names := packageLookupNames("pypi", "My_Package.Name")
	if len(names) != 2 {
		t.Fatalf("want raw+normalized, got %v", names)
	}
	if names[1] != "my-package-name" {
		t.Fatalf("normalized mismatch: %v", names)
	}
}

func TestMergeDepDirectWins(t *testing.T) {
	m := map[string]*Dep{}
	mergeDep(m, &Dep{Ecosystem: "Go", Name: "x", Version: "v1", Direct: false, SourceFiles: []string{"go.sum"}})
	mergeDep(m, &Dep{Ecosystem: "Go", Name: "x", Version: "v1", Direct: true, SourceFiles: []string{"go.mod"}})
	if len(m) != 1 {
		t.Fatalf("want 1 merged, got %d", len(m))
	}
	for _, d := range m {
		if !d.Direct {
			t.Fatal("direct should win")
		}
		if len(d.SourceFiles) != 2 {
			t.Fatalf("want 2 source files, got %d", len(d.SourceFiles))
		}
	}
}
