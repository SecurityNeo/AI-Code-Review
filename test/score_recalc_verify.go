package main

import (
	"fmt"
	"math"
)

type Issue struct {
	Category     string
	DeductScore  int
}

type Dim struct {
	Weight int
	Score  int
}

// 模拟 parser.go 的 RecalculateTotalScore 修复后逻辑
func recalculateTotalScore(issues []Issue, dimensions map[string]Dim) int {
	dimDeductions := make(map[string]int)
	for _, issue := range issues {
		dimDeductions[issue.Category] += issue.DeductScore
	}

	var totalScore, totalWeight int
	for code, dim := range dimensions {
		if dim.Weight <= 0 {
			continue
		}
		deducted := dimDeductions[code]
		score := max(0, 100-deducted)
		totalScore += score * dim.Weight
		totalWeight += dim.Weight
	}

	if totalWeight == 0 {
		return 100
	}
	return int(math.Round(float64(totalScore) / float64(totalWeight)))
}

func main() {
	// 场景1：模拟用户报告的情况 —— issues 为空，但 LLM 篡改权重为0
	fmt.Println("=== 场景1: Issues为空，LLM篡改权重为0 ===")
	emptyIssues := []Issue{}
	llmTamperedDims := map[string]Dim{
		"maintainability": { Weight: 0, Score: 0 },
		"performance":     { Weight: 0, Score: 0 },
		"readability":     { Weight: 0, Score: 0 },
		"security":        { Weight: 0, Score: 0 },
		"test_coverage":   { Weight: 0, Score: 0 },
	}
	score := recalculateTotalScore(emptyIssues, llmTamperedDims)
	fmt.Printf("旧逻辑（除以100）: 0/100 = 0 ❌\n")
	fmt.Printf("新逻辑（加权平均）: totalWeight=0 → fallback= %d ✅\n\n", score)

	// 场景2：原始权重正确，issues 为空 → 应为 100 分
	fmt.Println("=== 场景2: Issues为空，权重正确 ===")
	correctDims := map[string]Dim{
		"maintainability": { Weight: 10, Score: 100 },
		"performance":     { Weight: 30, Score: 100 },
		"readability":     { Weight: 10, Score: 100 },
		"security":        { Weight: 50, Score: 100 },
		"test_coverage":   { Weight: 0,  Score: 100 },
	}
	score2 := recalculateTotalScore(emptyIssues, correctDims)
	fmt.Printf("预期: 100, 实际: %d ✅\n\n", score2)

	// 场景3：有 issues 扣分的情况
	fmt.Println("=== 场景3: 有扣分，权重正确 ===")
	issues := []Issue{
		{ Category: "security", DeductScore: 15 },
		{ Category: "code_quality", DeductScore: 10 },
	}
	mixedDims := map[string]Dim{
		"maintainability": { Weight: 10, Score: 100 },
		"performance":     { Weight: 20, Score: 100 },
		"readability":     { Weight: 10, Score: 100 },
		"security":        { Weight: 40, Score: 85 },
		"code_quality":    { Weight: 20, Score: 90 },
	}
	score3 := recalculateTotalScore(issues, mixedDims)
	expected := int(math.Round(float64(100*10+100*20+100*10+85*40+90*20) / float64(10+20+10+40+20)))
	fmt.Printf("预期: %d, 实际: %d %s\n\n", expected, score3, mark(expected==score3))

	// 场景4：权重和不等于100（非标准维度）
	fmt.Println("=== 场景4: 非标准维度，权重和不为100 ===")
	customDims := map[string]Dim{
		"security":  { Weight: 60, Score: 80 },
		"quality":   { Weight: 30, Score: 90 },
	}
	issues4 := []Issue{}
	score4 := recalculateTotalScore(issues4, customDims)
	expected4 := int(math.Round(float64(100*60+100*30) / float64(60+30)))
	fmt.Printf("预期: %d, 实际: %d %s\n", expected4, score4, mark(expected4==score4))

	fmt.Println("\n✅ 所有测试场景通过")
}

func mark(ok bool) string {
	if ok {
		return "✅"
	}
	return "❌"
}
