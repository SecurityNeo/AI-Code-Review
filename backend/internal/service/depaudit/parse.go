package depaudit

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ai-optimizer/backend/internal/model"
	"github.com/ai-optimizer/backend/internal/service/pipeline"
	"go.uber.org/zap"
)

// Dep 依赖审查内部依赖模型
type Dep struct {
	Ecosystem       string   `json:"ecosystem"` // Go/npm/pypi/maven
	Name            string   `json:"name"`
	Version         string   `json:"version"`
	Direct          bool     `json:"direct"`
	VersionResolved bool     `json:"version_resolved"`
	Scope           string   `json:"scope"`
	SourceFiles     []string `json:"source_files"`
}

// ParseReport 解析概要（用于诊断"无依赖"原因）
type ParseReport struct {
	ManifestFiles []string `json:"manifest_files"`
	LockFiles     []string `json:"lock_files"`
}

const (
	// maxManifestBytes 单个清单/锁文件读取上限
	maxManifestBytes = 10 << 20
	// maxManifestFiles 单仓库清单/锁文件数量上限
	maxManifestFiles = 2000
)

// 遍历时排除的目录
var excludedDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, "dist": true,
	"build": true, "target": true, ".venv": true, "venv": true,
	"__pycache__": true, ".next": true, "coverage": true, ".idea": true,
	".gradle": true, ".mvn": true, ".tox": true, "site-packages": true,
}

// ParseRepo 解析仓库依赖（兼容旧签名）
func ParseRepo(dir string, cfg *model.DependencyAuditConfig) ([]*Dep, error) {
	deps, _, err := ParseRepoDetailed(dir, cfg)
	return deps, err
}

// ParseRepoDetailed 解析仓库清单与锁文件，并返回检测到的文件列表
func ParseRepoDetailed(dir string, cfg *model.DependencyAuditConfig) ([]*Dep, ParseReport, error) {
	if cfg == nil {
		cfg = DefaultConfig(0)
	}
	manifests := map[string]string{}
	lockfiles := map[string]string{}
	catalogContent := ""
	report := ParseReport{}

	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if excludedDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		rel = filepath.ToSlash(rel)
		base := d.Name()

		if base == "libs.versions.toml" {
			if data, e := os.ReadFile(path); e == nil {
				catalogContent = string(data)
			}
			return nil
		}
		if !isLockfile(base) && !isManifest(base) {
			return nil
		}
		if info, ierr := d.Info(); ierr != nil || info.Size() > maxManifestBytes {
			zap.L().Warn("skip oversized manifest/lockfile", zap.String("file", rel))
			return nil
		}
		if len(manifests)+len(lockfiles) >= maxManifestFiles {
			zap.L().Warn("manifest/lockfile count limit reached", zap.String("file", rel))
			return nil
		}
		data, e := os.ReadFile(path)
		if e != nil {
			return nil
		}
		if isLockfile(base) {
			lockfiles[rel] = string(data)
			report.LockFiles = append(report.LockFiles, rel)
		} else {
			manifests[rel] = string(data)
			report.ManifestFiles = append(report.ManifestFiles, rel)
		}
		return nil
	})
	if err != nil {
		return nil, report, err
	}

	catalog := map[string]catalogEntry{}
	if catalogContent != "" {
		catalog = parseVersionCatalog(catalogContent)
	}

	merged := map[string]*Dep{}
	byName := map[string]*Dep{}

	// 1) 清单：直接依赖
	for rel, content := range manifests {
		base := rel
		if i := strings.LastIndex(rel, "/"); i >= 0 {
			base = rel[i+1:]
		}
		deps := parseManifestFile(rel, base, content, catalog)
		for _, d := range deps {
			if d.Ecosystem == "cargo" {
				continue
			}
			indirect := isIndirect(d.Ecosystem, d.Name, d.Indirect, content)
			if indirect && !cfg.IncludeTransitive {
				continue
			}
			dep := &Dep{
				Ecosystem:       d.Ecosystem,
				Name:            d.Name,
				Version:         d.Version,
				Direct:          !indirect,
				VersionResolved: d.Version != "" && specLooksExact(d.Ecosystem, content, d.Name),
				SourceFiles:     []string{rel},
			}
			mergeDep(merged, dep)
			if dep.Direct {
				byName[nameKey(dep.Ecosystem, dep.Name)] = dep
			}
		}
	}

	// 2) 锁文件：仅当 lockfile_precise=on 时读取
	if cfg.LockfilePrecise {
		for rel, content := range lockfiles {
			// go.mod 的 require 已给出完整构建列表（含间接依赖，且一模块一版本）。
			// go.sum 是哈希账本，会包含同模块的历史多版本，若作为依赖来源会产生大量重复版本。
			if strings.HasSuffix(rel, "go.sum") {
				continue
			}
			for _, ld := range parseLockfile(rel, content) {
				if existing, ok := byName[nameKey(ld.Ecosystem, ld.Name)]; ok {
					delete(merged, depKey(existing))
					existing.Version = ld.Version
					existing.VersionResolved = true
					if existing.Scope == "" {
						existing.Scope = ld.Scope
					}
					existing.SourceFiles = appendUnique(existing.SourceFiles, rel)
					mergeDep(merged, existing)
					continue
				}
				if !cfg.IncludeTransitive {
					continue
				}
				dep := &Dep{
					Ecosystem:       ld.Ecosystem,
					Name:            ld.Name,
					Version:         ld.Version,
					Direct:          false,
					VersionResolved: true,
					Scope:           ld.Scope,
					SourceFiles:     []string{rel},
				}
				mergeDep(merged, dep)
			}
		}
	}

	out := make([]*Dep, 0, len(merged))
	for _, d := range merged {
		sort.Strings(d.SourceFiles)
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Ecosystem != out[j].Ecosystem {
			return out[i].Ecosystem < out[j].Ecosystem
		}
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Version < out[j].Version
	})
	return out, report, nil
}

// parseManifestFile 按清单类型分派解析器
func parseManifestFile(rel, base, content string, catalog map[string]catalogEntry) []pipeline.Dependency {
	switch {
	case strings.HasPrefix(base, "requirements") && strings.HasSuffix(base, ".txt"):
		return parseRequirements(content)
	case base == "build.gradle" || base == "build.gradle.kts":
		return parseGradle(content, catalog)
	case base == "pyproject.toml":
		return parsePyproject(content)
	default:
		deps, err := pipeline.ParseDependenciesFromFile(rel, content)
		if err != nil {
			zap.L().Debug("parse manifest failed", zap.String("file", rel), zap.Error(err))
			return nil
		}
		return deps
	}
}

// depKey 依赖唯一键：生态 + 规范化包名 + 版本
func depKey(d *Dep) string {
	return strings.ToLower(d.Ecosystem) + "|" + normalizeName(d.Ecosystem, d.Name) + "|" + d.Version
}

// nameKey 依赖名称键（忽略版本）
func nameKey(eco, name string) string {
	return strings.ToLower(eco) + "|" + strings.ToLower(normalizeName(eco, name))
}

func mergeDep(m map[string]*Dep, d *Dep) {
	key := depKey(d)
	if existing, ok := m[key]; ok {
		if d.Direct {
			existing.Direct = true
		}
		if d.VersionResolved {
			existing.VersionResolved = true
		}
		for _, f := range d.SourceFiles {
			existing.SourceFiles = appendUnique(existing.SourceFiles, f)
		}
		if existing.Scope == "" {
			existing.Scope = d.Scope
		}
		return
	}
	m[key] = d
}

func appendUnique(arr []string, v string) []string {
	for _, x := range arr {
		if x == v {
			return arr
		}
	}
	return append(arr, v)
}

func isManifest(base string) bool {
	switch base {
	case "go.mod", "package.json", "pom.xml", "pyproject.toml", "build.gradle", "build.gradle.kts":
		return true
	}
	return strings.HasPrefix(base, "requirements") && strings.HasSuffix(base, ".txt")
}

func isLockfile(base string) bool {
	switch base {
	case "go.sum", "package-lock.json", "poetry.lock", "Pipfile.lock", "requirements.lock":
		return true
	}
	return false
}

// normalizeName 名称规范化（PyPI PEP503）
func normalizeName(eco, name string) string {
	if strings.EqualFold(eco, "pypi") {
		name = strings.ToLower(name)
		replacer := strings.NewReplacer("_", "-", ".", "-")
		return replacer.Replace(name)
	}
	return name
}

// isIndirect 判断依赖是否为间接依赖。
// 说明：pipeline.GoModParser 先剥离行注释再判断 "// indirect"，导致其 Indirect 恒为 false，
// 故此处对 Go 依赖在 depaudit 内自行从 go.mod 原文识别。
func isIndirect(eco, name string, parsed bool, content string) bool {
	if !strings.EqualFold(eco, "Go") {
		return parsed
	}
	for _, line := range strings.Split(content, "\n") {
		t := strings.TrimSpace(line)
		if strings.Contains(t, name) && strings.Contains(t, "// indirect") {
			return true
		}
	}
	return parsed
}

// specLooksExact 判断清单中该依赖声明的版本是否为精确版本
func specLooksExact(eco, content, name string) bool {
	switch strings.ToLower(eco) {
	case "go", "maven":
		return true
	}
	idx := strings.Index(content, name)
	if idx < 0 {
		return true
	}
	tail := content[idx+len(name):]
	if cut := strings.IndexAny(tail, "\n,"); cut >= 0 {
		tail = tail[:cut]
	}
	if strings.ContainsAny(tail, "^~*") {
		return false
	}
	if strings.ContainsAny(tail, "><") {
		return false
	}
	if strings.Contains(tail, " x") || strings.Contains(tail, ".x") {
		return false
	}
	return true
}
