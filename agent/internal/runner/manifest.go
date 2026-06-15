package runner

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Manifest represents a compliance bundle's manifest.yaml (legacy format).
type Manifest struct {
	Name           string `yaml:"name"`
	Version        string `yaml:"version"`
	Framework      string `yaml:"framework"`
	RuntimeVersion string `yaml:"runtime_version"`
	TargetType     string `yaml:"target_type"`
	Entrypoint     string `yaml:"entrypoint"`
	VendorDir      string `yaml:"vendor_dir"`
	Benchmark      struct {
		Name    string `yaml:"name"`
		Version string `yaml:"version"`
		CISID   string `yaml:"cis_id"`
	} `yaml:"benchmark"`
	Outputs struct {
		Format string `yaml:"format"`
	} `yaml:"outputs"`
}

// BundleManifest represents the ADR 010 bundle.yaml format with per-control
// enumeration. When present, the runner iterates controls individually
// instead of running a single monolithic entrypoint.
type BundleManifest struct {
	ID        string   `yaml:"id"`
	Name      string   `yaml:"name"`
	Version   string   `yaml:"version"`
	Framework string   `yaml:"framework"`
	Engine    string   `yaml:"engine"`
	Controls  []string `yaml:"controls"`
	// VendorDir is the bundle-relative path to vendored Python deps, prepended to
	// PYTHONPATH for each control (e.g. "content/vendor"). Empty → the runner
	// falls back to content/vendor when that directory exists.
	VendorDir string `yaml:"vendor_dir"`
}

// LoadManifest reads and parses the manifest.yaml from the given bundle path.
func LoadManifest(bundlePath string) (*Manifest, error) {
	data, err := os.ReadFile(filepath.Join(bundlePath, "manifest.yaml"))
	if err != nil {
		return nil, fmt.Errorf("reading manifest: %w", err)
	}

	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parsing manifest: %w", err)
	}

	if m.Entrypoint == "" {
		return nil, fmt.Errorf("manifest missing entrypoint")
	}

	return &m, nil
}

// ReadBundleManifest reads the ADR 010 bundle.yaml from a bundle directory.
// Returns (nil, nil) if the file does not exist — this signals a legacy
// bundle that should fall back to manifest.yaml / content/checks.py.
func ReadBundleManifest(bundlePath string) (*BundleManifest, error) {
	data, err := os.ReadFile(filepath.Join(bundlePath, "bundle.yaml"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil // legacy bundle
		}
		return nil, fmt.Errorf("reading bundle.yaml: %w", err)
	}

	var m BundleManifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parsing bundle.yaml: %w", err)
	}

	if len(m.Controls) == 0 {
		return nil, fmt.Errorf("bundle.yaml has no controls listed")
	}

	return &m, nil
}
