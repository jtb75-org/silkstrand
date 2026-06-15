package middleware

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const tenantClaimsKey contextKey = "tenant_claims"

// TenantClaims is the JWT payload issued to tenant end users (as opposed to
// backoffice admins, which use AdminClaims). The same HS256 secret is shared
// with every DC API, which validates the token to scope requests to the
// correct tenant.
type TenantClaims struct {
	Sub        string `json:"sub"`          // user UUID
	Email      string `json:"email"`
	TenantID   string `json:"tenant_id"`    // DC-side tenant UUID — what the DC validates
	BoTenantID string `json:"bo_tenant_id"` // Backoffice-side tenant UUID — for switch-org and UI routing
	DCID       string `json:"dc_id"`        // active tenant's DC
	Role       string `json:"role"`         // "admin" or "member"
	Iss        string `json:"iss,omitempty"`
	Aud        string `json:"aud,omitempty"`
	Iat        int64  `json:"iat"`
	Exp        int64  `json:"exp"`
}

// CreateTenantJWT issues a tenant-scoped token. Expiry is 1h by design; the
// frontend calls /tenant-auth/switch-org or refreshes on expiry.
func CreateTenantJWT(secret, userID, email, tenantID, boTenantID, dcID, role string, expiry time.Duration) (string, error) {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	now := time.Now()
	claims := TenantClaims{
		Sub:        userID,
		Email:      email,
		TenantID:   tenantID,
		BoTenantID: boTenantID,
		DCID:       dcID,
		Role:       role,
		Iss:        BackofficeIssuer,
		Aud:        TenantAudience,
		Iat:        now.Unix(),
		Exp:        now.Add(expiry).Unix(),
	}
	body, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(body)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(header + "." + payload))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return header + "." + payload + "." + sig, nil
}

// ValidateTenantJWT verifies the signature and expiry and returns the claims.
func ValidateTenantJWT(token, secret string) (*TenantClaims, error) {
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
	body, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, errInvalidToken
	}
	var claims TenantClaims
	if err := json.Unmarshal(body, &claims); err != nil {
		return nil, errInvalidToken
	}
	if claims.Exp > 0 && time.Now().Unix() > claims.Exp {
		return nil, errTokenExpired
	}
	if claims.Sub == "" || claims.TenantID == "" {
		return nil, errInvalidToken
	}
	// iss/aud validation (audit 3.4). Transitional: accept tokens that
	// predate the iss/aud rollout (tenant tokens have a 1h lifetime), but
	// reject any token that carries an explicit mismatch.
	if claims.Iss != "" && claims.Iss != BackofficeIssuer {
		return nil, errInvalidToken
	}
	if claims.Aud != "" && claims.Aud != TenantAudience {
		return nil, errInvalidToken
	}
	return &claims, nil
}

// TenantAuth middleware validates TenantClaims and stores them in context.
// Use this for /api/v1/tenant-auth/me, /switch-org, and (later) any
// backoffice-hosted tenant-scoped endpoints.
func TenantAuth(secret string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authHeader := r.Header.Get("Authorization")
			if authHeader == "" {
				writeJSONErr(w, http.StatusUnauthorized, "missing authorization header")
				return
			}
			parts := strings.SplitN(authHeader, " ", 2)
			if len(parts) != 2 || parts[0] != "Bearer" {
				writeJSONErr(w, http.StatusUnauthorized, "invalid authorization header")
				return
			}
			claims, err := ValidateTenantJWT(parts[1], secret)
			if err != nil {
				writeJSONErr(w, http.StatusUnauthorized, "invalid token")
				return
			}
			ctx := context.WithValue(r.Context(), tenantClaimsKey, claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// GetTenantClaims retrieves tenant claims from context (nil if unauthenticated).
func GetTenantClaims(ctx context.Context) *TenantClaims {
	v, _ := ctx.Value(tenantClaimsKey).(*TenantClaims)
	return v
}

// SetTenantClaims stores tenant claims in the context (mirrors SetAdminClaims;
// used by handler tests to simulate an authenticated tenant request).
func SetTenantClaims(ctx context.Context, claims *TenantClaims) context.Context {
	return context.WithValue(ctx, tenantClaimsKey, claims)
}

func writeJSONErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	fmt.Fprintf(w, `{"error":%q}`, msg)
}
