package pipeline

import (
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

// getStageLLMEnhanceTimeout 从 ReviewAgentConfig 读取指定阶段的 LLM 增强超时（秒）
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
