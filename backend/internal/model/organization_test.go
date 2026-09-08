package model

import (
	"testing"
	"time"
)

func TestGitLabInstance_SetAPIToken(t *testing.T) {
	inst := &GitLabInstance{}
	plain := "glpat-xxxxxxxxxxxx"

	if err := inst.SetAPIToken(plain); err != nil {
		t.Fatalf("SetAPIToken failed: %v", err)
	}

	if inst.APITokenEnc == "" {
		t.Fatal("APITokenEnc should not be empty after encryption")
	}

	decrypted, err := inst.GetAPIToken()
	if err != nil {
		t.Fatalf("GetAPIToken failed: %v", err)
	}

	if decrypted != plain {
		t.Fatalf("decrypted token mismatch: got %q, want %q", decrypted, plain)
	}
}

func TestGitLabInstance_GetAPIToken_Empty(t *testing.T) {
	inst := &GitLabInstance{APITokenEnc: ""}
	decrypted, err := inst.GetAPIToken()
	if err != nil {
		t.Fatalf("GetAPIToken on empty should not error: %v", err)
	}
	if decrypted != "" {
		t.Fatalf("expected empty string, got %q", decrypted)
	}
}

func TestOrgUserInvite_IsExpired(t *testing.T) {
	past := time.Now().Add(-1 * time.Hour)
	future := time.Now().Add(1 * time.Hour)

	invite := &OrgUserInvite{ExpiresAt: past}
	if !invite.IsExpired() {
		t.Fatal("invite should be expired")
	}

	invite2 := &OrgUserInvite{ExpiresAt: future}
	if invite2.IsExpired() {
		t.Fatal("invite should not be expired")
	}
}

func TestOrgUserInvite_CanAccept(t *testing.T) {
	future := time.Now().Add(1 * time.Hour)
	past := time.Now().Add(-1 * time.Hour)

	// pending + not expired = can accept
	invite1 := &OrgUserInvite{Status: "pending", ExpiresAt: future}
	if !invite1.CanAccept() {
		t.Fatal("pending + not expired should be acceptible")
	}

	// accepted = cannot accept
	invite2 := &OrgUserInvite{Status: "accepted", ExpiresAt: future}
	if invite2.CanAccept() {
		t.Fatal("accepted should not be acceptible")
	}

	// expired = cannot accept
	invite3 := &OrgUserInvite{Status: "pending", ExpiresAt: past}
	if invite3.CanAccept() {
		t.Fatal("expired should not be acceptible")
	}
}

func TestOrganization_Fields(t *testing.T) {
	org := Organization{
		Code:             "test-org",
		Name:             "Test Organization",
		Type:             "enterprise",
		Status:           "active",
		Timezone:         "Asia/Shanghai",
		Locale:           "zh-CN",
		MaxProjects:      100,
		LogRetentionDays: 365,
	}

	if org.Code != "test-org" {
		t.Fatalf("Code mismatch")
	}
	if org.MaxProjects != 100 {
		t.Fatalf("MaxProjects should be 100")
	}
	if org.LogRetentionDays != 365 {
		t.Fatalf("LogRetentionDays should be 365")
	}
}

func TestDepartment_Fields(t *testing.T) {
	dept := Department{
		OrgID:    1,
		Code:     "rd",
		Name:     "研发中心",
		ParentID: 0,
		Status:   "active",
	}
	if dept.OrgID != 1 {
		t.Fatal("OrgID mismatch")
	}
	if dept.Status != "active" {
		t.Fatalf("Status should be active, got %q", dept.Status)
	}
}

func TestTeam_Fields(t *testing.T) {
	team := Team{
		OrgID:  1,
		DeptID: 1,
		Code:   "backend",
		Name:   "后端组",
	}
	if team.OrgID != 1 {
		t.Fatal("OrgID mismatch")
	}
}

func TestResourceQuota_Fields(t *testing.T) {
	quota := ResourceQuota{
		OrgID:        1,
		ResourceType: "projects",
		LimitValue:   10,
		UsedValue:    5,
		PeriodType:   "monthly",
	}
	if quota.OrgID != 1 {
		t.Fatal("OrgID mismatch")
	}
	if quota.PeriodType != "monthly" {
		t.Fatalf("PeriodType should be monthly, got %q", quota.PeriodType)
	}
}

func TestGitLabUserBinding_Fields(t *testing.T) {
	binding := GitLabUserBinding{
		UserID:           1,
		GitLabInstanceID: 2,
		GitLabUserID:     100,
		IsPrimary:        true,
	}
	if binding.UserID != 1 {
		t.Fatal("UserID mismatch")
	}
}
