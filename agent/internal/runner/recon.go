package runner

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jtb75/silkstrand/agent/internal/runner/recon"
	"github.com/jtb75/silkstrand/agent/internal/tunnel"
)

// ReconRunner orchestrates the recon pipeline (naabu → httpx → nuclei).
// Implements Runner. Streams asset_discovered batches over the tunnel
// via the EmitFunc handed to the constructor.
type ReconRunner struct {
	emit recon.EmitFunc
}

// NewReconRunner constructs a recon runner. emit is the callback the
// runner uses to ship interim asset_discovered messages back to the
// API. Typically this closes over tun.Send.
func NewReconRunner(emit recon.EmitFunc) *ReconRunner {
	return &ReconRunner{emit: emit}
}

// Run is part of the Runner interface but not used for recon — the
// existing RunRequest envelope lacks scan_id / target_identifier, which
// the recon pipeline needs. The agent main loop dispatches discovery
// directives to ReconRun directly.
func (r *ReconRunner) Run(_ context.Context, _ RunRequest) (json.RawMessage, error) {
	return nil, fmt.Errorf("ReconRunner.Run not used directly; main dispatches to ReconRun")
}

// ReconRun is the entry point used by main.go for discovery directives.
// It bypasses the RunRequest envelope (which lacks scan_id and
// target_identifier fields) and consumes the directive directly.
func ReconRun(ctx context.Context, d tunnel.DirectivePayload, emit recon.EmitFunc) (*recon.PipelineResult, error) {
	return recon.Run(ctx, recon.PipelineRequest{
		ScanID:           d.ScanID,
		ChunkID:          d.ChunkID,
		ChunkIndex:       d.ChunkIndex,
		TargetIdentifier: d.TargetIdentifier,
		TargetConfig:     d.TargetConfig,
		Emit:             emit,
	})
}
