package wiki

import (
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
type fakeIngestPagesClient struct {
	titleOverride string
	body          string
	quote         string
}

func (f *fakeIngestPagesClient) Complete(ctx context.Context, system, user string) (string, error) {
	return "", errors.New("Complete not used in ingest path")
}

func (f *fakeIngestPagesClient) CompleteStream(ctx context.Context, system, user string, w io.Writer) (string, error) {
	return f.Complete(ctx, system, user)
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
