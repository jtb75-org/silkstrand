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
