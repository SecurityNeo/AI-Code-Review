package depaudit

import (
	"regexp"
	"strings"

	"github.com/ai-optimizer/backend/internal/service/pipeline"
)

// ========== 依赖声明解析（返回 pipeline.Dependency 以复用现有流程） ==========

// parseDependencySpec 解析形如 "name==1.2" / "name>=1.2" / "name" / "name[extra]" 的声明
// 返回 (name, version, resolved)
func parseDependencySpec(spec string) (string, string, bool) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return "", "", false
	}
	// 去掉环境标记与注释
	if i := strings.IndexAny(spec, ";#"); i >= 0 {
		spec = strings.TrimSpace(spec[:i])
	}
	idx := strings.IndexAny(spec, "<>=!~")
	if idx < 0 {
		name := strings.TrimSpace(spec)
		name = stripExtras(name)
		return name, "", false // 未固定版本
	}
	name := stripExtras(strings.TrimSpace(spec[:idx]))
	rest := spec[idx:]
	op := rest[:len(rest)-len(strings.TrimLeft(rest, "<>=!~"))]
	ver := strings.TrimSpace(rest[len(op):])
	resolved := op == "==" || op == "==="
	return name, ver, resolved
}

func stripExtras(name string) string {
	if i := strings.Index(name, "["); i > 0 {
		return strings.TrimSpace(name[:i])
	}
	return name
}

// parseRequirements 解析 requirements.txt（含未固定版本的行）
func parseRequirements(content string) []pipeline.Dependency {
	var out []pipeline.Dependency
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "-") {
			continue
		}
		name, ver, _ := parseDependencySpec(line)
		if name == "" {
			continue
		}
		out = append(out, pipeline.Dependency{Ecosystem: "pypi", Name: name, Version: ver})
	}
	return out
}

// ========== pyproject.toml ==========

var (
	tomlSectionRE = regexp.MustCompile(`^\s*\[([^\]]+)\]\s*$`)
	tomlKVRE      = regexp.MustCompile(`^\s*([A-Za-z0-9_.\-]+)\s*=\s*(.+?)\s*$`)
	tomlQuotedRE  = regexp.MustCompile(`["']([^"']+)["']`)
)

// parsePyproject 支持 PEP 621 [project].dependencies 与 Poetry [tool.poetry*.dependencies]
func parsePyproject(content string) []pipeline.Dependency {
	var out []pipeline.Dependency
	lines := strings.Split(content, "\n")
	section := ""
	inDepsArray := false

	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if m := tomlSectionRE.FindStringSubmatch(line); m != nil {
			section = m[1]
			inDepsArray = false
			continue
		}

		// PEP 621: dependencies = [ "a>=1", "b" ]
		if (section == "project" || section == "project.optional-dependencies") && strings.HasPrefix(line, "dependencies") && strings.Contains(line, "[") {
			inDepsArray = true
			line = line[strings.Index(line, "[")+1:]
		}
		if inDepsArray {
			for _, q := range tomlQuotedRE.FindAllStringSubmatch(line, -1) {
				name, ver, _ := parseDependencySpec(q[1])
				if name != "" {
					out = append(out, pipeline.Dependency{Ecosystem: "pypi", Name: name, Version: ver})
				}
			}
			if strings.Contains(line, "]") {
				inDepsArray = false
			}
			continue
		}

		// Poetry: [tool.poetry.dependencies] / [tool.poetry.group.x.dependencies]
		if section == "tool.poetry.dependencies" || strings.HasSuffix(section, ".dependencies") && strings.HasPrefix(section, "tool.poetry") {
			m := tomlKVRE.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			name := m[1]
			if name == "python" {
				continue
			}
			ver := ""
			val := m[2]
			if strings.HasPrefix(val, "{") {
				if vm := regexp.MustCompile(`version\s*=\s*"([^"]+)"`).FindStringSubmatch(val); vm != nil {
					ver = strings.TrimLeft(vm[1], "^~=<> ")
				} else if strings.Contains(val, "git") || strings.Contains(val, "path") {
					out = append(out, pipeline.Dependency{Ecosystem: "pypi", Name: name})
					continue
				}
			} else if qm := tomlQuotedRE.FindStringSubmatch(val); qm != nil {
				ver = strings.TrimLeft(qm[1], "^~=<> ")
			}
			out = append(out, pipeline.Dependency{Ecosystem: "pypi", Name: name, Version: ver})
		}
	}
	return out
}

// ========== Gradle ==========

type catalogEntry struct {
	Module  string
	Version string
}

var (
	gradleQuotedRE  = regexp.MustCompile(`(?m)^\s*(?:implementation|api|compileOnly|runtimeOnly|testImplementation|testRuntimeOnly|testCompileOnly|annotationProcessor|developmentOnly|providedCompile|providedRuntime)\s*[\(\s]\s*["']([^"']+)["']`)
	gradleMapRE     = regexp.MustCompile(`group\s*:\s*["']([^"']+)["']\s*,\s*name\s*:\s*["']([^"']+)["'](?:\s*,\s*version\s*:\s*["']([^"']+)["'])?`)
	gradleCatalogRE = regexp.MustCompile(`(?m)^\s*(?:implementation|api|compileOnly|runtimeOnly|testImplementation|testRuntimeOnly|annotationProcessor|developmentOnly)\s*\(\s*libs\.([A-Za-z0-9_.]+)\s*\)`)
)

// parseVersionCatalog 解析 gradle/libs.versions.toml
func parseVersionCatalog(content string) map[string]catalogEntry {
	out := map[string]catalogEntry{}
	lines := strings.Split(content, "\n")
	section := ""
	versions := map[string]string{}
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if m := tomlSectionRE.FindStringSubmatch(line); m != nil {
			section = m[1]
			continue
		}
		if section == "versions" {
			if m := tomlKVRE.FindStringSubmatch(line); m != nil {
				if qm := tomlQuotedRE.FindStringSubmatch(m[2]); qm != nil {
					versions[m[1]] = qm[1]
				}
			}
			continue
		}
		if section == "libraries" {
			m := tomlKVRE.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			alias := m[1]
			val := m[2]
			module := ""
			ver := ""
			if mm := regexp.MustCompile(`module\s*=\s*"([^"]+)"`).FindStringSubmatch(val); mm != nil {
				module = mm[1]
			}
			if vm := regexp.MustCompile(`version\s*=\s*"([^"]+)"`).FindStringSubmatch(val); vm != nil {
				ver = vm[1]
			} else if vr := regexp.MustCompile(`version\.ref\s*=\s*"([^"]+)"`).FindStringSubmatch(val); vr != nil {
				ver = versions[vr[1]]
			}
			if module != "" {
				out[alias] = catalogEntry{Module: module, Version: ver}
				out[strings.ReplaceAll(alias, "-", ".")] = catalogEntry{Module: module, Version: ver}
			}
		}
	}
	return out
}

// parseGradle 解析 build.gradle / build.gradle.kts 的直接依赖
func parseGradle(content string, catalog map[string]catalogEntry) []pipeline.Dependency {
	seen := map[string]bool{}
	var out []pipeline.Dependency
	add := func(coord string) {
		parts := strings.Split(coord, ":")
		if len(parts) < 2 {
			return
		}
		group, artifact := parts[0], parts[1]
		ver := ""
		if len(parts) >= 3 {
			ver = parts[2]
		}
		if strings.HasPrefix(ver, "$") || strings.Contains(ver, "${") {
			ver = ""
		}
		name := group + ":" + artifact
		key := name + "@" + ver
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, pipeline.Dependency{Ecosystem: "maven", Name: name, Version: ver})
	}

	for _, m := range gradleQuotedRE.FindAllStringSubmatch(content, -1) {
		add(m[1])
	}
	for _, m := range gradleMapRE.FindAllStringSubmatch(content, -1) {
		coord := m[1] + ":" + m[2]
		if m[3] != "" {
			coord += ":" + m[3]
		}
		add(coord)
	}
	for _, m := range gradleCatalogRE.FindAllStringSubmatch(content, -1) {
		if e, ok := catalog[m[1]]; ok {
			coord := e.Module
			if e.Version != "" {
				coord += ":" + e.Version
			}
			add(coord)
		}
	}
	return out
}
