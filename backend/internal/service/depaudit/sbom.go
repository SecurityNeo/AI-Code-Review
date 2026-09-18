package depaudit

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	cdx "github.com/CycloneDX/cyclonedx-go"
	"github.com/ai-optimizer/backend/internal/model"
	"gorm.io/gorm"
)

// SBOMInput SBOM 生成输入
type SBOMInput struct {
	ProjectName       string
	Branch            string
	CommitSHA         string
	VulnDBSnapshotAt  *time.Time
	Items             []*model.DependencyAuditItem
	Vulns             []*model.DependencyAuditVuln
	IncludeTransitive bool
}

// GenerateSBOMZip 生成包含 CycloneDX + SPDX + dependencies.csv + README 的 zip
func GenerateSBOMZip(in SBOMInput) ([]byte, error) {
	now := time.Now().UTC()
	cdxData, err := buildCycloneDX(in, now)
	if err != nil {
		return nil, err
	}
	spdx, err := buildSPDX(in, now)
	if err != nil {
		return nil, err
	}
	csvData, err := buildDependenciesCSV(in.Items)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	files := []struct {
		name string
		data []byte
	}{
		{"sbom.cyclonedx.json", cdxData},
		{"sbom.spdx.json", spdx},
		{"dependencies.csv", csvData},
		{"README.txt", []byte(buildSBOMReadme(in, now))},
	}
	for _, f := range files {
		w, err := zw.Create(f.name)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(f.data); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// RunWithProject 任务下的项目 run（附带项目名）
type RunWithProject struct {
	Run         model.DependencyAuditRun
	ProjectName string
}

// LatestRunsForTask 返回任务下每个项目最新 attempt 的 run
func LatestRunsForTask(db *gorm.DB, taskID uint) []RunWithProject {
	var runs []model.DependencyAuditRun
	db.Where("task_id = ?", taskID).Order("attempt asc").Find(&runs)
	latest := map[uint]model.DependencyAuditRun{}
	for _, r := range runs {
		if cur, ok := latest[r.ProjectID]; !ok || r.Attempt > cur.Attempt {
			latest[r.ProjectID] = r
		}
	}
	ids := make([]uint, 0, len(latest))
	for _, r := range latest {
		ids = append(ids, r.ProjectID)
	}
	names := map[uint]string{}
	if len(ids) > 0 {
		var projects []model.Project
		db.Where("id IN ?", ids).Find(&projects)
		for _, p := range projects {
			names[p.ID] = p.Name
		}
	}
	out := make([]RunWithProject, 0, len(latest))
	for _, r := range latest {
		out = append(out, RunWithProject{Run: r, ProjectName: names[r.ProjectID]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ProjectName < out[j].ProjectName })
	return out
}

// TaskRunIDs 返回任务下每个项目最新 attempt 的 run ID
func TaskRunIDs(db *gorm.DB, taskID uint) []uint {
	runs := LatestRunsForTask(db, taskID)
	ids := make([]uint, 0, len(runs))
	for _, r := range runs {
		ids = append(ids, r.Run.ID)
	}
	return ids
}

// EcosystemCount 生态依赖计数
type EcosystemCount struct {
	Ecosystem string `json:"ecosystem"`
	Count     int    `json:"count"`
}

// TaskDependencySummary 任务级依赖汇总：按生态 + 直接/间接
func TaskDependencySummary(db *gorm.DB, taskID uint) (ecos []EcosystemCount, direct, indirect int) {
	ids := TaskRunIDs(db, taskID)
	if len(ids) == 0 {
		return nil, 0, 0
	}
	db.Model(&model.DependencyAuditItem{}).
		Select("ecosystem, COUNT(*) as count").
		Where("run_id IN ?", ids).Group("ecosystem").
		Order("count desc").Scan(&ecos)

	var rows []struct {
		DependencyType string
		C              int
	}
	db.Model(&model.DependencyAuditItem{}).
		Select("dependency_type, COUNT(*) as c").
		Where("run_id IN ?", ids).Group("dependency_type").Scan(&rows)
	for _, r := range rows {
		if r.DependencyType == "direct" {
			direct = r.C
		} else {
			indirect += r.C
		}
	}
	return ecos, direct, indirect
}

// RunsData 一个 run 的明细
type RunsData struct {
	Items []*model.DependencyAuditItem
	Vulns []*model.DependencyAuditVuln
}

// LoadRunsData 批量加载若干 run 的 items/vulns
func LoadRunsData(db *gorm.DB, runIDs []uint) map[uint]RunsData {
	out := map[uint]RunsData{}
	if len(runIDs) == 0 {
		return out
	}
	var items []model.DependencyAuditItem
	db.Where("run_id IN ?", runIDs).Find(&items)
	var vulns []model.DependencyAuditVuln
	db.Where("run_id IN ?", runIDs).Find(&vulns)
	for i := range items {
		it := items[i]
		d := out[it.RunID]
		d.Items = append(d.Items, &it)
		out[it.RunID] = d
	}
	for i := range vulns {
		v := vulns[i]
		d := out[v.RunID]
		d.Vulns = append(d.Vulns, &v)
		out[v.RunID] = d
	}
	return out
}

// GenerateTaskSBOM 生成任务级 SBOM（合并 zip，内按项目分目录）
func GenerateTaskSBOM(db *gorm.DB, task *model.DependencyAuditTask) ([]byte, error) {
	now := time.Now().UTC()
	runs := LatestRunsForTask(db, task.ID)
	runIDs := make([]uint, 0, len(runs))
	for _, r := range runs {
		runIDs = append(runIDs, r.Run.ID)
	}
	data := LoadRunsData(db, runIDs)

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	add := func(name string, content []byte) error {
		w, err := zw.Create(name)
		if err != nil {
			return err
		}
		_, err = w.Write(content)
		return err
	}

	// 任务汇总
	var summary bytes.Buffer
	sw := csv.NewWriter(&summary)
	_ = sw.Write([]string{"project", "status", "dep_total", "vuln_total", "vuln_critical", "vuln_high", "license_unknown", "skip_reason", "error"})
	for _, r := range runs {
		_ = sw.Write([]string{
			r.ProjectName, r.Run.Status, itoa(r.Run.DepTotal), itoa(r.Run.VulnTotal),
			itoa(r.Run.VulnCritical), itoa(r.Run.VulnHigh), itoa(r.Run.LicenseUnknown),
			sanitizeCSV(r.Run.SkipReason), sanitizeCSV(r.Run.ErrorMessage),
		})
	}
	sw.Flush()
	if err := add("summary.csv", summary.Bytes()); err != nil {
		return nil, err
	}

	for _, r := range runs {
		dir := "projects/" + sanitizeFileName(r.ProjectName, r.Run.ProjectID) + "/"
		d := data[r.Run.ID]
		in := SBOMInput{
			ProjectName: r.ProjectName, Branch: r.Run.Branch, CommitSHA: r.Run.SnapshotCommit,
			VulnDBSnapshotAt: r.Run.VulnDBSnapshotAt, Items: d.Items, Vulns: d.Vulns,
		}
		cdxData, err := buildCycloneDX(in, now)
		if err != nil {
			return nil, err
		}
		spdxData, err := buildSPDX(in, now)
		if err != nil {
			return nil, err
		}
		csvData, err := buildDependenciesCSV(d.Items)
		if err != nil {
			return nil, err
		}
		if err := add(dir+"sbom.cyclonedx.json", cdxData); err != nil {
			return nil, err
		}
		if err := add(dir+"sbom.spdx.json", spdxData); err != nil {
			return nil, err
		}
		if err := add(dir+"dependencies.csv", csvData); err != nil {
			return nil, err
		}
	}

	if err := add("README.txt", []byte(buildTaskReadme(task, runs, now))); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func buildTaskReadme(task *model.DependencyAuditTask, runs []RunWithProject, now time.Time) string {
	var sb strings.Builder
	sb.WriteString("CodeGuard 依赖审查 · 任务 SBOM\n")
	sb.WriteString("=============================\n\n")
	fmt.Fprintf(&sb, "任务 ID: %d\n", task.ID)
	fmt.Fprintf(&sb, "触发方式: %s\n", task.TriggerType)
	fmt.Fprintf(&sb, "状态: %s\n", task.Status)
	fmt.Fprintf(&sb, "生成时间: %s\n", now.Format(time.RFC3339))
	fmt.Fprintf(&sb, "项目数: %d\n", len(runs))
	sb.WriteString("许可证数据来源: 本地库（deps.dev / 离线导入）\n")
	sb.WriteString("可达性口径: import 级精确判定（非函数级调用链）\n")
	sb.WriteString("\n目录: projects/<项目>/ 下含 sbom.cyclonedx.json / sbom.spdx.json / dependencies.csv；summary.csv 为项目汇总。\n")
	return sb.String()
}

// ========== CycloneDX 1.5（cyclonedx-go） ==========

func buildCycloneDX(in SBOMInput, now time.Time) ([]byte, error) {
	bom := cdx.NewBOM()
	bom.SpecVersion = cdx.SpecVersion1_5

	components := make([]cdx.Component, 0, len(in.Items))
	for _, it := range in.Items {
		comp := cdx.Component{
			Type:       cdx.ComponentTypeLibrary,
			BOMRef:     it.PURL,
			Name:       it.PackageName,
			Version:    it.PackageVersion,
			PackageURL: it.PURL,
		}
		licenses := cdxLicenses(it.LicenseExpr)
		comp.Licenses = &licenses
		components = append(components, comp)
	}
	bom.Components = &components

	toolComp := cdx.Component{Type: cdx.ComponentTypeApplication, Name: "dependency-audit", Version: "1.0.0"}
	bom.Metadata = &cdx.Metadata{
		Timestamp: now.Format(time.RFC3339),
		Tools:     &cdx.ToolsChoice{Components: &[]cdx.Component{toolComp}},
		Component: &cdx.Component{
			Type:    cdx.ComponentTypeApplication,
			Name:    in.ProjectName,
			Version: in.CommitSHA,
		},
	}

	if in.IncludeTransitive {
		deps := make([]string, 0, len(in.Items))
		for _, it := range in.Items {
			deps = append(deps, it.PURL)
		}
		bom.Dependencies = &[]cdx.Dependency{{Ref: "root", Dependencies: &deps}}
	}

	if len(in.Vulns) > 0 {
		vulns := make([]cdx.Vulnerability, 0, len(in.Vulns))
		for _, v := range in.Vulns {
			entry := cdx.Vulnerability{
				ID:          v.VulnID,
				Description: v.Summary,
				Ratings:     &[]cdx.VulnerabilityRating{{Severity: cdxSeverity(v.Severity)}},
				Affects:     &[]cdx.Affects{{Ref: purlForItem(in.Items, v.ItemID)}},
			}
			if v.Aliases != "" {
				entry.Recommendation = "aliases: " + v.Aliases
			}
			if v.Allowlisted {
				entry.Analysis = &cdx.VulnerabilityAnalysis{
					State:  cdx.IASNotAffected,
					Detail: "allowlisted by organization",
				}
			}
			vulns = append(vulns, entry)
		}
		bom.Vulnerabilities = &vulns
	}

	var buf bytes.Buffer
	enc := cdx.NewBOMEncoder(&buf, cdx.BOMFileFormatJSON)
	enc.SetPretty(true)
	if err := enc.EncodeVersion(bom, cdx.SpecVersion1_5); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func cdxLicenses(expr string) cdx.Licenses {
	expr = strings.TrimSpace(expr)
	if expr == "" || strings.EqualFold(expr, noAssertion) {
		return cdx.Licenses{{License: &cdx.License{Name: "NOASSERTION"}}}
	}
	if strings.Contains(expr, " OR ") || strings.Contains(expr, " AND ") {
		return cdx.Licenses{{Expression: expr}}
	}
	return cdx.Licenses{{License: &cdx.License{ID: expr}}}
}

func cdxSeverity(s string) cdx.Severity {
	switch strings.ToLower(s) {
	case "critical":
		return cdx.SeverityCritical
	case "high":
		return cdx.SeverityHigh
	case "medium":
		return cdx.SeverityMedium
	case "low":
		return cdx.SeverityLow
	default:
		return cdx.SeverityUnknown
	}
}

func purlForItem(items []*model.DependencyAuditItem, itemID uint) string {
	for _, it := range items {
		if it.ID == itemID {
			return it.PURL
		}
	}
	return ""
}

// ========== SPDX 2.3（手写，结构简单） ==========

func buildSPDX(in SBOMInput, now time.Time) ([]byte, error) {
	packages := make([]map[string]interface{}, 0, len(in.Items))
	for _, it := range in.Items {
		license := it.LicenseExpr
		if license == "" {
			license = noAssertion
		}
		packages = append(packages, map[string]interface{}{
			"SPDXID":           spdxID(it.Ecosystem, it.PackageName, it.PackageVersion),
			"name":             it.PackageName,
			"versionInfo":      it.PackageVersion,
			"downloadLocation": "NOASSERTION",
			"licenseConcluded": license,
			"licenseDeclared":  license,
			"externalRefs": []map[string]string{{
				"referenceCategory": "PACKAGE-MANAGER",
				"referenceType":     "purl",
				"referenceLocator":  it.PURL,
			}},
		})
	}
	doc := map[string]interface{}{
		"spdxVersion":       "SPDX-2.3",
		"dataLicense":       "CC0-1.0",
		"SPDXID":            "SPDXRef-DOCUMENT",
		"name":              in.ProjectName + "-" + in.Branch,
		"documentNamespace": fmt.Sprintf("https://codeguard.local/spdx/%s/%s/%d", in.ProjectName, in.Branch, now.Unix()),
		"creationInfo": map[string]interface{}{
			"created":  now.Format(time.RFC3339),
			"creators": []string{"Tool: CodeGuard-dependency-audit-1.0.0"},
		},
		"packages": packages,
	}
	return json.MarshalIndent(doc, "", "  ")
}

// spdxID 生成唯一 SPDXID；包含生态以避免同名同版本跨生态冲突
func spdxID(eco, name, version string) string {
	san := strings.NewReplacer("/", "-", ":", "-", "@", "-", ".", "-", " ", "-").Replace(name)
	san = strings.Trim(san, "-")
	return "SPDXRef-Package-" + strings.ToLower(eco) + "-" + san + "-" + strings.TrimPrefix(version, "v")
}

func buildDependenciesCSV(items []*model.DependencyAuditItem) ([]byte, error) {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	_ = w.Write([]string{"ecosystem", "package_name", "version", "type", "version_resolved", "source_files", "purl", "license", "license_status", "reachable"})
	for _, it := range items {
		_ = w.Write([]string{
			it.Ecosystem, it.PackageName, it.PackageVersion, it.DependencyType,
			boolStr(it.VersionResolved), it.SourceFiles, it.PURL,
			sanitizeCSV(it.LicenseExpr), it.LicenseStatus, it.Reachable,
		})
	}
	w.Flush()
	return buf.Bytes(), w.Error()
}

func buildSBOMReadme(in SBOMInput, now time.Time) string {
	var sb strings.Builder
	sb.WriteString("CodeGuard 依赖审查 SBOM\n")
	sb.WriteString("=======================\n\n")
	fmt.Fprintf(&sb, "项目: %s\n", in.ProjectName)
	fmt.Fprintf(&sb, "分支: %s\n", in.Branch)
	fmt.Fprintf(&sb, "Commit: %s\n", in.CommitSHA)
	fmt.Fprintf(&sb, "生成时间: %s\n", now.Format(time.RFC3339))
	fmt.Fprintf(&sb, "组件数: %d\n", len(in.Items))
	fmt.Fprintf(&sb, "漏洞数: %d\n", len(in.Vulns))
	if in.VulnDBSnapshotAt != nil {
		fmt.Fprintf(&sb, "漏洞库快照: %s\n", in.VulnDBSnapshotAt.Format(time.RFC3339))
	}
	sb.WriteString("许可证数据来源: deps.dev / 离线导入\n")
	sb.WriteString("可达性口径: import 级精确判定（非函数级调用链）\n")
	sb.WriteString("\n文件说明:\n")
	sb.WriteString("  sbom.cyclonedx.json  CycloneDX 1.5\n")
	sb.WriteString("  sbom.spdx.json       SPDX 2.3\n")
	sb.WriteString("  dependencies.csv     依赖清单\n")
	return sb.String()
}
