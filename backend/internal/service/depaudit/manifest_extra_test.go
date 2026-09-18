package depaudit

import (
	"testing"
)

func TestParseRequirements_UnpinnedAndPinned(t *testing.T) {
	content := "fastapi\nuvicorn\nrequests==2.31.0\nflask>=2.0\n# comment\n-r base.txt\n"
	deps := parseRequirements(content)
	if len(deps) != 4 {
		t.Fatalf("want 4 deps, got %d: %+v", len(deps), deps)
	}
	byName := map[string]string{}
	for _, d := range deps {
		byName[d.Name] = d.Version
	}
	if v, ok := byName["fastapi"]; !ok || v != "" {
		t.Fatalf("fastapi should be present with empty version: %+v", byName)
	}
	if byName["requests"] != "2.31.0" {
		t.Fatalf("requests version mismatch: %+v", byName)
	}
}

func TestParsePyproject_PEP621(t *testing.T) {
	content := `[project]
name = "demo"
dependencies = [
  "fastapi>=0.100",
  "requests",
]

[tool.poetry.dependencies]
python = "^3.10"
httpx = "^0.27"
`
	deps := parsePyproject(content)
	names := map[string]bool{}
	for _, d := range deps {
		names[d.Name] = true
	}
	for _, want := range []string{"fastapi", "requests", "httpx"} {
		if !names[want] {
			t.Fatalf("missing %s in %+v", want, deps)
		}
	}
	if names["python"] {
		t.Fatal("poetry 'python' should be skipped")
	}
}

func TestParseGradle_QuotedMapAndCatalog(t *testing.T) {
	content := `
dependencies {
  implementation 'org.springframework.boot:spring-boot-starter-web'
  implementation 'com.github.pagehelper:pagehelper-spring-boot-starter:1.2.3'
  implementation group: 'org.apache.commons', name: 'commons-lang3', version: '3.12.0'
  implementation(libs.guava)
}`
	catalog := parseVersionCatalog(`[libraries]
guava = { module = "com.google.guava:guava", version = "33.2.1-jre" }
`)
	deps := parseGradle(content, catalog)
	got := map[string]string{}
	for _, d := range deps {
		got[d.Name] = d.Version
	}
	if v, ok := got["org.springframework.boot:spring-boot-starter-web"]; !ok || v != "" {
		t.Fatalf("spring starter should be present unresolved: %+v", got)
	}
	if got["com.github.pagehelper:pagehelper-spring-boot-starter"] != "1.2.3" {
		t.Fatalf("pagehelper version mismatch: %+v", got)
	}
	if got["com.google.guava:guava"] != "33.2.1-jre" {
		t.Fatalf("catalog guava mismatch: %+v", got)
	}
}

func TestParseRepoDetailed_GradleAndUnpinned(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "build.gradle", "dependencies {\n implementation 'com.google.guava:guava:33.2.1-jre'\n}\n")
	writeFile(t, dir, "api/requirements.txt", "fastapi\nrequests==2.31.0\n")

	deps, rep, err := ParseRepoDetailed(dir, cfgFor(true, false))
	if err != nil {
		t.Fatal(err)
	}
	if findByKey(deps, "maven", "com.google.guava:guava", "33.2.1-jre") == nil {
		t.Fatalf("gradle dep not parsed: %+v", deps)
	}
	if d := findByKey(deps, "pypi", "fastapi", ""); d == nil || d.VersionResolved {
		t.Fatalf("unpinned fastapi should be present and unresolved: %+v", deps)
	}
	if len(rep.ManifestFiles) != 2 {
		t.Fatalf("want 2 manifest files in report, got %+v", rep.ManifestFiles)
	}
}

func TestParseRepoDetailed_NoManifest(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "README.md", "hello")
	_, rep, err := ParseRepoDetailed(dir, cfgFor(true, false))
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.ManifestFiles) != 0 {
		t.Fatalf("want no manifest, got %+v", rep.ManifestFiles)
	}
}
