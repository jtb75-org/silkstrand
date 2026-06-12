package recon

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

func TestStartProgress(t *testing.T) {
	var calls atomic.Int64
	stop := startProgress(context.Background(), 5*time.Millisecond, "test", func() []any {
		calls.Add(1)
		return []any{"n", calls.Load()}
	})
	time.Sleep(45 * time.Millisecond) // ~9 ticks
	stop()
	time.Sleep(20 * time.Millisecond) // let the goroutine exit
	got := calls.Load()
	if got < 2 {
		t.Fatalf("expected several progress calls, got %d", got)
	}
	time.Sleep(20 * time.Millisecond)
	if calls.Load() != got {
		t.Errorf("calls continued after stop: %d -> %d", got, calls.Load())
	}
}

func TestClassifyTarget(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"10.0.0.0/24", targetCIDR},
		{"10.0.0.0/8", targetCIDR},
		{"10.0.0.1-10.0.0.50", targetRange},
		{"10.0.0.5", targetIP},
		{"2001:db8::1", targetIP},
		{"app.example.com", targetHostname},
		{"my-app.example.com", targetHostname}, // hyphen must NOT read as a range
		{"a-b-c.internal", targetHostname},
		{"status.example.com", targetHostname},
		{"  app.example.com  ", targetHostname},
	}
	for _, c := range cases {
		if got := classifyTarget(c.in); got != c.want {
			t.Errorf("classifyTarget(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestVetHostnameAllowlist(t *testing.T) {
	// app.example.com is allowlisted by name; 10.0.0.0/16 by CIDR. evil.com is
	// not listed. Resolution is injected so no real DNS is needed (D11).
	allow := &Allowlist{Allow: []string{"app.example.com", "10.0.0.0/16"}}
	if err := allow.parse(); err != nil {
		t.Fatal(err)
	}
	orig := lookupIP
	defer func() { lookupIP = orig }()

	cases := []struct {
		name     string
		target   string
		resolved string
		wantErr  bool
	}{
		{"allowed name + allowed IP", "app.example.com", "10.0.0.5", false},
		{"unlisted name + allowed IP", "evil.com", "10.0.0.5", true},         // name not in allowlist
		{"allowed name + disallowed IP", "app.example.com", "8.8.8.8", true}, // resolves out of scope
	}
	for _, c := range cases {
		lookupIP = func(string) ([]net.IP, error) { return []net.IP{net.ParseIP(c.resolved)}, nil }
		err := vetTargetAgainstAllowlist(c.target, allow)
		if (err != nil) != c.wantErr {
			t.Errorf("%s: err=%v wantErr=%v", c.name, err, c.wantErr)
		}
	}
}
