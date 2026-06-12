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
	// It must not hang indefinitely; a couple of backoff cycles is plenty.
	if elapsed > 5*time.Second {
		t.Fatalf("pingWithRetry took %s, expected it to give up quickly", elapsed)
	}
}
