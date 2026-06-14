package audit

import "context"

// contextKey is an unexported type for request-scoped audit metadata keys, so
// these values can't collide with context keys set by other packages.
type contextKey int

const (
	contextKeyIP contextKey = iota
	contextKeyUserAgent
	contextKeyActorEmail
)

// WithRequestMetadata returns a child context carrying request-scoped audit
// metadata (client IP, User-Agent, actor email). Empty values are not stored,
// so absent fields are simply omitted from enriched payloads — background /
// system events (rule.fired, scheduler fetches, agent lifecycle) that never set
// them stay null by design. Typically called once per request by auth middleware.
func WithRequestMetadata(ctx context.Context, ip, userAgent, actorEmail string) context.Context {
	if ip != "" {
		ctx = context.WithValue(ctx, contextKeyIP, ip)
	}
	if userAgent != "" {
		ctx = context.WithValue(ctx, contextKeyUserAgent, userAgent)
	}
	if actorEmail != "" {
		ctx = context.WithValue(ctx, contextKeyActorEmail, actorEmail)
	}
	return ctx
}

func ipFromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(contextKeyIP).(string)
	return v, ok && v != ""
}

func userAgentFromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(contextKeyUserAgent).(string)
	return v, ok && v != ""
}

func actorEmailFromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(contextKeyActorEmail).(string)
	return v, ok && v != ""
}

// enrich merges request-scoped metadata (ip, user_agent, actor_email) from ctx
// into the event's payload at persist time. It:
//   - never overwrites a key a call site set explicitly;
//   - never mutates the caller's payload map (works on a copy);
//   - is a no-op when ctx carries none of the values, so background/system
//     events keep those keys absent rather than null/empty (and never panic on
//     a bare context).
func enrich(ctx context.Context, e Event) Event {
	ip, hasIP := ipFromContext(ctx)
	ua, hasUA := userAgentFromContext(ctx)
	email, hasEmail := actorEmailFromContext(ctx)
	if !hasIP && !hasUA && !hasEmail {
		return e
	}

	merged := make(map[string]any, len(e.Payload)+3)
	for k, v := range e.Payload {
		merged[k] = v
	}
	setIfAbsent := func(present bool, key, val string) {
		if !present {
			return
		}
		if _, exists := merged[key]; exists {
			return
		}
		merged[key] = val
	}
	setIfAbsent(hasIP, "ip", ip)
	setIfAbsent(hasUA, "user_agent", ua)
	setIfAbsent(hasEmail, "actor_email", email)

	e.Payload = merged
	return e
}
