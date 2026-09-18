package depaudit

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/ai-optimizer/backend/internal/model"
	"go.uber.org/zap"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const depsDevBase = "https://api.deps.dev/v3"

// LicenseClient deps.dev 许可证客户端
type LicenseClient struct {
	hc *http.Client
}

func NewLicenseClient() *LicenseClient {
	return &LicenseClient{hc: &http.Client{Timeout: 15 * time.Second}}
}

// ToDepsDevSystem 内部生态标识 → deps.dev system
func ToDepsDevSystem(eco string) string {
	switch strings.ToLower(eco) {
	case "go":
		return "GO"
	case "npm":
		return "NPM"
	case "pypi":
		return "PYPI"
	case "maven":
		return "MAVEN"
	default:
		return strings.ToUpper(eco)
	}
}

// VersionLicense 查询单个坐标的许可证
func (c *LicenseClient) VersionLicense(ctx context.Context, eco, name, version string) (spdx string, raw string, err error) {
	if name == "" || version == "" {
		return noAssertion, "", nil
	}
	encoded := url.PathEscape(name)
	// Maven 需要 groupId:artifactId，冒号显式编码
	encoded = strings.ReplaceAll(encoded, ":", "%3A")
	u := fmt.Sprintf("%s/systems/%s/packages/%s/versions/%s",
		depsDevBase, ToDepsDevSystem(eco), encoded, url.PathEscape(version))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", "", err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// 排空响应体以便连接复用
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		if resp.StatusCode == http.StatusNotFound {
			return noAssertion, "", nil
		}
		return "", "", fmt.Errorf("deps.dev status %d", resp.StatusCode)
	}
	var body struct {
		Licenses []string `json:"licenses"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", "", err
	}
	expr := JoinLicenses(body.Licenses)
	raw = strings.Join(body.Licenses, ",")
	return expr, raw, nil
}

// latestPyPIVersion 查询 PyPI 包的最新版本（用于未固定版本依赖的许可证估算）
func latestPyPIVersion(ctx context.Context, name string) (string, error) {
	hc := &http.Client{Timeout: 20 * time.Second}
	u := fmt.Sprintf("https://pypi.org/pypi/%s/json", url.PathEscape(name))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("pypi status %d", resp.StatusCode)
	}
	var body struct {
		Info struct {
			Version string `json:"version"`
		} `json:"info"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", err
	}
	if body.Info.Version == "" {
		return "", fmt.Errorf("no latest version for %s", name)
	}
	return body.Info.Version, nil
}

// getCachedLicense 查询全局缓存
func getCachedLicense(db *gorm.DB, eco, name, version string) (*model.PackageLicense, error) {
	var rec model.PackageLicense
	err := db.Where("ecosystem = ? AND package_name = ? AND version = ?", eco, name, version).First(&rec).Error
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

func upsertLicense(db *gorm.DB, rec *model.PackageLicense) error {
	now := time.Now()
	rec.FetchedAt = &now
	return db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "ecosystem"}, {Name: "package_name"}, {Name: "version"}},
		DoUpdates: clause.AssignmentColumns([]string{"license_spdx", "license_raw", "data_source", "fetched_at", "updated_at"}),
	}).Create(rec).Error
}

// ResolveLicenses 为依赖明细填充许可证（缓存 → deps.dev → NOASSERTION）
func ResolveLicenses(ctx context.Context, db *gorm.DB, items []*model.DependencyAuditItem) {
	client := NewLicenseClient()
	sem := make(chan struct{}, 5)
	var wg sync.WaitGroup

	for _, it := range items {
		if !it.VersionResolved || it.PackageVersion == "" {
			it.LicenseExpr = noAssertion
			it.LicenseSource = "none"
			continue
		}
		rec, cerr := getCachedLicense(db, it.Ecosystem, it.PackageName, it.PackageVersion)
		if cerr == nil {
			it.LicenseExpr = rec.LicenseSPDX
			it.LicenseSource = rec.DataSource
			continue
		}
		if !errors.Is(cerr, gorm.ErrRecordNotFound) {
			zap.L().Warn("license cache lookup failed",
				zap.String("pkg", it.PackageName), zap.Error(cerr))
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(item *model.DependencyAuditItem) {
			defer wg.Done()
			defer func() { <-sem }()
			reqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			expr, raw, err := client.VersionLicense(reqCtx, item.Ecosystem, item.PackageName, item.PackageVersion)
			if err != nil {
				zap.L().Debug("deps.dev query failed",
					zap.String("pkg", item.PackageName), zap.Error(err))
				item.LicenseExpr = noAssertion
				item.LicenseSource = "none"
				return
			}
			if expr != "" {
				if err := upsertLicense(db, &model.PackageLicense{
					Ecosystem: item.Ecosystem, PackageName: item.PackageName, Version: item.PackageVersion,
					LicenseSPDX: expr, LicenseRaw: raw, DataSource: "sync",
				}); err != nil {
					zap.L().Warn("upsert license failed",
						zap.String("pkg", item.PackageName), zap.Error(err))
				}
			}
			item.LicenseExpr = expr
			item.LicenseSource = "deps.dev"
		}(it)
	}
	wg.Wait()
}

// Coordinate 许可证同步坐标
type Coordinate struct {
	Ecosystem string `json:"ecosystem"`
	Name      string `json:"name"`
	Version   string `json:"version"`
}

// SyncLicenses 按坐标同步许可证（后台用），并上报进度
func SyncLicenses(ctx context.Context, db *gorm.DB, coords []Coordinate) (int, int) {
	client := NewLicenseClient()
	ok, failed := 0, 0
	for i, c := range coords {
		if ctx.Err() != nil {
			break
		}
		reqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		expr, raw, err := client.VersionLicense(reqCtx, c.Ecosystem, c.Name, c.Version)
		cancel()
		if err != nil {
			failed++
			SetSyncProgress(SyncKindLicense, i+1, failed)
			continue
		}
		if err := upsertLicense(db, &model.PackageLicense{
			Ecosystem: c.Ecosystem, PackageName: c.Name, Version: c.Version,
			LicenseSPDX: expr, LicenseRaw: raw, DataSource: "sync",
		}); err != nil {
			zap.L().Warn("upsert license failed", zap.String("pkg", c.Name), zap.Error(err))
			failed++
			SetSyncProgress(SyncKindLicense, i+1, failed)
			continue
		}
		ok++
		SetSyncProgress(SyncKindLicense, i+1, failed)
	}
	return ok, failed
}

// ImportLicenses 离线导入（CSV: ecosystem,package_name,version,license_spdx；或 JSON 数组）
func ImportLicenses(db *gorm.DB, filename string, r io.Reader) (imported int, errs []string, err error) {
	var records []Coordinate
	var spdxList []string

	if strings.HasSuffix(strings.ToLower(filename), ".json") {
		var arr []struct {
			Ecosystem   string `json:"ecosystem"`
			PackageName string `json:"package_name"`
			Version     string `json:"version"`
			LicenseSPDX string `json:"license_spdx"`
		}
		if err := json.NewDecoder(r).Decode(&arr); err != nil {
			return 0, nil, err
		}
		for _, a := range arr {
			records = append(records, Coordinate{a.Ecosystem, a.PackageName, a.Version})
			spdxList = append(spdxList, a.LicenseSPDX)
		}
	} else {
		reader := csv.NewReader(r)
		reader.FieldsPerRecord = -1
		first := true
		for {
			row, e := reader.Read()
			if e == io.EOF {
				break
			}
			if e != nil {
				return imported, errs, e
			}
			if len(row) == 0 {
				continue
			}
			if first {
				first = false
				if strings.EqualFold(strings.TrimSpace(row[0]), "ecosystem") {
					continue
				}
			}
			if len(row) < 4 {
				errs = append(errs, "列数不足: "+strings.Join(row, ","))
				continue
			}
			records = append(records, Coordinate{
				Ecosystem: strings.TrimSpace(row[0]),
				Name:      strings.TrimSpace(row[1]),
				Version:   strings.TrimSpace(row[2]),
			})
			spdxList = append(spdxList, strings.TrimSpace(row[3]))
		}
	}

	for i, c := range records {
		if c.Ecosystem == "" || c.Name == "" || c.Version == "" {
			errs = append(errs, fmt.Sprintf("第 %d 行坐标不完整", i+1))
			continue
		}
		expr, raw := NormalizeSPDX(spdxList[i])
		if err := upsertLicense(db, &model.PackageLicense{
			Ecosystem: c.Ecosystem, PackageName: c.Name, Version: c.Version,
			LicenseSPDX: expr, LicenseRaw: raw, DataSource: "import",
		}); err != nil {
			errs = append(errs, fmt.Sprintf("第 %d 行写入失败: %v", i+1, err))
			continue
		}
		imported++
	}
	if imported == 0 && len(records) == 0 {
		return 0, nil, errors.New("文件中没有有效数据")
	}
	return imported, errs, nil
}

// LicenseStats 许可证缓存统计
func LicenseStats(db *gorm.DB) map[string]interface{} {
	var total int64
	var ecosystems int64
	db.Model(&model.PackageLicense{}).Count(&total)
	db.Model(&model.PackageLicense{}).Distinct("ecosystem").Count(&ecosystems)
	var last model.PackageLicense
	db.Where("fetched_at IS NOT NULL").Order("fetched_at desc").First(&last)
	res := map[string]interface{}{
		"total":        total,
		"ecosystems":   ecosystems,
		"last_sync_at": nil,
		"sync":         SyncStatus(SyncKindLicense),
	}
	if last.ID != 0 && last.FetchedAt != nil {
		res["last_sync_at"] = last.FetchedAt
	}
	return res
}
