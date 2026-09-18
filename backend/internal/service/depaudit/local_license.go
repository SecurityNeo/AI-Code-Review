package depaudit

import (
	"strings"

	"github.com/ai-optimizer/backend/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func licenseKey(eco, name, version string) string {
	return candidateKey(eco, name) + "|" + version
}

// LoadLocalLicenses 按依赖明细坐标批量读取本地许可证库
func LoadLocalLicenses(db *gorm.DB, items []*model.DependencyAuditItem) map[string]*model.PackageLicense {
	out := map[string]*model.PackageLicense{}
	// 注意：空版本是合法查询键（未固定版本的依赖按 version="" 存有估算许可证），不能跳过
	pending := make([]*model.DependencyAuditItem, 0, len(items))
	for _, it := range items {
		pending = append(pending, it)
	}
	const chunk = 200
	for i := 0; i < len(pending); i += chunk {
		end := i + chunk
		if end > len(pending) {
			end = len(pending)
		}
		part := pending[i:end]
		conds := make([]string, 0, len(part))
		args := make([]interface{}, 0, len(part)*3)
		for _, it := range part {
			conds = append(conds, "(ecosystem = ? AND package_name = ? AND version = ?)")
			args = append(args, it.Ecosystem, it.PackageName, it.PackageVersion)
		}
		var rows []model.PackageLicense
		db.Where(strings.Join(conds, " OR "), args...).Find(&rows)
		for j := range rows {
			r := rows[j]
			out[licenseKey(r.Ecosystem, r.PackageName, r.Version)] = &r
		}
	}
	return out
}

// ApplyLocalLicenses 用本地库填充明细的许可证信息（未命中 → NOASSERTION）
func ApplyLocalLicenses(items []*model.DependencyAuditItem, lib map[string]*model.PackageLicense) {
	for _, it := range items {
		if rec, ok := lib[licenseKey(it.Ecosystem, it.PackageName, it.PackageVersion)]; ok {
			it.LicenseExpr = rec.LicenseSPDX
			it.LicenseSource = rec.DataSource
			continue
		}
		it.LicenseExpr = noAssertion
		it.LicenseSource = "none"
	}
}

// EnrichItemsFromCache 用当前本地许可证库补全明细中仍为"未知(NOASSERTION/空)"的项。
// 仅填充未知项、不覆盖已识别值；并同步重算该行的合规状态，保证与展示一致（不落库）。
func EnrichItemsFromCache(db *gorm.DB, orgID uint, items []*model.DependencyAuditItem) {
	var missing []*model.DependencyAuditItem
	for _, it := range items {
		if it.LicenseExpr == "" || it.LicenseExpr == noAssertion {
			missing = append(missing, it)
		}
	}
	if len(missing) == 0 {
		return
	}
	lib := LoadLocalLicenses(db, missing)
	cfg, _ := GetConfig(db, orgID)
	for _, it := range missing {
		rec, ok := lib[licenseKey(it.Ecosystem, it.PackageName, it.PackageVersion)]
		if !ok {
			continue
		}
		it.LicenseExpr = rec.LicenseSPDX
		it.LicenseSource = rec.DataSource
		it.LicenseStatus, it.LicenseMatchMsg = EvaluateLicense(it.LicenseExpr, cfg)
	}
}

// EnqueueMissingItems 将明细中本地库缺失的坐标写入同步队列
func EnqueueMissingItems(db *gorm.DB, items []*model.DependencyAuditItem, lib map[string]*model.PackageLicense) {
	var licenseBatch []*model.DependencyAuditSyncQueue
	seen := map[string]bool{}
	for _, it := range items {
		// 空版本：PyPI 允许按最新版本估算；范围未解析（有版本）仍跳过
		if it.PackageVersion == "" {
			if !isPypiEco(it.Ecosystem) {
				continue
			}
		} else if !it.VersionResolved {
			continue
		}
		key := licenseKey(it.Ecosystem, it.PackageName, it.PackageVersion)
		if _, ok := lib[key]; ok || seen[key] {
			continue
		}
		seen[key] = true
		licenseBatch = append(licenseBatch, &model.DependencyAuditSyncQueue{
			Ecosystem: it.Ecosystem, Name: it.PackageName, Version: it.PackageVersion,
			Kind: SyncKindLicense, Status: "pending",
		})
	}

	// 导入名（仅 pypi/maven）
	var importBatch []*model.DependencyAuditSyncQueue
	seenImp := map[string]bool{}
	for _, it := range items {
		if it.PackageVersion == "" || !it.VersionResolved || !isImportNameEco(it.Ecosystem) {
			continue
		}
		key := licenseKey(it.Ecosystem, it.PackageName, it.PackageVersion)
		if seenImp[key] {
			continue
		}
		seenImp[key] = true
		importBatch = append(importBatch, &model.DependencyAuditSyncQueue{
			Ecosystem: it.Ecosystem, Name: it.PackageName, Version: it.PackageVersion,
			Kind: SyncKindImportNames, Status: "pending",
		})
	}

	if len(licenseBatch) > 0 {
		db.Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(licenseBatch, 500)
	}
	if len(importBatch) > 0 {
		db.Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(importBatch, 500)
	}
}
