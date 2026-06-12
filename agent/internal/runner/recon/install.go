package recon

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// runtimesBaseURL is the public download URL (downloads.silkstrand.io,
// backed by MinIO) hosting PD binaries + curated nuclei-templates tarballs.
// Override via SILKSTRAND_RUNTIMES_BASE_URL for airgapped/test mirrors.
var runtimesBaseURL = func() string {
	if v := os.Getenv("SILKSTRAND_RUNTIMES_BASE_URL"); v != "" {
		return v
	}
	return "https://downloads.silkstrand.io/runtimes"
}()

// runtimesDir is where installed binaries live on disk. Override via
// SILKSTRAND_RUNTIMES_DIR for airgapped or test environments.
var runtimesDir = func() string {
	if v := os.Getenv("SILKSTRAND_RUNTIMES_DIR"); v != "" {
		return v
	}
	return "/var/lib/silkstrand/runtimes"
}()

// ErrUnsupportedPlatform indicates no PD binary is published for the
// agent's GOOS/GOARCH combination (e.g. windows/arm64). The recon
// directive is rejected with `platform_unsupported`; compliance scans
// are unaffected.
var ErrUnsupportedPlatform = errors.New("platform_unsupported")

// ErrPinsMissing means pdpins.go hasn't been populated yet (pre-PR #6).
// The recon directive is rejected; this is the expected state on first
// deploy.
var ErrPinsMissing = errors.New("pd_pins_missing")

// installMu serializes installs across goroutines for the same tool.
var installMu sync.Mutex

// EnsureTool returns the absolute path to a verified PD binary,
// fetching it from the runtimes bucket on first call. Subsequent calls
// re-verify the on-disk file's sha256 and reuse it.
func EnsureTool(name string) (string, error) {
	installMu.Lock()
	defer installMu.Unlock()

	tool, ok := lookupPin(name)
	if !ok {
		return "", fmt.Errorf("unknown PD tool: %s", name)
	}
	if tool.Version == "" {
		return "", ErrPinsMissing
	}
	platformKey := runtime.GOOS + "-" + runtime.GOARCH
	expectedSHA, ok := tool.SHA256[platformKey]
	if !ok || expectedSHA == "" {
		return "", fmt.Errorf("%w: %s on %s", ErrUnsupportedPlatform, name, platformKey)
	}

	exeName := name
	if runtime.GOOS == "windows" {
		exeName += ".exe"
	}
	dir := filepath.Join(runtimesDir, name, tool.Version)
	target := filepath.Join(dir, exeName)

	// Reuse if present and sha matches.
	if got, err := fileSHA256(target); err == nil && got == expectedSHA {
		return target, nil
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("creating runtime dir: %w", err)
	}

	url := fmt.Sprintf("%s/%s/%s/%s/%s",
		runtimesBaseURL, name, tool.Version, platformKey, exeName)
	tmp := target + ".tmp"
	if err := downloadAndVerify(url, tmp, expectedSHA); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		return "", fmt.Errorf("chmod: %w", err)
	}
	if err := os.Rename(tmp, target); err != nil {
		return "", fmt.Errorf("atomic rename: %w", err)
	}
	return target, nil
}

func lookupPin(name string) (PDTool, bool) {
	for _, t := range pdTools {
		if t.Name == name {
			return t, true
		}
	}
	return PDTool{}, false
}

// EnsureTemplates returns the absolute path to the nuclei-templates
// directory, downloading + verifying the tarball on first call.
func EnsureTemplates() (string, error) {
	installMu.Lock()
	defer installMu.Unlock()

	if nucleiTemplatesPin.Version == "" {
		return "", ErrPinsMissing
	}
	dir := filepath.Join(runtimesDir, "nuclei-templates", nucleiTemplatesPin.Version)
	if _, err := os.Stat(dir); err == nil {
		return dir, nil
	}

	url := fmt.Sprintf("%s/nuclei-templates/%s.tar.gz", runtimesBaseURL, nucleiTemplatesPin.Version)
	tmpTar := filepath.Join(os.TempDir(),
		fmt.Sprintf("silkstrand-templates-%s.tar.gz", nucleiTemplatesPin.Version))
	if err := downloadAndVerify(url, tmpTar, nucleiTemplatesPin.SHA256); err != nil {
		_ = os.Remove(tmpTar)
		return "", err
	}
	defer os.Remove(tmpTar)

	stagingDir := dir + ".staging"
	_ = os.RemoveAll(stagingDir)
	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		return "", fmt.Errorf("creating templates staging dir: %w", err)
	}
	if err := extractTarGz(tmpTar, stagingDir); err != nil {
		_ = os.RemoveAll(stagingDir)
		return "", err
	}
	if err := os.Rename(stagingDir, dir); err != nil {
		_ = os.RemoveAll(stagingDir)
		return "", fmt.Errorf("atomic rename: %w", err)
	}
	return dir, nil
}

// extractTarGz unpacks a gzipped tar into dst. Path traversal is
// rejected.
func extractTarGz(src, dst string) error {
	f, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open tarball: %w", err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("gzip reader: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	dstAbs, err := filepath.Abs(dst)
	if err != nil {
		return err
	}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("tar read: %w", err)
		}
		// Reject path traversal.
		target := filepath.Join(dstAbs, hdr.Name)
		if !strings.HasPrefix(target, dstAbs+string(os.PathSeparator)) && target != dstAbs {
			return fmt.Errorf("tar path traversal: %s", hdr.Name)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("mkdir %s: %w", target, err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf("mkdir parent of %s: %w", target, err)
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode)&0o755)
			if err != nil {
				return fmt.Errorf("open %s: %w", target, err)
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return fmt.Errorf("write %s: %w", target, err)
			}
			out.Close()
		default:
			// Skip symlinks, devices, etc. — templates are pure files.
		}
	}
}

func downloadAndVerify(url, dst, expectedSHA string) error {
	client := &http.Client{Timeout: 10 * time.Minute}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("downloading %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d", url, resp.StatusCode)
	}
	f, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("creating %s: %w", dst, err)
	}
	hasher := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, hasher), resp.Body); err != nil {
		f.Close()
		return fmt.Errorf("writing %s: %w", dst, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", dst, err)
	}
	got := hex.EncodeToString(hasher.Sum(nil))
	if got != expectedSHA {
		return fmt.Errorf("sha256 mismatch: got %s, want %s", got, expectedSHA)
	}
	return nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
