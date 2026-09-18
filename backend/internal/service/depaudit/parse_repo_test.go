package depaudit

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ai-optimizer/backend/internal/model"
)

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func cfgFor(lockPrecise, transitive bool) *model.DependencyAuditConfig {
	return &model.DependencyAuditConfig{LockfilePrecise: lockPrecise, IncludeTransitive: transitive}
}

func TestParseRepo_GoSwitches(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "go.mod", `module example.com/app

go 1.21

require (
	github.com/gin-gonic/gin v1.9.1
	golang.org/x/text v0.3.7 // indirect
)
`)
	writeFile(t, dir, "go.sum", `github.com/gin-gonic/gin v1.9.1 h1:abc=
github.com/gin-gonic/gin v1.9.1/go.mod h1:def=
golang.org/x/text v0.3.7 h1:xyz=
`)

	// 默认：精确版本开、传递依赖关 -> 仅直接依赖且版本来自 go.sum
	deps, err := ParseRepo(dir, cfgFor(true, false))
	if err != nil {
		t.Fatal(err)
	}
	if len(deps) != 1 {
		t.Fatalf("want 1 direct dep, got %d: %+v", len(deps), deps)
	}
	if deps[0].Name != "github.com/gin-gonic/gin" || !deps[0].Direct || !deps[0].VersionResolved {
		t.Fatalf("unexpected direct dep: %+v", deps[0])
	}

	// 传递依赖开 -> 纳入 golang.org/x/text
	deps2, _ := ParseRepo(dir, cfgFor(true, true))
	if len(deps2) != 2 {
		t.Fatalf("want 2 deps with transitive, got %d", len(deps2))
	}

	// 精确版本关 -> 不读锁文件，go.mod 的 indirect 仍可按开关排除
	deps3, _ := ParseRepo(dir, cfgFor(false, false))
	if len(deps3) != 1 {
		t.Fatalf("want 1 dep without lockfile, got %d", len(deps3))
	}
}

func TestParseRepo_NpmLockOverride(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{
  "dependencies": {
    "lodash": "^4.17.21"
  }
}`)
	writeFile(t, dir, "package-lock.json", `{
  "lockfileVersion": 3,
  "packages": {
    "": {"name": "app"},
    "node_modules/lodash": {"version": "4.17.21"},
    "node_modules/minimist": {"version": "1.2.8"}
  }
}`)

	deps, _ := ParseRepo(dir, cfgFor(true, false))
	if len(deps) != 1 {
		t.Fatalf("want only lodash, got %d: %+v", len(deps), deps)
	}
	if deps[0].Name != "lodash" || deps[0].Version != "4.17.21" || !deps[0].VersionResolved {
		t.Fatalf("lockfile should override version and mark resolved: %+v", deps[0])
	}

	deps2, _ := ParseRepo(dir, cfgFor(true, true))
	if findByKey(deps2, "npm", "minimist", "1.2.8") == nil {
		t.Fatal("transitive minimist should be included")
	}
}
