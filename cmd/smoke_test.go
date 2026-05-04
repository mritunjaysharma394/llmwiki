package cmd

import (
	"context"
	"strings"
	"testing"

	"github.com/mritunjaysharma394/llmwiki/internal/ingest"
	"github.com/mritunjaysharma394/llmwiki/internal/schema"
	"github.com/mritunjaysharma394/llmwiki/internal/wiki"
)

// TestSmokeIngestThenAsk exercises the launch-surface smoke path at the Go
// level: read the smoke fixture, chunk, ingest -> pages, ask a question.
//
// In replay mode (default), the test skips when no cassette has been recorded —
// see integrationClient in ingest_integration_test.go. Recording is opt-in:
// run `LLMWIKI_RECORD=1 go test ./cmd/ -run TestSmokeIngestThenAsk -v` with
// ANTHROPIC_API_KEY set, then commit the resulting smoke__*.json files.
func TestSmokeIngestThenAsk(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	files, err := ingest.ReadLocalFiles("../internal/ingest/testdata/smoke-source.md", ingest.DefaultWalkOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("got %d files, want 1", len(files))
	}
	chunks := ingest.ChunkSourceFiles(files, 16*1024)
	if len(chunks) == 0 {
		t.Fatal("ChunkSourceFiles returned no chunks")
	}
	client := integrationClient(t, "smoke")
	pages, err := wiki.IngestSourceFilesToPages(context.Background(), client, chunks[0].Files, nil, schema.Bundled())
	if err != nil {
		t.Fatalf("IngestSourceFilesToPages: %v", err)
	}
	if len(pages) == 0 {
		t.Fatal("smoke ingest produced no pages")
	}
	answer, err := wiki.AnswerQuestion(context.Background(), client, "what is the smoke source about?", pages, schema.Bundled())
	if err != nil {
		t.Fatalf("AnswerQuestion: %v", err)
	}
	if !strings.Contains(strings.ToLower(answer), "smoke") {
		t.Errorf("answer doesn't mention 'smoke': %q", answer)
	}
}
