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
	"strings"
	"time"

	"github.com/jtb75/silkstrand/agent/internal/nettls"
)

// Apply downloads the binary for this host's OS+arch, verifies the
// expected SHA-256, and replaces the currently-running executable.
// On success the caller should exit the process so the service
// manager can restart with the new binary.
//
// baseURL is the public S3/MinIO base (e.g. https://downloads.silkstrand.io/agent).
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
	client := nettls.Client(120 * time.Second)
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
// environment (Docker, Kubernetes, podman). Upgrade-in-place doesn't make
// sense there — the image is immutable.
func InContainer() bool {
	return detectContainer(os.Getenv, fileExists, os.ReadFile)
}

// detectContainer is the testable core of InContainer. Its dependencies on the
// environment, the filesystem, and file contents are injected so the detection
// logic can be exercised without a real container. Detection is best-effort:
// any read error simply falls through to the next signal, never an error.
func detectContainer(getenv func(string) string, exists func(string) bool, read func(string) ([]byte, error)) bool {
	// Primary k8s signal: the kubelet always injects this into every pod.
	// It is present even on modern nodes (cgroup v2 unified hierarchy, systemd
	// driver) where /proc/*/cgroup is a bare "0::/" with no container markers.
	if getenv("KUBERNETES_SERVICE_HOST") != "" {
		return true
	}
	// Runtime markers: Docker writes /.dockerenv, podman writes /run/.containerenv.
	if exists("/.dockerenv") || exists("/run/.containerenv") {
		return true
	}
	// cgroup markers: v1 named hierarchies ("…/docker/…", "…/kubepods/…") and
	// v2 unified paths ("0::/kubepods/…"). Check both PID 1 and self so we
	// cover hosts where one is bare but the other carries the path.
	//
	// The substring match is intentionally broad and we deliberately do NOT
	// tighten it to path-segment matching. in_container detection should err
	// toward TRUE because the two failure directions are asymmetric:
	//   - false negative (container seen as a binary) → the UI offers "Upgrade
	//     in place", which silently fails on an immutable image. Harmful — it's
	//     the exact bug this fix addresses.
	//   - false positive (binary seen as a container) → the UI offers "Recreate
	//     from image", at worst a redundant/no-op instruction. Harmless.
	// So a rare false positive (e.g. a bare-metal cgroup path that happens to
	// contain one of these substrings) is the acceptable, conservative trade.
	for _, path := range []string{"/proc/1/cgroup", "/proc/self/cgroup"} {
		b, err := read(path)
		if err != nil {
			continue
		}
		s := string(b)
		for _, marker := range []string{"docker", "kubepods", "containerd"} {
			if strings.Contains(s, marker) {
				return true
			}
		}
	}
	return false
}

// fileExists reports whether path exists (best-effort; any stat error is false).
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
