package pipeline

import (
	"context"
	"fmt"
	"strings"

	"github.com/ai-optimizer/backend/internal/engine"
	"github.com/ai-optimizer/backend/internal/model"
	"github.com/ai-optimizer/backend/pkg/llm"
	"go.uber.org/zap"
)

// ========== TestSuggestionExecutor ==========

type TestSuggestionExecutor struct {
	llmService LLMService
	modelID    uint
}

func NewTestSuggestionExecutor(llm LLMService, modelID uint) *TestSuggestionExecutor {
	return &TestSuggestionExecutor{llmService: llm, modelID: modelID}
}

func (e *TestSuggestionExecutor) Code() string { return "test_suggestion" }

func (e *TestSuggestionExecutor) Execute(ctx StageContext) error {
	// 检查任务是否已被取消
	select {
	case <-ctx.Done():
		return fmt.Errorf("任务已取消")
	default:
	}

	// 1. 获取 AST 结构化数据（复用 context_extract 的产出）
	var astStructured *ASTContext
	if v, ok := ctx.GetInput("ast_structured").(*ASTContext); ok {
		astStructured = v
	}

	if astStructured == nil {
		// 尝试从上下文获取 ast_context 文本做简化分析
		astText, _ := ctx.GetInput("ast_context").(string)
		if astText == "" {
			return nil // 无 AST 数据，跳过
		}
		return e.suggestFromASTText(ctx, astText)
	}

	// 2. 筛选新增/修改的函数（对比 diff 文件列表）
	changedFiles := getChangedFilePathsFromContext(ctx)

	var suggestions []engine.TestSuggestionItem

	for _, fn := range astStructured.Functions {
		// 只关注变更文件中的函数
		if !isInChangedFiles(fn.FilePath, changedFiles) {
			continue
		}

		scenarios := analyzeFunctionForTests(fn)
		if len(scenarios) > 0 {
			suggestions = append(suggestions, engine.TestSuggestionItem{
				FunctionName: fn.Name,
				FilePath:     fn.FilePath,
				LineNumber:   fn.LineStart,
				Scenarios:    scenarios,
			})
		}
	}

	// 3. LLM 增强（Phase 3）
	var enricher *AgentEnricher
	if e.llmService != nil && len(suggestions) > 0 {
		task := ctx.Task()
		fileContents, _ := ctx.GetOutput("file_contents").(map[string]string)
		modelID := e.modelID
		if modelID == 0 && task != nil && task.UsedModelID > 0 {
			modelID = task.UsedModelID
		}
		// modelID 可以为 0（LLMService.ChatCompletionStructured 会走全局主备链路获取主模型）
		enrichTimeout := getStageLLMEnhanceTimeout("test_suggestion")
		enricher = NewAgentEnricher(e.llmService, EnricherConfig{
			ModelID:       modelID,
			MaxTokens:     3000,
			EnrichTimeout: enrichTimeout,
		})
		enriched, err := enricher.EnrichTestSuggestions(context.Background(), &task.ID, modelID, suggestions, fileContents)
		if err == nil && len(enriched) > 0 {
			for funcName, scenarios := range enriched {
				for i := range suggestions {
					if suggestions[i].FunctionName == funcName {
						var llmScenarios []llm.TestScenario
						for _, sc := range scenarios {
							llmScenarios = append(llmScenarios, llm.TestScenario{
								Name:      sc.Name,
								Input:     sc.Input,
								Expected:  sc.Expected,
								Reasoning: sc.Reasoning,
								Priority:  sc.Priority,
							})
						}
						if len(llmScenarios) > 0 {
							suggestions[i].Scenarios = llmScenarios
						}
						break
					}
				}
			}
			ctx.SetOutput("test_suggestion_llm_enriched", true)
		} else if err != nil {
			zap.L().Warn("test_suggestion LLM enrichment failed, using heuristic results", zap.Error(err))
		}
	}

	// 4. 产出注入
	ctx.SetOutput("test_suggestions", suggestions)

	if len(suggestions) > 0 {
		ctx.SetOutput("test_suggestion_markdown", buildTestSuggestionMarkdown(suggestions))
	}

	llmPrompt := ""
	llmRawOutput := ""
	modelName := ""
	inputTokens := 0
	outputTokens := 0
	if enricher != nil {
		llmPrompt = enricher.LastPrompt()
		llmRawOutput = enricher.LastRawContent()
		modelName = enricher.LastModelName()
		inputTokens = enricher.LastInputTokens()
		outputTokens = enricher.LastOutputTokens()
	}

	// 5. 保存 Pipeline 快照
	ctx.SaveInputSnapshot(&model.TaskPipelineExecution{ID: ctx.ExecutionID()}, map[string]interface{}{
		"function_count": len(astStructured.Functions),
		"changed_files":  len(changedFiles),
	})
	ctx.SaveOutputSnapshot(&model.TaskPipelineExecution{ID: ctx.ExecutionID()}, map[string]interface{}{
		"suggestion_count":     len(suggestions),
		"suggestions":          suggestions,
		"llm_prompt":           llmPrompt,
		"llm_prompt_available": llmPrompt != "",
		"llm_raw_output":       llmRawOutput,
		"model_name":           modelName,
		"input_tokens":         inputTokens,
		"output_tokens":        outputTokens,
	})

	return nil
}

func (e *TestSuggestionExecutor) suggestFromASTText(ctx StageContext, astText string) error {
	// 简化分析：从 AST 文本中提取函数签名，做基础建议
	// 实际场景下 context_extract 会产出结构化 AST，此处为降级处理
	return nil
}

func getChangedFilePathsFromContext(ctx StageContext) []string {
	files, ok := ctx.GetInput("diff_files").([]map[string]interface{})
	if !ok {
		return nil
	}
	var paths []string
	for _, f := range files {
		if p, ok := f["path"].(string); ok && p != "" {
			paths = append(paths, p)
		}
	}
	return paths
}

func isInChangedFiles(filePath string, changedFiles []string) bool {
	for _, cf := range changedFiles {
		if cf == filePath {
			return true
		}
	}
	return false
}

func analyzeFunctionForTests(fn FunctionSignature) []llm.TestScenario {
	var scenarios []llm.TestScenario

	// 检查 error 返回值
	hasError := false
	for _, ret := range fn.Returns {
		if strings.Contains(strings.ToLower(ret), "error") {
			hasError = true
			break
		}
	}
	if hasError {
		scenarios = append(scenarios, llm.TestScenario{
			Name:      "错误返回值场景",
			Input:     "参数无效",
			Expected:  "返回 error",
			Reasoning: "函数签名包含 error 返回值",
			Priority:  "should_have",
		})
	}

	// 检查函数体关键词（基于 FunctionSignature.Body 文本）
	lowerBody := strings.ToLower(fn.Body)

	// if 分支
	ifCount := strings.Count(lowerBody, "if ")
	if ifCount > 0 {
		scenarios = append(scenarios, llm.TestScenario{
			Name:      fmt.Sprintf("if 分支覆盖（%d 个条件）", ifCount),
			Input:     "满足/不满足各 if 条件",
			Expected:  "覆盖所有分支路径",
			Reasoning: fmt.Sprintf("函数体包含 %d 个 if 条件分支", ifCount),
			Priority:  "should_have",
		})
	}

	// switch 分支
	if strings.Contains(lowerBody, "switch ") {
		switchCount := strings.Count(lowerBody, "case ")
		if switchCount > 0 {
			scenarios = append(scenarios, llm.TestScenario{
				Name:      fmt.Sprintf("switch 分支覆盖（%d 个 case）", switchCount),
				Input:     "匹配各 case 及 default",
				Expected:  "覆盖所有 case 路径",
				Reasoning: fmt.Sprintf("函数体包含 switch，共 %d 个 case", switchCount),
				Priority:  "should_have",
			})
		}
	}

	// panic/recover
	if strings.Contains(lowerBody, "panic(") {
		scenarios = append(scenarios, llm.TestScenario{
			Name:      "panic 触发与 recover",
			Input:     "触发 panic 的条件",
			Expected:  "recover 正常恢复或 panic 传播",
			Reasoning: "函数体包含 panic 调用",
			Priority:  "nice_to_have",
		})
	}

	// nil 检查
	if strings.Contains(lowerBody, "nil") {
		scenarios = append(scenarios, llm.TestScenario{
			Name:      "nil 指针参数处理",
			Input:     "nil 参数",
			Expected:  "不 panic，返回预期结果或 error",
			Reasoning: "函数体包含 nil 检查逻辑",
			Priority:  "should_have",
		})
	}

	// 边界值（基于参数类型推断）
	for i, param := range fn.Params {
		paramType := strings.ToLower(param)
		// 提取参数名（如果格式是 "name type"）
		paramName := fmt.Sprintf("参数%d", i+1)
		parts := strings.Fields(param)
		if len(parts) >= 2 {
			paramName = parts[0]
			paramType = strings.ToLower(strings.Join(parts[1:], " "))
		}
		if isNumericTypeStr(paramType) {
			scenarios = append(scenarios, llm.TestScenario{
				Name:      fmt.Sprintf("边界值测试：%s", paramName),
				Input:     fmt.Sprintf("%s = 0 / 最大值 / 负值", paramName),
				Expected:  "正确处理边界值",
				Reasoning: fmt.Sprintf("%s 为数值类型，需测试边界值", paramName),
				Priority:  "nice_to_have",
			})
		}
		if isStringTypeStr(paramType) {
			scenarios = append(scenarios, llm.TestScenario{
				Name:      fmt.Sprintf("边界值测试：%s", paramName),
				Input:     fmt.Sprintf("%s = 空字符串 / 超长字符串", paramName),
				Expected:  "正确处理边界值",
				Reasoning: fmt.Sprintf("%s 为字符串类型，需测试边界值", paramName),
				Priority:  "nice_to_have",
			})
		}
	}

	return scenarios
}

func isNumericTypeStr(t string) bool {
	numericTypes := []string{"int", "int8", "int16", "int32", "int64", "uint", "uint8", "uint16", "uint32", "uint64", "float32", "float64", "byte", "rune"}
	lower := strings.ToLower(t)
	for _, nt := range numericTypes {
		if lower == nt || strings.HasPrefix(lower, nt+" ") {
			return true
		}
	}
	return false
}

func isStringTypeStr(t string) bool {
	lower := strings.ToLower(t)
	return lower == "string" || strings.HasPrefix(lower, "string ")
}

func buildTestSuggestionMarkdown(suggestions []engine.TestSuggestionItem) string {
	if len(suggestions) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("## 测试建议\n\n以下函数建议补充测试场景：\n\n")
	sb.WriteString("| 函数 | 文件 | 建议测试场景 |\n")
	sb.WriteString("|:---|:---|:---|\n")

	for _, s := range suggestions {
		var sceneNames []string
		for _, sc := range s.Scenarios {
			sceneNames = append(sceneNames, sc.Name)
		}
		scenes := strings.Join(sceneNames, " · ")
		sb.WriteString(fmt.Sprintf("| `%s` | `%s:%d` | %s |\n", s.FunctionName, s.FilePath, s.LineNumber, scenes))
	}

	sb.WriteString("\n> 注：以上为基于代码分支结构的自动分析建议，实际测试场景需结合业务逻辑判断。\n")
	return sb.String()
}

// ========== ImpactAnalysisExecutor ==========

type ImpactAnalysisExecutor struct {
	llmService LLMService
	modelID    uint
}

func NewImpactAnalysisExecutor(llm LLMService, modelID uint) *ImpactAnalysisExecutor {
	return &ImpactAnalysisExecutor{llmService: llm, modelID: modelID}
}

func (e *ImpactAnalysisExecutor) Code() string { return "impact_analysis" }

func (e *ImpactAnalysisExecutor) Execute(ctx StageContext) error {
	// 1. 获取跨文件调用链（依赖 context_extract 的产出）
	crossFileText := getCrossFileCallChainText(ctx)
	if crossFileText == "" {
		ctx.SetOutput("impact_analysis_skipped_reason", "缺少跨文件调用链数据（代码理解器未启用或深度为0）")
		return nil
	}

	// 2. 获取 AST 上下文
	astCtx := getASTContextFromStage(ctx)
	if astCtx == nil {
		return nil
	}

	// 检查任务是否已被取消
	select {
	case <-ctx.Done():
		return fmt.Errorf("任务已取消")
	default:
	}

	var findings []engine.ImpactFinding

	// 3. 分析导出函数签名变更
	findings = append(findings, e.analyzeFunctionSignatureChanges(astCtx, crossFileText)...)

	// 4. 分析结构体字段变更
	findings = append(findings, e.analyzeStructChanges(astCtx, crossFileText)...)

	// 5. 分析配置文件变更
	findings = append(findings, e.analyzeConfigFiles(ctx)...)

	// 6. LLM 增强（Phase 3）
	var enricher *AgentEnricher
	if e.llmService != nil && len(findings) > 0 {
		task := ctx.Task()
		crossFileText := getCrossFileCallChainText(ctx)
		callChains := make(map[string][]string)
		for _, f := range findings {
			callChains[f.SymbolName] = extractAffectedFiles(crossFileText, f.SymbolName)
		}
		modelID := e.modelID
		if modelID == 0 && task != nil && task.UsedModelID > 0 {
			modelID = task.UsedModelID
		}
		// modelID 可以为 0（LLMService.ChatCompletionStructured 会走全局主备链路获取主模型）
		enrichTimeout := getStageLLMEnhanceTimeout("impact_analysis")
		enricher = NewAgentEnricher(e.llmService, EnricherConfig{
			ModelID:       modelID,
			MaxTokens:     3000,
			EnrichTimeout: enrichTimeout,
		})
		enriched, err := enricher.EnrichImpactAnalysis(context.Background(), &task.ID, modelID, findings, callChains)
		if err == nil && len(enriched) > 0 {
			for i := range findings {
				if note, ok := enriched[findings[i].SymbolName]; ok {
					if note.MigrationSteps != "" {
						findings[i].Suggestion = note.MigrationSteps
					}
					if note.Compatibility != "" {
						findings[i].Type = note.Compatibility
					}
				}
			}
			ctx.SetOutput("impact_analysis_llm_enriched", true)
		} else if err != nil {
			zap.L().Warn("impact_analysis LLM enrichment failed, using heuristic results", zap.Error(err))
		}
	}

	// 7. 产出注入
	ctx.SetOutput("impact_analysis_findings", findings)

	if len(findings) > 0 {
		ctx.SetOutput("impact_analysis_markdown", buildImpactAnalysisMarkdown(findings))
	}

	llmPrompt := ""
	llmRawOutput := ""
	modelName := ""
	inputTokens := 0
	outputTokens := 0
	if enricher != nil {
		llmPrompt = enricher.LastPrompt()
		llmRawOutput = enricher.LastRawContent()
		modelName = enricher.LastModelName()
		inputTokens = enricher.LastInputTokens()
		outputTokens = enricher.LastOutputTokens()
	}

	// 7. 保存 Pipeline 快照
	ctx.SaveInputSnapshot(&model.TaskPipelineExecution{ID: ctx.ExecutionID()}, map[string]interface{}{
		"cross_file_available": crossFileText != "",
		"function_count":       len(astCtx.Functions),
		"struct_count":         len(astCtx.Structs),
	})
	ctx.SaveOutputSnapshot(&model.TaskPipelineExecution{ID: ctx.ExecutionID()}, map[string]interface{}{
		"finding_count":        len(findings),
		"findings":             findings,
		"llm_prompt":           llmPrompt,
		"llm_prompt_available": llmPrompt != "",
		"llm_raw_output":       llmRawOutput,
		"model_name":           modelName,
		"input_tokens":         inputTokens,
		"output_tokens":        outputTokens,
	})

	return nil
}

func (e *ImpactAnalysisExecutor) analyzeFunctionSignatureChanges(astCtx *ASTContext, crossFileText string) []engine.ImpactFinding {
	var findings []engine.ImpactFinding

	for _, fn := range astCtx.Functions {
		if !isExportedSymbol(fn.Name) {
			continue
		}

		// 检查是否有跨文件调用者
		if !strings.Contains(crossFileText, fn.Name) {
			continue
		}

		// 如果函数签名看起来有变更（基于 heuristics：函数体较大变化）
		if len(fn.Body) > 0 && hasSignatureChangeIndicators(fn) {
			// 从 cross_file_text 中提取受影响文件
			affectedFiles := extractAffectedFiles(crossFileText, fn.Name)
			if len(affectedFiles) > 0 {
				findings = append(findings, engine.ImpactFinding{
					Type:          "breaking_change",
					FilePath:      fn.FilePath,
					SymbolName:    fn.Name,
					ChangeDesc:    "导出函数可能存在签名变更",
					AffectedFiles: affectedFiles,
					Severity:      "critical",
					Suggestion:    "确认是否为有意为之的 API 变更，如有下游调用者需要同步更新",
				})
			}
		}
	}

	return findings
}

func (e *ImpactAnalysisExecutor) analyzeStructChanges(astCtx *ASTContext, crossFileText string) []engine.ImpactFinding {
	var findings []engine.ImpactFinding

	for _, st := range astCtx.Structs {
		if !isExportedSymbol(st.Name) {
			continue
		}

		// 检查结构体是否有外部使用
		if strings.Contains(crossFileText, st.Name) {
			findings = append(findings, engine.ImpactFinding{
				Type:       "schema_change",
				FilePath:   st.FilePath,
				SymbolName: st.Name,
				ChangeDesc: "结构体变更可能影响序列化/反序列化",
				Severity:   "high",
				Suggestion: "确认 JSON/XML/proto 序列化是否受影响，确认数据库迁移是否同步",
			})
		}
	}

	return findings
}

func (e *ImpactAnalysisExecutor) analyzeConfigFiles(ctx StageContext) []engine.ImpactFinding {
	var findings []engine.ImpactFinding

	files, ok := ctx.GetInput("diff_files").([]map[string]interface{})
	if !ok {
		return findings
	}

	for _, f := range files {
		path, _ := f["path"].(string)
		if path == "" {
			continue
		}

		lowerPath := strings.ToLower(path)
		if strings.Contains(lowerPath, "migration") || strings.Contains(lowerPath, "migrate") {
			findings = append(findings, engine.ImpactFinding{
				Type:       "migration",
				FilePath:   path,
				SymbolName: "DB Migration",
				ChangeDesc: "数据库迁移文件发生变更",
				Severity:   "high",
				Suggestion: "请验证数据迁移脚本是否可回滚，确认是否影响现有数据",
			})
		}

		if strings.HasSuffix(lowerPath, ".yaml") || strings.HasSuffix(lowerPath, ".yml") ||
			strings.HasSuffix(lowerPath, ".json") || strings.HasSuffix(lowerPath, ".toml") {
			findings = append(findings, engine.ImpactFinding{
				Type:       "config_change",
				FilePath:   path,
				SymbolName: "Config File",
				ChangeDesc: "配置文件发生变更",
				Severity:   "medium",
				Suggestion: "确认配置变更是否需要在部署时同步更新环境变量或配置中心",
			})
		}
	}

	return findings
}

func isExportedSymbol(name string) bool {
	if name == "" {
		return false
	}
	// Go: 大写开头；简化处理
	first := name[0]
	return first >= 'A' && first <= 'Z'
}

func hasSignatureChangeIndicators(fn FunctionSignature) bool {
	// 简化 heuristics：如果函数参数/返回值看起来有变化（这里用占位逻辑）
	// 实际更精确的实现需要对比基线版本的函数签名
	// 当前简化：有参数的导出函数都提示检查
	return len(fn.Params) > 0
}

func extractAffectedFiles(crossFileText, symbolName string) []string {
	var files []string
	lines := strings.Split(crossFileText, "\n")
	for _, line := range lines {
		if strings.Contains(line, symbolName) && strings.Contains(line, "文件:") {
			parts := strings.Split(line, "文件:")
			if len(parts) > 1 {
				file := strings.TrimSpace(parts[1])
				if file != "" && !strContains(files, file) {
					files = append(files, file)
				}
			}
		}
	}
	return files
}

func buildImpactAnalysisMarkdown(findings []engine.ImpactFinding) string {
	if len(findings) == 0 {
		return ""
	}
	var sb strings.Builder

	// 分类统计
	var breakingChanges []engine.ImpactFinding
	var others []engine.ImpactFinding
	for _, f := range findings {
		if f.Type == "breaking_change" {
			breakingChanges = append(breakingChanges, f)
		} else {
			others = append(others, f)
		}
	}

	if len(breakingChanges) > 0 {
		sb.WriteString(fmt.Sprintf("## 变更影响分析\n\n⚠️ 发现 %d 处潜在 Breaking Change：\n\n", len(breakingChanges)))
		for _, f := range breakingChanges {
			sb.WriteString(fmt.Sprintf("**`%s`** 在 `%s` 发生变更\n", f.SymbolName, f.FilePath))
			sb.WriteString(fmt.Sprintf("- 影响范围：%s\n", strings.Join(f.AffectedFiles, ", ")))
			sb.WriteString(fmt.Sprintf("- 建议：%s\n\n", f.Suggestion))
		}
	}

	if len(others) > 0 {
		sb.WriteString("### 其他变更提示\n\n")
		for _, f := range others {
			sb.WriteString(fmt.Sprintf("- **%s** `%s` — %s\n", f.Type, f.FilePath, f.ChangeDesc))
		}
	}

	return sb.String()
}
