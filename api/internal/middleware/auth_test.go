package middleware

import (
	"net/http"
	"testing"
)

// TestClientIP covers the X-Forwarded-For first-hop preference (traefik/
// cloudflared sit in front) and the RemoteAddr port-stripping fallback.
func TestClientIP(t *testing.T) {
	tests := []struct {
		name       string
		remoteAddr string
		xff        string
		want       string
	}{
		{"xff first hop wins", "10.0.0.1:5000", "203.0.113.7, 70.41.3.18, 150.172.238.178", "203.0.113.7"},
		{"xff single value", "10.0.0.1:5000", "203.0.113.9", "203.0.113.9"},
		{"xff trims whitespace", "10.0.0.1:5000", "  203.0.113.5 , 1.2.3.4", "203.0.113.5"},
		{"no xff strips port", "192.0.2.4:44321", "", "192.0.2.4"},
		{"no xff bare address", "192.0.2.9", "", "192.0.2.9"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &http.Request{RemoteAddr: tt.remoteAddr, Header: http.Header{}}
			if tt.xff != "" {
				r.Header.Set("X-Forwarded-For", tt.xff)
			}
			if got := clientIP(r); got != tt.want {
				t.Errorf("clientIP() = %q, want %q", got, tt.want)
			}
		})
	}
}
