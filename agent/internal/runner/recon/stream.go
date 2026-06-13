package recon

import (
	"sync"
	"time"

	"github.com/jtb75/silkstrand/agent/internal/tunnel"
)

// EmitFunc is the callback the recon runner uses to publish messages
// over the WSS tunnel mid-scan. Provided by the agent main loop.
type EmitFunc func(msgType string, payload any) error

// Batcher groups DiscoveredAssetUpsert findings and flushes on size or
// time, whichever hits first. Per the agent plan §5: max(N=10, T=2s)
// for naabu/httpx; nuclei results flush per-asset (batch size 1)
// because CVE results arrive late and large.
type Batcher struct {
	scanID     string
	chunkID    string
	chunkIndex int
	emit       EmitFunc
	maxSize    int
	maxAge     time.Duration
	stage      string

	mu       sync.Mutex
	buf      []tunnel.DiscoveredAssetUpsert
	seq      int
	timer    *time.Timer
	stopOnce sync.Once
	stopped  bool
}

// NewBatcher creates a batcher for one (scan_id, stage) pair.
func NewBatcher(scanID, stage string, emit EmitFunc, maxSize int, maxAge time.Duration) *Batcher {
	return &Batcher{
		scanID:  scanID,
		stage:   stage,
		emit:    emit,
		maxSize: maxSize,
		maxAge:  maxAge,
	}
}

// WithChunk tags every emitted batch with the parent scan chunk. Empty chunkID
// preserves the legacy one-scan/one-directive protocol.
func (b *Batcher) WithChunk(chunkID string, chunkIndex int) *Batcher {
	b.chunkID = chunkID
	b.chunkIndex = chunkIndex
	return b
}

// Add queues a finding. Triggers a flush if the batch is full.
func (b *Batcher) Add(a tunnel.DiscoveredAssetUpsert) {
	b.mu.Lock()
	if b.stopped {
		b.mu.Unlock()
		return
	}
	b.buf = append(b.buf, a)
	if b.timer == nil {
		b.timer = time.AfterFunc(b.maxAge, b.flushFromTimer)
	}
	if len(b.buf) >= b.maxSize {
		batch := b.takeLocked()
		b.mu.Unlock()
		b.send(batch)
		return
	}
	b.mu.Unlock()
}

// Flush sends any pending batch immediately.
func (b *Batcher) Flush() {
	b.mu.Lock()
	batch := b.takeLocked()
	b.mu.Unlock()
	if len(batch) > 0 {
		b.send(batch)
	}
}

// Stop flushes once and disables further Adds.
func (b *Batcher) Stop() {
	b.stopOnce.Do(func() {
		b.mu.Lock()
		b.stopped = true
		batch := b.takeLocked()
		b.mu.Unlock()
		if len(batch) > 0 {
			b.send(batch)
		}
	})
}

func (b *Batcher) flushFromTimer() {
	b.mu.Lock()
	batch := b.takeLocked()
	b.mu.Unlock()
	if len(batch) > 0 {
		b.send(batch)
	}
}

// takeLocked drains the buffer and resets the timer. Caller holds b.mu.
func (b *Batcher) takeLocked() []tunnel.DiscoveredAssetUpsert {
	if b.timer != nil {
		b.timer.Stop()
		b.timer = nil
	}
	if len(b.buf) == 0 {
		return nil
	}
	out := b.buf
	b.buf = nil
	return out
}

func (b *Batcher) send(batch []tunnel.DiscoveredAssetUpsert) {
	if b.emit == nil {
		return
	}
	b.mu.Lock()
	b.seq++
	seq := b.seq
	b.mu.Unlock()
	payload := tunnel.AssetDiscoveredPayload{
		ScanID:     b.scanID,
		ChunkID:    b.chunkID,
		ChunkIndex: b.chunkIndex,
		BatchSeq:   seq,
		Stage:      b.stage,
		Assets:     batch,
	}
	_ = b.emit(tunnel.TypeAssetDiscovered, payload)
}
