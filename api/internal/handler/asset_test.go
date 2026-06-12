package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jtb75/silkstrand/api/internal/model"
	"github.com/jtb75/silkstrand/api/internal/store"
)

func TestNormalizeDNSName(t *testing.T) {
	cases := []struct {
		in       string
		wantName string
		wantWild bool
		wantSkip bool // reason != ""
	}{
		{"app.example.com", "app.example.com", false, false},
		{"  APP.Example.COM  ", "app.example.com", false, false},               // trim + lowercase
		{"https://app.example.com/login?x=1", "app.example.com", false, false}, // scheme + path
		{"app.example.com:8443", "app.example.com", false, false},              // port
		{"user@app.example.com", "app.example.com", false, false},              // userinfo
		{"api.example.com.", "api.example.com", false, false},                  // trailing dot
		{"*.example.com", "*.example.com", true, false},                        // wildcard
		{"10.0.0.5", "", false, true},                                          // bare IP -> skip
		{"2001:db8::1", "", false, true},                                       // IPv6 -> skip
		{"not a host", "", false, true},
		{"", "", false, true},
		{"under_score.example.com", "", false, true}, // invalid char
		{"-bad.example.com", "", false, true},        // edge hyphen
	}
	for _, c := range cases {
		name, wild, reason := normalizeDNSName(c.in)
		skip := reason != ""
		if skip != c.wantSkip {
			t.Errorf("%q: skip=%v (reason %q) want skip=%v", c.in, skip, reason, c.wantSkip)
			continue
		}
		if c.wantSkip {
			continue
		}
		if name != c.wantName || wild != c.wantWild {
			t.Errorf("%q: got (%q, wild=%v) want (%q, wild=%v)", c.in, name, wild, c.wantName, c.wantWild)
		}
	}
}

type fakeAssetStore struct {
	store.Store
	upserted []store.UpsertAssetInput
}

func (f *fakeAssetStore) UpsertAsset(_ context.Context, in store.UpsertAssetInput) (*model.Asset, error) {
	f.upserted = append(f.upserted, in)
	h := in.Hostname
	return &model.Asset{ID: "asset-" + in.Hostname, Hostname: &h, ResourceType: in.ResourceType}, nil
}

func TestImportDNS(t *testing.T) {
	st := &fakeAssetStore{}
	h := NewAssetHandler(st)
	// app appears 3 ways (scheme/path, :port, plain) → one asset; api once;
	// a wildcard; a bare IP + garbage → skipped.
	body := `{"names":[
		"https://App.Example.com/login",
		"app.example.com:8443",
		"app.example.com",
		"api.example.com.",
		"*.internal.example.com",
		"10.0.0.5",
		"not a host"
	]}`
	r := httptest.NewRequest(http.MethodPost, "/api/v1/assets/import-dns", strings.NewReader(body))
	r = r.WithContext(store.WithTenantID(r.Context(), "t1"))
	w := httptest.NewRecorder()
	h.ImportDNS(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d (body %s)", w.Code, w.Body.String())
	}
	var resp struct {
		Imported         []map[string]any    `json:"imported"`
		Wildcards        []string            `json:"wildcards"`
		Skipped          []map[string]string `json:"skipped"`
		AllowlistEntries []string            `json:"allowlist_entries"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Imported) != 2 {
		t.Errorf("imported = %d, want 2 (app + api, deduped) — %v", len(resp.Imported), resp.Imported)
	}
	if len(st.upserted) != 2 {
		t.Errorf("UpsertAsset called %d times, want 2", len(st.upserted))
	}
	for _, in := range st.upserted {
		if in.ResourceType != model.ResourceTypeHTTPService {
			t.Errorf("upsert resource_type = %q, want http_service", in.ResourceType)
		}
		if in.Source != "imported" {
			t.Errorf("upsert source = %q, want imported", in.Source)
		}
	}
	if len(resp.Wildcards) != 1 || resp.Wildcards[0] != "*.internal.example.com" {
		t.Errorf("wildcards = %v, want [*.internal.example.com]", resp.Wildcards)
	}
	if len(resp.Skipped) != 2 {
		t.Errorf("skipped = %d, want 2 (IP + garbage) — %v", len(resp.Skipped), resp.Skipped)
	}
	// allowlist_entries = 2 concrete + 1 wildcard, sorted ('*' < 'a').
	if len(resp.AllowlistEntries) != 3 || resp.AllowlistEntries[0] != "*.internal.example.com" {
		t.Errorf("allowlist_entries = %v, want 3 sorted with wildcard first", resp.AllowlistEntries)
	}
}
