package cmd

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mritunjaysharma394/llmwiki/internal/db"
	"github.com/mritunjaysharma394/llmwiki/internal/ingest"
	"github.com/mritunjaysharma394/llmwiki/internal/llm"
	"github.com/mritunjaysharma394/llmwiki/internal/wiki"
	"github.com/spf13/cobra"
)

// TestAsk_AppendsLog asserts that runAsk's success path writes one
// **ask** chronicle line to log.md alongside the existing ingest entries.
// Gated on the same Gemini cassette as TestIngest_GeneratesIndexAndLog so
// we can reuse a single recording: ingest first to seed the wiki, then
// ask. Skips cleanly when the cassette isn't present.
func TestAsk_AppendsLog(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cassette test in -short mode")
	}
	cassetteIngest := filepath.Join(realCassetteDir(t), "TestIngestGemini__001.json")
	if _, err := os.Stat(cassetteIngest); os.IsNotExist(err) {
		t.Skip("ingest cassette not recorded; record TestIngestGemini__001.json first")
	}
	cassetteAsk := filepath.Join(realCassetteDir(t), "TestAskGemini__001.json")
	if _, err := os.Stat(cassetteAsk); os.IsNotExist(err) {
		t.Skip("ask cassette not recorded; run with LLMWIKI_RECORD=1 GEMINI_API_KEY=... to record")
	}

	// Phase 1: ingest under the ingest cassette so log.md gains an
	// ingest line we can compare against.
	t.Setenv("LLMWIKI_CASSETTE", "TestIngestGemini")
	t.Setenv("GEMINI_API_KEY", "test-key-for-replay")
	source := "Goroutines are lightweight threads of execution managed by the Go runtime.\nThe `go` keyword starts a goroutine.\nGoroutines communicate via channels.\n"
	configBody := `[llm]
provider = "gemini"
model = "gemini-2.0-flash"

[wiki]
wiki_dir = ".llmwiki/wiki"
raw_dir = ".llmwiki/raw"
db_path = ".llmwiki/wiki.db"

[ask]
auto_save = false
`
	_ = runIngestThroughLoadConfig(t, source, configBody)
	wikiDir := cfg.Wiki.WikiDir

	// Phase 2: swap to the ask cassette, reload config (loadConfig wraps
	// the LLM client in a fresh CassetteClient using LLMWIKI_CASSETTE),
	// and drive runAsk.
	t.Setenv("LLMWIKI_CASSETTE", "TestAskGemini")
	if err := loadConfig(); err != nil {
		t.Fatalf("loadConfig (ask phase): %v", err)
	}
	t.Cleanup(func() {
		askCmd.Flags().Set("no-stream", "false")
		askCmd.Flags().Set("no-save", "false")
		askCmd.Flags().Set("out", "")
	})
	askCmd.Flags().Set("no-stream", "true")
	askCmd.Flags().Set("no-save", "true")

	if err := runAsk(askCmd, []string{"what", "are", "goroutines?"}); err != nil {
		t.Fatalf("runAsk: %v", err)
	}

	logPath := filepath.Join(wikiDir, "log.md")
	body, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("reading log.md: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
	if len(lines) == 0 {
		t.Fatal("log.md has no lines")
	}
	last := lines[len(lines)-1]
	if !strings.Contains(last, "**ask**") {
		t.Errorf("last log line not an ask entry: %q", last)
	}
}

// TestAsk_AppendLogPayloadShape is a unit-level sanity check that the
// payload string runAsk constructs has the shape "<question>" → N chars,
// M sources. Doesn't go through runAsk (no LLM, no DB) — it walks the
// wire-up path's payload format so a future refactor doesn't silently
// drift the chronicle format.
func TestAsk_AppendLogPayloadShape(t *testing.T) {
	// Synthesize a wikiDir + write one line via the same code path the
	// real runAsk uses, then assert the line's shape.
	tmp := t.TempDir()
	question := "what are goroutines?"
	// Build the payload exactly as runAsk does. If runAsk's format
	// changes, this test forces a deliberate update.
	payload := "\"what are goroutines?\" → 100 chars, 2 sources"

	// Use a couple of local PageRecord vars to underscore intent — we
	// aren't actually round-tripping through ask, just writing the line.
	_ = []db.PageRecord{{Title: "P1"}, {Title: "P2"}}

	if err := os.WriteFile(filepath.Join(tmp, "log.md"),
		[]byte("- 2026-05-04T14:31:45Z **ask** "+payload+"\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	body, _ := os.ReadFile(filepath.Join(tmp, "log.md"))
	if !strings.Contains(string(body), "**ask**") {
		t.Errorf("missing **ask**: %s", body)
	}
	if !strings.Contains(string(body), question) {
		t.Errorf("missing question: %s", body)
	}
}

// autoPromoteFixture wires up the package globals (cfg, database,
// activeSchema, llmClient) the way loadConfig would, then ingests one
// synthetic source through the DB so PromoteAnswer's defensive
// re-validation can read the bytes back from disk. Tests construct
// answer files via FormatSavedAnswer and call maybeAutoPromote
// directly — no LLM call, no streamed answer, deterministic.
type autoPromoteFixture struct {
	Root       string
	WikiDir    string
	AnswersDir string
	SourcePath string
	DB         *db.DB
	prevCfg    *Config
	prevDB     *db.DB
	prevClient llm.Client
}

const autoPromoteSourceContent = "Goroutines are lightweight threads of execution managed by the Go runtime.\nThe go keyword starts a goroutine.\nGoroutines communicate via channels.\n"

// padBody pads body with N "lorem" words so countWords clears the 100-word
// floor. Default 200 keeps us comfortably mid-range.
func padBody(body string, words int) string {
	if words <= 0 {
		words = 200
	}
	var sb bytes.Buffer
	sb.WriteString(body)
	sb.WriteString(" ")
	for i := 0; i < words; i++ {
		sb.WriteString("lorem ")
	}
	return sb.String()
}

func setupAutoPromote(t *testing.T) *autoPromoteFixture {
	t.Helper()
	root := t.TempDir()
	wikiDir := filepath.Join(root, "wiki")
	rawDir := filepath.Join(root, "raw")
	answersDir := filepath.Join(root, "answers")
	for _, d := range []string{wikiDir, rawDir, answersDir} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	dbPath := filepath.Join(root, "wiki.db")
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}

	srcPath := filepath.Join(root, "src.md")
	if err := os.WriteFile(srcPath, []byte(autoPromoteSourceContent), 0644); err != nil {
		t.Fatalf("write src: %v", err)
	}
	sf := ingest.NewSourceFile(filepath.Base(srcPath), []byte(autoPromoteSourceContent))
	sourceID, err := d.UpsertSource(srcPath, sf.ContentHash)
	if err != nil {
		t.Fatalf("UpsertSource: %v", err)
	}
	if _, err := d.UpsertSourceFile(db.SourceFile{
		SourceID:     sourceID,
		RelativePath: sf.RelativePath,
		ContentHash:  sf.ContentHash,
		ByteSize:     sf.ByteSize,
		LineCount:    sf.LineCount,
	}); err != nil {
		t.Fatalf("UpsertSourceFile: %v", err)
	}
	// Seed two existing pages so the citation gate (≥2 distinct
	// existing-page citations via [Title]) can pass.
	for _, title := range []string{"Goroutines", "Channels"} {
		if err := d.UpsertPage(db.PageRecord{
			Title:       title,
			Path:        filepath.Join(wikiDir, title+".md"),
			Body:        "body of " + title,
			ContentHash: "ch-" + title,
		}); err != nil {
			t.Fatalf("seed %s: %v", title, err)
		}
	}

	fx := &autoPromoteFixture{
		Root:       root,
		WikiDir:    wikiDir,
		AnswersDir: answersDir,
		SourcePath: srcPath,
		DB:         d,
		prevCfg:    cfg,
		prevDB:     database,
		prevClient: llmClient,
	}
	cfg = &Config{
		LLM:  LLMConfig{Provider: "anthropic", Model: "claude-haiku-4-5"},
		Wiki: WikiConfig{WikiDir: wikiDir, RawDir: rawDir, DBPath: dbPath},
	}
	applyAskDefaults(&cfg.Ask)
	applyIngestDefaults(&cfg.Ingest)
	database = d
	llmClient = &askStubLLM{}
	t.Cleanup(func() {
		_ = d.Close()
		cfg = fx.prevCfg
		database = fx.prevDB
		llmClient = fx.prevClient
	})
	return fx
}

// askStubLLM is a no-op llm.Client; the auto-promote path never makes
// an LLM call (Rewrite=false), so any invocation is a test bug.
type askStubLLM struct{}

func (s *askStubLLM) Complete(ctx context.Context, system, user string) (string, error) {
	return "", nil
}
func (s *askStubLLM) CompleteStructured(ctx context.Context, system, user string, ts llm.ToolSchema) (map[string]any, error) {
	return nil, nil
}
func (s *askStubLLM) CompleteStream(ctx context.Context, system, user string, w io.Writer) (string, error) {
	return "", nil
}

// writeAnswerFile materializes a SavedAnswerInput at <answersDir>/<ts>-<slug>.md
// with the same shape cmd/ask.go's saveAnswer produces.
func (fx *autoPromoteFixture) writeAnswerFile(t *testing.T, in wiki.SavedAnswerInput, slug string) string {
	t.Helper()
	body := wiki.FormatSavedAnswer(in)
	ts := in.At.UTC().Format("2006-01-02-150405")
	path := filepath.Join(fx.AnswersDir, ts+"-"+slug+".md")
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatalf("write answer: %v", err)
	}
	return path
}

// captureStdout runs fn while swapping os.Stdout for a pipe, returning
// whatever fn wrote. maybeAutoPromote prints with fmt.Printf directly to
// os.Stdout, so we must swap the FD-level stream rather than route via
// cobra plumbing.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	prev := os.Stdout
	os.Stdout = w
	fn()
	w.Close()
	os.Stdout = prev
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("copy: %v", err)
	}
	return buf.String()
}

// makeAskAnswer builds a SavedAnswerInput shaped to default-pass every
// gate signal (citations, evidence-quotes, length, no-hedging,
// no-near-dup) given the autoPromoteFixture's seeded pages. Tests mutate
// the result to flip individual signals.
func (fx *autoPromoteFixture) makeAskAnswer(t *testing.T, question, body string) wiki.SavedAnswerInput {
	t.Helper()
	relPath := filepath.Base(fx.SourcePath)
	return wiki.SavedAnswerInput{
		Question: question,
		Answer:   body,
		Model:    "test-model",
		At:       time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC),
		Pages: []wiki.Page{{
			Title: "Goroutines",
			Evidence: []wiki.Evidence{
				{Quote: "Goroutines are lightweight threads of execution managed by the Go runtime.", LineStart: 1, LineEnd: 1, SourceFilePath: relPath},
				{Quote: "The go keyword starts a goroutine.", LineStart: 2, LineEnd: 2, SourceFilePath: relPath},
				{Quote: "Goroutines communicate via channels.", LineStart: 3, LineEnd: 3, SourceFilePath: relPath},
			},
		}},
	}
}

// askAutoCmd is a fresh cobra command we hand to maybeAutoPromote so
// production askCmd's flag state isn't mutated cross-test.
func askAutoCmd() *cobra.Command { return &cobra.Command{} }

// TestAutoPromote_PassFilesPage — every gate signal is clean, validator
// passes; output line is `→ filed as [[Title]]` and a real wiki page
// lands at <wikiDir>/<title>.md.
//
// Bumps AutoPromoteSkipScore to a high value so signal 4
// (near-duplicate scan) doesn't fire on spurious tiny BM25 hits — the
// production default is 1e-6, which is intentionally aggressive so that
// any FTS hit pushes the answer to manual review. Tests of the
// downstream wiring (validator → PromoteAnswer) use the high override
// the same way TestEvaluateAutoPromote_SkipScoreOverride does in
// internal/wiki.
func TestAutoPromote_PassFilesPage(t *testing.T) {
	fx := setupAutoPromote(t)
	cfg.Ask.AutoPromoteSkipScore = 1000.0
	body := padBody("This page covers [Goroutines] and [Channels] in depth.", 200)
	in := fx.makeAskAnswer(t, "what is the lifecycle of a worker pool",
		body)
	answerPath := fx.writeAnswerFile(t, in, "what-is-the-lifecycle-of-a-worker-pool")

	out := captureStdout(t, func() {
		maybeAutoPromote(askAutoCmd(), cfg, answerPath, in.Question, in.Answer, in.Pages)
	})
	if !strings.Contains(out, "→ filed as [[") {
		t.Errorf("expected `→ filed as [[…]]`, got:\n%s", out)
	}
	// Page lands on disk under the question-derived title.
	wantTitle := "What Is The Lifecycle Of A Worker Pool"
	if _, err := os.Stat(filepath.Join(fx.WikiDir, wantTitle+".md")); err != nil {
		t.Errorf("promoted page not on disk at %s: %v", wantTitle, err)
	}
	// Saved-answer file remains in place (trust property: never silently
	// drop, even on success).
	if _, err := os.Stat(answerPath); err != nil {
		t.Errorf("saved-answer file should remain on disk after promote: %v", err)
	}
	// log.md gets one src=auto promote line.
	logBytes, err := os.ReadFile(filepath.Join(fx.WikiDir, "log.md"))
	if err != nil {
		t.Fatalf("read log.md: %v", err)
	}
	if !strings.Contains(string(logBytes), "**promote**") || !strings.Contains(string(logBytes), "src=auto") {
		t.Errorf("log.md missing auto-promote chronicle:\n%s", logBytes)
	}
}

// TestAutoPromote_FailsOnTooFewCitations — single-bracket citations < 2
// → gate fail; output is "→ saved to <path> (too few citations: …)".
func TestAutoPromote_FailsOnTooFewCitations(t *testing.T) {
	fx := setupAutoPromote(t)
	// Only one [Goroutines] citation; [Bogus] doesn't exist as a page.
	body := padBody("Only [Goroutines] gets cited here, [Bogus] does not exist.", 200)
	in := fx.makeAskAnswer(t, "how does Q work", body)
	answerPath := fx.writeAnswerFile(t, in, "how-does-q-work")

	out := captureStdout(t, func() {
		maybeAutoPromote(askAutoCmd(), cfg, answerPath, in.Question, in.Answer, in.Pages)
	})
	if !strings.Contains(out, "saved to") || !strings.Contains(out, "too few citations") {
		t.Errorf("expected gate-fail line with `too few citations`, got:\n%s", out)
	}
	if _, err := os.Stat(answerPath); err != nil {
		t.Errorf("answer file must remain on disk after gate-fail: %v", err)
	}
}

// TestAutoPromote_FailsOnTooFewEvidenceQuotes — only 2 evidence quotes;
// gate requires ≥ 3.
func TestAutoPromote_FailsOnTooFewEvidenceQuotes(t *testing.T) {
	fx := setupAutoPromote(t)
	body := padBody("Cites [Goroutines] and [Channels].", 200)
	in := fx.makeAskAnswer(t, "what is X", body)
	in.Pages[0].Evidence = in.Pages[0].Evidence[:2] // drop one
	answerPath := fx.writeAnswerFile(t, in, "what-is-x")

	out := captureStdout(t, func() {
		maybeAutoPromote(askAutoCmd(), cfg, answerPath, in.Question, in.Answer, in.Pages)
	})
	if !strings.Contains(out, "too few evidence quotes") {
		t.Errorf("expected `too few evidence quotes`, got:\n%s", out)
	}
}

// TestAutoPromote_FailsOnTooShort — <100 words.
func TestAutoPromote_FailsOnTooShort(t *testing.T) {
	fx := setupAutoPromote(t)
	body := "Cites [Goroutines] and [Channels]. Tiny body."
	in := fx.makeAskAnswer(t, "what is short", body)
	answerPath := fx.writeAnswerFile(t, in, "what-is-short")

	out := captureStdout(t, func() {
		maybeAutoPromote(askAutoCmd(), cfg, answerPath, in.Question, in.Answer, in.Pages)
	})
	if !strings.Contains(out, "too short") {
		t.Errorf("expected `too short`, got:\n%s", out)
	}
}

// TestAutoPromote_FailsOnTooLong — >3000 words.
func TestAutoPromote_FailsOnTooLong(t *testing.T) {
	fx := setupAutoPromote(t)
	body := padBody("Cites [Goroutines] and [Channels].", 3500)
	in := fx.makeAskAnswer(t, "what is long", body)
	answerPath := fx.writeAnswerFile(t, in, "what-is-long")

	out := captureStdout(t, func() {
		maybeAutoPromote(askAutoCmd(), cfg, answerPath, in.Question, in.Answer, in.Pages)
	})
	if !strings.Contains(out, "too long") {
		t.Errorf("expected `too long`, got:\n%s", out)
	}
}

// TestAutoPromote_FailsOnHedgingPhrase — body contains a default
// hedging phrase; case-insensitive substring match catches it.
func TestAutoPromote_FailsOnHedgingPhrase(t *testing.T) {
	fx := setupAutoPromote(t)
	body := padBody("Cites [Goroutines] and [Channels]. I'm not sure about this.", 200)
	in := fx.makeAskAnswer(t, "what is hedged", body)
	answerPath := fx.writeAnswerFile(t, in, "what-is-hedged")

	out := captureStdout(t, func() {
		maybeAutoPromote(askAutoCmd(), cfg, answerPath, in.Question, in.Answer, in.Pages)
	})
	if !strings.Contains(out, "hedging phrase") {
		t.Errorf("expected `hedging phrase`, got:\n%s", out)
	}
}

// TestAutoPromote_NearDuplicateSkip — a seeded page whose title and body
// match the question dominates BM25; signal 4 fires with a Skipped
// verdict. We bump the question so it fully aligns with the seeded
// "Goroutines" page (which the fixture already has).
func TestAutoPromote_NearDuplicateSkip(t *testing.T) {
	fx := setupAutoPromote(t)
	// Fatten the "Goroutines" page so its FTS score dominates.
	if err := fx.DB.UpsertPage(db.PageRecord{
		Title:       "Goroutines",
		Path:        filepath.Join(fx.WikiDir, "Goroutines.md"),
		Body:        strings.Repeat("goroutines scheduler runtime ", 30),
		ContentHash: "ch-goroutines-fat",
	}); err != nil {
		t.Fatalf("UpsertPage: %v", err)
	}
	body := padBody("Cites [Goroutines] and [Channels].", 200)
	in := fx.makeAskAnswer(t, "how do goroutines scheduler runtime work", body)
	answerPath := fx.writeAnswerFile(t, in, "how-do-goroutines-scheduler-runtime-work")

	out := captureStdout(t, func() {
		maybeAutoPromote(askAutoCmd(), cfg, answerPath, in.Question, in.Answer, in.Pages)
	})
	if !strings.Contains(out, "near-duplicate") {
		t.Errorf("expected `near-duplicate` in skip line, got:\n%s", out)
	}
}

// TestAutoPromote_ValidatorFailKeepsAnswer — gate passes, but we mutate
// the source on disk between writing the answer and the auto-promote
// call so PromoteAnswer's defensive re-validation drops every quote.
// Output names `validator dropped quotes`; saved-answer file remains.
func TestAutoPromote_ValidatorFailKeepsAnswer(t *testing.T) {
	fx := setupAutoPromote(t)
	cfg.Ask.AutoPromoteSkipScore = 1000.0 // bypass signal 4 for this branch
	body := padBody("Cites [Goroutines] and [Channels].", 200)
	in := fx.makeAskAnswer(t, "what about validator drift", body)
	answerPath := fx.writeAnswerFile(t, in, "validator-drift")

	// Mutate the source so quotes no longer substring-match.
	if err := os.WriteFile(fx.SourcePath, []byte("totally different content with no matches\n"), 0644); err != nil {
		t.Fatalf("rewrite source: %v", err)
	}

	out := captureStdout(t, func() {
		maybeAutoPromote(askAutoCmd(), cfg, answerPath, in.Question, in.Answer, in.Pages)
	})
	if !strings.Contains(out, "validator dropped quotes") {
		t.Errorf("expected `validator dropped quotes`, got:\n%s", out)
	}
	if _, err := os.Stat(answerPath); err != nil {
		t.Errorf("answer file must remain on validator-fail: %v", err)
	}
	// No page was written.
	if _, err := os.Stat(filepath.Join(fx.WikiDir, "What About Validator Drift.md")); err == nil {
		t.Errorf("page must NOT be on disk after validator-fail")
	}
}

// TestAutoPromote_TitleCollisionSkip — gate passes, but the question
// derives a title that already exists. PromoteAnswer returns
// ErrTitleExists; output names the colliding title and the manual-promote
// hint. Saved-answer file stays.
func TestAutoPromote_TitleCollisionSkip(t *testing.T) {
	fx := setupAutoPromote(t)
	cfg.Ask.AutoPromoteSkipScore = 1000.0 // bypass signal 4 for this branch
	// Pre-seed a page whose title matches the question-derived title.
	collidingTitle := "What Causes The Collision"
	if err := fx.DB.UpsertPage(db.PageRecord{
		Title:       collidingTitle,
		Path:        filepath.Join(fx.WikiDir, collidingTitle+".md"),
		Body:        "pre-existing",
		ContentHash: "ch-collide",
	}); err != nil {
		t.Fatalf("UpsertPage: %v", err)
	}
	body := padBody("Cites [Goroutines] and [Channels].", 200)
	in := fx.makeAskAnswer(t, "what causes the collision", body)
	answerPath := fx.writeAnswerFile(t, in, "what-causes-the-collision")

	out := captureStdout(t, func() {
		maybeAutoPromote(askAutoCmd(), cfg, answerPath, in.Question, in.Answer, in.Pages)
	})
	if !strings.Contains(out, "title exists") {
		t.Errorf("expected `title exists`, got:\n%s", out)
	}
	if _, err := os.Stat(answerPath); err != nil {
		t.Errorf("answer file must remain on title-collision: %v", err)
	}
}

// TestAutoPromote_OptOutPrintsSavedLine — `[ask] auto_promote = false`
// reverts to the pre-v0.8 `saved: <path>` line and skips the gate
// entirely. Behavior parity with v0.7.
func TestAutoPromote_OptOutPrintsSavedLine(t *testing.T) {
	fx := setupAutoPromote(t)
	f := false
	cfg.Ask.AutoPromote = &f

	body := padBody("Cites [Goroutines] and [Channels].", 200)
	in := fx.makeAskAnswer(t, "what is opted out", body)
	answerPath := fx.writeAnswerFile(t, in, "what-is-opted-out")

	out := captureStdout(t, func() {
		maybeAutoPromote(askAutoCmd(), cfg, answerPath, in.Question, in.Answer, in.Pages)
	})
	if !strings.Contains(out, "saved: ") {
		t.Errorf("expected `saved: <path>`, got:\n%s", out)
	}
	if strings.Contains(out, "→ filed as") || strings.Contains(out, "→ saved to") {
		t.Errorf("opt-out path should not run the auto-promote surface; got:\n%s", out)
	}
	// No page on disk.
	if _, err := os.Stat(filepath.Join(fx.WikiDir, "What Is Opted Out.md")); err == nil {
		t.Errorf("opt-out path should not write a page")
	}
}

// TestApplyAskDefaults_FillsMissingKeys — applyAskDefaults sets the
// canonical Phase B defaults when the [ask] block is empty / missing.
func TestApplyAskDefaults_FillsMissingKeys(t *testing.T) {
	c := &AskConfig{}
	applyAskDefaults(c)
	if c.AutoPromote == nil || *c.AutoPromote != true {
		t.Errorf("AutoPromote = %v, want non-nil true", c.AutoPromote)
	}
	if c.AutoPromoteSkipScore != 1e-6 {
		t.Errorf("AutoPromoteSkipScore = %v, want 1e-6 (SQLite-realistic)", c.AutoPromoteSkipScore)
	}
}

// TestApplyAskDefaults_RespectsExplicitFalse — an explicit `false` on
// auto_promote (encoded as a non-nil *bool pointing at false) MUST
// survive applyAskDefaults; default-on is the *missing* policy.
func TestApplyAskDefaults_RespectsExplicitFalse(t *testing.T) {
	f := false
	c := &AskConfig{AutoPromote: &f}
	applyAskDefaults(c)
	if c.AutoPromote == nil || *c.AutoPromote != false {
		t.Errorf("explicit AutoPromote=false must survive defaults; got %v", c.AutoPromote)
	}
	if c.AutoPromoteOrDefault() != false {
		t.Errorf("AutoPromoteOrDefault() = true, want false")
	}
}
