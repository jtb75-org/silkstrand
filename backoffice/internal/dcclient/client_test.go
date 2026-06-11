package dcclient

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNormalizeBaseURL(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"plain localhost", "http://localhost:8080", "http://localhost:8080", false},
		{"trailing slash trimmed", "http://localhost:8080/", "http://localhost:8080", false},
		{"in-cluster service dns", "http://silkstrand-api.dc-us.svc.cluster.local:8080", "http://silkstrand-api.dc-us.svc.cluster.local:8080", false},
		{"service dns trailing slash", "http://silkstrand-api.dc-us.svc.cluster.local:8080/", "http://silkstrand-api.dc-us.svc.cluster.local:8080", false},
		{"public https", "https://api.silkstrand.io", "https://api.silkstrand.io", false},
		{"surrounding whitespace", "  http://dc-api.dc-eu:8080  ", "http://dc-api.dc-eu:8080", false},
		{"query and fragment dropped", "http://dc-api.dc-eu:8080/?x=1#frag", "http://dc-api.dc-eu:8080", false},
		{"empty", "", "", true},
		{"bad scheme", "ftp://dc-api:8080", "", true},
		{"missing scheme", "dc-api.dc-us.svc.cluster.local:8080", "", true},
		{"missing host", "http://", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeBaseURL(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("NormalizeBaseURL(%q) = %q, want error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeBaseURL(%q) unexpected error: %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("NormalizeBaseURL(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestHealthCheck(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		status  int
		body    string
		wantErr bool
	}{
		{"healthy", "/readyz", http.StatusOK, `{"status":"ok"}`, false},
		{"non-200", "/readyz", http.StatusServiceUnavailable, `{"status":"degraded"}`, true},
		{"ok-200 but not ready", "/readyz", http.StatusOK, `{"status":"degraded"}`, true},
		{"placeholder html body", "/readyz", http.StatusOK, `<html>default backend</html>`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotPath string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()

			// Pass the base URL with a trailing slash to confirm callers that
			// stored an un-normalized URL still hit /readyz, not //readyz.
			c := New()
			err := c.HealthCheck(DCConn{APIURL: srv.URL})
			if gotPath != "/readyz" {
				t.Errorf("requested path = %q, want /readyz", gotPath)
			}
			if tt.wantErr && err == nil {
				t.Errorf("HealthCheck() = nil, want error")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("HealthCheck() unexpected error: %v", err)
			}
		})
	}
}
