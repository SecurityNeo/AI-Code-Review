package depaudit

import (
	"bufio"
	"encoding/json"
	"strings"
)

// parseLockfile 解析锁文件，返回精确版本依赖（Direct 未知，默认 false）
func parseLockfile(relPath, content string) []*Dep {
	base := relPath
	if idx := strings.LastIndex(relPath, "/"); idx >= 0 {
		base = relPath[idx+1:]
	}
	switch base {
	case "go.sum":
		return parseGoSum(content)
	case "package-lock.json":
		return parsePackageLock(content)
	case "poetry.lock":
		return parsePoetryLock(content)
	case "Pipfile.lock":
		return parsePipfileLock(content)
	case "requirements.lock":
		return parseRequirementLock(content)
	}
	return nil
}

// go.sum: <module> <version>[/go.mod] <hash>
func parseGoSum(content string) []*Dep {
	seen := map[string]bool{}
	var out []*Dep
	scanner := bufio.NewScanner(strings.NewReader(content))
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		module := fields[0]
		version := strings.TrimSuffix(fields[1], "/go.mod")
		if version == "" || !strings.HasPrefix(version, "v") {
			continue
		}
		key := module + "@" + version
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, &Dep{Ecosystem: "Go", Name: module, Version: version, Direct: false, VersionResolved: true})
	}
	return out
}

// package-lock.json: lockfileVersion 1 的 dependencies / 2+ 的 packages
func parsePackageLock(content string) []*Dep {
	var raw struct {
		Packages map[string]struct {
			Version string `json:"version"`
			Dev     bool   `json:"dev"`
		} `json:"packages"`
		Dependencies map[string]struct {
			Version      string                     `json:"version"`
			Dev          bool                       `json:"dev"`
			Dependencies map[string]json.RawMessage `json:"dependencies"`
		} `json:"dependencies"`
	}
	if err := json.Unmarshal([]byte(content), &raw); err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []*Dep
	add := func(name, version string, dev bool) {
		name = cleanNpmLockName(name)
		if name == "" || version == "" {
			return
		}
		key := name + "@" + version
		if seen[key] {
			return
		}
		seen[key] = true
		scope := "runtime"
		if dev {
			scope = "dev"
		}
		out = append(out, &Dep{Ecosystem: "npm", Name: name, Version: version, VersionResolved: true, Scope: scope})
	}

	for k, v := range raw.Packages {
		if k == "" {
			continue
		}
		add(k, v.Version, v.Dev)
	}
	// v1 fallback
	var walk func(map[string]struct {
		Version      string                     `json:"version"`
		Dev          bool                       `json:"dev"`
		Dependencies map[string]json.RawMessage `json:"dependencies"`
	})
	walk = func(deps map[string]struct {
		Version      string                     `json:"version"`
		Dev          bool                       `json:"dev"`
		Dependencies map[string]json.RawMessage `json:"dependencies"`
	}) {
		for name, v := range deps {
			add(name, v.Version, v.Dev)
			if len(v.Dependencies) > 0 {
				var nested map[string]struct {
					Version      string                     `json:"version"`
					Dev          bool                       `json:"dev"`
					Dependencies map[string]json.RawMessage `json:"dependencies"`
				}
				if err := json.Unmarshal(mustMarshal(v.Dependencies), &nested); err == nil {
					walk(nested)
				}
			}
		}
	}
	if len(raw.Packages) == 0 {
		walk(raw.Dependencies)
	}
	return out
}

func mustMarshal(v interface{}) []byte {
	b, _ := json.Marshal(v)
	return b
}

// cleanNpmLockName 从 packages key 提取包名，如 node_modules/@scope/pkg
func cleanNpmLockName(key string) string {
	if strings.Contains(key, "node_modules/") {
		key = key[strings.LastIndex(key, "node_modules/")+len("node_modules/"):]
	}
	return key
}

// poetry.lock: 手写解析 [[package]] 块
func parsePoetryLock(content string) []*Dep {
	var out []*Dep
	var name, version string
	flush := func() {
		if name != "" && version != "" {
			out = append(out, &Dep{Ecosystem: "pypi", Name: name, Version: version, VersionResolved: true})
		}
		name, version = "", ""
	}
	scanner := bufio.NewScanner(strings.NewReader(content))
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "[[package]]") {
			flush()
			continue
		}
		if strings.HasPrefix(line, "name = ") {
			name = trimTomlString(line[len("name = "):])
		} else if strings.HasPrefix(line, "version = ") {
			version = trimTomlString(line[len("version = "):])
		}
	}
	flush()
	return out
}

func trimTomlString(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, `"'`)
	return s
}

// Pipfile.lock: {"default":{...},"develop":{...}}
func parsePipfileLock(content string) []*Dep {
	var raw struct {
		Default map[string]struct {
			Version string `json:"version"`
		} `json:"default"`
		Develop map[string]struct {
			Version string `json:"version"`
		} `json:"develop"`
	}
	if err := json.Unmarshal([]byte(content), &raw); err != nil {
		return nil
	}
	var out []*Dep
	seen := map[string]bool{}
	add := func(m map[string]struct {
		Version string `json:"version"`
	}, scope string) {
		for name, v := range m {
			ver := strings.TrimPrefix(strings.TrimSpace(v.Version), "==")
			if ver == "" {
				continue
			}
			key := strings.ToLower(name) + "@" + ver
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, &Dep{Ecosystem: "pypi", Name: name, Version: ver, VersionResolved: true, Scope: scope})
		}
	}
	add(raw.Default, "runtime")
	add(raw.Develop, "dev")
	return out
}

// requirements.lock: pip-compile 风格 name==version
func parseRequirementLock(content string) []*Dep {
	var out []*Dep
	scanner := bufio.NewScanner(strings.NewReader(content))
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "-") {
			continue
		}
		parts := strings.SplitN(line, "==", 2)
		if len(parts) != 2 {
			continue
		}
		name := strings.TrimSpace(parts[0])
		ver := strings.TrimSpace(strings.SplitN(parts[1], " ", 2)[0])
		if name == "" || ver == "" {
			continue
		}
		out = append(out, &Dep{Ecosystem: "pypi", Name: name, Version: ver, VersionResolved: true})
	}
	return out
}
