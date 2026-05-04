// Package cmd — promote_integration_test.go
//
// Cassette-driven end-to-end tests for the v1.2 `llmwiki promote` flow.
// They drive the full init → ingest → ask → promote loop through the
// real loadConfig path (so the cassette wraps the configured Gemini /
// Anthropic / OpenAI-compat client just as it does for ingest).
//
// Recording is a manual operator step:
//
//	LLMWIKI_RECORD=1 GEMINI_API_KEY=... go test ./cmd/ -run TestPromoteAnswerHappyPath -v
//	LLMWIKI_RECORD=1 GEMINI_API_KEY=... go test ./cmd/ -run TestPromoteAnswerStaleEvidence -v
//
// In replay-skip mode (no cassette on disk) the tests Skip cleanly so
// CI stays green without fixtures. Same pattern Phase D established
// for TestIngestGemini / TestIngestOpenAICompat and Phase F's
// TestMCPWritePageRoundtrip.

package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mritunjaysharma394/llmwiki/internal/cliutil"
	"github.com/mritunjaysharma394/llmwiki/internal/wiki"
)

// TestPromoteAnswerHappyPath drives a full ingest → ask → promote loop
// against a recorded cassette. Asserts the promoted page lands on disk
// with the same evidence quotes the answer file carried, and log.md
// gains a **promote** chronicle line — the v1.2 analogue of
// TestIngest_GeneratesIndexAndLog's **ingest** assertion.
//
// Trust property covered: every evidence quote on the promoted page
// substring-matches the originally-ingested source bytes. The cassette
// records both the ingest call and the ask call against Gemini Flash
// (cfg.LLM.Model = "gemini-2.0-flash"); promote itself does not call
// the LLM (verbatim body) so no third segment is needed.
func TestPromoteAnswerHappyPath(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cassette test in -short mode")
	}
	cassette := filepath.Join(realCassetteDir(t), "TestPromoteAnswerHappyPath__001.json")
	if _, err := os.Stat(cassette); os.IsNotExist(err) && os.Getenv("LLMWIKI_RECORD") == "" {
		t.Skip("cassette not recorded; run with LLMWIKI_RECORD=1 GEMINI_API_KEY=... to record")
	}
	if os.Getenv("LLMWIKI_RECORD") != "" && os.Getenv("GEMINI_API_KEY") == "" {
		t.Fatal("LLMWIKI_RECORD set but GEMINI_API_KEY missing")
	}
	t.Setenv("LLMWIKI_CASSETTE", "TestPromoteAnswerHappyPath")
	if os.Getenv("GEMINI_API_KEY") == "" {
		t.Setenv("GEMINI_API_KEY", "test-key-for-replay")
	}
	resetPromoteFlags(t)

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

	// Phase 1: ingest synthetic source so the wiki has at least one page
	// for ask to retrieve. Reuses runIngestThroughLoadConfig from
	// ingest_test.go — same loadConfig + symlinked-cassettes wiring.
	pages := runIngestThroughLoadConfig(t, source, configBody)
	if len(pages) == 0 {
		t.Fatal("ingest produced no pages")
	}
	wikiDir := cfg.Wiki.WikiDir
	answersDir := filepath.Join(filepath.Dir(wikiDir), "answers")
	if err := os.MkdirAll(answersDir, 0755); err != nil {
		t.Fatalf("mkdir answers: %v", err)
	}

	// Phase 2: ask a question. runAsk's saveAnswer drops a
	// .llmwiki/answers/<ts>-<slug>.md file we can promote in phase 3.
	t.Cleanup(func() {
		askCmd.Flags().Set("no-stream", "false")
		askCmd.Flags().Set("no-save", "false")
		askCmd.Flags().Set("out", "")
	})
	askCmd.Flags().Set("no-stream", "true")
	askCmd.Flags().Set("no-save", "false")
	if err := runAsk(askCmd, []string{"what", "are", "goroutines?"}); err != nil {
		t.Fatalf("runAsk: %v", err)
	}
	answerPath := latestAnswerFile(t, answersDir)

	// Phase 3: promote. No LLM call (verbatim body). Validator re-checks
	// every quote against the ingested source.
	promoteCmd.Flags().Set("title", "Goroutines Promoted")
	if err := runPromote(promoteCmd, []string{answerPath}); err != nil {
		t.Fatalf("runPromote: %v", err)
	}

	// Page lands at <wikiDir>/<title>.md.
	pagePath := filepath.Join(wikiDir, "Goroutines Promoted.md")
	pageBytes, err := os.ReadFile(pagePath)
	if err != nil {
		t.Fatalf("promoted page not on disk: %v", err)
	}
	page, err := wiki.ParsePage(string(pageBytes))
	if err != nil {
		t.Fatalf("parse promoted page: %v", err)
	}
	if len(page.Evidence) == 0 {
		t.Errorf("promoted page has no evidence")
	}

	// Trust property: every quote substring-matches the original source.
	for _, e := range page.Evidence {
		if !strings.Contains(source, e.Quote) {
			t.Errorf("promoted-page evidence quote not in source: %q", e.Quote)
		}
	}

	// log.md got a **promote** line.
	logBytes, err := os.ReadFile(filepath.Join(wikiDir, "log.md"))
	if err != nil {
		t.Fatalf("read log.md: %v", err)
	}
	if !strings.Contains(string(logBytes), "**promote**") {
		t.Errorf("log.md missing **promote** line:\n%s", logBytes)
	}
}

// TestPromoteAnswerStaleEvidence drives the same ingest → ask flow as
// TestPromoteAnswerHappyPath, then mutates the source file on disk so
// every quote in the saved answer fails defensive re-validation.
// Asserts wiki.PromoteAnswer surfaces ErrEvidenceInvalid (rendered via
// cliutil.UserError), no page hits disk, no **promote** line in log.md.
//
// This is the v1.2 analogue of TestMCPWritePageRoundtrip's invalid-
// evidence assertion: the same trust-property guarantee that gates
// ingest and mcp.write_page also gates promote.
func TestPromoteAnswerStaleEvidence(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cassette test in -short mode")
	}
	cassette := filepath.Join(realCassetteDir(t), "TestPromoteAnswerStaleEvidence__001.json")
	if _, err := os.Stat(cassette); os.IsNotExist(err) && os.Getenv("LLMWIKI_RECORD") == "" {
		t.Skip("cassette not recorded; run with LLMWIKI_RECORD=1 GEMINI_API_KEY=... to record")
	}
	if os.Getenv("LLMWIKI_RECORD") != "" && os.Getenv("GEMINI_API_KEY") == "" {
		t.Fatal("LLMWIKI_RECORD set but GEMINI_API_KEY missing")
	}
	t.Setenv("LLMWIKI_CASSETTE", "TestPromoteAnswerStaleEvidence")
	if os.Getenv("GEMINI_API_KEY") == "" {
		t.Setenv("GEMINI_API_KEY", "test-key-for-replay")
	}
	resetPromoteFlags(t)

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
	pages := runIngestThroughLoadConfig(t, source, configBody)
	if len(pages) == 0 {
		t.Fatal("ingest produced no pages")
	}
	wikiDir := cfg.Wiki.WikiDir
	answersDir := filepath.Join(filepath.Dir(wikiDir), "answers")
	if err := os.MkdirAll(answersDir, 0755); err != nil {
		t.Fatalf("mkdir answers: %v", err)
	}

	t.Cleanup(func() {
		askCmd.Flags().Set("no-stream", "false")
		askCmd.Flags().Set("no-save", "false")
		askCmd.Flags().Set("out", "")
	})
	askCmd.Flags().Set("no-stream", "true")
	askCmd.Flags().Set("no-save", "false")
	if err := runAsk(askCmd, []string{"what", "are", "goroutines?"}); err != nil {
		t.Fatalf("runAsk: %v", err)
	}
	answerPath := latestAnswerFile(t, answersDir)

	// Mutate every ingested source on disk so no quote substring-matches.
	// runIngestThroughLoadConfig writes its synthetic source under
	// t.TempDir(); we walk db.GetAllSources to find the URI. Each source
	// URI is an absolute path on disk for local-file ingests.
	srcs, err := database.GetAllSources()
	if err != nil {
		t.Fatalf("GetAllSources: %v", err)
	}
	if len(srcs) == 0 {
		t.Fatal("no sources recorded — ingest didn't run as expected")
	}
	for _, s := range srcs {
		if err := os.WriteFile(s.URI, []byte("totally different content with no quote matches\n"), 0644); err != nil {
			t.Fatalf("rewrite source %s: %v", s.URI, err)
		}
	}

	// Promote. Defensive re-validation must drop every quote.
	promoteCmd.Flags().Set("title", "Stale Goroutines")
	err = runPromote(promoteCmd, []string{answerPath})
	if err == nil {
		t.Fatal("expected error for stale evidence; got nil")
	}
	var ue *cliutil.UserError
	if errors.As(err, &ue) {
		if !strings.Contains(ue.Cause, "evidence_invalid") {
			t.Errorf("UserError.Cause should mention evidence_invalid; got %q", ue.Cause)
		}
	} else if !errors.Is(err, wiki.ErrEvidenceInvalid) {
		// runPromote wraps ErrEvidenceInvalid via cliutil.Wrap, which
		// returns *cliutil.UserError; if the error type changes we want
		// to know loudly.
		t.Fatalf("expected *cliutil.UserError or wiki.ErrEvidenceInvalid; got %T: %v", err, err)
	}

	// No page on disk.
	if _, err := os.Stat(filepath.Join(wikiDir, "Stale Goroutines.md")); err == nil {
		t.Error("Stale Goroutines.md should NOT exist after evidence_invalid")
	}

	// No **promote** line in log.md (file may exist from ingest, but the
	// promote line must not be present).
	if logBytes, err := os.ReadFile(filepath.Join(wikiDir, "log.md")); err == nil {
		if strings.Contains(string(logBytes), "**promote**") {
			t.Errorf("log.md should NOT contain **promote** after evidence_invalid:\n%s", logBytes)
		}
	}
}

// latestAnswerFile returns the absolute path of the most-recently-written
// .md file under answersDir. The promote tests use this to discover the
// file runAsk just created without depending on saveAnswer's exact name
// format.
func latestAnswerFile(t *testing.T, answersDir string) string {
	t.Helper()
	entries, err := os.ReadDir(answersDir)
	if err != nil {
		t.Fatalf("read answers dir: %v", err)
	}
	var newest string
	var newestMod time.Time
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		full := filepath.Join(answersDir, e.Name())
		fi, err := os.Stat(full)
		if err != nil {
			continue
		}
		if fi.ModTime().After(newestMod) {
			newest = full
			newestMod = fi.ModTime()
		}
	}
	if newest == "" {
		t.Fatalf("no answer file found in %s", answersDir)
	}
	return newest
}
