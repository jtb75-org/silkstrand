package handler

import (
	"encoding/json"
	"testing"

	"github.com/jtb75/silkstrand/api/internal/model"
	"github.com/jtb75/silkstrand/api/internal/store"
)

// strptr is defined in agents_test.go (same package).

func ep(id, service string) store.EndpointRow {
	return store.EndpointRow{
		Asset:    model.Asset{ID: "a-" + id},
		Endpoint: model.AssetEndpoint{ID: id, Service: strptr(service)},
	}
}

func endpointCollection(id, name, predicate string) model.Collection {
	return model.Collection{
		ID:        id,
		Name:      name,
		Scope:     model.CollectionScopeEndpoint,
		Predicate: json.RawMessage(predicate),
	}
}

func TestAggregateCoverageByCollection(t *testing.T) {
	views := []store.EndpointRow{
		ep("e1", "postgresql"),
		ep("e2", "postgresql"),
		ep("e3", "postgresql"),
		ep("e4", "mysql"),
	}
	// e1 + e4 are covered (have an enabled scan_definition).
	covered := map[string]bool{"e1": true, "e4": true}

	colls := []model.Collection{
		endpointCollection("c-pg", "Postgres", `{"service":"postgresql"}`), // 3 matched, 1 covered → 33%
		endpointCollection("c-my", "MySQL", `{"service":"mysql"}`),         // 1 matched, 1 covered → 100%
		// asset-scope collection must be skipped entirely.
		{ID: "c-asset", Name: "Assets", Scope: model.CollectionScopeAsset, Predicate: json.RawMessage(`{}`)},
	}

	items, truncated := aggregateCoverageByCollection(views, covered, colls, 20)

	if truncated {
		t.Errorf("truncated = true, want false")
	}
	if len(items) != 2 {
		t.Fatalf("got %d rows, want 2 (asset-scope skipped): %+v", len(items), items)
	}
	// Worst-coverage first: Postgres (33%) before MySQL (100%).
	if items[0].CollectionID != "c-pg" {
		t.Errorf("row[0] = %s, want c-pg (worst coverage first)", items[0].CollectionID)
	}
	if items[0].MatchedEndpoints != 3 || items[0].CoveredEndpoints != 1 || items[0].CoveragePercent != 33 {
		t.Errorf("postgres row = %+v, want matched=3 covered=1 pct=33", items[0])
	}
	if items[1].CollectionID != "c-my" || items[1].CoveragePercent != 100 {
		t.Errorf("mysql row = %+v, want c-my pct=100", items[1])
	}
}

func TestAggregateCoverageByCollection_TruncatesWorstFirst(t *testing.T) {
	views := []store.EndpointRow{ep("e1", "postgresql")}
	covered := map[string]bool{} // nothing covered → all 0%

	// 3 endpoint collections, limit 2 → truncated, keep the 2 (here all 0%,
	// tie-broken by name: "A","B" kept, "C" dropped).
	colls := []model.Collection{
		endpointCollection("c-c", "C", `{"service":"postgresql"}`),
		endpointCollection("c-a", "A", `{"service":"postgresql"}`),
		endpointCollection("c-b", "B", `{"service":"postgresql"}`),
	}

	items, truncated := aggregateCoverageByCollection(views, covered, colls, 2)
	if !truncated {
		t.Errorf("truncated = false, want true")
	}
	if len(items) != 2 {
		t.Fatalf("got %d rows, want 2", len(items))
	}
	if items[0].Name != "A" || items[1].Name != "B" {
		t.Errorf("kept %q,%q, want A,B (tie-break by name)", items[0].Name, items[1].Name)
	}
}
