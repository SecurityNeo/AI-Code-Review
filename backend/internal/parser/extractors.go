package parser

// TreeSitterExtractor 统一提取器接口
type TreeSitterExtractor interface {
	ParseFile(filePath string, content []byte) (*UnifiedAST, error)
	Language() string
	FileExts() []string
}

// ExtractorRegistry 提取器注册中心
type ExtractorRegistry struct {
	extractors map[string]TreeSitterExtractor // language -> extractor
	byExt      map[string]string              // ext -> language
}

// NewExtractorRegistry 创建提取器注册中心
func NewExtractorRegistry() *ExtractorRegistry {
	return &ExtractorRegistry{
		extractors: make(map[string]TreeSitterExtractor),
		byExt: map[string]string{
			".go":   "golang",
			".java": "java",
			".py":   "python",
			".js":   "javascript",
			".ts":   "typescript",
			".jsx":  "javascript",
			".tsx":  "typescript",
		},
	}
}

// Register 注册提取器
func (r *ExtractorRegistry) Register(extractor TreeSitterExtractor) {
	r.extractors[extractor.Language()] = extractor
	// 注册文件扩展名映射
	for _, ext := range extractor.FileExts() {
		r.byExt[ext] = extractor.Language()
	}
}

// GetExtractorByLanguage 按语言获取提取器
func (r *ExtractorRegistry) GetExtractorByLanguage(language string) (TreeSitterExtractor, bool) {
	// 处理别名
	lang := normalizeLanguage(language)
	e, ok := r.extractors[lang]
	return e, ok
}

// GetExtractorByExt 按扩展名获取提取器
func (r *ExtractorRegistry) GetExtractorByExt(ext string) (TreeSitterExtractor, bool) {
	lang, ok := r.byExt[ext]
	if !ok {
		return nil, false
	}
	return r.GetExtractorByLanguage(lang)
}

// GetLanguageByExt 按扩展名获取语言
func (r *ExtractorRegistry) GetLanguageByExt(ext string) string {
	return r.byExt[ext]
}

// GetAllLanguages 获取所有支持的语言
func (r *ExtractorRegistry) GetAllLanguages() []string {
	langs := make([]string, 0, len(r.extractors))
	for lang := range r.extractors {
		langs = append(langs, lang)
	}
	return langs
}

// GlobalRegistry 全局提取器注册中心
var GlobalRegistry = NewExtractorRegistry()

// normalizeLanguage 统一语言名称
func normalizeLanguage(lang string) string {
	switch lang {
	case "golang", "go":
		return "golang"
	case "javascript", "js", "frontend":
		return "javascript"
	case "typescript", "ts":
		return "typescript"
	case "python", "py":
		return "python"
	case "java":
		return "java"
	default:
		return lang
	}
}
