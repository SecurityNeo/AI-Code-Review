package depaudit

import (
	"testing"

	"github.com/ai-optimizer/backend/internal/model"
)

func cfgEnforced(allowed, forbidden, review, def string) *model.DependencyAuditConfig {
	return &model.DependencyAuditConfig{
		LicenseComplianceMode:  ModeEnforced,
		AllowedLicenses:        allowed,
		ForbiddenLicenses:      forbidden,
		ReviewRequiredLicenses: review,
		DefaultLicenseAction:   def,
	}
}

func TestEvaluateLicense_ReportOnly(t *testing.T) {
	cfg := &model.DependencyAuditConfig{LicenseComplianceMode: ModeReportOnly}
	status, _ := EvaluateLicense("MIT", cfg)
	if status != LicenseNA {
		t.Fatalf("want na, got %s", status)
	}
}

func TestEvaluateLicense_LooseOR(t *testing.T) {
	cfg := cfgEnforced(`["MIT"]`, `["GPL-3.0"]`, "", "review")
	status, _ := EvaluateLicense("MIT OR GPL-3.0", cfg)
	if status != LicenseCompliant {
		t.Fatalf("want compliant, got %s", status)
	}
}

func TestEvaluateLicense_AllForbidden(t *testing.T) {
	cfg := cfgEnforced(`["MIT"]`, `["GPL-3.0"]`, "", "review")
	status, _ := EvaluateLicense("GPL-3.0", cfg)
	if status != LicenseNoncompliant {
		t.Fatalf("want non_compliant, got %s", status)
	}
}

func TestEvaluateLicense_UnknownDefaultReview(t *testing.T) {
	cfg := cfgEnforced(`["MIT"]`, `["GPL-3.0"]`, "", "review")
	status, _ := EvaluateLicense("BSD-4-Clause", cfg)
	if status != LicenseReview {
		t.Fatalf("want review, got %s", status)
	}
}

func TestEvaluateLicense_NoAssertion(t *testing.T) {
	cfg := cfgEnforced(`["MIT"]`, "", "", "review")
	status, _ := EvaluateLicense(noAssertion, cfg)
	if status != LicenseReview {
		t.Fatalf("want review, got %s", status)
	}
}

func TestEvaluateLicense_WithKeptWhole(t *testing.T) {
	// WITH 作为整体，不应把 exception 当独立 license 判定
	cfg := cfgEnforced(`["gpl-2.0-only with classpath-exception-2.0"]`, "", "", "review")
	status, _ := EvaluateLicense("GPL-2.0-only WITH Classpath-exception-2.0", cfg)
	if status != LicenseCompliant {
		t.Fatalf("want compliant, got %s", status)
	}
}

func TestNormalizeSPDX(t *testing.T) {
	if e, _ := NormalizeSPDX(""); e != noAssertion {
		t.Fatalf("empty want NOASSERTION, got %s", e)
	}
	if e, _ := NormalizeSPDX("non-standard"); e != "LicenseRef-non-standard" {
		t.Fatalf("non-standard mismatch: %s", e)
	}
	if e, _ := NormalizeSPDX("MIT"); e != "MIT" {
		t.Fatalf("MIT mismatch: %s", e)
	}
}
