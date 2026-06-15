package handler

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jtb75/silkstrand/api/internal/events"
	"github.com/jtb75/silkstrand/api/internal/middleware"
)

const testSecret = "test-jwt-secret"

func TestMintStreamToken(t *testing.T) {
	bus := events.NewMemoryBus()
	h := NewEventsHandler(bus, testSecret)

	body := bytes.NewBufferString(`{"filter":{"kinds":["scan_progress"],"scan_id":"scan-1"}}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/events/stream-tokens", body)
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(middleware.SetClaims(req.Context(), &middleware.Claims{TenantID: "tenant-1"}))
	rec := httptest.NewRecorder()

	h.MintStreamToken(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var resp StreamTokenResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Token == "" {
		t.Fatal("token: empty")
	}
	if time.Until(resp.ExpiresAt) <= 0 || time.Until(resp.ExpiresAt) > 2*streamTokenTTL {
		t.Errorf("expires_at out of range: %v", resp.ExpiresAt)
	}

	// Round-trip: verify claims parse and carry the filter we asked for.
	sc, err := verifyStreamToken(resp.Token, testSecret)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if sc.TenantID != "tenant-1" {
		t.Errorf("tenant_id: got %q want tenant-1", sc.TenantID)
	}
	if len(sc.Filter.Kinds) != 1 || sc.Filter.Kinds[0] != "scan_progress" {
		t.Errorf("kinds: got %v want [scan_progress]", sc.Filter.Kinds)
	}
	if sc.Filter.ScanID != "scan-1" {
		t.Errorf("scan_id: got %q want scan-1", sc.Filter.ScanID)
	}
}

func TestMintStreamTokenMissingClaims(t *testing.T) {
	bus := events.NewMemoryBus()
	h := NewEventsHandler(bus, testSecret)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/events/stream-tokens", nil)
	rec := httptest.NewRecorder()
	h.MintStreamToken(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status: got %d want 401", rec.Code)
	}
}

func TestStreamSSEFraming(t *testing.T) {
	bus := events.NewMemoryBus()
	h := NewEventsHandler(bus, testSecret)

	// Mint a token for tenant-1 with a kind filter.
	sc := streamClaims{
		Typ:      streamTokenType,
		TenantID: "tenant-1",
		Filter:   streamFilter{Kinds: []string{"scan_progress"}},
		Exp:      time.Now().Add(streamTokenTTL).Unix(),
	}
	token, err := signStreamToken(sc, testSecret)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	// Open the stream in a goroutine; read the first SSE frame.
	srv := httptest.NewServer(http.HandlerFunc(h.Stream))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"?token="+token, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: got %d want 200 body=%s", resp.StatusCode, string(b))
	}
	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("Content-Type: got %q want text/event-stream", got)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control: got %q want no-cache", got)
	}

	// Give the server a moment to register the subscription before publishing.
	// Poll the bus subscriber count rather than sleeping blindly.
	deadline := time.Now().Add(time.Second)
	for bus.SubscriberCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if bus.SubscriberCount() == 0 {
		t.Fatal("subscriber never registered")
	}

	payload, _ := json.Marshal(map[string]any{"stage": "naabu", "state": "started"})
	ev := events.Event{
		Kind:       "scan_progress",
		TenantID:   "tenant-1",
		ResourceID: "scan-1",
		OccurredAt: time.Now().UTC().Truncate(time.Second),
		Payload:    payload,
	}
	if err := bus.Publish(ctx, ev); err != nil {
		t.Fatalf("publish: %v", err)
	}

	// Read one SSE frame: "event: <kind>\ndata: <json>\n\n"
	br := bufio.NewReader(resp.Body)
	eventLine, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("read event line: %v", err)
	}
	if !strings.HasPrefix(eventLine, "event: scan_progress") {
		t.Errorf("event line: got %q want 'event: scan_progress'", eventLine)
	}
	dataLine, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("read data line: %v", err)
	}
	dataLine = strings.TrimPrefix(dataLine, "data: ")
	dataLine = strings.TrimSpace(dataLine)
	var got events.Event
	if err := json.Unmarshal([]byte(dataLine), &got); err != nil {
		t.Fatalf("decode data: %v data=%q", err, dataLine)
	}
	if got.Kind != "scan_progress" {
		t.Errorf("decoded kind: got %q want scan_progress", got.Kind)
	}
}

// TestStreamKindIntersection verifies the security invariant that an
// attacker holding a narrow stream token cannot widen scope via the
// query string. The token allows only scan_progress; ?kind=agent_log
// yields an empty intersection, which the handler rejects with 403.
func TestStreamKindIntersection(t *testing.T) {
	bus := events.NewMemoryBus()
	h := NewEventsHandler(bus, testSecret)

	sc := streamClaims{
		Typ:      streamTokenType,
		TenantID: "tenant-1",
		Filter:   streamFilter{Kinds: []string{"scan_progress"}},
		Exp:      time.Now().Add(streamTokenTTL).Unix(),
	}
	token, _ := signStreamToken(sc, testSecret)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/events/stream?token="+token+"&kind=agent_log", nil)
	rec := httptest.NewRecorder()
	h.Stream(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status: got %d want 403 body=%s", rec.Code, rec.Body.String())
	}
}

func TestStreamRejectsNonStreamToken(t *testing.T) {
	bus := events.NewMemoryBus()
	h := NewEventsHandler(bus, testSecret)

	// Forge a token with typ!=stream — verifyStreamToken must reject.
	sc := streamClaims{
		Typ:      "wrong",
		TenantID: "tenant-1",
		Exp:      time.Now().Add(streamTokenTTL).Unix(),
	}
	token, _ := signStreamToken(sc, testSecret)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/events/stream?token="+token, nil)
	rec := httptest.NewRecorder()
	h.Stream(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status: got %d want 401", rec.Code)
	}
}

func TestStreamExpiredTokenRejected(t *testing.T) {
	bus := events.NewMemoryBus()
	h := NewEventsHandler(bus, testSecret)

	sc := streamClaims{
		Typ:      streamTokenType,
		TenantID: "tenant-1",
		Exp:      time.Now().Add(-time.Hour).Unix(),
	}
	token, _ := signStreamToken(sc, testSecret)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/events/stream?token="+token, nil)
	rec := httptest.NewRecorder()
	h.Stream(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status: got %d want 401", rec.Code)
	}
}

func TestStreamMissingAuth(t *testing.T) {
	bus := events.NewMemoryBus()
	h := NewEventsHandler(bus, testSecret)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/events/stream", nil)
	rec := httptest.NewRecorder()
	h.Stream(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status: got %d want 401", rec.Code)
	}
}

func TestSplitCSV(t *testing.T) {
	cases := map[string][]string{
		"":             nil,
		"a":            {"a"},
		"a,b":          {"a", "b"},
		" a , b , , c": {"a", "b", "c"},
	}
	for in, want := range cases {
		got := splitCSV(in)
		if !stringSliceEq(got, want) {
			t.Errorf("splitCSV(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestIntersectKinds(t *testing.T) {
	cases := []struct {
		a, b   []string
		want   []string
		wantOK bool
	}{
		{nil, nil, nil, true},
		{nil, []string{"x"}, []string{"x"}, true},
		{[]string{"x"}, nil, []string{"x"}, true},
		{[]string{"a", "b"}, []string{"b", "c"}, []string{"b"}, true},
		{[]string{"a"}, []string{"x"}, nil, false},
	}
	for _, tc := range cases {
		got, ok := intersectKinds(tc.a, tc.b)
		if ok != tc.wantOK {
			t.Errorf("intersectKinds(%v, %v) ok = %v, want %v", tc.a, tc.b, ok, tc.wantOK)
		}
		if !stringSliceEq(got, tc.want) {
			t.Errorf("intersectKinds(%v, %v) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

func stringSliceEq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// fakeDeadliner records SetWriteDeadline calls for the SSE write-deadline test.
type fakeDeadliner struct {
	called bool
	got    time.Time
	err    error
}

func (f *fakeDeadliner) SetWriteDeadline(t time.Time) error {
	f.called = true
	f.got = t
	return f.err
}

// The long-lived SSE stream must clear the server WriteTimeout (a zero/no
// deadline), otherwise the second heartbeat lands past the 30s WriteTimeout and
// the stream drops at ~50s.
func TestClearStreamWriteDeadline(t *testing.T) {
	f := &fakeDeadliner{}
	if err := clearStreamWriteDeadline(f); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !f.called {
		t.Fatal("SetWriteDeadline was not called")
	}
	if !f.got.IsZero() {
		t.Errorf("write deadline = %v, want zero (no timeout)", f.got)
	}
}

func TestClearStreamWriteDeadlineErrorPropagates(t *testing.T) {
	f := &fakeDeadliner{err: http.ErrNotSupported}
	if err := clearStreamWriteDeadline(f); err != http.ErrNotSupported {
		t.Errorf("err = %v, want ErrNotSupported (caller logs + degrades)", err)
	}
}
