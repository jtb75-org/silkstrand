package store

import (
	"database/sql"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// TestPingWithRetryTimesOut points at a dead port so every ping is refused,
// then asserts pingWithRetry gives up within a bounded time and returns the
// underlying error rather than hanging or succeeding.
//
// maxWait (50ms) is deliberately shorter than the first backoff (500ms): a
// ping fails almost instantly, so the function must spend the rest of its time
// in the backoff wait. This catches the regression nara flagged — if the sleep
// were not capped to the deadline it would block a full 500ms, ~10x maxWait.
func TestPingWithRetryTimesOut(t *testing.T) {
	db, err := sql.Open("pgx", "postgres://u:p@127.0.0.1:1/none?sslmode=disable&connect_timeout=1")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()

	start := time.Now()
	err = pingWithRetry(db, 50*time.Millisecond)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected an error pinging a dead port, got nil")
	}
	// The whole operation — including the backoff sleep — must be bounded by
	// maxWait. Allow generous slack for scheduling, but well under the 500ms
	// first backoff that an uncapped sleep would incur.
	if elapsed > 300*time.Millisecond {
		t.Fatalf("pingWithRetry took %s for a 50ms deadline; sleep is not deadline-bounded", elapsed)
	}
}
