package audit

import (
	"context"
	"testing"
)

func ctxWith(ip, ua, email string) context.Context {
	return WithRequestMetadata(context.Background(), ip, ua, email)
}

// TestEnrich covers the request-scoped payload merge: metadata present → keys
// added; bare context → keys omitted (no panic); explicit call-site keys
// survive and are never clobbered; empty ctx values are dropped.
func TestEnrich(t *testing.T) {
	const ip, ua, email = "203.0.113.7", "curl/8.0", "sam@acme.test"

	tests := []struct {
		name    string
		ctx     context.Context
		payload map[string]any
		want    map[string]any
	}{
		{
			name:    "all metadata merged into empty payload",
			ctx:     ctxWith(ip, ua, email),
			payload: nil,
			want:    map[string]any{"ip": ip, "user_agent": ua, "actor_email": email},
		},
		{
			name:    "bare context omits all metadata keys",
			ctx:     context.Background(),
			payload: map[string]any{"name": "thing"},
			want:    map[string]any{"name": "thing"},
		},
		{
			name:    "explicit resource_label survives enrichment",
			ctx:     ctxWith(ip, ua, email),
			payload: map[string]any{"resource_label": "studio-mssql-sa"},
			want: map[string]any{
				"resource_label": "studio-mssql-sa",
				"ip":             ip, "user_agent": ua, "actor_email": email,
			},
		},
		{
			name:    "context does not clobber explicit metadata keys",
			ctx:     ctxWith(ip, ua, email),
			payload: map[string]any{"ip": "10.0.0.9", "actor_email": "explicit@x"},
			want: map[string]any{
				"ip": "10.0.0.9", "actor_email": "explicit@x", "user_agent": ua,
			},
		},
		{
			name:    "partial metadata adds only present keys (empty values dropped)",
			ctx:     ctxWith(ip, "", ""),
			payload: nil,
			want:    map[string]any{"ip": ip},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := enrich(tt.ctx, Event{Payload: tt.payload}).Payload
			if len(got) != len(tt.want) {
				t.Fatalf("payload key count = %d, want %d (got=%v)", len(got), len(tt.want), got)
			}
			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("payload[%q] = %v, want %v", k, got[k], v)
				}
			}
		})
	}
}

// TestEnrichDoesNotMutateCallerPayload ensures the merge works on a copy so the
// caller's map (which it may still hold) is never modified.
func TestEnrichDoesNotMutateCallerPayload(t *testing.T) {
	orig := map[string]any{"name": "x"}
	_ = enrich(ctxWith("1.1.1.1", "", ""), Event{Payload: orig})
	if _, leaked := orig["ip"]; leaked {
		t.Fatal("enrich mutated the caller's payload map")
	}
	if len(orig) != 1 {
		t.Fatalf("caller payload changed: %v", orig)
	}
}

// TestEnrichBareContextNilPayload confirms a background/system event (bare ctx,
// no payload) is returned untouched — nil stays nil, no allocation, no panic.
func TestEnrichBareContextNilPayload(t *testing.T) {
	got := enrich(context.Background(), Event{}).Payload
	if got != nil {
		t.Fatalf("expected nil payload for bare ctx + nil payload, got %v", got)
	}
}
