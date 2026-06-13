package scheduler

import (
	"testing"

	"github.com/jtb75/silkstrand/api/internal/model"
)

func TestSplitDiscoveryTarget(t *testing.T) {
	agentID := "agent-1"
	cases := []struct {
		name       string
		targetType string
		target     string
		maxIPs     int
		want       []struct {
			ident   string
			start   string
			end     string
			ipCount int
		}
	}{
		{
			name:       "small cidr stays one cidr chunk",
			targetType: model.TargetTypeCIDR,
			target:     "10.0.0.0/30",
			maxIPs:     1024,
			want: []struct {
				ident   string
				start   string
				end     string
				ipCount int
			}{{"10.0.0.0/30", "10.0.0.0", "10.0.0.3", 4}},
		},
		{
			name:       "cidr splits into aligned cidrs",
			targetType: model.TargetTypeCIDR,
			target:     "10.0.0.0/24",
			maxIPs:     64,
			want: []struct {
				ident   string
				start   string
				end     string
				ipCount int
			}{
				{"10.0.0.0/26", "10.0.0.0", "10.0.0.63", 64},
				{"10.0.0.64/26", "10.0.0.64", "10.0.0.127", 64},
				{"10.0.0.128/26", "10.0.0.128", "10.0.0.191", 64},
				{"10.0.0.192/26", "10.0.0.192", "10.0.0.255", 64},
			},
		},
		{
			name:       "odd range normalizes to cidr-compatible chunks",
			targetType: model.TargetTypeNetworkRange,
			target:     "10.0.0.1-10.0.0.5",
			maxIPs:     2,
			want: []struct {
				ident   string
				start   string
				end     string
				ipCount int
			}{
				{"10.0.0.1", "10.0.0.1", "10.0.0.1", 1},
				{"10.0.0.2/31", "10.0.0.2", "10.0.0.3", 2},
				{"10.0.0.4/31", "10.0.0.4", "10.0.0.5", 2},
			},
		},
		{
			name:       "unaligned range never emits dash ranges",
			targetType: model.TargetTypeNetworkRange,
			target:     "10.0.0.3-10.0.0.9",
			maxIPs:     4,
			want: []struct {
				ident   string
				start   string
				end     string
				ipCount int
			}{
				{"10.0.0.3", "10.0.0.3", "10.0.0.3", 1},
				{"10.0.0.4/30", "10.0.0.4", "10.0.0.7", 4},
				{"10.0.0.8/31", "10.0.0.8", "10.0.0.9", 2},
			},
		},
		{
			name:       "hostname stays one chunk",
			targetType: "hostname",
			target:     "app.example.com",
			maxIPs:     64,
			want: []struct {
				ident   string
				start   string
				end     string
				ipCount int
			}{{"app.example.com", "", "", 0}},
		},
		{
			name:       "ipv6 cidr stays one chunk for slice one",
			targetType: model.TargetTypeCIDR,
			target:     "2001:db8::/126",
			maxIPs:     64,
			want: []struct {
				ident   string
				start   string
				end     string
				ipCount int
			}{{"2001:db8::/126", "", "", 0}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := splitDiscoveryTarget("scan-1", "tenant-1", &agentID, tc.targetType, tc.target, tc.maxIPs)
			if err != nil {
				t.Fatalf("splitDiscoveryTarget: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("chunks = %d, want %d (%+v)", len(got), len(tc.want), got)
			}
			for i := range got {
				if got[i].ChunkIndex != i {
					t.Errorf("chunk %d index = %d", i, got[i].ChunkIndex)
				}
				if got[i].ScanID != "scan-1" || got[i].TenantID != "tenant-1" {
					t.Errorf("chunk %d scan/tenant = %q/%q", i, got[i].ScanID, got[i].TenantID)
				}
				if got[i].AgentID == nil || *got[i].AgentID != agentID {
					t.Errorf("chunk %d agent = %v", i, got[i].AgentID)
				}
				if got[i].TargetIdentifier != tc.want[i].ident {
					t.Errorf("chunk %d ident = %q, want %q", i, got[i].TargetIdentifier, tc.want[i].ident)
				}
				if deref(got[i].IPStart) != tc.want[i].start || deref(got[i].IPEnd) != tc.want[i].end {
					t.Errorf("chunk %d range = %q-%q, want %q-%q",
						i, deref(got[i].IPStart), deref(got[i].IPEnd), tc.want[i].start, tc.want[i].end)
				}
				if got[i].IPCount != tc.want[i].ipCount {
					t.Errorf("chunk %d ip_count = %d, want %d", i, got[i].IPCount, tc.want[i].ipCount)
				}
			}
		})
	}
}

func TestSplitDiscoveryTargetErrors(t *testing.T) {
	cases := []struct {
		targetType string
		target     string
	}{
		{model.TargetTypeCIDR, "10.0.0.0/not-a-mask"},
		{model.TargetTypeNetworkRange, "10.0.0.5-10.0.0.1"},
		{model.TargetTypeNetworkRange, "10.0.0.1-not-an-ip"},
	}
	for _, tc := range cases {
		if _, err := splitDiscoveryTarget("scan-1", "tenant-1", nil, tc.targetType, tc.target, 64); err == nil {
			t.Errorf("splitDiscoveryTarget(%q, %q) expected error", tc.targetType, tc.target)
		}
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
