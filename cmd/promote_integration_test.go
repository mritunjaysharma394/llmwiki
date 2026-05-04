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
//
// In replay-skip mode (no cassette on disk) the test Skips cleanly so
// CI stays green without fixtures. Same pattern Phase D established
// for TestIngestGemini / TestIngestOpenAICompat and Phase F's
// TestMCPWritePageRoundtrip.

package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
