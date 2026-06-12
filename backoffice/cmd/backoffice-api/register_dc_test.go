package main

import "testing"

func TestNormalizeAPIURL(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"in-cluster service dns", "http://silkstrand-api.dc-us.svc.cluster.local:8080", "http://silkstrand-api.dc-us.svc.cluster.local:8080", false},
		{"trailing slash trimmed", "http://silkstrand-api.dc-us.svc.cluster.local:8080/", "http://silkstrand-api.dc-us.svc.cluster.local:8080", false},
		{"public https", "https://api.silkstrand.io", "https://api.silkstrand.io", false},
		{"surrounding whitespace", "  http://dc-api.dc-eu:8080  ", "http://dc-api.dc-eu:8080", false},
		{"query and fragment dropped", "http://dc-api.dc-eu:8080/?x=1#f", "http://dc-api.dc-eu:8080", false},
		{"empty", "", "", true},
		{"bad scheme", "ftp://dc-api:8080", "", true},
		{"missing scheme", "dc-api.dc-us.svc.cluster.local:8080", "", true},
		{"missing host", "http://", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeAPIURL(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("normalizeAPIURL(%q) = %q, want error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeAPIURL(%q) unexpected error: %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("normalizeAPIURL(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
