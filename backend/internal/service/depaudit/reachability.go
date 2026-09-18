package depaudit

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ai-optimizer/backend/internal/model"
	"gorm.io/gorm"
)

const maxImportFiles = 50000

var (
	goImportBlockRE = regexp.MustCompile(`(?s)import\s*\((.*?)\)`)
	goImportOneRE   = regexp.MustCompile(`(?m)^\s*import\s+(?:[\w.]+\s+)?"([^"]+)"`)
	goQuotedRE      = regexp.MustCompile(`"([^"]+)"`)

	pyImportRE = regexp.MustCompile(`(?m)^\s*import\s+([A-Za-z0-9_.,\s]+)`)
	pyFromRE   = regexp.MustCompile(`(?m)^\s*from\s+([A-Za-z0-9_.]+)\s+import`)

	javaImportRE = regexp.MustCompile(`(?m)^\s*import\s+(?:static\s+)?([A-Za-z0-9_.]+)\s*;`)

	jsFromRE    = regexp.MustCompile(`(?m)from\s+['"]([^'"]+)['"]`)
	jsRequireRE = regexp.MustCompile(`require\(\s*['"]([^'"]+)['"]\s*\)`)
	jsDynImpRE  = regexp.MustCompile(`import\(\s*['"]([^'"]+)['"]\s*\)`)
)

// AnalyzeReachability 计算依赖可达性（import 级精确判定）。
// 仅处理 targets 中标记的坐标（通常为 high/critical 漏洞涉及的依赖），其余保持 null。
func AnalyzeReachability(ctx context.Context, db *gorm.DB, dir string, items []*model.DependencyAuditItem, targets map[uint]bool) {
	if len(targets) == 0 {
		return
	}
	imports := extractImports(ctx, dir)

	for _, it := range items {
		if !targets[it.ID] {
			continue
		}
		if !it.VersionResolved || it.PackageVersion == "" {
			it.Reachable = "unknown"
			continue
		}
		switch strings.ToLower(it.Ecosystem) {
		case "go", "npm":
			if matchImport(imports, it.PackageName, "/") {
				it.Reachable = "true"
			} else {
				it.Reachable = "false"
			}
		case "pypi", "maven":
			rec := ensureImportNames(ctx, db, it.Ecosystem, it.PackageName, it.PackageVersion)
			if rec == nil {
				it.Reachable = "unknown"
				continue
			}
			var paths []string
			_ = json.Unmarshal([]byte(rec.ImportPaths), &paths)
			it.Reachable = "false"
			for _, p := range paths {
				if matchImport(imports, p, ".") {
					it.Reachable = "true"
					break
				}
			}
		default:
			it.Reachable = "unknown"
		}
	}
}

func matchImport(set map[string]bool, base, sep string) bool {
	if base == "" {
		return false
	}
	if set[base] {
		return true
	}
	prefix := base + sep
	for imp := range set {
		if strings.HasPrefix(imp, prefix) {
			return true
		}
	}
	return false
}

func extractImports(ctx context.Context, dir string) map[string]bool {
	set := map[string]bool{}
	count := 0
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		// 定期检查上下文，避免大仓库扫描超出项目超时后仍长时间占用 goroutine
		if count%512 == 0 && ctx.Err() != nil {
			return filepath.SkipAll
		}
		if err != nil || d.IsDir() {
			if d != nil && d.IsDir() && excludedDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if count >= maxImportFiles {
			return filepath.SkipAll
		}
		info, ierr := d.Info()
		if ierr != nil || info.Size() > 2*1024*1024 {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		switch ext {
		case ".go", ".py", ".java", ".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs":
		default:
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		count++
		collectImports(string(data), ext, set)
		return nil
	})
	return set
}

func collectImports(content, ext string, set map[string]bool) {
	switch ext {
	case ".go":
		for _, m := range goImportBlockRE.FindAllStringSubmatch(content, -1) {
			for _, q := range goQuotedRE.FindAllStringSubmatch(m[1], -1) {
				set[q[1]] = true
			}
		}
		for _, m := range goImportOneRE.FindAllStringSubmatch(content, -1) {
			set[m[1]] = true
		}
	case ".py":
		for _, m := range pyImportRE.FindAllStringSubmatch(content, -1) {
			for _, part := range strings.Split(m[1], ",") {
				name := strings.TrimSpace(strings.SplitN(part, " as ", 2)[0])
				if name != "" {
					set[name] = true
				}
			}
		}
		for _, m := range pyFromRE.FindAllStringSubmatch(content, -1) {
			set[m[1]] = true
		}
	case ".java":
		for _, m := range javaImportRE.FindAllStringSubmatch(content, -1) {
			set[m[1]] = true
		}
	default: // js/ts
		for _, re := range []*regexp.Regexp{jsFromRE, jsRequireRE, jsDynImpRE} {
			for _, m := range re.FindAllStringSubmatch(content, -1) {
				spec := m[1]
				if strings.HasPrefix(spec, ".") || strings.HasPrefix(spec, "/") || strings.HasPrefix(spec, "node:") {
					continue
				}
				set[spec] = true
			}
		}
	}
}
