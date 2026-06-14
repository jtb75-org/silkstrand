package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jtb75/silkstrand/api/internal/audit"
	"github.com/jtb75/silkstrand/api/internal/middleware"
)

// newAuditHandlerWithFakeList returns an AuditHandler whose list seam records
// whether it was called and returns an empty result, so tests never touch a DB.
func newAuditHandlerWithFakeList(called *bool) *AuditHandler {
	return &AuditHandler{
		db: nil,
		list: func(context.Context, *sql.DB, audit.ListFilter) (*audit.ListResult, error) {
			*called = true
			return &audit.ListResult{Items: []audit.StoredEvent{}}, nil
		},
	}
}

func TestAuditHandlerList_AdminGate(t *testing.T) {
	tests := []struct {
		name       string
		claims     *middleware.Claims
		wantStatus int
		wantListed bool // whether the list seam should have run
	}{
		{
			name:       "admin gets 200 and the query runs",
			claims:     &middleware.Claims{TenantID: "t1", Role: "admin"},
			wantStatus: http.StatusOK,
			wantListed: true,
		},
		{
			name:       "member is forbidden and the query never runs",
			claims:     &middleware.Claims{TenantID: "t1", Role: "member"},
			wantStatus: http.StatusForbidden,
			wantListed: false,
		},
		{
			name:       "empty role is forbidden",
			claims:     &middleware.Claims{TenantID: "t1", Role: ""},
			wantStatus: http.StatusForbidden,
			wantListed: false,
		},
		{
			name:       "missing claims is unauthorized",
			claims:     nil,
			wantStatus: http.StatusUnauthorized,
			wantListed: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var listed bool
			h := newAuditHandlerWithFakeList(&listed)

			r := httptest.NewRequest(http.MethodGet, "/api/v1/audit-events", nil)
			if tt.claims != nil {
				r = r.WithContext(middleware.SetClaims(r.Context(), tt.claims))
			}
			w := httptest.NewRecorder()

			h.List(w, r)

			if w.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d (body=%s)", w.Code, tt.wantStatus, w.Body.String())
			}
			if listed != tt.wantListed {
				t.Errorf("list seam called = %v, want %v", listed, tt.wantListed)
			}
		})
	}
}

// TestAuditHandlerList_AdminResultShape confirms the admin path returns the
// lister's result as JSON (no regression to list behavior).
func TestAuditHandlerList_AdminResultShape(t *testing.T) {
	h := &AuditHandler{
		db: nil,
		list: func(context.Context, *sql.DB, audit.ListFilter) (*audit.ListResult, error) {
			return &audit.ListResult{
				Items:      []audit.StoredEvent{{ID: "ev1", EventType: "credential.fetch"}},
				NextCursor: "cur1",
			}, nil
		},
	}
	r := httptest.NewRequest(http.MethodGet, "/api/v1/audit-events", nil)
	r = r.WithContext(middleware.SetClaims(r.Context(), &middleware.Claims{TenantID: "t1", Role: "admin"}))
	w := httptest.NewRecorder()

	h.List(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	var got audit.ListResult
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(got.Items) != 1 || got.Items[0].ID != "ev1" {
		t.Errorf("items = %+v, want one item ev1", got.Items)
	}
	if got.NextCursor != "cur1" {
		t.Errorf("next_cursor = %q, want cur1", got.NextCursor)
	}
}
