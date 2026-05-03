package cmd

import (
	"bytes"
	"context"
	"fmt"
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
	chunks := ingest.ChunkSourceFiles(files, 16*1024)
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

// buildSimplePDF constructs a minimal valid 2-page text PDF byte stream. It
// mirrors the helper in internal/ingest/pdf_test.go (which is unexported) so
// this test can build a fixture in t.TempDir() without depending on a checked
// in binary or a recording step.
func buildSimplePDF(page1Text, page2Text string) []byte {
	mkContent := func(text string) string {
		var sb strings.Builder
		sb.WriteString("BT\n/F1 18 Tf\n72 720 Td\n")
		lines := strings.Split(text, "\n")
		for i, ln := range lines {
			if i > 0 {
				sb.WriteString("0 -22 Td\n")
			}
			esc := strings.NewReplacer(`\`, `\\`, `(`, `\(`, `)`, `\)`).Replace(ln)
			fmt.Fprintf(&sb, "(%s) Tj\n", esc)
		}
		sb.WriteString("ET\n")
		return sb.String()
	}
	c1 := mkContent(page1Text)
	c2 := mkContent(page2Text)
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R 5 0 R] /Count 2 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 7 0 R >> >> >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(c1), c1),
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 6 0 R /Resources << /Font << /F1 7 0 R >> >> >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(c2), c2),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n")
	buf.WriteString("%\xe2\xe3\xcf\xd3\n")
	offsets := make([]int, len(objs)+1)
	for i, body := range objs {
		offsets[i+1] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", i+1, body)
	}
	xrefStart := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n", len(objs)+1)
	buf.WriteString("0000000000 65535 f \n")
	for i := 1; i <= len(objs); i++ {
		fmt.Fprintf(&buf, "%010d 00000 n \n", offsets[i])
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xrefStart)
	return buf.Bytes()
}

func TestIngestPDF(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cassette PDF test in -short mode")
	}
	page1 := "alpha beta gamma delta epsilon zeta eta theta iota kappa"
	page2 := "lambda mu nu xi omicron pi rho sigma tau upsilon phi chi"
	pdfPath := filepath.Join(t.TempDir(), "simple.pdf")
	if err := os.WriteFile(pdfPath, buildSimplePDF(page1, page2), 0o644); err != nil {
		t.Fatalf("write simple pdf: %v", err)
	}

	files, err := ingest.ReadPDF(pdfPath)
	if err != nil {
		t.Fatalf("ReadPDF: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("ReadPDF returned no pages")
	}
	for _, f := range files {
		if !strings.HasPrefix(f.RelativePath, "page-") {
			t.Errorf("SourceFile RelativePath %q does not look like page-N", f.RelativePath)
		}
	}

	// Cassette gate — skips cleanly when no fixture is present.
	client := integrationClient(t, "ingest_pdf")

	pages, err := wiki.IngestSourceFilesToPages(context.Background(), client, files, nil)
	if err != nil {
		t.Fatalf("IngestSourceFilesToPages: %v", err)
	}
	if len(pages) == 0 {
		t.Fatal("got 0 pages")
	}
	byPath := map[string]ingest.SourceFile{}
	for _, f := range files {
		byPath[f.RelativePath] = f
	}
	for _, p := range pages {
		if len(p.Evidence) == 0 {
			t.Errorf("page %q has no evidence", p.Title)
		}
		for _, e := range p.Evidence {
			if !strings.HasPrefix(e.SourceFilePath, "page-") {
				t.Errorf("page %q evidence source_file %q does not look like page-N", p.Title, e.SourceFilePath)
			}
			f, ok := byPath[e.SourceFilePath]
			if !ok {
				t.Errorf("page %q evidence source_file %q not in returned files", p.Title, e.SourceFilePath)
				continue
			}
			if !strings.Contains(f.Content, e.Quote) {
				t.Errorf("page %q evidence quote not in %s: %q", p.Title, e.SourceFilePath, e.Quote)
			}
		}
	}
}

func TestIngestRepo(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cassette repo test in -short mode")
	}
	fixture := filepath.Join("..", "internal", "ingest", "testdata", "dirs", "minirepo")

	files, err := ingest.ReadLocalFiles(fixture, ingest.DefaultWalkOptions())
	if err != nil {
		t.Fatalf("ReadLocalFiles: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("ReadLocalFiles returned no files")
	}
	// Walker must drop node_modules.
	for _, f := range files {
		if strings.Contains(f.RelativePath, "node_modules") {
			t.Errorf("node_modules leaked into walker output: %s", f.RelativePath)
		}
	}

	// Cassette gate — skips cleanly when no fixture is present.
	client := integrationClient(t, "ingest_repo")

	pages, err := wiki.IngestSourceFilesToPages(context.Background(), client, files, nil)
	if err != nil {
		t.Fatalf("IngestSourceFilesToPages: %v", err)
	}
	if len(pages) == 0 {
		t.Fatal("got 0 pages")
	}
	knownPaths := map[string]bool{}
	for _, f := range files {
		knownPaths[f.RelativePath] = true
	}
	for _, p := range pages {
		if len(p.Evidence) == 0 {
			t.Errorf("page %q has no evidence", p.Title)
		}
		for _, e := range p.Evidence {
			if e.SourceFilePath == "" {
				t.Errorf("page %q has evidence without SourceFilePath", p.Title)
				continue
			}
			if !knownPaths[e.SourceFilePath] {
				t.Errorf("page %q evidence source_file %q not among walked files", p.Title, e.SourceFilePath)
			}
		}
	}
}
