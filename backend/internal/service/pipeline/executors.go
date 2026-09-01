package pipeline

	import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ai-optimizer/backend/config"
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
		"task_id":        task.ID,
		"mr_iid":         task.MRMergeID,
		"mr_title":       task.MRTitle,
		"author":         task.MRAuthor,
		"project_id":     task.ProjectID,
		"project_name":   task.Project.Name,
		"task_type":      task.TaskType,
		"trigger_source": task.TriggerSource,
	})

	// 检查 1：MR IID 是否有效
	if task.MRMergeID <= 0 {
		reason := fmt.Sprintf("无效 MR IID: %d", task.MRMergeID)
		ctx.SaveOutputSnapshot(&model.TaskPipelineExecution{ID: ctx.ExecutionID()}, map[string]interface{}{
			"mr_iid":   task.MRMergeID,
			"mr_title": task.MRTitle,
			"author":   task.MRAuthor,
			"result":   "失败",
			"reason":   reason,
		})
		return fmt.Errorf("%s", reason)
	}

	// 检查 2：触发事件是否在全局配置白名单中
	var agentCfg model.ReviewAgentConfig
	if err := model.DB.First(&agentCfg, 1).Error; err == nil {
		allowedEvents := agentCfg.TriggerEventCodes()
		if len(allowedEvents) > 0 {
			matched := false
			ts := task.TriggerSource
			if ts == "" {
				// trigger_source 为空表示来源不明（如 Dashboard 手动触发），默认放行
				matched = true
			} else {
				for _, ev := range allowedEvents {
					if ev == ts {
						matched = true
						break
					}
					// 兼容旧配置：merge_request 包含所有子事件（merge_request_open/update/reopen）
					if ev == "merge_request" && strings.HasPrefix(ts, "merge_request_") {
						matched = true
						break
					}
				}
			}
			if !matched {
				reason := fmt.Sprintf("触发源 '%s' 不在允许列表中（当前允许: %v）", ts, allowedEvents)
				ctx.SaveOutputSnapshot(&model.TaskPipelineExecution{ID: ctx.ExecutionID()}, map[string]interface{}{
					"mr_iid":         task.MRMergeID,
					"mr_title":       task.MRTitle,
					"author":         task.MRAuthor,
					"result":         "阻止",
					"reason":         reason,
					"trigger_source": ts,
				})
				return fmt.Errorf("%s", reason)
			}
		}
	}

	ctx.SaveOutputSnapshot(&model.TaskPipelineExecution{ID: ctx.ExecutionID()}, map[string]interface{}{
		"mr_iid":   task.MRMergeID,
		"mr_title": task.MRTitle,
		"author":   task.MRAuthor,
		"result":   "通过",
		"reason":   "MR IID 有效且触发源在允许列表中",
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

	if repoDir != "" {
		// ========== 方案 C：基于完整代码库的跨文件分析 ==========
		analyzer := NewCrossFileAnalyzer(repoDir, lang)
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
		// 检查任务是否已被取消
		select {
		case <-ctx.Done():
			return fmt.Errorf("任务已取消")
		default:
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
			"functions":        len(fileAST.Functions),
			"functions_count":  len(fileAST.Functions), // 兼容旧前端/消费者
			"structs":          len(fileAST.Structs),
			"structs_count":    len(fileAST.Structs), // 兼容旧前端/消费者
			"interfaces":       len(fileAST.Interfaces),
			"interfaces_count": len(fileAST.Interfaces), // 兼容旧前端/消费者
			"imports":          len(fileAST.Imports),
			"imports_count":    len(fileAST.Imports), // 兼容旧前端/消费者
			"callers":          len(fileAST.Callers),
			"callers_count":    len(fileAST.Callers), // 兼容旧前端/消费者
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

	// 将完整文件内容保存到 output，供后续 dependency_scan 解析依赖使用
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

	// 按文件路径排序，保证输出顺序稳定
	paths := make([]string, 0, len(cfc.FileContexts))
	for path := range cfc.FileContexts {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	for _, path := range paths {
		fc := cfc.FileContexts[path]
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
	// 优先读取未过滤的原始 diff（包含 go.mod / package.json 等依赖清单）
	// 如果原始 diff 不存在（如旧任务），则回退到过滤后的 diff_files
	diffFiles := ctx.GetInput("diff_files_raw")
	if diffFiles == nil {
		diffFiles = ctx.GetInput("diff_files")
	}
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
	// 记录每个依赖（name:version）是否属于本次 diff 变更
	depChangeMap := make(map[string]bool)

	for _, f := range files {
		path, _ := f["path"].(string)
		if path == "" {
			continue
		}
		// 检查任务是否已被取消
		select {
		case <-ctx.Done():
			return fmt.Errorf("任务已取消")
		default:
		}

		content := e.getFileContent(ctx, path)
		if content == "" {
			continue
		}

		// 提取该文件 diff 中的 + 行内容（去掉 + 号），用于内容级匹配
		var changedContents map[string]bool
		if diffText, _ := f["diff"].(string); diffText != "" {
			changedContents = extractChangedLineContents(diffText)
		}

		deps, err := ParseDependenciesFromFile(path, content)
		if err != nil {
			zap.L().Debug("parse dependencies failed, file is not a dependency manifest", zap.String("path", path), zap.Error(err))
			continue
		}

		// 按行切分 content，用于和 diff 中的 + 行做内容匹配
		// contentLines 和 dep.LineNumber 始终来自同一数据源（完整文件或 diff 片段），保证一致
		contentLines := strings.Split(content, "\n")

		for _, dep := range deps {
			// 用依赖声明所在行的文本内容与 diff + 行匹配，避免绝对行号错位
			if changedContents != nil && dep.LineNumber > 0 && dep.LineNumber <= len(contentLines) {
				lineText := strings.TrimSpace(contentLines[dep.LineNumber-1])
				dep.IsInDiff = changedContents[lineText]
			}
			// 记录变更标记，供后续组装 engine.DependencyVuln 使用
			// 使用 OR 语义：只要有一个同名同版本的依赖声明落在 diff + 行上，就视为变更
			// ⚠️ 必须在生态过滤之前记录，避免变更项被过滤后漏标记
			key := dep.Name + ":" + dep.Version
			if dep.IsInDiff {
				depChangeMap[key] = true
			}
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
			isRelated := depChangeMap[m.PackageName+":"+m.CurrentVersion]
			dependencyVulns = append(dependencyVulns, engine.DependencyVuln{
				PackageName:       m.PackageName,
				CurrentVersion:    m.CurrentVersion,
				VulnID:            m.VulnID,
				Aliases:           m.Aliases,
				Severity:          m.Severity,
				Summary:           m.Summary,
				FixedVersion:      m.FixedVersion,
				IsRelatedToChange: isRelated,
			})
		}
	}

	depMarkdown := ""
	if len(dependencyVulns) > 0 {
		depMarkdown = engine.BuildDependencyVulnsSection(dependencyVulns)
	}
	ctx.SetOutput("dependency_vulns_markdown", depMarkdown)
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
		"dependency_vulns":      dependencyVulns,
		"deps_parsed":           len(allDeps),
		"direct_deps":           directDepCount,
		"indirect_deps":         indirectDepCount,
		"prompt_injection":      depMarkdown,
	})

	return nil
}

// getFileContent 获取文件内容：
// 1. 优先从 context_extract 输出的 file_contents 中获取（完整文件内容）
// 2. 其次从 diff_files_raw / diff_files 的 "content" 字段获取
// 3. 最后从 diff 字段解析出完整内容（新增/修改的依赖清单文件通常是完整或近完整内容）
func (e *DependencyScanExecutor) getFileContent(ctx StageContext, filePath string) string {
	// 1. 从 context_extract 输出的 file_contents 中查找
	if fc, ok := ctx.GetOutput("file_contents").(map[string]string); ok {
		if content, exists := fc[filePath]; exists && content != "" {
			return content
		}
	}
	// 2. 从 diff 列表的 "content" 字段中查找
	for _, key := range []string{"diff_files_raw", "diff_files"} {
		if files := ctx.GetInput(key); files != nil {
			if fileMaps, ok := files.([]map[string]interface{}); ok {
				for _, f := range fileMaps {
					if p, _ := f["path"].(string); p != filePath {
						continue
					}
					if content, ok := f["content"].(string); ok && content != "" {
						return content
					}
					// 3. 从 diff 字段解析完整内容（关键！被过滤后 file_contents 缺失时，diff 中仍有内容）
					if diffText, ok := f["diff"].(string); ok && diffText != "" {
						content := extractContentFromDiff(diffText)
						if content != "" {
							return content
						}
					}
				}
			}
		}
	}
	return ""
}

// extractContentFromDiff 从 unified diff 字符串中提取完整的新文件内容
// 对于新增文件，diff 中的 + 行去掉 + 后即为完整内容；
// 对于修改文件，保留上下文行（空格前缀）和 + 行（去掉 +），去掉 - 行和 diff header。
func extractContentFromDiff(diffText string) string {
	lines := strings.Split(diffText, "\n")
	var result []string
	inHunk := false
	for _, line := range lines {
		if len(line) == 0 {
			// 空行：如果已经进入 hunk，保留作为内容的一部分
			if inHunk {
				result = append(result, "")
			}
			continue
		}
		// diff header 行：跳过
		if strings.HasPrefix(line, "diff --git") ||
			strings.HasPrefix(line, "index ") ||
			strings.HasPrefix(line, "--- ") ||
			strings.HasPrefix(line, "+++ ") ||
			strings.HasPrefix(line, "@@") {
			inHunk = true
			continue
		}
		// new file mode / deleted file mode / old mode / new mode
		if strings.HasPrefix(line, "new file mode") ||
			strings.HasPrefix(line, "deleted file mode") ||
			strings.HasPrefix(line, "old mode") ||
			strings.HasPrefix(line, "new mode") ||
			strings.HasPrefix(line, "similarity index") ||
			strings.HasPrefix(line, "rename from") ||
			strings.HasPrefix(line, "rename to") ||
			strings.HasPrefix(line, "Binary files") {
			continue
		}
		if !inHunk {
			// 不在 hunk 中，遇到非 header 行，可能是 "\\ No newline at end of file"
			if strings.HasPrefix(line, "\\ No newline") {
				continue
			}
			// 如果还没进入 hunk（如文件没有内容变更但有 mode 变更），也不处理
			continue
		}
		// hunk 内容行
		switch line[0] {
		case '+':
			result = append(result, line[1:])
		case '-':
			// 删除行：在新版本中不存在，跳过
			continue
		case ' ':
			// 上下文行：保留（去掉前导空格）
			result = append(result, line[1:])
		case '\\':
			// "\ No newline at end of file"
			continue
		default:
			// 意外情况，保留原行
			result = append(result, line)
		}
	}
	return strings.Join(result, "\n")
}

func stringSliceContains(arr []string, s string) bool {
	for _, v := range arr {
		if v == s {
			return true
		}
	}
	return false
}

// extractChangedLineContents 从 unified diff 中提取 + 行的内容（去掉 + 号和首尾空格）
// 用于与 Parser 解析出的依赖声明行做内容级匹配，避免绝对行号错位
func extractChangedLineContents(diffText string) map[string]bool {
	changed := make(map[string]bool)
	for _, line := range strings.Split(diffText, "\n") {
		if len(line) > 0 && line[0] == '+' && !strings.HasPrefix(line, "+++") {
			changed[strings.TrimSpace(line[1:])] = true
		}
	}
	return changed
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

	// 注入 Phase C 扩展智能体产出
	promptCtx.AgentMarkdowns = make(map[string]string)
	if secretFindings, ok := ctx.GetOutput("secret_scan_findings").([]model.SecretScanFinding); ok && len(secretFindings) > 0 {
		promptCtx.SecretScanFindings = secretFindings
	}
	if auditFindings, ok := ctx.GetOutput("security_audit_findings").([]model.SecurityAuditFinding); ok && len(auditFindings) > 0 {
		promptCtx.SecurityAuditFindings = auditFindings
	}
	if impactFindings, ok := ctx.GetOutput("impact_analysis_findings").([]engine.ImpactFinding); ok && len(impactFindings) > 0 {
		promptCtx.ImpactFindings = impactFindings
	}
	if testSuggestions, ok := ctx.GetOutput("test_suggestions").([]engine.TestSuggestionItem); ok && len(testSuggestions) > 0 {
		promptCtx.TestSuggestions = testSuggestions
	}

	// 读取各智能体预渲染 Markdown
	if md, ok := ctx.GetOutput("secret_scan_markdown").(string); ok && md != "" {
		promptCtx.AgentMarkdowns["secret_scan"] = md
	}
	if md, ok := ctx.GetOutput("security_audit_markdown").(string); ok && md != "" {
		promptCtx.AgentMarkdowns["security_audit"] = md
	}
	if md, ok := ctx.GetOutput("impact_analysis_markdown").(string); ok && md != "" {
		promptCtx.AgentMarkdowns["impact_analysis"] = md
	}
	if md, ok := ctx.GetOutput("test_suggestion_markdown").(string); ok && md != "" {
		promptCtx.AgentMarkdowns["test_suggestion"] = md
	}
	if md, ok := ctx.GetOutput("dependency_vulns_markdown").(string); ok && md != "" {
		promptCtx.AgentMarkdowns["dependency_scan"] = md
	}
	if r, ok := ctx.GetOutput("code_understanding_report").(string); ok && r != "" {
		promptCtx.AgentMarkdowns["code_understanding"] = r
	}

	// ==================== 场景 A：单批轻量收集（与多批统一） ====================
	var actualModelID uint
	if plan.BatchCount == 1 {
		batchResult, modelID, inTk, outTk, modelName, err := e.executeBatchCollection(ctx, plan.Batches[0], promptCtx, task)
		if err != nil {
			ctx.SaveOutputSnapshot(&model.TaskPipelineExecution{ID: ctx.ExecutionID()}, map[string]interface{}{
				"plan":        plan,
				"error":       err.Error(),
				"batch_count": plan.BatchCount,
			})
			return err
		}
		actualModelID = modelID

		ctx.SetOutput("batch_review_results", []*llm.BatchReviewResult{batchResult})
		ctx.SetOutput("model_id", actualModelID)

		// 同步 token 用量到 selfExec
		if selfExec := ctx.GetSelfExec(); selfExec != nil {
			selfExec.InputTokens += inTk
			selfExec.OutputTokens += outTk
			if selfExec.ModelName == "" {
				selfExec.ModelName = modelName
			}
			if selfExec.LLMModelID == nil {
				selfExec.LLMModelID = &actualModelID
			}
		}

		// 单批场景也需要更新批次进度和 output_snapshot，保证前端详情面板能正确显示批次进度
		ctx.UpdateProgress(nil, 1, plan.BatchCount)
		zap.L().Info("单批评审完成",
			zap.Uint("task_id", task.ID),
			zap.Int("issue_count", len(batchResult.Issues)),
			zap.Int("input_tokens", inTk),
			zap.Int("output_tokens", outTk),
			zap.String("model_name", modelName))
		outputSnap := map[string]interface{}{
			"plan":        plan,
			"batch_count": plan.BatchCount,
			"model_id":    actualModelID,
		}
		if selfExec := ctx.GetSelfExec(); selfExec != nil {
			outputSnap["input_tokens"] = selfExec.InputTokens
			outputSnap["output_tokens"] = selfExec.OutputTokens
			outputSnap["model_name"] = selfExec.ModelName
		}
		ctx.SaveOutputSnapshot(&model.TaskPipelineExecution{ID: ctx.ExecutionID()}, outputSnap)
		return nil
	}

	// ==================== 场景 B/C：多批收集 + 汇总裁决 ====================
	// S2: 多批评审（按 wave 分组并行）
	maxParallel := 3 // 默认 3
	if pm, ok := ctx.GetInput("_batch_parallel_max").(int); ok && pm > 0 {
		maxParallel = pm
	}
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
				// 检查任务是否已被取消
				select {
				case <-ctx.Done():
					errCh <- fmt.Errorf("任务已取消")
					return
				default:
				}
				result, modelID, inTk, outTk, mName, err := e.executeBatchCollection(ctx, plan.Batches[batchIdx], promptCtx, task)
				if err != nil {
					errCh <- fmt.Errorf("wave %d, batch %d 失败: %w", waveIdx+1, batchIdx+1, err)
					return
				}
				mu.Lock()
				batchResults[batchIdx] = result
				if actualModelID == 0 {
					actualModelID = modelID
				}
				// 累加 token 用量
				if exec := ctx.GetSelfExec(); exec != nil {
					exec.InputTokens += inTk
					exec.OutputTokens += outTk
					if mName != "" && exec.ModelName == "" {
						exec.ModelName = mName
					}
					if exec.LLMModelID == nil {
						exec.LLMModelID = &modelID
					}
				}
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

	// S3: 【改造】不再内部调用 executeScoreArbitration
	// 评审结果存储到 PipelineContext，供 review_arbitration 独立阶段处理
	ctx.SetOutput("batch_review_results", batchResults)
	ctx.SetOutput("model_id", actualModelID)

	outputSnap := map[string]interface{}{
		"plan":        plan,
		"batch_count": plan.BatchCount,
		"model_id":    actualModelID,
	}
	if selfExec := ctx.GetSelfExec(); selfExec != nil {
		outputSnap["input_tokens"] = selfExec.InputTokens
		outputSnap["output_tokens"] = selfExec.OutputTokens
		outputSnap["model_name"] = selfExec.ModelName
	}
	ctx.SaveOutputSnapshot(&model.TaskPipelineExecution{ID: ctx.ExecutionID()}, outputSnap)

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
	configuredBudget := SysCfgMaxTokensPerBatch()
	estimator := NewTokenEstimator()

	// 【新增】估算系统开销
	estimatedOverhead := estimator.EstimateOverheadTokens(overhead)

	// 【新增】计算有效预算（预算不足时自动扩大）
	effectiveBudget, budgetWarning := CalculateEffectiveBudget(configuredBudget, estimatedOverhead)
	if budgetWarning != "" {
		zap.L().Warn("分批评审预算自动扩大",
			zap.Uint("task_id", task.ID),
			zap.Int("configured_budget", configuredBudget),
			zap.Int("estimated_overhead", estimatedOverhead),
			zap.Int("effective_budget", effectiveBudget))
	}

	// 使用 effectiveBudget 进行分批（而非用户配置值）
	batches := SmartSplitIntoBatches(files, effectiveBudget, overhead)

	availableTokens := effectiveBudget - estimatedOverhead
	if availableTokens <= 0 {
		availableTokens = effectiveBudget / 5 // 至少保留20%空间
	}

	plan := &BatchPlan{
		TotalFiles:        len(files),
		MaxTokensPerBatch: configuredBudget, // 用户原始配置
		EffectiveBudget:   effectiveBudget,  // 自动扩大后的实际预算
		EstimatedOverhead: estimatedOverhead,
		BatchCount:        len(batches),
		Strategy:          "multi",
		OverheadTokens:    estimatedOverhead,
		AvailableTokens:   availableTokens,
		BudgetWarning:     budgetWarning,
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
	availableForDiff := effectiveBudget - tokenBreakdown["total_overhead"].(int)

	ctx.SaveOutputSnapshot(exec, map[string]interface{}{
		"plan": map[string]interface{}{
			"max_tokens_per_batch": configuredBudget,
			"effective_budget":     effectiveBudget,
			"estimated_overhead":   estimatedOverhead,
			"available_tokens":     availableTokens,
			"budget_warning":       budgetWarning,
			"strategy":             plan.Strategy,
			"batch_count":          plan.BatchCount,
			"total_files":          plan.TotalFiles,
			"batches":              plan.Batches,
		},
		"token_breakdown":     tokenBreakdown,
		"available_for_diff":  availableForDiff,
		"truncated_files":     truncatedFiles,
		"truncation_warnings": truncationWarnings,
		"context_breakdown":   overhead,
		"input_files":         files,
	})

	// 【新增】保存预算信息到 execution 记录（使用 Updates 避免覆盖 snapshot 字段）
	exec.EstimatedOverhead = estimatedOverhead
	exec.LLMInputBudget = configuredBudget
	exec.EffectiveBudget = effectiveBudget
	exec.BudgetWarning = budgetWarning
	model.DB.Model(exec).Updates(map[string]interface{}{
		"estimated_overhead": estimatedOverhead,
		"actual_overhead":    0, // batch_plan 阶段无实际开销
		"llm_input_budget":   configuredBudget,
		"effective_budget":   effectiveBudget,
		"budget_warning":     budgetWarning,
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
// Deprecated: 单批场景已统一使用 executeBatchCollection，不再要求模型计算分数。
// 保留实现以供紧急回滚，但不再被调用。
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

	result, err := e.llmService.ChatCompletionStructured(ctx, &task.ID, task.UsedModelID, "single_batch_structured", "", userPrompt, responseFormat)
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

	// 【修复】将 token 同步到 selfExec，供 Pipeline 列表页展示
	if selfExec := ctx.GetSelfExec(); selfExec != nil {
		selfExec.InputTokens = result.InputTokens
		selfExec.OutputTokens = result.OutputTokens
		selfExec.ModelName = result.ModelName
		if selfExec.LLMModelID == nil {
			selfExec.LLMModelID = &result.ModelID
		}
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

	// 【新增】保存实际开销到 execution 记录，并异步保存校准数据
	exec.ActualOverhead = result.InputTokens - calcDiffTokens(fileDetails)
	model.DB.Model(exec).Update("actual_overhead", exec.ActualOverhead)
	go func() {
		estOH := NewTokenEstimator().EstimateOverheadTokens(BuildBatchContext(ctx))
		saveOverheadCalibration(
			task.ID, exec.ID, exec.StageCode,
			len(promptCtx.Rules), estOH,
			result.InputTokens, calcDiffTokens(fileDetails),
		)
	}()

	ctx.MarkSuccess(exec)

	return parsedResult, result.ModelID, nil
}

// filterCrossFileContextForBatch 从全局跨文件调用链文本中提取仅与当前批次文件相关的部分。
// crossFileText 由 buildCrossFileCallChainText 生成，按 "### 文件: path" 分段。
func filterCrossFileContextForBatch(crossFileText string, batchFilePaths []string) string {
	if crossFileText == "" || len(batchFilePaths) == 0 {
		return crossFileText
	}

	// 快速查找集合
	batchSet := make(map[string]bool, len(batchFilePaths))
	for _, p := range batchFilePaths {
		batchSet[p] = true
	}

	const header = "## 跨文件调用链上下文\n\n"
	if !strings.HasPrefix(crossFileText, header) {
		return crossFileText
	}

	var result strings.Builder
	result.WriteString(header)

	trimmed := strings.TrimPrefix(crossFileText, header)
	// 按 "### 文件: " 分割段落
	sections := strings.Split(trimmed, "### 文件: ")

	keptAny := false
	for _, section := range sections[1:] { // sections[0] 为空或残留
		section = strings.TrimPrefix(section, "### 文件: ")
		lines := strings.SplitN(section, "\n", 2)
		if len(lines) < 1 {
			continue
		}
		filePath := strings.TrimSpace(lines[0])
		if batchSet[filePath] {
			result.WriteString("### 文件: ")
			result.WriteString(section)
			keptAny = true
		}
	}

	if !keptAny {
		return ""
	}
	return result.String()
}

// executeBatchCollection 场景 B：分批评审收集模式
// 返回 (batchResult, modelID, inputTokens, outputTokens, modelName, error)
func (e *BatchReviewFrameExecutor) executeBatchCollection(ctx StageContext, detail BatchDetail, promptCtx *engine.PromptContext, task *model.Task) (*llm.BatchReviewResult, uint, int, int, string, error) {
	exec := ctx.CreateChildExecution(fmt.Sprintf("batch_review_%d", detail.Index), detail.Index)
	ctx.MarkRunning(exec)

	c, ok := ctx.(*stageContextImpl)
	if !ok {
		return nil, 0, 0, 0, "", fmt.Errorf("invalid context type")
	}
	batchFiles := c.GetBatchFiles(detail.Index - 1)
	if batchFiles == nil {
		ctx.MarkFailed(exec, "批次文件未找到")
		return nil, 0, 0, 0, "", fmt.Errorf("批次 %d 文件未找到", detail.Index)
	}

	plan := ctx.GetOutput("batch_plan").(*BatchPlan)

	// 构建分批评审收集 Prompt（使用截断后的 batchFiles）
	batchFileMaps := make([]map[string]interface{}, 0, len(batchFiles))
	for _, bf := range batchFiles {
		if fm, ok := bf.(map[string]interface{}); ok {
			batchFileMaps = append(batchFileMaps, fm)
		}
	}

	// 为当前批次过滤跨文件调用链上下文，避免每批都传入无关的调用链信息
	batchPromptCtx := *promptCtx
	batchPromptCtx.CrossFileContext = filterCrossFileContextForBatch(promptCtx.CrossFileContext, detail.FilePaths)

	// 【新增】按批次文件过滤 code_understanding 内容，每批仅注入当前批次相关的 AST/图谱摘要
	// 注意：AgentMarkdowns 是 map（引用类型），必须深拷贝以避免并发写入 panic
	batchAgentMarkdowns := make(map[string]string, len(promptCtx.AgentMarkdowns)+1)
	for k, v := range promptCtx.AgentMarkdowns {
		batchAgentMarkdowns[k] = v
	}

	if codeReport, ok := ctx.GetOutput("code_understanding_structured").(*CodeUnderstandingReport); ok && codeReport != nil {
		batchMarkdown := formatReportTextForBatch(codeReport, detail.FilePaths)
		batchAgentMarkdowns["code_understanding"] = batchMarkdown
	} else if fullMarkdown, ok := ctx.GetOutput("code_understanding_report").(string); ok && fullMarkdown != "" {
		// fallback：从全局 Markdown 文本中按文件路径做简单过滤
		batchMarkdown := filterCodeUnderstandingMarkdownByFiles(fullMarkdown, detail.FilePaths)
		if batchMarkdown != "" {
			batchAgentMarkdowns["code_understanding"] = batchMarkdown
		}
	}
	batchPromptCtx.AgentMarkdowns = batchAgentMarkdowns

	isLastBatch := detail.Index == plan.BatchCount
	userPrompt := engine.BuildBatchCollectionPrompt(&batchPromptCtx, detail.Index, plan.BatchCount, batchFileMaps, isLastBatch)

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

	result, err := e.llmService.ChatCompletionStructured(ctx, &task.ID, task.UsedModelID, "batch_collection", "", userPrompt, responseFormat)
	if err != nil {
		ctx.SaveOutputSnapshot(exec, map[string]interface{}{
			"batch_index": detail.Index,
			"error":       err.Error(),
			"prompt":      userPrompt,
		})
		ctx.MarkFailed(exec, err.Error())
		return nil, 0, 0, 0, "", err
	}

	// Refusal 检测
	if result.Response != nil && len(result.Response.Choices) > 0 && result.Response.Choices[0].Message.Refusal != "" {
		ctx.MarkFailed(exec, "模型拒绝回答")
		return nil, 0, 0, 0, "", fmt.Errorf("模型拒绝回答: %s", result.Response.Choices[0].Message.Refusal)
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
		return nil, 0, 0, 0, "", err
	}

	exec.InputTokens = result.InputTokens
	exec.OutputTokens = result.OutputTokens
	exec.ModelName = result.ModelName
	if exec.LLMModelID == nil {
		exec.LLMModelID = &result.ModelID
	}

	ctx.SaveOutputSnapshot(exec, map[string]interface{}{
		"batch_index":      detail.Index,
		"total_batches":    plan.BatchCount,
		"file_count":       detail.FileCount,
		"file_paths":       detail.FilePaths,
		"file_details":     fileDetails,
		"issue_count":      len(batchResult.Issues),
		"input_tokens":     result.InputTokens,
		"output_tokens":    result.OutputTokens,
		"model_name":       result.ModelName,
		"model_id":         result.ModelID,
		"prompt":           userPrompt,
		"review_result":    result.Content,
		"available_tokens": plan.AvailableTokens,
		"overhead_tokens":  plan.OverheadTokens,
	})

	// 【新增】保存实际开销到 execution 记录，并异步保存校准数据
	actualOH := result.InputTokens - calcDiffTokens(fileDetails)
	if actualOH < 0 {
		actualOH = 0
	}
	exec.ActualOverhead = actualOH
	model.DB.Model(exec).Update("actual_overhead", actualOH)
	go func() {
		saveOverheadCalibration(
			task.ID, exec.ID, exec.StageCode,
			len(promptCtx.Rules), plan.EstimatedOverhead,
			result.InputTokens, calcDiffTokens(fileDetails),
		)
	}()

	ctx.MarkSuccess(exec)

	return batchResult, result.ModelID, result.InputTokens, result.OutputTokens, result.ModelName, nil
}

// executeScoreArbitration 场景 C：汇总裁决模式
// ========== PostProcessExecutor ==========
type PostProcessExecutor struct{}

func (e *PostProcessExecutor) Code() string { return "post_process" }

func (e *PostProcessExecutor) Execute(ctx StageContext) error {
	task := ctx.Task()
	if task == nil {
		return nil
	}

	// 获取 review_arbitration 阶段生成的结构化结果
	result, ok := ctx.GetOutput("ai_review_result").(*llm.AIReviewResult)
	if !ok || result == nil {
		zap.L().Warn("Pipeline: 未找到 ai_review_result，跳过 post_process")
		return nil
	}

	score := result.TotalScore

	// 【改造】使用 AssembleFullMarkdownReport 组装完整报告（含扩展区域）
	promptCtx := getPromptContext(ctx)
	var report string
	if promptCtx != nil {
		// 从 dependency_scan 阶段输出获取漏洞结果（prompt_ctx 中的 DependencyVulns 可能为空）
		// 只取本次变更相关的漏洞，避免非变更依赖的漏洞出现在最终报告中
		var depVulns []engine.DependencyVuln
		if v, ok := ctx.GetOutput("dependency_vulns").([]engine.DependencyVuln); ok {
			for _, dep := range v {
				if dep.IsRelatedToChange {
					depVulns = append(depVulns, dep)
				}
			}
		}
		var err error
		report, err = engine.AssembleFullMarkdownReport(result, promptCtx.GitLabCommentTemplate, depVulns)
		if err != nil {
			zap.L().Warn("Pipeline: 组装完整报告失败，回退到简易报告", zap.Error(err))
			report = fmt.Sprintf("## 🤖 AI 代码评审报告\n\n**综合评分：%d/100**\n\n%s",
				result.TotalScore, result.Summary)
		}
		// 追加智能体配置段落
		if section := promptCtx.AgentStatusSection(); section != "" {
			report += section
		}
	} else {
		report = fmt.Sprintf("## 🤖 AI 代码评审报告\n\n**综合评分：%d/100**\n\n%s",
			result.TotalScore, result.Summary)
	}

	// 【P0】行号校正：在入库前校正 issues 的行号
	rawDiff := extractRawDiffFromContext(ctx)
	if rawDiff != "" {
		cfg := config.Load().DiffLineMap
		result.Issues = engine.ApplyLineCorrections(result.Issues, rawDiff, &cfg, task.ProjectID)
	}

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

	// 更新 AIResponse（Markdown 报告）
	model.DB.Model(&model.Task{}).Where("id = ?", task.ID).Update("ai_response", report)

	// 将报告写入 Pipeline 共享输出，供 engine markTaskSuccess 读取（防止被覆盖为空）
	ctx.SetOutput("final_report", report)
	ctx.SetOutput("score", score)

	ctx.SaveInputSnapshot(&model.TaskPipelineExecution{ID: ctx.ExecutionID()}, map[string]interface{}{
		"task_id":      task.ID,
		"final_report": report,
		"score":        score,
	})

	// 6. 评论发布状态：仅表示 "具备发布条件"（报告非空且关联 MR 信息完整）
	// 真正的 GitLab 发布是在 Pipeline 成功后由 executePipelineReviewTask 异步执行 postReviewComment 完成的，
	// 此处 snapshot 无法反映实际发布结果（网络/token/MR状态等均可能导致失败）。
	canPostComment := report != "" && task.MRMergeID > 0 && task.ProjectID > 0
	ctx.SaveOutputSnapshot(&model.TaskPipelineExecution{ID: ctx.ExecutionID()}, map[string]interface{}{
		"status":           "completed",
		"task_id":          task.ID,
		"final_report":     report,
		"score":            score,
		"comment_posted":   false,          // 避免前端误认为"已发布"；实际发布结果见 task.mr_thread_id 或日志
		"comment_eligible": canPostComment, // 是否具备发布条件
	})
	return nil
}

// ========== 辅助函数 ==========

// getPromptContext 从 StageContext 获取 PromptContext
func getPromptContext(ctx StageContext) *engine.PromptContext {
	if v, ok := ctx.GetInput("prompt_context").(*engine.PromptContext); ok {
		// 注入代码理解器报告（如果存在）
		if r, ok := ctx.GetInput("code_understanding_report").(string); ok && r != "" {
			v.CodeUnderstandingReport = r
		}
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

// calcDiffTokens 从 fileDetails 计算 diff 内容的 token 估算值
func calcDiffTokens(fileDetails []map[string]interface{}) int {
	tokens := 0
	for _, fd := range fileDetails {
		if diff, ok := fd["diff"].(string); ok && diff != "" {
			tokens += len(diff) / 4
		}
	}
	return tokens
}

// saveOverheadCalibration 保存 Token 开销校准记录（异步执行）
// 用于后续任务的开销估算校准，使预算计算更加精准。
func saveOverheadCalibration(
	taskID uint,
	execID uint,
	stageCode string,
	ruleCount int,
	estimatedOverhead int,
	actualInputTokens int,
	diffTokens int,
) {
	actualOverhead := actualInputTokens - diffTokens
	if actualOverhead < 0 {
		actualOverhead = 0
	}

	calibration := model.OverheadCalibration{
		TaskID:            taskID,
		ExecutionID:       execID,
		StageCode:         stageCode,
		RuleCount:         ruleCount,
		EstimatedOverhead: estimatedOverhead,
		ActualOverhead:    actualOverhead,
		TotalInputTokens:  actualInputTokens,
		DiffTokens:        diffTokens,
	}

	if model.DB == nil {
		return
	}
	if err := model.DB.Create(&calibration).Error; err != nil {
		zap.L().Warn("保存开销校准记录失败", zap.Error(err))
	}
}

// extractRawDiffFromContext 从 Pipeline Context 中提取原始 diff 文本
// 【P0】优先读取 "raw_diff_full"（未截断的完整 unified diff）
// 回退到 "diff_files_raw"（可能被 SmartSplitIntoBatches 截断过）
// 最后回退到 "diff_files"
func extractRawDiffFromContext(ctx StageContext) string {
	// 1. 优先：原始完整 unified diff（在 task.go executePipelineReviewTask 中预置）
	if v := ctx.GetInput("raw_diff_full"); v != nil {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}

	// 2. 回退：从 diff_files_raw / diff_files 重建（这些可能已被截断）
	for _, key := range []string{"diff_files_raw", "diff_files"} {
		val := ctx.GetInput(key)
		if val == nil {
			continue
		}
		files, ok := val.([]map[string]interface{})
		if !ok {
			continue
		}
		var parts []string
		for _, f := range files {
			if diffText, ok := f["diff"].(string); ok && diffText != "" {
				parts = append(parts, diffText)
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, "\n")
		}
	}
	return ""
}
