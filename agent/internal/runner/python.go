package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

const pythonTimeout = 5 * time.Minute

// PythonRunner executes Python-based compliance bundles.
type PythonRunner struct{}

// NewPythonRunner creates a new PythonRunner.
func NewPythonRunner() *PythonRunner {
	return &PythonRunner{}
}

// Run executes a Python compliance bundle and returns the results as JSON.
func (r *PythonRunner) Run(ctx context.Context, req RunRequest) (json.RawMessage, error) {
	tmpDir, err := os.MkdirTemp("", "silkstrand-scan-*")
	if err != nil {
		return nil, fmt.Errorf("creating temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// Write target config
	targetConfigPath := filepath.Join(tmpDir, "target_config.json")
	if err := os.WriteFile(targetConfigPath, req.TargetConfig, 0o600); err != nil {
		return nil, fmt.Errorf("writing target config: %w", err)
	}

	// Build command with timeout
	runCtx, cancel := context.WithTimeout(ctx, pythonTimeout)
	defer cancel()

	bundlePath, err := filepath.Abs(req.BundlePath)
	if err != nil {
		return nil, fmt.Errorf("resolving bundle path: %w", err)
	}
	entrypoint := filepath.Join(bundlePath, req.Manifest.Entrypoint)
	cmd := exec.CommandContext(runCtx, "python3", entrypoint)
	cmd.Dir = bundlePath

	env := os.Environ()
	env = append(env, "SILKSTRAND_TARGET_CONFIG="+targetConfigPath)

	// If the manifest declares a vendor directory, prepend it to PYTHONPATH so
	// the bundle's pure-Python dependencies are importable without touching
	// system site-packages.
	if p := vendorPythonPath(bundlePath, req.Manifest.VendorDir, os.Getenv("PYTHONPATH")); p != "" {
		env = append(env, p)
	}

	// Pass credentials if provided. On Unix we hand the bundle a pipe via
	// ExtraFiles so it can read from /dev/fd/3 — credentials never touch
	// disk. On Windows (no /dev/fd) we fall back to a 0o600 temp file.
	// In both cases the bundle consumes the same env-var contract:
	// SILKSTRAND_CREDENTIALS points at a path it can open and read.
	if len(req.Credentials) > 0 && string(req.Credentials) != "null" {
		if runtime.GOOS == "windows" {
			credentialsPath := filepath.Join(tmpDir, "credentials.json")
			if err := os.WriteFile(credentialsPath, req.Credentials, 0o600); err != nil {
				return nil, fmt.Errorf("writing credentials: %w", err)
			}
			env = append(env, "SILKSTRAND_CREDENTIALS="+credentialsPath)
		} else {
			pr, pw, err := os.Pipe()
			if err != nil {
				return nil, fmt.Errorf("creating credential pipe: %w", err)
			}
			defer pr.Close()
			// Credentials JSON fits comfortably in the kernel pipe buffer
			// (default 64KiB on Linux), so a synchronous write + close
			// before exec leaves the data buffered for the child to read.
			if _, err := pw.Write(req.Credentials); err != nil {
				pw.Close()
				return nil, fmt.Errorf("writing credential pipe: %w", err)
			}
			if err := pw.Close(); err != nil {
				return nil, fmt.Errorf("closing credential pipe: %w", err)
			}
			cmd.ExtraFiles = []*os.File{pr}
			env = append(env, "SILKSTRAND_CREDENTIALS=/dev/fd/3")
		}
	}

	cmd.Env = env

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	slog.Info("executing bundle", "entrypoint", req.Manifest.Entrypoint, "bundle", req.Manifest.Name)

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("executing bundle: %w\nstderr: %s", err, stderr.String())
	}

	if stderr.Len() > 0 {
		slog.Debug("bundle stderr output", "stderr", stderr.String())
	}

	// Validate stdout is valid JSON
	output := stdout.Bytes()
	if !json.Valid(output) {
		return nil, fmt.Errorf("bundle output is not valid JSON: %s", string(output))
	}

	return json.RawMessage(output), nil
}

// RunControl executes a single control's check.py entrypoint and returns
// its JSON result. Each control emits a single JSON object (not an array).
func (r *PythonRunner) RunControl(ctx context.Context, req ControlRunRequest) (json.RawMessage, error) {
	tmpDir, err := os.MkdirTemp("", "silkstrand-control-*")
	if err != nil {
		return nil, fmt.Errorf("creating temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	targetConfigPath := filepath.Join(tmpDir, "target_config.json")
	if err := os.WriteFile(targetConfigPath, req.TargetConfig, 0o600); err != nil {
		return nil, fmt.Errorf("writing target config: %w", err)
	}

	runCtx, cancel := context.WithTimeout(ctx, pythonTimeout)
	defer cancel()

	bundlePath, err := filepath.Abs(req.BundlePath)
	if err != nil {
		return nil, fmt.Errorf("resolving bundle path: %w", err)
	}
	entrypoint := filepath.Join(bundlePath, req.Entrypoint)
	cmd := exec.CommandContext(runCtx, "python3", entrypoint)
	cmd.Dir = bundlePath

	env := os.Environ()
	env = append(env, "SILKSTRAND_TARGET_CONFIG="+targetConfigPath)

	if p := vendorPythonPath(bundlePath, req.VendorDir, os.Getenv("PYTHONPATH")); p != "" {
		env = append(env, p)
	}

	if len(req.Credentials) > 0 && string(req.Credentials) != "null" {
		if runtime.GOOS == "windows" {
			credentialsPath := filepath.Join(tmpDir, "credentials.json")
			if err := os.WriteFile(credentialsPath, req.Credentials, 0o600); err != nil {
				return nil, fmt.Errorf("writing credentials: %w", err)
			}
			env = append(env, "SILKSTRAND_CREDENTIALS="+credentialsPath)
		} else {
			pr, pw, err := os.Pipe()
			if err != nil {
				return nil, fmt.Errorf("creating credential pipe: %w", err)
			}
			defer pr.Close()
			if _, err := pw.Write(req.Credentials); err != nil {
				pw.Close()
				return nil, fmt.Errorf("writing credential pipe: %w", err)
			}
			if err := pw.Close(); err != nil {
				return nil, fmt.Errorf("closing credential pipe: %w", err)
			}
			cmd.ExtraFiles = []*os.File{pr}
			env = append(env, "SILKSTRAND_CREDENTIALS=/dev/fd/3")
		}
	}

	cmd.Env = env

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("executing control %s: %w\nstderr: %s", req.ControlID, err, stderr.String())
	}

	if stderr.Len() > 0 {
		slog.Debug("control stderr output", "control", req.ControlID, "stderr", stderr.String())
	}

	output := stdout.Bytes()
	if !json.Valid(output) {
		return nil, fmt.Errorf("control %s output is not valid JSON: %s", req.ControlID, string(output))
	}

	return json.RawMessage(output), nil
}

// vendorPythonPath builds the PYTHONPATH env entry that prepends a bundle's
// vendored dependencies (bundlePath/vendorDir) ahead of any existing PYTHONPATH,
// so a control's pure-Python deps import without touching system site-packages.
// Returns "" when vendorDir is empty (no PYTHONPATH override). Shared by the
// whole-bundle and per-control runners.
func vendorPythonPath(bundlePath, vendorDir, existing string) string {
	if vendorDir == "" {
		return ""
	}
	vendorPath := filepath.Join(bundlePath, vendorDir)
	if existing != "" {
		return "PYTHONPATH=" + vendorPath + string(os.PathListSeparator) + existing
	}
	return "PYTHONPATH=" + vendorPath
}
