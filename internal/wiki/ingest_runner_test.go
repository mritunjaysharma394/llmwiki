package wiki

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mritunjaysharma394/llmwiki/internal/db"
	"github.com/mritunjaysharma394/llmwiki/internal/ingest"
	"github.com/mritunjaysharma394/llmwiki/internal/llm"
)

// fakeIngestPagesClient stubs CompleteStructured with a single page whose
// evidence quote substring-matches the source. The page title is taken
// from titleOverride so each test can drive a specific new-this-batch
// title through the pipeline (and then assert retro-linking against it).
//
// completeFn (Phase E) is invoked for the contradiction-detection
// per-pair call. When nil, Complete returns "[]" (no contradictions).
type fakeIngestPagesClient struct {
	titleOverride string
	body          string
	quote         string
	completeFn    func(ctx context.Context, system, user string) (string, error)
}

func (f *fakeIngestPagesClient) Complete(ctx context.Context, system, user string) (string, error) {
	if f.completeFn != nil {
		return f.completeFn(ctx, system, user)
	}
	return "[]", nil
}

func (f *fakeIngestPagesClient) CompleteStream(ctx context.Context, system, user string, w io.Writer) (string, error) {
	out, err := f.Complete(ctx, system, user)
	if err != nil {
		return "", err
	}
	_, _ = w.Write([]byte(out))
	return out, nil
}

func (f *fakeIngestPagesClient) CompleteStructured(ctx context.Context, system, user string, ts llm.ToolSchema) (map[string]any, error) {
	// The validator attributes evidence by the `=== <path> ===` header in
	// the user prompt, so pull the path out and quote it back. Mirrors the
	// fakeIngestClient in internal/mcp/server_test.go.
	path := "source"
	for _, line := range strings.Split(user, "\n") {
		if strings.HasPrefix(line, "=== ") && strings.HasSuffix(line, " ===") {
			path = strings.TrimSuffix(strings.TrimPrefix(line, "=== "), " ===")
			break
		}
	}
	return map[string]any{
		"pages": []any{
			map[string]any{
				"title": f.titleOverride,
				"body":  f.body,
				"evidence": []any{
					map[string]any{"quote": f.quote, "source_file": path},
				},
			},
		},
	}, nil
}

// TestIngestSource_RetroLinksExistingPages pre-seeds two pages whose
// bodies mention the title "Mutex" in bare prose, runs IngestSource
// against a synthetic source whose LLM-stubbed page is titled "Mutex",
// and asserts:
//   - both pre-existing pages now contain `[[Mutex]]` on disk
//   - IngestRunResult.RetroLinkedPages == 2
//   - index.md is regenerated AFTER the retro-link step (so it reflects
//     the rewritten existing pages' updated_at)
func TestIngestSource_RetroLinksExistingPages(t *testing.T) {
	root := t.TempDir()
	wikiDir := filepath.Join(root, "wiki")
	rawDir := filepath.Join(root, "raw")
	for _, d := range []string{wikiDir, rawDir} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	database, err := db.Open(filepath.Join(root, "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	// Seed: a placeholder source so the seeded pages have a SourceID FK.
	srcID, err := database.UpsertSource("test://seed", "h-seed")
	if err != nil {
		t.Fatalf("UpsertSource seed: %v", err)
	}

	// Pre-seed two existing pages mentioning "Mutex" in bare prose.
	for _, seed := range []struct{ title, body string }{
		{"Goroutine Scheduling", "Goroutine Scheduling sometimes interacts with Mutex during contention.\n"},
		{"Channel Internals", "Channel Internals never blocks on Mutex in the fast path.\n"},
	} {
		path := filepath.Join(wikiDir, seed.title+".md")
		page := Page{
			Title:       seed.title,
			Body:        seed.body,
			ContentHash: HashContent(seed.body),
			SourceIDs:   []int64{srcID},
		}
		if err := WritePage(page, wikiDir); err != nil {
			t.Fatalf("seed WritePage %s: %v", seed.title, err)
		}
		if err := database.UpsertPage(db.PageRecord{
			Title:       seed.title,
			Path:        path,
			Body:        seed.body,
			ContentHash: page.ContentHash,
			SourceIDs:   []int64{srcID},
		}); err != nil {
			t.Fatalf("seed UpsertPage %s: %v", seed.title, err)
		}
	}

	// Source the new page is ingested from. The stub returns a Mutex page
	// whose evidence quote substring-matches this content.
	srcPath := filepath.Join(root, "mutex.md")
	srcBody := "Mutex coordinates access to shared state.\n"
	if err := os.WriteFile(srcPath, []byte(srcBody), 0644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	cfg := IngestSourceConfig{
		WikiDir:          wikiDir,
		RawDir:           rawDir,
		RespectGitignore: true,
	}
	client := &fakeIngestPagesClient{
		titleOverride: "Mutex",
		body:          "Body about Mutex.",
		quote:         "Mutex coordinates access to shared state.",
	}
	res, err := IngestSource(context.Background(), cfg, database, client, srcPath, IngestOptions{})
	if err != nil {
		t.Fatalf("IngestSource: %v", err)
	}
	if res.PagesWritten != 1 {
		t.Fatalf("PagesWritten = %d, want 1", res.PagesWritten)
	}
	if res.RetroLinkedPages != 2 {
		t.Errorf("RetroLinkedPages = %d, want 2", res.RetroLinkedPages)
	}

	// Both seeded pages on disk now contain [[Mutex]].
	for _, want := range []string{"Goroutine Scheduling", "Channel Internals"} {
		body, err := os.ReadFile(filepath.Join(wikiDir, want+".md"))
		if err != nil {
			t.Fatalf("read %s: %v", want, err)
		}
		if !strings.Contains(string(body), "[[Mutex]]") {
			t.Errorf("page %s missing [[Mutex]] after retro-link:\n%s", want, body)
		}
	}

	// index.md was regenerated AFTER retro-linking — its body must already
	// contain [[Mutex]] (the new page) and the wikilink-form titles.
	indexBytes, err := os.ReadFile(filepath.Join(wikiDir, "index.md"))
	if err != nil {
		t.Fatalf("read index.md: %v", err)
	}
	if !strings.Contains(string(indexBytes), "[[Mutex]]") {
		t.Errorf("index.md missing [[Mutex]] entry:\n%s", indexBytes)
	}
}

// TestIngestSource_ContradictionPassAppendsToContradictionsMD seeds an
// existing page that claims X (with valid evidence), then ingests a
// synthetic source whose generated page claims ~X. The fake client's
// Complete returns a hand-crafted contradiction tuple. Asserts:
//   - the new page lands (validator gate already passed)
//   - <wikiDir>/contradictions.md is created with an RFC3339-stamped block
//   - IngestRunResult.ContradictionsFlagged == 1
//   - the inline log output contains "!! 1 contradiction(s) flagged"
func TestIngestSource_ContradictionPassAppendsToContradictionsMD(t *testing.T) {
	root := t.TempDir()
	wikiDir := filepath.Join(root, "wiki")
	rawDir := filepath.Join(root, "raw")
	for _, d := range []string{wikiDir, rawDir} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	database, err := db.Open(filepath.Join(root, "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	srcID, err := database.UpsertSource("test://seed", "h-seed")
	if err != nil {
		t.Fatalf("UpsertSource seed: %v", err)
	}
	// Seed a source_file row so existing-page evidence resolves.
	sfID, err := database.UpsertSourceFile(db.SourceFile{
		SourceID: srcID, RelativePath: "seed.md", ContentHash: "h", ByteSize: 1, LineCount: 1,
	})
	if err != nil {
		t.Fatalf("UpsertSourceFile: %v", err)
	}

	// Pre-seed an existing page claiming Mutex always blocks.
	existingTitle := "Mutex Behavior"
	existingQuote := "Mutex always blocks until acquired."
	existingBody := "Mutex always blocks until acquired.\n"
	existingPath := filepath.Join(wikiDir, existingTitle+".md")
	existingPage := Page{
		Title:       existingTitle,
		Body:        existingBody,
		ContentHash: HashContent(existingBody),
		SourceIDs:   []int64{srcID},
		Evidence: []Evidence{
			{Quote: existingQuote, LineStart: 1, LineEnd: 1, SourceFilePath: "seed.md"},
		},
	}
	if err := WritePage(existingPage, wikiDir); err != nil {
		t.Fatalf("seed WritePage: %v", err)
	}
	if err := database.UpsertPage(db.PageRecord{
		Title: existingTitle, Path: existingPath, Body: existingBody,
		ContentHash: existingPage.ContentHash, SourceIDs: []int64{srcID},
	}); err != nil {
		t.Fatalf("seed UpsertPage: %v", err)
	}
	stored, _ := database.GetPage(existingTitle)
	if err := database.InsertEvidence(stored.ID, srcID, []db.Evidence{
		{Quote: existingQuote, LineStart: 1, LineEnd: 1, SourceFileID: &sfID},
	}); err != nil {
		t.Fatalf("seed InsertEvidence: %v", err)
	}

	// Source the new page is ingested from. The ingest stub's evidence
	// quote substring-matches this content. The body mentions
	// "Mutex Behavior" as a token so candidate selection picks the
	// existing page.
	srcPath := filepath.Join(root, "lockfree.md")
	newQuote := "Mutex never blocks acquisition path."
	srcBody := newQuote + "\nThis Lock-free Mutex contradicts the Mutex Behavior page.\n"
	if err := os.WriteFile(srcPath, []byte(srcBody), 0644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	cfg := IngestSourceConfig{WikiDir: wikiDir, RawDir: rawDir, RespectGitignore: true}
	client := &fakeIngestPagesClient{
		titleOverride: "Lock-free Mutex",
		body:          "Lock-free Mutex never blocks acquisition path. Mutex Behavior is wrong.",
		quote:         newQuote,
		completeFn: func(ctx context.Context, system, user string) (string, error) {
			// Hand-craft the contradiction tuple naming both validated
			// quotes verbatim.
			return `[{"a_quote":"` + newQuote + `","b_quote":"` + existingQuote + `","description":"Mutex behavior disagreement"}]`, nil
		},
	}
	var logBuf bytes.Buffer
	res, err := IngestSource(context.Background(), cfg, database, client, srcPath, IngestOptions{Logger: &logBuf})
	if err != nil {
		t.Fatalf("IngestSource: %v", err)
	}

	if res.PagesWritten != 1 {
		t.Errorf("PagesWritten = %d, want 1", res.PagesWritten)
	}
	if res.ContradictionsFlagged != 1 {
		t.Errorf("ContradictionsFlagged = %d, want 1", res.ContradictionsFlagged)
	}

	// Page on disk.
	if _, err := os.Stat(filepath.Join(wikiDir, "Lock-free Mutex.md")); err != nil {
		t.Errorf("new page not written: %v", err)
	}

	// contradictions.md exists with RFC3339 timestamp + both quote sides.
	contraBytes, err := os.ReadFile(filepath.Join(wikiDir, "contradictions.md"))
	if err != nil {
		t.Fatalf("read contradictions.md: %v", err)
	}
	contra := string(contraBytes)
	for _, want := range []string{
		"**ingest** " + srcPath,
		`new page "Lock-free Mutex"`,
		"[[Mutex Behavior]]",
		newQuote,
		existingQuote,
	} {
		if !strings.Contains(contra, want) {
			t.Errorf("contradictions.md missing %q\nfull:\n%s", want, contra)
		}
	}

	// Inline summary mentions the count.
	if !strings.Contains(logBuf.String(), "1 contradiction(s) flagged") {
		t.Errorf("inline log missing flagged-count line:\n%s", logBuf.String())
	}
}

// TestIngestSource_ContradictionLLMFailureDoesNotBlockIngest verifies
// the trust property: an LLM error in the contradiction pass logs WARN
// and returns no contradictions, but the page still lands.
func TestIngestSource_ContradictionLLMFailureDoesNotBlockIngest(t *testing.T) {
	root := t.TempDir()
	wikiDir := filepath.Join(root, "wiki")
	rawDir := filepath.Join(root, "raw")
	for _, d := range []string{wikiDir, rawDir} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	database, err := db.Open(filepath.Join(root, "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	srcID, err := database.UpsertSource("test://seed", "h-seed")
	if err != nil {
		t.Fatalf("UpsertSource seed: %v", err)
	}
	sfID, err := database.UpsertSourceFile(db.SourceFile{
		SourceID: srcID, RelativePath: "seed.md", ContentHash: "h", ByteSize: 1, LineCount: 1,
	})
	if err != nil {
		t.Fatalf("UpsertSourceFile: %v", err)
	}

	// Seed an existing page so the per-pair call has a candidate.
	existingTitle := "Mutex Behavior"
	existingQuote := "Mutex always blocks until acquired."
	existingBody := "Mutex always blocks until acquired.\n"
	existingPath := filepath.Join(wikiDir, existingTitle+".md")
	if err := WritePage(Page{
		Title: existingTitle, Body: existingBody,
		ContentHash: HashContent(existingBody), SourceIDs: []int64{srcID},
		Evidence: []Evidence{{Quote: existingQuote, LineStart: 1, LineEnd: 1, SourceFilePath: "seed.md"}},
	}, wikiDir); err != nil {
		t.Fatalf("seed WritePage: %v", err)
	}
	if err := database.UpsertPage(db.PageRecord{
		Title: existingTitle, Path: existingPath, Body: existingBody,
		ContentHash: HashContent(existingBody), SourceIDs: []int64{srcID},
	}); err != nil {
		t.Fatalf("seed UpsertPage: %v", err)
	}
	stored, _ := database.GetPage(existingTitle)
	_ = database.InsertEvidence(stored.ID, srcID, []db.Evidence{
		{Quote: existingQuote, LineStart: 1, LineEnd: 1, SourceFileID: &sfID},
	})

	srcPath := filepath.Join(root, "lockfree.md")
	srcBody := "Mutex never blocks acquisition path. Mutex Behavior page differs.\n"
	if err := os.WriteFile(srcPath, []byte(srcBody), 0644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	// Capture stderr for WARN assertion.
	origStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	cfg := IngestSourceConfig{WikiDir: wikiDir, RawDir: rawDir, RespectGitignore: true}
	client := &fakeIngestPagesClient{
		titleOverride: "Lock-free Mutex",
		body:          "Lock-free Mutex differs from Mutex Behavior.",
		quote:         "Mutex never blocks acquisition path.",
		completeFn: func(ctx context.Context, system, user string) (string, error) {
			return "", errors.New("simulated LLM 500")
		},
	}
	res, err := IngestSource(context.Background(), cfg, database, client, srcPath, IngestOptions{Logger: io.Discard})

	w.Close()
	os.Stderr = origStderr
	stderrBuf := make([]byte, 4096)
	n, _ := r.Read(stderrBuf)
	stderrText := string(stderrBuf[:n])

	if err != nil {
		t.Fatalf("IngestSource should not fail on contradiction LLM error: %v", err)
	}
	if res.PagesWritten != 1 {
		t.Errorf("PagesWritten = %d, want 1 (page must land despite contradiction failure)", res.PagesWritten)
	}
	if res.ContradictionsFlagged != 0 {
		t.Errorf("ContradictionsFlagged = %d, want 0 on LLM error", res.ContradictionsFlagged)
	}
	// New page on disk.
	if _, err := os.Stat(filepath.Join(wikiDir, "Lock-free Mutex.md")); err != nil {
		t.Errorf("new page missing despite contradiction failure: %v", err)
	}
	// contradictions.md NOT created.
	if _, err := os.Stat(filepath.Join(wikiDir, "contradictions.md")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("contradictions.md should not exist after LLM error; stat err = %v", err)
	}
	// WARN on stderr.
	if !strings.Contains(stderrText, "WARN") {
		t.Errorf("expected WARN on stderr; got %q", stderrText)
	}
}

// updateSeamCall captures one invocation of the package-level
// updateExistingFn seam: the order index (0,1,2... by call), the
// sourceID, the new titles, the source-file paths, and the options
// the runner constructed.
type updateSeamCall struct {
	order      int
	sourceID   int64
	newTitles  []string
	srcPaths   []string
	opts       UpdateExistingOptions
	resultRet  UpdateResult
	errRet     error
}

// installUpdateSeamRecorder swaps updateExistingFn for a recording stub
// returning the supplied result/err. Restores the original on cleanup.
// The recorder also records call ordering relative to a counter that
// can be incremented by other seams (e.g. RegenerateIndex) so tests
// can assert ordering across phases.
type updateSeamRecorder struct {
	mu         sync.Mutex
	calls      []*updateSeamCall
	counter    *int
	resultRet  UpdateResult
	errRet     error
}

func (r *updateSeamRecorder) record(
	ctx context.Context,
	cfg IngestSourceConfig,
	database *db.DB,
	client llm.Client,
	sourceID int64,
	newSourceFiles []ingest.SourceFile,
	newPageTitles []string,
	opts UpdateExistingOptions,
) (UpdateResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	srcPaths := make([]string, len(newSourceFiles))
	for i, f := range newSourceFiles {
		srcPaths[i] = f.RelativePath
	}
	titlesCopy := append([]string{}, newPageTitles...)
	*r.counter++
	c := &updateSeamCall{
		order:     *r.counter,
		sourceID:  sourceID,
		newTitles: titlesCopy,
		srcPaths:  srcPaths,
		opts:      opts,
		resultRet: r.resultRet,
		errRet:    r.errRet,
	}
	r.calls = append(r.calls, c)
	return r.resultRet, r.errRet
}

// installSeams swaps updateExistingFn (and wraps detectIngestContradictionsFn
// + regenerateIndexFn) so tests can assert call ordering. Returns a
// shared counter pointer the caller can read after IngestSource returns
// and a teardown function to restore originals.
type ingestSeamHarness struct {
	updateRec     *updateSeamRecorder
	contraOrder   int
	indexOrder    int
	counter       int
}

func installIngestSeams(t *testing.T, upRes UpdateResult, upErr error) *ingestSeamHarness {
	t.Helper()
	h := &ingestSeamHarness{}
	h.updateRec = &updateSeamRecorder{
		counter:   &h.counter,
		resultRet: upRes,
		errRet:    upErr,
	}
	origUpdate := updateExistingFn
	origContra := detectIngestContradictionsFn
	origIndex := regenerateIndexFn
	updateExistingFn = h.updateRec.record
	detectIngestContradictionsFn = func(
		ctx context.Context,
		client llm.Client,
		newPages []Page,
		existingPages []db.PageRecord,
		candidateLimit int,
		database *db.DB,
	) ([]Contradiction, error) {
		h.counter++
		h.contraOrder = h.counter
		return origContra(ctx, client, newPages, existingPages, candidateLimit, database)
	}
	regenerateIndexFn = func(wikiDir string, recs []db.PageRecord, srcs []db.Source, ts time.Time) error {
		h.counter++
		h.indexOrder = h.counter
		return origIndex(wikiDir, recs, srcs, ts)
	}
	t.Cleanup(func() {
		updateExistingFn = origUpdate
		detectIngestContradictionsFn = origContra
		regenerateIndexFn = origIndex
	})
	return h
}

// setupBasicIngestEnv pre-seeds an empty wiki + DB and a small source
// file that the fakeIngestPagesClient stub can ingest into a single
// page titled "Mutex".
func setupBasicIngestEnv(t *testing.T) (cfg IngestSourceConfig, database *db.DB, srcPath string) {
	t.Helper()
	root := t.TempDir()
	wikiDir := filepath.Join(root, "wiki")
	rawDir := filepath.Join(root, "raw")
	for _, d := range []string{wikiDir, rawDir} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	database, err := db.Open(filepath.Join(root, "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	srcPath = filepath.Join(root, "mutex.md")
	srcBody := "Mutex coordinates access to shared state.\n"
	if err := os.WriteFile(srcPath, []byte(srcBody), 0644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	cfg = IngestSourceConfig{WikiDir: wikiDir, RawDir: rawDir, RespectGitignore: true}
	return cfg, database, srcPath
}

// TestIngestSource_UpdateExistingFlagOff_DoesNotCallUpdate — the seam
// must never fire when opts.UpdateExisting is false (the default).
func TestIngestSource_UpdateExistingFlagOff_DoesNotCallUpdate(t *testing.T) {
	cfg, database, srcPath := setupBasicIngestEnv(t)
	h := installIngestSeams(t, UpdateResult{}, nil)
	client := &fakeIngestPagesClient{
		titleOverride: "Mutex",
		body:          "Body about Mutex.",
		quote:         "Mutex coordinates access to shared state.",
	}
	_, err := IngestSource(context.Background(), cfg, database, client, srcPath, IngestOptions{})
	if err != nil {
		t.Fatalf("IngestSource: %v", err)
	}
	if len(h.updateRec.calls) != 0 {
		t.Errorf("update seam fired %d time(s); want 0", len(h.updateRec.calls))
	}
}

// TestIngestSource_UpdateExistingFlagOn_CallsUpdateBetweenContradictionsAndIndex
// asserts the call fires AFTER the contradiction pass and BEFORE the
// index regeneration.
func TestIngestSource_UpdateExistingFlagOn_CallsUpdateBetweenContradictionsAndIndex(t *testing.T) {
	cfg, database, srcPath := setupBasicIngestEnv(t)
	h := installIngestSeams(t, UpdateResult{}, nil)
	client := &fakeIngestPagesClient{
		titleOverride: "Mutex",
		body:          "Body about Mutex.",
		quote:         "Mutex coordinates access to shared state.",
	}
	_, err := IngestSource(context.Background(), cfg, database, client, srcPath, IngestOptions{
		UpdateExisting: true,
	})
	if err != nil {
		t.Fatalf("IngestSource: %v", err)
	}
	if len(h.updateRec.calls) != 1 {
		t.Fatalf("update seam fired %d time(s); want 1", len(h.updateRec.calls))
	}
	updateOrder := h.updateRec.calls[0].order
	if !(h.contraOrder > 0 && updateOrder > h.contraOrder) {
		t.Errorf("expected update (%d) AFTER contradictions (%d)", updateOrder, h.contraOrder)
	}
	if !(h.indexOrder > 0 && updateOrder < h.indexOrder) {
		t.Errorf("expected update (%d) BEFORE regenerate-index (%d)", updateOrder, h.indexOrder)
	}
}

// TestIngestSource_UpdateExistingPropagatesIntoIngestRunResult — synthetic
// 1-updated/1-failed batch surfaces in IngestRunResult.
func TestIngestSource_UpdateExistingPropagatesIntoIngestRunResult(t *testing.T) {
	cfg, database, srcPath := setupBasicIngestEnv(t)
	upRes := UpdateResult{
		Updated:           []string{"Stale Page"},
		Failed:            []UpdateFailure{{Title: "Other Page", Reason: "zero-quotes-matched"}},
		PagesUpdated:      1,
		PagesUpdateFailed: 1,
	}
	installIngestSeams(t, upRes, nil)
	client := &fakeIngestPagesClient{
		titleOverride: "Mutex",
		body:          "Body about Mutex.",
		quote:         "Mutex coordinates access to shared state.",
	}
	res, err := IngestSource(context.Background(), cfg, database, client, srcPath, IngestOptions{
		UpdateExisting: true,
	})
	if err != nil {
		t.Fatalf("IngestSource: %v", err)
	}
	if res.PagesUpdated != 1 {
		t.Errorf("PagesUpdated = %d, want 1", res.PagesUpdated)
	}
	if res.PagesUpdateFailed != 1 {
		t.Errorf("PagesUpdateFailed = %d, want 1", res.PagesUpdateFailed)
	}
	if !equalStrSlices(res.UpdatedTitles, []string{"Stale Page"}) {
		t.Errorf("UpdatedTitles = %v, want [Stale Page]", res.UpdatedTitles)
	}
	if len(res.UpdateFailures) != 1 || res.UpdateFailures[0].Title != "Other Page" {
		t.Errorf("UpdateFailures = %+v, want one entry with Title=Other Page", res.UpdateFailures)
	}
}

// TestIngestSource_UpdateExistingTunablesPropagated — explicit
// IngestOptions tunables must reach the seam intact.
func TestIngestSource_UpdateExistingTunablesPropagated(t *testing.T) {
	cfg, database, srcPath := setupBasicIngestEnv(t)
	h := installIngestSeams(t, UpdateResult{}, nil)
	client := &fakeIngestPagesClient{
		titleOverride: "Mutex",
		body:          "Body about Mutex.",
		quote:         "Mutex coordinates access to shared state.",
	}
	_, err := IngestSource(context.Background(), cfg, database, client, srcPath, IngestOptions{
		UpdateExisting:                       true,
		DebugUpdates:                         true,
		UpdateExistingMaxCandidatesPerSource: 3,
		UpdateExistingMaxCandidatesTotal:     7,
		UpdateExistingQuoteFloor:             4,
	})
	if err != nil {
		t.Fatalf("IngestSource: %v", err)
	}
	if len(h.updateRec.calls) != 1 {
		t.Fatalf("update seam fired %d time(s); want 1", len(h.updateRec.calls))
	}
	got := h.updateRec.calls[0].opts
	if got.MaxCandidatesPerSource != 3 {
		t.Errorf("MaxCandidatesPerSource = %d, want 3", got.MaxCandidatesPerSource)
	}
	if got.MaxCandidatesTotal != 7 {
		t.Errorf("MaxCandidatesTotal = %d, want 7", got.MaxCandidatesTotal)
	}
	if got.QuoteFloor != 4 {
		t.Errorf("QuoteFloor = %d, want 4", got.QuoteFloor)
	}
	if !got.DebugUpdates {
		t.Errorf("DebugUpdates = false, want true")
	}
}

// TestIngestSource_UpdateExistingPassesSourceID — the sourceID handed to
// the seam must match the one the runner just upserted (so
// page_update_log.source_id is populated correctly).
func TestIngestSource_UpdateExistingPassesSourceID(t *testing.T) {
	cfg, database, srcPath := setupBasicIngestEnv(t)
	h := installIngestSeams(t, UpdateResult{}, nil)
	client := &fakeIngestPagesClient{
		titleOverride: "Mutex",
		body:          "Body about Mutex.",
		quote:         "Mutex coordinates access to shared state.",
	}
	_, err := IngestSource(context.Background(), cfg, database, client, srcPath, IngestOptions{
		UpdateExisting: true,
	})
	if err != nil {
		t.Fatalf("IngestSource: %v", err)
	}
	if len(h.updateRec.calls) != 1 {
		t.Fatalf("update seam fired %d time(s); want 1", len(h.updateRec.calls))
	}
	gotSourceID := h.updateRec.calls[0].sourceID
	storedSrc, err := database.GetSource(srcPath)
	if err != nil || storedSrc == nil {
		t.Fatalf("GetSource(%q): err=%v rec=%v", srcPath, err, storedSrc)
	}
	if gotSourceID != storedSrc.ID {
		t.Errorf("sourceID passed = %d, want %d (the source row created by IngestSource)",
			gotSourceID, storedSrc.ID)
	}
	// Also: the new titles handed to the seam should be the just-written
	// page titles.
	if !equalStrSlices(h.updateRec.calls[0].newTitles, []string{"Mutex"}) {
		t.Errorf("newTitles = %v, want [Mutex]", h.updateRec.calls[0].newTitles)
	}
}

// TestIngestSource_UpdateExistingLogsSummaryLines — capture Logger output
// and assert it contains the spec's Flow 4 summary lines for both
// the updated and failed cases.
func TestIngestSource_UpdateExistingLogsSummaryLines(t *testing.T) {
	cfg, database, srcPath := setupBasicIngestEnv(t)
	upRes := UpdateResult{
		Updated:           []string{"Stale Page A", "Stale Page B"},
		Failed:            []UpdateFailure{{Title: "Doomed Page", Reason: "zero-quotes-matched"}},
		PagesUpdated:      2,
		PagesUpdateFailed: 1,
	}
	installIngestSeams(t, upRes, nil)
	client := &fakeIngestPagesClient{
		titleOverride: "Mutex",
		body:          "Body about Mutex.",
		quote:         "Mutex coordinates access to shared state.",
	}
	var logBuf bytes.Buffer
	_, err := IngestSource(context.Background(), cfg, database, client, srcPath, IngestOptions{
		UpdateExisting: true,
		Logger:         &logBuf,
	})
	if err != nil {
		t.Fatalf("IngestSource: %v", err)
	}
	got := logBuf.String()
	for _, want := range []string{
		"2 page(s) updated:",
		"~ Stale Page A",
		"~ Stale Page B",
		"1 page(s) update FAILED",
		"kept at previous version",
		"✗ Doomed Page",
		"zero-quotes-matched",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("log output missing %q\nfull log:\n%s", want, got)
		}
	}
}

// TestIngestSource_UpdateExistingSecondRetroLinkPass_SeesUpdatedBodies —
// pre-seed an existing page whose body mentions a "Stale Page" title.
// The update seam stub returns Updated=["Stale Page"]; on disk we
// rewrite "Stale Page"'s body to include the new "Mutex" title in
// bare prose. After IngestSource returns, the second retro-link pass
// (over newTitles + Updated = [Mutex, Stale Page]) must rewrite Stale
// Page's body to wrap "Mutex" in [[wikilinks]].
func TestIngestSource_UpdateExistingSecondRetroLinkPass_SeesUpdatedBodies(t *testing.T) {
	cfg, database, srcPath := setupBasicIngestEnv(t)
	// Seed Stale Page's body containing the bare-prose "Mutex" reference
	// that the second retro-link pass should wrap.
	srcID, err := database.UpsertSource("test://seed-pre", "h-pre")
	if err != nil {
		t.Fatalf("UpsertSource seed: %v", err)
	}
	staleBody := "Stale Page references Mutex behavior in the runtime.\n"
	stalePath := filepath.Join(cfg.WikiDir, "Stale Page.md")
	stalePage := Page{
		Title:       "Stale Page",
		Body:        staleBody,
		ContentHash: HashContent(staleBody),
		SourceIDs:   []int64{srcID},
	}
	if err := WritePage(stalePage, cfg.WikiDir); err != nil {
		t.Fatalf("seed WritePage: %v", err)
	}
	if err := database.UpsertPage(db.PageRecord{
		Title: "Stale Page", Path: stalePath, Body: staleBody,
		ContentHash: stalePage.ContentHash, SourceIDs: []int64{srcID},
	}); err != nil {
		t.Fatalf("seed UpsertPage: %v", err)
	}

	// Stub the update seam to return the page as "Updated" without
	// actually modifying the on-disk body — what we want to verify
	// is that the second retro-link pass runs, sees Stale Page (still
	// containing bare "Mutex"), and wraps the reference.
	installIngestSeams(t, UpdateResult{
		Updated:      []string{"Stale Page"},
		PagesUpdated: 1,
	}, nil)

	client := &fakeIngestPagesClient{
		titleOverride: "Mutex",
		body:          "Body about Mutex.",
		quote:         "Mutex coordinates access to shared state.",
	}
	res, err := IngestSource(context.Background(), cfg, database, client, srcPath, IngestOptions{
		UpdateExisting: true,
	})
	if err != nil {
		t.Fatalf("IngestSource: %v", err)
	}
	if res.PagesUpdated != 1 {
		t.Errorf("PagesUpdated = %d, want 1", res.PagesUpdated)
	}
	staleBytes, err := os.ReadFile(stalePath)
	if err != nil {
		t.Fatalf("read Stale Page: %v", err)
	}
	if !strings.Contains(string(staleBytes), "[[Mutex]]") {
		t.Errorf("Stale Page body missing [[Mutex]] after second retro-link pass:\n%s", staleBytes)
	}
}
