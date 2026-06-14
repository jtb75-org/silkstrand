package recon

import (
	"encoding/json"
	"testing"
)

func TestClassifyTemplate(t *testing.T) {
	tests := []struct {
		id          string
		wantService string
		wantHTTP    bool
	}{
		{"mssql-detect", "mssql", false},
		{"postgres-detect", "postgresql", false},
		{"redis-detect", "redis", false},
		{"rdp-detection", "rdp", false},
		{"http-detect", "http", true},   // web → httpx owns it
		{"https-detect", "https", true}, // web → httpx owns it
		// Unmapped community templates fall back to the stripped id, non-web.
		{"maverick-ssh-detect", "maverick-ssh", false},
		{"weblogic-t3-detect", "weblogic-t3", false},
		{"some-new-service-detection", "some-new-service", false},
		{"WeirdCase-Detect", "weirdcase", false},
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			svc, http := classifyTemplate(tt.id)
			if svc != tt.wantService || http != tt.wantHTTP {
				t.Errorf("classifyTemplate(%q) = (%q,%v), want (%q,%v)", tt.id, svc, http, tt.wantService, tt.wantHTTP)
			}
		})
	}
}

func TestClassifyPort(t *testing.T) {
	hit := func(id string, extracted ...string) NucleiNetworkHit {
		return NucleiNetworkHit{TemplateID: id, Extracted: extracted}
	}
	tests := []struct {
		name        string
		hits        []NucleiNetworkHit
		wantFill    bool
		wantService string
		wantVersion string
	}{
		{"no hits", nil, false, "", ""},
		{
			"non-HTTP only backfills with version",
			[]NucleiNetworkHit{hit("mssql-detect", "Microsoft SQL Server 2022")},
			true, "mssql", "Microsoft SQL Server 2022",
		},
		{
			"non-HTTP no version",
			[]NucleiNetworkHit{hit("ssh-detect")},
			true, "ssh", "",
		},
		{
			"HTTP-family only → httpx owns it, no backfill",
			[]NucleiNetworkHit{hit("http-detect")},
			false, "", "",
		},
		{
			// duplicate/conflicting hits on one port → web-precedence wins.
			"BOTH http-family and non-HTTP → no backfill",
			[]NucleiNetworkHit{hit("mssql-detect", "MSSQL 2022"), hit("http-detect")},
			false, "", "",
		},
		{
			"two non-HTTP hits → first usable wins",
			[]NucleiNetworkHit{hit("redis-detect", "7.2.0"), hit("ssh-detect", "OpenSSH 9")},
			true, "redis", "7.2.0",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := classifyPort(tt.hits)
			if d.backfill != tt.wantFill || d.service != tt.wantService || d.version != tt.wantVersion {
				t.Errorf("classifyPort = {fill:%v svc:%q ver:%q}, want {fill:%v svc:%q ver:%q}",
					d.backfill, d.service, d.version, tt.wantFill, tt.wantService, tt.wantVersion)
			}
		})
	}
}

func TestParseExtracted(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []string
	}{
		{"array", `["Microsoft SQL Server 2022"]`, []string{"Microsoft SQL Server 2022"}},
		{"bare string", `"OpenSSH 9.6"`, []string{"OpenSSH 9.6"}},
		{"empty array", `[]`, []string{}},
		{"absent", ``, nil},
		{"null", `null`, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseExtracted(json.RawMessage(tt.raw))
			if len(got) != len(tt.want) {
				t.Fatalf("parseExtracted(%s) = %v, want %v", tt.raw, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("parseExtracted(%s)[%d] = %q, want %q", tt.raw, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestFirstExtracted(t *testing.T) {
	if got := firstExtracted([]string{"  ", "", "v1.2"}); got != "v1.2" {
		t.Errorf("firstExtracted = %q, want v1.2", got)
	}
	if got := firstExtracted(nil); got != "" {
		t.Errorf("firstExtracted(nil) = %q, want empty", got)
	}
}
