package handler

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"testing"
)

func buildTestTarball(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	for name, content := range files {
		hdr := &tar.Header{
			Name: name,
			Mode: 0o644,
			Size: int64(len(content)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("writing tar header for %s: %v", name, err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("writing tar content for %s: %v", name, err)
		}
	}
	tw.Close()
	gw.Close()
	return buf.Bytes()
}

func TestParseBundleTarball(t *testing.T) {
	tarball := buildTestTarball(t, map[string]string{
		"bundle.yaml": `id: cis-postgresql-16
name: CIS PostgreSQL 16 Benchmark
version: 2.0.4
framework: cis-postgresql-16
engine: postgresql
controls:
  - pg-tls-enabled
  - pg-log-connections
`,
		"controls/pg-tls-enabled/control.yaml": `id: "6.8"
title: Ensure TLS is enabled and configured correctly
section: PostgreSQL Settings
severity: HIGH
`,
		"controls/pg-log-connections/control.yaml": `id: "3.1.20"
title: Ensure log_connections is enabled
section: Logging
severity: MEDIUM
`,
	})

	manifest, controlFiles, err := parseBundleTarball(tarball)
	if err != nil {
		t.Fatalf("parseBundleTarball: %v", err)
	}

	if manifest.ID != "cis-postgresql-16" {
		t.Errorf("manifest.ID = %q, want cis-postgresql-16", manifest.ID)
	}
	if manifest.Name != "CIS PostgreSQL 16 Benchmark" {
		t.Errorf("manifest.Name = %q, want CIS PostgreSQL 16 Benchmark", manifest.Name)
	}
	if manifest.Version != "2.0.4" {
		t.Errorf("manifest.Version = %q, want 2.0.4", manifest.Version)
	}
	if manifest.Engine != "postgresql" {
		t.Errorf("manifest.Engine = %q, want postgresql", manifest.Engine)
	}
	if len(manifest.Controls) != 2 {
		t.Fatalf("manifest.Controls length = %d, want 2", len(manifest.Controls))
	}
	if manifest.Controls[0] != "pg-tls-enabled" {
		t.Errorf("manifest.Controls[0] = %q, want pg-tls-enabled", manifest.Controls[0])
	}

	if len(controlFiles) != 2 {
		t.Fatalf("controlFiles length = %d, want 2", len(controlFiles))
	}

	tls := controlFiles["pg-tls-enabled"]
	if tls == nil {
		t.Fatal("controlFiles missing pg-tls-enabled")
	}
	if tls.Title != "Ensure TLS is enabled and configured correctly" {
		t.Errorf("tls.Title = %q", tls.Title)
	}
	if tls.Severity != "HIGH" {
		t.Errorf("tls.Severity = %q, want HIGH", tls.Severity)
	}
	if tls.Section != "PostgreSQL Settings" {
		t.Errorf("tls.Section = %q, want PostgreSQL Settings", tls.Section)
	}
}

func TestParseBundleTarball_LegacyFormat(t *testing.T) {
	tarball := buildTestTarball(t, map[string]string{
		"bundle.yaml": `id: cis-mssql-2022
name: CIS MSSQL 2022
version: 1.0.0
framework: cis-mssql-2022
engine: mssql
controls:
  - mssql-ad-hoc-distributed-queries
`,
		"content/controls/2.1-ad-hoc-distributed-queries.yaml": `id: "2.1"
title: Ensure Ad Hoc Distributed Queries is disabled
section: Surface Area Reduction
severity: MEDIUM
`,
	})

	manifest, controlFiles, err := parseBundleTarball(tarball)
	if err != nil {
		t.Fatalf("parseBundleTarball: %v", err)
	}

	if manifest.ID != "cis-mssql-2022" {
		t.Errorf("manifest.ID = %q", manifest.ID)
	}

	// Legacy control should be keyed by filename stem.
	cm, ok := controlFiles["2.1-ad-hoc-distributed-queries"]
	if !ok {
		t.Fatal("missing legacy control file entry")
	}
	if cm.Title != "Ensure Ad Hoc Distributed Queries is disabled" {
		t.Errorf("cm.Title = %q", cm.Title)
	}
}

func TestParseBundleTarball_NoBundleYaml(t *testing.T) {
	tarball := buildTestTarball(t, map[string]string{
		"README.md": "nothing here",
	})
	_, _, err := parseBundleTarball(tarball)
	if err == nil {
		t.Fatal("expected error for missing bundle.yaml")
	}
}

func TestBuildControlRows(t *testing.T) {
	manifest := &bundleManifest{
		ID:     "test-bundle",
		Engine: "postgresql",
		Controls: []string{
			"pg-tls-enabled",
			"pg-unknown-control",
		},
	}
	controlFiles := map[string]*controlManifest{
		"pg-tls-enabled": {
			ID:       "6.8",
			Title:    "Ensure TLS is enabled",
			Section:  "Network",
			Severity: "HIGH",
		},
	}

	rows := buildControlRows("bundle-uuid", manifest, controlFiles)
	if len(rows) != 2 {
		t.Fatalf("len(rows) = %d, want 2", len(rows))
	}

	// First control should have metadata from the control.yaml.
	if rows[0].Name != "Ensure TLS is enabled" {
		t.Errorf("rows[0].Name = %q, want Ensure TLS is enabled", rows[0].Name)
	}
	sev := "high"
	if rows[0].Severity == nil || *rows[0].Severity != sev {
		t.Errorf("rows[0].Severity = %v, want %q", rows[0].Severity, sev)
	}

	// Second control has no matching yaml — should use control ID as name.
	if rows[1].Name != "pg-unknown-control" {
		t.Errorf("rows[1].Name = %q, want pg-unknown-control", rows[1].Name)
	}
}

func TestBundlePublicURL(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		bundle  string
		version string
		want    string
	}{
		{
			name:    "base set -> public tarball URL (gcs_path)",
			baseURL: "https://downloads.silkstrand.io/agent/bundles",
			bundle:  "cis-postgresql-16",
			version: "1.0.0",
			want:    "https://downloads.silkstrand.io/agent/bundles/cis-postgresql-16-1.0.0.tar.gz",
		},
		{
			name:    "trailing slash on base is tolerated",
			baseURL: "https://downloads.silkstrand.io/agent/bundles/",
			bundle:  "cis-mssql-2022",
			version: "2.1.0",
			want:    "https://downloads.silkstrand.io/agent/bundles/cis-mssql-2022-2.1.0.tar.gz",
		},
		{
			name:    "no base -> empty (gcs_path stays NULL, local-only)",
			baseURL: "",
			bundle:  "cis-mongodb-8",
			version: "1.0.0",
			want:    "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := bundlePublicURL(tc.baseURL, tc.bundle, tc.version); got != tc.want {
				t.Errorf("bundlePublicURL(%q, %q, %q) = %q, want %q",
					tc.baseURL, tc.bundle, tc.version, got, tc.want)
			}
		})
	}
}
