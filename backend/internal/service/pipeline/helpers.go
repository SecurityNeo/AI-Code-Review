package pipeline

import (
	"path/filepath"
	"strings"
	"time"

	"github.com/ai-optimizer/backend/internal/model"
)

// getASTContextFromStage 从 StageContext 获取 AST 上下文
func getASTContextFromStage(ctx StageContext) *ASTContext {
	if v, ok := ctx.GetInput("ast_structured").(*ASTContext); ok {
		return v
	}
	return nil
}

// strContains 检查切片中是否包含指定字符串
func strContains(arr []string, target string) bool {
	for _, s := range arr {
		if s == target {
			return true
		}
	}
	return false
}

// detectLanguageFromFiles 从文件路径列表检测主要语言
func detectLanguageFromFiles(files []map[string]interface{}) string {
	counts := map[string]int{"golang": 0, "java": 0, "python": 0, "javascript": 0}
	for _, f := range files {
		path, _ := f["path"].(string)
		switch {
		case strings.HasSuffix(path, ".go"):
			counts["golang"]++
		case strings.HasSuffix(path, ".java"):
			counts["java"]++
		case strings.HasSuffix(path, ".py"):
			counts["python"]++
		case strings.HasSuffix(path, ".js"), strings.HasSuffix(path, ".ts"), strings.HasSuffix(path, ".jsx"), strings.HasSuffix(path, ".tsx"):
			counts["javascript"]++
		}
	}
	maxCount, maxLang := 0, "golang"
	for lang, count := range counts {
		if count > maxCount {
			maxCount = count
			maxLang = lang
		}
	}
	return maxLang
}

// getStageLLMEnhanceTimeoutFromCtx 从 StageContext 的配置快照读取指定阶段的 LLM 增强超时（秒）
// 多租户改造：优先从 OrgConfigSnapshot 读取，避免 Stage 间配置不一致。
func getStageLLMEnhanceTimeoutFromCtx(ctx StageContext, stageCode string) time.Duration {
	def := 300 * time.Second
	snap := GetOrgConfigSnapshotFromCtx(ctx)
	if snap != nil && snap.AgentConfig.ID > 0 {
		if sc := snap.AgentConfig.StageConfig(stageCode); sc != nil {
			if t, ok := sc["llm_enhance_timeout"].(float64); ok && t > 0 {
				return time.Duration(t) * time.Second
			}
		}
		return def
	}
	// fallback：向后兼容（旧调用路径无 StageContext 时仍走原逻辑）
	return getStageLLMEnhanceTimeout(stageCode)
}

// getStageLLMEnhanceTimeout 从 ReviewAgentConfig 读取指定阶段的 LLM 增强超时（秒）
// Deprecated: 多租户改造后建议在 StageContext 可用时使用 getStageLLMEnhanceTimeoutFromCtx，
// 以保持 Stage 间配置一致性。
func getStageLLMEnhanceTimeout(stageCode string) time.Duration {
	def := 300 * time.Second
	var agentCfg model.ReviewAgentConfig
	if err := model.DB.First(&agentCfg, 1).Error; err != nil {
		return def
	}
	if sc := agentCfg.StageConfig(stageCode); sc != nil {
		if t, ok := sc["llm_enhance_timeout"].(float64); ok && t > 0 {
			return time.Duration(t) * time.Second
		}
	}
	return def
}

// detectLanguageFromPath 根据单个文件扩展名检测编程语言，用于 parseASTSimple
// 等需要根据文件路径自动选择语言解析器的场景。
func detectLanguageFromPath(filePath string) string {
	if filePath == "" {
		return "unknown"
	}
	ext := strings.ToLower(filepath.Ext(filePath))
	switch ext {
	case ".go":
		return "golang"
	case ".java":
		return "java"
	case ".py":
		return "python"
	case ".js", ".jsx":
		return "javascript"
	case ".ts", ".tsx":
		return "typescript"
	case ".vue":
		// Vue SFC 复用 JavaScript/TypeScript 解析器
		return "javascript"
	case ".php":
		return "php"
	case ".rb":
		return "ruby"
	case ".cpp", ".cc", ".cxx", ".c++", ".h", ".hpp":
		return "cpp"
	default:
		return "unknown"
	}
}
