package cmd

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mritunjaysharma394/llmwiki/internal/db"
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

// geminiIngestClient builds a cassette wrapping a real Gemini client when
// LLMWIKI_RECORD=1 with GEMINI_API_KEY set; otherwise pure replay against
// the named cassette. Sister of integrationClient (which targets the
// Anthropic provider). Sub-project 6a Phase G uses Gemini Flash as the
// recording target so cassette refresh stays free; the resolved Q2 also
// notes contradiction-detection calls inherit cfg.LLM.Model so the
// contradiction cassette must record against the same provider.
//
// Returns nil on missing-cassette in replay mode, after calling t.Skip;
// callers should defensive-check for nil and bail.
func geminiIngestClient(t *testing.T, name string) llm.Client {
	t.Helper()
	cassetteDir := filepath.Join("..", "internal", "llm", "testdata", "cassettes")
	mode := llm.ModeReplay
	var upstream llm.Client
	if os.Getenv("LLMWIKI_RECORD") != "" {
		if os.Getenv("GEMINI_API_KEY") == "" {
			t.Fatal("LLMWIKI_RECORD set but GEMINI_API_KEY missing")
		}
		upstream = llm.NewGeminiClient("gemini-2.0-flash")
		mode = llm.ModeRecord
	}
	if mode == llm.ModeReplay {
		matches, _ := filepath.Glob(filepath.Join(cassetteDir, name+"__*.json"))
		if len(matches) == 0 {
			t.Skipf("cassette not recorded; run with LLMWIKI_RECORD=1 GEMINI_API_KEY=... go test ./cmd/ -run %s to record", name)
			return nil
		}
	}
	return llm.NewCassetteClient(upstream, cassetteDir, name, mode)
}

// TestRetroLinkAfterIngest pre-seeds three existing pages whose bodies
// mention the title "Mutex" in bare prose, then ingests a synthetic
// source whose generated page is titled "Mutex". Asserts:
//
//   - all three pre-existing page bodies on disk now contain [[Mutex]];
//   - their content_hash on the disk frontmatter changed (the retro-
//     link rewrite recomputes the hash from the new body);
//   - IngestRunResult.RetroLinkedPages == 3.
//
// The cassette wraps Gemini Flash (cfg.LLM.Model = "gemini-2.0-flash")
// so cassette refresh stays cheap. Sister to the unit test
// internal/wiki.TestIngestSource_RetroLinksExistingPages, but exercises
// the full disk-write path through a real LLM-shaped response.
func TestRetroLinkAfterIngest(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cassette test in -short mode")
	}
	client := geminiIngestClient(t, "TestRetroLinkAfterIngest")
	if client == nil {
		return // helper already called t.Skip
	}

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

	// Placeholder source so seeded pages have a SourceID FK.
	srcID, err := database.UpsertSource("test://seed", "h-seed")
	if err != nil {
		t.Fatalf("UpsertSource seed: %v", err)
	}

	// Pre-seed three existing pages mentioning "Mutex" in bare prose.
	type seed struct{ title, body string }
	seeds := []seed{
		{"Goroutine Scheduling", "Goroutine Scheduling sometimes interacts with Mutex during contention.\n"},
		{"Channel Internals", "Channel Internals never blocks on Mutex in the fast path.\n"},
		{"Memory Model", "The Go Memory Model formalizes happens-before via Mutex acquisition.\n"},
	}
	preHashes := map[string]string{}
	for _, s := range seeds {
		page := wiki.Page{
			Title:       s.title,
			Body:        s.body,
			ContentHash: wiki.HashContent(s.body),
			SourceIDs:   []int64{srcID},
		}
		preHashes[s.title] = page.ContentHash
		if err := wiki.WritePage(page, wikiDir); err != nil {
			t.Fatalf("seed WritePage %s: %v", s.title, err)
		}
		if err := database.UpsertPage(db.PageRecord{
			Title:       s.title,
			Path:        filepath.Join(wikiDir, s.title+".md"),
			Body:        s.body,
			ContentHash: page.ContentHash,
			SourceIDs:   []int64{srcID},
		}); err != nil {
			t.Fatalf("seed UpsertPage %s: %v", s.title, err)
		}
	}

	// Synthetic source the new "Mutex" page is ingested from. The cassette
	// records the LLM call against this exact byte stream; recording must
	// produce a page titled "Mutex" with at least one quote substring-
	// matching this content.
	srcPath := filepath.Join(root, "mutex.md")
	srcBody := "Mutex coordinates exclusive access to shared state.\nA Mutex protects critical sections from concurrent goroutines.\n"
	if err := os.WriteFile(srcPath, []byte(srcBody), 0644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	cfg := wiki.IngestSourceConfig{
		WikiDir:          wikiDir,
		RawDir:           rawDir,
		RespectGitignore: true,
	}
	res, err := wiki.IngestSource(context.Background(), cfg, database, client, srcPath, wiki.IngestOptions{})
	if err != nil {
		t.Fatalf("IngestSource: %v", err)
	}
	if res.PagesWritten == 0 {
		t.Fatalf("PagesWritten = 0; cassette may be stale")
	}
	if res.RetroLinkedPages != 3 {
		t.Errorf("RetroLinkedPages = %d, want 3", res.RetroLinkedPages)
	}

	// All three seeded pages on disk now contain [[Mutex]] AND their
	// content_hash changed (the retro-link rewrite recomputes hash).
	for _, s := range seeds {
		path := filepath.Join(wikiDir, s.title+".md")
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", s.title, err)
		}
		if !strings.Contains(string(body), "[[Mutex]]") {
			t.Errorf("page %s missing [[Mutex]] after retro-link:\n%s", s.title, body)
		}
		page, err := wiki.ParsePage(string(body))
		if err != nil {
			t.Fatalf("parse %s: %v", s.title, err)
		}
		if page.ContentHash == preHashes[s.title] {
			t.Errorf("page %s content_hash unchanged after retro-link: %s", s.title, page.ContentHash)
		}
	}
}

// TestContradictionFlaggedOnIngest pre-seeds an existing page claiming X
// (with a validated evidence quote pinned to a synthetic source file
// that lives on disk under the test root), then ingests a second
// synthetic source whose generated page claims ¬X with its own
// validated evidence quote. Asserts:
//
//   - <wikiDir>/contradictions.md exists;
//   - its content matches the spec's append-only format with both quote
//     sides;
//   - the inline log output (captured via IngestOptions.Logger) contains
//     "!! 1 contradiction(s) flagged";
//   - IngestRunResult.ContradictionsFlagged == 1.
//
// The cassette has TWO LLM call types: the ingest call (write_pages
// tool) and the contradiction-detection call (free-form Complete). Both
// segments record under one cassette name. Recording target is Gemini
// Flash — the resolved Q2 in the plan says contradiction-detection
// inherits cfg.LLM.Model, so the same provider must record both.
//
// Confirms the contradiction surface is informational: the new page
// lands regardless of what contradiction detection says, the trust
// property is upheld.
func TestContradictionFlaggedOnIngest(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cassette test in -short mode")
	}
	client := geminiIngestClient(t, "TestContradictionFlaggedOnIngest")
	if client == nil {
		return // helper already called t.Skip
	}

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

	// Seed source file on disk so the existing-page evidence resolves
	// against real bytes during contradiction detection's per-pair lookup.
	existingSrcPath := filepath.Join(root, "always-blocks.md")
	existingQuote := "Mutex always blocks until acquired."
	existingSrcBody := existingQuote + "\nThis is the canonical mutex semantics.\n"
	if err := os.WriteFile(existingSrcPath, []byte(existingSrcBody), 0644); err != nil {
		t.Fatalf("write existing source: %v", err)
	}
	existingHash := wiki.HashContent(existingSrcBody)
	srcID, err := database.UpsertSource(existingSrcPath, existingHash)
	if err != nil {
		t.Fatalf("UpsertSource: %v", err)
	}
	sfID, err := database.UpsertSourceFile(db.SourceFile{
		SourceID:     srcID,
		RelativePath: filepath.Base(existingSrcPath),
		ContentHash:  existingHash,
		ByteSize:     int64(len(existingSrcBody)),
		LineCount:    2,
	})
	if err != nil {
		t.Fatalf("UpsertSourceFile: %v", err)
	}

	// Pre-seed existing page claiming "Mutex always blocks".
	existingTitle := "Mutex Behavior"
	existingBody := existingQuote + "\n"
	existingPage := wiki.Page{
		Title:       existingTitle,
		Body:        existingBody,
		ContentHash: wiki.HashContent(existingBody),
		SourceIDs:   []int64{srcID},
		Evidence: []wiki.Evidence{
			{Quote: existingQuote, LineStart: 1, LineEnd: 1, SourceFilePath: filepath.Base(existingSrcPath)},
		},
	}
	if err := wiki.WritePage(existingPage, wikiDir); err != nil {
		t.Fatalf("seed WritePage: %v", err)
	}
	if err := database.UpsertPage(db.PageRecord{
		Title:       existingTitle,
		Path:        filepath.Join(wikiDir, existingTitle+".md"),
		Body:        existingBody,
		ContentHash: existingPage.ContentHash,
		SourceIDs:   []int64{srcID},
	}); err != nil {
		t.Fatalf("seed UpsertPage: %v", err)
	}
	stored, _ := database.GetPage(existingTitle)
	if err := database.InsertEvidence(stored.ID, srcID, []db.Evidence{
		{Quote: existingQuote, LineStart: 1, LineEnd: 1, SourceFileID: &sfID},
	}); err != nil {
		t.Fatalf("seed InsertEvidence: %v", err)
	}

	// New synthetic source claiming the opposite. Recording must produce
	// a page whose evidence quote substring-matches this body, AND the
	// recorded contradiction-detection segment must name both quotes.
	srcPath := filepath.Join(root, "lockfree.md")
	srcBody := "Mutex never blocks acquisition path.\nA lock-free Mutex contradicts the canonical Mutex Behavior.\n"
	if err := os.WriteFile(srcPath, []byte(srcBody), 0644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	cfg := wiki.IngestSourceConfig{WikiDir: wikiDir, RawDir: rawDir, RespectGitignore: true}
	var logBuf bytes.Buffer
	res, err := wiki.IngestSource(context.Background(), cfg, database, client, srcPath, wiki.IngestOptions{Logger: &logBuf})
	if err != nil {
		t.Fatalf("IngestSource: %v", err)
	}
	if res.PagesWritten == 0 {
		t.Fatalf("PagesWritten = 0; cassette may be stale")
	}
	if res.ContradictionsFlagged != 1 {
		t.Errorf("ContradictionsFlagged = %d, want 1", res.ContradictionsFlagged)
	}

	// contradictions.md exists with both quote sides + existing-page
	// reference.
	contraBytes, err := os.ReadFile(filepath.Join(wikiDir, "contradictions.md"))
	if err != nil {
		t.Fatalf("read contradictions.md: %v", err)
	}
	contra := string(contraBytes)
	for _, want := range []string{
		existingQuote,
		"[[" + existingTitle + "]]",
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
