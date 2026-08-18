package pipeline

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ai-optimizer/backend/internal/dataflow"
	"github.com/ai-optimizer/backend/internal/framework"
	"github.com/ai-optimizer/backend/internal/graph"
	"github.com/ai-optimizer/backend/internal/model"
	"github.com/ai-optimizer/backend/internal/parser"
	"go.uber.org/zap"
)

// CodeUnderstandingExecutor 代码理解器 Pipeline Stage
// 三阶段：context_extract → symbol_graph → report_merge
type CodeUnderstandingExecutor struct {
	graphStorage graph.GraphStorage
	graphCache   *graph.GraphCache
}

// subStageInfo 子阶段执行信息（用于前端展示）
type subStageInfo struct {
	Name       string                 `json:"name"`
	Status     string                 `json:"status"` // pending/running/success/failed/skipped
	StartAt    string                 `json:"start_at,omitempty"`
	EndAt      string                 `json:"end_at,omitempty"`
	DurationMs int64                  `json:"duration_ms,omitempty"`
	Input      map[string]interface{} `json:"input,omitempty"`
	Output     map[string]interface{} `json:"output,omitempty"`
	Error      string                 `json:"error,omitempty"`
}

// NewCodeUnderstandingExecutor 创建代码理解器执行器
func NewCodeUnderstandingExecutor(workspace string) *CodeUnderstandingExecutor {
	storage := graph.NewSQLiteGraphStorage(workspace)
	ttl := 30 * time.Minute
	if v := os.Getenv("CODEGUARD_GRAPH_CACHE_TTL_MINUTES"); v != "" {
		if minutes, err := strconv.Atoi(v); err == nil && minutes > 0 {
			ttl = time.Duration(minutes) * time.Minute
		}
	}
	cache := graph.NewGraphCache(ttl)
	return &CodeUnderstandingExecutor{
		graphStorage: storage,
		graphCache:   cache,
	}
}

// Code 返回阶段编码
func (e *CodeUnderstandingExecutor) Code() string { return "code_understanding" }

// Execute 执行代码理解三阶段：context_extract → symbol_graph → report_merge
func (e *CodeUnderstandingExecutor) Execute(ctx StageContext) error {
	exec := &model.TaskPipelineExecution{ID: ctx.ExecutionID()}
	ctx.MarkRunning(exec)

	// 读取三阶段超时配置
	var ctxTimeout, graphTimeout, reportTimeout time.Duration
	func() {
		defer func() { recover() }()
		agentCfg := ctx.GetInput("_agent_config").(model.ReviewAgentConfig)
		t1, t2, t3 := agentCfg.CodeUnderstandingTimeouts()
		ctxTimeout = time.Duration(t1) * time.Second
		graphTimeout = time.Duration(t2) * time.Second
		reportTimeout = time.Duration(t3) * time.Second
	}()
	if ctxTimeout <= 0 {
		ctxTimeout = 30 * time.Second
	}
	if graphTimeout <= 0 {
		graphTimeout = 60 * time.Second
	}
	if reportTimeout <= 0 {
		reportTimeout = 10 * time.Second
	}

	task := ctx.Task()
	lang := "golang"
	if task != nil && task.Project.Language != "" {
		lang = task.Project.Language
	}
	var subStages []subStageInfo

	if task == nil {
		zap.L().Warn("code_understanding: task is nil, falling back to degraded mode")
		subStages = append(subStages, subStageInfo{Name: "context_extract", Status: "skipped"})
		subStages = append(subStages, subStageInfo{Name: "symbol_graph", Status: "skipped"})
		subStages = append(subStages, subStageInfo{Name: "report_merge", Status: "skipped"})
		e.emitEmpty(ctx, exec, "task is nil", subStages)
		return nil
	}

	// 1. 准备输入数据
	diffFiles := ctx.GetInput("diff_files")
	files, ok := diffFiles.([]map[string]interface{})
	if !ok || len(files) == 0 {
		subStages = append(subStages, subStageInfo{Name: "context_extract", Status: "skipped", Input: map[string]interface{}{"reason": "no diff files"}})
		subStages = append(subStages, subStageInfo{Name: "symbol_graph", Status: "skipped"})
		subStages = append(subStages, subStageInfo{Name: "report_merge", Status: "skipped"})
		e.emitEmpty(ctx, exec, "no diff files", subStages)
		return nil
	}

	repoDir, _ := ctx.GetOutput("repo_dir").(string)
	fullFileMap, _ := ctx.GetOutput("file_contents").(map[string]string)
	if fullFileMap == nil {
		fullFileMap = make(map[string]string)
	}
	var callChainText string

	// 2. 阶段一：context_extract
	ctxStage := subStageInfo{
		Name:   "context_extract",
		Status: "running",
		Input: map[string]interface{}{
			"file_count":  len(files),
			"repo_dir":    repoDir,
			"timeout_sec": ctxTimeout.Seconds(),
		},
		StartAt: time.Now().Format(time.RFC3339),
	}
	asts, parsedCount, err := e.runWithTimeout(func() ([]*parser.UnifiedAST, int, error) {
		return e.contextExtract(ctx, files, repoDir, fullFileMap)
	}, ctxTimeout)
	ctxStage.EndAt = time.Now().Format(time.RFC3339)
	ctxStage.DurationMs = time.Since(time.Now().Add(-ctxTimeout)).Milliseconds()
	if ctxTimeout > 0 {
		ctxStage.DurationMs = int64(ctxTimeout.Milliseconds())
	}

	if err != nil {
		ctxStage.Status = "failed"
		ctxStage.Error = err.Error()
		ctxStage.Output = map[string]interface{}{"parsed_files": parsedCount}
		subStages = append(subStages, ctxStage)
		subStages = append(subStages, subStageInfo{Name: "symbol_graph", Status: "skipped"})
		subStages = append(subStages, subStageInfo{Name: "report_merge", Status: "skipped"})
		e.emitDegraded(ctx, exec, "context_extract failed: "+err.Error(), subStages, len(files))
		return nil
	}
	ctxStage.Status = "success"
	ctxStage.Output = map[string]interface{}{
		"parsed_files": parsedCount,
		"total_files":  len(files),
		"language": func() string {
			if task != nil {
				return task.Project.Language
			}
			return "golang"
		}(),
	}
	subStages = append(subStages, ctxStage)

	// 2.4 将 AST 结果转换为 ASTContext，供下游 impact_analysis / batch_algorithm 使用
	astCtx := e.buildASTContext(asts, lang)
	astText := formatASTContext(astCtx)
	ctx.SetInput("ast_context", astText)
	ctx.SetInput("ast_structured", astCtx)

	// 2.5 跨文件调用链分析（供后续 impact_analysis 等阶段使用）
	if repoDir != "" {
		changedPaths := make([]string, 0, len(files))
		for _, f := range files {
			if p, ok := f["path"].(string); ok && p != "" {
				changedPaths = append(changedPaths, p)
			}
		}
		depth := 1
		if d, ok := ctx.GetInput("_code_understanding_depth").(int); ok && d > 0 {
			depth = d
		} else if d := SysCfgCallChainDepth(); d > 0 {
			depth = d
		}
		if len(changedPaths) > 0 && depth > 0 {
			analyzer := NewCrossFileAnalyzer(repoDir, lang, depth)
			crossFileCtx, crossErr := analyzer.Analyze(changedPaths)
			if crossErr == nil && crossFileCtx != nil {
				callChainText = buildCrossFileCallChainText(crossFileCtx)
				ctx.SetInput("cross_file_call_chain", callChainText)
			}
		}
	}

	// 3. 阶段二：symbol_graph
	graphStage := subStageInfo{
		Name:   "symbol_graph",
		Status: "running",
		Input: map[string]interface{}{
			"has_baseline": e.graphStorage.Exists(uint64(task.ProjectID)),
			"timeout_sec":  graphTimeout.Seconds(),
		},
		StartAt: time.Now().Format(time.RFC3339),
	}

	projectID := uint64(task.ProjectID)
	hasBaseline := e.graphStorage.Exists(projectID)
	var graphResult *graph.SymbolGraphResult
	var reviewView *graph.ReviewView

	if hasBaseline {
		changedFiles := make(map[string][]byte)
		for _, ast := range asts {
			if content, ok := fullFileMap[ast.FilePath]; ok {
				changedFiles[ast.FilePath] = []byte(content)
			}
		}
		reviewView, err = e.buildReviewView(ctx, projectID, changedFiles)
		if err != nil {
			zap.L().Warn("build review view failed, using baseline only", zap.Error(err))
			graphStage.Output = map[string]interface{}{"review_view_built": false, "error": err.Error()}
		} else {
			graphStage.Output = map[string]interface{}{"review_view_built": true}
		}
	} else {
		sg := graph.NewSymbolGraph(e.graphStorage, e.graphCache, zap.L(), "")
		var activeAdapters []graph.ASTAdapter
		for _, ast := range asts {
			for _, adapter := range framework.GlobalAdapterRegistry.GetAdaptersForAST(ast) {
				found := false
				for _, existing := range activeAdapters {
					if existing.Name() == adapter.Name() {
						found = true
						break
					}
				}
				if !found {
					activeAdapters = append(activeAdapters, adapter)
				}
			}
		}
		graphResult, err = sg.BuildFromASTs(projectID, asts, activeAdapters)
		if err != nil {
			zap.L().Warn("build symbol graph from ASTs failed", zap.Error(err))
			graphStage.Output = map[string]interface{}{"graph_built": false, "error": err.Error()}
		} else {
			graphStage.Output = map[string]interface{}{
				"graph_built":    true,
				"node_count":     graphResult.NodeCount,
				"relation_count": graphResult.RelCount,
				"endpoint_count": len(graphResult.Endpoints),
				"frameworks":     graphResult.Frameworks,
			}
		}
	}
	graphStage.EndAt = time.Now().Format(time.RFC3339)
	if err != nil {
		graphStage.Status = "failed"
		graphStage.Error = err.Error()
	} else {
		graphStage.Status = "success"
	}
	subStages = append(subStages, graphStage)

	// 4. 阶段三：report_merge
	reportStage := subStageInfo{
		Name:   "report_merge",
		Status: "running",
		Input: map[string]interface{}{
			"ast_count":   len(asts),
			"timeout_sec": reportTimeout.Seconds(),
		},
		StartAt: time.Now().Format(time.RFC3339),
	}
	report := e.buildReport(asts, graphResult, reviewView, parsedCount, len(files), lang, callChainText)
	reportStage.EndAt = time.Now().Format(time.RFC3339)
	reportStage.Status = "success"
	reportStage.Output = map[string]interface{}{
		"mode":             report.Mode,
		"parsed_files":     parsedCount,
		"node_count":       report.SymbolGraphSummary.NodeCount,
		"endpoint_count":   report.EndpointCount,
		"taint_flow_count": report.TaintFlowCount,
	}
	subStages = append(subStages, reportStage)

	// 输出到 Pipeline 上下文
	ctx.SetOutput("code_understanding_report", report.ReportText)
	ctx.SetOutput("code_understanding_prompt", report.PromptInjection)
	ctx.SetOutput("code_understanding_structured", report)
	ctx.SetInput("code_understanding_report", report.ReportText)

	ctx.SaveOutputSnapshot(exec, map[string]interface{}{
		"status":                        "success",
		"mode":                          report.Mode,
		"parsed_files":                  parsedCount,
		"total_files":                   len(files),
		"node_count":                    report.SymbolGraphSummary.NodeCount,
		"endpoint_count":                report.EndpointCount,
		"taint_flow_count":              report.TaintFlowCount,
		"sub_stages":                    subStages,
		"summary":                       report.Summary,
		"code_understanding_structured": report,
	})

	ctx.MarkSuccess(exec)
	return nil
}

// runWithTimeout 在超时内执行函数
func (e *CodeUnderstandingExecutor) runWithTimeout(fn func() ([]*parser.UnifiedAST, int, error), timeout time.Duration) ([]*parser.UnifiedAST, int, error) {
	type result struct {
		asts        []*parser.UnifiedAST
		parsedCount int
		err         error
	}
	ch := make(chan result, 1)
	go func() {
		asts, count, err := fn()
		ch <- result{asts, count, err}
	}()
	select {
	case r := <-ch:
		return r.asts, r.parsedCount, r.err
	case <-time.After(timeout):
		return nil, 0, fmt.Errorf("子阶段执行超时 (限制 %.0f 秒)", timeout.Seconds())
	}
}

func (e *CodeUnderstandingExecutor) emitEmpty(ctx StageContext, exec *model.TaskPipelineExecution, reason string, subStages []subStageInfo) {
	ctx.SetOutput("code_understanding_report", "")
	ctx.SetOutput("code_understanding_prompt", "")
	ctx.SetOutput("code_understanding_structured", map[string]interface{}{"mode": "skipped", "reason": reason})
	ctx.SaveOutputSnapshot(exec, map[string]interface{}{
		"status":     "skipped",
		"reason":     reason,
		"sub_stages": subStages,
	})
	ctx.MarkSuccess(exec)
}

func (e *CodeUnderstandingExecutor) emitDegraded(ctx StageContext, exec *model.TaskPipelineExecution, reason string, subStages []subStageInfo, totalFiles int) {
	ctx.SetOutput("code_understanding_report", "")
	ctx.SetOutput("code_understanding_prompt", "")
	ctx.SetOutput("code_understanding_structured", map[string]interface{}{
		"mode":        "degraded",
		"reason":      reason,
		"files_total": totalFiles,
	})
	exec.Degraded = true
	exec.DegradedReason = reason
	ctx.SaveOutputSnapshot(exec, map[string]interface{}{
		"status":          "degraded",
		"mode":            "degraded",
		"degraded_reason": reason,
		"sub_stages":      subStages,
	})
	ctx.MarkSuccess(exec)
}

// contextExtract 阶段一：解析变更文件
func (e *CodeUnderstandingExecutor) contextExtract(ctx StageContext, files []map[string]interface{}, repoDir string, fullFileMap map[string]string) ([]*parser.UnifiedAST, int, error) {
	var asts []*parser.UnifiedAST
	parsedCount := 0

	for _, f := range files {
		path, _ := f["path"].(string)
		if path == "" {
			continue
		}
		select {
		case <-ctx.Done():
			return asts, parsedCount, fmt.Errorf("task cancelled")
		default:
		}

		content := fullFileMap[path]
		if content == "" && repoDir != "" {
			contentBytes, err := os.ReadFile(filepath.Join(repoDir, path))
			if err == nil {
				content = string(contentBytes)
			}
		}
		if content == "" {
			continue
		}

		ext := filepath.Ext(path)
		extractor, ok := parser.GlobalRegistry.GetExtractorByExt(ext)
		if !ok {
			continue
		}

		ast, err := extractor.ParseFile(path, []byte(content))
		if err != nil {
			zap.L().Warn("parse file failed", zap.String("path", path), zap.Error(err))
			continue
		}
		asts = append(asts, ast)
		parsedCount++
	}

	return asts, parsedCount, nil
}

// buildASTContext 将 []*parser.UnifiedAST 转换为下游阶段需要的 *ASTContext
func (e *CodeUnderstandingExecutor) buildASTContext(asts []*parser.UnifiedAST, lang string) *ASTContext {
	astCtx := &ASTContext{
		Language: lang,
	}
	importSet := make(map[string]bool)
	for _, ast := range asts {
		for _, fn := range ast.Functions {
			params := make([]string, 0, len(fn.Params))
			for _, p := range fn.Params {
				params = append(params, p.Type)
			}
			astCtx.Functions = append(astCtx.Functions, FunctionSignature{
				Name:       fn.Name,
				Receiver:   fn.Receiver,
				Params:     params,
				Returns:    fn.Returns,
				IsExported: fn.IsExported,
				FilePath:   ast.FilePath,
				LineStart:  fn.Location.LineStart,
				Body:       fn.BodySnippet,
			})
		}
		for _, tp := range ast.Types {
			astCtx.Structs = append(astCtx.Structs, TypeSignature{
				Name:       tp.Name,
				Kind:       tp.Kind,
				IsExported: tp.IsExported,
				FilePath:   ast.FilePath,
			})
		}
		for _, im := range ast.Imports {
			if !importSet[im.Path] {
				importSet[im.Path] = true
				astCtx.Imports = append(astCtx.Imports, im.Path)
			}
		}
		for _, cs := range ast.CallSites {
			astCtx.Callers = append(astCtx.Callers, cs.TargetFunc)
		}
	}
	return astCtx
}

// buildReviewView 构建评审视图
func (e *CodeUnderstandingExecutor) buildReviewView(ctx StageContext, projectID uint64, changedFiles map[string][]byte) (*graph.ReviewView, error) {
	sg := graph.NewSymbolGraph(e.graphStorage, e.graphCache, zap.L(), "")
	return sg.BuildReviewView(ctx, projectID, changedFiles)
}

// buildReport 阶段三：生成报告
func (e *CodeUnderstandingExecutor) buildReport(asts []*parser.UnifiedAST, graphResult *graph.SymbolGraphResult, reviewView *graph.ReviewView, parsedCount, totalCount int, lang string, crossFileCallChain string) *CodeUnderstandingReport {
	report := &CodeUnderstandingReport{
		Status:        "success",
		ReportVersion: "1.0",
		GeneratedAt:   time.Now(),
		Mode:          "full",
		ParsedFiles:   parsedCount,
		TotalFiles:    totalCount,
		FileList:      make([]string, 0, len(asts)),
		FunctionList:  make([]string, 0),
		ASTData: &ContextExtractResult{
			Status:      "success",
			FileResults: make([]FileASTResult, 0, len(asts)),
		},
	}

	// 文件列表和AST数据
	for _, ast := range asts {
		report.FileList = append(report.FileList, ast.FilePath)
		for _, fn := range ast.Functions {
			report.FunctionList = append(report.FunctionList, fn.Name)
		}
		report.TotalFunctions += len(ast.Functions)
		report.TotalTypes += len(ast.Types)

		// 收集函数签名和结构体字段详情
		var funcDetails []map[string]interface{}
		for _, fn := range ast.Functions {
			sig := fn.Name + "("
			for i, p := range fn.Params {
				if i > 0 {
					sig += ", "
				}
				sig += p.Type
			}
			sig += ")"
			if len(fn.Returns) > 0 {
				sig += " " + strings.Join(fn.Returns, ", ")
			}
			funcDetails = append(funcDetails, map[string]interface{}{
				"name":      fn.Name,
				"signature": sig,
				"receiver":  fn.Receiver,
				"line":      fn.Location.LineStart,
			})
		}
		var typeDetails []map[string]interface{}
		for _, tp := range ast.Types {
			var fields []string
			for _, f := range tp.Fields {
				fields = append(fields, f.Name+" "+f.Type)
			}
			typeDetails = append(typeDetails, map[string]interface{}{
				"name":   tp.Name,
				"kind":   tp.Kind,
				"fields": fields,
				"line":   tp.Location.LineStart,
			})
		}

		report.ASTData.FileResults = append(report.ASTData.FileResults, FileASTResult{
			Path:            ast.FilePath,
			Functions:       len(ast.Functions),
			Types:           len(ast.Types),
			Imports:         len(ast.Imports),
			FunctionDetails: funcDetails,
			TypeDetails:     typeDetails,
		})
	}

	// 符号图摘要
	if graphResult != nil {
		report.SymbolGraphSummary = SymbolGraphSummary{
			NodeCount: graphResult.NodeCount,
			RelCount:  graphResult.RelCount,
		}
		report.GraphData = &SymbolGraphResult{
			Status:        "success",
			ScanType:      "incremental",
			NodeCount:     graphResult.NodeCount,
			RelationCount: graphResult.RelCount,
		}
		report.EndpointCount = len(graphResult.Endpoints)
		var frameworks []string
		for _, ep := range graphResult.Endpoints {
			report.Endpoints = append(report.Endpoints, EndpointSummary{
				Path:      ep.Path,
				Method:    ep.Method,
				Handler:   ep.HandlerFunc,
				File:      ep.File,
				Line:      ep.Line,
				Framework: ep.Framework,
			})
			if ep.Framework != "" {
				found := false
				for _, f := range frameworks {
					if f == ep.Framework {
						found = true
						break
					}
				}
				if !found {
					frameworks = append(frameworks, ep.Framework)
				}
			}
		}
		if report.GraphData != nil {
			report.GraphData.Frameworks = frameworks
		}
	} else if reviewView != nil {
		// 有 baseline 但 graphResult 为 nil 时，从 reviewView 获取图谱统计
		g := reviewView.GetGraph()
		if g != nil {
			nodes := g.AllNodes()
			relCount := g.RelationCount()
			report.SymbolGraphSummary = SymbolGraphSummary{
				NodeCount: len(nodes),
				RelCount:  relCount,
			}
			report.GraphData = &SymbolGraphResult{
				Status:        "success",
				ScanType:      "incremental",
				NodeCount:     len(nodes),
				RelationCount: relCount,
			}
			// 从 reviewView 的图中提取端点
			var frameworks []string
			for _, node := range nodes {
				if node.Type == graph.NodeEndpoint {
					report.Endpoints = append(report.Endpoints, EndpointSummary{
						Path:      node.Name,
						Method:    getStringProp(node.Properties, "method"),
						Handler:   getStringProp(node.Properties, "handler"),
						File:      node.File,
						Line:      node.Location.LineStart,
						Framework: getStringProp(node.Properties, "framework"),
					})
					if fw := getStringProp(node.Properties, "framework"); fw != "" {
						found := false
						for _, f := range frameworks {
							if f == fw {
								found = true
								break
							}
						}
						if !found {
							frameworks = append(frameworks, fw)
						}
					}
				}
			}
			report.EndpointCount = len(report.Endpoints)
			if report.GraphData != nil {
				report.GraphData.Frameworks = frameworks
			}
		}
	}

	// 评审视图分析
	if reviewView != nil {
		endpoints := reviewView.FindEndpoints()
		// 只有当 graphResult 为 nil 时才需要补充端点（避免重复）
		if graphResult == nil {
			report.EndpointCount = len(endpoints)
			report.Endpoints = nil // 清空之前从 baseline 提取的，重新填充
			for _, ep := range endpoints {
				report.Endpoints = append(report.Endpoints, EndpointSummary{
					Path:      ep.Name,
					Method:    getStringProp(ep.Properties, "method"),
					Handler:   getStringProp(ep.Properties, "handler"),
					File:      ep.File,
					Line:      ep.Location.LineStart,
					Framework: getStringProp(ep.Properties, "framework"),
				})
			}
		}

		// 使用 v2 污点分析引擎（基于 AST，更精确）
		engine := dataflow.NewEngineV2(lang, asts, reviewView.GetGraph())
		dfResult := engine.Analyze()
		report.TaintFlowCount = len(dfResult.Flows)
		for _, flow := range dfResult.Flows {
			report.TaintFlows = append(report.TaintFlows, TaintFlowSummary{
				SourceFunc: flow.Source.Function,
				SourceFile: flow.Source.File,
				SinkFunc:   flow.Sink.Function,
				SinkFile:   flow.Sink.File,
				Category:   flow.Category,
				RiskLevel:  flow.RiskLevel,
			})
		}
		if report.GraphData != nil {
			report.GraphData.SecurityPaths = make([]string, 0, len(dfResult.Flows))
			for _, f := range dfResult.Flows {
				// 利用 Path 字段格式化完整的变量流转链
				if len(f.Path) > 0 {
					var chain []string
					for _, p := range f.Path {
						if p.VarName != "" {
							chain = append(chain, p.VarName)
						} else {
							chain = append(chain, p.Function)
						}
					}
					report.GraphData.SecurityPaths = append(report.GraphData.SecurityPaths,
						fmt.Sprintf("%s: %s", f.Category, strings.Join(chain, " \u2192 ")))
				} else {
					// 无传播路径时回退到两点式
					report.GraphData.SecurityPaths = append(report.GraphData.SecurityPaths,
						fmt.Sprintf("%s: %s \u2192 %s", f.Category, f.Source.Function, f.Sink.Function))
				}
			}
		}
	}

	// Summary
	report.Summary = CodeUnderstandingSummary{
		TotalFiles:         report.TotalFiles,
		TotalFunctions:     report.TotalFunctions,
		TotalStructs:       report.TotalTypes,
		TotalNodes:         report.SymbolGraphSummary.NodeCount,
		TotalRelations:     report.SymbolGraphSummary.RelCount,
		FrameworksDetected: getFrameworks(report.Endpoints),
		SecurityPathCount:  report.TaintFlowCount,
		IsGraphAvailable:   report.SymbolGraphSummary.NodeCount > 0,
	}

	// 自动降级：无图谱节点时降级为 AST Only
	if report.SymbolGraphSummary.NodeCount == 0 {
		report.Mode = "ast_only"
		report.Status = "partial"
	}

	// Prompt注入文本（只展示与变更文件相关的端点和污点流）
	report.CrossFileCallChain = crossFileCallChain
	report.ReportText = e.formatReportText(report, report.FileList)
	report.PromptInjection = report.ReportText
	return report
}

// formatReportText 格式化报告文本，用于Prompt注入
// changedFiles: 本次变更的文件列表，只展示与变更相关的端点和污点流
func (e *CodeUnderstandingExecutor) formatReportText(report *CodeUnderstandingReport, changedFiles []string) string {
	changedSet := make(map[string]bool)
	for _, f := range changedFiles {
		changedSet[f] = true
	}

	var b strings.Builder
	b.WriteString("## 基于AST与知识图谱的代码变更分析报告\n\n")
	b.WriteString(fmt.Sprintf("- **解析文件数**: %d / %d\n", report.ParsedFiles, report.TotalFiles))
	b.WriteString(fmt.Sprintf("- **函数总数**: %d\n", report.TotalFunctions))
	b.WriteString(fmt.Sprintf("- **类型总数**: %d\n", report.TotalTypes))

	// 只统计与变更文件相关的端点
	changedEndpoints := filterEndpointsByFiles(report.Endpoints, changedSet)
	b.WriteString(fmt.Sprintf("- **API端点**: %d (变更文件中 %d)\n", report.EndpointCount, len(changedEndpoints)))

	// 只统计与变更文件相关的污点流
	changedFlows := filterTaintFlowsByFiles(report.TaintFlows, changedSet)
	b.WriteString(fmt.Sprintf("- **污点流**: %d (涉及变更文件 %d)\n", report.TaintFlowCount, len(changedFlows)))

	if report.GraphData != nil && len(report.GraphData.Frameworks) > 0 {
		b.WriteString(fmt.Sprintf("- **框架**: %s\n", strings.Join(report.GraphData.Frameworks, ", ")))
	}

	if len(changedEndpoints) > 0 {
		b.WriteString("\n### API端点 (变更相关)\n")
		for _, ep := range changedEndpoints {
			b.WriteString(fmt.Sprintf("- `%s %s` (%s:%d)\n", ep.Method, ep.Path, ep.File, ep.Line))
		}
	}

	if len(changedFlows) > 0 {
		b.WriteString("\n### 潜在数据流风险 (变更相关)\n")
		for _, tf := range changedFlows {
			b.WriteString(fmt.Sprintf("- **%s**: %s (%s) → %s (%s) (风险: %s)\n",
				tf.Category, tf.SourceFunc, tf.SourceFile, tf.SinkFunc, tf.SinkFile, tf.RiskLevel))
		}
	}

	// 变更函数列表（完整注入，不截断）
	if len(report.FunctionList) > 0 {
		b.WriteString("\n### 变更函数列表\n")
		for _, fn := range report.FunctionList {
			b.WriteString(fmt.Sprintf("- %s\n", fn))
		}
	}

	// 跨文件调用链上下文
	if report.CrossFileCallChain != "" {
		b.WriteString("\n### 跨文件调用链上下文\n")
		b.WriteString(report.CrossFileCallChain)
	}

	return b.String()
}

// filterEndpointsByFiles 过滤出与变更文件相关的端点
func filterEndpointsByFiles(endpoints []EndpointSummary, changedSet map[string]bool) []EndpointSummary {
	var result []EndpointSummary
	for _, ep := range endpoints {
		if changedSet[ep.File] {
			result = append(result, ep)
		}
	}
	return result
}

// filterTaintFlowsByFiles 过滤出与变更文件相关的污点流
func filterTaintFlowsByFiles(flows []TaintFlowSummary, changedSet map[string]bool) []TaintFlowSummary {
	var result []TaintFlowSummary
	for _, tf := range flows {
		if changedSet[tf.SourceFile] || changedSet[tf.SinkFile] {
			result = append(result, tf)
		}
	}
	return result
}

// CodeUnderstandingReport 代码理解报告
type CodeUnderstandingReport struct {
	Status             string                   `json:"status"`         // success | degraded | ast_only
	ReportVersion      string                   `json:"report_version"` // "1.0"
	GeneratedAt        time.Time                `json:"generated_at"`
	Mode               string                   `json:"mode"` // full | degraded | ast_only
	ParsedFiles        int                      `json:"parsed_files"`
	TotalFiles         int                      `json:"total_files"`
	FileList           []string                 `json:"file_list"`
	TotalFunctions     int                      `json:"total_functions"`
	TotalTypes         int                      `json:"total_types"`
	FunctionList       []string                 `json:"function_list"`
	ASTData            *ContextExtractResult    `json:"ast_data,omitempty"`
	GraphData          *SymbolGraphResult       `json:"graph_data,omitempty"`
	SymbolGraphSummary SymbolGraphSummary       `json:"symbol_graph_summary"`
	EndpointCount      int                      `json:"endpoint_count"`
	Endpoints          []EndpointSummary        `json:"endpoints,omitempty"`
	TaintFlowCount     int                      `json:"taint_flow_count"`
	TaintFlows         []TaintFlowSummary       `json:"taint_flows,omitempty"`
	CrossFileCallChain string                   `json:"cross_file_call_chain,omitempty"` // 跨文件调用链文本
	ReportText         string                   `json:"report_text,omitempty"`         // Prompt注入文本
	PromptInjection    string                   `json:"prompt_injection"`              // 同上，兼容命名
	Summary            CodeUnderstandingSummary `json:"summary"`
}

// ContextExtractResult AST提取结果（子阶段1产出）
type ContextExtractResult struct {
	Status      string          `json:"status"`
	Language    string          `json:"language"`
	FileResults []FileASTResult `json:"file_results,omitempty"`
	Errors      []string        `json:"errors,omitempty"`
}

// FileASTResult 单文件AST结果
type FileASTResult struct {
	Path            string                   `json:"path"`
	Size            int                      `json:"size"`
	Functions       int                      `json:"functions"`
	Types           int                      `json:"types"`
	Imports         int                      `json:"imports"`
	ParseError      string                   `json:"parse_error,omitempty"`
	FunctionDetails []map[string]interface{} `json:"function_details,omitempty"`
	TypeDetails     []map[string]interface{} `json:"type_details,omitempty"`
}

// SymbolGraphResult 符号图分析结果（子阶段2产出）
type SymbolGraphResult struct {
	Status        string   `json:"status"`
	ScanType      string   `json:"scan_type"`
	NodeCount     int      `json:"node_count"`
	RelationCount int      `json:"relation_count"`
	Frameworks    []string `json:"frameworks,omitempty"`
	SecurityPaths []string `json:"security_paths,omitempty"`
	Error         string   `json:"error,omitempty"`
}

// CodeUnderstandingSummary 报告摘要
type CodeUnderstandingSummary struct {
	TotalFiles          int      `json:"total_files"`
	TotalFunctions      int      `json:"total_functions"`
	TotalStructs        int      `json:"total_structs"`
	TotalNodes          int      `json:"total_nodes"`
	TotalRelations      int      `json:"total_relations"`
	FrameworksDetected  []string `json:"frameworks_detected,omitempty"`
	SecurityPathCount   int      `json:"security_path_count"`
	BreakingChangeCount int      `json:"breaking_change_count"`
	IsGraphAvailable    bool     `json:"is_graph_available"`
}

// SymbolGraphSummary 符号图摘要
type SymbolGraphSummary struct {
	NodeCount int `json:"node_count"`
	RelCount  int `json:"rel_count"`
}

// EndpointSummary 端点摘要
type EndpointSummary struct {
	Path      string `json:"path"`
	Method    string `json:"method,omitempty"`
	Handler   string `json:"handler,omitempty"`
	File      string `json:"file"`
	Line      int    `json:"line"`
	Framework string `json:"framework,omitempty"`
}

// TaintFlowSummary 污点流摘要
type TaintFlowSummary struct {
	SourceFunc string `json:"source_func"`
	SourceFile string `json:"source_file"`
	SinkFunc   string `json:"sink_func"`
	SinkFile   string `json:"sink_file"`
	Category   string `json:"category"`
	RiskLevel  string `json:"risk_level"`
}

// reviewViewAdapter 适配graph.ReviewView到dataflow.ReviewViewIface
type reviewViewAdapter struct {
	view *graph.ReviewView
}

func (a *reviewViewAdapter) GetGraph() *graph.MemorySymbolGraph {
	return a.view.GetGraph()
}

func (a *reviewViewAdapter) FindEndpoints() []graph.MemorySymbolNode {
	return a.view.FindEndpoints()
}

func getFrameworks(endpoints []EndpointSummary) []string {
	var result []string
	seen := make(map[string]bool)
	for _, ep := range endpoints {
		if ep.Framework != "" && !seen[ep.Framework] {
			seen[ep.Framework] = true
			result = append(result, ep.Framework)
		}
	}
	return result
}

func getStringProp(props map[string]interface{}, key string) string {
	if props == nil {
		return ""
	}
	if v, ok := props[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func init() {
	// 注册到全局 executor 注册表（由 Engine 统一管理）
	// Engine.NewEngine 中通过 e.Register 注册
}
