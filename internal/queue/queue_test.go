package queue

import (
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// openTestDB returns a fresh *sql.DB rooted in t.TempDir(). New() will
// CREATE TABLE the queue schema so we don't need the v6 migration
// fixture here — that's exercised in internal/db's own test suite.
//
// Concurrency note: the connection pool is limited to 1 so the
// concurrent-producer / concurrent-consumer tests don't hit SQLITE_BUSY
// across pool members. modernc/sqlite's PRAGMA busy_timeout is
// per-connection, so a single-conn pool is the simplest deterministic
// fixture. Production db.Open() callers run the same way in v0.7;
// Phase B/E will tune this if real concurrency becomes a bottleneck.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	s, err := sql.Open("sqlite", filepath.Join(dir, "wiki.db"))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	s.SetMaxOpenConns(1)
	t.Cleanup(func() { s.Close() })
	return s
}

func newQueue(t *testing.T) *Queue {
	t.Helper()
	q, err := New(openTestDB(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return q
}

func TestNew_CreatesTable(t *testing.T) {
	s := openTestDB(t)
	if _, err := New(s); err != nil {
		t.Fatalf("New: %v", err)
	}
	var name string
	if err := s.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='ingest_queue'`).Scan(&name); err != nil {
		t.Errorf("ingest_queue table not created: %v", err)
	}
}

func TestNew_Idempotent(t *testing.T) {
	s := openTestDB(t)
	if _, err := New(s); err != nil {
		t.Fatalf("first New: %v", err)
	}
	if _, err := New(s); err != nil {
		t.Fatalf("second New: %v", err)
	}
}

func TestEnqueue_AssignsIDAndPendingStatus(t *testing.T) {
	q := newQueue(t)
	id, err := q.Enqueue("file:///x")
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if id == 0 {
		t.Error("Enqueue returned id 0")
	}
	got, err := q.Get(id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != StatusPending {
		t.Errorf("status = %q, want pending", got.Status)
	}
	if got.SourceURI != "file:///x" {
		t.Errorf("uri = %q", got.SourceURI)
	}
	if got.Attempts != 0 {
		t.Errorf("attempts = %d, want 0", got.Attempts)
	}
}

func TestEnqueue_RejectsEmptyURI(t *testing.T) {
	q := newQueue(t)
	if _, err := q.Enqueue(""); err == nil {
		t.Error("Enqueue(\"\") should error")
	}
}

func TestNextPending_ReturnsErrEmptyWhenIdle(t *testing.T) {
	q := newQueue(t)
	_, err := q.NextPending()
	if !errors.Is(err, ErrEmpty) {
		t.Errorf("err = %v, want ErrEmpty", err)
	}
}

func TestNextPending_ClaimsOldestFirst(t *testing.T) {
	q := newQueue(t)
	// Use a virtual clock so enqueue timestamps are strictly ordered.
	clock := time.Date(2026, 5, 5, 0, 0, 0, 0, time.UTC)
	q.SetClock(func() time.Time { return clock })

	id1, _ := q.Enqueue("a")
	clock = clock.Add(time.Second)
	id2, _ := q.Enqueue("b")

	first, err := q.NextPending()
	if err != nil {
		t.Fatalf("NextPending: %v", err)
	}
	if first.ID != id1 {
		t.Errorf("first claim = %d, want %d", first.ID, id1)
	}
	if first.Status != StatusRunning {
		t.Errorf("status = %q, want running", first.Status)
	}

	second, err := q.NextPending()
	if err != nil {
		t.Fatalf("second NextPending: %v", err)
	}
	if second.ID != id2 {
		t.Errorf("second claim = %d, want %d", second.ID, id2)
	}

	if _, err := q.NextPending(); !errors.Is(err, ErrEmpty) {
		t.Errorf("third NextPending err = %v, want ErrEmpty", err)
	}
}

func TestMarkSuccess_TerminatesRow(t *testing.T) {
	q := newQueue(t)
	id, _ := q.Enqueue("a")
	it, _ := q.NextPending()
	if err := q.MarkSuccess(it.ID); err != nil {
		t.Fatalf("MarkSuccess: %v", err)
	}
	got, _ := q.Get(id)
	if got.Status != StatusDone {
		t.Errorf("status = %q, want done", got.Status)
	}
	if _, err := q.NextPending(); !errors.Is(err, ErrEmpty) {
		t.Errorf("done row should not re-emerge: %v", err)
	}
}

func TestMarkRetrying_AppliesBackoff(t *testing.T) {
	q := newQueue(t)
	clock := time.Date(2026, 5, 5, 0, 0, 0, 0, time.UTC)
	q.SetClock(func() time.Time { return clock })
	q.SetBackoffs([]time.Duration{5 * time.Second, 30 * time.Second, 5 * time.Minute})

	id, _ := q.Enqueue("a")
	it, _ := q.NextPending()
	if err := q.MarkRetrying(it.ID, "first failure"); err != nil {
		t.Fatalf("MarkRetrying: %v", err)
	}
	got, _ := q.Get(id)
	if got.Status != StatusRetrying {
		t.Errorf("status = %q, want retrying", got.Status)
	}
	if got.Attempts != 1 {
		t.Errorf("attempts = %d, want 1", got.Attempts)
	}
	if got.LastError != "first failure" {
		t.Errorf("last_error = %q", got.LastError)
	}
	wantNext := clock.Add(5 * time.Second)
	if !got.NextAttempt.Equal(wantNext) {
		t.Errorf("next_attempt_at = %v, want %v", got.NextAttempt, wantNext)
	}

	// Backoff window not yet elapsed → row stays invisible.
	if _, err := q.NextPending(); !errors.Is(err, ErrEmpty) {
		t.Errorf("retrying row in backoff should not surface: %v", err)
	}

	// Advance past the backoff window.
	clock = clock.Add(6 * time.Second)
	it2, err := q.NextPending()
	if err != nil {
		t.Fatalf("NextPending after backoff: %v", err)
	}
	if it2.ID != id {
		t.Errorf("re-claim id = %d, want %d", it2.ID, id)
	}
	if it2.Attempts != 1 {
		t.Errorf("attempts on re-claim = %d, want 1", it2.Attempts)
	}

	// Second failure → backoff[1] = 30s.
	if err := q.MarkRetrying(it2.ID, "second failure"); err != nil {
		t.Fatalf("MarkRetrying #2: %v", err)
	}
	got, _ = q.Get(id)
	if got.Attempts != 2 {
		t.Errorf("attempts = %d, want 2", got.Attempts)
	}
	wantNext2 := clock.Add(30 * time.Second)
	if !got.NextAttempt.Equal(wantNext2) {
		t.Errorf("second next_attempt_at = %v, want %v", got.NextAttempt, wantNext2)
	}
}

func TestMarkRetrying_ClampsBackoffPastTable(t *testing.T) {
	q := newQueue(t)
	clock := time.Date(2026, 5, 5, 0, 0, 0, 0, time.UTC)
	q.SetClock(func() time.Time { return clock })
	q.SetBackoffs([]time.Duration{1 * time.Second, 2 * time.Second})

	id, _ := q.Enqueue("a")

	// Drive attempts past len(backoffs) and confirm we don't panic.
	for i := 0; i < 4; i++ {
		clock = clock.Add(10 * time.Second)
		it, err := q.NextPending()
		if err != nil {
			t.Fatalf("NextPending iter %d: %v", i, err)
		}
		if err := q.MarkRetrying(it.ID, "fail"); err != nil {
			t.Fatalf("MarkRetrying iter %d: %v", i, err)
		}
	}
	got, _ := q.Get(id)
	if got.Attempts != 4 {
		t.Errorf("attempts = %d, want 4", got.Attempts)
	}
	// Last backoff should be clamped to the final entry (2s).
	wantNext := clock.Add(2 * time.Second)
	if !got.NextAttempt.Equal(wantNext) {
		t.Errorf("clamped next_attempt_at = %v, want %v", got.NextAttempt, wantNext)
	}
}

func TestMarkFailed_TerminatesRow(t *testing.T) {
	q := newQueue(t)
	id, _ := q.Enqueue("a")
	it, _ := q.NextPending()
	if err := q.MarkFailed(it.ID, "give up"); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}
	got, _ := q.Get(id)
	if got.Status != StatusFailed {
		t.Errorf("status = %q, want failed", got.Status)
	}
	if got.LastError != "give up" {
		t.Errorf("last_error = %q", got.LastError)
	}
	if _, err := q.NextPending(); !errors.Is(err, ErrEmpty) {
		t.Errorf("failed row should be terminal: %v", err)
	}
}

// TestCrashResume — a producer enqueues two rows, the consumer claims
// the first row (status='running'), then crashes (we just stop calling
// methods). On restart the new Queue instance pointed at the same DB
// must still see the first row as recoverable (after the stale window)
// AND must see the second pending row immediately. This is the
// crash-resume invariant from plan §5.
func TestCrashResume_RecoversRunningAndPending(t *testing.T) {
	s := openTestDB(t)
	q1, err := New(s)
	if err != nil {
		t.Fatalf("New q1: %v", err)
	}
	clock := time.Date(2026, 5, 5, 0, 0, 0, 0, time.UTC)
	q1.SetClock(func() time.Time { return clock })
	q1.SetStaleRunningWindow(30 * time.Second)

	id1, _ := q1.Enqueue("a")
	id2, _ := q1.Enqueue("b")

	// Claim id1 → status=running.
	it, err := q1.NextPending()
	if err != nil {
		t.Fatalf("NextPending: %v", err)
	}
	if it.ID != id1 {
		t.Fatalf("claimed %d, want %d", it.ID, id1)
	}
	// Simulate crash: drop q1 and create q2 against the same *sql.DB.
	q2, err := New(s)
	if err != nil {
		t.Fatalf("New q2: %v", err)
	}
	// Advance clock past the stale-running window.
	clock = clock.Add(45 * time.Second)
	q2.SetClock(func() time.Time { return clock })
	q2.SetStaleRunningWindow(30 * time.Second)

	// First claim: the recovered id1 (oldest).
	got1, err := q2.NextPending()
	if err != nil {
		t.Fatalf("recover NextPending: %v", err)
	}
	if got1.ID != id1 {
		t.Errorf("recovered id = %d, want %d", got1.ID, id1)
	}
	// Second claim: the never-claimed id2.
	got2, err := q2.NextPending()
	if err != nil {
		t.Fatalf("second NextPending: %v", err)
	}
	if got2.ID != id2 {
		t.Errorf("pending id = %d, want %d", got2.ID, id2)
	}
}

// TestConcurrentProducers — N goroutines call Enqueue on the same
// queue; the table ends up with exactly N rows and ids are unique.
// This is the concurrent-producer case from the brief.
func TestConcurrentProducers(t *testing.T) {
	q := newQueue(t)
	const N = 50
	var wg sync.WaitGroup
	wg.Add(N)
	ids := make(chan int64, N)
	for i := 0; i < N; i++ {
		go func(i int) {
			defer wg.Done()
			id, err := q.Enqueue("file:///x")
			if err != nil {
				t.Errorf("Enqueue: %v", err)
				return
			}
			ids <- id
		}(i)
	}
	wg.Wait()
	close(ids)
	seen := map[int64]bool{}
	for id := range ids {
		if seen[id] {
			t.Errorf("duplicate id %d", id)
		}
		seen[id] = true
	}
	if len(seen) != N {
		t.Errorf("got %d distinct ids, want %d", len(seen), N)
	}
}

// TestConcurrentConsumers — N rows, M goroutines consuming via
// NextPending. Every row is claimed exactly once; total claims = N.
// SQLite serializes writes so the CAS-on-status pattern in NextPending
// must hold.
func TestConcurrentConsumers(t *testing.T) {
	q := newQueue(t)
	const N = 30
	for i := 0; i < N; i++ {
		if _, err := q.Enqueue("file:///x"); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
	}
	const M = 8
	var wg sync.WaitGroup
	wg.Add(M)
	claimed := make(chan int64, N*2)
	for w := 0; w < M; w++ {
		go func() {
			defer wg.Done()
			for {
				it, err := q.NextPending()
				if errors.Is(err, ErrEmpty) {
					return
				}
				if err != nil {
					t.Errorf("NextPending: %v", err)
					return
				}
				claimed <- it.ID
				if err := q.MarkSuccess(it.ID); err != nil {
					t.Errorf("MarkSuccess: %v", err)
				}
			}
		}()
	}
	wg.Wait()
	close(claimed)
	seen := map[int64]bool{}
	for id := range claimed {
		if seen[id] {
			t.Errorf("row %d claimed twice", id)
		}
		seen[id] = true
	}
	if len(seen) != N {
		t.Errorf("claimed %d, want %d", len(seen), N)
	}
}

// TestStatusConstants — guard against silent renames.
func TestStatusConstants(t *testing.T) {
	for _, s := range []string{StatusPending, StatusRunning, StatusRetrying, StatusDone, StatusFailed} {
		if s == "" {
			t.Error("status constant is empty")
		}
	}
}
