package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestFetchAgentVersion(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		want    string
		wantErr bool
	}{
		{name: "valid version trimmed", status: 200, body: "0.1.52\n", want: "0.1.52"},
		{name: "valid v-prefixed release tag", status: 200, body: "v0.1.52\n", want: "v0.1.52"},
		{name: "valid with build metadata", status: 200, body: "1.2.3-rc.1+abc ", want: "1.2.3-rc.1+abc"},
		{name: "empty body is error", status: 200, body: "   \n", wantErr: true},
		{name: "junk/html is rejected", status: 200, body: "<html>nope</html>", wantErr: true},
		{name: "non-200 is error", status: 404, body: "not found", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/latest/VERSION" {
					t.Errorf("unexpected path %q", r.URL.Path)
				}
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()

			got, err := fetchAgentVersion(context.Background(), srv.Client(), srv.URL)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got version %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("version = %q, want %q", got, tt.want)
			}
		})
	}
}

// latestAgentVersion must degrade to "latest" (prior behavior) when the manifest
// is unavailable, and must serve a real version from cache.
func TestLatestAgentVersionFallbackAndCache(t *testing.T) {
	t.Run("fallback to latest on error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer srv.Close()
		h := &AgentsHandler{httpClient: &http.Client{Timeout: 2 * time.Second}}
		if got := h.latestAgentVersion(context.Background(), srv.URL); got != "latest" {
			t.Errorf("got %q, want \"latest\" fallback", got)
		}
	})

	t.Run("serves real version and caches it", func(t *testing.T) {
		var hits int
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits++
			_, _ = w.Write([]byte("0.2.0"))
		}))
		defer srv.Close()
		h := &AgentsHandler{httpClient: &http.Client{Timeout: 2 * time.Second}}
		if got := h.latestAgentVersion(context.Background(), srv.URL); got != "0.2.0" {
			t.Fatalf("got %q, want 0.2.0", got)
		}
		// Second call should hit the cache, not the server.
		if got := h.latestAgentVersion(context.Background(), srv.URL); got != "0.2.0" {
			t.Fatalf("cached got %q, want 0.2.0", got)
		}
		if hits != 1 {
			t.Errorf("server hit %d times, want 1 (second call should be cached)", hits)
		}
	})
}
