package runner

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestPythonRunner_Run(t *testing.T) {
	// Skip if python3 is not available
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}

	_, thisFile, _, _ := runtime.Caller(0)
	testdataDir := filepath.Join(filepath.Dir(thisFile), "testdata", "test-bundle")

	manifest, err := LoadManifest(testdataDir)
	if err != nil {
		t.Fatalf("loading manifest: %v", err)
	}

	if manifest.Name != "test-bundle" {
		t.Errorf("manifest.Name = %q, want %q", manifest.Name, "test-bundle")
	}
	if manifest.Entrypoint != "content/checks.py" {
		t.Errorf("manifest.Entrypoint = %q, want %q", manifest.Entrypoint, "content/checks.py")
	}

	runner := NewPythonRunner()
	targetConfig := json.RawMessage(`{"type": "database", "identifier": "localhost:5432"}`)

	results, err := runner.Run(context.Background(), RunRequest{
		BundlePath:   testdataDir,
		Manifest:     manifest,
		TargetConfig: targetConfig,
	})
	if err != nil {
		t.Fatalf("run error: %v", err)
	}

	// Validate results structure
	var parsed map[string]interface{}
	if err := json.Unmarshal(results, &parsed); err != nil {
		t.Fatalf("results not valid JSON: %v", err)
	}

	if parsed["schema_version"] != "1" {
		t.Errorf("schema_version = %v, want %q", parsed["schema_version"], "1")
	}
	if parsed["status"] != "completed" {
		t.Errorf("status = %v, want %q", parsed["status"], "completed")
	}

	summary, ok := parsed["summary"].(map[string]interface{})
	if !ok {
		t.Fatal("missing summary in results")
	}
	if summary["total"] != float64(1) {
		t.Errorf("summary.total = %v, want 1", summary["total"])
	}
	if summary["pass"] != float64(1) {
		t.Errorf("summary.pass = %v, want 1", summary["pass"])
	}
}

func TestLoadManifest(t *testing.T) {
	_, thisFile, _, _ := runtime.Caller(0)
	testdataDir := filepath.Join(filepath.Dir(thisFile), "testdata", "test-bundle")

	m, err := LoadManifest(testdataDir)
	if err != nil {
		t.Fatalf("loading manifest: %v", err)
	}

	if m.Name != "test-bundle" {
		t.Errorf("Name = %q, want %q", m.Name, "test-bundle")
	}
	if m.Version != "1.0.0" {
		t.Errorf("Version = %q, want %q", m.Version, "1.0.0")
	}
	if m.Framework != "python" {
		t.Errorf("Framework = %q, want %q", m.Framework, "python")
	}
	if m.TargetType != "database" {
		t.Errorf("TargetType = %q, want %q", m.TargetType, "database")
	}
	if m.Entrypoint != "content/checks.py" {
		t.Errorf("Entrypoint = %q, want %q", m.Entrypoint, "content/checks.py")
	}
	if m.Benchmark.CISID != "CIS_TEST" {
		t.Errorf("Benchmark.CISID = %q, want %q", m.Benchmark.CISID, "CIS_TEST")
	}
}

func TestLoadManifest_MissingEntrypoint(t *testing.T) {
	tmpDir := t.TempDir()
	data := []byte("name: test\nversion: 1.0.0\nframework: python\n")
	if err := writeTestFile(tmpDir, "manifest.yaml", data); err != nil {
		t.Fatal(err)
	}

	_, err := LoadManifest(tmpDir)
	if err == nil {
		t.Fatal("expected error for missing entrypoint")
	}
}

func TestLoadManifest_MissingFile(t *testing.T) {
	_, err := LoadManifest(t.TempDir())
	if err == nil {
		t.Fatal("expected error for missing manifest file")
	}
}

// --- BundleManifest (ADR 010) tests ---

func TestReadBundleManifest(t *testing.T) {
	tmpDir := t.TempDir()
	data := []byte(`id: cis-postgresql-16
name: CIS PostgreSQL 16
version: 2.0.4
framework: cis-postgresql-16
engine: postgresql
vendor_dir: content/vendor
controls:
  - pg-tls-enabled
  - pg-log-connections
`)
	if err := writeTestFile(tmpDir, "bundle.yaml", data); err != nil {
		t.Fatal(err)
	}

	m, err := ReadBundleManifest(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m == nil {
		t.Fatal("expected non-nil manifest")
	}
	if m.ID != "cis-postgresql-16" {
		t.Errorf("ID = %q, want %q", m.ID, "cis-postgresql-16")
	}
	if m.Engine != "postgresql" {
		t.Errorf("Engine = %q, want %q", m.Engine, "postgresql")
	}
	if m.VendorDir != "content/vendor" {
		t.Errorf("VendorDir = %q, want %q", m.VendorDir, "content/vendor")
	}
	if len(m.Controls) != 2 {
		t.Fatalf("Controls len = %d, want 2", len(m.Controls))
	}
	if m.Controls[0] != "pg-tls-enabled" {
		t.Errorf("Controls[0] = %q, want %q", m.Controls[0], "pg-tls-enabled")
	}
}

func TestReadBundleManifest_Missing(t *testing.T) {
	m, err := ReadBundleManifest(t.TempDir())
	if err != nil {
		t.Fatalf("expected nil error for missing bundle.yaml, got: %v", err)
	}
	if m != nil {
		t.Fatal("expected nil manifest for missing bundle.yaml")
	}
}

func TestReadBundleManifest_NoControls(t *testing.T) {
	tmpDir := t.TempDir()
	data := []byte(`id: empty
name: Empty
version: 1.0.0
framework: test
engine: test
controls: []
`)
	if err := writeTestFile(tmpDir, "bundle.yaml", data); err != nil {
		t.Fatal(err)
	}

	_, err := ReadBundleManifest(tmpDir)
	if err == nil {
		t.Fatal("expected error for empty controls list")
	}
}

func TestRunControl(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}

	_, thisFile, _, _ := runtime.Caller(0)
	testdataDir := filepath.Join(filepath.Dir(thisFile), "testdata", "control-bundle")

	r := NewPythonRunner()
	targetConfig := json.RawMessage(`{"type": "database", "identifier": "localhost:5432"}`)

	result, err := r.RunControl(context.Background(), ControlRunRequest{
		BundlePath:   testdataDir,
		ControlID:    "test-control",
		Entrypoint:   "controls/test-control/check.py",
		TargetConfig: targetConfig,
	})
	if err != nil {
		t.Fatalf("RunControl error: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("result not valid JSON: %v", err)
	}
	if parsed["control_id"] != "test-control" {
		t.Errorf("control_id = %v, want %q", parsed["control_id"], "test-control")
	}
	if parsed["status"] != "pass" {
		t.Errorf("status = %v, want %q", parsed["status"], "pass")
	}
}

func writeTestFile(dir, name string, data []byte) error {
	return os.WriteFile(filepath.Join(dir, name), data, 0o644)
}

func TestVendorPythonPath(t *testing.T) {
	bundle := "/bundles/cis-postgresql-16/2.0.4"
	vendorPath := filepath.Join(bundle, "content/vendor")
	sep := string(os.PathListSeparator)

	tests := []struct {
		name      string
		vendorDir string
		existing  string
		want      string
	}{
		{
			name:      "vendor dir, no existing PYTHONPATH",
			vendorDir: "content/vendor",
			existing:  "",
			want:      "PYTHONPATH=" + vendorPath,
		},
		{
			name:      "vendor dir prepended to existing PYTHONPATH",
			vendorDir: "content/vendor",
			existing:  "/opt/extra",
			want:      "PYTHONPATH=" + vendorPath + sep + "/opt/extra",
		},
		{
			name:      "no vendor dir -> no PYTHONPATH override",
			vendorDir: "",
			existing:  "/opt/extra",
			want:      "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := vendorPythonPath(bundle, tc.vendorDir, tc.existing); got != tc.want {
				t.Errorf("vendorPythonPath(%q, %q, %q) = %q, want %q",
					bundle, tc.vendorDir, tc.existing, got, tc.want)
			}
		})
	}
}
