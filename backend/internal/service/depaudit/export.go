package depaudit

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"strings"
	"time"

	"github.com/ai-optimizer/backend/internal/model"
	"github.com/xuri/excelize/v2"
)

// Sheet 一个工作表
type Sheet struct {
	Name string
	Rows [][]string
}

// BuildItemsCSV 依赖清单 CSV
func BuildItemsCSV(items []*model.DependencyAuditItem) ([]byte, error) {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	_ = w.Write([]string{"生态", "包名", "版本", "类型", "版本精确", "来源文件", "purl", "许可证", "合规状态", "可达性"})
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

// BuildVulnsCSV 漏洞清单 CSV
func BuildVulnsCSV(vulns []*model.DependencyAuditVuln) ([]byte, error) {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	_ = w.Write([]string{"严重度", "包名", "版本", "漏洞ID", "别名", "修复版本", "摘要", "是否豁免", "可达性"})
	for _, v := range vulns {
		_ = w.Write([]string{
			v.Severity, v.PackageName, v.PackageVersion, v.VulnID,
			sanitizeCSV(v.Aliases), v.FixedVersions, sanitizeCSV(v.Summary),
			boolStr(v.Allowlisted), v.Reachable,
		})
	}
	w.Flush()
	return buf.Bytes(), w.Error()
}

// BuildExcel 生成多 sheet xlsx（excelize）
func BuildExcel(run *model.DependencyAuditRun, items []*model.DependencyAuditItem, vulns []*model.DependencyAuditVuln) ([]byte, error) {
	overview := [][]string{
		{"项目ID", fmt.Sprintf("%d", run.ProjectID)},
		{"分支", run.Branch},
		{"Commit", run.CommitSHA},
		{"触发方式", run.TriggerType},
		{"扫描时间", timeStr(run.CompletedAt)},
		{"依赖总数", fmt.Sprintf("%d", run.DepTotal)},
		{"直接依赖", fmt.Sprintf("%d", run.DepDirect)},
		{"间接依赖", fmt.Sprintf("%d", run.DepIndirect)},
		{"漏洞总数(不含豁免)", fmt.Sprintf("%d", run.VulnTotal)},
		{"严重", fmt.Sprintf("%d", run.VulnCritical)},
		{"高危", fmt.Sprintf("%d", run.VulnHigh)},
		{"中危", fmt.Sprintf("%d", run.VulnMedium)},
		{"低危", fmt.Sprintf("%d", run.VulnLow)},
		{"已豁免", fmt.Sprintf("%d", run.VulnAllowlisted)},
		{"许可证合规", fmt.Sprintf("%d", run.LicenseCompliant)},
		{"许可证不合规", fmt.Sprintf("%d", run.LicenseNoncompliant)},
		{"许可证待审查", fmt.Sprintf("%d", run.LicenseReview)},
		{"许可证未知", fmt.Sprintf("%d", run.LicenseUnknown)},
	}

	depRows := [][]string{{"生态", "包名", "版本", "类型", "版本精确", "来源文件", "purl", "许可证", "合规状态", "可达性"}}
	for _, it := range items {
		depRows = append(depRows, []string{
			it.Ecosystem, it.PackageName, it.PackageVersion, it.DependencyType,
			boolStr(it.VersionResolved), it.SourceFiles, it.PURL,
			it.LicenseExpr, it.LicenseStatus, it.Reachable,
		})
	}

	vulnRows := [][]string{{"严重度", "包名", "版本", "漏洞ID", "别名", "修复版本", "摘要", "是否豁免", "可达性"}}
	for _, v := range vulns {
		vulnRows = append(vulnRows, []string{
			v.Severity, v.PackageName, v.PackageVersion, v.VulnID,
			v.Aliases, v.FixedVersions, v.Summary,
			boolStr(v.Allowlisted), v.Reachable,
		})
	}

	licRows := [][]string{{"生态", "包名", "版本", "许可证", "合规状态", "判定依据"}}
	for _, it := range items {
		licRows = append(licRows, []string{
			it.Ecosystem, it.PackageName, it.PackageVersion,
			it.LicenseExpr, it.LicenseStatus, it.LicenseMatchMsg,
		})
	}

	sheets := []Sheet{
		{Name: "概览", Rows: overview},
		{Name: "依赖清单", Rows: depRows},
		{Name: "漏洞", Rows: vulnRows},
		{Name: "许可证", Rows: licRows},
	}
	return writeXLSX(sheets)
}

func timeStr(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format("2006-01-02 15:04:05")
}

func writeXLSX(sheets []Sheet) ([]byte, error) {
	f := excelize.NewFile()
	defer f.Close()

	for i, s := range sheets {
		name := excelizeSheetName(s.Name)
		if i == 0 {
			_ = f.SetSheetName("Sheet1", name)
		} else {
			if _, err := f.NewSheet(name); err != nil {
				return nil, err
			}
		}
		for r, row := range s.Rows {
			for c, val := range row {
				cell, err := excelize.CoordinatesToCellName(c+1, r+1)
				if err != nil {
					continue
				}
				if err := f.SetCellValue(name, cell, val); err != nil {
					return nil, err
				}
			}
		}
	}
	f.SetActiveSheet(0)
	buf, err := f.WriteToBuffer()
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// excelizeSheetName 规范化 sheet 名（<=31 字符，去除非法字符）
func excelizeSheetName(name string) string {
	replacer := strings.NewReplacer("/", "-", "\\", "-", "?", "-", "*", "-", "[", "-", "]", "-", ":", "-")
	name = replacer.Replace(name)
	runes := []rune(name)
	if len(runes) > 31 {
		name = string(runes[:31])
	}
	if name == "" {
		name = "Sheet"
	}
	return name
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// sanitizeCSV 防 CSV/Excel 注入
func sanitizeCSV(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	switch s[0] {
	case '=', '+', '-', '@':
		return "'" + s
	}
	return s
}
