package store

import (
	"strings"
	"testing"
	"time"
)

// buildFindingWhereBase is the shared filter used by ListFindings and the
// filter-aware FindingsSeveritySummary; these cover that it honors the same
// clauses (and that the summary path, which blanks Severity, omits it).
func TestBuildFindingWhereBase(t *testing.T) {
	t.Run("source_kinds OR-match takes precedence and is parameterized", func(t *testing.T) {
		where, args := buildFindingWhereBase("t1", FindingFilter{
			SourceKinds: []string{"bundle_compliance", "network_compliance"},
			Status:      "open",
		})
		if !strings.Contains(where, "source_kind = ANY($2::text[])") {
			t.Errorf("where = %q, want source_kind ANY clause", where)
		}
		if !strings.Contains(where, "status = $3") {
			t.Errorf("where = %q, want status = $3", where)
		}
		if len(args) != 3 {
			t.Fatalf("args = %v, want 3 (tenant, kinds, status)", args)
		}
		kinds, ok := args[1].([]string)
		if !ok || len(kinds) != 2 {
			t.Errorf("args[1] = %v, want []string of len 2", args[1])
		}
	})

	t.Run("single source_kind fallback (no ANY)", func(t *testing.T) {
		where, _ := buildFindingWhereBase("t1", FindingFilter{SourceKind: "network_vuln"})
		if !strings.Contains(where, "source_kind = $2") {
			t.Errorf("where = %q, want source_kind = $2", where)
		}
		if strings.Contains(where, "ANY") {
			t.Errorf("single source_kind must not use ANY: %q", where)
		}
	})

	t.Run("severity present when set, omitted on the summary path (blanked)", func(t *testing.T) {
		with, _ := buildFindingWhereBase("t1", FindingFilter{Severity: "high"})
		if !strings.Contains(with, "severity = $2") {
			t.Errorf("want severity clause when set: %q", with)
		}
		without, _ := buildFindingWhereBase("t1", FindingFilter{}) // summary blanks Severity
		if strings.Contains(without, "severity") {
			t.Errorf("summary path must not filter by severity: %q", without)
		}
	})

	t.Run("date range uses last_seen bounds", func(t *testing.T) {
		since := time.Now().Add(-24 * time.Hour)
		until := time.Now()
		where, args := buildFindingWhereBase("t1", FindingFilter{Since: &since, Until: &until})
		if !strings.Contains(where, "last_seen >= $2") || !strings.Contains(where, "last_seen <= $3") {
			t.Errorf("where = %q, want last_seen bounds", where)
		}
		if len(args) != 3 {
			t.Fatalf("args = %v, want 3", args)
		}
	})
}
