package cache

import (
	"archive/tar"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jtb75/silkstrand/agent/internal/nettls"
)

// ErrNotCached indicates the requested bundle is not in the local cache.
var ErrNotCached = errors.New("bundle not cached")

// Cache manages local storage of compliance bundles.
type Cache struct {
	bundleDir string
	publicKey ed25519.PublicKey // if nil, signature verification is skipped (dev mode)
}

// New creates a new Cache rooted at bundleDir.
// If publicKey is nil, signature verification is disabled (local dev only).
func New(bundleDir string, publicKey ed25519.PublicKey) *Cache {
	return &Cache{bundleDir: bundleDir, publicKey: publicKey}
}

// manifestFiles lists the manifest file names checked when looking for a
// cached bundle. bundle.yaml is the current format; manifest.yaml is the
// legacy name kept for backward compatibility.
var manifestFiles = []string{"manifest.yaml", "bundle.yaml"}

// sanitizeBundleName normalises a display name (e.g. "CIS Microsoft SQL
// Server 2022 Benchmark") into a filesystem-safe directory name
// ("cis-microsoft-sql-server-2022-benchmark").
func sanitizeBundleName(name string) string {
	s := strings.ToLower(name)
	s = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '.' {
			return r
		}
		return '-'
	}, s)
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	return strings.Trim(s, "-")
}

// hasManifest returns true if dir contains any recognised manifest file.
func hasManifest(dir string) bool {
	for _, m := range manifestFiles {
		if _, err := os.Stat(filepath.Join(dir, m)); err == nil {
			return true
		}
	}
	return false
}

// Get returns the path to a cached bundle directory. It checks two layouts:
//  1. Versioned: {bundleDir}/{bundleName}/{version}/(manifest.yaml|bundle.yaml)
//  2. Flat (local dev): {bundleDir}/{bundleName}/(manifest.yaml|bundle.yaml)
//
// The lookup tries both the raw bundleName and its sanitized form so that
// display names ("CIS Microsoft SQL Server 2022 Benchmark") resolve to
// on-disk directories like "cis-microsoft-sql-server-2022-benchmark".
//
// Returns ErrNotCached if neither layout has the bundle.
func (c *Cache) Get(bundleName, version string) (string, error) {
	candidates := []string{bundleName}
	if san := sanitizeBundleName(bundleName); san != bundleName {
		candidates = append(candidates, san)
	}

	for _, name := range candidates {
		// Check versioned layout first
		versionedPath := filepath.Join(c.bundleDir, name, version)
		if hasManifest(versionedPath) {
			if err := c.verify(versionedPath); err != nil {
				return "", err
			}
			return versionedPath, nil
		}

		// Check flat layout (local dev convenience)
		flatPath := filepath.Join(c.bundleDir, name)
		if hasManifest(flatPath) {
			if err := c.verify(flatPath); err != nil {
				return "", err
			}
			return flatPath, nil
		}
	}

	return "", ErrNotCached
}

// verify checks the Ed25519 signature of a bundle's manifest.
// If no public key is configured, verification is skipped (dev mode).
func (c *Cache) verify(bundlePath string) error {
	if c.publicKey == nil {
		return nil // dev mode — no verification
	}

	sigPath := filepath.Join(bundlePath, "signature.sig")
	sig, err := os.ReadFile(sigPath)
	if err != nil {
		return fmt.Errorf("bundle missing signature file: %w", err)
	}

	// Try each recognised manifest file name.
	var manifest []byte
	for _, m := range manifestFiles {
		data, readErr := os.ReadFile(filepath.Join(bundlePath, m))
		if readErr == nil {
			manifest = data
			break
		}
	}
	if manifest == nil {
		return fmt.Errorf("reading manifest for verification: no manifest found in %s", bundlePath)
	}

	if !ed25519.Verify(c.publicKey, manifest, sig) {
		return fmt.Errorf("bundle signature verification failed")
	}

	return nil
}

// Store extracts a .tar.gz bundle archive into the cache at {bundleDir}/{sanitizedName}/{version}/.
func (c *Cache) Store(bundleName, version string, data []byte) (string, error) {
	destDir := filepath.Join(c.bundleDir, sanitizeBundleName(bundleName), version)
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", fmt.Errorf("creating cache directory: %w", err)
	}

	r, err := gzip.NewReader(strings.NewReader(string(data)))
	if err != nil {
		return "", fmt.Errorf("opening gzip reader: %w", err)
	}
	defer r.Close()

	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("reading tar: %w", err)
		}

		target := filepath.Join(destDir, hdr.Name)

		// Prevent path traversal
		if !strings.HasPrefix(filepath.Clean(target), filepath.Clean(destDir)) {
			return "", fmt.Errorf("invalid tar entry path: %s", hdr.Name)
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return "", fmt.Errorf("creating directory %s: %w", target, err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return "", fmt.Errorf("creating parent directory: %w", err)
			}
			f, err := os.Create(target)
			if err != nil {
				return "", fmt.Errorf("creating file %s: %w", target, err)
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return "", fmt.Errorf("writing file %s: %w", target, err)
			}
			f.Close()
		}
	}

	return destDir, nil
}

// GetOrFetch returns a cached bundle directory, downloading the tarball from
// url if the bundle isn't already present. Expects a sibling .sha256 file next
// to the tarball (same layout as our agent-binary releases) and verifies the
// download against it before extracting.
func (c *Cache) GetOrFetch(bundleName, version, url string) (string, error) {
	if path, err := c.Get(bundleName, version); err == nil {
		return path, nil
	} else if !errors.Is(err, ErrNotCached) {
		return "", err
	}
	if url == "" {
		return "", fmt.Errorf("bundle %s v%s not cached and no URL supplied", bundleName, version)
	}

	slog.Info("fetching bundle", "name", bundleName, "version", version, "url", url)

	client := nettls.Client(2 * time.Minute)

	// Read the advertised checksum first; refuse to download without one.
	shaResp, err := client.Get(url + ".sha256")
	if err != nil {
		return "", fmt.Errorf("fetching bundle checksum: %w", err)
	}
	defer shaResp.Body.Close()
	if shaResp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("bundle checksum not found (HTTP %d) at %s.sha256", shaResp.StatusCode, url)
	}
	shaData, _ := io.ReadAll(io.LimitReader(shaResp.Body, 1<<12))
	expected := strings.TrimSpace(strings.Fields(string(shaData))[0])
	if len(expected) != 64 {
		return "", fmt.Errorf("invalid SHA-256 format at %s.sha256: %q", url, expected)
	}

	// Download the tarball.
	tarResp, err := client.Get(url)
	if err != nil {
		return "", fmt.Errorf("fetching bundle: %w", err)
	}
	defer tarResp.Body.Close()
	if tarResp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("bundle download returned HTTP %d for %s", tarResp.StatusCode, url)
	}

	h := sha256.New()
	body, err := io.ReadAll(io.TeeReader(tarResp.Body, h))
	if err != nil {
		return "", fmt.Errorf("reading bundle body: %w", err)
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != expected {
		return "", fmt.Errorf("bundle checksum mismatch: expected %s, got %s", expected, got)
	}

	path, err := c.Store(bundleName, version, body)
	if err != nil {
		return "", fmt.Errorf("storing bundle: %w", err)
	}
	// Run the existing signature check after extraction.
	if err := c.verify(path); err != nil {
		return "", err
	}
	slog.Info("cached bundle", "path", path)
	return path, nil
}
