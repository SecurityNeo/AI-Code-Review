package engine

// DefaultExcludePatternsByLanguage 按语言的默认排除规则
// 此列表排除锁文件、依赖目录、构建产物，以及核心依赖清单文件（如 go.mod, package.json）。
// 核心依赖清单由 dependency_scan 阶段直接从 repo_dir 读取，不需要从 diff_files 中获取。
var DefaultExcludePatternsByLanguage = map[string][]string{
	"golang": {
		"go.mod",
		"go.sum",
		"go.work",
		"go.work.sum",
		"vendor/**",
		"Gopkg.toml",
		"Gopkg.lock",
		"glide.yaml",
		"glide.lock",
	},
	"nodejs": {
		"node_modules/**",
		"package.json",
		"package-lock.json",
		"yarn.lock",
		"pnpm-lock.yaml",
		"npm-shrinkwrap.json",
		"dist/**",
		"build/**",
		".next/**",
		".nuxt/**",
		"coverage/**",
	},
	"java": {
		"target/**",
		"build/**",
		"out/**",
		".mvn/**",
		"gradle/wrapper/**",
		"gradle.lockfile",
		"*.class",
		"pom.xml",
		"build.gradle",
	},
	"python": {
		"__pycache__/**",
		"*.pyc",
		".pytest_cache/**",
		"*.egg-info/**",
		"dist/**",
		"build/**",
		"Pipfile.lock",
		"poetry.lock",
		"requirements.txt",
		"setup.py",
		"pyproject.toml",
		"Pipfile",
	},
	"rust": {
		"target/**",
		"Cargo.lock",
		"Cargo.toml",
	},
	"php": {
		"vendor/**",
		"composer.lock",
		"composer.json",
	},
	"ruby": {
		"vendor/bundle/**",
		"Gemfile.lock",
		"Gemfile",
	},
	"cpp": {
		"build/**",
		"cmake-build-*/**",
		"out/**",
		".vs/**",
	},
	"dotnet": {
		"bin/**",
		"obj/**",
		"packages/**",
		"packages.lock.json",
	},
	"dart": {
		"build/**",
		".dart_tool/**",
		"pubspec.lock",
		"pubspec.yaml",
	},
	"swift": {
		".build/**",
		"Package.resolved",
		"Package.swift",
	},
}

// CommonExcludePatterns 通用排除规则（不限语言）
var CommonExcludePatterns = []string{
	".git/**",
	".gitignore",
	".gitattributes",
	".idea/**",
	".vscode/**",
	"*.iml",
	"*.log",
	"logs/**",
	"tmp/**",
	"temp/**",
	"*.tmp",
}
