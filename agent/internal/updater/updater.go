// Package updater performs in-place agent binary upgrades triggered by the
// server. Download + verify + atomic replace + exit. The service manager
// (systemd, launchd) restarts the agent; the new binary runs.
package updater

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// Apply downloads the binary for this host's OS+arch, verifies the
// expected SHA-256, and replaces the currently-running executable.
// On success the caller should exit the process so the service
// manager can restart with the new binary.
//
// baseURL is the public S3/MinIO base (e.g. https://s3.ng20.org/silkstrand-agent-releases).
// version is the release folder ("v0.1.4" or "latest").
// expectedSHA256 is the hex SHA-256 the server advertised for this platform; empty skips
// verification (discouraged — keep it strict for prod).
func Apply(baseURL, version, expectedSHA256 string) error {
	if InContainer() {
		return fmt.Errorf("in-place upgrade not supported in container; update the image and restart")
	}
	target, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locating own binary: %w", err)
	}
	target, err = filepath.EvalSymlinks(target)
	if err != nil {
		return fmt.Errorf("resolving symlinks on %s: %w", target, err)
	}

	suffix := runtime.GOOS + "-" + runtime.GOARCH
	url := fmt.Sprintf("%s/%s/silkstrand-agent-%s", baseURL, version, suffix)

	// Download
	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("downloading %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download returned %d for %s", resp.StatusCode, url)
	}

	// Write to a sibling temp file, verify, swap atomically.
	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, ".silkstrand-agent-*")
	if err != nil {
		return fmt.Errorf("creating temp file in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op if we've renamed it

	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), resp.Body); err != nil {
		tmp.Close()
		return fmt.Errorf("writing binary: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing temp file: %w", err)
	}

	actualSHA := hex.EncodeToString(h.Sum(nil))
	if expectedSHA256 != "" && actualSHA != expectedSHA256 {
		return fmt.Errorf("checksum mismatch: expected %s, got %s", expectedSHA256, actualSHA)
	}

	// Match the target's mode/ownership; fall back to 0755.
	mode := os.FileMode(0o755)
	if st, err := os.Stat(target); err == nil {
		mode = st.Mode() & 0o777
	}
	if err := os.Chmod(tmpPath, mode); err != nil {
		return fmt.Errorf("chmod temp file: %w", err)
	}

	if err := os.Rename(tmpPath, target); err != nil {
		return fmt.Errorf("replacing %s: %w", target, err)
	}
	return nil
}

// InContainer returns true when the agent is running inside a container
// environment (Docker, Kubernetes). Upgrade-in-place doesn't make sense
// there — the image is immutable.
func InContainer() bool {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	if b, err := os.ReadFile("/proc/1/cgroup"); err == nil {
		s := string(b)
		for _, marker := range []string{"docker", "kubepods", "containerd"} {
			if contains(s, marker) {
				return true
			}
		}
	}
	return false
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
