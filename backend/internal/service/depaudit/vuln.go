package depaudit

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ai-optimizer/backend/internal/model"
	"github.com/ai-optimizer/backend/internal/service/pipeline"
	"gorm.io/gorm"
)

// allowEntry 白名单条目（仅启用）
type allowEntry struct {
	VulnID      string
	PackageName string
	Ecosystem   string
	Version     string
}

func loadAllowlist(db *gorm.DB, orgID uint) []allowEntry {
	var rows []model.VulnerabilityAllowlist
	db.Where("enabled = ? AND org_id = ?", true, orgID).Find(&rows)
	out := make([]allowEntry, 0, len(rows))
	for _, r := range rows {
		out = append(out, allowEntry{r.VulnID, r.PackageName, r.Ecosystem, r.Version})
	}
	return out
}

func matchAllowlist(entries []allowEntry, vulnID, pkg, eco, version string) bool {
	for _, e := range entries {
		if e.VulnID != vulnID {
			continue
		}
		if e.PackageName != "" && !strings.EqualFold(e.PackageName, pkg) {
			continue
		}
		if e.Ecosystem != "" && !strings.EqualFold(e.Ecosystem, eco) {
			continue
		}
		if e.Version != "" && e.Version != version {
			continue
		}
		return true
	}
	return false
}

// MatchResult 漏洞匹配统计
type MatchResult struct {
	Vulns       []*model.DependencyAuditVuln
	Total       int
	Critical    int
	High        int
	Medium      int
	Low         int
	Allowlisted int
	SnapshotAt  *time.Time
}

// MatchVulnerabilities 只读匹配漏洞并打白名单标记。
// 通过按生态批量 IN 查询候选漏洞，避免逐依赖查询（N+1）。
func MatchVulnerabilities(db *gorm.DB, orgID uint, items []*model.DependencyAuditItem) *MatchResult {
	res := &MatchResult{}
	matcher := pipeline.NewVersionMatcher()
	allow := loadAllowlist(db, orgID)

	candidatesByKey := loadCandidates(db, items)
	seen := map[string]bool{} // itemID|vulnID，避免同一条记录重复入表

	for _, it := range items {
		if !it.VersionResolved || it.PackageVersion == "" {
			continue
		}
		candidates := lookupCandidates(candidatesByKey, it.Ecosystem, it.PackageName)
		for _, c := range candidates {
			matched := false
			var affectedRanges []map[string]interface{}
			var fixedVersions []string
			if c.AffectedVersions != "" {
				_ = json.Unmarshal([]byte(c.AffectedVersions), &affectedRanges)
			}
			if c.FixedVersions != "" {
				_ = json.Unmarshal([]byte(c.FixedVersions), &fixedVersions)
			}
			if len(affectedRanges) > 0 {
				matched = matcher.MatchOSVRange(it.PackageVersion, affectedRanges)
			}
			if !matched && len(fixedVersions) > 0 {
				for _, fv := range fixedVersions {
					if matcher.Compare(it.PackageVersion, fv) < 0 {
						matched = true
						break
					}
				}
			}
			if !matched && len(affectedRanges) == 0 && len(fixedVersions) == 0 {
				matched = true
			}
			if !matched {
				continue
			}
			dedupKey := fmt.Sprintf("%d|%s", it.ID, c.VulnID)
			if seen[dedupKey] {
				continue
			}
			seen[dedupKey] = true

			fixedJSON := c.FixedVersions
			v := &model.DependencyAuditVuln{
				RunID:          it.RunID,
				ItemID:         it.ID,
				OrgID:          it.OrgID,
				ProjectID:      it.ProjectID,
				PackageName:    it.PackageName,
				PackageVersion: it.PackageVersion,
				VulnID:         c.VulnID,
				Aliases:        c.Aliases,
				Severity:       normalizeSeverity(c.Severity),
				FixedVersions:  fixedJSON,
				Summary:        c.Summary,
				Allowlisted:    matchAllowlist(allow, c.VulnID, it.PackageName, it.Ecosystem, it.PackageVersion),
			}
			res.Vulns = append(res.Vulns, v)

			if v.Allowlisted {
				res.Allowlisted++
				continue
			}
			res.Total++
			switch v.Severity {
			case "critical":
				res.Critical++
			case "high":
				res.High++
			case "medium":
				res.Medium++
			case "low":
				res.Low++
			}
		}
	}

	var last model.VulnerabilityRecord
	if err := db.Order("imported_at desc").First(&last).Error; err == nil {
		t := last.ImportedAt
		res.SnapshotAt = &t
	}
	return res
}

// packageLookupNames 返回用于查询/索引的包名变体（PyPI 同时兼容原名与 PEP503 名）
func packageLookupNames(eco, name string) []string {
	if strings.EqualFold(eco, "pypi") {
		n := normalizeName("pypi", name)
		if n != name {
			return []string{name, n}
		}
	}
	return []string{name}
}

func candidateKey(eco, name string) string {
	return strings.ToLower(eco) + "|" + strings.ToLower(name)
}

// lookupCandidates 依次尝试包名变体查找候选漏洞
func lookupCandidates(m map[string][]model.VulnerabilityRecord, eco, name string) []model.VulnerabilityRecord {
	for _, n := range packageLookupNames(eco, name) {
		if v, ok := m[candidateKey(eco, n)]; ok {
			return v
		}
	}
	return nil
}

// loadCandidates 按生态批量加载候选漏洞，避免逐依赖查询
func loadCandidates(db *gorm.DB, items []*model.DependencyAuditItem) map[string][]model.VulnerabilityRecord {
	namesByEco := map[string]map[string]bool{}
	for _, it := range items {
		if !it.VersionResolved || it.PackageVersion == "" {
			continue
		}
		if namesByEco[it.Ecosystem] == nil {
			namesByEco[it.Ecosystem] = map[string]bool{}
		}
		for _, n := range packageLookupNames(it.Ecosystem, it.PackageName) {
			namesByEco[it.Ecosystem][n] = true
		}
	}

	out := map[string][]model.VulnerabilityRecord{}
	const chunkSize = 500
	for eco, names := range namesByEco {
		list := make([]string, 0, len(names))
		for n := range names {
			list = append(list, n)
		}
		for i := 0; i < len(list); i += chunkSize {
			end := i + chunkSize
			if end > len(list) {
				end = len(list)
			}
			var rows []model.VulnerabilityRecord
			if err := db.Where("ecosystem = ? AND package_name IN ?", eco, list[i:end]).Find(&rows).Error; err != nil {
				continue
			}
			for _, r := range rows {
				for _, n := range packageLookupNames(r.Ecosystem, r.PackageName) {
					k := candidateKey(eco, n)
					out[k] = append(out[k], r)
				}
			}
		}
	}
	return out
}

func normalizeSeverity(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "critical", "high", "medium", "low":
		return s
	default:
		return "unknown"
	}
}
