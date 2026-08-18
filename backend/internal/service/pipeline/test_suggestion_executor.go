package pipeline

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

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

	// 读取智能体配置
	var cfg *model.ReviewAgentConfig
	if v, ok := ctx.GetInput("_agent_config").(model.ReviewAgentConfig); ok {
		cfg = &v
	}

	// 读取本阶段可配置参数
	llmMaxTokens := 3000
	llmTimeoutSec := 300
	functionBodyMaxLen := 2000
	contextLinesBefore := 3
	contextLinesAfter := 10
	if cfg != nil {
		llmMaxTokens = cfg.GetStageParam("test_suggestion", "llm_max_tokens", llmMaxTokens)
		llmTimeoutSec = cfg.GetStageParam("test_suggestion", "llm_timeout_sec", llmTimeoutSec)
		functionBodyMaxLen = cfg.GetStageParam("test_suggestion", "function_body_max_len", functionBodyMaxLen)
		contextLinesBefore = cfg.GetStageParam("test_suggestion", "context_lines_before", contextLinesBefore)
		contextLinesAfter = cfg.GetStageParam("test_suggestion", "context_lines_after", contextLinesAfter)
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

	// 2.1 已有测试覆盖信息注入
	for i := range suggestions {
		testCallers := findTestCallersForFunction(ctx, suggestions[i].FunctionName, suggestions[i].FilePath)
		if len(testCallers) > 0 {
			var lines []string
			for _, tc := range testCallers {
				lines = append(lines, fmt.Sprintf("- %s:%d %s", tc.File, tc.Line, tc.Function))
			}
			suggestions[i].ExistingTests = strings.Join(lines, "\n")
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
		enricher = NewAgentEnricher(e.llmService, EnricherConfig{
			ModelID:            modelID,
			MaxTokens:          llmMaxTokens,
			EnrichTimeout:      time.Duration(llmTimeoutSec) * time.Second,
			FunctionBodyMaxLen: functionBodyMaxLen,
			ContextLinesBefore: contextLinesBefore,
			ContextLinesAfter:  contextLinesAfter,
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

	testSuggestionMarkdown := ""
	if len(suggestions) > 0 {
		testSuggestionMarkdown = buildTestSuggestionMarkdown(suggestions)
		ctx.SetOutput("test_suggestion_markdown", testSuggestionMarkdown)
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
		"prompt_injection":     testSuggestionMarkdown,
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
	sb.WriteString("## 测试建议报告\n\n以下函数建议补充测试场景：\n\n")
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
	// 读取智能体配置
	var cfg *model.ReviewAgentConfig
	if v, ok := ctx.GetInput("_agent_config").(model.ReviewAgentConfig); ok {
		cfg = &v
	}

	// 读取本阶段可配置参数
	llmMaxTokens := 3000
	llmTimeoutSec := 300
	callerSnippetMaxLen := 500
	maxCallersPerFinding := 3
	if cfg != nil {
		llmMaxTokens = cfg.GetStageParam("impact_analysis", "llm_max_tokens", llmMaxTokens)
		llmTimeoutSec = cfg.GetStageParam("impact_analysis", "llm_timeout_sec", llmTimeoutSec)
		callerSnippetMaxLen = cfg.GetStageParam("impact_analysis", "caller_snippet_max_len", callerSnippetMaxLen)
		maxCallersPerFinding = cfg.GetStageParam("impact_analysis", "max_callers_per_finding", maxCallersPerFinding)
	}

	// 1. 获取跨文件调用链（依赖 context_extract / code_understanding 的产出）
	crossFileText := getCrossFileCallChainText(ctx)
	if crossFileText == "" {
		// 保存快照说明跳过原因，避免前端详情为空
		ctx.SaveInputSnapshot(&model.TaskPipelineExecution{ID: ctx.ExecutionID()}, map[string]interface{}{
			"cross_file_available": false,
			"reason":               "缺少跨文件调用链数据（代码理解器未启用或深度为0）",
		})
		ctx.SaveOutputSnapshot(&model.TaskPipelineExecution{ID: ctx.ExecutionID()}, map[string]interface{}{
			"finding_count":  0,
			"findings":       []engine.ImpactFinding{},
			"skipped_reason": "缺少跨文件调用链数据（代码理解器未启用或深度为0）",
		})
		return nil
	}

	// 2. 获取 AST 上下文
	astCtx := getASTContextFromStage(ctx)
	if astCtx == nil {
		ctx.SaveInputSnapshot(&model.TaskPipelineExecution{ID: ctx.ExecutionID()}, map[string]interface{}{
			"cross_file_available": true,
			"reason":               "缺少 AST 上下文",
		})
		ctx.SaveOutputSnapshot(&model.TaskPipelineExecution{ID: ctx.ExecutionID()}, map[string]interface{}{
			"finding_count":  0,
			"findings":       []engine.ImpactFinding{},
			"skipped_reason": "缺少 AST 上下文",
		})
		return nil
	}

	// 检查任务是否已被取消
	select {
	case <-ctx.Done():
		return fmt.Errorf("任务已取消")
	default:
	}

	// 获取当前 fileContents 和基线 fileContents 用于对比
	fileContents, _ := ctx.GetOutput("file_contents").(map[string]string)
	baselineContents := make(map[string]string)
	if repoPath, ok := ctx.GetInput("repo_path").(string); ok && repoPath != "" {
		// 并发获取基线文件（限制 10 并发，避免 git 进程爆炸）
		var wg sync.WaitGroup
		var mu sync.Mutex
		sem := make(chan struct{}, 10)
		for path := range fileContents {
			wg.Add(1)
			go func(p string) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				oldContent := getBaselineFileContent(repoPath, p)
				if oldContent != "" {
					mu.Lock()
					baselineContents[p] = oldContent
					mu.Unlock()
				}
			}(path)
		}
		wg.Wait()
	}

	var findings []engine.ImpactFinding

	// 3. 分析导出函数签名变更
	findings = append(findings, e.analyzeFunctionSignatureChanges(astCtx, crossFileText, fileContents, baselineContents)...)

	// 4. 分析结构体字段变更
	findings = append(findings, e.analyzeStructChanges(astCtx, crossFileText, fileContents, baselineContents)...)

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
		enricher = NewAgentEnricher(e.llmService, EnricherConfig{
			ModelID:              modelID,
			MaxTokens:            llmMaxTokens,
			EnrichTimeout:        time.Duration(llmTimeoutSec) * time.Second,
			CallerSnippetMaxLen:  callerSnippetMaxLen,
			MaxCallersPerFinding: maxCallersPerFinding,
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

	impactAnalysisMarkdown := ""
	if len(findings) > 0 {
		impactAnalysisMarkdown = buildImpactAnalysisMarkdown(findings)
		ctx.SetOutput("impact_analysis_markdown", impactAnalysisMarkdown)
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
		"prompt_injection":     impactAnalysisMarkdown,
	})

	return nil
}

func (e *ImpactAnalysisExecutor) analyzeFunctionSignatureChanges(astCtx *ASTContext, crossFileText string, fileContents, baselineContents map[string]string) []engine.ImpactFinding {
	var findings []engine.ImpactFinding

	for _, fn := range astCtx.Functions {
		if !isExportedSymbol(fn.Name) {
			continue
		}

		// 检查是否有跨文件调用者
		if !strings.Contains(crossFileText, fn.Name) {
			continue
		}

		// 基线对比：提取变更前/后签名
		var beforeSig, afterSig string
		if oldContent, ok := baselineContents[fn.FilePath]; ok && oldContent != "" {
			oldAST := parseASTSimple(oldContent, fn.FilePath)
			for _, oldFn := range oldAST.Functions {
				if oldFn.Name == fn.Name {
					beforeSig = oldFn.Signature()
					break
				}
			}
		}
		// 变更后签名从当前 AST 提取
		afterSig = fn.Signature()

		// 如果签名确实发生了变化，或者没有基线数据但启发式指示潜在变更
		isDiff := beforeSig != "" && afterSig != "" && beforeSig != afterSig
		if isDiff || (beforeSig == "" && len(fn.Body) > 0 && hasSignatureChangeIndicators(fn)) {
			affectedFiles := extractAffectedFiles(crossFileText, fn.Name)
			if len(affectedFiles) > 0 {
				changeDesc := "导出函数可能存在签名变更"
				if isDiff {
					changeDesc = fmt.Sprintf("导出函数签名变更: %s → %s", beforeSig, afterSig)
				}
				findings = append(findings, engine.ImpactFinding{
					Type:          "breaking_change",
					FilePath:      fn.FilePath,
					SymbolName:    fn.Name,
					ChangeDesc:    changeDesc,
					AffectedFiles: affectedFiles,
					Severity:      "critical",
					Suggestion:    "确认是否为有意为之的 API 变更，如有下游调用者需要同步更新",
					BeforeSig:     beforeSig,
					AfterSig:      afterSig,
				})
			}
		}
	}

	return findings
}

func (e *ImpactAnalysisExecutor) analyzeStructChanges(astCtx *ASTContext, crossFileText string, fileContents, baselineContents map[string]string) []engine.ImpactFinding {
	var findings []engine.ImpactFinding

	for _, st := range astCtx.Structs {
		if !isExportedSymbol(st.Name) {
			continue
		}

		// 基线对比：提取变更前/后字段列表
		var beforeFields, afterFields []string
		if oldContent, ok := baselineContents[st.FilePath]; ok && oldContent != "" {
			oldAST := parseASTSimple(oldContent, st.FilePath)
			for _, oldSt := range oldAST.Structs {
				if oldSt.Name == st.Name {
					beforeFields = oldSt.Fields
					break
				}
			}
		}
		afterFields = st.Fields

		// 检查结构体是否有外部使用
		if strings.Contains(crossFileText, st.Name) {
			changeDesc := "结构体变更可能影响序列化/反序列化"
			if len(beforeFields) > 0 || len(afterFields) > 0 {
				changed := compareStructFields(beforeFields, afterFields)
				if changed != "" {
					changeDesc = "结构体字段变更: " + changed
				}
			}
			findings = append(findings, engine.ImpactFinding{
				Type:         "schema_change",
				FilePath:     st.FilePath,
				SymbolName:   st.Name,
				ChangeDesc:   changeDesc,
				Severity:     "high",
				Suggestion:   "确认 JSON/XML/proto 序列化是否受影响，确认数据库迁移是否同步",
				BeforeFields: beforeFields,
				AfterFields:  afterFields,
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
		sb.WriteString(fmt.Sprintf("## 变更影响分析报告\n\n⚠️ 发现 %d 处潜在 Breaking Change：\n\n", len(breakingChanges)))
		for _, f := range breakingChanges {
			sb.WriteString(fmt.Sprintf("**`%s`** 在 `%s` 发生变更\n", f.SymbolName, f.FilePath))
			sb.WriteString(fmt.Sprintf("- 影响范围：%s\n", strings.Join(f.AffectedFiles, ", ")))
			sb.WriteString(fmt.Sprintf("- 建议：%s\n\n", f.Suggestion))
		}
	}

	if len(others) > 0 {
		// 按 (Type, ChangeDesc) 分组，避免重复描述浪费 Token
		type groupKey struct {
			Type       string
			ChangeDesc string
		}
		groups := make(map[groupKey][]engine.ImpactFinding)
		for _, f := range others {
			k := groupKey{Type: f.Type, ChangeDesc: f.ChangeDesc}
			groups[k] = append(groups[k], f)
		}

		sb.WriteString("### 其他变更提示\n\n")
		for k, items := range groups {
			sb.WriteString(fmt.Sprintf("以下%s：\n", k.ChangeDesc))
			for _, f := range items {
				sb.WriteString(fmt.Sprintf("- **%s** `%s` (`%s`)\n", k.Type, f.SymbolName, f.FilePath))
			}
			sb.WriteString("\n")
		}
	}

	return sb.String()
}

// TestCallerInfo 测试调用者信息
type TestCallerInfo struct {
	File     string
	Line     int
	Function string
}

// findTestCallersForFunction 查找当前函数被哪些测试函数调用。
// 利用 cross_file_call_chain 从 StageContext 获取调用链数据，筛选出测试文件中的调用者。
func findTestCallersForFunction(ctx StageContext, funcName, filePath string) []TestCallerInfo {
	var callers []TestCallerInfo

	// 尝试从 cross_file_call_chain 文本中解析调用者信息
	crossFileText := getCrossFileCallChainText(ctx)
	if crossFileText == "" {
		return callers
	}

	lines := strings.Split(crossFileText, "\n")
	for _, line := range lines {
		// 精确匹配目标函数在最后一个箭头右侧且被反引号包裹：-> `funcName`
		// 避免子串误报（如 GetUser 误匹配 GetUserProfile），同时避免箭头左侧同名误报
		lastArrow := strings.LastIndex(line, "->")
		if lastArrow < 0 {
			continue
		}
		afterArrow := strings.TrimSpace(line[lastArrow+2:])
		if !strings.HasPrefix(afterArrow, fmt.Sprintf("`%s`", funcName)) {
			continue
		}
		// 进一步确认该行是测试文件中的调用（文件名包含 _test.go）
		if strings.Contains(line, "_test.go") {
			// 简单解析：提取文件路径和函数名
			// 格式示例："- caller_test.go:45 `TestCreateUser` -> `CreateUser`"
			parts := strings.Split(line, "->")
			if len(parts) >= 2 {
				left := strings.TrimSpace(parts[0])
				// 提取 caller 函数名（在 `` 中）
				if start := strings.Index(left, "`"); start >= 0 {
					if end := strings.Index(left[start+1:], "`"); end >= 0 {
						callerFunc := left[start+1 : start+1+end]
						// 提取文件路径和行号
						if fileEnd := strings.Index(left, "`"); fileEnd > 0 {
							locPart := strings.TrimSpace(left[:fileEnd])
							// locPart 形如 "- caller_test.go:45 "
							locPart = strings.TrimPrefix(locPart, "-")
							locPart = strings.TrimSpace(locPart)
							fileLine := strings.SplitN(locPart, ":", 2)
							if len(fileLine) == 2 {
								cLine, _ := strconv.Atoi(fileLine[1])
								callers = append(callers, TestCallerInfo{
									File:     fileLine[0],
									Line:     cLine,
									Function: callerFunc,
								})
							}
						}
					}
				}
			}
		}
	}

	return callers
}

// getBaselineFileContent 通过 git show HEAD:path 获取基线版本的文件内容。
func getBaselineFileContent(repoPath, filePath string) string {
	if repoPath == "" || filePath == "" {
		return ""
	}
	// 命令默认超时 30 秒，防止 git 进程挂起阻塞 pipeline
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// 移除 filePath 前导斜杠，防止 git 将其解析为绝对路径
	cleanPath := strings.TrimPrefix(filePath, "/")
	cmd := exec.CommandContext(ctx, "git", "-C", repoPath, "show", fmt.Sprintf("HEAD:%s", cleanPath))
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return string(out)
}

// parseASTSimple 对单文件内容做简化的 AST 解析，返回仅包含函数和结构体的 ASTContext。
// 这是基线对比的辅助函数，不需要完整的 Tree-sitter 解析。
func parseASTSimple(content, filePath string) *ASTContext {
	ctx := &ASTContext{
		Language:   "golang",
		Functions:  make([]FunctionSignature, 0),
		Structs:    make([]TypeSignature, 0),
		Interfaces: make([]TypeSignature, 0),
	}
	// 使用包级缓存的 ASTExtractor，避免每次调用都重新初始化
	goExtractorOnce.Do(func() {
		goExtractor = NewASTExtractor("golang")
	})
	files := []map[string]interface{}{
		{"path": filePath, "diff": content},
	}
	astCtx := goExtractor.Extract(files)
	if astCtx != nil {
		return astCtx
	}
	return ctx
}

// goExtractorOnce / goExtractor 用于 parseASTSimple 的 ASTExtractor 单例缓存。
var goExtractorOnce sync.Once
var goExtractor *ASTExtractor

// compareStructFields 对比两组字段列表，返回差异描述。
func compareStructFields(before, after []string) string {
	beforeSet := make(map[string]bool)
	for _, f := range before {
		beforeSet[f] = true
	}
	afterSet := make(map[string]bool)
	for _, f := range after {
		afterSet[f] = true
	}
	var added, removed []string
	for f := range afterSet {
		if !beforeSet[f] {
			added = append(added, f)
		}
	}
	for f := range beforeSet {
		if !afterSet[f] {
			removed = append(removed, f)
		}
	}
	if len(added) == 0 && len(removed) == 0 {
		return ""
	}
	desc := ""
	if len(added) > 0 {
		desc += fmt.Sprintf("新增字段: %s", strings.Join(added, ", "))
	}
	if len(removed) > 0 {
		if desc != "" {
			desc += "; "
		}
		desc += fmt.Sprintf("删除字段: %s", strings.Join(removed, ", "))
	}
	return desc
}
