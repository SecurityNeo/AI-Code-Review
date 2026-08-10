package pipeline

import (
	"regexp"
	"strconv"
	"strings"
)

// VersionMatcher 语义化版本比较器
// 支持标准语义化版本、Go pseudo-version、带 v 前缀、OSV range 匹配
type VersionMatcher struct{}

func NewVersionMatcher() *VersionMatcher {
	return &VersionMatcher{}
}

// ParseSemver 将版本号解析为可比较的整数元组
// 返回: major, minor, patch, special, pseudoTime, isPseudo
func (m *VersionMatcher) ParseSemver(v string) (major, minor, patch int, special string, pseudoTime int64, isPseudo bool) {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "v")
	v = strings.TrimPrefix(v, "V")

	// 检查 pseudo-version: vX.0.0-yyyymmddhhmmss-abcdefabcdef
	if IsPseudoVersion(v) {
		parts := strings.Split(v, "-")
		if len(parts) >= 2 {
			// parts[0] = X.0.0, parts[1] = yyyymmddhhmmss
			baseParts := strings.Split(parts[0], ".")
			if len(baseParts) >= 1 {
				major, _ = strconv.Atoi(baseParts[0])
			}
			if len(baseParts) >= 2 {
				minor, _ = strconv.Atoi(baseParts[1])
			}
			if len(baseParts) >= 3 {
				patch, _ = strconv.Atoi(baseParts[2])
			}
			if len(parts[1]) >= 14 {
				pseudoTime, _ = strconv.ParseInt(parts[1][:14], 10, 64)
			}
			if len(parts) >= 3 {
				special = strings.Join(parts[2:], "-")
			}
			isPseudo = true
			return
		}
	}

	// 检查是否是时间戳格式: v0.0.0-20200202123456-abcdef
	pseudoRe := regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)-(\d+)-?([a-f0-9]+)`)
	if pseudoRe.MatchString(v) {
		matches := pseudoRe.FindStringSubmatch(v)
		major, _ = strconv.Atoi(matches[1])
		minor, _ = strconv.Atoi(matches[2])
		patch, _ = strconv.Atoi(matches[3])
		if len(matches[4]) >= 14 {
			pseudoTime, _ = strconv.ParseInt(matches[4][:14], 10, 64)
		}
		special = matches[5]
		isPseudo = true
		return
	}

	// 标准语义化版本: major.minor.patch[-/+]special
	re := regexp.MustCompile(`^(\d+)(?:\.(\d+))?(?:\.(\d+))?(?:[-+.]?(.*))?$`)
	matches := re.FindStringSubmatch(v)
	if len(matches) > 1 && matches[1] != "" {
		major, _ = strconv.Atoi(matches[1])
	}
	if len(matches) > 2 && matches[2] != "" {
		minor, _ = strconv.Atoi(matches[2])
	}
	if len(matches) > 3 && matches[3] != "" {
		patch, _ = strconv.Atoi(matches[3])
	}
	if len(matches) > 4 {
		special = matches[4]
	}

	return
}

// Compare 比较两个版本号
// 返回值: -1 (a < b), 0 (a == b), 1 (a > b)
func (m *VersionMatcher) Compare(a, b string) int {
	ma, mna, pa, sa, pta, ia := m.ParseSemver(a)
	mb, mnb, pb, sb, ptb, ib := m.ParseSemver(b)

	// 1. 如果都是 pseudo-version，先比较 base 版本，再比较时间戳
	if ia && ib {
		if ma != mb {
			return compareInt(ma, mb)
		}
		if mna != mnb {
			return compareInt(mna, mnb)
		}
		if pa != pb {
			return compareInt(pa, pb)
		}
		// 比较时间戳（越大越新）
		if pta != ptb {
			return compareInt64(pta, ptb)
		}
		// 比较 commit hash 前缀（按字符串比较）
		if sa != sb {
			if sa < sb {
				return -1
			}
			return 1
		}
		return 0
	}

	// 2. 标准版本比较
	if ma != mb {
		return compareInt(ma, mb)
	}
	if mna != mnb {
		return compareInt(mna, mnb)
	}
	if pa != pb {
		return compareInt(pa, pb)
	}

	// 3. 比较 special 部分（pre-release）
	// 无 pre-release 的版本 > 有 pre-release 的版本
	hasSA := sa != ""
	hasSB := sb != ""
	if !hasSA && hasSB {
		return 1 // a > b
	}
	if hasSA && !hasSB {
		return -1 // a < b
	}
	if sa != sb {
		if sa < sb {
			return -1
		}
		return 1
	}

	return 0
}

// MatchOSVRange 判断版本是否匹配 OSV affected range
// ranges: [{"introduced": "0", "fixed": "1.2.3"}, {"introduced": "1.4.0", "fixed": "1.5.0"}]
func (m *VersionMatcher) MatchOSVRange(version string, ranges []map[string]interface{}) bool {
	version = strings.TrimSpace(version)

	for _, r := range ranges {
		introduced, hasIntroduced := r["introduced"].(string)
		fixed, hasFixed := r["fixed"].(string)
		limit, hasLimit := r["limit"].(string)

		// 如果没有 introduced，默认从 "0" 开始
		if !hasIntroduced || introduced == "" {
			introduced = "0"
		}

		// 版本 >= introduced
		if m.Compare(version, introduced) < 0 {
			// 版本比 introduced 还小，不在当前 range
			continue
		}

		// 如果有 fixed，版本 < fixed
		if hasFixed && fixed != "" {
			if m.Compare(version, fixed) < 0 {
				return true // 在 [introduced, fixed) 范围内
			}
			continue
		}

		// 如果有 limit，版本 < limit
		if hasLimit && limit != "" {
			if m.Compare(version, limit) < 0 {
				return true // 在 [introduced, limit) 范围内
			}
			continue
		}

		// 既没有 fixed 也没有 limit，只要 >= introduced 就受影响（如 introduced: "0"）
		if !hasFixed && !hasLimit {
			return true
		}
	}

	return false
}

// MatchVersionList 判断版本是否在受影响版本列表中
func (m *VersionMatcher) MatchVersionList(version string, versions []string) bool {
	for _, v := range versions {
		if m.Compare(version, v) == 0 {
			return true
		}
	}
	return false
}

func compareInt(a, b int) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

func compareInt64(a, b int64) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}
