package cmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/mritunjaysharma394/llmwiki/internal/db"
	"github.com/mritunjaysharma394/llmwiki/internal/llm"
	"github.com/mritunjaysharma394/llmwiki/internal/queue"
	"github.com/mritunjaysharma394/llmwiki/internal/wiki"
)

// setupWatchEnv mirrors setupMaintainEnv: chdir to a fresh temp dir,
// write a minimal config that includes a [watch] block, loadConfig.
// Returns the wiki root.
func setupWatchEnv(t *testing.T) string {
	t.Helper()
	root := chdirTemp(t)
	resetProviderFlags(t)
	t.Setenv("GEMINI_API_KEY", "test-key-not-used")
	writeMinimalConfig(t, `[llm]
provider = "gemini"
model = "gemini-2.0-flash"

[wiki]
wiki_dir = ".llmwiki/wiki"
raw_dir = ".llmwiki/raw"
db_path = ".llmwiki/wiki.db"

[watch]
dirs = []
debounce_seconds = 2
max_attempts = 3
`)
	for _, d := range []string{".llmwiki/wiki", ".llmwiki/raw", ".llmwiki/answers"} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	if err := loadConfig(); err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	return root
}

// TestWatch_ProducerEnqueuesOnFsnotifyEvent asserts that a Create event
// from fsnotify lands as a pending row in the queue after the debounce
// window elapses.
func TestWatch_ProducerEnqueuesOnFsnotifyEvent(t *testing.T) {
	setupWatchEnv(t)

	srcDir := t.TempDir()
	w, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatalf("fsnotify.NewWatcher: %v", err)
	}
	defer w.Close()
	if err := w.Add(srcDir); err != nil {
		t.Fatalf("watcher.Add: %v", err)
	}

	q, err := queue.New(database.SQL())
	if err != nil {
		t.Fatalf("queue.New: %v", err)
	}

	done := make(chan struct{})
	defer close(done)

	go runWatchProducer(w, q, 50*time.Millisecond, done)

	// Trigger a Create event by writing a file in srcDir.
	path := filepath.Join(srcDir, "paper.md")
	if err := os.WriteFile(path, []byte("# Paper\n"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	// Poll the queue until the row lands or we time out.
	deadline := time.Now().Add(2 * time.Second)
	var found bool
	for time.Now().Before(deadline) {
		it, qerr := q.NextPending()
		if qerr == nil {
			if it.SourceURI == path {
				found = true
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !found {
		t.Fatalf("expected queue row for %q within 2s, never appeared", path)
	}
}

// TestWatch_ProducerDebouncesRapidWrites asserts that several rapid
// writes to the same path inside the debounce window collapse into a
// single queue row, not one per write.
func TestWatch_ProducerDebouncesRapidWrites(t *testing.T) {
	setupWatchEnv(t)

	srcDir := t.TempDir()
	w, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatalf("fsnotify.NewWatcher: %v", err)
	}
	defer w.Close()
	if err := w.Add(srcDir); err != nil {
		t.Fatalf("watcher.Add: %v", err)
	}

	q, err := queue.New(database.SQL())
	if err != nil {
		t.Fatalf("queue.New: %v", err)
	}

	done := make(chan struct{})
	defer close(done)

	go runWatchProducer(w, q, 200*time.Millisecond, done)

	path := filepath.Join(srcDir, "paper.md")
	// 5 quick writes inside the debounce window. Each Write event
	// resets the timer, so only the final fire-after-200ms enqueues.
	for i := 0; i < 5; i++ {
		if err := os.WriteFile(path, []byte("v"), 0644); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Wait past the debounce window; then drain the queue and count.
	time.Sleep(400 * time.Millisecond)
	count := 0
	for {
		it, qerr := q.NextPending()
		if errors.Is(qerr, queue.ErrEmpty) {
			break
		}
		if qerr != nil {
			t.Fatalf("NextPending: %v", qerr)
		}
		if it.SourceURI == path {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("debounce should coalesce 5 rapid writes into 1 enqueue, got %d", count)
	}
}

// TestWatch_ConsumerHappyPath_MarkSuccess: a queue row gets dispatched
// to the ingest seam, the seam returns ok, and the consumer flips the
// row to status='done'.
func TestWatch_ConsumerHappyPath_MarkSuccess(t *testing.T) {
	setupWatchEnv(t)

	q, err := queue.New(database.SQL())
	if err != nil {
		t.Fatalf("queue.New: %v", err)
	}

	id, err := q.Enqueue("/tmp/foo.md")
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	// Stub the ingest seam: pretend ingest succeeded with 2 pages.
	prev := ingestFn
	t.Cleanup(func() { ingestFn = prev })
	var called atomic.Int32
	ingestFn = func(_ context.Context, _ wiki.IngestSourceConfig, _ *db.DB, _ llm.Client, src string, _ wiki.IngestOptions) (wiki.IngestRunResult, error) {
		called.Add(1)
		return wiki.IngestRunResult{
			Source:                src,
			PagesWritten:          2,
			RetroLinkedPages:      1,
			ContradictionsFlagged: 0,
		}, nil
	}

	done := make(chan struct{})
	go runWatchConsumer(context.Background(), q, cfg, 3, done)

	// Wait for the row to flip to 'done'.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		it, _ := q.Get(id)
		if it.Status == queue.StatusDone {
			close(done)
			if called.Load() == 0 {
				t.Fatalf("ingestFn never called despite Status=done")
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	close(done)
	t.Fatalf("queue row never flipped to 'done' within 2s")
}

// TestWatch_ConsumerRetries_OnIngestFailure: ingestFn returns an
// error; the consumer should call MarkRetrying on the first failure
// (since attempts < max). The post-call row reads as status='retrying'
// with attempts=1.
func TestWatch_ConsumerRetries_OnIngestFailure(t *testing.T) {
	setupWatchEnv(t)

	q, err := queue.New(database.SQL())
	if err != nil {
		t.Fatalf("queue.New: %v", err)
	}
	// Long-enough backoff that the row stays in 'retrying' visibly
	// instead of being re-claimed immediately on the next consumer tick.
	q.SetBackoffs([]time.Duration{10 * time.Second, 10 * time.Second, 10 * time.Second})

	id, err := q.Enqueue("/tmp/bad.md")
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	prev := ingestFn
	t.Cleanup(func() { ingestFn = prev })
	ingestFn = func(_ context.Context, _ wiki.IngestSourceConfig, _ *db.DB, _ llm.Client, _ string, _ wiki.IngestOptions) (wiki.IngestRunResult, error) {
		return wiki.IngestRunResult{}, errors.New("synthetic ingest failure")
	}

	done := make(chan struct{})
	// Use a max-attempts of 5 so the first failure does NOT terminate.
	go runWatchConsumer(context.Background(), q, cfg, 5, done)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		it, _ := q.Get(id)
		if it.Status == queue.StatusRetrying && it.Attempts >= 1 {
			close(done)
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	close(done)
	t.Fatalf("queue row never flipped to 'retrying' within 2s")
}

// TestWatch_ConsumerMarksFailedAtCap: when attempts already equals
// (cap-1) and ingest fails again, the consumer should MarkFailed
// (terminal), not MarkRetrying.
func TestWatch_ConsumerMarksFailedAtCap(t *testing.T) {
	setupWatchEnv(t)

	q, err := queue.New(database.SQL())
	if err != nil {
		t.Fatalf("queue.New: %v", err)
	}
	q.SetBackoffs([]time.Duration{1 * time.Millisecond, 1 * time.Millisecond, 1 * time.Millisecond})

	id, err := q.Enqueue("/tmp/cap.md")
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	// Force the row's attempts forward so the next failure trips the cap.
	if _, err := database.SQL().Exec(`UPDATE ingest_queue SET attempts = 2 WHERE id = ?`, id); err != nil {
		t.Fatalf("force attempts: %v", err)
	}

	prev := ingestFn
	t.Cleanup(func() { ingestFn = prev })
	ingestFn = func(_ context.Context, _ wiki.IngestSourceConfig, _ *db.DB, _ llm.Client, _ string, _ wiki.IngestOptions) (wiki.IngestRunResult, error) {
		return wiki.IngestRunResult{}, errors.New("third strike")
	}

	done := make(chan struct{})
	go runWatchConsumer(context.Background(), q, cfg, 3, done)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		it, _ := q.Get(id)
		if it.Status == queue.StatusFailed {
			close(done)
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	close(done)
	t.Fatalf("queue row never flipped to 'failed' within 2s")
}

// TestWatch_ConsumerExitsOnDoneSignal asserts that closing the done
// channel causes the consumer goroutine to return promptly even when
// the queue has work to do. This is the SIGINT/graceful-shutdown path.
func TestWatch_ConsumerExitsOnDoneSignal(t *testing.T) {
	setupWatchEnv(t)

	q, err := queue.New(database.SQL())
	if err != nil {
		t.Fatalf("queue.New: %v", err)
	}

	prev := ingestFn
	t.Cleanup(func() { ingestFn = prev })
	// Block the ingest seam long enough that close(done) has to
	// interrupt the loop *between* dispatches to exit. We don't
	// actually enqueue anything — the consumer should exit on the
	// idle path (NextPending returns ErrEmpty → consumer sleeps →
	// done fires).
	ingestFn = func(_ context.Context, _ wiki.IngestSourceConfig, _ *db.DB, _ llm.Client, _ string, _ wiki.IngestOptions) (wiki.IngestRunResult, error) {
		return wiki.IngestRunResult{}, nil
	}

	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		runWatchConsumer(context.Background(), q, cfg, 3, done)
	}()

	close(done)
	// runWatchConsumer should return promptly. Use a generous timeout
	// to avoid CI flakes.
	doneCh := make(chan struct{})
	go func() { wg.Wait(); close(doneCh) }()
	select {
	case <-doneCh:
	case <-time.After(2 * time.Second):
		t.Fatalf("consumer did not exit after close(done) within 2s")
	}
}

// TestWatch_ResolveDirsRequiresArgOrConfig — bare invocation with no
// arg AND empty [watch] dirs should return a UserError directing the
// user to either pass <dir> or set dirs.
func TestWatch_ResolveDirsRequiresArgOrConfig(t *testing.T) {
	setupWatchEnv(t)
	_, err := resolveWatchDirs(watchCmd, nil, cfg)
	if err == nil {
		t.Fatalf("resolveWatchDirs with no arg + empty config should error")
	}
}

// TestWatch_ResolveDirsArgWins — positional arg takes precedence over
// any config-side dirs.
func TestWatch_ResolveDirsArgWins(t *testing.T) {
	setupWatchEnv(t)
	cfg.Watch.Dirs = []string{"/tmp/from-config"}
	dirs, err := resolveWatchDirs(watchCmd, []string{t.TempDir()}, cfg)
	if err != nil {
		t.Fatalf("resolveWatchDirs: %v", err)
	}
	if len(dirs) != 1 {
		t.Fatalf("want 1 dir, got %d (%v)", len(dirs), dirs)
	}
	if dirs[0] == "/tmp/from-config" {
		t.Fatalf("positional arg should win, got config dir back: %v", dirs)
	}
}
