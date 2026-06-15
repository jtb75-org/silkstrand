package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jtb75/silkstrand/backoffice/internal/middleware"
	"github.com/jtb75/silkstrand/backoffice/internal/model"
	"github.com/jtb75/silkstrand/backoffice/internal/store"
)

// auditStubStore records audit entries and stubs the few methods the audited
// handlers touch; embeds store.Store so any unexpected call panics.
type auditStubStore struct {
	store.Store
	audits []model.AuditEntry
	dc     *model.DataCenter
	member *model.Membership
	admins int
}

func (s *auditStubStore) LogAudit(_ context.Context, e model.AuditEntry) error {
	s.audits = append(s.audits, e)
	return nil
}
func (s *auditStubStore) UpdateUserStatus(context.Context, string, string) error { return nil }
func (s *auditStubStore) DeleteUser(context.Context, string) error               { return nil }
func (s *auditStubStore) UpdateMembershipStatus(context.Context, string, string, string) error {
	return nil
}
func (s *auditStubStore) DeleteMembership(context.Context, string, string) error { return nil }
func (s *auditStubStore) CreateDataCenter(_ context.Context, dc model.DataCenter) (*model.DataCenter, error) {
	dc.ID = "dc-1"
	return &dc, nil
}
func (s *auditStubStore) GetDataCenter(context.Context, string) (*model.DataCenter, error) {
	return s.dc, nil
}
func (s *auditStubStore) DeleteDataCenter(context.Context, string) error { return nil }
func (s *auditStubStore) GetMembership(context.Context, string, string) (*model.Membership, error) {
	return s.member, nil
}
func (s *auditStubStore) CountActiveAdmins(context.Context, string) (int, error) {
	return s.admins, nil
}

// auditFor returns the recorded entry with the given action (or fails).
func auditFor(t *testing.T, audits []model.AuditEntry, action string) model.AuditEntry {
	t.Helper()
	for _, a := range audits {
		if a.Action == action {
			return a
		}
	}
	t.Fatalf("no audit entry with action %q; got %+v", action, audits)
	return model.AuditEntry{}
}

func tenantAdminReq(method, target, body string) *http.Request {
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
	} else {
		r = httptest.NewRequest(method, target, nil)
	}
	ctx := middleware.SetTenantClaims(r.Context(), &middleware.TenantClaims{
		Sub: "tu-1", Email: "admin@tenant.io", Role: model.MembershipRoleAdmin, BoTenantID: "t1",
	})
	return r.WithContext(ctx)
}

func TestUserUpdateStatusAudits(t *testing.T) {
	st := &auditStubStore{}
	h := NewUserHandler(st)
	r := adminReq(http.MethodPut, "/api/v1/users/u1/status", `{"status":"suspended"}`)
	r.SetPathValue("id", "u1")
	w := httptest.NewRecorder()
	h.UpdateStatus(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", w.Code, w.Body.String())
	}
	e := auditFor(t, st.audits, "user.suspend")
	if e.TargetID == nil || *e.TargetID != "u1" {
		t.Errorf("target_id = %v, want u1", e.TargetID)
	}
}

func TestUserDeleteAudits(t *testing.T) {
	st := &auditStubStore{}
	h := NewUserHandler(st)
	r := adminReq(http.MethodDelete, "/api/v1/users/u1", "")
	r.SetPathValue("id", "u1")
	w := httptest.NewRecorder()
	h.Delete(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", w.Code)
	}
	auditFor(t, st.audits, "user.delete")
}

func TestUserMembershipStatusAudits(t *testing.T) {
	st := &auditStubStore{}
	h := NewUserHandler(st)
	r := adminReq(http.MethodPut, "/api/v1/users/u1/memberships/t9/status", `{"status":"active"}`)
	r.SetPathValue("id", "u1")
	r.SetPathValue("tenant_id", "t9")
	w := httptest.NewRecorder()
	h.UpdateMembershipStatus(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", w.Code)
	}
	e := auditFor(t, st.audits, "user.membership.status")
	if e.TenantID == nil || *e.TenantID != "t9" {
		t.Errorf("tenant_id = %v, want t9", e.TenantID)
	}
}

func TestDataCenterCreateAudits(t *testing.T) {
	st := &auditStubStore{}
	key := make([]byte, 32) // AES-256 key for crypto.Encrypt
	h := NewDataCenterHandler(st, nil, key)
	body := `{"name":"DC US","region":"us","api_url":"http://dc.example:8080","api_key":"k","environment":"prod"}`
	r := adminReq(http.MethodPost, "/api/v1/data-centers", body)
	w := httptest.NewRecorder()
	h.Create(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", w.Code, w.Body.String())
	}
	e := auditFor(t, st.audits, "datacenter.create")
	if e.TargetID == nil || *e.TargetID != "dc-1" {
		t.Errorf("target_id = %v, want dc-1", e.TargetID)
	}
}

func TestDataCenterDeleteAudits(t *testing.T) {
	st := &auditStubStore{dc: &model.DataCenter{ID: "dc-1", Name: "DC US"}}
	h := NewDataCenterHandler(st, nil, make([]byte, 32))
	r := adminReq(http.MethodDelete, "/api/v1/data-centers/dc-1", "")
	r.SetPathValue("id", "dc-1")
	w := httptest.NewRecorder()
	h.Delete(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", w.Code)
	}
	auditFor(t, st.audits, "datacenter.delete")
}

func TestTenantAuthUpdateMemberStatusAudits(t *testing.T) {
	st := &auditStubStore{
		member: &model.Membership{Role: model.MembershipRoleMember, Status: model.MembershipStatusActive},
		admins: 2,
	}
	h := NewTenantAuthHandler(st, nil, "", "")
	r := tenantAdminReq(http.MethodPut, "/api/v1/tenant-auth/members/u2/status", `{"status":"suspended"}`)
	r.SetPathValue("user_id", "u2")
	w := httptest.NewRecorder()
	h.UpdateMemberStatus(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", w.Code, w.Body.String())
	}
	e := auditFor(t, st.audits, "member.status")
	if e.ActorType != model.ActorTypeTenantUser {
		t.Errorf("actor_type = %q, want tenant_user", e.ActorType)
	}
	if e.TenantID == nil || *e.TenantID != "t1" {
		t.Errorf("tenant_id = %v, want t1", e.TenantID)
	}
}
