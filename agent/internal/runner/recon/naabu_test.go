package recon

import (
	"slices"
	"strings"
	"testing"
)

func TestNaabuScanArgs(t *testing.T) {
	connect := append([]string{"-scan-type", "c"}, connectTuning...)

	tests := []struct {
		name     string
		env      string
		hasRaw   bool
		wantArgs []string
		wantMode string
	}{
		{
			name:     "caps present, no override -> SYN default (no scan-type arg)",
			env:      "",
			hasRaw:   true,
			wantArgs: nil,
			wantMode: "s",
		},
		{
			name:     "caps absent, no override -> CONNECT auto-selected + tuning",
			env:      "",
			hasRaw:   false,
			wantArgs: connect,
			wantMode: "c",
		},
		{
			name:     "env override c wins even with caps present",
			env:      "c",
			hasRaw:   true,
			wantArgs: connect,
			wantMode: "c",
		},
		{
			name:     "env override s wins even with caps absent",
			env:      "s",
			hasRaw:   false,
			wantArgs: []string{"-scan-type", "s"},
			wantMode: "s",
		},
		{
			name:     "env override is trimmed",
			env:      "  c  ",
			hasRaw:   true,
			wantArgs: connect,
			wantMode: "c",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotArgs, gotMode, gotReason := naabuScanArgs(tc.env, tc.hasRaw)
			if !slices.Equal(gotArgs, tc.wantArgs) {
				t.Errorf("args = %v, want %v", gotArgs, tc.wantArgs)
			}
			if gotMode != tc.wantMode {
				t.Errorf("mode = %q, want %q", gotMode, tc.wantMode)
			}
			if gotReason == "" {
				t.Error("reason should never be empty")
			}
		})
	}
}

func TestNaabuPortArgs(t *testing.T) {
	tests := []struct {
		name  string
		ports string
		want  []string
	}{
		{
			name:  "explicit ports -> -p with the given list",
			ports: "80,443,27017",
			want:  []string{"-p", "80,443,27017"},
		},
		{
			name:  "empty -> curated default",
			ports: "",
			want:  []string{"-p", defaultNaabuPorts},
		},
		{
			name:  "whitespace-only -> curated default",
			ports: "   ",
			want:  []string{"-p", defaultNaabuPorts},
		},
		{
			name:  "explicit ports are trimmed",
			ports: "  8080  ",
			want:  []string{"-p", "8080"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := naabuPortArgs(tc.ports); !slices.Equal(got, tc.want) {
				t.Errorf("naabuPortArgs(%q) = %v, want %v", tc.ports, got, tc.want)
			}
		})
	}
}

// The curated default must cover the DB/service ports top-100 misses — the whole
// reason it exists. Guard against an accidental edit dropping one.

func TestParseCapEffNetRaw(t *testing.T) {
	// CAP_NET_RAW is bit 13 (0x2000).
	tests := []struct {
		name       string
		status     string
		wantHasCap bool
		wantOK     bool
	}{
		{
			name:       "CAP_NET_RAW present (only that bit)",
			status:     "Name:\tnaabu\nCapEff:\t0000000000002000\nSeccomp:\t0\n",
			wantHasCap: true,
			wantOK:     true,
		},
		{
			name:       "full cap set includes CAP_NET_RAW",
			status:     "CapEff:\t000001ffffffffff\n",
			wantHasCap: true,
			wantOK:     true,
		},
		{
			name:       "typical container default set has CAP_NET_RAW",
			status:     "CapEff:\t00000000a80425fb\n", // 0xa80425fb has bit 13 set
			wantHasCap: true,
			wantOK:     true,
		},
		{
			// The case navi/dino flagged: root (uid 0) with all caps dropped.
			// CapEff is parseable and lacks bit 13 -> not capable, so the caller
			// must select CONNECT even though euid==0.
			name:       "root with caps dropped -> CapEff zero, not capable",
			status:     "Name:\tnaabu\nUid:\t0\t0\t0\t0\nCapEff:\t0000000000000000\n",
			wantHasCap: false,
			wantOK:     true,
		},
		{
			name:       "no CapEff line -> ok=false (caller falls back)",
			status:     "Name:\tnaabu\nUid:\t0\t0\t0\t0\n",
			wantHasCap: false,
			wantOK:     false,
		},
		{
			name:       "garbled CapEff -> ok=false (caller falls back)",
			status:     "CapEff:\tnot-hex\n",
			wantHasCap: false,
			wantOK:     false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotCap, gotOK := parseCapEffNetRaw([]byte(tc.status))
			if gotCap != tc.wantHasCap || gotOK != tc.wantOK {
				t.Errorf("parseCapEffNetRaw() = (%v, %v), want (%v, %v)",
					gotCap, gotOK, tc.wantHasCap, tc.wantOK)
			}
		})
	}
}

// End-to-end of the detection→mode decision for the flagged scenario: a parsed
// CapEff without CAP_NET_RAW must yield CONNECT, with the bit set must yield SYN.
func TestScanModeFromCapEff(t *testing.T) {
	rootNoCap, ok := parseCapEffNetRaw([]byte("Uid:\t0\t0\t0\t0\nCapEff:\t0000000000000000\n"))
	if !ok || rootNoCap {
		t.Fatalf("setup: root-without-NET_RAW parse = (%v, %v)", rootNoCap, ok)
	}
	if _, mode, _ := naabuScanArgs("", rootNoCap); mode != "c" {
		t.Errorf("root without CAP_NET_RAW: mode = %q, want CONNECT (c)", mode)
	}

	withCap, ok := parseCapEffNetRaw([]byte("CapEff:\t0000000000002000\n"))
	if !ok || !withCap {
		t.Fatalf("setup: with-NET_RAW parse = (%v, %v)", withCap, ok)
	}
	if _, mode, _ := naabuScanArgs("", withCap); mode != "s" {
		t.Errorf("with CAP_NET_RAW: mode = %q, want SYN (s)", mode)
	}
}

func TestDefaultNaabuPortsCoversDBPorts(t *testing.T) {
	set := map[string]struct{}{}
	for _, p := range strings.Split(defaultNaabuPorts, ",") {
		set[strings.TrimSpace(p)] = struct{}{}
	}
	for _, want := range []string{"5432", "27017", "1433", "3306", "6379", "5984", "9200"} {
		if _, ok := set[want]; !ok {
			t.Errorf("defaultNaabuPorts missing DB/service port %s", want)
		}
	}
}
