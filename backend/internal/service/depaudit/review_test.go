package depaudit

import "testing"

func TestSanitizeMessage(t *testing.T) {
	in := "clone failed: fatal: unable to access 'https://oauth2:ghp_SECRET@github.com/org/repo.git/'"
	out := sanitizeMessage(in)
	if out == in {
		t.Fatal("credentials were not sanitized")
	}
	want := "clone failed: fatal: unable to access 'https://***@github.com/org/repo.git/'"
	if out != want {
		t.Fatalf("got %q, want %q", out, want)
	}
}

func TestSPDXIDUniqueness(t *testing.T) {
	a := spdxID("npm", "pkg", "1.0.0")
	b := spdxID("pypi", "pkg", "1.0.0")
	if a == b {
		t.Fatalf("spdxID should differ across ecosystems: %s", a)
	}
}

func TestSyncStatusLifecycle(t *testing.T) {
	kind := "test_kind"
	if st := SyncStatus(kind); st.Status != SyncIdle {
		t.Fatalf("want idle, got %s", st.Status)
	}
	if !TryStartSync(kind, 3) {
		t.Fatal("first start should succeed")
	}
	if TryStartSync(kind, 3) {
		t.Fatal("re-entrant start should be rejected")
	}
	if !IsSyncing(kind) {
		t.Fatal("should be running")
	}
	SetSyncProgress(kind, 2, 1)
	if st := SyncStatus(kind); st.Done != 2 || st.Failed != 1 {
		t.Fatalf("progress mismatch: %+v", st)
	}
	FinishSync(kind, 1, "成功 2，失败 1")
	st := SyncStatus(kind)
	if st.Status != SyncFailed || st.FinishedAt == nil {
		t.Fatalf("finish mismatch: %+v", st)
	}
	if IsSyncing(kind) {
		t.Fatal("should not be running after finish")
	}
}

func TestEvaluateLicense_ComplexExpressionReview(t *testing.T) {
	cfg := cfgEnforced(`["MIT","Apache-2.0"]`, `[]`, "", "review")
	// 带括号的复杂表达式应安全降级为 review，而不是误判合规
	status, _ := EvaluateLicense("(MIT OR GPL-3.0) AND Apache-2.0", cfg)
	if status != LicenseReview {
		t.Fatalf("complex expression should be review, got %s", status)
	}
}
