package nettls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// selfSignedPEM writes a throwaway CA cert to a temp file and returns its path.
func selfSignedPEM(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Unix(0, 0),
		NotAfter:              time.Unix(1<<31, 0),
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func reset() { tlsConf = nil }

func TestConfigureEmptyUsesSystemPool(t *testing.T) {
	reset()
	if err := Configure(Options{}); err != nil {
		t.Fatalf("empty Configure: %v", err)
	}
	if tlsConf != nil {
		t.Error("empty CA path should leave tlsConf nil (system pool)")
	}
	// Client/WSDialer still work with system defaults.
	if Client(time.Second).Transport == nil {
		t.Error("Client should have a transport")
	}
	tr := Client(time.Second).Transport.(*http.Transport)
	if tr.TLSClientConfig != nil && tr.TLSClientConfig.RootCAs != nil {
		t.Error("system-pool client should not pin custom RootCAs")
	}
}

func TestConfigureMissingFile(t *testing.T) {
	reset()
	if err := Configure(Options{CACertPath: filepath.Join(t.TempDir(), "nope.pem")}); err == nil {
		t.Fatal("expected error for missing CA file")
	}
}

func TestConfigureGarbageFails(t *testing.T) {
	reset()
	p := filepath.Join(t.TempDir(), "junk.pem")
	if err := os.WriteFile(p, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Configure(Options{CACertPath: p}); err == nil {
		t.Fatal("expected error when no certs parse (fail loudly, never silent)")
	}
}

func TestConfigureValidCA(t *testing.T) {
	reset()
	if err := Configure(Options{CACertPath: selfSignedPEM(t)}); err != nil {
		t.Fatalf("valid CA: %v", err)
	}
	if tlsConf == nil || tlsConf.RootCAs == nil {
		t.Fatal("valid CA should set tlsConf.RootCAs")
	}
	// Both the HTTP client and the WS dialer must carry the custom pool.
	if tr := Client(time.Second).Transport.(*http.Transport); tr.TLSClientConfig == nil || tr.TLSClientConfig.RootCAs == nil {
		t.Error("HTTP client missing custom RootCAs")
	}
	if d := WSDialer(); d.TLSClientConfig == nil || d.TLSClientConfig.RootCAs == nil {
		t.Error("WS dialer missing custom RootCAs")
	}
	reset()
}
