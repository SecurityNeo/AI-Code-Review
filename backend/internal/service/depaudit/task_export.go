package depaudit

import (
	"bytes"
	"encoding/csv"
	"fmt"

	"github.com/ai-optimizer/backend/internal/model"
)

// BuildTaskItemsCSV 任务级依赖清单（合并单文件 + 项目列）
func BuildTaskItemsCSV(runs []RunWithProject, data map[uint]RunsData) ([]byte, error) {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	_ = w.Write([]string{"项目", "生态", "包名", "版本", "类型", "版本精确", "来源文件", "purl", "许可证", "合规状态", "可达性"})
	for _, r := range runs {
		for _, it := range data[r.Run.ID].Items {
			_ = w.Write([]string{
				r.ProjectName, it.Ecosystem, it.PackageName, it.PackageVersion, it.DependencyType,
				boolStr(it.VersionResolved), it.SourceFiles, it.PURL,
				sanitizeCSV(it.LicenseExpr), it.LicenseStatus, it.Reachable,
			})
		}
	}
	w.Flush()
	return buf.Bytes(), w.Error()
}

// BuildTaskVulnsCSV 任务级漏洞清单（合并单文件 + 项目列）
func BuildTaskVulnsCSV(runs []RunWithProject, data map[uint]RunsData) ([]byte, error) {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	_ = w.Write([]string{"项目", "严重度", "包名", "版本", "漏洞ID", "别名", "修复版本", "摘要", "是否豁免", "可达性"})
	for _, r := range runs {
		for _, v := range data[r.Run.ID].Vulns {
			_ = w.Write([]string{
				r.ProjectName, v.Severity, v.PackageName, v.PackageVersion, v.VulnID,
				sanitizeCSV(v.Aliases), v.FixedVersions, sanitizeCSV(v.Summary),
				boolStr(v.Allowlisted), v.Reachable,
			})
		}
	}
	w.Flush()
	return buf.Bytes(), w.Error()
}

// BuildTaskExcel 任务级 Excel：概览按项目汇总 + 明细带项目列
func BuildTaskExcel(task *model.DependencyAuditTask, runs []RunWithProject, data map[uint]RunsData) ([]byte, error) {
	overview := [][]string{
		{"任务ID", fmt.Sprintf("%d", task.ID)},
		{"触发方式", task.TriggerType},
		{"状态", task.Status},
		{"项目数", fmt.Sprintf("%d", len(runs))},
		{"依赖总数", fmt.Sprintf("%d", task.DepTotal)},
		{"漏洞总数(不含豁免)", fmt.Sprintf("%d", task.VulnTotal)},
		{"严重", fmt.Sprintf("%d", task.VulnCritical)},
		{"高危", fmt.Sprintf("%d", task.VulnHigh)},
		{"中危", fmt.Sprintf("%d", task.VulnMedium)},
		{"低危", fmt.Sprintf("%d", task.VulnLow)},
		{"已豁免", fmt.Sprintf("%d", task.VulnAllowlisted)},
		{"许可证合规", fmt.Sprintf("%d", task.LicenseCompliant)},
		{"许可证不合规", fmt.Sprintf("%d", task.LicenseNoncompliant)},
		{"许可证待审查", fmt.Sprintf("%d", task.LicenseReview)},
		{"许可证未知", fmt.Sprintf("%d", task.LicenseUnknown)},
		{""},
		{"项目", "分支", "状态", "依赖数", "漏洞数", "严重", "高危", "许可证未知", "快照Commit", "跳过原因", "错误"},
	}
	for _, r := range runs {
		overview = append(overview, []string{
			r.ProjectName, r.Run.Branch, r.Run.Status,
			fmt.Sprintf("%d", r.Run.DepTotal), fmt.Sprintf("%d", r.Run.VulnTotal),
			fmt.Sprintf("%d", r.Run.VulnCritical), fmt.Sprintf("%d", r.Run.VulnHigh),
			fmt.Sprintf("%d", r.Run.LicenseUnknown), r.Run.SnapshotCommit,
			r.Run.SkipReason, r.Run.ErrorMessage,
		})
	}

	depRows := [][]string{{"项目", "生态", "包名", "版本", "类型", "版本精确", "来源文件", "purl", "许可证", "合规状态", "可达性"}}
	for _, r := range runs {
		for _, it := range data[r.Run.ID].Items {
			depRows = append(depRows, []string{
				r.ProjectName, it.Ecosystem, it.PackageName, it.PackageVersion, it.DependencyType,
				boolStr(it.VersionResolved), it.SourceFiles, it.PURL,
				it.LicenseExpr, it.LicenseStatus, it.Reachable,
			})
		}
	}

	vulnRows := [][]string{{"项目", "严重度", "包名", "版本", "漏洞ID", "别名", "修复版本", "摘要", "是否豁免", "可达性"}}
	for _, r := range runs {
		for _, v := range data[r.Run.ID].Vulns {
			vulnRows = append(vulnRows, []string{
				r.ProjectName, v.Severity, v.PackageName, v.PackageVersion, v.VulnID,
				v.Aliases, v.FixedVersions, v.Summary, boolStr(v.Allowlisted), v.Reachable,
			})
		}
	}

	licRows := [][]string{{"项目", "生态", "包名", "版本", "许可证", "合规状态", "判定依据"}}
	for _, r := range runs {
		for _, it := range data[r.Run.ID].Items {
			licRows = append(licRows, []string{
				r.ProjectName, it.Ecosystem, it.PackageName, it.PackageVersion,
				it.LicenseExpr, it.LicenseStatus, it.LicenseMatchMsg,
			})
		}
	}

	return writeXLSX([]Sheet{
		{Name: "概览", Rows: overview},
		{Name: "依赖清单", Rows: depRows},
		{Name: "漏洞", Rows: vulnRows},
		{Name: "许可证", Rows: licRows},
	})
}
