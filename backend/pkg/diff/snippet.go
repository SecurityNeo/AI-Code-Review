package diff

import (
	"regexp"
	"strings"
)

// lineAnnotationRe 行首的行号注解，形如：
//
//	[new94|old74]（上下文）、[new96|old-]（新增）、[new-|old76]（删除）
//
// 行首允许有空白，注解后允许一个分隔空格/制表符。
var lineAnnotationRe = regexp.MustCompile(`^[ \t]*\[new([^\]\r\n|]*)\|old([^\]\r\n]*)\][ \t]?`)

// StripLineAnnotations 去掉 code_snippet 中的 [newN|oldM] 行号注解，
// 并还原被模型一并抄入的 diff 前缀（上下文空格 / 新增 + / 删除 -）。
// 用于把带行号注入的片段还原为"纯代码片段"。
func StripLineAnnotations(snippet string) string {
	// 快速路径：绝大多数 snippet 不含注解
	if snippet == "" || !strings.Contains(snippet, "[new") {
		return snippet
	}
	lines := strings.Split(snippet, "\n")
	for i, ln := range lines {
		lines[i] = stripLineAnnotation(ln)
	}
	return strings.Join(lines, "\n")
}

// stripLineAnnotation 处理单行：去掉行首注解，并按注解类型还原 diff 前缀。
func stripLineAnnotation(line string) string {
	m := lineAnnotationRe.FindStringSubmatchIndex(line)
	if m == nil {
		return line
	}
	newTok := line[m[2]:m[3]]
	oldTok := line[m[4]:m[5]]
	rest := line[m[1]:]

	// 依据注解推断该行在原始 diff 中的前缀字符
	expect := byte(' ') // 默认上下文
	switch {
	case strings.TrimSpace(newTok) == "-":
		expect = '-' // [new-|oldM] → 删除行
	case strings.TrimSpace(oldTok) == "-":
		expect = '+' // [newN|old-] → 新增行
	}

	// 仅当剩余内容确以期望前缀开头时才剥离，避免误删代码自身的 +/- 字符
	if len(rest) > 0 && rest[0] == expect {
		rest = rest[1:]
	}
	return rest
}
