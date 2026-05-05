// Package cmd — watch.go
//
// `llmwiki watch <dir>` is the sub-project 8 Phase E fsnotify watcher.
// It turns "drop a file in this folder" into "wiki updates itself"
// (plan §"Six design calls #5"). The watcher is the single feature that
// converts llmwiki from "CLI tool" to "living wiki" as a UX shift.
//
// Architecture:
//
//	producer goroutine
//	  └── fsnotify.Watcher.Events  → debounce 2s per path → q.Enqueue
//	consumer goroutine
//	  └── q.NextPending  → ingestFn (wiki.IngestSource)
//	                       ├── ok          → q.MarkSuccess
//	                       └── err         → q.MarkRetrying  (attempts < cap)
//	                                          q.MarkFailed   (attempts ≥ cap)
//	signal handler
//	  └── SIGINT/SIGTERM → close(done) → both goroutines drain → exit 0
//
// Crash-resume: on startup we drain whatever the queue picks up via
// NextPending — any 'pending' / 'retrying-window-elapsed' / 'running-stale'
// rows from a prior watch process get processed without re-enqueuing.
// Phase A's queue handles this; we just call NextPending in a loop.
//
// The producer-side debouncer:
//   - Per-path map[string]*time.Timer holds the latest pending fire.
//   - Each Create / Write event resets the timer (AfterFunc) to 2s.
//   - When the timer fires, it Enqueues the path and removes the entry.
//   - Events for paths still being processed are coalesced into a single
//     enqueue (the queue is the producer's "dedup-via-debounce-window"
//     surface; the queue itself does NOT dedup — see queue.go's comment).
package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/mritunjaysharma394/llmwiki/internal/cliutil"
	"github.com/mritunjaysharma394/llmwiki/internal/queue"
	"github.com/mritunjaysharma394/llmwiki/internal/wiki"
	"github.com/spf13/cobra"
)

// WatchConfig surfaces under [watch] in .llmwiki/config.toml. Empty
// defaults; users opt in by passing <dir> as an argument or by setting
// `dirs` in config (plan §5: "Empty default; user opts in by passing
// <dir> arg or by setting dirs in config").
type WatchConfig struct {
	Dirs            []string `toml:"dirs"`
	DebounceSeconds int      `toml:"debounce_seconds"`
	MaxAttempts     int      `toml:"max_attempts"`
}

// DebounceOrDefault returns the configured debounce window in seconds,
// defaulting to 2 (plan §5) when unset.
func (w WatchConfig) DebounceOrDefault() int {
	if w.DebounceSeconds <= 0 {
		return 2
	}
	return w.DebounceSeconds
}

// MaxAttemptsOrDefault returns the configured retry cap, defaulting to
// queue.MaxAttempts (3) when unset.
func (w WatchConfig) MaxAttemptsOrDefault() int {
	if w.MaxAttempts <= 0 {
		return queue.MaxAttempts
	}
	return w.MaxAttempts
}

var watchCmd = &cobra.Command{
	Use:   "watch [dir]",
	Short: "Watch a directory for new sources and ingest them automatically",
	Long: `Watch a directory tree (recursive once for the top level) for created
or modified files. Each event debounces 2s so partial writes coalesce
into one ingest, then enqueues the path into the SQLite-backed work
queue. A consumer goroutine drains the queue, calling the same ingest
pipeline 'llmwiki ingest <source>' uses.

Persistent across crashes: a watch restart picks up any pending /
retrying queue rows from the prior run without re-enqueuing.

Retry policy: 3 attempts with exponential backoff (5s, 30s, 5min); a
4th failure marks the row terminally 'failed' and the watcher logs
to stderr and moves on. Ctrl-C drains the in-flight ingest, closes
fsnotify, and exits 0.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runWatch,
}

// ingestFn is the package-level seam tests swap to capture watch's
// ingest dispatch without firing the full LLM pipeline. Production uses
// wiki.IngestSource directly. Mirrors the updateExistingFn pattern in
// internal/wiki/ingest_runner.go.
var ingestFn = wiki.IngestSource

func runWatch(cmd *cobra.Command, args []string) error {
	dirs, err := resolveWatchDirs(cmd, args, cfg)
	if err != nil {
		return err
	}

	q, err := queue.New(database.SQL())
	if err != nil {
		return cliutil.Wrap("opening ingest queue", err,
			"the v6 migration may not have applied; try `llmwiki status` to verify the DB is reachable")
	}

	debounce := time.Duration(cfg.Watch.DebounceOrDefault()) * time.Second
	maxAttempts := cfg.Watch.MaxAttemptsOrDefault()

	ctx, cancel := context.WithCancel(cmd.Context())
	defer cancel()

	// Signal handling: SIGINT/SIGTERM close `done`, which both producer
	// and consumer select on. Drain the in-flight ingest, then exit 0.
	done := make(chan struct{})
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		signal.Stop(sigCh)
		fmt.Fprintln(os.Stdout, "\n# stopping watch...")
		close(done)
		cancel()
	}()

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return cliutil.Wrap("creating fsnotify watcher", err,
			"the OS may have hit its inotify limit; try increasing fs.inotify.max_user_watches on Linux or restarting after closing other watch processes on macOS")
	}
	defer watcher.Close()

	for _, d := range dirs {
		if err := watcher.Add(d); err != nil {
			return cliutil.Wrap(fmt.Sprintf("adding %q to watcher", d), err,
				"verify the directory exists and is readable; pass an absolute path if a relative path is ambiguous from your shell")
		}
		fmt.Printf("# watching %s ... (Ctrl-C to stop)\n", d)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		runWatchProducer(watcher, q, debounce, done)
	}()
	go func() {
		defer wg.Done()
		runWatchConsumer(ctx, q, cfg, maxAttempts, done)
	}()
	wg.Wait()
	return nil
}

// resolveWatchDirs picks the dir set per plan §5 precedence: positional
// arg wins (single-shot); else `[watch] dirs` in config; else error.
func resolveWatchDirs(_ *cobra.Command, args []string, c *Config) ([]string, error) {
	if len(args) > 0 {
		dir := args[0]
		info, err := os.Stat(dir)
		if err != nil {
			return nil, cliutil.Wrap(fmt.Sprintf("stat %q", dir), err,
				"pass a path to an existing directory")
		}
		if !info.IsDir() {
			return nil, cliutil.Wrap(fmt.Sprintf("%q is not a directory", dir), nil,
				"watch operates on directories; for a single file pass its parent dir")
		}
		abs, _ := filepath.Abs(dir)
		return []string{abs}, nil
	}
	if c != nil && len(c.Watch.Dirs) > 0 {
		out := make([]string, 0, len(c.Watch.Dirs))
		for _, d := range c.Watch.Dirs {
			abs, _ := filepath.Abs(d)
			out = append(out, abs)
		}
		return out, nil
	}
	return nil, cliutil.Wrap("no directory to watch", nil,
		"pass <dir> as an argument or set `dirs = [\"~/wiki/sources\"]` under [watch] in .llmwiki/config.toml")
}

// runWatchProducer subscribes to Create/Write fsnotify events and
// debounces per-path. When the per-path timer fires, the path is
// Enqueued onto the queue. Exits when the events channel closes or
// `done` fires.
func runWatchProducer(w *fsnotify.Watcher, q *queue.Queue, debounce time.Duration, done <-chan struct{}) {
	timers := map[string]*time.Timer{}
	var mu sync.Mutex

	enqueue := func(path string) {
		if _, err := q.Enqueue(path); err != nil {
			fmt.Fprintf(os.Stderr, "  WARN enqueue %s: %v\n", path, err)
			return
		}
		fmt.Printf("[+] %s → queued\n", filepath.Base(path))
	}

	for {
		select {
		case <-done:
			// Cancel pending timers so we don't enqueue post-shutdown.
			mu.Lock()
			for _, t := range timers {
				t.Stop()
			}
			timers = nil
			mu.Unlock()
			return
		case ev, ok := <-w.Events:
			if !ok {
				return
			}
			// Filter to Create/Write — Rename/Remove/Chmod aren't ingest
			// triggers (the file's gone or changed permissions, neither
			// of which we want to re-ingest). plan §5: "fsnotify on the
			// directory; debounce 2s per file (don't fire on partial writes)".
			if !(ev.Op&fsnotify.Create == fsnotify.Create || ev.Op&fsnotify.Write == fsnotify.Write) {
				continue
			}
			path := ev.Name
			// Skip directories (e.g. when the user mkdir's a sub-folder
			// inside the watched root). Stat may race with the file
			// disappearing again; treat any stat error as "skip silently".
			info, err := os.Stat(path)
			if err != nil || info.IsDir() {
				continue
			}
			mu.Lock()
			if t, exists := timers[path]; exists {
				t.Stop()
			}
			timers[path] = time.AfterFunc(debounce, func() {
				mu.Lock()
				delete(timers, path)
				mu.Unlock()
				enqueue(path)
			})
			mu.Unlock()
		case err, ok := <-w.Errors:
			if !ok {
				return
			}
			fmt.Fprintf(os.Stderr, "  WARN fsnotify: %v\n", err)
		}
	}
}

// runWatchConsumer drains the queue. On NextPending success, dispatches
// to ingestFn; on err, MarkRetrying (with backoff handled by Phase A's
// next_attempt_at column) or MarkFailed when attempts hit the cap.
//
// Empty-queue case: sleep briefly (250ms) then re-check. The producer
// may not have fired in the same scheduler tick, so we don't busy-spin.
func runWatchConsumer(ctx context.Context, q *queue.Queue, c *Config, maxAttempts int, done <-chan struct{}) {
	for {
		select {
		case <-done:
			return
		default:
		}

		it, err := q.NextPending()
		if errors.Is(err, queue.ErrEmpty) {
			// Tight sleep so a SIGINT during idle still surfaces fast.
			select {
			case <-done:
				return
			case <-time.After(250 * time.Millisecond):
				continue
			}
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "  WARN queue NextPending: %v\n", err)
			select {
			case <-done:
				return
			case <-time.After(time.Second):
				continue
			}
		}

		// Dispatch to the ingest pipeline. Logger=nil → io.Discard
		// (the watcher prints its own one-line summary instead of the
		// full ingest progress block). We deliberately bypass
		// buildWikiIngestOptions (which reads cobra flags from a *Command,
		// creating an init-cycle when threaded through watchCmd) and
		// thread the ingest defaults straight from cfg + activeSchema.
		opts := wiki.IngestOptions{
			UpdateExisting:                       c.Ingest.UpdateExistingOrDefault(),
			UpdateExistingMaxCandidatesPerSource: c.Ingest.UpdateExistingMaxCandidatesPerSource,
			UpdateExistingMaxCandidatesTotal:     c.Ingest.UpdateExistingMaxCandidatesTotal,
			UpdateExistingQuoteFloor:             c.Ingest.UpdateExistingQuoteFloor,
			Schema:                               activeSchema,
		}
		res, ierr := ingestFn(ctx, toWikiIngestConfig(c), database, llmClient, it.SourceURI, opts)
		if ierr != nil {
			attempts := it.Attempts + 1
			if attempts >= maxAttempts {
				fmt.Fprintf(os.Stderr, "[!] %s → failed after %d attempts: %v\n",
					filepath.Base(it.SourceURI), attempts, ierr)
				if mErr := q.MarkFailed(it.ID, ierr.Error()); mErr != nil {
					fmt.Fprintf(os.Stderr, "  WARN MarkFailed %d: %v\n", it.ID, mErr)
				}
				continue
			}
			fmt.Fprintf(os.Stderr, "[!] %s → retry %d/%d: %v\n",
				filepath.Base(it.SourceURI), attempts, maxAttempts, ierr)
			if mErr := q.MarkRetrying(it.ID, ierr.Error()); mErr != nil {
				fmt.Fprintf(os.Stderr, "  WARN MarkRetrying %d: %v\n", it.ID, mErr)
			}
			continue
		}

		if mErr := q.MarkSuccess(it.ID); mErr != nil {
			fmt.Fprintf(os.Stderr, "  WARN MarkSuccess %d: %v\n", it.ID, mErr)
		}
		fmt.Printf("[✓] %s → %d pages, %d retro-links, %d contradictions\n",
			filepath.Base(it.SourceURI),
			res.PagesWritten, res.RetroLinkedPages, res.ContradictionsFlagged)
	}
}
