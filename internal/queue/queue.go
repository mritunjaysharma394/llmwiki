// Package queue is the SQLite-backed crash-resumable work queue that
// drives sub-project 8's `llmwiki watch` ingest loop. It lives in the
// same wiki.db file as everything else (one DB → one truth) and uses
// the v6 migration's `ingest_queue` table.
//
// API contract (Phase A):
//
//	q.Enqueue(uri)            → status='pending'; returns the row id
//	q.NextPending()           → atomically claims the oldest 'pending'
//	                            row whose backoff window has elapsed,
//	                            flips it to 'running', returns it
//	q.MarkSuccess(id)         → status='done'
//	q.MarkRetrying(id, err)   → attempts++, status='retrying',
//	                            next_attempt_at = now + backoff(attempts)
//	q.MarkFailed(id, err)     → status='failed' (terminal)
//
// Crash semantics: a `watch` restart calls NextPending repeatedly to
// drain whatever was 'pending', 'retrying' (whose window has elapsed),
// or 'running' (left half-claimed by a crashed predecessor). 'running'
// rows are recoverable by NextPending after a stale window so a crashed
// worker doesn't strand work.
//
// Retry policy (per plan §5): 3 attempts with exponential backoff —
// 5s, 30s, 5min — then 'failed'. The schedule is encoded as a slice
// `defaultBackoffs` so tests can swap a faster table.
//
// The queue defensively `CREATE TABLE IF NOT EXISTS` on New() so it is
// usable against a raw *sql.DB that pre-dates the v6 migration (for
// isolated tests). Production callers always go through db.Open() which
// runs the v6 migration; the defensive create is a no-op there.
package queue

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// Status values are stored as TEXT in the ingest_queue table. The set
// is closed; New() enforces the schema and tests assert against these
// constants.
const (
	StatusPending  = "pending"
	StatusRunning  = "running"
	StatusRetrying = "retrying"
	StatusDone     = "done"
	StatusFailed   = "failed"
)

// MaxAttempts is the default cap from plan §5 ("3 attempts with
// exponential backoff"). Callers may override per-call by reading
// Attempts on the returned Item and deciding to MarkFailed earlier.
const MaxAttempts = 3

// defaultBackoffs is the per-attempt wait schedule. After the Nth
// failure we wait defaultBackoffs[N-1] before the row is eligible to be
// returned by NextPending again. After len(defaultBackoffs) failures
// the caller MarkFailed's the row.
var defaultBackoffs = []time.Duration{
	5 * time.Second,
	30 * time.Second,
	5 * time.Minute,
}

// staleRunningWindow is how long a 'running' row may sit before
// NextPending will recover it on a subsequent call. Tuned conservatively:
// long enough that a healthy ingest pass (which can be tens of seconds
// to a minute on a large source) never trips it, short enough that a
// crashed watch process doesn't strand work for more than a few
// minutes.
var staleRunningWindow = 10 * time.Minute

// ErrEmpty is returned by NextPending when no row is eligible to be
// claimed. Callers (watch's consumer goroutine) sleep and retry on
// this error.
var ErrEmpty = errors.New("queue: no pending work")

// Queue is the public surface. It wraps a *sql.DB; callers either pass
// the same handle that internal/db.DB owns (production) or open a
// dedicated handle for isolation in tests.
type Queue struct {
	sql *sql.DB
	now func() time.Time // overridable in tests for deterministic backoff windows

	backoffs       []time.Duration // overridable in tests
	staleRunningTo time.Duration   // overridable in tests
}

// Item is one row of the queue surfaced to callers. Status is the
// post-NextPending value ('running'); Attempts is the count BEFORE
// this dispatch (so a fresh enqueue surfaces Attempts=0; the first
// retry surfaces Attempts=1).
type Item struct {
	ID          int64
	SourceURI   string
	Attempts    int
	LastError   string
	Status      string
	EnqueuedAt  time.Time
	UpdatedAt   time.Time
	NextAttempt time.Time // zero ⇒ eligible immediately
}

// New wraps an existing *sql.DB and ensures the ingest_queue table is
// present. Idempotent against the v6 migration's CREATE TABLE: both
// statements use IF NOT EXISTS and identical column shapes.
func New(s *sql.DB) (*Queue, error) {
	q := &Queue{
		sql:            s,
		now:            func() time.Time { return time.Now().UTC() },
		backoffs:       defaultBackoffs,
		staleRunningTo: staleRunningWindow,
	}
	if err := q.ensureTable(); err != nil {
		return nil, err
	}
	return q, nil
}

func (q *Queue) ensureTable() error {
	// PRAGMA busy_timeout makes concurrent producers/consumers wait
	// instead of fast-failing on the global SQLite write lock. The
	// underlying *sql.DB may have multiple connections; busy_timeout is
	// per-connection but applying it here is best-effort (the production
	// db.Open path applies its own pragmas; this is the test-fixture
	// safety net).
	if _, err := q.sql.Exec(`PRAGMA busy_timeout = 5000`); err != nil {
		return fmt.Errorf("set busy_timeout: %w", err)
	}
	// Same DDL the v6 migration emits; IF NOT EXISTS makes this a no-op
	// against a properly-migrated db.DB and a one-shot for raw test
	// fixtures.
	_, err := q.sql.Exec(`CREATE TABLE IF NOT EXISTS ingest_queue (
		id INTEGER PRIMARY KEY,
		source_uri TEXT NOT NULL,
		attempts INTEGER NOT NULL DEFAULT 0,
		last_error TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL DEFAULT 'pending',
		enqueued_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		next_attempt_at DATETIME
	)`)
	if err != nil {
		return fmt.Errorf("ensure ingest_queue: %w", err)
	}
	if _, err := q.sql.Exec(`CREATE INDEX IF NOT EXISTS idx_ingest_queue_status ON ingest_queue(status)`); err != nil {
		return fmt.Errorf("ensure idx_ingest_queue_status: %w", err)
	}
	return nil
}

// Enqueue records a new pending work item for the given source URI.
// Same URI may be enqueued multiple times — the queue does not dedup
// (the watch debouncer upstream handles that, and the user may
// legitimately re-trigger a re-ingest). Returns the new row id.
func (q *Queue) Enqueue(uri string) (int64, error) {
	if uri == "" {
		return 0, fmt.Errorf("queue: empty source uri")
	}
	now := q.now().Format(time.RFC3339)
	res, err := q.sql.Exec(
		`INSERT INTO ingest_queue (source_uri, status, enqueued_at, updated_at)
		VALUES (?, ?, ?, ?)`,
		uri, StatusPending, now, now,
	)
	if err != nil {
		return 0, fmt.Errorf("enqueue: %w", err)
	}
	return res.LastInsertId()
}

// NextPending atomically claims the oldest eligible row (status =
// 'pending', or 'retrying'/'running' whose backoff window has elapsed),
// flips it to 'running', and returns it. Returns ErrEmpty if no row
// qualifies.
//
// "Eligible" means:
//   - status = 'pending' (always)
//   - status = 'retrying' AND next_attempt_at <= now
//   - status = 'running' AND updated_at <= now - staleRunningWindow
//     (a crashed worker left this row claimed; recover it)
//
// The transition is wrapped in a transaction with SELECT … LIMIT 1
// followed by UPDATE … WHERE id=? AND status=<observed>. SQLite's
// default serialized isolation means concurrent producers on the same
// *sql.DB don't double-claim a row.
func (q *Queue) NextPending() (Item, error) {
	now := q.now()
	staleCut := now.Add(-q.staleRunningTo)

	tx, err := q.sql.Begin()
	if err != nil {
		return Item{}, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	row := tx.QueryRow(`
		SELECT id, source_uri, attempts, last_error, status,
		       enqueued_at, updated_at, COALESCE(next_attempt_at, '')
		FROM ingest_queue
		WHERE status = 'pending'
		   OR (status = 'retrying' AND (next_attempt_at IS NULL OR next_attempt_at <= ?))
		   OR (status = 'running'  AND updated_at <= ?)
		ORDER BY id
		LIMIT 1`,
		now.Format(time.RFC3339), staleCut.Format(time.RFC3339))

	var it Item
	var enq, upd, nxt string
	if err := row.Scan(&it.ID, &it.SourceURI, &it.Attempts, &it.LastError, &it.Status, &enq, &upd, &nxt); err != nil {
		if err == sql.ErrNoRows {
			return Item{}, ErrEmpty
		}
		return Item{}, fmt.Errorf("select next: %w", err)
	}
	it.EnqueuedAt, _ = time.Parse(time.RFC3339, enq)
	it.UpdatedAt, _ = time.Parse(time.RFC3339, upd)
	if nxt != "" {
		it.NextAttempt, _ = time.Parse(time.RFC3339, nxt)
	}

	// Compare-and-swap on the observed status so a concurrent producer
	// who already claimed this row in another tx loses gracefully.
	res, err := tx.Exec(
		`UPDATE ingest_queue SET status = ?, updated_at = ? WHERE id = ? AND status = ?`,
		StatusRunning, now.Format(time.RFC3339), it.ID, it.Status,
	)
	if err != nil {
		return Item{}, fmt.Errorf("claim: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		// Lost the race; surface ErrEmpty so caller retries.
		return Item{}, ErrEmpty
	}
	if err := tx.Commit(); err != nil {
		return Item{}, fmt.Errorf("commit claim: %w", err)
	}
	it.Status = StatusRunning
	it.UpdatedAt = now
	return it, nil
}

// MarkSuccess flips a row to 'done'. Idempotent: a second call against
// an already-done row is a no-op (still returns nil) since 'done' is
// terminal.
func (q *Queue) MarkSuccess(id int64) error {
	now := q.now().Format(time.RFC3339)
	_, err := q.sql.Exec(
		`UPDATE ingest_queue SET status = ?, updated_at = ?, last_error = '' WHERE id = ?`,
		StatusDone, now, id,
	)
	if err != nil {
		return fmt.Errorf("mark success: %w", err)
	}
	return nil
}

// MarkRetrying increments attempts, records the error, and sets
// next_attempt_at to now + backoffs[attempts-1]. If attempts has
// already exceeded len(backoffs), the caller should MarkFailed
// instead — MarkRetrying clamps to the last backoff window so a
// caller who slips past the cap doesn't get a panic, but the
// recommended posture is to check Attempts on the returned Item and
// branch.
func (q *Queue) MarkRetrying(id int64, errMsg string) error {
	now := q.now()

	// Read current attempts so the backoff is correct without a
	// follow-up SELECT.
	var attempts int
	if err := q.sql.QueryRow(`SELECT attempts FROM ingest_queue WHERE id = ?`, id).Scan(&attempts); err != nil {
		return fmt.Errorf("read attempts: %w", err)
	}
	attempts++ // post-increment: this attempt just failed.

	idx := attempts - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(q.backoffs) {
		idx = len(q.backoffs) - 1
	}
	next := now.Add(q.backoffs[idx])

	_, err := q.sql.Exec(
		`UPDATE ingest_queue SET status = ?, attempts = ?, last_error = ?, updated_at = ?, next_attempt_at = ? WHERE id = ?`,
		StatusRetrying, attempts, errMsg, now.Format(time.RFC3339), next.Format(time.RFC3339), id,
	)
	if err != nil {
		return fmt.Errorf("mark retrying: %w", err)
	}
	return nil
}

// MarkFailed flips a row to terminal 'failed' state and records the
// final error. The caller (watch's consumer) typically calls this
// after MarkRetrying has returned an item with attempts >= MaxAttempts.
func (q *Queue) MarkFailed(id int64, errMsg string) error {
	now := q.now().Format(time.RFC3339)
	_, err := q.sql.Exec(
		`UPDATE ingest_queue SET status = ?, last_error = ?, updated_at = ? WHERE id = ?`,
		StatusFailed, errMsg, now, id,
	)
	if err != nil {
		return fmt.Errorf("mark failed: %w", err)
	}
	return nil
}

// Get reads a single row by id. Returns ErrEmpty when not found so
// callers don't have to special-case sql.ErrNoRows.
func (q *Queue) Get(id int64) (Item, error) {
	row := q.sql.QueryRow(`
		SELECT id, source_uri, attempts, last_error, status,
		       enqueued_at, updated_at, COALESCE(next_attempt_at, '')
		FROM ingest_queue WHERE id = ?`, id)
	var it Item
	var enq, upd, nxt string
	if err := row.Scan(&it.ID, &it.SourceURI, &it.Attempts, &it.LastError, &it.Status, &enq, &upd, &nxt); err != nil {
		if err == sql.ErrNoRows {
			return Item{}, ErrEmpty
		}
		return Item{}, fmt.Errorf("get: %w", err)
	}
	it.EnqueuedAt, _ = time.Parse(time.RFC3339, enq)
	it.UpdatedAt, _ = time.Parse(time.RFC3339, upd)
	if nxt != "" {
		it.NextAttempt, _ = time.Parse(time.RFC3339, nxt)
	}
	return it, nil
}

// SetClock overrides the queue's clock for testing. Production callers
// never use this. The setter accepts time.Time-returning closures so
// tests can advance a virtual clock between calls.
func (q *Queue) SetClock(now func() time.Time) { q.now = now }

// SetBackoffs overrides the per-attempt backoff schedule for tests.
// Production callers use defaultBackoffs from plan §5.
func (q *Queue) SetBackoffs(b []time.Duration) { q.backoffs = b }

// SetStaleRunningWindow overrides the running-row recovery window for
// tests. Production callers use the package default.
func (q *Queue) SetStaleRunningWindow(d time.Duration) { q.staleRunningTo = d }
