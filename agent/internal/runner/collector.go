package runner

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// collectorMap maps target engine types to collector binary base names.
// An empty string means no Go collector exists yet for that engine.
var collectorMap = map[string]string{
	"mssql":      "mssql-collector",
	"postgresql": "postgresql-collector",
	"mongodb":    "mongodb-collector",
	"database":   "", // generic — no collector yet
}

// DetermineCollector returns the collector binary base name for the given
// target type, or "" if no Go collector is available.
func DetermineCollector(targetType string) string {
	return collectorMap[strings.ToLower(targetType)]
}

// collectorRuntimesBaseURL is the public MinIO bucket hosting collector
// binaries. Shares the same bucket as the PD recon tools.
var collectorRuntimesBaseURL = func() string {
	if v := os.Getenv("SILKSTRAND_RUNTIMES_BASE_URL"); v != "" {
		return v
	}
	return "https://downloads.silkstrand.io/runtimes"
}()

// collectorRuntimesDir is where cached collector binaries live on disk.
var collectorRuntimesDir = func() string {
	if v := os.Getenv("SILKSTRAND_RUNTIMES_DIR"); v != "" {
		return v
	}
	return "/var/lib/silkstrand/runtimes"
}()

// collectorMu serializes collector installs.
var collectorMu sync.Mutex

// EnsureCollector returns the absolute path to a collector binary,
// downloading from MinIO on first use. Falls back to $PATH and the
// bundles directory if the download is unavailable (local dev).
//
// The binary name on disk and in MinIO follows the pattern:
//
//	{collectorID}-{os}-{arch}   e.g. mssql-collector-darwin-arm64
//
// Cached at: {runtimesDir}/collectors/{binaryName}
func EnsureCollector(collectorID string) (string, error) {
	collectorMu.Lock()
	defer collectorMu.Unlock()

	platformKey := runtime.GOOS + "-" + runtime.GOARCH
	binaryName := collectorID + "-" + platformKey
	if runtime.GOOS == "windows" {
		binaryName += ".exe"
	}

	cacheDir := filepath.Join(collectorRuntimesDir, "collectors")
	cachedPath := filepath.Join(cacheDir, binaryName)

	// 1. Return cached binary if it exists and is executable.
	if info, err := os.Stat(cachedPath); err == nil && !info.IsDir() {
		return cachedPath, nil
	}

	// 2. Try downloading from MinIO.
	url := fmt.Sprintf("%s/collectors/%s", collectorRuntimesBaseURL, binaryName)
	shaURL := url + ".sha256"

	if path, err := downloadCollector(url, shaURL, cacheDir, binaryName); err == nil {
		return path, nil
	} else {
		slog.Debug("collector MinIO download unavailable, trying fallbacks",
			"collector", collectorID, "error", err)
	}

	// 3. Fallback: check $PATH.
	if path, err := exec.LookPath(collectorID); err == nil {
		return path, nil
	}

	// 4. Fallback: check bundles directory (for local dev with binaries
	//    placed alongside bundles).
	bundleDir := os.Getenv("SILKSTRAND_BUNDLE_DIR")
	if bundleDir == "" {
		bundleDir = "./bundles"
	}
	localPath := filepath.Join(bundleDir, binaryName)
	if info, err := os.Stat(localPath); err == nil && !info.IsDir() {
		return localPath, nil
	}
	// Also try without platform suffix (dev convenience).
	localPathShort := filepath.Join(bundleDir, collectorID)
	if info, err := os.Stat(localPathShort); err == nil && !info.IsDir() {
		return localPathShort, nil
	}

	return "", fmt.Errorf("collector %s not found (checked cache, MinIO, PATH, bundles)", collectorID)
}

// downloadCollector fetches a collector binary + its .sha256 sidecar from
// MinIO, verifies the hash, and stores it in cacheDir.
func downloadCollector(url, shaURL, cacheDir, binaryName string) (string, error) {
	client := &http.Client{Timeout: 5 * time.Minute}

	// Fetch expected SHA256.
	shaResp, err := client.Get(shaURL)
	if err != nil {
		return "", fmt.Errorf("fetching sha256: %w", err)
	}
	defer shaResp.Body.Close()
	if shaResp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("sha256 not found (HTTP %d): %s", shaResp.StatusCode, shaURL)
	}
	shaData, _ := io.ReadAll(io.LimitReader(shaResp.Body, 1<<12))
	expected := strings.TrimSpace(strings.Fields(string(shaData))[0])
	if len(expected) != 64 {
		return "", fmt.Errorf("invalid sha256 format: %q", expected)
	}

	// Download binary.
	resp, err := client.Get(url)
	if err != nil {
		return "", fmt.Errorf("downloading collector: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("collector download HTTP %d: %s", resp.StatusCode, url)
	}

	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", fmt.Errorf("creating cache dir: %w", err)
	}

	target := filepath.Join(cacheDir, binaryName)
	tmp := target + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return "", fmt.Errorf("creating temp file: %w", err)
	}
	hasher := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, hasher), resp.Body); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return "", fmt.Errorf("writing collector: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("closing temp file: %w", err)
	}

	got := hex.EncodeToString(hasher.Sum(nil))
	if got != expected {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("sha256 mismatch: got %s, want %s", got, expected)
	}

	if err := os.Chmod(tmp, 0o755); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("chmod: %w", err)
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("atomic rename: %w", err)
	}
	return target, nil
}

// CollectorOutput is the expected JSON envelope from a collector binary.
type CollectorOutput struct {
	CollectorID string         `json:"collector_id"`
	Facts       map[string]any `json:"facts"`
}

// RunCollector executes a collector binary, passing credentials via stdin
// and reading the facts JSON from stdout. The collector binary receives
// a JSON object on stdin with the target config and credentials merged.
func RunCollector(ctx context.Context, binaryPath string, targetConfig, creds json.RawMessage) (*CollectorOutput, error) {
	// Build stdin payload: merge target_config + credentials.
	input := map[string]json.RawMessage{
		"target_config": targetConfig,
		"credentials":   creds,
	}
	stdinData, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("marshaling collector input: %w", err)
	}

	cmd := exec.CommandContext(ctx, binaryPath)
	cmd.Stdin = bytes.NewReader(stdinData)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("collector execution failed: %w: %s", err, stderr.String())
	}

	var result CollectorOutput
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		return nil, fmt.Errorf("parsing collector output: %w", err)
	}

	if result.Facts == nil {
		return nil, fmt.Errorf("collector returned no facts")
	}

	return &result, nil
}
