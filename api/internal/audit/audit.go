package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// Event represents a single audit event to be persisted.
type Event struct {
	TenantID     string
	EventType    string
	ActorType    string
	ActorID      string
	ResourceType string
	ResourceID   string
	Payload      map[string]any
}

// Writer is the interface for emitting audit events. Implementations
// must be safe for concurrent use.
type Writer interface {
	// Emit enqueues an audit event for persistence. It never blocks the
	// caller beyond a channel send; events are dropped if the queue is full.
	Emit(ctx context.Context, e Event)
	// Close flushes any buffered events and releases resources.
	Close()
}

// --- NoopWriter --------------------------------------------------------

// NoopWriter discards all events. Used in tests and when the feature is
// disabled via AUDIT_EVENTS_ENABLED=false.
type NoopWriter struct{}

func (NoopWriter) Emit(context.Context, Event) {}
func (NoopWriter) Close()                      {}

// --- PostgresWriter ----------------------------------------------------

const (
	chanCap       = 1000
	flushInterval = 50 * time.Millisecond
	batchMax      = 100
)

// PostgresWriter buffers events in a channel and batch-inserts them on a
// background goroutine. Emit never blocks — if the channel is full, the
// event is dropped and a counter incremented.
type PostgresWriter struct {
	db      *sql.DB
	ch      chan Event
	done    chan struct{}
	wg      sync.WaitGroup
	dropped atomic.Int64
}

// NewPostgresWriter creates a writer backed by the given *sql.DB. The
// caller must call Close to flush pending events on shutdown.
func NewPostgresWriter(db *sql.DB) *PostgresWriter {
	w := &PostgresWriter{
		db:   db,
		ch:   make(chan Event, chanCap),
		done: make(chan struct{}),
	}
	w.wg.Add(1)
	go w.flusher()
	return w
}

func (w *PostgresWriter) Emit(ctx context.Context, e Event) {
	// Merge request-scoped metadata (ip/user_agent/actor_email) from ctx into
	// the payload here, in the caller's goroutine — the background flusher runs
	// on a detached context and would not see these values.
	e = enrich(ctx, e)
	select {
	case w.ch <- e:
	default:
		n := w.dropped.Add(1)
		if n%100 == 1 {
			slog.Warn("audit event dropped (queue full)", "total_dropped", n)
		}
	}
}

func (w *PostgresWriter) Close() {
	close(w.ch)
	w.wg.Wait()
	if d := w.dropped.Load(); d > 0 {
		slog.Warn("audit writer closed", "total_dropped", d)
	}
}

func (w *PostgresWriter) flusher() {
	defer w.wg.Done()
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()
	buf := make([]Event, 0, batchMax)
	for {
		select {
		case e, ok := <-w.ch:
			if !ok {
				// Channel closed — flush remaining.
				if len(buf) > 0 {
					w.flush(buf)
				}
				return
			}
			buf = append(buf, e)
			if len(buf) >= batchMax {
				w.flush(buf)
				buf = buf[:0]
			}
		case <-ticker.C:
			if len(buf) > 0 {
				w.flush(buf)
				buf = buf[:0]
			}
		}
	}
}

func (w *PostgresWriter) flush(batch []Event) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		slog.Error("audit flush: begin tx", "error", err)
		return
	}

	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO audit_events (tenant_id, event_type, actor_type, actor_id, resource_type, resource_id, payload)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`)
	if err != nil {
		slog.Error("audit flush: prepare", "error", err)
		_ = tx.Rollback()
		return
	}
	defer stmt.Close()

	for _, e := range batch {
		payload, _ := json.Marshal(e.Payload)
		if len(payload) == 0 {
			payload = []byte("{}")
		}
		var actorID, resourceType, resourceID *string
		if e.ActorID != "" {
			actorID = &e.ActorID
		}
		if e.ResourceType != "" {
			resourceType = &e.ResourceType
		}
		if e.ResourceID != "" {
			resourceID = &e.ResourceID
		}
		if _, err := stmt.ExecContext(ctx, e.TenantID, e.EventType, e.ActorType,
			actorID, resourceType, resourceID, payload); err != nil {
			slog.Error("audit flush: insert", "event_type", e.EventType, "error", err)
		}
	}

	if err := tx.Commit(); err != nil {
		slog.Error("audit flush: commit", "error", err)
	}
}
