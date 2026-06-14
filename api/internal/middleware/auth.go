package middleware

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/jtb75/silkstrand/api/internal/audit"
)

// Auth validates HS256-signed tenant JWTs issued by the backoffice. The
// backoffice signs with TENANT_JWT_SECRET; each DC validates with the same
// value (passed as JWT_SECRET env var here to preserve existing naming).
func Auth(jwtSecret string) func(http.Handler) http.Handler {
	slog.Info("auth mode: HS256 (tenant JWT)")
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authHeader := r.Header.Get("Authorization")
			if authHeader == "" {
				http.Error(w, `{"error":"missing authorization header"}`, http.StatusUnauthorized)
				return
			}
			parts := strings.SplitN(authHeader, " ", 2)
			if len(parts) != 2 || parts[0] != "Bearer" {
				http.Error(w, `{"error":"invalid authorization header"}`, http.StatusUnauthorized)
				return
			}
			claims, err := validateHMACJWT(parts[1], jwtSecret)
			if err != nil {
				slog.Debug("token validation failed", "error", err)
				http.Error(w, `{"error":"invalid token"}`, http.StatusUnauthorized)
				return
			}
			ctx := SetClaims(r.Context(), claims)
			// Stash request-scoped audit metadata so audit.Writer.Emit can
			// enrich payloads with ip/user_agent/actor_email without per-site
			// churn. Empty values are dropped inside WithRequestMetadata.
			ctx = audit.WithRequestMetadata(ctx, clientIP(r), r.UserAgent(), claims.Email)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// clientIP returns the originating client IP. traefik/cloudflared sit in front,
// so honor the first hop of X-Forwarded-For when present; otherwise fall back to
// the connection's RemoteAddr host (stripping the port).
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i >= 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// Expected JWT iss/aud values for tenant tokens (audit 3.4). Matches the
// backoffice's BackofficeIssuer / TenantAudience constants.
const (
	expectedIssuer   = "silkstrand-backoffice"
	expectedAudience = "silkstrand-tenant-api"
)

// Claims is the tenant-scoped JWT payload. Compatible with both the
// backoffice's new TenantClaims shape (sub + tenant_id) and the legacy
// shape (user_id + tenant_id) so dev tools that still produce the older
// layout keep working.
type Claims struct {
	Sub      string `json:"sub"`
	UserID   string `json:"user_id"`
	TenantID string `json:"tenant_id"`
	DCID     string `json:"dc_id"`
	Role     string `json:"role"`
	Email    string `json:"email"`
	Iss      string `json:"iss,omitempty"`
	Aud      string `json:"aud,omitempty"`
	Exp      int64  `json:"exp"`
}

// validateHMACJWT validates a JWT signed with HMAC-SHA256 and returns its
// claims. Requires a non-empty tenant_id claim.
func validateHMACJWT(token, secret string) (*Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errInvalidToken
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(parts[0] + "." + parts[1]))
	expectedSig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(parts[2]), []byte(expectedSig)) {
		return nil, errInvalidToken
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, errInvalidToken
	}

	var claims Claims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, errInvalidToken
	}

	if claims.Exp > 0 && time.Now().Unix() > claims.Exp {
		return nil, errTokenExpired
	}
	if claims.TenantID == "" {
		return nil, errInvalidToken
	}
	// iss/aud validation (audit 3.4). Transitional: accept tokens that
	// predate the iss/aud rollout (tenant tokens have a 1h lifetime),
	// but reject any token that carries an explicit mismatch — that
	// blocks an admin token from being replayed against a tenant route.
	if claims.Iss != "" && claims.Iss != expectedIssuer {
		return nil, errInvalidToken
	}
	if claims.Aud != "" && claims.Aud != expectedAudience {
		return nil, errInvalidToken
	}
	return &claims, nil
}

type errType string

func (e errType) Error() string { return string(e) }

const (
	errInvalidToken errType = "invalid token"
	errTokenExpired errType = "token expired"
)
