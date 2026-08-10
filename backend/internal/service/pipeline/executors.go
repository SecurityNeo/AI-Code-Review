package pipeline

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ai-optimizer/backend/internal/engine"
	"github.com/ai-optimizer/backend/internal/model"
	"github.com/ai-optimizer/backend/pkg/gitlab"
	"github.com/ai-optimizer/backend/pkg/llm"
	"go.uber.org/zap"
)

// ========== TriggerCheckExecutor ==========
type TriggerCheckExecutor struct{}

func (e *TriggerCheckExecutor) Code() string { return "trigger_check" }

func (e *TriggerCheckExecutor) Execute(ctx StageContext) error {
	task := ctx.Task()
	if task == nil {
		return fmt.Errorf("task is nil")
	}

	// 保存输入快照（完整的任务信息）
	ctx.SaveInputSnapshot(&model.TaskPipelineExecution{ID: ctx.ExecutionID()}, map[string]interface{}{
		"task_id":      task.ID,
		"mr_iid":       task.MRMergeID,
		"mr_title":     task.MRTitle,
		"author":       task.MRAuthor,
		"project_id":   task.ProjectID,
		"project_name": task.Project.Name,
		"task_type":    task.TaskType,
	})

	// 简单检查：MR IID 是否有效
	result := "通过"
	reason := "MR IID 有效"
	if task.MRMergeID <= 0 {
		result = "失败"
		reason = fmt.Sprintf("无效 MR IID: %d", task.MRMergeID)
		ctx.SaveOutputSnapshot(&model.TaskPipelineExecution{ID: ctx.ExecutionID()}, map[string]interface{}{
			"mr_iid":   task.MRMergeID,
			"mr_title": task.MRTitle,
			"author":   task.MRAuthor,
			"result":   result,
			"reason":   reason,
		})
		return fmt.Errorf("%s", reason)
	}

	ctx.SaveOutputSnapshot(&model.TaskPipelineExecution{ID: ctx.ExecutionID()}, map[string]interface{}{
		"mr_iid":   task.MRMergeID,
		"mr_title": task.MRTitle,
		"author":   task.MRAuthor,
		"result":   result,
		"reason":   reason,
	})
	return nil
}

// ========== GitCloneExecutor ==========
type GitCloneExecutor struct{}

func (e *GitCloneExecutor) Code() string { return "git_clone" }

func (e *GitCloneExecutor) Execute(ctx StageContext) error {
	// Diff 文件已由调用方预加载到 context inputs 中
	diffFiles := ctx.GetInput("diff_files")
	if diffFiles == nil {
		return fmt.Errorf("diff_files 未预加载到 context")
	}
	files, ok := diffFiles.([]map[string]interface{})
	if !ok {
		return fmt.Errorf("diff_files 类型错误")
	}

	// 保存输入快照
	fileList := make([]map[string]interface{}, 0, len(files))
	for _, f := range files {
		fileList = append(fileList, map[string]interface{}{
			"path":      f["path"],
			"old_path":  f["old_path"],
			"additions": f["additions"],
			"deletions": f["deletions"],
		})
	}
	ctx.SaveInputSnapshot(&model.TaskPipelineExecution{ID: ctx.ExecutionID()}, map[string]interface{}{
		"file_count": len(files),
		"files":      fileList,
	})

	// 获取 source_branch
	task := ctx.Task()
	sourceBranch := ""
	if task != nil {
		sourceBranch = task.SourceBranch
		if sourceBranch == "" && task.Project.GitLabProjectID > 0 && task.MRMergeID > 0 && task.Project.ProjectPath != "" {
			host := gitlab.ExtractGitLabHost(task.Project.ProjectPath)
			if host != "" && task.Project.AccessToken != "" {
				client := gitlab.NewClient(host, task.Project.AccessToken)
				if mr, err := client.GetMergeRequest(task.Project.GitLabProjectID, task.MRMergeID); err == nil {
					sourceBranch = mr.SourceBranch
					task.SourceBranch = sourceBranch
					zap.L().Info("Pipeline 获取 source_branch",
						zap.String("source_branch", sourceBranch),
						zap.String("target_branch", mr.TargetBranch))
				} else {
					zap.L().Warn("Pipeline 获取 MR 详情失败", zap.Error(err))
				}
			}
		}
	}

	// ====== 方案 C：持久化代码仓库 ======
	var repoDir string
	if task != nil && sourceBranch != "" && task.Project.ProjectPath != "" {
		mgr := GetRepoManager()
		dir, err := mgr.EnsureRepo(task.Project.ProjectPath, task.Project.AccessToken, sourceBranch, task.ProjectID)
		if err != nil {
			zap.L().Warn("Pipeline git clone 失败，降级为 API 模式", zap.Error(err))
		} else {
			repoDir = dir
			zap.L().Info("Pipeline 代码仓库就绪",
				zap.String("repo_dir", repoDir),
				zap.String("branch", sourceBranch))
		}
	}

	// 将关键数据传递到 context output 供后续阶段使用
	ctx.SetOutput("source_branch", sourceBranch)
	ctx.SetOutput("diff_files", files)
	ctx.SetOutput("repo_dir", repoDir)

	additions := 0
	deletions := 0
	for _, f := range files {
		if a, ok := f["additions"].(int); ok {
			additions += a
		}
		if d, ok := f["deletions"].(int); ok {
			deletions += d
		}
	}

	ctx.SaveOutputSnapshot(&model.TaskPipelineExecution{ID: ctx.ExecutionID()}, map[string]interface{}{
		"file_count":    len(files),
		"additions":     additions,
		"deletions":     deletions,
		"files":         fileList,
		"source_branch": sourceBranch,
		"repo_dir":      repoDir,
	})
	return nil
}

func getFileStatus(f interface{}) string {
	// 简化判断
	if fm, ok := f.(map[string]interface{}); ok {
		if deleted, _ := fm["deleted_file"].(bool); deleted {
			return "deleted"
		}
		if new, _ := fm["new_file"].(bool); new {
			return "added"
		}
		return "modified"
	}
	return "modified"
}

// ========== ContextExtractExecutor ==========
type ContextExtractExecutor struct{}

func (e *ContextExtractExecutor) Code() string { return "context_extract" }

func (e *ContextExtractExecutor) Execute(ctx StageContext) error {
	// 使用当前阶段自身的 execution（由 Engine 预先创建），避免创建冗余子 execution
	exec := &model.TaskPipelineExecution{ID: ctx.ExecutionID()}
	ctx.MarkRunning(exec)

	diffFiles := ctx.GetInput("diff_files")
	files, ok := diffFiles.([]map[string]interface{})
	if !ok || len(files) == 0 {
		reason := "diff_files 为空"
		if diffFiles == nil {
			reason = "diff_files 为空"
		} else if !ok {
			reason = "diff_files 类型错误"
		}
		ctx.SaveOutputSnapshot(exec, map[string]interface{}{
			"status":    "skipped",
			"reason":    reason,
			"ast_nodes": 0,
		})
		ctx.MarkSuccess(exec)
		return nil
	}

	// 确定语言
	lang := "golang"
	if t := ctx.Task(); t != nil && t.Project.Language != "" {
		lang = t.Project.Language
	}

	// 获取 source_branch 和 repo_dir
	sourceBranch := ""
	if sb, ok := ctx.GetOutput("source_branch").(string); ok && sb != "" {
		sourceBranch = sb
	} else if t := ctx.Task(); t != nil && t.SourceBranch != "" {
		sourceBranch = t.SourceBranch
	}
	repoDir, _ := ctx.GetOutput("repo_dir").(string)

	// 从变更文件中提取路径列表
	changedPaths := make([]string, 0, len(files))
	for _, f := range files {
		if p, ok := f["path"].(string); ok && p != "" {
			changedPaths = append(changedPaths, p)
		}
	}

	// 保存输入快照
	ctx.SaveInputSnapshot(exec, map[string]interface{}{
		"file_count":    len(files),
		"language":      lang,
		"branch":        sourceBranch,
		"repo_dir":      repoDir,
		"changed_files": changedPaths,
	})

	var crossFileCtx *CrossFileContext
	var perFileResults []map[string]interface{}
	var astCtx *ASTContext
	depth := 1

	if repoDir != "" {
		// ========== 方案 C：基于完整代码库的跨文件分析 ==========
		if d := SysCfgCallChainDepth(); d > 0 {
			depth = d
		}
		analyzer := NewCrossFileAnalyzer(repoDir, lang, depth)
		var err error
		crossFileCtx, err = analyzer.Analyze(changedPaths)
		if err != nil {
			zap.L().Warn("Pipeline 跨文件分析失败，降级为单文件分析", zap.Error(err))
		}
	}

	// 单文件 AST 分析（无论是否成功做了跨文件分析，都补充单文件 AST）
	extractor := NewASTExtractor(lang)
	totalFetched := 0
	fullFileMap := make(map[string]string)

	for _, f := range files {
		path, _ := f["path"].(string)
		if path == "" {
			continue
		}
		var content string
		var err error
		if repoDir != "" {
			// 从本地仓库读取
			contentBytes, readErr := os.ReadFile(filepath.Join(repoDir, path))
			if readErr == nil {
				content = string(contentBytes)
			}
		}
		if content == "" {
			// 回退到 API 获取
			if t := ctx.Task(); t != nil && t.Project.GitLabProjectID > 0 && sourceBranch != "" && t.Project.AccessToken != "" {
				host := gitlab.ExtractGitLabHost(t.Project.ProjectPath)
				if host != "" {
					client := gitlab.NewClient(host, t.Project.AccessToken)
					content, err = client.GetRepositoryFileRaw(t.Project.GitLabProjectID, path, sourceBranch)
					if err != nil {
						zap.L().Warn("Pipeline 获取完整文件失败",
							zap.String("path", path),
							zap.String("branch", sourceBranch),
							zap.Error(err))
						continue
					}
				}
			}
		}
		if content == "" {
			continue
		}
		totalFetched++
		fullFileMap[path] = content
		fileAST := extractor.ExtractSingle(path, content)
		perFileResults = append(perFileResults, map[string]interface{}{
			"path":             path,
			"size":             len(content),
			"language":         lang,
			"functions_count":  len(fileAST.Functions),
			"structs_count":    len(fileAST.Structs),
			"interfaces_count": len(fileAST.Interfaces),
			"imports_count":    len(fileAST.Imports),
			"functions":        fileAST.Functions,
			"structs":          fileAST.Structs,
			"interfaces":       fileAST.Interfaces,
			"imports":          fileAST.Imports,
			"callers":          fileAST.Callers,
		})
	}

	// 汇总 AST
	if len(fullFileMap) > 0 {
		astCtx = extractor.ExtractFull(fullFileMap)
	} else {
		astCtx = extractor.Extract(files)
	}
	astText := formatASTContext(astCtx)
	ctx.SetInput("ast_context", astText)
	ctx.SetInput("ast_structured", astCtx)

	// 将完整文件内容保存到 output，供后续 RiskAnalysis 解析依赖使用
	ctx.SetOutput("file_contents", fullFileMap)

	// 构建输出快照（非常丰富）
	output := map[string]interface{}{
		"status":             "success",
		"language":           lang,
		"source_branch":      sourceBranch,
		"repo_dir":           repoDir,
		"full_files_fetched": totalFetched,
		"total_files":        len(files),
		"per_file_analysis":  perFileResults,
		"ast_summary": map[string]interface{}{
			"functions":  len(astCtx.Functions),
			"structs":    len(astCtx.Structs),
			"interfaces": len(astCtx.Interfaces),
			"imports":    astCtx.Imports,
			"callers":    astCtx.Callers,
		},
		"functions_detail":  astCtx.Functions,
		"structs_detail":    astCtx.Structs,
		"interfaces_detail": astCtx.Interfaces,
		"imports_detail":    astCtx.Imports,
		"total_chars":       astCtx.TotalChars,
		"ast_text":          astText,
	}

	// 添加跨文件分析结果
	if crossFileCtx != nil {
		output["cross_file_enabled"] = true
		output["cross_file_total_files"] = crossFileCtx.TotalFiles
		output["cross_file_total_symbols"] = crossFileCtx.TotalSymbols
		output["cross_file_total_call_edges"] = crossFileCtx.TotalCallEdges
		output["cross_file_depth"] = depth

		// 每个变更文件的调用链
		for path, fc := range crossFileCtx.FileContexts {
			output["cross_file_"+path] = map[string]interface{}{
				"upstream_callers":     fc.UpstreamCallers,
				"downstream_deps":      fc.DownstreamDeps,
				"same_package_symbols": fc.SamePackageSymbols,
			}
		}

		// 全局调用链摘要（用于 prompt 注入）
		callChainText := buildCrossFileCallChainText(crossFileCtx)
		output["cross_file_call_chain_text"] = callChainText
		ctx.SetInput("cross_file_call_chain", callChainText)
	} else {
		output["cross_file_enabled"] = false
	}

	ctx.SaveOutputSnapshot(exec, output)
	ctx.MarkSuccess(exec)
	return nil
}

// buildCrossFileCallChainText 将跨文件调用链格式化为可注入 Prompt 的文本
func buildCrossFileCallChainText(cfc *CrossFileContext) string {
	if cfc == nil || len(cfc.FileContexts) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("## 跨文件调用链上下文\n\n")

	for path, fc := range cfc.FileContexts {
		sb.WriteString(fmt.Sprintf("### 文件: %s\n\n", path))

		if len(fc.UpstreamCallers) > 0 {
			sb.WriteString("**被以下文件调用：**\n")
			for _, caller := range fc.UpstreamCallers {
				sb.WriteString(fmt.Sprintf("- %s:%d `%s` → `%s`\n",
					caller.File, caller.Line, caller.Function, caller.CallExpr))
				if caller.CodeSnippet != "" {
					sb.WriteString(fmt.Sprintf("  ```\n  %s\n  ```\n",
						strings.ReplaceAll(caller.CodeSnippet, "\n", "\n  ")))
				}
			}
			sb.WriteString("\n")
		}

		if len(fc.SamePackageSymbols) > 0 {
			relevant := fc.SamePackageSymbols
			if len(relevant) > 10 {
				relevant = relevant[:10]
			}
			sb.WriteString("**同包其他符号：**\n")
			for _, sym := range relevant {
				if sym.Type == "func" {
					sb.WriteString(fmt.Sprintf("- func %s\n", sym.Name))
				} else {
					sb.WriteString(fmt.Sprintf("- %s %s\n", sym.Type, sym.Name))
				}
			}
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

// formatASTContext 将 AST 上下文格式化为文本摘要
func formatASTContext(ast *ASTContext) string {
	if ast == nil || (len(ast.Functions) == 0 && len(ast.Structs) == 0 && len(ast.Interfaces) == 0) {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("## 代码结构上下文\n\n")

	if len(ast.Functions) > 0 {
		sb.WriteString("### 涉及的函数签名\n")
		for _, fn := range ast.Functions {
			if fn.Receiver != "" {
				sb.WriteString(fmt.Sprintf("- func (%s) %s(", fn.Receiver, fn.Name))
			} else {
				sb.WriteString(fmt.Sprintf("- func %s(", fn.Name))
			}
			for i, p := range fn.Params {
				if i > 0 {
					sb.WriteString(", ")
				}
				sb.WriteString(p)
			}
			sb.WriteString(")")
			if len(fn.Returns) > 0 {
				sb.WriteString(" ")
				if len(fn.Returns) > 1 {
					sb.WriteString("(")
				}
				for i, r := range fn.Returns {
					if i > 0 {
						sb.WriteString(", ")
					}
					sb.WriteString(r)
				}
				if len(fn.Returns) > 1 {
					sb.WriteString(")")
				}
			}
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}

	if len(ast.Structs) > 0 {
		sb.WriteString("### 涉及的结构体\n")
		for _, s := range ast.Structs {
			sb.WriteString(fmt.Sprintf("- type %s %s\n", s.Name, s.Kind))
		}
		sb.WriteString("\n")
	}

	if len(ast.Interfaces) > 0 {
		sb.WriteString("### 涉及的接口\n")
		for _, i := range ast.Interfaces {
			sb.WriteString(fmt.Sprintf("- type %s interface\n", i.Name))
		}
		sb.WriteString("\n")
	}

	if len(ast.Imports) > 0 {
		sb.WriteString("### 新增/修改的依赖\n")
		for _, im := range ast.Imports {
			sb.WriteString(fmt.Sprintf("- %s\n", im))
		}
		sb.WriteString("\n")
	}

	if len(ast.Callers) > 0 {
		sb.WriteString("### 调用链\n")
		for _, c := range ast.Callers {
			sb.WriteString(fmt.Sprintf("- %s\n", c))
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

// ========== DependencyScanExecutor ==========
type DependencyScanExecutor struct{}

func (e *DependencyScanExecutor) Code() string { return "dependency_scan" }

// effectiveEcosystems 根据项目语言确定需要扫描的依赖生态
func (e *DependencyScanExecutor) effectiveEcosystems(ctx StageContext) []string {
	task := ctx.Task()
	if task == nil || task.Project.Language == "" || task.Project.Language == "common" {
		return nil // 不过滤，扫描所有生态
	}
	switch task.Project.Language {
	case "golang":
		return []string{"Go"}
	case "python":
		return []string{"pypi"}
	case "frontend":
		return []string{"npm"}
	case "java":
		return []string{"maven"}
	default:
		return nil
	}
}

func (e *DependencyScanExecutor) Execute(ctx StageContext) error {
	diffFiles := ctx.GetInput("diff_files")
	if diffFiles == nil {
		ctx.SetOutput("dependency_vulns", []engine.DependencyVuln{})
		ctx.SetOutput("dependency_risk_score", 100)
		ctx.SaveOutputSnapshot(&model.TaskPipelineExecution{ID: ctx.ExecutionID()}, map[string]interface{}{
			"status":                "success",
			"dependency_vuln_count": 0,
			"dependency_risk_score": 100,
			"deps_parsed":           0,
		})
		return nil
	}
	files, ok := diffFiles.([]map[string]interface{})
	if !ok {
		return nil
	}

	ecofilter := e.effectiveEcosystems(ctx)

	var allDeps []Dependency
	directDepCount := 0
	indirectDepCount := 0

	for _, f := range files {
		path, _ := f["path"].(string)
		if path == "" {
			continue
		}

		content := e.getFileContent(ctx, path)
		if content == "" {
			continue
		}

		deps, err := ParseDependenciesFromFile(path, content)
		if err != nil {
			zap.L().Warn("parse dependencies failed", zap.String("path", path), zap.Error(err))
			continue
		}

		for _, dep := range deps {
			// 生态过滤：如果指定了生态且不匹配则跳过
			if len(ecofilter) > 0 && !stringSliceContains(ecofilter, dep.Ecosystem) {
				continue
			}
			allDeps = append(allDeps, dep)
			if dep.Indirect {
				indirectDepCount++
			} else {
				directDepCount++
			}
		}
	}

	// Query vulnerabilities
	var depVulnMatches map[string][]VulnerabilityMatch
	var depVulnCount int
	var dependencyRiskScore int = 100

	if len(allDeps) > 0 {
		querier := NewVulnerabilityQuerier()
		depVulnMatches, depVulnCount, _ = querier.QueryByDependencies(allDeps, ecofilter)
		dependencyRiskScore = querier.CalculateDependencyRiskScore(depVulnMatches)
	}

	// Convert to engine.DependencyVuln for prompt injection
	var dependencyVulns []engine.DependencyVuln
	for _, matches := range depVulnMatches {
		for _, m := range matches {
			dependencyVulns = append(dependencyVulns, engine.DependencyVuln{
				PackageName:    m.PackageName,
				CurrentVersion: m.CurrentVersion,
				VulnID:         m.VulnID,
				Aliases:        m.Aliases,
				Severity:       m.Severity,
				Summary:        m.Summary,
				FixedVersion:   m.FixedVersion,
			})
		}
	}

	ctx.SetOutput("dependency_vulns", dependencyVulns)
	ctx.SetOutput("dependency_risk_score", dependencyRiskScore)

	ctx.SaveInputSnapshot(&model.TaskPipelineExecution{ID: ctx.ExecutionID()}, map[string]interface{}{
		"deps_parsed":   len(allDeps),
		"direct_deps":   directDepCount,
		"indirect_deps": indirectDepCount,
	})
	ctx.SaveOutputSnapshot(&model.TaskPipelineExecution{ID: ctx.ExecutionID()}, map[string]interface{}{
		"status":                "success",
		"dependency_vuln_count": depVulnCount,
		"dependency_risk_score": dependencyRiskScore,
		"dependency_vulns":      depVulnMatches,
		"deps_parsed":           len(allDeps),
		"direct_deps":           directDepCount,
		"indirect_deps":         indirectDepCount,
		"ecosystem_filter":      ecofilter,
	})
	return nil
}

func (e *DependencyScanExecutor) getFileContent(ctx StageContext, filePath string) string {
	if fc, ok := ctx.GetOutput("file_contents").(map[string]string); ok {
		if content, exists := fc[filePath]; exists && content != "" {
			return content
		}
	}
	if files := ctx.GetInput("diff_files"); files != nil {
		if fileMaps, ok := files.([]map[string]interface{}); ok {
			for _, f := range fileMaps {
				if p, _ := f["path"].(string); p == filePath {
					if content, ok := f["content"].(string); ok && content != "" {
						return content
					}
				}
			}
		}
	}
	return ""
}

func stringSliceContains(arr []string, s string) bool {
	for _, v := range arr {
		if v == s {
			return true
		}
	}
	return false
}

// ========== RiskAnalysisExecutor ==========
type RiskAnalysisExecutor struct{}

func (e *RiskAnalysisExecutor) Code() string { return "risk_analysis" }

func (e *RiskAnalysisExecutor) Execute(ctx StageContext) error {
	diffFiles := ctx.GetInput("diff_files")
	if diffFiles == nil {
		return nil
	}
	files, ok := diffFiles.([]map[string]interface{})
	if !ok {
		return nil
	}

	totalLines := 0
	fileDetails := make([]map[string]interface{}, 0, len(files))
	for _, f := range files {
		lines := 0
		if diff, ok := f["diff"].(string); ok {
			lines = strings.Count(diff, "\n")
		}
		totalLines += lines
		fileDetails = append(fileDetails, map[string]interface{}{
			"path":      f["path"],
			"additions": f["additions"],
			"deletions": f["deletions"],
			"lines":     lines,
		})
	}

	// 从 dependency_scan 阶段获取依赖风险分
	dependencyRiskScore := 100
	if score, ok := ctx.GetOutput("dependency_risk_score").(int); ok {
		dependencyRiskScore = score
	}

	// 从 dependency_scan 阶段获取漏洞统计
	depVulnCount := 0
	if depVulns, ok := ctx.GetOutput("dependency_vulns").([]engine.DependencyVuln); ok {
		depVulnCount = len(depVulns)
	}

	// ====== Overall Risk Level ======
	riskLevel := "low"
	if totalLines > 1000 || dependencyRiskScore < 30 {
		riskLevel = "high"
	} else if totalLines > 500 || dependencyRiskScore < 60 {
		riskLevel = "medium"
	}

	ctx.SetInput("risk_level", riskLevel)

	ctx.SaveInputSnapshot(&model.TaskPipelineExecution{ID: ctx.ExecutionID()}, map[string]interface{}{
		"file_count": len(files),
		"files":      fileDetails,
	})

	ctx.SaveOutputSnapshot(&model.TaskPipelineExecution{ID: ctx.ExecutionID()}, map[string]interface{}{
		"status":                "success",
		"risk_level":            riskLevel,
		"total_lines":           totalLines,
		"file_count":            len(files),
		"file_details":          fileDetails,
		"dependency_vuln_count": depVulnCount,
		"dependency_risk_score": dependencyRiskScore,
		"thresholds": map[string]interface{}{
			"high_lines":        1000,
			"medium_lines":      500,
			"low_lines":         0,
			"high_dep_score":    30,
			"medium_dep_score":  60,
			"dependency_weight": 0.2,
		},
	})
	return nil
}

// ========== BatchReviewFrameExecutor ==========
type BatchReviewFrameExecutor struct {
	llmService LLMService
}

func NewBatchReviewFrameExecutor(llm LLMService) *BatchReviewFrameExecutor {
	return &BatchReviewFrameExecutor{
		llmService: llm,
	}
}

func (e *BatchReviewFrameExecutor) Code() string { return "batch_review_frame" }

func (e *BatchReviewFrameExecutor) Execute(ctx StageContext) error {
	task := ctx.Task()
	if task == nil {
		return fmt.Errorf("task is nil")
	}

	// 从 context 获取 Prompt 构建上下文
	promptCtx := getPromptContext(ctx)
	if promptCtx == nil {
		return fmt.Errorf("prompt_context 未在 Pipeline inputs 中预加载")
	}

	// S1: 分批决策
	plan, err := e.executeBatchPlan(ctx, task)
	if err != nil {
		return err
	}

	// 更新 promptCtx 的 CrossFileContext（从 context 获取，可能在 context_extract 阶段生成）
	if crossFile := getCrossFileCallChainText(ctx); crossFile != "" {
		promptCtx.CrossFileContext = crossFile
	}

	// 注入依赖漏洞扫描结果（从 dependency_scan 阶段输出获取）
	if depVulns, ok := ctx.GetOutput("dependency_vulns").([]engine.DependencyVuln); ok && len(depVulns) > 0 {
		promptCtx.DependencyVulns = depVulns
	}

	// ==================== 场景 A：单批直接结构化评审 ====================
	var actualModelID uint
	if plan.BatchCount == 1 {
		parsedResult, modelID, err := e.executeSingleBatchStructured(ctx, plan.Batches[0], promptCtx, task)
		if err != nil {
			return err
		}
		actualModelID = modelID
		// 组装 Markdown 评论
		report := e.assembleMarkdownReport(parsedResult, promptCtx)
		ctx.SetOutput("ai_review_result", parsedResult)
		ctx.SetOutput("final_report", report)
		ctx.SetOutput("score", parsedResult.TotalScore)
		ctx.SetOutput("model_id", actualModelID)
		ctx.SaveOutputSnapshot(&model.TaskPipelineExecution{ID: ctx.ExecutionID()}, map[string]interface{}{
			"plan":         plan,
			"final_report": report,
			"score":        parsedResult.TotalScore,
			"issue_count":  len(parsedResult.Issues),
			"batch_count":  1,
			"model_id":     actualModelID,
		})
		return nil
	}

	// ==================== 场景 B/C：多批收集 + 汇总裁决 ====================
	// S2: 多批评审（按 wave 分组并行）
	maxParallel := SysCfgBatchParallelMax()
	waves := GroupBatchesIntoWaves(plan.BatchCount, maxParallel)
	batchResults := make([]*llm.BatchReviewResult, plan.BatchCount)
	var completedCount int32

	for waveIdx, wave := range waves {
		zap.L().Info("Pipeline 执行 wave",
			zap.Int("wave", waveIdx+1),
			zap.Int("total_waves", len(waves)),
			zap.Int("batches", len(wave)))

		var wg sync.WaitGroup
		var mu sync.Mutex
		errCh := make(chan error, len(wave))

		for _, batchIdx := range wave {
			batchIdx := batchIdx
			wg.Add(1)
			go func() {
				defer wg.Done()
				result, err := e.executeBatchCollection(ctx, plan.Batches[batchIdx], promptCtx, task)
				if err != nil {
					errCh <- fmt.Errorf("wave %d, batch %d 失败: %w", waveIdx+1, batchIdx+1, err)
					return
				}
				mu.Lock()
				batchResults[batchIdx] = result
				mu.Unlock()
				completed := int(atomic.AddInt32(&completedCount, 1))
				ctx.UpdateProgress(nil, completed, plan.BatchCount)
			}()
		}
		wg.Wait()
		close(errCh)
		if len(errCh) > 0 {
			ctx.SaveOutputSnapshot(&model.TaskPipelineExecution{ID: ctx.ExecutionID()}, map[string]interface{}{
				"plan":        plan,
				"error":       "批次评审失败",
				"batch_count": plan.BatchCount,
			})
			return <-errCh
		}
	}

	// S3: 汇总裁决（强制）
	parsedResult, modelID, err := e.executeScoreArbitration(ctx, batchResults, promptCtx, task)
	if err != nil {
		ctx.SaveOutputSnapshot(&model.TaskPipelineExecution{ID: ctx.ExecutionID()}, map[string]interface{}{
			"plan":        plan,
			"error":       err.Error(),
			"batch_count": plan.BatchCount,
		})
		return err
	}
	actualModelID = modelID

	// 组装 Markdown 评论
	report := e.assembleMarkdownReport(parsedResult, promptCtx)
	ctx.SetOutput("ai_review_result", parsedResult)
	ctx.SetOutput("final_report", report)
	ctx.SetOutput("score", parsedResult.TotalScore)
	ctx.SetOutput("model_id", actualModelID)

	ctx.SaveOutputSnapshot(&model.TaskPipelineExecution{ID: ctx.ExecutionID()}, map[string]interface{}{
		"plan":         plan,
		"final_report": report,
		"score":        parsedResult.TotalScore,
		"issue_count":  len(parsedResult.Issues),
		"batch_count":  plan.BatchCount,
		"model_id":     actualModelID,
	})

	return nil
}

// executeBatchPlan 分批决策（保持不变）
func (e *BatchReviewFrameExecutor) executeBatchPlan(ctx StageContext, task *model.Task) (*BatchPlan, error) {
	exec := ctx.CreateChildExecution("batch_plan", 0)
	ctx.MarkRunning(exec)

	diffFiles := ctx.GetInput("diff_files")
	if diffFiles == nil {
		ctx.MarkFailed(exec, "diff_files 为空")
		return nil, fmt.Errorf("diff_files 为空")
	}
	files, ok := diffFiles.([]map[string]interface{})
	if !ok {
		ctx.MarkFailed(exec, "diff_files 类型错误")
		return nil, fmt.Errorf("diff_files 类型错误")
	}

	overhead := BuildBatchContext(ctx)
	maxTokens := SysCfgMaxTokensPerBatch()
	estimator := NewTokenEstimator()
	batches := SmartSplitIntoBatches(files, maxTokens, overhead)

	overheadTokens := estimator.EstimateOverheadTokens(overhead)
	availableTokens := maxTokens - overheadTokens
	if availableTokens <= 0 {
		availableTokens = maxTokens / 2
	}

	plan := &BatchPlan{
		TotalFiles:        len(files),
		MaxTokensPerBatch: maxTokens,
		BatchCount:        len(batches),
		Strategy:          "multi",
		OverheadTokens:    overheadTokens,
		AvailableTokens:   availableTokens,
		Batches:           make([]BatchDetail, len(batches)),
	}
	if len(batches) == 1 {
		plan.Strategy = "single"
	}

	// 截断文件列表 + 警告
	var truncatedFiles []map[string]interface{}
	var truncationWarnings []string

	for i, batch := range batches {
		isTruncated := false
		for _, f := range batch.Files {
			if fm, ok := f.(map[string]interface{}); ok {
				if t, _ := fm["truncated"].(bool); t {
					isTruncated = true
					path, _ := fm["path"].(string)
					origDiff, _ := ctx.GetInput("diff_files").([]map[string]interface{})
					origTokens := 0
					for _, of := range origDiff {
						if of["path"] == path {
							if d, ok := of["diff"].(string); ok {
								origTokens = len(d) / 4
							}
						}
					}
					truncatedFiles = append(truncatedFiles, map[string]interface{}{
						"path":                 path,
						"batch_index":          i + 1,
						"original_diff_tokens": origTokens,
						"truncated_to_tokens":  plan.AvailableTokens,
					})
					truncationWarnings = append(truncationWarnings,
						fmt.Sprintf("批次 %d 中 %s 的 diff 被截断至 %d tokens", i+1, path, plan.AvailableTokens))
				}
			}
		}
		plan.Batches[i] = BatchDetail{
			Index:       i + 1,
			FileCount:   len(batch.Files),
			TokenEst:    batch.TokenEst,
			FilePaths:   batch.FilePaths,
			IsTruncated: isTruncated,
		}
	}

	tokenBreakdown := overhead.ToBreakdown(estimator)
	availableForDiff := maxTokens - tokenBreakdown["total_overhead"].(int)

	ctx.SaveOutputSnapshot(exec, map[string]interface{}{
		"plan":                plan,
		"token_breakdown":     tokenBreakdown,
		"available_for_diff":  availableForDiff,
		"truncated_files":     truncatedFiles,
		"truncation_warnings": truncationWarnings,
		"context_breakdown":   overhead,
		"input_files":         files,
	})
	ctx.MarkSuccess(exec)

	ctx.SetOutput("batch_plan", plan)
	for i, batch := range batches {
		if c, ok := ctx.(*stageContextImpl); ok {
			c.SetBatchFiles(i, batch.Files)
		}
	}

	return plan, nil
}

// executeSingleBatchStructured 场景 A：单批完整结构化评审
func (e *BatchReviewFrameExecutor) executeSingleBatchStructured(ctx StageContext, detail BatchDetail, promptCtx *engine.PromptContext, task *model.Task) (*llm.AIReviewResult, uint, error) {
	exec := ctx.CreateChildExecution("batch_review_1", 1)
	ctx.MarkRunning(exec)

	// 构建完整结构化 Prompt
	userPrompt, responseFormat := engine.BuildFullStructuredPrompt(promptCtx)

	fileDetails := buildBatchFileDetails(ctx, 0, detail.FilePaths)

	ctx.SaveInputSnapshot(exec, map[string]interface{}{
		"batch_index":  1,
		"file_count":   detail.FileCount,
		"file_paths":   detail.FilePaths,
		"file_details": fileDetails,
		"prompt":       userPrompt,
		"model_id":     task.UsedModelID,
		"mode":         "single_batch_structured",
	})

	result, err := e.llmService.ChatCompletionStructured(&task.ID, task.UsedModelID, "single_batch_structured", "", userPrompt, responseFormat)
	if err != nil {
		ctx.SaveOutputSnapshot(exec, map[string]interface{}{
			"batch_index": 1,
			"error":       err.Error(),
			"prompt":      userPrompt,
		})
		ctx.MarkFailed(exec, err.Error())
		return nil, 0, err
	}

	// Refusal 检测
	if result.Response != nil && len(result.Response.Choices) > 0 && result.Response.Choices[0].Message.Refusal != "" {
		ctx.MarkFailed(exec, "模型拒绝回答: "+result.Response.Choices[0].Message.Refusal)
		return nil, 0, fmt.Errorf("模型拒绝回答: %s", result.Response.Choices[0].Message.Refusal)
	}

	// 解析 + 后置校验
	parsedResult, err := engine.ParseReviewResult(result.Content, nil, getRetryConfig(), promptCtx.DeductScoreConfig)
	if err != nil {
		ctx.SaveOutputSnapshot(exec, map[string]interface{}{
			"batch_index": 1,
			"error":       err.Error(),
			"raw_content": result.Content,
			"prompt":      userPrompt,
		})
		ctx.MarkFailed(exec, err.Error())
		return nil, 0, err
	}

	exec.InputTokens = result.InputTokens
	exec.OutputTokens = result.OutputTokens
	exec.ModelName = result.ModelName
	if exec.LLMModelID == nil {
		exec.LLMModelID = &result.ModelID
	}

	ctx.SaveOutputSnapshot(exec, map[string]interface{}{
		"batch_index":   1,
		"file_count":    detail.FileCount,
		"file_paths":    detail.FilePaths,
		"file_details":  fileDetails,
		"parsed_issues": parsedResult.Issues,
		"total_score":   parsedResult.TotalScore,
		"dimensions":    parsedResult.Dimensions,
		"issue_count":   len(parsedResult.Issues),
		"input_tokens":  result.InputTokens,
		"output_tokens": result.OutputTokens,
		"model_name":    result.ModelName,
		"model_id":      result.ModelID,
		"prompt":        userPrompt,
		"review_result": result.Content,
	})
	ctx.MarkSuccess(exec)

	return parsedResult, result.ModelID, nil
}

// executeBatchCollection 场景 B：分批评审收集模式
func (e *BatchReviewFrameExecutor) executeBatchCollection(ctx StageContext, detail BatchDetail, promptCtx *engine.PromptContext, task *model.Task) (*llm.BatchReviewResult, error) {
	exec := ctx.CreateChildExecution(fmt.Sprintf("batch_review_%d", detail.Index), detail.Index)
	ctx.MarkRunning(exec)

	c, ok := ctx.(*stageContextImpl)
	if !ok {
		return nil, fmt.Errorf("invalid context type")
	}
	batchFiles := c.GetBatchFiles(detail.Index - 1)
	if batchFiles == nil {
		ctx.MarkFailed(exec, "批次文件未找到")
		return nil, fmt.Errorf("批次 %d 文件未找到", detail.Index)
	}

	plan := ctx.GetOutput("batch_plan").(*BatchPlan)

	// 构建分批评审收集 Prompt（使用截断后的 batchFiles）
	batchFileMaps := make([]map[string]interface{}, 0, len(batchFiles))
	for _, bf := range batchFiles {
		if fm, ok := bf.(map[string]interface{}); ok {
			batchFileMaps = append(batchFileMaps, fm)
		}
	}
	userPrompt := engine.BuildBatchCollectionPrompt(promptCtx, detail.Index, plan.BatchCount, batchFileMaps)

	fileDetails := buildBatchFileDetails(ctx, detail.Index-1, detail.FilePaths)

	ctx.SaveInputSnapshot(exec, map[string]interface{}{
		"batch_index":      detail.Index,
		"total_batches":    plan.BatchCount,
		"file_count":       detail.FileCount,
		"file_paths":       detail.FilePaths,
		"file_details":     fileDetails,
		"prompt":           userPrompt,
		"model_id":         task.UsedModelID,
		"available_tokens": plan.AvailableTokens,
		"mode":             "batch_collection",
	})

	// 使用简化 JSON Schema 调用结构化输出
	responseFormat := &llm.ResponseFormat{
		Type: "json_schema",
		JSONSchema: &llm.JSONSchema{
			Name:   "batch_review_result",
			Strict: true,
			Schema: llm.GetBatchCollectionJSONSchema(),
		},
	}

	result, err := e.llmService.ChatCompletionStructured(&task.ID, task.UsedModelID, "batch_collection", "", userPrompt, responseFormat)
	if err != nil {
		ctx.SaveOutputSnapshot(exec, map[string]interface{}{
			"batch_index": detail.Index,
			"error":       err.Error(),
			"prompt":      userPrompt,
		})
		ctx.MarkFailed(exec, err.Error())
		return nil, err
	}

	// Refusal 检测
	if result.Response != nil && len(result.Response.Choices) > 0 && result.Response.Choices[0].Message.Refusal != "" {
		ctx.MarkFailed(exec, "模型拒绝回答")
		return nil, fmt.Errorf("模型拒绝回答: %s", result.Response.Choices[0].Message.Refusal)
	}

	// 解析简化结果
	batchResult, err := engine.ParseBatchReviewResult(result.Content, promptCtx.DeductScoreConfig)
	if err != nil {
		ctx.SaveOutputSnapshot(exec, map[string]interface{}{
			"batch_index": detail.Index,
			"error":       err.Error(),
			"raw_content": result.Content,
			"prompt":      userPrompt,
		})
		ctx.MarkFailed(exec, err.Error())
		return nil, err
	}

	exec.InputTokens = result.InputTokens
	exec.OutputTokens = result.OutputTokens
	exec.ModelName = result.ModelName
	if exec.LLMModelID == nil {
		exec.LLMModelID = &result.ModelID
	}

	ctx.SaveOutputSnapshot(exec, map[string]interface{}{
		"batch_index":   detail.Index,
		"total_batches": plan.BatchCount,
		"file_count":    detail.FileCount,
		"file_paths":    detail.FilePaths,
		"file_details":  fileDetails,
		"issue_count":   len(batchResult.Issues),
		"input_tokens":  result.InputTokens,
		"output_tokens": result.OutputTokens,
		"model_name":    result.ModelName,
		"model_id":      result.ModelID,
		"prompt":        userPrompt,
		"review_result": result.Content,
	})
	ctx.MarkSuccess(exec)

	return batchResult, nil
}

// executeScoreArbitration 场景 C：汇总裁决模式
func (e *BatchReviewFrameExecutor) executeScoreArbitration(ctx StageContext, batchResults []*llm.BatchReviewResult, promptCtx *engine.PromptContext, task *model.Task) (*llm.AIReviewResult, uint, error) {
	exec := ctx.CreateChildExecution("batch_summary", 0)
	ctx.MarkRunning(exec)

	userPrompt, responseFormat := engine.BuildScoreArbitrationPrompt(promptCtx, batchResults)

	ctx.SaveInputSnapshot(exec, map[string]interface{}{
		"batch_count":        len(batchResults),
		"merged_issue_count": countTotalIssues(batchResults),
		"prompt":             userPrompt,
		"model_id":           task.UsedModelID,
		"mode":               "score_arbitration",
	})

	result, err := e.llmService.ChatCompletionStructured(&task.ID, task.UsedModelID, "score_arbitration", "", userPrompt, responseFormat)
	if err != nil {
		ctx.SaveOutputSnapshot(exec, map[string]interface{}{
			"batch_count": len(batchResults),
			"error":       err.Error(),
			"prompt":      userPrompt,
		})
		ctx.MarkFailed(exec, err.Error())
		return nil, 0, err
	}

	// Refusal 检测
	if result.Response != nil && len(result.Response.Choices) > 0 && result.Response.Choices[0].Message.Refusal != "" {
		ctx.MarkFailed(exec, "模型拒绝回答")
		return nil, 0, fmt.Errorf("模型拒绝回答: %s", result.Response.Choices[0].Message.Refusal)
	}

	// 解析 + 后置校验
	parsedResult, err := engine.ParseReviewResult(result.Content, nil, getRetryConfig(), promptCtx.DeductScoreConfig)
	if err != nil {
		ctx.SaveOutputSnapshot(exec, map[string]interface{}{
			"batch_count": len(batchResults),
			"error":       err.Error(),
			"raw_content": result.Content,
			"prompt":      userPrompt,
		})
		ctx.MarkFailed(exec, err.Error())
		return nil, 0, err
	}

	exec.InputTokens = result.InputTokens
	exec.OutputTokens = result.OutputTokens
	exec.ModelName = result.ModelName
	if exec.LLMModelID == nil {
		exec.LLMModelID = &result.ModelID
	}

	ctx.SaveOutputSnapshot(exec, map[string]interface{}{
		"batch_count":   len(batchResults),
		"total_score":   parsedResult.TotalScore,
		"dimensions":    parsedResult.Dimensions,
		"issue_count":   len(parsedResult.Issues),
		"input_tokens":  result.InputTokens,
		"output_tokens": result.OutputTokens,
		"model_name":    result.ModelName,
		"model_id":      result.ModelID,
		"prompt":        userPrompt,
		"review_result": result.Content,
	})
	ctx.MarkSuccess(exec)

	return parsedResult, result.ModelID, nil
}

// assembleMarkdownReport 将结构化结果组装为 Markdown 评论
func (e *BatchReviewFrameExecutor) assembleMarkdownReport(result *llm.AIReviewResult, promptCtx *engine.PromptContext) string {
	report, err := engine.AssembleMarkdownCommentWithVulns(result, promptCtx.GitLabCommentTemplate, promptCtx.DependencyVulns)
	if err != nil {
		// 组装失败，fallback 到原始 JSON 的简易 Markdown
		return fmt.Sprintf("## 🤖 AI 代码评审报告\n\n**综合评分：%d/100**\n\n%s",
			result.TotalScore, result.Summary)
	}
	return report
}

// ========== PostProcessExecutor ==========
type PostProcessExecutor struct{}

func (e *PostProcessExecutor) Code() string { return "post_process" }

func (e *PostProcessExecutor) Execute(ctx StageContext) error {
	task := ctx.Task()
	if task == nil {
		return nil
	}

	finalReport, _ := ctx.GetOutput("final_report").(string)
	score, _ := ctx.GetOutput("score").(int)

	// 获取 Pipeline 内部生成的结构化结果
	result, ok := ctx.GetOutput("ai_review_result").(*llm.AIReviewResult)
	if ok && result != nil {
		// 1. 持久化结构化数据到 review_issues
		if err := engine.PersistStructuredReview(task.ID, result); err != nil {
			zap.L().Warn("Pipeline: 持久化结构化评审失败", zap.Error(err))
		}

		// 2. 持久化任务实际使用的评审规则（含截断记录）
		if selected, ok := ctx.GetInput("selected_rules").([]model.ReviewRule); ok {
			var truncated []model.ReviewRule
			if t, ok2 := ctx.GetInput("truncated_rules").([]model.ReviewRule); ok2 {
				truncated = t
			}
			if err := engine.PersistTaskReviewRules(task.ID, selected, truncated); err != nil {
				zap.L().Warn("Pipeline: 持久化任务评审规则失败", zap.Error(err))
			}
		}

		// 3. 持久化 Task 字段（含实际使用的模型 ID）
		updates := map[string]interface{}{
			"ai_response_json": marshalJSON(result),
			"dimension_scores": marshalJSON(result.Dimensions),
			"issue_count":      len(result.Issues),
			"score_value":      result.TotalScore,
			"raw_ai_score":     result.OriginalTotalScore,
		}
		// 从 Pipeline 输出获取实际使用的模型 ID
		if modelID, ok := ctx.GetOutput("model_id").(uint); ok && modelID > 0 {
			updates["model_id"] = modelID
			zap.L().Info("Pipeline: 更新任务实际使用模型", zap.Uint("task_id", task.ID), zap.Uint("model_id", modelID))
		}
		model.DB.Model(&model.Task{}).Where("id = ?", task.ID).Updates(updates)
	} else {
		zap.L().Warn("Pipeline: 未生成结构化结果，跳过持久化", zap.Uint("task_id", task.ID))
	}

	// 更新 AIResponse（Markdown 报告）
	if finalReport != "" {
		model.DB.Model(&model.Task{}).Where("id = ?", task.ID).Update("ai_response", finalReport)
	}

	ctx.SaveInputSnapshot(&model.TaskPipelineExecution{ID: ctx.ExecutionID()}, map[string]interface{}{
		"task_id":      task.ID,
		"final_report": finalReport,
		"score":        score,
	})

	ctx.SaveOutputSnapshot(&model.TaskPipelineExecution{ID: ctx.ExecutionID()}, map[string]interface{}{
		"status":         "completed",
		"task_id":        task.ID,
		"final_report":   finalReport,
		"score":          score,
		"comment_posted": finalReport != "",
	})
	return nil
}

// ========== 辅助函数 ==========

// getPromptContext 从 StageContext 获取 PromptContext
func getPromptContext(ctx StageContext) *engine.PromptContext {
	if v, ok := ctx.GetInput("prompt_context").(*engine.PromptContext); ok {
		return v
	}
	return nil
}

// getRetryConfig 从 SystemConfig 读取重试配置
func getRetryConfig() *engine.RetryConfig {
	var sysCfg model.SystemConfig
	cfg := &engine.RetryConfig{
		MaxAttempts:       3,
		InitialDelay:      2 * time.Second,
		BackoffMultiplier: 2.0,
		MaxDelay:          30 * time.Second,
		FallbackStrategy:  "regex",
	}
	if err := model.DB.First(&sysCfg).Error; err == nil {
		if sysCfg.JSONRetryMaxAttempts > 0 {
			cfg.MaxAttempts = sysCfg.JSONRetryMaxAttempts
		}
		if sysCfg.JSONRetryInitialDelaySec > 0 {
			cfg.InitialDelay = time.Duration(sysCfg.JSONRetryInitialDelaySec) * time.Second
		}
		if sysCfg.JSONRetryBackoffMultiplier > 0 {
			cfg.BackoffMultiplier = sysCfg.JSONRetryBackoffMultiplier
		}
		if sysCfg.JSONRetryMaxDelaySec > 0 {
			cfg.MaxDelay = time.Duration(sysCfg.JSONRetryMaxDelaySec) * time.Second
		}
		if sysCfg.JSONRetryFallbackStrategy != "" {
			cfg.FallbackStrategy = sysCfg.JSONRetryFallbackStrategy
		}
	}
	return cfg
}

// countTotalIssues 统计所有批次的问题总数
func countTotalIssues(batchResults []*llm.BatchReviewResult) int {
	total := 0
	for _, br := range batchResults {
		if br != nil {
			total += len(br.Issues)
		}
	}
	return total
}

// marshalJSON 安全 JSON 序列化
func marshalJSON(v interface{}) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// buildBatchFileDetails 从批次文件中提取文件详情（path, additions, deletions, status）
func buildBatchFileDetails(ctx StageContext, batchIdx int, filePaths []string) []map[string]interface{} {
	diffFiles := ctx.GetInput("diff_files")
	if diffFiles == nil {
		return nil
	}
	allFiles, ok := diffFiles.([]map[string]interface{})
	if !ok {
		return nil
	}
	var details []map[string]interface{}
	for _, fp := range filePaths {
		for _, f := range allFiles {
			path, _ := f["path"].(string)
			if path != fp {
				continue
			}
			additions, _ := f["additions"].(int)
			deletions, _ := f["deletions"].(int)
			status := "modified"
			if deleted, _ := f["deleted_file"].(bool); deleted {
				status = "deleted"
			} else if newFile, _ := f["new_file"].(bool); newFile {
				status = "added"
			}
			details = append(details, map[string]interface{}{
				"path":      path,
				"status":    status,
				"additions": additions,
				"deletions": deletions,
			})
		}
	}
	return details
}

// getASTContextText 从 StageContext 获取 AST 上下文文本
func getASTContextText(ctx StageContext) string {
	if v, ok := ctx.GetInput("ast_context").(string); ok {
		return v
	}
	return ""
}

// getCrossFileCallChainText 从 StageContext 获取跨文件调用链文本
func getCrossFileCallChainText(ctx StageContext) string {
	if v, ok := ctx.GetInput("cross_file_call_chain").(string); ok {
		return v
	}
	return ""
}

func extractScoreFromReport(report string) int {
	re := regexp.MustCompile(`AI\s*评分\s*[:：]\s*(\d+(?:\.\d+)?)\s*分`)
	matches := re.FindAllStringSubmatch(report, -1)
	if len(matches) == 0 {
		return 0
	}
	scoreStr := matches[len(matches)-1][1]
	score, err := strconv.ParseFloat(scoreStr, 64)
	if err != nil {
		return 0
	}
	if score < 0 {
		score = 0
	}
	if score > 100 {
		score = 100
	}
	return int(score)
}
