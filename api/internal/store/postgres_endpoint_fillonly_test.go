package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jtb75/silkstrand/api/internal/model"
)

// TestUpsertAssetEndpointFillOnly covers the ADR 019 P1 backfill semantics:
// FillOnly is existing-wins (never overwrite a known service/version, fill NULL
// only), service and version coalesce independently, and empty values never
// clobber. The default path stays incoming-wins (httpx/naabu).
func TestUpsertAssetEndpointFillOnly(t *testing.T) {
	st := newTestStore(t)
	const tenantID = "f0f0f0f0-9999-4999-8999-000000000009"
	ctx := WithTenantID(context.Background(), tenantID)

	exec := func(q string, args ...any) {
		if _, err := st.db.ExecContext(context.Background(), q, args...); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	exec(`DELETE FROM tenants WHERE id = $1`, tenantID)
	exec(`INSERT INTO tenants (id, name) VALUES ($1, 'fillonly-test')`, tenantID)
	t.Cleanup(func() { _, _ = st.db.ExecContext(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenantID) })

	asset, err := st.UpsertAsset(ctx, UpsertAssetInput{TenantID: tenantID, PrimaryIP: "10.9.9.9", Source: "discovered"})
	if err != nil {
		t.Fatalf("UpsertAsset: %v", err)
	}

	svc := func(ae *model.AssetEndpoint) string {
		if ae.Service == nil {
			return "<nil>"
		}
		return *ae.Service
	}
	ver := func(ae *model.AssetEndpoint) string {
		if ae.Version == nil {
			return "<nil>"
		}
		return *ae.Version
	}

	// 1. naabu: endpoint with no service/version.
	ep, err := st.UpsertAssetEndpoint(ctx, UpsertAssetEndpointInput{AssetID: asset.ID, Port: 1433})
	if err != nil {
		t.Fatalf("naabu upsert: %v", err)
	}
	if ep.Service != nil || ep.Version != nil {
		t.Fatalf("after naabu want NULL svc/ver, got %q/%q", svc(ep), ver(ep))
	}

	// 2. nuclei-network fill-only fills the NULLs.
	ep, _ = st.UpsertAssetEndpoint(ctx, UpsertAssetEndpointInput{AssetID: asset.ID, Port: 1433, Service: "mssql", Version: "2022", FillOnly: true})
	if svc(ep) != "mssql" || ver(ep) != "2022" {
		t.Fatalf("fill-only into NULL: want mssql/2022, got %q/%q", svc(ep), ver(ep))
	}

	// 3. fill-only again with different values → existing wins (no overwrite).
	ep, _ = st.UpsertAssetEndpoint(ctx, UpsertAssetEndpointInput{AssetID: asset.ID, Port: 1433, Service: "WRONG", Version: "0.0", FillOnly: true})
	if svc(ep) != "mssql" || ver(ep) != "2022" {
		t.Errorf("fill-only must not overwrite existing: want mssql/2022, got %q/%q", svc(ep), ver(ep))
	}

	// 4. httpx incoming-wins overwrites service; an empty version must not clobber.
	ep, _ = st.UpsertAssetEndpoint(ctx, UpsertAssetEndpointInput{AssetID: asset.ID, Port: 1433, Service: "nginx", FillOnly: false})
	if svc(ep) != "nginx" {
		t.Errorf("incoming-wins should overwrite service to nginx, got %q", svc(ep))
	}
	if ver(ep) != "2022" {
		t.Errorf("empty incoming version must not clobber existing, got %q", ver(ep))
	}

	// 5. service and version fill independently: service present, version NULL →
	// fill-only fills version while leaving the existing service untouched.
	ep, _ = st.UpsertAssetEndpoint(ctx, UpsertAssetEndpointInput{AssetID: asset.ID, Port: 5432, Service: "postgresql"})
	if svc(ep) != "postgresql" || ep.Version != nil {
		t.Fatalf("seed port 5432: want postgresql/NULL, got %q/%q", svc(ep), ver(ep))
	}
	ep, _ = st.UpsertAssetEndpoint(ctx, UpsertAssetEndpointInput{AssetID: asset.ID, Port: 5432, Service: "ignored", Version: "16.1", FillOnly: true})
	if svc(ep) != "postgresql" {
		t.Errorf("fill-only service existing-wins: want postgresql, got %q", svc(ep))
	}
	if ver(ep) != "16.1" {
		t.Errorf("fill-only version should fill independently when NULL: want 16.1, got %q", ver(ep))
	}

	// 6. FillOnly must NOT wipe technologies. Seed via the incoming-wins path
	// (httpx-style, with technologies), then a nuclei-network fill-only upsert
	// carrying none must preserve the existing technologies.
	ep, _ = st.UpsertAssetEndpoint(ctx, UpsertAssetEndpointInput{
		AssetID: asset.ID, Port: 8443, Service: "nginx",
		Technologies: json.RawMessage(`["nginx","php"]`),
	})
	if !strings.Contains(string(ep.Technologies), "nginx") {
		t.Fatalf("seed technologies: got %s", ep.Technologies)
	}
	ep, _ = st.UpsertAssetEndpoint(ctx, UpsertAssetEndpointInput{
		AssetID: asset.ID, Port: 8443, Service: "ignored", FillOnly: true,
	})
	if !strings.Contains(string(ep.Technologies), "nginx") {
		t.Errorf("FillOnly wiped technologies: got %s, want preserved nginx/php", ep.Technologies)
	}
}
