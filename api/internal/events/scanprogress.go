package events

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/jtb75/silkstrand/api/internal/model"
)

// PublishScanProgress publishes a "scan_progress" event (#387). It is the one
// place that stamps the envelope (Kind/ResourceType/ResourceID + the top-level
// scan_id the bus filters on), so every emitter — the WSS ingest handlers and
// the chunk scheduler — produces an identical shape. Non-blocking; failures are
// logged and swallowed, like scan_status.
func PublishScanProgress(ctx context.Context, bus Bus, tenantID string, p model.ScanProgress) {
	if bus == nil || tenantID == "" || p.ScanID == "" {
		return
	}
	payload, err := json.Marshal(p)
	if err != nil {
		slog.Warn("scan_progress marshal failed", "scan_id", p.ScanID, "error", err)
		return
	}
	if err := bus.Publish(ctx, Event{
		TenantID:     tenantID,
		Kind:         "scan_progress",
		ResourceType: "scan",
		ResourceID:   p.ScanID,
		OccurredAt:   time.Now().UTC(),
		Payload:      payload,
	}); err != nil {
		slog.Warn("scan_progress publish failed", "scan_id", p.ScanID, "error", err)
	}
}
