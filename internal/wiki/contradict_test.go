// Package wiki — contradict_test.go
//
// Tests for DetectIngestContradictions, the per-pair contradiction
// detector that runs after each ingest's persist loop. Sibling to
// DetectContradictions (whole-wiki batcher used by `llmwiki lint`); this
// one is targeted at the just-written pages and surfaces structured
// tuples instead of a free-form text blob.
package wiki

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mritunjaysharma394/llmwiki/internal/db"
	"github.com/mritunjaysharma394/llmwiki/internal/llm"
)

// stubContradictClient is a minimal llm.Client whose Complete returns a
// canned body or error. Tests fixture the LLM response so detection logic
// (filtering, dedup) is exercised without real LLM calls.
type stubContradictClient struct {
	completeFn func(ctx context.Context, system, user string) (string, error)
	calls      int
	lastUser   string
	lastSystem string
}

func (s *stubContradictClient) Complete(ctx context.Context, system, user string) (string, error) {
	s.calls++
	s.lastUser = user
	s.lastSystem = system
	if s.completeFn != nil {
		return s.completeFn(ctx, system, user)
	}
	return "[]", nil
}

func (s *stubContradictClient) CompleteStructured(ctx context.Context, system, user string, ts llm.ToolSchema) (map[string]any, error) {
	return nil, errors.New("CompleteStructured not used by DetectIngestContradictions")
}

func (s *stubContradictClient) CompleteStream(ctx context.Context, system, user string, w io.Writer) (string, error) {
	return s.Complete(ctx, system, user)
}

// contradictFixture sets up a fresh wikiDir + db for the per-pair tests.
type contradictFixture struct {
	WikiDir string
	DB      *db.DB
	SrcID   int64
	FileID  int64
}

func setupContradictFixture(t *testing.T) *contradictFixture {
	t.Helper()
	root := t.TempDir()
	wikiDir := filepath.Join(root, "wiki")
	if err := os.MkdirAll(wikiDir, 0755); err != nil {
		t.Fatalf("mkdir wiki: %v", err)
	}
	database, err := db.Open(filepath.Join(root, "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	srcID, err := database.UpsertSource("test://src", "deadbeef")
	if err != nil {
		t.Fatalf("UpsertSource: %v", err)
	}
	fileID, err := database.UpsertSourceFile(db.SourceFile{
		SourceID:     srcID,
		RelativePath: "src.md",
		ContentHash:  "h-src",
		ByteSize:     1,
		LineCount:    1,
	})
	if err != nil {
		t.Fatalf("UpsertSourceFile: %v", err)
	}
	return &contradictFixture{WikiDir: wikiDir, DB: database, SrcID: srcID, FileID: fileID}
}

// seedExistingPage writes a page (disk + DB) with one evidence quote and
// returns its DB row.
func (f *contradictFixture) seedExistingPage(t *testing.T, title, body, quote string) db.PageRecord {
	t.Helper()
	now := time.Now().UTC()
	p := Page{
		Title:       title,
		Body:        body,
		ContentHash: HashContent(body),
		UpdatedAt:   now,
		SourceIDs:   []int64{f.SrcID},
		Evidence: []Evidence{
			{Quote: quote, LineStart: 1, LineEnd: 1, SourceFilePath: "src.md"},
		},
	}
	if err := WritePage(p, f.WikiDir); err != nil {
		t.Fatalf("seed WritePage %s: %v", title, err)
	}
	rec := db.PageRecord{
		Title:       title,
		Path:        PagePath(f.WikiDir, title),
		Body:        body,
		ContentHash: p.ContentHash,
		SourceIDs:   []int64{f.SrcID},
	}
	if err := f.DB.UpsertPage(rec); err != nil {
		t.Fatalf("seed UpsertPage %s: %v", title, err)
	}
	stored, err := f.DB.GetPage(title)
	if err != nil || stored == nil {
		t.Fatalf("GetPage %s: %v", title, err)
	}
	sfid := f.FileID
	if err := f.DB.InsertEvidence(stored.ID, f.SrcID, []db.Evidence{
		{Quote: quote, LineStart: 1, LineEnd: 1, SourceFileID: &sfid},
	}); err != nil {
		t.Fatalf("InsertEvidence %s: %v", title, err)
	}
	return *stored
}

// TestDetectIngestContradictions_CandidateSelection seeds five existing
// pages with distinct titles + evidence; the new page shares keywords
// with two of them. Assert the candidate list (the pages the per-pair
// LLM call would run against) names exactly those two — capped at
// candidateLimit.
func TestDetectIngestContradictions_CandidateSelection(t *testing.T) {
	fx := setupContradictFixture(t)
	fx.seedExistingPage(t, "Mutex Internals", "Mutex coordinates exclusive access.\n", "Mutex coordinates exclusive access.")
	fx.seedExistingPage(t, "Channel Patterns", "Channels carry typed messages between goroutines.\n", "Channels carry typed messages between goroutines.")
	fx.seedExistingPage(t, "Goroutine Lifecycle", "Goroutines are lightweight green threads.\n", "Goroutines are lightweight green threads.")
	fx.seedExistingPage(t, "Garbage Collection", "The runtime collects unreachable allocations.\n", "The runtime collects unreachable allocations.")
	fx.seedExistingPage(t, "Reflection Notes", "Reflection inspects type structure at runtime.\n", "Reflection inspects type structure at runtime.")

	allRecs, err := fx.DB.AllPages()
	if err != nil {
		t.Fatalf("AllPages: %v", err)
	}

	// New page mentions Mutex + Channel — exactly two candidates expected.
	newPage := Page{
		Title: "Concurrency Primer",
		Body:  "Mutex and Channel are the two synchronization primitives.\n",
		Evidence: []Evidence{
			{Quote: "Mutex and Channel are the two synchronization primitives.", LineStart: 1, LineEnd: 1, SourceFilePath: "src.md"},
		},
	}

	got := selectCandidates(newPage, allRecs, 5, fx.DB)
	if len(got) != 2 {
		t.Fatalf("candidates = %d, want 2 (got titles=%v)", len(got), candidateTitles(got))
	}
	titles := candidateTitles(got)
	if !contains(titles, "Mutex Internals") || !contains(titles, "Channel Patterns") {
		t.Errorf("candidates = %v, want Mutex Internals + Channel Patterns", titles)
	}

	// Cap at candidateLimit=1 — only the first match survives.
	got1 := selectCandidates(newPage, allRecs, 1, fx.DB)
	if len(got1) != 1 {
		t.Errorf("candidates with limit=1 = %d, want 1", len(got1))
	}
}

func candidateTitles(recs []db.PageRecord) []string {
	out := make([]string, 0, len(recs))
	for _, r := range recs {
		out = append(out, r.Title)
	}
	return out
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

// TestDetectIngestContradictions_FiltersHallucinatedQuotes fixtures an LLM
// response naming a quote that does NOT appear in either page's evidence.
// The detector must drop the tuple — never trust the LLM's claim about
// what each page said.
func TestDetectIngestContradictions_FiltersHallucinatedQuotes(t *testing.T) {
	fx := setupContradictFixture(t)
	fx.seedExistingPage(t, "Mutex Says A",
		"Mutex behaves one way.\n",
		"Mutex always blocks until acquired.")

	newPage := Page{
		Title: "Mutex Says Not A",
		Body:  "Mutex behaves another way.\n",
		Evidence: []Evidence{
			{Quote: "Mutex never blocks acquisition path.", LineStart: 1, LineEnd: 1, SourceFilePath: "src.md"},
		},
	}

	// LLM hallucinates: claims page A said "completely fabricated quote"
	// and page B said "another lie that wasn't in the inputs".
	client := &stubContradictClient{
		completeFn: func(ctx context.Context, system, user string) (string, error) {
			return `[{"a_quote":"completely fabricated quote","b_quote":"another lie that wasn't in the inputs","description":"made up"}]`, nil
		},
	}

	allRecs, _ := fx.DB.AllPages()
	got, err := DetectIngestContradictions(context.Background(), client, []Page{newPage}, allRecs, 5, fx.DB)
	if err != nil {
		t.Fatalf("DetectIngestContradictions: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0 contradictions (hallucinated quotes filtered out); got %d: %+v", len(got), got)
	}
}

// TestDetectIngestContradictions_HappyPath fixtures an LLM response
// naming two valid contradicting quotes (each appears in its respective
// page's already-validated evidence). One Contradiction is returned with
// both quotes, both source files, both line ranges, and the LLM's
// description verbatim.
func TestDetectIngestContradictions_HappyPath(t *testing.T) {
	fx := setupContradictFixture(t)
	existingQuote := "Mutex always blocks until acquired."
	fx.seedExistingPage(t, "Mutex Behavior",
		"Mutex behaves one way.\nMutex always blocks until acquired.\n",
		existingQuote)

	newQuote := "Mutex never blocks acquisition path."
	newPage := Page{
		Title: "Lock-free Mutex",
		Body:  "Lock-free Mutex never blocks acquisition path.\n",
		Evidence: []Evidence{
			{Quote: newQuote, LineStart: 5, LineEnd: 5, SourceFilePath: "new.md"},
		},
	}

	client := &stubContradictClient{
		completeFn: func(ctx context.Context, system, user string) (string, error) {
			return `[{"a_quote":"` + newQuote + `","b_quote":"` + existingQuote + `","description":"both pages disagree on whether Mutex can block"}]`, nil
		},
	}

	allRecs, _ := fx.DB.AllPages()
	got, err := DetectIngestContradictions(context.Background(), client, []Page{newPage}, allRecs, 5, fx.DB)
	if err != nil {
		t.Fatalf("DetectIngestContradictions: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 contradiction; got %d: %+v", len(got), got)
	}
	c := got[0]
	if c.NewPageTitle != "Lock-free Mutex" {
		t.Errorf("NewPageTitle = %q, want Lock-free Mutex", c.NewPageTitle)
	}
	if c.NewPageQuote != newQuote {
		t.Errorf("NewPageQuote = %q, want %q", c.NewPageQuote, newQuote)
	}
	if c.NewPageSourceFile != "new.md" {
		t.Errorf("NewPageSourceFile = %q, want new.md", c.NewPageSourceFile)
	}
	if c.NewPageLines != [2]int{5, 5} {
		t.Errorf("NewPageLines = %v, want [5,5]", c.NewPageLines)
	}
	if c.ExistingTitle != "Mutex Behavior" {
		t.Errorf("ExistingTitle = %q, want Mutex Behavior", c.ExistingTitle)
	}
	if c.ExistingQuote != existingQuote {
		t.Errorf("ExistingQuote = %q, want %q", c.ExistingQuote, existingQuote)
	}
	if c.ExistingSourceFile != "src.md" {
		t.Errorf("ExistingSourceFile = %q, want src.md", c.ExistingSourceFile)
	}
	if c.ExistingLines != [2]int{1, 1} {
		t.Errorf("ExistingLines = %v, want [1,1]", c.ExistingLines)
	}
	if c.Description != "both pages disagree on whether Mutex can block" {
		t.Errorf("Description = %q", c.Description)
	}
}

// TestDetectIngestContradictions_LLMErrorReturnsEmptyAndLogs verifies that
// a stub returning an error produces (nil, nil) — contradiction detection
// is informational and MUST NEVER block trust-validated ingest.
func TestDetectIngestContradictions_LLMErrorReturnsEmptyAndLogs(t *testing.T) {
	fx := setupContradictFixture(t)
	fx.seedExistingPage(t, "Mutex Behavior",
		"Mutex always blocks until acquired.\n",
		"Mutex always blocks until acquired.")

	newPage := Page{
		Title: "Lock-free Mutex",
		Body:  "Lock-free Mutex never blocks acquisition path.\n",
		Evidence: []Evidence{
			{Quote: "Lock-free Mutex never blocks acquisition path.", LineStart: 1, LineEnd: 1, SourceFilePath: "new.md"},
		},
	}

	client := &stubContradictClient{
		completeFn: func(ctx context.Context, system, user string) (string, error) {
			return "", errors.New("simulated LLM timeout")
		},
	}

	// Capture stderr to assert WARN was logged.
	origStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	allRecs, _ := fx.DB.AllPages()
	got, err := DetectIngestContradictions(context.Background(), client, []Page{newPage}, allRecs, 5, fx.DB)

	w.Close()
	os.Stderr = origStderr
	stderrBuf := make([]byte, 4096)
	n, _ := r.Read(stderrBuf)
	stderrText := string(stderrBuf[:n])

	if err != nil {
		t.Fatalf("expected nil error (informational); got %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0 contradictions on LLM error; got %d", len(got))
	}
	if !strings.Contains(stderrText, "WARN") || !strings.Contains(stderrText, "contradiction") {
		t.Errorf("expected WARN line on stderr; got %q", stderrText)
	}
}

// TestDetectIngestContradictions_DedupsBidirectionalPairs asserts that
// dedup keys are by (newPageTitle, existingTitle) ordered pair: when
// the same new-page title appears twice in newPages (defensively, in
// case the caller doesn't pre-filter), the result holds one
// Contradiction per ordered pair, not one per iteration.
func TestDetectIngestContradictions_DedupsBidirectionalPairs(t *testing.T) {
	fx := setupContradictFixture(t)
	existingQuote := "Mutex always blocks until acquired."
	fx.seedExistingPage(t, "Mutex Behavior",
		"Mutex always blocks until acquired.\n",
		existingQuote)

	newQuote := "Mutex never blocks acquisition path."
	newPage := Page{
		Title: "Lock-free Mutex",
		Body:  "Mutex never blocks acquisition path. The Lock-free Mutex page differs from Mutex Behavior.\n",
		Evidence: []Evidence{
			{Quote: newQuote, LineStart: 1, LineEnd: 1, SourceFilePath: "new.md"},
		},
	}

	client := &stubContradictClient{
		completeFn: func(ctx context.Context, system, user string) (string, error) {
			// Same response every call.
			return `[{"a_quote":"` + newQuote + `","b_quote":"` + existingQuote + `","description":"d"}]`, nil
		},
	}

	allRecs, _ := fx.DB.AllPages()
	// Pass the same page twice in newPages to force dup attempts. The
	// dedup key is (newPageTitle, existingTitle), so identical new
	// entries collapse to one Contradiction.
	got, err := DetectIngestContradictions(context.Background(), client, []Page{newPage, newPage}, allRecs, 5, fx.DB)
	if err != nil {
		t.Fatalf("DetectIngestContradictions: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("expected 1 deduped contradiction; got %d: %+v", len(got), got)
	}
	// LLM should be called only once thanks to dedup short-circuit.
	if client.calls != 1 {
		t.Errorf("expected 1 LLM call (deduped); got %d", client.calls)
	}
}

// TestDetectIngestContradictions_EmptyNewPages: empty input → empty
// result, no LLM calls.
func TestDetectIngestContradictions_EmptyNewPages(t *testing.T) {
	fx := setupContradictFixture(t)
	fx.seedExistingPage(t, "Mutex Behavior",
		"Mutex always blocks until acquired.\n",
		"Mutex always blocks until acquired.")

	client := &stubContradictClient{
		completeFn: func(ctx context.Context, system, user string) (string, error) {
			t.Fatalf("LLM should not be called with empty newPages")
			return "", nil
		},
	}

	allRecs, _ := fx.DB.AllPages()
	got, err := DetectIngestContradictions(context.Background(), client, nil, allRecs, 5, fx.DB)
	if err != nil {
		t.Fatalf("DetectIngestContradictions: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0 contradictions with empty newPages; got %d", len(got))
	}
	if client.calls != 0 {
		t.Errorf("expected 0 LLM calls; got %d", client.calls)
	}
}

// TestFormatContradictionMarkdown asserts the spec'd append-only block
// shape: RFC3339 timestamp + "**ingest** <source>" header + nested
// hyphen-bullet quote pairs annotated with "(<path>:<a>-<b>)".
func TestFormatContradictionMarkdown(t *testing.T) {
	at := time.Date(2026, 5, 4, 14, 30, 12, 0, time.UTC)
	contras := []Contradiction{{
		NewPageTitle:       "Lock-free Mutex",
		NewPageQuote:       "Mutex never blocks.",
		NewPageSourceFile:  "new.md",
		NewPageLines:       [2]int{5, 5},
		ExistingTitle:      "Mutex Behavior",
		ExistingQuote:      "Mutex always blocks.",
		ExistingSourceFile: "src.md",
		ExistingLines:      [2]int{1, 1},
		Description:        "disagreement on blocking",
	}}

	out := FormatContradictionMarkdown(contras, "test://source", at)

	mustContain := []string{
		"2026-05-04T14:30:12Z",
		"**ingest** test://source",
		`new page "Lock-free Mutex"`,
		"[[Mutex Behavior]]",
		`> "Mutex never blocks." (new.md:5-5)`,
		`> "Mutex always blocks." (src.md:1-1)`,
	}
	for _, want := range mustContain {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\nfull output:\n%s", want, out)
		}
	}
}

// TestAppendContradictions writes a block, then a second block, then
// verifies the file is append-only (both blocks present, in order).
func TestAppendContradictions(t *testing.T) {
	dir := t.TempDir()
	t1 := time.Date(2026, 5, 4, 14, 30, 12, 0, time.UTC)
	t2 := time.Date(2026, 5, 4, 15, 0, 0, 0, time.UTC)

	c1 := []Contradiction{{
		NewPageTitle: "A", NewPageQuote: "qA", NewPageSourceFile: "f.md", NewPageLines: [2]int{1, 1},
		ExistingTitle: "B", ExistingQuote: "qB", ExistingSourceFile: "g.md", ExistingLines: [2]int{2, 2},
		Description: "d1",
	}}
	c2 := []Contradiction{{
		NewPageTitle: "C", NewPageQuote: "qC", NewPageSourceFile: "f.md", NewPageLines: [2]int{3, 3},
		ExistingTitle: "D", ExistingQuote: "qD", ExistingSourceFile: "g.md", ExistingLines: [2]int{4, 4},
		Description: "d2",
	}}

	if err := AppendContradictions(dir, c1, "src1", t1); err != nil {
		t.Fatalf("AppendContradictions 1: %v", err)
	}
	if err := AppendContradictions(dir, c2, "src2", t2); err != nil {
		t.Fatalf("AppendContradictions 2: %v", err)
	}

	body, err := os.ReadFile(filepath.Join(dir, "contradictions.md"))
	if err != nil {
		t.Fatalf("read contradictions.md: %v", err)
	}
	out := string(body)
	if !strings.Contains(out, "src1") || !strings.Contains(out, "src2") {
		t.Errorf("missing one or both source markers:\n%s", out)
	}
	if !strings.Contains(out, "[[B]]") || !strings.Contains(out, "[[D]]") {
		t.Errorf("missing existing-title wikilinks:\n%s", out)
	}
	// Order: src1 must come before src2.
	if strings.Index(out, "src1") > strings.Index(out, "src2") {
		t.Errorf("append order violated:\n%s", out)
	}
}
