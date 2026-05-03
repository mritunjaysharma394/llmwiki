package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mritunjaysharma394/llmwiki/internal/ingest"
	"github.com/mritunjaysharma394/llmwiki/internal/llm"
	"github.com/mritunjaysharma394/llmwiki/internal/wiki"
)

// integrationClient builds a cassette wrapping a real Anthropic client when
// LLMWIKI_RECORD=1 with ANTHROPIC_API_KEY set; otherwise pure replay.
// In replay mode, missing cassettes cause the test to skip rather than fail —
// recordings are an opt-in dev aid, not a CI requirement.
func integrationClient(t *testing.T, name string) llm.Client {
	t.Helper()
	cassetteDir := filepath.Join("..", "internal", "llm", "testdata", "cassettes")
	mode := llm.ModeReplay
	var upstream llm.Client
	if os.Getenv("LLMWIKI_RECORD") != "" {
		if os.Getenv("ANTHROPIC_API_KEY") == "" {
			t.Fatal("LLMWIKI_RECORD set but ANTHROPIC_API_KEY missing")
		}
		upstream = llm.NewAnthropicClient("claude-haiku-4-5")
		mode = llm.ModeRecord
	}
	if mode == llm.ModeReplay {
		matches, _ := filepath.Glob(filepath.Join(cassetteDir, name+"__*.json"))
		if len(matches) == 0 {
			t.Skipf("no cassette for %q (run with LLMWIKI_RECORD=1 ANTHROPIC_API_KEY=... to record)", name)
		}
	}
	return llm.NewCassetteClient(upstream, cassetteDir, name, mode)
}

func TestIngestSmall(t *testing.T) {
	source := "Goroutines are lightweight threads of execution managed by the Go runtime.\nThe `go` keyword starts a goroutine.\nGoroutines communicate via channels.\n"
	client := integrationClient(t, "ingest_small")

	pages, err := wiki.IngestToPages(context.Background(), client, source, nil)
	if err != nil {
		t.Fatalf("IngestToPages: %v", err)
	}
	if len(pages) == 0 {
		t.Fatal("got 0 pages")
	}
	for _, p := range pages {
		if len(p.Evidence) == 0 {
			t.Errorf("page %q has no evidence", p.Title)
		}
		for _, e := range p.Evidence {
			if !strings.Contains(source, e.Quote) {
				t.Errorf("page %q evidence quote not in source: %q", p.Title, e.Quote)
			}
			if e.LineStart < 1 || e.LineEnd < e.LineStart {
				t.Errorf("page %q bad line range %d-%d", p.Title, e.LineStart, e.LineEnd)
			}
		}
	}
}

func TestIngestMultiChunk(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping multi-chunk integration test in -short mode")
	}
	var sb strings.Builder
	sb.WriteString("# Section A: HTTP servers in Go\n\n")
	for i := 0; i < 600; i++ {
		sb.WriteString("HTTP servers in Go are built around the net/http package.\n")
	}
	sb.WriteString("\n# Section B: SQL drivers\n\n")
	for i := 0; i < 600; i++ {
		sb.WriteString("Database/sql provides a thin abstraction over driver implementations.\n")
	}
	sb.WriteString("\n# Section C: Context propagation\n\n")
	for i := 0; i < 600; i++ {
		sb.WriteString("context.Context propagates deadlines and cancellation signals.\n")
	}
	source := sb.String()

	client := integrationClient(t, "ingest_multichunk")
	files := []ingest.SourceFile{ingest.NewSourceFile("doc", []byte(source))}
	chunks := ingest.ChunkSourceFiles(files, ingestChunkSize)
	if len(chunks) < 3 {
		t.Fatalf("expected ≥3 chunks, got %d", len(chunks))
	}
	var allPages []wiki.Page
	for i, c := range chunks {
		// Use the legacy string-based wrapper so this test continues to replay
		// against sub-project 1 cassettes recorded before the multi-file rewrite.
		pages, err := wiki.IngestToPages(context.Background(), client, c.Text, nil)
		if err != nil {
			t.Fatalf("chunk %d: %v", i, err)
		}
		allPages = append(allPages, pages...)
	}
	if len(allPages) == 0 {
		t.Fatal("no pages")
	}
	for _, p := range allPages {
		if len(p.Evidence) == 0 {
			t.Errorf("page %q has no evidence", p.Title)
		}
	}
}

func TestAskWithHits(t *testing.T) {
	pages := []wiki.Page{{
		Title: "Goroutines",
		Body:  "Goroutines are lightweight threads of execution managed by the Go runtime.",
		Evidence: []wiki.Evidence{
			{Quote: "lightweight threads of execution", LineStart: 1, LineEnd: 1},
		},
	}}
	client := integrationClient(t, "ask_with_hits")
	answer, err := wiki.AnswerQuestion(context.Background(), client, "what are goroutines?", pages)
	if err != nil {
		t.Fatalf("AnswerQuestion: %v", err)
	}
	if !strings.Contains(strings.ToLower(answer), "goroutine") {
		t.Errorf("answer doesn't mention goroutines: %s", answer)
	}
}

func TestAskNoHits(t *testing.T) {
	client := integrationClient(t, "ask_no_hits")
	answer, err := wiki.AnswerQuestion(context.Background(), client, "what is etcd?", nil)
	if err != nil {
		t.Fatalf("AnswerQuestion: %v", err)
	}
	if answer == "" {
		t.Error("empty answer")
	}
}
