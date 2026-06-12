// Package nettls centralizes the agent's outbound TLS + egress-proxy
// configuration so every HTTPS/WSS path — bootstrap, the WSS tunnel, bundle /
// collector / runtime downloads, the self-updater, and self-deregister — trusts
// the same custom CA and honors the same proxy (ADR 013 D3).
//
// Configuration is write-once: Configure is called exactly once at startup,
// before any network call or goroutine, and the loaded *tls.Config is immutable
// thereafter — Client/WSDialer only read it. This mirrors how the stdlib treats
// http.DefaultTransport: a single process-wide outbound transport, set up front,
// not threaded through every call site (the agent builds clients in ~7 places).
package nettls

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/gorilla/websocket"
)

// Options are the TLS/proxy knobs resolved from agent config. The proxy is read
// from the standard HTTPS_PROXY/NO_PROXY environment via
// http.ProxyFromEnvironment, so only the CA path lives here.
type Options struct {
	// CACertPath is a PEM file appended to the system trust store. Empty means
	// "system pool only" (the common case; the host's update-ca-certificates is
	// the primary path).
	CACertPath string
}

// tlsConf is the process-wide outbound TLS config. nil means "use Go defaults"
// (the system trust store). Written once by Configure, read-only afterward.
var tlsConf *tls.Config

// Configure loads and validates the custom CA exactly once, at startup. It
// fails loudly if a CA path is set but unreadable or contributes no
// certificates — it never silently downgrades to an unverified connection and
// never sets InsecureSkipVerify. The CA path and fingerprint are logged; the
// contents never are.
func Configure(opts Options) error {
	if opts.CACertPath == "" {
		return nil // system trust store
	}
	pem, err := os.ReadFile(opts.CACertPath)
	if err != nil {
		return fmt.Errorf("reading CA cert %s: %w", opts.CACertPath, err)
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pem) {
		return fmt.Errorf("no certificates parsed from CA cert %s (expected PEM)", opts.CACertPath)
	}
	tlsConf = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	sum := sha256.Sum256(pem)
	slog.Info("custom CA loaded for outbound TLS",
		"path", opts.CACertPath, "sha256", hex.EncodeToString(sum[:]))
	return nil
}

// transport returns a fresh *http.Transport honoring the configured CA and the
// HTTPS_PROXY/NO_PROXY environment.
func transport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = http.ProxyFromEnvironment
	if tlsConf != nil {
		t.TLSClientConfig = tlsConf.Clone()
	}
	return t
}

// Client returns an *http.Client with the given timeout using the shared
// outbound transport (custom CA + egress proxy).
func Client(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: transport()}
}

// WSDialer returns a gorilla/websocket dialer with the same TLS + proxy as
// Client, so the WSS tunnel trusts the custom CA and honors the proxy too.
func WSDialer() *websocket.Dialer {
	d := *websocket.DefaultDialer // copy the stdlib defaults
	d.Proxy = http.ProxyFromEnvironment
	if tlsConf != nil {
		d.TLSClientConfig = tlsConf.Clone()
	}
	return &d
}
