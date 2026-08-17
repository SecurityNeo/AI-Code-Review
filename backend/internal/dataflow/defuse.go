package dataflow

import (
	"strings"
)

// DefUseChain 单函数内的 def-use 链
type DefUseChain struct {
	Defs      []DefPoint
	Uses      []UsePoint
	ParamFlow []ParamFlowEdge
}

// DefPoint 变量定义点
type DefPoint struct {
	VarName  string
	Line     int
	Kind     string // assign | param | decl
	FuncName string
}

// UsePoint 变量使用点
type UsePoint struct {
	VarName  string
	Line     int
	Kind     string // call_arg | condition | return
	FuncName string
}

// ParamFlowEdge 参数流向边
type ParamFlowEdge struct {
	FromVar string
	ToVar   string
	Line    int
}

// DefUseAnalyzer def-use 分析器
type DefUseAnalyzer struct {
}

// NewDefUseAnalyzer 创建分析器
func NewDefUseAnalyzer() *DefUseAnalyzer {
	return &DefUseAnalyzer{}
}

// AnalyzeFunction 分析函数体的 def-use 链
func (a *DefUseAnalyzer) AnalyzeFunction(funcName, bodySnippet string) *DefUseChain {
	chain := &DefUseChain{}
	lines := strings.Split(bodySnippet, "\n")

	// 简化的行级 def-use 分析
	for i, line := range lines {
		lineNum := i + 1
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "//") {
			continue
		}

		// 检测赋值定义
		if idx := strings.Index(trimmed, "="); idx > 0 {
			left := strings.TrimSpace(trimmed[:idx])
			// 跳过 ==, !=, <=, >=, :=
			if strings.HasSuffix(left, ":") {
				left = strings.TrimSuffix(left, ":")
			}
			if !strings.Contains(left, "==") && !strings.Contains(left, "!=") &&
				!strings.Contains(left, "<=") && !strings.Contains(left, ">=") {
				for _, varName := range extractVarNames(left) {
					if varName != "" {
						chain.Defs = append(chain.Defs, DefPoint{
							VarName:  varName,
							Line:     lineNum,
							Kind:     "assign",
							FuncName: funcName,
						})
					}
				}
			}
		}

		// 检测变量使用（简化的词法分析）
		for _, varName := range interestingVars {
			if strings.Contains(trimmed, varName) && !strings.Contains(trimmed, "var ") {
				// 检查是否不是定义
				if !strings.HasPrefix(trimmed, varName+" ") && !strings.HasPrefix(trimmed, varName+"=") {
					chain.Uses = append(chain.Uses, UsePoint{
						VarName:  varName,
						Line:     lineNum,
						Kind:     "reference",
						FuncName: funcName,
					})
				}
			}
		}
	}

	return chain
}

// interestingVars 常见需追踪的变量名
var interestingVars = []string{
	"req", "request", "resp", "response", "data", "input", "userInput",
	"query", "cmd", "command", "sql", "filePath", "path", "url",
}

// extractVarNames 从左值中提取变量名（简化版）
func extractVarNames(left string) []string {
	// 处理逗号分隔的多重赋值
	var names []string
	for _, part := range strings.Split(left, ",") {
		part = strings.TrimSpace(part)
		if part == "" || part == "_" {
			continue
		}
		// 基本过滤：只保留简单的标识符
		if isSimpleIdentifier(part) {
			names = append(names, part)
		}
	}
	return names
}

func isSimpleIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			continue
		}
		return false
	}
	return true
}
