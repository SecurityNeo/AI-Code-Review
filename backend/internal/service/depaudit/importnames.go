package depaudit

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/ai-optimizer/backend/internal/model"
	"go.uber.org/zap"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const maxArtifactBytes = 50 * 1024 * 1024

// ImportNamesSyncOne 同步单个坐标的导入名映射（供后台手动同步调用）
func ImportNamesSyncOne(ctx context.Context, db *gorm.DB, eco, name, version string) {
	_ = ensureImportNames(ctx, db, eco, name, version)
}

// SyncImportNames 批量同步导入名映射并上报进度
func SyncImportNames(ctx context.Context, db *gorm.DB, coords []Coordinate) (ok, failed int) {
	for i, co := range coords {
		if ctx.Err() != nil {
			break
		}
		if rec := ensureImportNames(ctx, db, co.Ecosystem, co.Name, co.Version); rec != nil {
			ok++
		} else {
			failed++
		}
		SetSyncProgress(SyncKindImportNames, i+1, failed)
	}
	return ok, failed
}

// ensureImportNames 获取并缓存制品导入名映射（Go/npm 无需，返回 nil）
func ensureImportNames(ctx context.Context, db *gorm.DB, eco, name, version string) *model.PackageImportName {
	lower := strings.ToLower(eco)
	if lower == "go" || lower == "npm" {
		return nil
	}
	var rec model.PackageImportName
	if err := db.Where("ecosystem = ? AND package_name = ? AND version = ?", eco, name, version).First(&rec).Error; err == nil {
		return &rec
	}
	if lower != "pypi" && lower != "maven" {
		return nil
	}

	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var paths []string
	var artifactRef string
	var err error
	if lower == "pypi" {
		paths, artifactRef, err = fetchPyPIModules(reqCtx, name, version)
	} else {
		paths, artifactRef, err = fetchMavenPackages(reqCtx, name, version)
	}
	if err != nil || len(paths) == 0 {
		if err != nil {
			zap.L().Debug("fetch import names failed", zap.String("eco", eco), zap.String("pkg", name), zap.Error(err))
		}
		return nil
	}
	precision := "exact"
	if len(paths) > 5000 {
		paths = collapseToPrefix(paths)
		precision = "prefix"
	}
	pathsJSON, _ := json.Marshal(paths)
	rec = model.PackageImportName{
		Ecosystem: eco, PackageName: name, Version: version,
		ImportPaths: string(pathsJSON), Precision: precision,
		DataSource: "sync", ArtifactRef: artifactRef,
	}
	_ = upsertImportName(db, &rec)
	return &rec
}

func upsertImportName(db *gorm.DB, rec *model.PackageImportName) error {
	now := time.Now()
	rec.FetchedAt = &now
	return db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "ecosystem"}, {Name: "package_name"}, {Name: "version"}},
		DoUpdates: clause.AssignmentColumns([]string{"import_paths", "precision", "data_source", "artifact_ref", "fetched_at", "updated_at"}),
	}).Create(rec).Error
}

// fetchPyPIModules 下载 wheel 并从 RECORD/top_level.txt 提取模块路径
func fetchPyPIModules(ctx context.Context, name, version string) ([]string, string, error) {
	hc := &http.Client{Timeout: 30 * time.Second}
	metaURL := fmt.Sprintf("https://pypi.org/pypi/%s/%s/json", url.PathEscape(name), url.PathEscape(version))
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, metaURL, nil)
	resp, err := hc.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("pypi json status %d", resp.StatusCode)
	}
	var meta struct {
		URLs []struct {
			PackageType string `json:"packagetype"`
			URL         string `json:"url"`
		} `json:"urls"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		return nil, "", err
	}
	wheelURL := ""
	for _, u := range meta.URLs {
		if u.PackageType == "bdist_wheel" {
			wheelURL = u.URL
			break
		}
	}
	if wheelURL == "" {
		return nil, "", fmt.Errorf("no wheel for %s==%s", name, version)
	}
	data, err := download(ctx, hc, wheelURL)
	if err != nil {
		return nil, "", err
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, "", err
	}
	set := map[string]bool{}
	for _, f := range zr.File {
		switch {
		case strings.HasSuffix(f.Name, ".dist-info/RECORD"):
			modules := modulesFromRecord(f)
			for _, m := range modules {
				set[m] = true
			}
		case strings.HasSuffix(f.Name, ".dist-info/top_level.txt"):
			for _, m := range readLines(f) {
				if m != "" {
					set[m] = true
				}
			}
		}
	}
	return sortedKeys(set), wheelURL, nil
}

func modulesFromRecord(f *zip.File) []string {
	var out []string
	rc, err := f.Open()
	if err != nil {
		return nil
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, 5*1024*1024))
	if err != nil {
		return nil
	}
	for _, line := range strings.Split(string(data), "\n") {
		path := strings.TrimSpace(strings.SplitN(line, ",", 2)[0])
		if !strings.HasSuffix(path, ".py") {
			continue
		}
		mod := strings.TrimSuffix(path, ".py")
		mod = strings.TrimSuffix(mod, "/__init__")
		mod = strings.ReplaceAll(mod, "/", ".")
		if mod != "" && !strings.Contains(mod, "..") && !strings.HasPrefix(mod, "..") {
			out = append(out, mod)
		}
	}
	return out
}

func readLines(f *zip.File) []string {
	rc, err := f.Open()
	if err != nil {
		return nil
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, 1*1024*1024))
	if err != nil {
		return nil
	}
	var out []string
	for _, l := range strings.Split(string(data), "\n") {
		l = strings.TrimSpace(l)
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

// fetchMavenPackages 下载 jar 并提取包路径
func fetchMavenPackages(ctx context.Context, name, version string) ([]string, string, error) {
	parts := strings.SplitN(name, ":", 2)
	if len(parts) != 2 {
		return nil, "", fmt.Errorf("invalid maven coordinate %s", name)
	}
	group, artifact := parts[0], parts[1]
	// 逐段转义，防止 groupId/artifact/version 中的特殊字符改变请求路径
	groupPath := escapePathSegments(strings.Split(group, "."))
	art := url.PathEscape(artifact)
	ver := url.PathEscape(version)
	jarURL := fmt.Sprintf("https://repo1.maven.org/maven2/%s/%s/%s/%s-%s.jar",
		groupPath, art, ver, art, ver)
	hc := &http.Client{Timeout: 30 * time.Second}
	data, err := download(ctx, hc, jarURL)
	if err != nil {
		return nil, "", err
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, "", err
	}
	set := map[string]bool{}
	for _, f := range zr.File {
		if !strings.HasSuffix(f.Name, ".class") {
			continue
		}
		idx := strings.LastIndex(f.Name, "/")
		if idx <= 0 {
			continue
		}
		pkg := strings.ReplaceAll(f.Name[:idx], "/", ".")
		set[pkg] = true
	}
	return sortedKeys(set), jarURL, nil
}

// escapePathSegments 对路径各段分别转义并拼接，保留 "/" 作为分隔符
func escapePathSegments(segments []string) string {
	escaped := make([]string, 0, len(segments))
	for _, s := range segments {
		escaped = append(escaped, url.PathEscape(s))
	}
	return strings.Join(escaped, "/")
}

func download(ctx context.Context, hc *http.Client, u string) ([]byte, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxArtifactBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxArtifactBytes {
		return nil, fmt.Errorf("artifact exceeds %d bytes", maxArtifactBytes)
	}
	return data, nil
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// collapseToPrefix 条目过多时收敛为顶层前缀
func collapseToPrefix(paths []string) []string {
	set := map[string]bool{}
	for _, p := range paths {
		parts := strings.Split(p, ".")
		if len(parts) >= 2 {
			set[strings.Join(parts[:2], ".")] = true
		} else {
			set[p] = true
		}
	}
	return sortedKeys(set)
}

// ImportNameStats 导入名映射统计
func ImportNameStats(db *gorm.DB) map[string]interface{} {
	var total, ecosystems int64
	db.Model(&model.PackageImportName{}).Count(&total)
	db.Model(&model.PackageImportName{}).Distinct("ecosystem").Count(&ecosystems)
	var last model.PackageImportName
	db.Where("fetched_at IS NOT NULL").Order("fetched_at desc").First(&last)
	res := map[string]interface{}{
		"total":        total,
		"ecosystems":   ecosystems,
		"last_sync_at": nil,
		"sync":         SyncStatus(SyncKindImportNames),
	}
	if last.ID != 0 && last.FetchedAt != nil {
		res["last_sync_at"] = last.FetchedAt
	}
	return res
}

// ImportImportNames 离线导入映射（CSV: ecosystem,package_name,version,import_paths(;分隔)）
func ImportImportNames(db *gorm.DB, filename string, r io.Reader) (int, []string, error) {
	var errs []string
	imported := 0
	lines, err := readAllLines(r)
	if err != nil {
		return 0, nil, err
	}
	for i, row := range lines {
		if len(row) < 4 {
			continue
		}
		if i == 0 && strings.EqualFold(strings.TrimSpace(row[0]), "ecosystem") {
			continue
		}
		eco := strings.TrimSpace(row[0])
		name := strings.TrimSpace(row[1])
		ver := strings.TrimSpace(row[2])
		parts := strings.Split(row[3], ";")
		var paths []string
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p != "" {
				paths = append(paths, p)
			}
		}
		if eco == "" || name == "" || ver == "" || len(paths) == 0 {
			errs = append(errs, fmt.Sprintf("第 %d 行数据不完整", i+1))
			continue
		}
		pathsJSON, _ := json.Marshal(paths)
		rec := model.PackageImportName{
			Ecosystem: eco, PackageName: name, Version: ver,
			ImportPaths: string(pathsJSON), Precision: "exact", DataSource: "import",
		}
		if err := upsertImportName(db, &rec); err != nil {
			errs = append(errs, fmt.Sprintf("第 %d 行写入失败: %v", i+1, err))
			continue
		}
		imported++
	}
	return imported, errs, nil
}
