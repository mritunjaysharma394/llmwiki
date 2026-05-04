package wiki

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mritunjaysharma394/llmwiki/internal/db"
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
