package graph

import (
	"fmt"
	"strings"
)

// DuplicatedGroup 重复函数组
type DuplicatedGroup struct {
	Signature   string          `json:"signature"`
	Count       int             `json:"count"`
	Occurrences []Occurrence    `json:"occurrences"`
}

// Occurrence 重复出现位置
type Occurrence struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Package  string `json:"package"`
	Receiver string `json:"receiver,omitempty"`
}

// APIHealthItem API健康度评估项
type APIHealthItem struct {
	Name       string   `json:"name"`
	File       string   `json:"file"`
	Line       int      `json:"line"`
	Package    string   `json:"package"`
	Issues     []string `json:"issues"`     // 具体问题列表
	Score      int      `json:"score"`      // 0-100，越高越健康
	HasDoc     bool     `json:"has_doc"`
	ParamCount int      `json:"param_count"`
}

// DetectDuplicatedFunctions 基于签名指纹检测重复函数
func DetectDuplicatedFunctions(g *MemorySymbolGraph) []DuplicatedGroup {
	// 1. 收集所有函数节点的签名指纹
	// 指纹 = 函数名 + 参数类型列表（忽略接收者，因为不同接收者可能逻辑重复）
	type fingerprint struct {
		name   string
		params string // 参数类型列表，如 "string,int"
		returns string
	}
	
	groups := make(map[string][]*MemorySymbolNode)
	
	for _, node := range g.AllNodes() {
		if node.Type != NodeFunc {
			continue
		}
		
		// 从 Properties 获取参数和返回类型（如果存储了）
		params := getStringProp(node.Properties, "params")
		returns := getStringProp(node.Properties, "returns")
		if params == "" {
			// 备选：从 Signature 中提取参数部分
			params = extractParamsFromSignature(node.Signature)
			returns = extractReturnsFromSignature(node.Signature)
		}
		
		// 构建指纹
		fp := fmt.Sprintf("%s(%s)->%s", node.Name, params, returns)
		groups[fp] = append(groups[fp], node)
	}
	
	// 2. 找出重复组（出现次数 > 1）
	var result []DuplicatedGroup
	for fp, nodes := range groups {
		if len(nodes) <= 1 {
			continue
		}
		
		// 排除同一个文件内的重复（通常是有意为之的 helper）
		fileSet := make(map[string]bool)
		for _, n := range nodes {
			fileSet[n.File] = true
		}
		if len(fileSet) <= 1 {
			continue // 只在同一个文件内重复的，跳过
		}
		
		occurrences := make([]Occurrence, 0, len(nodes))
		for _, n := range nodes {
			occurrences = append(occurrences, Occurrence{
				File:     n.File,
				Line:     n.Location.LineStart,
				Package:  n.Package,
				Receiver: getStringProp(n.Properties, "receiver"),
			})
		}
		
		result = append(result, DuplicatedGroup{
			Signature:   extractSignatureFromFingerprint(fp),
			Count:       len(nodes),
			Occurrences: occurrences,
		})
	}
	
	return result
}

// EvaluateAPIHealth 评估导出 API 的健康度
func EvaluateAPIHealth(g *MemorySymbolGraph) []APIHealthItem {
	var result []APIHealthItem
	
	for _, node := range g.AllNodes() {
		if node.Type != NodeFunc || !node.IsExported {
			continue
		}
		
		item := APIHealthItem{
			Name:    node.Name,
			File:    node.File,
			Line:    node.Location.LineStart,
			Package: node.Package,
			Score:   100,
		}
		
		// 检查 1：是否有文档注释
		docComment := getStringProp(node.Properties, "doc_comment")
		item.HasDoc = docComment != "" && len(docComment) > 10
		if !item.HasDoc {
			item.Issues = append(item.Issues, "缺少文档注释")
			item.Score -= 20
		}
		
		// 检查 2：参数数量是否合理（>5 个参数扣分）
		params := getStringProp(node.Properties, "params")
		if params == "" {
			params = extractParamsFromSignature(node.Signature)
		}
		paramCount := countParams(params)
		item.ParamCount = paramCount
		if paramCount > 5 {
			item.Issues = append(item.Issues, fmt.Sprintf("参数过多(%d个)", paramCount))
			item.Score -= 15
		}
		
		// 检查 3：是否返回 interface{}（Go 语言中不够类型安全）
		returns := getStringProp(node.Properties, "returns")
		if returns == "" {
			returns = extractReturnsFromSignature(node.Signature)
		}
		if strings.Contains(returns, "interface{}") {
			item.Issues = append(item.Issues, "返回 interface{}，类型安全性差")
			item.Score -= 10
		}
		
		// 检查 4：签名复杂度（长签名不易使用）
		sigLen := len(node.Signature)
		if sigLen > 100 {
			item.Issues = append(item.Issues, "签名过长")
			item.Score -= 5
		}
		
		// 检查 5：圈复杂度（如果 >15 视为高风险）
		complexity := getIntProp(node.Properties, "complexity")
		if complexity > 15 {
			item.Issues = append(item.Issues, fmt.Sprintf("圈复杂度过高(%d)", complexity))
			item.Score -= 20
		} else if complexity > 10 {
			item.Issues = append(item.Issues, fmt.Sprintf("圈复杂度偏高(%d)", complexity))
			item.Score -= 10
		}
		
		// 确保分数不超出范围
		if item.Score < 0 {
			item.Score = 0
		}
		if item.Score > 100 {
			item.Score = 100
		}
		
		result = append(result, item)
	}
	
	return result
}

// --- 辅助函数 ---

func extractParamsFromSignature(sig string) string {
	// 简化提取：从 (...) 中提取参数类型
	// 例如 "func Login(username string, password string) error" → "string,string"
	start := strings.Index(sig, "(")
	end := strings.Index(sig, ")")
	if start == -1 || end == -1 || end <= start {
		return ""
	}
	
	paramsStr := sig[start+1 : end]
	if paramsStr == "" {
		return ""
	}
	
	// 解析每个参数，提取类型
	var types []string
	for _, param := range strings.Split(paramsStr, ",") {
		param = strings.TrimSpace(param)
		// 处理 "name type" 格式
		parts := strings.Fields(param)
		if len(parts) >= 2 {
			types = append(types, parts[len(parts)-1])
		}
	}
	
	return strings.Join(types, ",")
}

func extractReturnsFromSignature(sig string) string {
	// 从 ) 后面提取返回类型
	end := strings.Index(sig, ")")
	if end == -1 || end >= len(sig)-1 {
		return ""
	}
	
	returns := strings.TrimSpace(sig[end+1:])
	// 去掉开头的 "func(...)" 前缀（如果存在）
	if strings.HasPrefix(returns, "func") {
		return ""
	}
	return returns
}

func extractSignatureFromFingerprint(fp string) string {
	// 从指纹恢复可读签名
	return fp
}

func countParams(paramsStr string) int {
	paramsStr = strings.TrimSpace(paramsStr)
	if paramsStr == "" {
		return 0
	}
	return len(strings.Split(paramsStr, ","))
}

func getIntProp(props map[string]interface{}, key string) int {
	if props == nil {
		return 0
	}
	if v, ok := props[key]; ok {
		switch n := v.(type) {
		case int:
			return n
		case int64:
			return int(n)
		case float64:
			return int(n)
		}
	}
	return 0
}
