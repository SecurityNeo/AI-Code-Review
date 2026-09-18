package depaudit

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"
	"time"

	cdx "github.com/CycloneDX/cyclonedx-go"
	"github.com/ai-optimizer/backend/internal/model"
)

func sampleData() ([]*model.DependencyAuditItem, []*model.DependencyAuditVuln) {
	items := []*model.DependencyAuditItem{
		{ID: 1, Ecosystem: "Go", PackageName: "github.com/gin-gonic/gin", PackageVersion: "v1.9.1",
			DependencyType: "direct", VersionResolved: true, SourceFiles: `["go.mod"]`,
			PURL: "pkg:golang/github.com/gin-gonic/gin@v1.9.1", LicenseExpr: "MIT", LicenseStatus: LicenseCompliant, Reachable: "true"},
		{ID: 2, Ecosystem: "npm", PackageName: "lodash", PackageVersion: "4.17.21",
			DependencyType: "indirect", VersionResolved: true, SourceFiles: `["package-lock.json"]`,
			PURL: "pkg:npm/lodash@4.17.21", LicenseExpr: "MIT OR GPL-3.0", LicenseStatus: LicenseReview, Reachable: "false"},
	}
	vulns := []*model.DependencyAuditVuln{
		{ID: 1, ItemID: 1, PackageName: "github.com/gin-gonic/gin", PackageVersion: "v1.9.1",
			VulnID: "GO-2023-0001", Aliases: "CVE-2023-0001", Severity: "high", Summary: "test vuln"},
		{ID: 2, ItemID: 2, PackageName: "lodash", PackageVersion: "4.17.21",
			VulnID: "GHSA-xxxx", Severity: "low", Allowlisted: true},
	}
	return items, vulns
}

func TestBuildExcel_WithExcelize(t *testing.T) {
	items, vulns := sampleData()
	run := &model.DependencyAuditRun{ID: 1, ProjectID: 1, Branch: "main", DepTotal: len(items)}
	data, err := BuildExcel(run, items, vulns)
	if err != nil {
		t.Fatalf("BuildExcel error: %v", err)
	}
	if len(data) < 4 || string(data[:2]) != "PK" {
		t.Fatalf("excel output is not a valid zip/xlsx (len=%d)", len(data))
	}
}

func TestGenerateSBOMZip_CycloneDXAndSPDX(t *testing.T) {
	items, vulns := sampleData()
	in := SBOMInput{
		ProjectName: "demo", Branch: "main", CommitSHA: "abc123",
		Items: items, Vulns: vulns, IncludeTransitive: true,
	}
	data, err := GenerateSBOMZip(in)
	if err != nil {
		t.Fatalf("GenerateSBOMZip error: %v", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("zip open: %v", err)
	}
	found := map[string]bool{}
	for _, f := range zr.File {
		found[f.Name] = true
	}
	for _, name := range []string{"sbom.cyclonedx.json", "sbom.spdx.json", "dependencies.csv", "README.txt"} {
		if !found[name] {
			t.Fatalf("missing %s in zip", name)
		}
	}

	// 校验 CycloneDX 可被官方解码且含组件/漏洞
	for _, f := range zr.File {
		if f.Name != "sbom.cyclonedx.json" {
			continue
		}
		rc, _ := f.Open()
		var bom cdx.BOM
		if err := cdx.NewBOMDecoder(rc, cdx.BOMFileFormatJSON).Decode(&bom); err != nil {
			t.Fatalf("decode cyclonedx: %v", err)
		}
		rc.Close()
		if bom.Components == nil || len(*bom.Components) != 2 {
			t.Fatalf("want 2 components, got %v", bom.Components)
		}
		if bom.Vulnerabilities == nil || len(*bom.Vulnerabilities) != 2 {
			t.Fatalf("want 2 vulnerabilities")
		}
	}
}

func TestCdxLicenses_Expression(t *testing.T) {
	ls := cdxLicenses("MIT OR GPL-3.0")
	if len(ls) != 1 || ls[0].Expression == "" {
		t.Fatalf("expected expression license, got %+v", ls)
	}
	ls2 := cdxLicenses("MIT")
	if len(ls2) != 1 || ls2[0].License == nil || ls2[0].License.ID != "MIT" {
		t.Fatalf("expected id license, got %+v", ls2)
	}
}

var _ = strings.TrimSpace
var _ = time.Now
