package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jtb75/silkstrand/api/internal/middleware"
	"github.com/jtb75/silkstrand/api/internal/model"
	"github.com/jtb75/silkstrand/api/internal/store"
)

func strptr(s string) *string { return &s }

func TestNormalizeZone(t *testing.T) {
	cases := []struct {
		in      string
		wantNil bool
		want    string
	}{
		{"", true, ""},
		{"   ", true, ""},
		{"---", true, ""},
		{"office-east", false, "office-east"},
		{"Office East", false, "office-east"},
		{"  East / Branch #2  ", false, "east-branch-2"},
		{"a__b..c", false, "a-b-c"},
	}
	for _, c := range cases {
		got := normalizeZone(c.in)
		if c.wantNil {
			if got != nil {
				t.Errorf("normalizeZone(%q) = %q, want nil", c.in, *got)
			}
			continue
		}
		if got == nil || *got != c.want {
			t.Errorf("normalizeZone(%q) = %v, want %q", c.in, got, c.want)
		}
	}
}

// fakeAgentStore implements just the reads AllowlistPreview needs; the rest of
// store.Store is the embedded nil interface (panics if unexpectedly called).
type fakeAgentStore struct {
	store.Store
	defs   []model.ScanDefinition
	agents []model.Agent
	snaps  map[string]*store.AgentAllowlistSnapshot
}

func (f *fakeAgentStore) ListScanDefinitions(context.Context) ([]model.ScanDefinition, error) {
	return f.defs, nil
}
func (f *fakeAgentStore) ListAgents(context.Context) ([]model.Agent, error) { return f.agents, nil }
func (f *fakeAgentStore) GetAgentAllowlist(_ context.Context, id string) (*store.AgentAllowlistSnapshot, error) {
	return f.snaps[id], nil
}

func TestAllowlistPreview(t *testing.T) {
	// Existing discovering agent dc-west (zone office-east) whose allowlist
	// snapshot covers 10.0.0.0/16 (private) and 203.0.113.0/24 (public).
	mkStore := func() *fakeAgentStore {
		return &fakeAgentStore{
			agents: []model.Agent{{ID: "ag-west", Name: "dc-west", Zone: strptr("office-east")}},
			defs: []model.ScanDefinition{{
				ID: "d1", Kind: model.ScanDefinitionKindDiscovery,
				ScopeKind: model.ScanDefinitionScopeAgentAllowlist,
				AgentID:   strptr("ag-west"), Enabled: true,
			}},
			snaps: map[string]*store.AgentAllowlistSnapshot{
				"ag-west": {AgentID: "ag-west", Allow: []string{"10.0.0.0/16", "203.0.113.0/24"}},
			},
		}
	}

	type resp struct {
		Overlaps  []map[string]any `json:"overlaps"`
		Redundant []string         `json:"redundant"`
	}
	call := func(t *testing.T, st *fakeAgentStore, body string) resp {
		t.Helper()
		h := NewAgentsHandler(st, nil, nil, nil, nil, "")
		r := httptest.NewRequest(http.MethodPost, "/api/v1/agents/allowlist-preview", bytes.NewBufferString(body))
		r = r.WithContext(middleware.SetClaims(r.Context(), &middleware.Claims{TenantID: "t1"}))
		w := httptest.NewRecorder()
		h.AllowlistPreview(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
		}
		var out resp
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return out
	}

	t.Run("same zone private overlap warns", func(t *testing.T) {
		out := call(t, mkStore(), `{"cidrs":["10.0.0.0/24"],"zone":"office-east"}`)
		if len(out.Overlaps) != 1 {
			t.Fatalf("want 1 overlap, got %d", len(out.Overlaps))
		}
	})
	t.Run("different zone private overlap suppressed", func(t *testing.T) {
		out := call(t, mkStore(), `{"cidrs":["10.0.0.0/24"],"zone":"office-west"}`)
		if len(out.Overlaps) != 0 {
			t.Fatalf("want 0 overlaps (zone-suppressed), got %d", len(out.Overlaps))
		}
	})
	t.Run("different zone public overlap always warns", func(t *testing.T) {
		out := call(t, mkStore(), `{"cidrs":["203.0.113.128/25"],"zone":"office-west"}`)
		if len(out.Overlaps) != 1 {
			t.Fatalf("want 1 overlap (public not suppressed), got %d", len(out.Overlaps))
		}
	})
	t.Run("unset input zone warns", func(t *testing.T) {
		out := call(t, mkStore(), `{"cidrs":["10.0.0.0/24"]}`)
		if len(out.Overlaps) != 1 {
			t.Fatalf("want 1 overlap (unset zone conservative), got %d", len(out.Overlaps))
		}
	})
	t.Run("no overlap", func(t *testing.T) {
		out := call(t, mkStore(), `{"cidrs":["192.168.50.0/24"],"zone":"office-west"}`)
		if len(out.Overlaps) != 0 {
			t.Fatalf("want 0 overlaps, got %d", len(out.Overlaps))
		}
	})
	t.Run("intra-input redundancy", func(t *testing.T) {
		out := call(t, mkStore(), `{"cidrs":["172.16.0.0/16","172.16.5.0/24"],"zone":"dc1"}`)
		if len(out.Redundant) != 1 {
			t.Fatalf("want 1 redundant note, got %d (%v)", len(out.Redundant), out.Redundant)
		}
	})

	// oneAgentStore builds a tenant with a single discovering agent whose
	// allowlist snapshot is `allow`.
	oneAgentStore := func(zone string, allow ...string) *fakeAgentStore {
		return &fakeAgentStore{
			agents: []model.Agent{{ID: "ag", Name: "other", Zone: strptr(zone)}},
			defs: []model.ScanDefinition{{
				ID: "d", Kind: model.ScanDefinitionKindDiscovery,
				ScopeKind: model.ScanDefinitionScopeAgentAllowlist,
				AgentID:   strptr("ag"), Enabled: true,
			}},
			snaps: map[string]*store.AgentAllowlistSnapshot{"ag": {AgentID: "ag", Allow: allow}},
		}
	}

	// Regression (nara P2 #1): a mixed supernet 10.0.0.0/7 spans public
	// 11.0.0.0/8, so a cross-zone overlap with it must NOT be suppressed.
	t.Run("mixed supernet does not suppress public overlap", func(t *testing.T) {
		out := call(t, oneAgentStore("office-east", "11.0.0.0/8"), `{"cidrs":["10.0.0.0/7"],"zone":"office-west"}`)
		if len(out.Overlaps) != 1 {
			t.Fatalf("want 1 overlap (public not suppressed), got %d", len(out.Overlaps))
		}
	})

	// Regression (nara P2 #2): "a-b" range entries must participate in overlap
	// math, on both the input and the source side.
	t.Run("range input overlaps cidr source", func(t *testing.T) {
		out := call(t, oneAgentStore("z", "10.0.0.0/24"), `{"cidrs":["10.0.0.20-10.0.0.30"],"zone":"z"}`)
		if len(out.Overlaps) != 1 {
			t.Fatalf("want 1 overlap (range input parsed), got %d", len(out.Overlaps))
		}
	})
	t.Run("range source overlaps cidr input", func(t *testing.T) {
		out := call(t, oneAgentStore("z", "10.0.0.10-10.0.0.50"), `{"cidrs":["10.0.0.0/24"],"zone":"z"}`)
		if len(out.Overlaps) != 1 {
			t.Fatalf("want 1 overlap (range source parsed), got %d", len(out.Overlaps))
		}
	})
}

func TestDiscoverScheduleToCron(t *testing.T) {
	cases := []struct {
		in      string
		wantNil bool
		want    string
		wantErr bool
	}{
		{"", true, "", false},
		{"off", true, "", false},
		{"OFF", true, "", false},
		{"daily", false, "0 3 * * *", false},
		{" Daily ", false, "0 3 * * *", false},
		{"weekly", false, "0 3 * * 1", false},
		{"hourly", false, "", true},
		{"garbage", false, "", true},
	}
	for _, c := range cases {
		got, err := discoverScheduleToCron(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("%q: expected error, got nil", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: unexpected error %v", c.in, err)
			continue
		}
		if c.wantNil {
			if got != nil {
				t.Errorf("%q: want nil cron, got %q", c.in, *got)
			}
			continue
		}
		if got == nil || *got != c.want {
			t.Errorf("%q: got %v want %q", c.in, got, c.want)
		}
	}
}
