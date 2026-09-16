package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/ai-optimizer/backend/pkg/llm"
)

// ToolExecutorMode 工具执行器模式常量
const (
	ToolModeFull      = "full"
	ToolModeDegraded  = "degraded"
	ToolModeAstOnly   = "ast_only"
	ToolModeSkipped   = "skipped"
)

// ToolExecutor 工具执行器
type ToolExecutor struct {
	report   *CodeUnderstandingReport
	cache    map[string]interface{}
	mode     string // "full" | "degraded" | "ast_only" | "skipped"
	batchDiffs map[string]string // file_path -> diff content (for get_diff)
}

// NewToolExecutor 创建工具执行器
func NewToolExecutor(report *CodeUnderstandingReport, batchDiffs map[string]string) *ToolExecutor {
	if report == nil {
		return &ToolExecutor{
			mode:       ToolModeSkipped,
			cache:      make(map[string]interface{}),
			batchDiffs: batchDiffs,
		}
	}
	return &ToolExecutor{
		report:     report,
		cache:      make(map[string]interface{}),
		mode:       report.Mode,
		batchDiffs: batchDiffs,
	}
}

// Execute 执行工具调用
func (te *ToolExecutor) Execute(ctx context.Context, call llm.ToolCall) (string, error) {
	cacheKey := fmt.Sprintf("%s:%s", call.Function.Name, call.Function.Arguments)
	if cached, ok := te.cache[cacheKey]; ok {
		return te.serialize(cached), nil
	}

	var args map[string]interface{}
	if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
		return fmt.Sprintf("参数解析错误: %v", err), nil
	}

	var result interface{}
	switch call.Function.Name {
	case "get_file_ast_summary":
		result = te.getFileASTSummary(args)
	case "get_function_callers":
		result = te.getFunctionCallers(args)
	case "get_function_callees":
		result = te.getFunctionCallees(args)
	case "get_data_flow_path":
		result = te.getDataFlowPath(args)
	case "get_api_endpoints":
		result = te.getAPIEndpoints(args)
	case "get_breaking_changes":
		result = te.getBreakingChanges(args)
	case "get_import_dependencies":
		result = te.getImportDependencies(args)
	case "get_diff":
		result = te.getDiff(args)
	default:
		return fmt.Sprintf("未知工具: %s", call.Function.Name), nil
	}

	te.cache[cacheKey] = result
	return te.serialize(result), nil
}

func (te *ToolExecutor) serialize(v interface{}) string {
	// 【优化】字符串类型直接返回，不经过 JSON 编码（避免引号转义浪费 token）
	if s, ok := v.(string); ok {
		const maxResultLen = 12000
		if len(s) > maxResultLen {
			return s[:maxResultLen] + "\n... [结果已截断，原始长度 " + fmt.Sprintf("%d", len(s)) + " 字符]"
		}
		return s
	}
	b, _ := json.MarshalIndent(v, "", "  ")
	result := string(b)
	// 防止工具返回结果过大，超出对话 Token 限制
	const maxResultLen = 10000
	if len(result) > maxResultLen {
		return result[:maxResultLen] + "\n... [结果已截断，原始长度 " + fmt.Sprintf("%d", len(result)) + " 字符]"
	}
	return result
}

// getFileASTSummary 获取文件的 AST 摘要
func (te *ToolExecutor) getFileASTSummary(args map[string]interface{}) interface{} {
	filePath, _ := args["file_path"].(string)
	if te.report == nil || te.report.ASTData == nil {
		return map[string]interface{}{
			"file": filePath,
			"note": "无 AST 数据",
		}
	}
	for _, fr := range te.report.ASTData.FileResults {
		if fr.Path == filePath {
			return map[string]interface{}{
				"file":       filePath,
				"functions":  len(fr.FunctionDetails),
				"types":      len(fr.TypeDetails),
				"imports":    len(fr.ImportList),
				"complexity": fr.ComplexityScore,
				"function_list": extractNames(fr.FunctionDetails, "name"),
				"type_list":     extractNames(fr.TypeDetails, "name"),
			}
		}
	}
	return map[string]interface{}{
		"file": filePath,
		"note": "文件未找到",
	}
}

// getFunctionCallers 获取函数的调用者
func (te *ToolExecutor) getFunctionCallers(args map[string]interface{}) interface{} {
	funcName, _ := args["function_name"].(string)
	
	if te.mode == ToolModeAstOnly || te.mode == ToolModeSkipped || te.report == nil {
		return te.getFunctionCallersFromAST(funcName)
	}
	
	// Full Mode: 优先使用 ReverseCallIndex
	if callers, ok := te.report.ReverseCallIndex[funcName]; ok && len(callers) > 0 {
		return map[string]interface{}{
			"function": funcName,
			"callers":  callers,
			"mode":     "full",
		}
	}
	
	// fallback 到 AST
	return te.getFunctionCallersFromAST(funcName)
}

// getFunctionCallersFromAST 从 AST 中查找调用者（AST-Only 降级）
func (te *ToolExecutor) getFunctionCallersFromAST(funcName string) interface{} {
	// 【优化】按文件分组，减少重复 file 路径 token
	fileCallers := make(map[string][]map[string]interface{})
	if te.report != nil && te.report.ASTData != nil {
		pattern := `[^a-zA-Z0-9_]` + regexp.QuoteMeta(funcName) + `\(`
		re, err := regexp.Compile(pattern)
		if err == nil {
			for _, fr := range te.report.ASTData.FileResults {
				var callers []map[string]interface{}
				for _, fd := range fr.FunctionDetails {
					body, _ := fd["body"].(string)
					if re.MatchString(body) {
						callers = append(callers, map[string]interface{}{
							"name": fd["name"],
							"line": fd["line"],
						})
					}
				}
				if len(callers) > 0 {
					fileCallers[fr.Path] = callers
				}
			}
		}
	}
	return map[string]interface{}{
		"function": funcName,
		"by_file":  fileCallers,
	}
}

// getFunctionCallees 获取函数的调用目标
func (te *ToolExecutor) getFunctionCallees(args map[string]interface{}) interface{} {
	funcName, _ := args["function_name"].(string)
	
	if te.mode == ToolModeAstOnly || te.mode == ToolModeSkipped || te.report == nil {
		return map[string]interface{}{
			"function": funcName,
			"callees":  []interface{}{},
			"note":     "AST-Only 模式：无法获取跨文件调用目标",
		}
	}
	
	// Full Mode: 从 GraphData Relations 中查找
	var callees []string
	if te.report.GraphData != nil {
		for _, rel := range te.report.GraphData.Relations {
			if src, ok := rel["source"].(string); ok && src == funcName {
				if tgt, ok := rel["target"].(string); ok {
					callees = append(callees, tgt)
				}
			}
		}
	}
	return map[string]interface{}{
		"function": funcName,
		"callees":  callees,
	}
}

// getDataFlowPath 获取数据流路径
func (te *ToolExecutor) getDataFlowPath(args map[string]interface{}) interface{} {
	sourceFunc, _ := args["source_func"].(string)
	sinkFunc, _ := args["sink_func"].(string)
	
	if te.mode == ToolModeAstOnly || te.mode == ToolModeSkipped || te.report == nil {
		// AST-Only: 降级为函数名模式推断
		return map[string]interface{}{
			"source":     sourceFunc,
			"sink":       sinkFunc,
			"paths":      []interface{}{},
			"note":       "AST-Only 模式：无法检测数据流传播路径",
			"suggestion": "手动检查 " + sourceFunc + " 是否直接或间接调用 " + sinkFunc,
		}
	}
	
	// Full Mode: 从 TaintFlows 中查找
	for _, tf := range te.report.TaintFlows {
		if tf.SourceFunc == sourceFunc && tf.SinkFunc == sinkFunc {
			// 【优化】path 压缩为二维数组，减少 key 重复 token
			var path [][]interface{}
			for _, s := range tf.Path {
				path = append(path, []interface{}{s.Function, s.File, s.Line})
			}
			return map[string]interface{}{
				"source":       sourceFunc,
				"sink":         sinkFunc,
				"category":     tf.Category,
				"risk":         tf.RiskLevel,
				"is_sanitized": tf.IsSanitized,
				"path":         path,
			}
		}
	}
	return map[string]interface{}{
		"source": sourceFunc,
		"sink":   sinkFunc,
		"paths":  []interface{}{},
		"note":   "未找到匹配的数据流路径",
	}
}

// getAPIEndpoints 获取 API 端点
func (te *ToolExecutor) getAPIEndpoints(args map[string]interface{}) interface{} {
	filePath, _ := args["file_path"].(string)
	
	if te.mode == ToolModeAstOnly || te.mode == ToolModeSkipped || te.report == nil {
		return map[string]interface{}{
			"file":      filePath,
			"endpoints": []interface{}{},
			"note":      "AST-Only 模式：无法检测框架适配器和路由绑定",
		}
	}
	
	var endpoints []map[string]interface{}
	var framework string
	for _, ep := range te.report.Endpoints {
		if ep.File == filePath {
			if framework == "" {
				framework = ep.Framework
			}
			endpoints = append(endpoints, map[string]interface{}{
				"path":    ep.Path,
				"method":  ep.Method,
				"handler": ep.Handler,
				"auth":    ep.AuthRequired,
			})
		}
	}
	result := map[string]interface{}{
		"file":      filePath,
		"endpoints": endpoints,
	}
	if framework != "" {
		result["framework"] = framework
	}
	return result
}

// getBreakingChanges 获取破坏性变更
func (te *ToolExecutor) getBreakingChanges(args map[string]interface{}) interface{} {
	filePath, _ := args["file_path"].(string)
	
	var changes []map[string]interface{}
	if te.report != nil {
		for _, bc := range te.report.BreakingChanges {
			if bc.File == filePath {
				changes = append(changes, map[string]interface{}{
					"description": bc.Description,
					"line":        bc.Line,
				})
			}
		}
	}
	
	// AST-Only 模式下，尝试检测函数签名变更
	if len(changes) == 0 && te.report != nil && te.report.ASTData != nil {
		for _, fr := range te.report.ASTData.FileResults {
			if fr.Path == filePath {
				for _, fd := range fr.FunctionDetails {
					if changed, ok := fd["signature_changed"].(bool); ok && changed {
						changes = append(changes, map[string]interface{}{
							"description": "函数签名变更",
							"function":    fd["name"],
							"line":        fd["line"],
							"source":      "ast",
						})
					}
				}
			}
		}
	}
	
	return map[string]interface{}{
		"file":    filePath,
		"changes": changes,
	}
}

// getImportDependencies 获取导入依赖（分离标准库和第三方/内部库）
func (te *ToolExecutor) getImportDependencies(args map[string]interface{}) interface{} {
	filePath, _ := args["file_path"].(string)
	
	if te.report != nil && te.report.ASTData != nil {
		for _, fr := range te.report.ASTData.FileResults {
			if fr.Path == filePath {
				// 分离标准库（减少 token：标准库通常不是评审重点）
				var stdlib []string
				var thirdParty []string
				for _, imp := range fr.ImportList {
					path, _ := imp["path"].(string)
					if path == "" {
						continue
					}
					if isStd, _ := imp["is_stdlib"].(bool); isStd {
						stdlib = append(stdlib, path)
					} else {
						thirdParty = append(thirdParty, path)
					}
				}
				result := map[string]interface{}{
					"file":         filePath,
					"stdlib_count": len(stdlib),
					"third_party":  thirdParty,
				}
				if len(stdlib) > 0 && len(stdlib) <= 5 {
					result["stdlib"] = stdlib
				} else if len(stdlib) > 5 {
					result["stdlib_sample"] = stdlib[:5]
				}
				return result
			}
		}
	}
	
	// fallback: 从 ImportDependencyNet 查找
	if te.report != nil {
		for _, entry := range te.report.ImportDependencyNet {
			if fp, ok := entry["file"].(string); ok && fp == filePath {
				return entry
			}
		}
	}
	
	return map[string]interface{}{
		"file":  filePath,
		"note":  "文件未找到",
		"count": 0,
	}
}

// getDiff 获取文件 diff 内容（自动截断，避免超出 Token 限制）
// 【优化】直接返回原始 diff 字符串，不包装 JSON，避免 JSON 编码导致 token 暴增
func (te *ToolExecutor) getDiff(args map[string]interface{}) interface{} {
	filePath, _ := args["file_path"].(string)
	const maxDiffChars = 12000 // 最多返回 12K 字符的 diff
	
	if diff, ok := te.batchDiffs[filePath]; ok {
		if len(diff) > maxDiffChars {
			return diff[:maxDiffChars] + "\n... [diff 已截断至 " + fmt.Sprintf("%d", maxDiffChars) + " 字符，原始长度 " + fmt.Sprintf("%d", len(diff)) + " 字符]"
		}
		return diff
	}
	return fmt.Sprintf("文件 %s 不在当前批次中，无法提供 diff", filePath)
}

// extractNames 从 map slice 中提取指定 key 的值列表
func extractNames(items []map[string]interface{}, key string) []string {
	result := make([]string, 0, len(items))
	for _, item := range items {
		if v, ok := item[key].(string); ok && v != "" {
			result = append(result, v)
		}
	}
	return result
}
