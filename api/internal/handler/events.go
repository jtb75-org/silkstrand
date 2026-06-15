package handler

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jtb75/silkstrand/api/internal/events"
	"github.com/jtb75/silkstrand/api/internal/middleware"
)

// EventsHandler wires the in-process events bus to the public
// /api/v1/events/* surface. Two routes:
//
//	POST /api/v1/events/stream-tokens  — mint a short-lived stream token
//	                                     from a regular tenant JWT (used
//	                                     by the browser because
//	                                     EventSource can't set headers)
//	GET  /api/v1/events/stream         — SSE stream; auth is either the
//	                                     tenant JWT (curl / CLI clients)
//	                                     or ?token=<stream-token>.
//
// PR A ships only the plumbing — no publishers exist yet.
type EventsHandler struct {
	bus       events.Bus
	jwtSecret string
}

// NewEventsHandler constructs a handler tied to the given bus and the
// tenant JWT secret. The same secret signs stream tokens so no extra
// key rotation surface is introduced by this feature.
func NewEventsHandler(bus events.Bus, jwtSecret string) *EventsHandler {
	return &EventsHandler{bus: bus, jwtSecret: jwtSecret}
}

// streamTokenTTL bounds how long a minted token is accepted by the SSE
// endpoint. 60s is enough for the browser to fetch the token and open
// the EventSource; short enough that a leaked token can't be replayed
// days later.
const streamTokenTTL = 60 * time.Second

// streamTokenType is the `typ` claim on stream tokens. Distinguishes
// them from regular tenant JWTs so the SSE endpoint won't accept a
// full-power tenant token in the query string (which would be a footgun
// for browser leakage — server logs, referer headers).
const streamTokenType = "stream"

// streamClaims is the payload of a stream token. Kept deliberately
// small; we only need tenant_id and the optional filter knobs.
type streamClaims struct {
	Typ      string       `json:"typ"`
	TenantID string       `json:"tenant_id"`
	Filter   streamFilter `json:"filter,omitempty"`
	Exp      int64        `json:"exp"`
}

// streamFilter is the on-the-wire shape baked into a stream token.
// Mirrors events.Filter minus TenantID (which is its own claim) so the
// client doesn't have to re-pass filter parameters when opening the
// EventSource.
type streamFilter struct {
	Kinds        []string `json:"kinds,omitempty"`
	ResourceType string   `json:"resource_type,omitempty"`
	ResourceID   string   `json:"resource_id,omitempty"`
	ScanID       string   `json:"scan_id,omitempty"`
}

// StreamTokenRequest is the JSON body of POST /api/v1/events/stream-tokens.
// Body is optional — an empty body mints a tenant-wide all-kinds token.
type StreamTokenRequest struct {
	Filter *streamFilter `json:"filter,omitempty"`
}

// StreamTokenResponse is the JSON payload returned after minting.
type StreamTokenResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// MintStreamToken is POST /api/v1/events/stream-tokens. Expects the
// caller to have already passed the tenant Auth + Tenant middleware, so
// middleware.GetClaims resolves a tenant_id. Body is optional — if
// present, the filter knobs are baked into the token and the SSE
// endpoint will use them as the subscription filter (additive to any
// query-string filters, which must be a subset).
func (h *EventsHandler) MintStreamToken(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil || claims.TenantID == "" {
		writeError(w, http.StatusUnauthorized, "missing tenant context")
		return
	}

	var req StreamTokenRequest
	// Body is optional; ignore decode errors on empty bodies.
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
	}

	exp := time.Now().Add(streamTokenTTL)
	sc := streamClaims{
		Typ:      streamTokenType,
		TenantID: claims.TenantID,
		Exp:      exp.Unix(),
	}
	if req.Filter != nil {
		sc.Filter = *req.Filter
	}
	token, err := signStreamToken(sc, h.jwtSecret)
	if err != nil {
		slog.Error("minting stream token", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to mint token")
		return
	}
	writeJSON(w, http.StatusOK, StreamTokenResponse{Token: token, ExpiresAt: exp})
}

// Stream is GET /api/v1/events/stream. Accepts auth in either of two
// forms:
//
//   - Regular tenant JWT in the Authorization header (for curl/CLI).
//     The standard Auth+Tenant middleware will already have set claims
//     on the context in this case.
//   - Stream token via ?token=<jwt> (for browser EventSource, which
//     can't attach custom headers).
//
// Query params kind, resource_type, resource_id, scan_id are optional
// additional filters on top of whatever the token's baked-in filter
// already enforces. We intersect both — a token that bakes in
// kind=scan_progress can't be used to subscribe to kind=audit.* just by
// changing the query string.
func (h *EventsHandler) Stream(w http.ResponseWriter, r *http.Request) {
	tenantID, tokenFilter, err := h.resolveStreamAuth(r)
	if err != nil {
		slog.Debug("events stream auth failed", "error", err)
		writeError(w, http.StatusUnauthorized, "invalid or missing credentials")
		return
	}

	// Layer the query-string filter on top of the token-baked filter.
	q := r.URL.Query()
	filter := events.Filter{
		TenantID:     tenantID,
		Kinds:        tokenFilter.Kinds,
		ResourceType: tokenFilter.ResourceType,
		ResourceID:   tokenFilter.ResourceID,
		ScanID:       tokenFilter.ScanID,
	}
	if raw := q.Get("kind"); raw != "" {
		reqKinds := splitCSV(raw)
		merged, ok := intersectKinds(filter.Kinds, reqKinds)
		if !ok {
			// Both sides provided non-empty sets and the intersection was
			// empty. Reject rather than silently delivering everything —
			// security invariant per the test comment.
			writeError(w, http.StatusForbidden, "kind query disjoint from token")
			return
		}
		filter.Kinds = merged
	}
	if v := q.Get("resource_type"); v != "" {
		if filter.ResourceType != "" && filter.ResourceType != v {
			writeError(w, http.StatusBadRequest, "resource_type query conflicts with token")
			return
		}
		filter.ResourceType = v
	}
	if v := q.Get("resource_id"); v != "" {
		if filter.ResourceID != "" && filter.ResourceID != v {
			writeError(w, http.StatusBadRequest, "resource_id query conflicts with token")
			return
		}
		filter.ResourceID = v
	}
	if v := q.Get("scan_id"); v != "" {
		if filter.ScanID != "" && filter.ScanID != v {
			writeError(w, http.StatusBadRequest, "scan_id query conflicts with token")
			return
		}
		filter.ScanID = v
	}

	// http.NewResponseController unwraps any middleware wrappers to reach
	// the underlying Flusher — a direct w.(http.Flusher) assertion fails
	// when logging/metrics middleware wraps the writer.
	rc := http.NewResponseController(w)

	// The server sets a 30s WriteTimeout for normal requests, but this stream
	// is long-lived. The write deadline is absolute from connection open, so the
	// SECOND heartbeat (~50s) lands past it — Flush errors, the handler returns,
	// and the client reconnects, flapping the "live" pill every ~50s. Clear the
	// write deadline so the stream isn't bounded by WriteTimeout. A writer that
	// can't unwrap to the underlying conn returns ErrNotSupported; log and
	// continue (no worse than the prior behavior) rather than drop the stream.
	if err := clearStreamWriteDeadline(rc); err != nil {
		slog.Warn("events stream: clearing write deadline failed; stream may drop at WriteTimeout",
			"error", err)
	}

	// SSE headers. Nginx, Cloud Run, and EventSource all expect these.
	h.writeSSEHeaders(w)
	if err := rc.Flush(); err != nil {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	ctx := r.Context()
	ch, unsub := h.bus.Subscribe(ctx, filter)
	defer unsub()

	// Heartbeat every 25s. Per the plan: proxies (nginx, Cloud Run, LB)
	// time connections out at ~60s of silence; 25s keeps us well under
	// both while not adding noticeable load.
	heartbeat := time.NewTicker(25 * time.Second)
	defer heartbeat.Stop()

	slog.Info("events stream opened",
		"tenant_id", tenantID,
		"kinds", filter.Kinds,
		"resource_type", filter.ResourceType,
		"resource_id", filter.ResourceID,
		"scan_id", filter.ScanID,
	)

	for {
		select {
		case <-ctx.Done():
			return
		case e, ok := <-ch:
			if !ok {
				return
			}
			if err := writeSSEEvent(w, e); err != nil {
				slog.Debug("sse write failed", "error", err)
				return
			}
			_ = rc.Flush()
		case <-heartbeat.C:
			// SSE comment lines start with ':'. Readable in curl -N as
			// `: ping` and ignored by EventSource, so the client never
			// sees them as events.
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			_ = rc.Flush()
		}
	}
}

// writeDeadlineClearer is the subset of *http.ResponseController used to lift
// the server WriteTimeout from the long-lived SSE stream. An interface so the
// behavior is unit-testable without a live server/TCP connection.
type writeDeadlineClearer interface {
	SetWriteDeadline(time.Time) error
}

// clearStreamWriteDeadline sets a zero (no) write deadline so a long-lived
// stream isn't cut off by the server's WriteTimeout. Returns the error so the
// caller can log and degrade rather than fail the stream.
func clearStreamWriteDeadline(rc writeDeadlineClearer) error {
	return rc.SetWriteDeadline(time.Time{})
}

// writeSSEHeaders sets the standard Server-Sent-Events headers. Split
// into its own method so tests can inspect the result without spinning
// up a real server + TCP connection.
func (h *EventsHandler) writeSSEHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// X-Accel-Buffering: no disables nginx's proxy buffering so events
	// reach the client promptly in our nginx → Cloud Run topology.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
}

// writeSSEEvent writes a single SSE frame. Format per spec:
//
//	event: <kind>
//	data: <json>
//	\n
//
// The trailing blank line is what terminates the frame; don't skip it.
func writeSSEEvent(w http.ResponseWriter, e events.Event) error {
	body, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("marshaling event: %w", err)
	}
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.Kind, body); err != nil {
		return err
	}
	return nil
}

// resolveStreamAuth accepts either:
//   - a tenant JWT in Authorization: Bearer <jwt> header (CLI/curl)
//   - a stream token in ?token=<jwt> (browser EventSource)
//
// The SSE endpoint is deliberately NOT behind the standard Auth/Tenant
// middleware because EventSource can't set headers. This handler does
// its own validation and returns the tenant_id + baked-in filter.
func (h *EventsHandler) resolveStreamAuth(r *http.Request) (string, streamFilter, error) {
	// Prefer stream token when present — it's the browser path and by
	// design is scoped narrower than the full tenant token.
	if qt := r.URL.Query().Get("token"); qt != "" {
		sc, err := verifyStreamToken(qt, h.jwtSecret)
		if err != nil {
			return "", streamFilter{}, fmt.Errorf("stream token: %w", err)
		}
		return sc.TenantID, sc.Filter, nil
	}

	// Fallback: regular tenant JWT, same validation the Auth middleware
	// does for other routes. Inlined here to avoid forcing every caller
	// to pass the middleware pipeline (we need a more flexible auth
	// surface for SSE).
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		return "", streamFilter{}, errors.New("missing authorization")
	}
	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) != 2 || parts[0] != "Bearer" {
		return "", streamFilter{}, errors.New("invalid authorization header")
	}
	claims, err := validateTenantJWT(parts[1], h.jwtSecret)
	if err != nil {
		return "", streamFilter{}, fmt.Errorf("tenant jwt: %w", err)
	}
	return claims.TenantID, streamFilter{}, nil
}

// --------------------------------------------------------------------
// Token mint/verify helpers.
//
// Stream tokens are tiny self-signed HS256 JWTs. We reuse the existing
// JWT_SECRET for signing rather than introducing a new rotation surface.
// The `typ` claim ("stream") is the guard: even though a stream token
// looks like a tenant JWT on the wire, the verify path only accepts
// typ=stream tokens on the SSE query-string path.
// --------------------------------------------------------------------

// jwtHeader is the constant header for HS256. Pre-encoded to cut out a
// per-mint allocation and ensure stable output.
var jwtHeader = base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))

func signStreamToken(sc streamClaims, secret string) (string, error) {
	payload, err := json.Marshal(sc)
	if err != nil {
		return "", err
	}
	body := jwtHeader + "." + base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return body + "." + sig, nil
}

// verifyStreamToken parses + validates a stream token, requiring typ=stream.
func verifyStreamToken(token, secret string) (*streamClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("malformed token")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(parts[0] + "." + parts[1]))
	expect := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(parts[2]), []byte(expect)) {
		return nil, errors.New("bad signature")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	var sc streamClaims
	if err := json.Unmarshal(raw, &sc); err != nil {
		return nil, fmt.Errorf("payload: %w", err)
	}
	if sc.Typ != streamTokenType {
		return nil, errors.New("wrong typ")
	}
	if sc.TenantID == "" {
		return nil, errors.New("missing tenant_id")
	}
	if sc.Exp > 0 && time.Now().Unix() > sc.Exp {
		return nil, errors.New("expired")
	}
	return &sc, nil
}

// tenantJWTClaims is a trimmed mirror of middleware.Claims — we only
// need tenant_id here and don't want this handler package to depend on
// internal middleware guts.
type tenantJWTClaims struct {
	TenantID string `json:"tenant_id"`
	Iss      string `json:"iss,omitempty"`
	Aud      string `json:"aud,omitempty"`
	Exp      int64  `json:"exp"`
}

// validateTenantJWT mirrors the HS256 validator in middleware.Auth. We
// don't reuse that middleware directly because it applies to the whole
// request (including the body), and the SSE handler has its own
// specialised flow.
func validateTenantJWT(token, secret string) (*tenantJWTClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("malformed token")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(parts[0] + "." + parts[1]))
	expect := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(parts[2]), []byte(expect)) {
		return nil, errors.New("bad signature")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	var c tenantJWTClaims
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("payload: %w", err)
	}
	if c.TenantID == "" {
		return nil, errors.New("missing tenant_id")
	}
	if c.Exp > 0 && time.Now().Unix() > c.Exp {
		return nil, errors.New("expired")
	}
	// Mirror middleware.Auth's iss/aud gate so admin tokens can't be
	// replayed against tenant routes.
	const (
		expectedIssuer   = "silkstrand-backoffice"
		expectedAudience = "silkstrand-tenant-api"
	)
	if c.Iss != "" && c.Iss != expectedIssuer {
		return nil, errors.New("bad iss")
	}
	if c.Aud != "" && c.Aud != expectedAudience {
		return nil, errors.New("bad aud")
	}
	return &c, nil
}

// --------------------------------------------------------------------
// utilities
// --------------------------------------------------------------------

// splitCSV splits "a,b, c" into []string{"a", "b", "c"}, trimming spaces
// and dropping empties. Used for the `kind` query param.
func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := parts[:0]
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// intersectKinds returns the intersection of a (token-baked) and b
// (query-string) kind allowlists, and a boolean that distinguishes
// "no filter (both empty)" from "disjoint (both non-empty, intersection
// empty)". Semantics:
//
//   - Either side empty → the other wins (empty = "all allowed").
//     Returns (other, true).
//   - Both non-empty with overlap → returns (overlap, true).
//   - Both non-empty with no overlap → returns (nil, false). Caller
//     must reject the request; delivering "" as "no filter" would let
//     an attacker widen a narrow token's scope.
func intersectKinds(a, b []string) ([]string, bool) {
	if len(a) == 0 {
		return b, true
	}
	if len(b) == 0 {
		return a, true
	}
	set := make(map[string]struct{}, len(a))
	for _, k := range a {
		set[k] = struct{}{}
	}
	var out []string
	for _, k := range b {
		if _, ok := set[k]; ok {
			out = append(out, k)
		}
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}
