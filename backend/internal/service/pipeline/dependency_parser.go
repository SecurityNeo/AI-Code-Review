package pipeline

import (
	"bufio"
	"fmt"
	"regexp"
	"strings"
)

// Dependency 解析出的依赖信息
type Dependency struct {
	Ecosystem  string // Go, npm, pypi, maven
	Name       string
	Version    string
	FilePath   string
	LineNumber int
	Indirect   bool // 仅 Go mod 使用
}

// DependencyParser 依赖解析器接口
type DependencyParser interface {
	Parse(content string, filePath string) ([]Dependency, error)
	Supports(filePath string) bool
}

// ========== GoModParser ==========

type GoModParser struct{}

func (p *GoModParser) Supports(filePath string) bool {
	return strings.HasSuffix(filePath, "go.mod")
}

func (p *GoModParser) Parse(content string, filePath string) ([]Dependency, error) {
	var deps []Dependency
	scanner := bufio.NewScanner(strings.NewReader(content))
	lineNo := 0
	inRequire := false

	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())

		// 单行 require: require github.com/gin-gonic/gin v1.9.1
		if strings.HasPrefix(line, "require ") && !strings.HasSuffix(line, "(") {
			dep := p.parseRequireLine(line, filePath, lineNo)
			if dep != nil {
				deps = append(deps, *dep)
			}
			continue
		}

		// 多行 require 块开始
		if line == "require (" {
			inRequire = true
			continue
		}

		// 多行 require 块结束
		if inRequire && line == ")" {
			inRequire = false
			continue
		}

		// 多行 require 块内容
		if inRequire {
			// 跳过头部的 replace / exclude 等
			if strings.HasPrefix(line, "//") || line == "" {
				continue
			}
			dep := p.parseRequireLine(line, filePath, lineNo)
			if dep != nil {
				deps = append(deps, *dep)
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return deps, nil
}

// 解析单行 require 语句
// 格式: module version [// indirect]
func (p *GoModParser) parseRequireLine(line, filePath string, lineNo int) *Dependency {
	// 去掉行注释前缀
	if idx := strings.Index(line, "//"); idx >= 0 {
		line = strings.TrimSpace(line[:idx])
	}
	if line == "" {
		return nil
	}

	// 格式: module version [// indirect]
	// 先识别 indirect 标记
	indirect := false
	if strings.HasSuffix(line, "// indirect") {
		indirect = true
		line = strings.TrimSpace(strings.TrimSuffix(line, "// indirect"))
	}

	fields := strings.Fields(line)
	if len(fields) < 2 {
		return nil
	}

	module := fields[0]
	version := strings.TrimPrefix(fields[1], "v")
	if version == "" {
		return nil
	}

	return &Dependency{
		Ecosystem:  "Go",
		Name:       module,
		Version:    normalizeGoVersion(fields[1]), // 保留 v 前缀
		FilePath:   filePath,
		LineNumber: lineNo,
		Indirect:   indirect,
	}
}

// normalizeGoVersion 规范化 Go 版本号
// pseudo-version: v0.0.0-20200202-12345678 → v0.0.0-2020020212345678 (统一格式)
func normalizeGoVersion(v string) string {
	v = strings.TrimSpace(v)
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}

	return v
}

// IsPseudoVersion 判断是否为 pseudo-version
// 格式: vX.0.0-yyyymmddhhmmss-abcdefabcdef
func IsPseudoVersion(v string) bool {
	v = strings.TrimPrefix(v, "v")
	re := regexp.MustCompile(`^\d+\.\d+\.\d+-\d{14}-[a-f0-9]{12,}$`)
	return re.MatchString(v)
}

// ========== PackageJSONParser ==========

type PackageJSONParser struct{}

func (p *PackageJSONParser) Supports(filePath string) bool {
	return strings.HasSuffix(filePath, "/package.json") || filePath == "package.json"
}

func (p *PackageJSONParser) Parse(content string, filePath string) ([]Dependency, error) {
	var deps []Dependency
	lineNo := 0
	scanner := bufio.NewScanner(strings.NewReader(content))
	inDeps := false

	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())

		// 识别 dependencies / devDependencies / peerDependencies
		if strings.Contains(line, `"dependencies"`) ||
			strings.Contains(line, `"devDependencies"`) ||
			strings.Contains(line, `"peerDependencies"`) ||
			strings.Contains(line, `"optionalDependencies"`) {
			if strings.Contains(line, "{") {
				inDeps = true
			}
			continue
		}

		if inDeps {
			// 依赖块结束
			if line == "}" || line == "}," {
				inDeps = false
				continue
			}

			// 解析 "name": "version"
			dep := p.parsePackageDepLine(line, filePath, lineNo)
			if dep != nil {
				deps = append(deps, *dep)
			}
		}
	}

	return deps, scanner.Err()
}

func (p *PackageJSONParser) parsePackageDepLine(line, filePath string, lineNo int) *Dependency {
	// 去掉尾部逗号
	line = strings.TrimSuffix(line, ",")
	line = strings.TrimSpace(line)

	// 格式: "name": "^1.2.3" 或 "name": "~1.2.3" 或 "name": "1.2.3"
	re := regexp.MustCompile(`^"([^"]+)":\s*"([^"]+)"`)
	matches := re.FindStringSubmatch(line)
	if len(matches) != 3 {
		return nil
	}

	name := matches[1]
	versionSpec := matches[2]

	// 提取纯版本号（去掉 ^ ~ >= <= > < 等前缀）
	version := extractNpmVersion(versionSpec)
	if version == "" {
		return nil
	}

	return &Dependency{
		Ecosystem:  "npm",
		Name:       name,
		Version:    version,
		FilePath:   filePath,
		LineNumber: lineNo,
	}
}

func extractNpmVersion(spec string) string {
	// 去掉前缀: ^ ~ >= <= > < =
	spec = strings.TrimSpace(spec)
	spec = regexp.MustCompile(`^[\^~>=<\s]+`).ReplaceAllString(spec, "")

	// 处理 range: "1.x" "1.2.x" 等
	if strings.Contains(spec, "x") || strings.Contains(spec, "X") || strings.Contains(spec, "*") {
		// 返回最低版本
		spec = strings.ReplaceAll(spec, "x", "0")
		spec = strings.ReplaceAll(spec, "X", "0")
		spec = strings.ReplaceAll(spec, "*", "0")
	}

	return spec
}

// ========== RequirementsTxtParser ==========

type RequirementsTxtParser struct{}

func (p *RequirementsTxtParser) Supports(filePath string) bool {
	return strings.HasSuffix(filePath, "/requirements.txt") || filePath == "requirements.txt"
}

func (p *RequirementsTxtParser) Parse(content string, filePath string) ([]Dependency, error) {
	var deps []Dependency
	scanner := bufio.NewScanner(strings.NewReader(content))
	lineNo := 0

	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())

		// 跳过注释和空行
		if line == "" || strings.HasPrefix(line, "#") ||
			strings.HasPrefix(line, "-") || strings.HasPrefix(line, "=") {
			continue
		}

		dep := p.parseRequirementLine(line, filePath, lineNo)
		if dep != nil {
			deps = append(deps, *dep)
		}
	}

	return deps, scanner.Err()
}

func (p *RequirementsTxtParser) parseRequirementLine(line, filePath string, lineNo int) *Dependency {
	// 格式: package==1.2.3、package>=1.2.3、package~=1.2.3
	// 也可能有 extras: package[extra]==1.2.3
	re := regexp.MustCompile(`^([a-zA-Z0-9_\-\.\[\]]+)([<>=!~]+)([^;\s]+)`)
	matches := re.FindStringSubmatch(line)
	if len(matches) != 4 {
		return nil
	}

	name := matches[1]
	version := strings.TrimSpace(matches[3])
	// 去掉 extras 标记
	if idx := strings.Index(name, "["); idx > 0 {
		name = name[:idx]
	}

	return &Dependency{
		Ecosystem:  "pypi",
		Name:       name,
		Version:    version,
		FilePath:   filePath,
		LineNumber: lineNo,
	}
}

// ========== PomXMLParser ==========

type PomXMLParser struct{}

func (p *PomXMLParser) Supports(filePath string) bool {
	return strings.HasSuffix(filePath, "/pom.xml") || filePath == "pom.xml"
}

func (p *PomXMLParser) Parse(content string, filePath string) ([]Dependency, error) {
	var deps []Dependency
	scanner := bufio.NewScanner(strings.NewReader(content))
	lineNo := 0
	inDependencies := false
	inDependency := false
	currentDep := Dependency{Ecosystem: "maven", FilePath: filePath}

	for scanner.Scan() {
		lineNo++
		line := scanner.Text()

		if strings.Contains(line, "<dependencies>") {
			inDependencies = true
			continue
		}
		if strings.Contains(line, "</dependencies>") {
			inDependencies = false
			continue
		}

		if !inDependencies {
			continue
		}

		if strings.Contains(line, "<dependency>") {
			inDependency = true
			currentDep = Dependency{Ecosystem: "maven", FilePath: filePath, LineNumber: lineNo}
			continue
		}
		if strings.Contains(line, "</dependency>") {
			inDependency = false
			if currentDep.Name != "" && currentDep.Version != "" {
				deps = append(deps, currentDep)
			}
			continue
		}

		if inDependency {
			if groupID := extractXMLTagValue(line, "groupId"); groupID != "" {
				currentDep.Name = groupID + ":"
			}
			if artifactID := extractXMLTagValue(line, "artifactId"); artifactID != "" {
				currentDep.Name += artifactID
			}
			if version := extractXMLTagValue(line, "version"); version != "" {
				currentDep.Version = version
			}
		}
	}

	return deps, scanner.Err()
}

func extractXMLTagValue(line, tag string) string {
	re := regexp.MustCompile(fmt.Sprintf(`<%s>([^<]+)</%s>`, tag, tag))
	matches := re.FindStringSubmatch(line)
	if len(matches) == 2 {
		return strings.TrimSpace(matches[1])
	}
	return ""
}

// ========== CargoTomlParser ==========

type CargoTomlParser struct{}

func (p *CargoTomlParser) Supports(filePath string) bool {
	return strings.HasSuffix(filePath, "/Cargo.toml") || filePath == "Cargo.toml"
}

func (p *CargoTomlParser) Parse(content string, filePath string) ([]Dependency, error) {
	var deps []Dependency
	scanner := bufio.NewScanner(strings.NewReader(content))
	lineNo := 0
	inDeps := false

	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())

		if strings.HasPrefix(line, "[") {
			if line == "[dependencies]" {
				inDeps = true
				continue
			}
			inDeps = false
			continue
		}

		if !inDeps {
			continue
		}

		dep := p.parseCargoDepLine(line, filePath, lineNo)
		if dep != nil {
			deps = append(deps, *dep)
		}
	}

	return deps, scanner.Err()
}

func (p *CargoTomlParser) parseCargoDepLine(line, filePath string, lineNo int) *Dependency {
	// Direct version: name = "version"
	re := regexp.MustCompile(`^([a-zA-Z0-9_-]+)\s*=\s*"([^"]+)"$`)
	matches := re.FindStringSubmatch(line)
	if len(matches) == 3 {
		return &Dependency{
			Ecosystem:  "cargo",
			Name:       matches[1],
			Version:    matches[2],
			FilePath:   filePath,
			LineNumber: lineNo,
		}
	}

	// Inline table with version: name = { version = "version", ... }
	re = regexp.MustCompile(`^([a-zA-Z0-9_-]+)\s*=\s*\{.*version\s*=\s*"([^"]+)".*\}$`)
	matches = re.FindStringSubmatch(line)
	if len(matches) == 3 {
		return &Dependency{
			Ecosystem:  "cargo",
			Name:       matches[1],
			Version:    matches[2],
			FilePath:   filePath,
			LineNumber: lineNo,
		}
	}

	return nil
}

// ========== 注册解析器 ==========

var dependencyParsers = []DependencyParser{
	&GoModParser{},
	&PackageJSONParser{},
	&RequirementsTxtParser{},
	&PomXMLParser{},
	&CargoTomlParser{},
}

// ParseDependenciesFromFile 根据文件路径自动选择解析器
func ParseDependenciesFromFile(filePath, content string) ([]Dependency, error) {
	for _, parser := range dependencyParsers {
		if parser.Supports(filePath) {
			return parser.Parse(content, filePath)
		}
	}
	return nil, fmt.Errorf("no parser supports file: %s", filePath)
}
