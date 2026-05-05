package wiki

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mritunjaysharma394/llmwiki/internal/db"
	"github.com/mritunjaysharma394/llmwiki/internal/llm"
	"github.com/mritunjaysharma394/llmwiki/internal/schema"
)

// maintainFixture spins up a temp wiki.db, a wiki dir, and an answers
// dir wired so RunMaintenance can compose its three steps end-to-end.
// Tests that exercise the refresh-stale leg seed local-file sources
// (no HTTP) and stub IngestSource via runMaintenanceIngestFn so the
// whole pipeline doesn't fire — we only care RunMaintenance passes
// the right Force=true and source URI through.
type maintainFixture struct {
	Root       string
	WikiDir    string
	AnswersDir string
	DB         *db.DB
}

func setupMaintainFixture(t *testing.T) *maintainFixture {
	t.Helper()
	root := t.TempDir()
	wikiDir := filepath.Join(root, "wiki")
	answersDir := filepath.Join(root, "answers")
	for _, d := range []string{wikiDir, answersDir} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	d, err := db.Open(filepath.Join(root, "wiki.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return &maintainFixture{Root: root, WikiDir: wikiDir, AnswersDir: answersDir, DB: d}
}

// seedLocalSource writes a file at <root>/<name> and inserts a sources
// row keyed by the absolute path with the matching content_hash. The
// returned path is what RunMaintenance's refresh-stale leg will hash
// via wiki.CurrentSourceHash.
func (f *maintainFixture) seedLocalSource(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(f.Root, name)
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatalf("write source %s: %v", name, err)
	}
	hash, err := CurrentSourceHash(path)
	if err != nil {
		t.Fatalf("CurrentSourceHash: %v", err)
	}
	if _, err := f.DB.UpsertSource(path, hash); err != nil {
		t.Fatalf("UpsertSource: %v", err)
	}
	return path
}

// rewriteSource rewrites the file in place so its bytes drift from the
// recorded hash — RunMaintenance's refresh-stale should pick it up.
func (f *maintainFixture) rewriteSource(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatalf("rewrite %s: %v", path, err)
	}
}

// stubLLM is a minimal llm.Client for tests; never asserted-on.
type stubLLM struct{ resp string }

func (s *stubLLM) Complete(ctx context.Context, system, user string) (string, error) {
	if s.resp == "" {
		return "No contradictions found.", nil
	}
	return s.resp, nil
}
func (s *stubLLM) CompleteStructured(ctx context.Context, system, user string, ts llm.ToolSchema) (map[string]any, error) {
	return nil, errors.New("stubLLM: CompleteStructured unused")
}
func (s *stubLLM) CompleteStream(ctx context.Context, system, user string, w io.Writer) (string, error) {
	resp := s.resp
	if resp == "" {
		resp = "No contradictions found."
	}
	if _, err := w.Write([]byte(resp)); err != nil {
		return "", err
	}
	return resp, nil
}

// TestRunMaintenance_AllStepsOff is the no-op baseline: every step
// disabled, no opts; result is zero-valued, no error, no DB writes.
func TestRunMaintenance_AllStepsOff(t *testing.T) {
	f := setupMaintainFixture(t)
	res, err := RunMaintenance(context.Background(), IngestSourceConfig{WikiDir: f.WikiDir}, f.DB, &stubLLM{}, schema.Bundled(), MaintainOpts{})
	if err != nil {
		t.Fatalf("RunMaintenance: %v", err)
	}
	if res.StaleSourcesChecked != 0 || res.StaleRefetched != 0 || res.PromotePendingTotal != 0 {
		t.Errorf("expected fully-zero result, got %+v", res)
	}
}

// TestRunMaintenance_RefreshStaleDetectsDriftAndCallsIngest seeds two
// local sources (one stable, one whose bytes we rewrite to drift),
// stubs runMaintenanceIngestFn, and asserts:
//   - StaleSourcesChecked == 2
//   - StaleRefetched == 1  (only the drifted one)
//   - the stubbed IngestSource saw Force=true and the drifted URI
func TestRunMaintenance_RefreshStaleDetectsDriftAndCallsIngest(t *testing.T) {
	f := setupMaintainFixture(t)
	stableURI := f.seedLocalSource(t, "stable.md", "stable bytes\n")
	driftURI := f.seedLocalSource(t, "drift.md", "old bytes\n")
	f.rewriteSource(t, driftURI, "NEW bytes that produce a different hash\n")

	type call struct {
		uri   string
		force bool
	}
	var calls []call
	prev := runMaintenanceIngestFn
	runMaintenanceIngestFn = func(ctx context.Context, cfg IngestSourceConfig, database *db.DB, client llm.Client, source string, opts IngestOptions) (IngestRunResult, error) {
		calls = append(calls, call{uri: source, force: opts.Force})
		return IngestRunResult{Source: source}, nil
	}
	t.Cleanup(func() { runMaintenanceIngestFn = prev })

	res, err := RunMaintenance(context.Background(), IngestSourceConfig{WikiDir: f.WikiDir}, f.DB, &stubLLM{}, schema.Bundled(), MaintainOpts{
		RefreshStale: true,
	})
	if err != nil {
		t.Fatalf("RunMaintenance: %v", err)
	}
	if res.StaleSourcesChecked != 2 {
		t.Errorf("StaleSourcesChecked = %d, want 2", res.StaleSourcesChecked)
	}
	if res.StaleRefetched != 1 {
		t.Errorf("StaleRefetched = %d, want 1", res.StaleRefetched)
	}
	if len(calls) != 1 {
		t.Fatalf("ingest stub called %d times, want 1", len(calls))
	}
	if calls[0].uri != driftURI {
		t.Errorf("called URI = %q, want %q", calls[0].uri, driftURI)
	}
	if !calls[0].force {
		t.Error("expected Force=true for refresh-stale")
	}
	// The stable source must NOT have been touched.
	for _, c := range calls {
		if c.uri == stableURI {
			t.Errorf("stable source %q was unexpectedly re-ingested", stableURI)
		}
	}
}

// TestRunMaintenance_RefreshStaleDryRunSkipsIngest — the same drift
// setup, but with DryRun=true. StaleRefetched is still 1 (the planned
// number of refreshes), but the stub must not be invoked.
func TestRunMaintenance_RefreshStaleDryRunSkipsIngest(t *testing.T) {
	f := setupMaintainFixture(t)
	driftURI := f.seedLocalSource(t, "drift.md", "old\n")
	f.rewriteSource(t, driftURI, "NEW\n")

	var called bool
	prev := runMaintenanceIngestFn
	runMaintenanceIngestFn = func(ctx context.Context, cfg IngestSourceConfig, database *db.DB, client llm.Client, source string, opts IngestOptions) (IngestRunResult, error) {
		called = true
		return IngestRunResult{}, nil
	}
	t.Cleanup(func() { runMaintenanceIngestFn = prev })

	res, err := RunMaintenance(context.Background(), IngestSourceConfig{WikiDir: f.WikiDir}, f.DB, &stubLLM{}, schema.Bundled(), MaintainOpts{
		RefreshStale: true,
		DryRun:       true,
	})
	if err != nil {
		t.Fatalf("RunMaintenance: %v", err)
	}
	if called {
		t.Error("DryRun=true must not invoke IngestSource")
	}
	if res.StaleRefetched != 1 {
		t.Errorf("DryRun StaleRefetched = %d, want 1", res.StaleRefetched)
	}
}

// TestRunMaintenance_RefreshStaleNetworkErrorIsLoggedNotAborted —
// seeds two sources (one missing on disk so CurrentSourceHash errors,
// one drifted). The missing one bumps RefetchErrors+1 and contributes
// one Errors entry; the drifted one still re-ingests.
func TestRunMaintenance_RefreshStaleNetworkErrorIsLoggedNotAborted(t *testing.T) {
	f := setupMaintainFixture(t)
	driftURI := f.seedLocalSource(t, "drift.md", "old\n")
	f.rewriteSource(t, driftURI, "NEW\n")
	// Insert a row pointing at a non-existent path. CurrentSourceHash
	// will fail with os.ErrNotExist — Errors should grow by one and
	// the rest of the pass should still run.
	missingURI := filepath.Join(f.Root, "ghost.md")
	if _, err := f.DB.UpsertSource(missingURI, "deadbeef"); err != nil {
		t.Fatalf("UpsertSource: %v", err)
	}

	var ingestCalls int
	prev := runMaintenanceIngestFn
	runMaintenanceIngestFn = func(ctx context.Context, cfg IngestSourceConfig, database *db.DB, client llm.Client, source string, opts IngestOptions) (IngestRunResult, error) {
		ingestCalls++
		return IngestRunResult{}, nil
	}
	t.Cleanup(func() { runMaintenanceIngestFn = prev })

	res, err := RunMaintenance(context.Background(), IngestSourceConfig{WikiDir: f.WikiDir}, f.DB, &stubLLM{}, schema.Bundled(), MaintainOpts{
		RefreshStale: true,
	})
	if err != nil {
		t.Fatalf("RunMaintenance: %v", err)
	}
	if res.RefetchErrors != 1 {
		t.Errorf("RefetchErrors = %d, want 1", res.RefetchErrors)
	}
	if len(res.Errors) != 1 {
		t.Errorf("Errors len = %d, want 1: %v", len(res.Errors), res.Errors)
	}
	if ingestCalls != 1 {
		t.Errorf("ingestCalls = %d, want 1 (the drifted source still refreshes)", ingestCalls)
	}
}

// TestRunMaintenance_LintProducesFastLintResult — three pages, one is
// an orphan; opts.Lint=true. Expect FastLint.OrphanCount == 1 and
// SchemaDriftPages bubbles up too.
func TestRunMaintenance_LintProducesFastLintResult(t *testing.T) {
	f := setupMaintainFixture(t)
	sch := schema.Bundled()
	for _, p := range []struct{ title, body string }{
		{"Foo", "Body of Foo. See [[Bar]]."},
		{"Bar", "Body of Bar. See [[Foo]]."},
		{"Lonely", "I have no inbound links."},
	} {
		if err := f.DB.UpsertPage(db.PageRecord{
			Title:       p.title,
			Path:        filepath.Join(f.WikiDir, p.title+".md"),
			Body:        p.body,
			ContentHash: HashContent(p.body),
		}); err != nil {
			t.Fatalf("UpsertPage %s: %v", p.title, err)
		}
		stored, _ := f.DB.GetPage(p.title)
		if stored != nil {
			_ = f.DB.UpdateSchemaHash(stored.ID, sch.Hash())
		}
	}

	res, err := RunMaintenance(context.Background(), IngestSourceConfig{WikiDir: f.WikiDir}, f.DB, &stubLLM{resp: "No contradictions found."}, sch, MaintainOpts{
		Lint: true,
	})
	if err != nil {
		t.Fatalf("RunMaintenance: %v", err)
	}
	if res.FastLint.OrphanCount != 1 {
		t.Errorf("OrphanCount = %d, want 1", res.FastLint.OrphanCount)
	}
}

// TestRunMaintenance_LintCountsContradictions — two pages, stub LLM
// returns a fake "Page A vs Page B: ..." line. Expect
// ContradictionsFound > 0.
func TestRunMaintenance_LintCountsContradictions(t *testing.T) {
	f := setupMaintainFixture(t)
	sch := schema.Bundled()
	for _, p := range []struct{ title, body string }{
		{"PA", "page a body"},
		{"PB", "page b body"},
	} {
		if err := f.DB.UpsertPage(db.PageRecord{
			Title: p.title, Path: filepath.Join(f.WikiDir, p.title+".md"),
			Body: p.body, ContentHash: HashContent(p.body),
		}); err != nil {
			t.Fatalf("UpsertPage %s: %v", p.title, err)
		}
	}
	res, err := RunMaintenance(context.Background(), IngestSourceConfig{WikiDir: f.WikiDir}, f.DB, &stubLLM{resp: "PA vs PB: PA says X, PB says Y."}, sch, MaintainOpts{
		Lint: true,
	})
	if err != nil {
		t.Fatalf("RunMaintenance: %v", err)
	}
	if res.ContradictionsFound < 1 {
		t.Errorf("ContradictionsFound = %d, want >= 1", res.ContradictionsFound)
	}
}

// writeAnswerFile drops a saved-answer file at <answersDir>/<ts>-<slug>.md
// shaped exactly like cmd/ask's saveAnswer does.
func (f *maintainFixture) writeAnswerFile(t *testing.T, slug string, in SavedAnswerInput) string {
	t.Helper()
	body := FormatSavedAnswer(in)
	ts := in.At.UTC().Format("2006-01-02-150405")
	path := filepath.Join(f.AnswersDir, ts+"-"+slug+".md")
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatalf("writeAnswerFile: %v", err)
	}
	return path
}

// promoteFixtureSetup seeds two existing pages so the citation gate can
// pass, and a source file the saved-answer evidence quotes reference.
func (f *maintainFixture) promoteFixtureSetup(t *testing.T) string {
	t.Helper()
	// Seed two existing pages so [Goroutines] / [Channels] count as
	// distinct existing-page citations.
	for _, title := range []string{"Goroutines", "Channels"} {
		if err := f.DB.UpsertPage(db.PageRecord{
			Title: title, Path: filepath.Join(f.WikiDir, title+".md"),
			Body: "body of " + title, ContentHash: HashContent("body of " + title),
		}); err != nil {
			t.Fatalf("seed %s: %v", title, err)
		}
	}
	// Source file backing the answer's evidence quotes.
	srcPath := filepath.Join(f.Root, "src.md")
	srcBody := "Goroutines are lightweight threads.\nThe go keyword starts one.\nThey communicate via channels.\n"
	if err := os.WriteFile(srcPath, []byte(srcBody), 0644); err != nil {
		t.Fatalf("write src: %v", err)
	}
	hash, _ := CurrentSourceHash(srcPath)
	srcID, err := f.DB.UpsertSource(srcPath, hash)
	if err != nil {
		t.Fatalf("UpsertSource: %v", err)
	}
	if _, err := f.DB.UpsertSourceFile(db.SourceFile{
		SourceID: srcID, RelativePath: filepath.Base(srcPath),
		ContentHash: hash, ByteSize: int64(len(srcBody)),
		LineCount: strings.Count(srcBody, "\n"),
	}); err != nil {
		t.Fatalf("UpsertSourceFile: %v", err)
	}
	return srcPath
}

// makePassingAnswer returns a SavedAnswerInput shaped to pass every
// gate signal (≥2 citations, ≥3 evidence quotes, 100–3000 words, no
// hedging). It pads a base body with "lorem" tokens so word count
// clears the floor.
func makePassingAnswer(question, srcRel string) SavedAnswerInput {
	body := "This page covers [Goroutines] and [Channels] in depth. "
	for i := 0; i < 200; i++ {
		body += "lorem "
	}
	return SavedAnswerInput{
		Question: question,
		Answer:   body,
		Model:    "test-model",
		At:       time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC),
		Pages: []Page{{
			Title: "Goroutines",
			Evidence: []Evidence{
				{Quote: "Goroutines are lightweight threads.", LineStart: 1, LineEnd: 1, SourceFilePath: srcRel},
				{Quote: "The go keyword starts one.", LineStart: 2, LineEnd: 2, SourceFilePath: srcRel},
				{Quote: "They communicate via channels.", LineStart: 3, LineEnd: 3, SourceFilePath: srcRel},
			},
		}},
	}
}

// TestRunMaintenance_PromotePendingPasses — write one passing answer
// file, run only PromotePending=true, assert PromotePendingPromoted=1
// and the stubbed PromoteAnswer was called once with Source="auto".
func TestRunMaintenance_PromotePendingPasses(t *testing.T) {
	f := setupMaintainFixture(t)
	srcPath := f.promoteFixtureSetup(t)
	in := makePassingAnswer("what is a goroutine question", filepath.Base(srcPath))
	f.writeAnswerFile(t, "what-is-a-goroutine-question", in)

	type call struct {
		path   string
		source string
	}
	var calls []call
	prev := runMaintenancePromoteFn
	runMaintenancePromoteFn = func(ctx context.Context, cfg IngestSourceConfig, database *db.DB, client llm.Client, answerPath string, opts PromoteOptions) (PromoteResult, error) {
		calls = append(calls, call{path: answerPath, source: opts.Source})
		return PromoteResult{Title: "What Is A Goroutine Question"}, nil
	}
	t.Cleanup(func() { runMaintenancePromoteFn = prev })

	res, err := RunMaintenance(context.Background(), IngestSourceConfig{WikiDir: f.WikiDir}, f.DB, &stubLLM{}, schema.Bundled(), MaintainOpts{
		PromotePending: true,
		AnswersDir:     f.AnswersDir,
		AutoPromoteCfg: AutoPromoteConfig{SkipScore: 1000.0},
	})
	if err != nil {
		t.Fatalf("RunMaintenance: %v", err)
	}
	if res.PromotePendingTotal != 1 {
		t.Errorf("PromotePendingTotal = %d, want 1", res.PromotePendingTotal)
	}
	if res.PromotePendingPromoted != 1 {
		t.Errorf("PromotePendingPromoted = %d, want 1", res.PromotePendingPromoted)
	}
	if len(calls) != 1 {
		t.Fatalf("PromoteAnswer called %d times, want 1", len(calls))
	}
	if calls[0].source != "auto" {
		t.Errorf("PromoteAnswer Source = %q, want %q", calls[0].source, "auto")
	}
}

// TestRunMaintenance_PromotePendingValidatorFailedStaysOnDisk — the
// stubbed PromoteAnswer returns ErrEvidenceInvalid. The result must
// bump PromotePendingValidatorFailed and the answer file must STILL
// exist on disk after the call (existing trust property: never silently
// drop).
func TestRunMaintenance_PromotePendingValidatorFailedStaysOnDisk(t *testing.T) {
	f := setupMaintainFixture(t)
	srcPath := f.promoteFixtureSetup(t)
	in := makePassingAnswer("validator fails this one question", filepath.Base(srcPath))
	answerPath := f.writeAnswerFile(t, "validator-fails", in)

	prev := runMaintenancePromoteFn
	runMaintenancePromoteFn = func(ctx context.Context, cfg IngestSourceConfig, database *db.DB, client llm.Client, answerPath string, opts PromoteOptions) (PromoteResult, error) {
		return PromoteResult{}, ErrEvidenceInvalid
	}
	t.Cleanup(func() { runMaintenancePromoteFn = prev })

	var buf bytes.Buffer
	res, err := RunMaintenance(context.Background(), IngestSourceConfig{WikiDir: f.WikiDir}, f.DB, &stubLLM{}, schema.Bundled(), MaintainOpts{
		PromotePending: true,
		AnswersDir:     f.AnswersDir,
		AutoPromoteCfg: AutoPromoteConfig{SkipScore: 1000.0},
		Logger:         &buf,
	})
	if err != nil {
		t.Fatalf("RunMaintenance: %v", err)
	}
	if res.PromotePendingValidatorFailed != 1 {
		t.Errorf("PromotePendingValidatorFailed = %d, want 1", res.PromotePendingValidatorFailed)
	}
	if _, err := os.Stat(answerPath); err != nil {
		t.Errorf("answer file should remain on disk after validator-fail: %v", err)
	}
	if !strings.Contains(buf.String(), "promote_failed") {
		t.Errorf("expected `promote_failed` line in logger output, got:\n%s", buf.String())
	}
}

// TestRunMaintenance_PromotePendingDryRunSkipsPromote — DryRun=true
// means the stub must not be called even when the gate would pass.
func TestRunMaintenance_PromotePendingDryRunSkipsPromote(t *testing.T) {
	f := setupMaintainFixture(t)
	srcPath := f.promoteFixtureSetup(t)
	in := makePassingAnswer("dry run question", filepath.Base(srcPath))
	f.writeAnswerFile(t, "dry-run", in)

	var called bool
	prev := runMaintenancePromoteFn
	runMaintenancePromoteFn = func(ctx context.Context, cfg IngestSourceConfig, database *db.DB, client llm.Client, answerPath string, opts PromoteOptions) (PromoteResult, error) {
		called = true
		return PromoteResult{}, nil
	}
	t.Cleanup(func() { runMaintenancePromoteFn = prev })

	res, err := RunMaintenance(context.Background(), IngestSourceConfig{WikiDir: f.WikiDir}, f.DB, &stubLLM{}, schema.Bundled(), MaintainOpts{
		PromotePending: true,
		DryRun:         true,
		AnswersDir:     f.AnswersDir,
		AutoPromoteCfg: AutoPromoteConfig{SkipScore: 1000.0},
	})
	if err != nil {
		t.Fatalf("RunMaintenance: %v", err)
	}
	if called {
		t.Error("DryRun=true must not invoke PromoteAnswer")
	}
	if res.PromotePendingPromoted != 1 {
		t.Errorf("PromotePendingPromoted (would-have) = %d, want 1", res.PromotePendingPromoted)
	}
}

// TestRunMaintenance_AnswersDirMissingIsNotAnError — if AnswersDir
// doesn't exist, promote-pending silently no-ops; no Errors entry,
// no panic. cron-friendly.
func TestRunMaintenance_AnswersDirMissingIsNotAnError(t *testing.T) {
	f := setupMaintainFixture(t)
	missing := filepath.Join(f.Root, "no-such-dir")
	res, err := RunMaintenance(context.Background(), IngestSourceConfig{WikiDir: f.WikiDir}, f.DB, &stubLLM{}, schema.Bundled(), MaintainOpts{
		PromotePending: true,
		AnswersDir:     missing,
	})
	if err != nil {
		t.Fatalf("RunMaintenance: %v", err)
	}
	if len(res.Errors) != 0 {
		t.Errorf("missing AnswersDir should not produce Errors, got %v", res.Errors)
	}
}

// TestCountContradictions — pin the counter on the three flavours of
// LLM response shape we expect: explicit "**Contradiction**" markdown,
// "Page A vs Page B" prose, and the no-op clean response.
func TestCountContradictions(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want int
	}{
		{"clean", "No contradictions found.", 0},
		{"empty", "", 0},
		{"vs-prose-one", "PA vs PB: PA says X, PB says Y.", 1},
		{"vs-prose-two", "PA vs PB: x.\nPC vs PD: y.\n", 2},
		{"contradiction-bold", "**Contradiction**: foo\n**Contradiction**: bar\n", 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := countContradictions(tc.in); got != tc.want {
				t.Errorf("countContradictions(%q) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

// TestRunMaintenance_ResolvesAnswersDirFromWikiDir — when AnswersDir
// is empty but WikiDir is set, the function falls back to
// <wiki-parent>/answers/. Verified by writing the answer to that
// derived path and confirming the file is found.
func TestRunMaintenance_ResolvesAnswersDirFromWikiDir(t *testing.T) {
	f := setupMaintainFixture(t)
	srcPath := f.promoteFixtureSetup(t)
	// Move our answer file into the derived-answers location.
	derived := filepath.Join(filepath.Dir(f.WikiDir), "answers")
	if err := os.MkdirAll(derived, 0755); err != nil {
		t.Fatalf("mkdir derived: %v", err)
	}
	in := makePassingAnswer("derived dir question", filepath.Base(srcPath))
	body := FormatSavedAnswer(in)
	ts := in.At.UTC().Format("2006-01-02-150405")
	if err := os.WriteFile(filepath.Join(derived, ts+"-derived.md"), []byte(body), 0644); err != nil {
		t.Fatalf("write derived answer: %v", err)
	}

	prev := runMaintenancePromoteFn
	runMaintenancePromoteFn = func(ctx context.Context, cfg IngestSourceConfig, database *db.DB, client llm.Client, answerPath string, opts PromoteOptions) (PromoteResult, error) {
		return PromoteResult{Title: "Derived"}, nil
	}
	t.Cleanup(func() { runMaintenancePromoteFn = prev })

	res, err := RunMaintenance(context.Background(), IngestSourceConfig{WikiDir: f.WikiDir}, f.DB, &stubLLM{}, schema.Bundled(), MaintainOpts{
		PromotePending: true,
		// AnswersDir intentionally empty — must derive.
		AutoPromoteCfg: AutoPromoteConfig{SkipScore: 1000.0},
	})
	if err != nil {
		t.Fatalf("RunMaintenance: %v", err)
	}
	if res.PromotePendingTotal != 1 {
		t.Errorf("PromotePendingTotal = %d, want 1 (derived AnswersDir didn't resolve)", res.PromotePendingTotal)
	}
}

// _ keeps the fmt + io imports in use even if a future test edit drops them.
var _ = fmt.Sprintf
var _ = io.Discard
