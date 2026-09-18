package depaudit

import (
	"encoding/json"
	"strings"

	"github.com/ai-optimizer/backend/internal/model"
)

// 许可证明细状态
const (
	LicenseCompliant    = "compliant"
	LicenseNoncompliant = "non_compliant"
	LicenseReview       = "review"
	LicenseUnknown      = "unknown"
	LicenseNA           = "na"
)

// 合规模式
const (
	ModeReportOnly = "report_only"
	ModeEnforced   = "enforced"
)

// 默认动作
const (
	ActionAllow  = "allow"
	ActionReview = "review"
)

// NOASSERTION SPDX 表示缺失
const noAssertion = "NOASSERTION"

// NormalizeSPDX 归一化许可证原始值
// 返回 (spdx表达式, 原始值)
func NormalizeSPDX(raw string) (string, string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return noAssertion, ""
	}
	lower := strings.ToLower(raw)
	if lower == "non-standard" || lower == "non_standard" {
		return "LicenseRef-non-standard", raw
	}
	return raw, raw
}

// JoinLicenses 将 deps.dev 返回的 licenses 数组拼接为表达式。
// 关系的确定性未知，采用宽松语义（任一满足）拼接为 OR；无数据返回 NOASSERTION。
func JoinLicenses(list []string) string {
	var cleaned []string
	for _, l := range list {
		l = strings.TrimSpace(l)
		if l != "" {
			cleaned = append(cleaned, l)
		}
	}
	if len(cleaned) == 0 {
		return noAssertion
	}
	return strings.Join(cleaned, " OR ")
}

// parseList 解析配置中的 JSON 数组字符串（兼容逗号分隔）
func parseList(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	var arr []string
	if strings.HasPrefix(s, "[") {
		if err := json.Unmarshal([]byte(s), &arr); err == nil {
			return normalizeIDs(arr)
		}
	}
	return normalizeIDs(strings.Split(s, ","))
}

func normalizeIDs(in []string) []string {
	var out []string
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v != "" {
			out = append(out, strings.ToLower(v))
		}
	}
	return out
}

func containsID(list []string, id string) bool {
	id = strings.ToLower(strings.TrimSpace(id))
	for _, v := range list {
		if v == id {
			return true
		}
	}
	return false
}

// EvaluateLicense 依据配置判定单个 SPDX 表达式。
// report_only 模式返回 LicenseNA；enforced 模式按宽松规则判定。
func EvaluateLicense(expr string, cfg *model.DependencyAuditConfig) (string, string) {
	expr = strings.TrimSpace(expr)
	if cfg == nil || cfg.LicenseComplianceMode != ModeEnforced {
		return LicenseNA, ""
	}
	if expr == "" || strings.EqualFold(expr, noAssertion) || strings.HasPrefix(expr, "LicenseRef-") {
		return LicenseReview, "no SPDX identifier"
	}

	allowed := parseList(cfg.AllowedLicenses)
	forbidden := parseList(cfg.ForbiddenLicenses)
	review := parseList(cfg.ReviewRequiredLicenses)
	defaultAction := cfg.DefaultLicenseAction
	if defaultAction == "" {
		defaultAction = ActionReview
	}

	alts := splitTopLevel(expr, " OR ")
	anyAllowed := false
	allForbidden := true
	var reasons []string

	for _, alt := range alts {
		ids := splitTopLevel(alt, " AND ")
		if len(ids) == 0 {
			ids = []string{alt}
		}
		state := ""
		for _, id := range ids {
			switch {
			case containsID(forbidden, id):
				state = "forbidden"
				reasons = append(reasons, id+" forbidden")
			case containsID(review, id):
				if state != "forbidden" {
					state = "review"
				}
				reasons = append(reasons, id+" requires review")
			case containsID(allowed, id):
				if state == "" {
					state = "allowed"
				}
			default:
				if state == "" {
					state = defaultAction
				}
			}
			if state == "forbidden" {
				break
			}
		}

		if state == "allowed" {
			anyAllowed = true
		}
		if state != "forbidden" {
			allForbidden = false
		}
	}

	switch {
	case anyAllowed:
		return LicenseCompliant, strings.Join(reasons, "; ")
	case allForbidden && len(alts) > 0:
		return LicenseNoncompliant, strings.Join(reasons, "; ")
	default:
		return LicenseReview, strings.Join(reasons, "; ")
	}
}

// splitTopLevel 按顶层分隔符拆分（不做完整 SPDX 括号解析，忽略括号内分隔符）
func splitTopLevel(expr, sep string) []string {
	if !strings.Contains(expr, sep) {
		return []string{strings.TrimSpace(expr)}
	}
	// WITH 保持整体：不拆分以 WITH 连接的项
	var out []string
	depth := 0
	start := 0
	for i := 0; i < len(expr); i++ {
		switch expr[i] {
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		default:
			if depth == 0 && strings.HasPrefix(expr[i:], sep) {
				out = append(out, strings.TrimSpace(expr[start:i]))
				i += len(sep) - 1
				start = i + 1
			}
		}
	}
	out = append(out, strings.TrimSpace(expr[start:]))
	return out
}
