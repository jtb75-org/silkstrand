package model

import "testing"

// ScanRollup is the single source of truth shared by the REST snapshot and the
// scan_progress stream (#387), so its normalization must be exact: chunked
// scans report real counts, non-chunked discovery is one implicit unit, and
// compliance has no chunk model.
func TestScanRollup(t *testing.T) {
	tests := []struct {
		name                            string
		scanType, status                string
		inTotal, inCompleted, inFailed  int
		wantTotal, wantCompleted, wantF int
	}{
		{
			name: "chunked running", scanType: ScanTypeDiscovery, status: ScanStatusRunning,
			inTotal: 4, inCompleted: 1, inFailed: 0,
			wantTotal: 4, wantCompleted: 1, wantF: 0,
		},
		{
			name: "chunked all complete", scanType: ScanTypeDiscovery, status: ScanStatusCompleted,
			inTotal: 4, inCompleted: 4, inFailed: 0,
			wantTotal: 4, wantCompleted: 4, wantF: 0,
		},
		{
			name: "chunked with a failure", scanType: ScanTypeDiscovery, status: ScanStatusFailed,
			inTotal: 4, inCompleted: 3, inFailed: 1,
			wantTotal: 4, wantCompleted: 3, wantF: 1,
		},
		{
			name: "non-chunked discovery running is one implicit unit", scanType: ScanTypeDiscovery, status: ScanStatusRunning,
			inTotal:   0,
			wantTotal: 1, wantCompleted: 0, wantF: 0,
		},
		{
			name: "non-chunked discovery completed", scanType: ScanTypeDiscovery, status: ScanStatusCompleted,
			inTotal:   0,
			wantTotal: 1, wantCompleted: 1, wantF: 0,
		},
		{
			name: "non-chunked discovery failed", scanType: ScanTypeDiscovery, status: ScanStatusFailed,
			inTotal:   0,
			wantTotal: 1, wantCompleted: 0, wantF: 1,
		},
		{
			name: "compliance completed has no chunk model", scanType: ScanTypeCompliance, status: ScanStatusCompleted,
			inTotal:   0,
			wantTotal: 0, wantCompleted: 0, wantF: 0,
		},
		{
			name: "compliance failed has no chunk model", scanType: ScanTypeCompliance, status: ScanStatusFailed,
			inTotal:   0,
			wantTotal: 0, wantCompleted: 0, wantF: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			total, completed, failed := ScanRollup(tt.scanType, tt.status, tt.inTotal, tt.inCompleted, tt.inFailed)
			if total != tt.wantTotal || completed != tt.wantCompleted || failed != tt.wantF {
				t.Errorf("ScanRollup = (%d,%d,%d), want (%d,%d,%d)",
					total, completed, failed, tt.wantTotal, tt.wantCompleted, tt.wantF)
			}
		})
	}
}
