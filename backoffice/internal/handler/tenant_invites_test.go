package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jtb75/silkstrand/backoffice/internal/middleware"
	"github.com/jtb75/silkstrand/backoffice/internal/model"
	"github.com/jtb75/silkstrand/backoffice/internal/store"
)

// inviteStubStore embeds store.Store so we only implement the handful of
// methods the invite handlers touch; any other call would panic (and that's
// the signal a handler reached for something it shouldn't).
type inviteStubStore struct {
	store.Store

	tenant     *model.Tenant
	pending    []model.PendingInvite
	members    []model.TenantMember
	created    *createdInvite
	updatedTok *tokenUpdate
	deletedID  string
	deletedTen string
	deleteErr  error
}

type createdInvite struct {
	tenantID, email, role string
	invitedBy             *string
}

type tokenUpdate struct {
	id     string
	expiry time.Time
}

func (s *inviteStubStore) GetTenant(_ context.Context, _ string) (*model.Tenant, error) {
	return s.tenant, nil
}

func (s *inviteStubStore) ListPendingInvitations(_ context.Context, _ string) ([]model.PendingInvite, error) {
	return s.pending, nil
}

func (s *inviteStubStore) ListTenantMembers(_ context.Context, _ string) ([]model.TenantMember, error) {
	return s.members, nil
}

func (s *inviteStubStore) CreateInvitation(_ context.Context, tenantID, email, role string, _ []byte, _ time.Time, invitedBy *string) (*model.Invitation, error) {
	s.created = &createdInvite{tenantID: tenantID, email: email, role: role, invitedBy: invitedBy}
	return &model.Invitation{ID: "inv-new", TenantID: tenantID, Email: email, Role: role}, nil
}

func (s *inviteStubStore) UpdateInvitationToken(_ context.Context, id string, _ []byte, expiresAt time.Time) error {
	s.updatedTok = &tokenUpdate{id: id, expiry: expiresAt}
	return nil
}

func (s *inviteStubStore) DeleteInvitation(_ context.Context, id, tenantID string) error {
	s.deletedID = id
	s.deletedTen = tenantID
	return s.deleteErr
}

func (s *inviteStubStore) LogAudit(_ context.Context, _ model.AuditEntry) error { return nil }

// recordingMailer captures SendInvite calls.
type recordingMailer struct {
	to, url, tenant string
	calls           int
}

func (m *recordingMailer) SendInvite(to, inviteURL, tenantName string) error {
	m.calls++
	m.to, m.url, m.tenant = to, inviteURL, tenantName
	return nil
}

func (m *recordingMailer) SendPasswordReset(_, _ string) error { return nil }

func newInviteHandler(s store.Store, m *recordingMailer) *TenantHandler {
	return NewTenantHandler(s, nil, m, "https://app.example.com", nil)
}

func adminReq(method, target string, body string) *http.Request {
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
	} else {
		r = httptest.NewRequest(method, target, nil)
	}
	ctx := middleware.SetAdminClaims(r.Context(), &middleware.AdminClaims{AdminID: "admin-1", Email: "a@b.io"})
	return r.WithContext(ctx)
}

func TestCreateInvite(t *testing.T) {
	st := &inviteStubStore{tenant: &model.Tenant{ID: "t1", Name: "Acme"}}
	m := &recordingMailer{}
	h := newInviteHandler(st, m)

	r := adminReq(http.MethodPost, "/api/v1/tenants/t1/invites", `{"email":"  USER@Example.com ","role":"admin"}`)
	r.SetPathValue("id", "t1")
	w := httptest.NewRecorder()
	h.CreateInvite(w, r)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", w.Code, w.Body.String())
	}
	if st.created == nil {
		t.Fatal("CreateInvitation was not called")
	}
	if st.created.email != "user@example.com" {
		t.Errorf("email = %q, want normalized lowercase/trimmed", st.created.email)
	}
	if st.created.role != "admin" {
		t.Errorf("role = %q, want admin", st.created.role)
	}
	if st.created.invitedBy == nil || *st.created.invitedBy != "admin-1" {
		t.Errorf("invitedBy = %v, want admin-1", st.created.invitedBy)
	}
	if m.calls != 1 || m.to != "user@example.com" {
		t.Errorf("mailer got calls=%d to=%q", m.calls, m.to)
	}
	if !strings.Contains(m.url, "/accept-invite?token=") {
		t.Errorf("invite url = %q, missing accept-invite token", m.url)
	}
}

func TestCreateInviteDefaultsRoleMember(t *testing.T) {
	st := &inviteStubStore{tenant: &model.Tenant{ID: "t1", Name: "Acme"}}
	h := newInviteHandler(st, &recordingMailer{})

	r := adminReq(http.MethodPost, "/api/v1/tenants/t1/invites", `{"email":"x@y.io"}`)
	r.SetPathValue("id", "t1")
	w := httptest.NewRecorder()
	h.CreateInvite(w, r)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", w.Code)
	}
	if st.created.role != model.MembershipRoleMember {
		t.Errorf("role = %q, want member default", st.created.role)
	}
}

func TestCreateInviteRejectsBadInput(t *testing.T) {
	cases := []struct{ name, body string }{
		{"empty email", `{"email":"","role":"member"}`},
		{"bad role", `{"email":"x@y.io","role":"owner"}`},
		{"malformed json", `{`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := &inviteStubStore{tenant: &model.Tenant{ID: "t1", Name: "Acme"}}
			h := newInviteHandler(st, &recordingMailer{})
			r := adminReq(http.MethodPost, "/api/v1/tenants/t1/invites", c.body)
			r.SetPathValue("id", "t1")
			w := httptest.NewRecorder()
			h.CreateInvite(w, r)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", w.Code)
			}
			if st.created != nil {
				t.Error("CreateInvitation should not have been called")
			}
		})
	}
}

func TestCreateInviteTenantNotFound(t *testing.T) {
	st := &inviteStubStore{tenant: nil}
	h := newInviteHandler(st, &recordingMailer{})
	r := adminReq(http.MethodPost, "/api/v1/tenants/t1/invites", `{"email":"x@y.io","role":"member"}`)
	r.SetPathValue("id", "t1")
	w := httptest.NewRecorder()
	h.CreateInvite(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestResendInviteRotatesToken(t *testing.T) {
	st := &inviteStubStore{
		tenant:  &model.Tenant{ID: "t1", Name: "Acme"},
		pending: []model.PendingInvite{{ID: "inv-9", Email: "p@q.io", Role: "member"}},
	}
	m := &recordingMailer{}
	h := newInviteHandler(st, m)

	r := adminReq(http.MethodPost, "/api/v1/tenants/t1/invites/inv-9/resend", "")
	r.SetPathValue("id", "t1")
	r.SetPathValue("inviteId", "inv-9")
	w := httptest.NewRecorder()
	h.ResendInvite(w, r)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", w.Code, w.Body.String())
	}
	if st.updatedTok == nil || st.updatedTok.id != "inv-9" {
		t.Fatalf("UpdateInvitationToken not called for inv-9: %+v", st.updatedTok)
	}
	if m.calls != 1 || m.to != "p@q.io" {
		t.Errorf("mailer got calls=%d to=%q", m.calls, m.to)
	}
}

func TestResendInviteNotFound(t *testing.T) {
	st := &inviteStubStore{
		tenant:  &model.Tenant{ID: "t1", Name: "Acme"},
		pending: []model.PendingInvite{{ID: "other", Email: "p@q.io"}},
	}
	h := newInviteHandler(st, &recordingMailer{})
	r := adminReq(http.MethodPost, "/api/v1/tenants/t1/invites/inv-9/resend", "")
	r.SetPathValue("id", "t1")
	r.SetPathValue("inviteId", "inv-9")
	w := httptest.NewRecorder()
	h.ResendInvite(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
	if st.updatedTok != nil {
		t.Error("token should not be rotated when invite not found")
	}
}

func TestDeleteInvitePassesTenantBoundary(t *testing.T) {
	st := &inviteStubStore{}
	h := newInviteHandler(st, &recordingMailer{})
	r := adminReq(http.MethodDelete, "/api/v1/tenants/t1/invites/inv-9", "")
	r.SetPathValue("id", "t1")
	r.SetPathValue("inviteId", "inv-9")
	w := httptest.NewRecorder()
	h.DeleteInvite(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", w.Code)
	}
	if st.deletedID != "inv-9" || st.deletedTen != "t1" {
		t.Errorf("DeleteInvitation(id=%q, tenant=%q), want (inv-9, t1)", st.deletedID, st.deletedTen)
	}
}

func TestListInvitesAndMembers(t *testing.T) {
	st := &inviteStubStore{
		pending: []model.PendingInvite{{ID: "inv-1", Email: "p@q.io"}},
		members: []model.TenantMember{{UserID: "u1", Email: "m@n.io", Role: "admin", Status: "active"}},
	}
	h := newInviteHandler(st, &recordingMailer{})

	r := adminReq(http.MethodGet, "/api/v1/tenants/t1/invites", "")
	r.SetPathValue("id", "t1")
	w := httptest.NewRecorder()
	h.ListInvites(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("ListInvites status = %d, want 200", w.Code)
	}
	var invites []model.PendingInvite
	if err := json.Unmarshal(w.Body.Bytes(), &invites); err != nil {
		t.Fatalf("decode invites: %v", err)
	}
	if len(invites) != 1 || invites[0].ID != "inv-1" {
		t.Errorf("invites = %+v", invites)
	}

	r2 := adminReq(http.MethodGet, "/api/v1/tenants/t1/members", "")
	r2.SetPathValue("id", "t1")
	w2 := httptest.NewRecorder()
	h.ListMembers(w2, r2)
	if w2.Code != http.StatusOK {
		t.Fatalf("ListMembers status = %d, want 200", w2.Code)
	}
	var members []model.TenantMember
	if err := json.Unmarshal(w2.Body.Bytes(), &members); err != nil {
		t.Fatalf("decode members: %v", err)
	}
	if len(members) != 1 || members[0].Email != "m@n.io" {
		t.Errorf("members = %+v", members)
	}
}
