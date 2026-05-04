package cmd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/mritunjaysharma394/llmwiki/internal/db"
	"github.com/mritunjaysharma394/llmwiki/internal/llm"
)

// stubLintLLM is a minimal llm.Client that returns canned responses so
// the lint tests can exercise runLint without recording cassettes. The
// schema_drift surface is independent of the LLM call (it only reads
// db.CountPagesByHashState), but the contradiction loop is unconditional
// when len(records) >= 2 and would otherwise hit the real provider.
type stubLintLLM struct {
	completeResp string
}

func (s *stubLintLLM) Complete(ctx context.Context, system, user string) (string, error) {
	if s.completeResp == "" {
		return "No contradictions found.", nil
	}
	return s.completeResp, nil
}

func (s *stubLintLLM) CompleteStructured(ctx context.Context, system, user string, ts llm.ToolSchema) (map[string]any, error) {
	return nil, errors.New("stubLintLLM: CompleteStructured not used by lint")
}

func (s *stubLintLLM) CompleteStream(ctx context.Context, system, user string, w io.Writer) (string, error) {
	resp := s.completeResp
	if resp == "" {
		resp = "No contradictions found."
	}
	if _, err := w.Write([]byte(resp)); err != nil {
		return "", err
	}
	return resp, nil
}

// setupLintEnv prepares a fresh tmp wiki with the package-level globals
// (database, llmClient, activeSchema) wired so runLint can be invoked.
// completeResp is the canned contradiction-check response.
func setupLintEnv(t *testing.T, completeResp string) {
	t.Helper()
	chdirTemp(t)
	resetProviderFlags(t)
	t.Setenv("GEMINI_API_KEY", "test-key-not-used")
	writeMinimalConfig(t, `[llm]
provider = "gemini"
model = "gemini-2.0-flash"

[wiki]
wiki_dir = ".llmwiki/wiki"
raw_dir = ".llmwiki/raw"
db_path = ".llmwiki/wiki.db"
`)
	for _, d := range []string{".llmwiki/wiki", ".llmwiki/raw"} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	if err := loadConfig(); err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	prevClient := llmClient
	llmClient = &stubLintLLM{completeResp: completeResp}
	t.Cleanup(func() { llmClient = prevClient })
}

// seedPage upserts a page and stamps its schema_hash to hash. Empty
// hash leaves the column at its default empty-string state (i.e. a
// pre-v0.7 ingest).
func seedPage(t *testing.T, title, hash string) {
	t.Helper()
	if err := database.UpsertPage(db.PageRecord{
		Title:       title,
		Path:        ".llmwiki/wiki/" + title + ".md",
		Body:        "body of " + title,
		ContentHash: "ch-" + title,
	}); err != nil {
		t.Fatalf("upsert %s: %v", title, err)
	}
	if hash == "" {
		return
	}
	rec, err := database.GetPage(title)
	if err != nil || rec == nil {
		t.Fatalf("get %s: %v", title, err)
	}
	if err := database.UpdateSchemaHash(rec.ID, hash); err != nil {
		t.Fatalf("UpdateSchemaHash %s: %v", title, err)
	}
}

// captureLintOutput swaps os.Stdout for a pipe and returns whatever
// runLint writes during the call. Mirrors captureStatusOutput.
func captureLintOutput(t *testing.T) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	prev := os.Stdout
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = prev })
	if err := runLint(lintCmd, nil); err != nil {
		t.Fatalf("runLint: %v", err)
	}
	w.Close()
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("copy: %v", err)
	}
	return buf.String()
}

// TestLint_SchemaDriftWarning_WhenPriorPagesExist seeds 5 pages — 3 at
// the active hash, 2 at a prior hash — and asserts runLint surfaces the
// schema_drift warning naming "2 page(s)" and the eager + lazy
// remediation recommendations.
func TestLint_SchemaDriftWarning_WhenPriorPagesExist(t *testing.T) {
	setupLintEnv(t, "")
	active := activeSchema.Hash()
	prior := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	for i, h := range []string{active, active, active, prior, prior} {
		seedPage(t, "P"+string(rune('A'+i)), h)
	}

	out := captureLintOutput(t)
	if !strings.Contains(out, "schema_drift") {
		t.Errorf("lint output missing 'schema_drift' line:\n%s", out)
	}
	if !strings.Contains(out, "2 page(s)") {
		t.Errorf("lint output should name the prior count '2 page(s)':\n%s", out)
	}
	if !strings.Contains(out, "llmwiki schema migrate") {
		t.Errorf("lint output should recommend 'llmwiki schema migrate':\n%s", out)
	}
	if !strings.Contains(out, "do nothing") {
		t.Errorf("lint output should mention the lazy 'do nothing' remediation:\n%s", out)
	}
	if !strings.Contains(out, "active hash: "+active[:8]) {
		t.Errorf("lint output should include the 8-char active hash prefix %q:\n%s", active[:8], out)
	}
}

// TestLint_NoSchemaDriftWarning_AllAtActive seeds 3 pages all at the
// active hash and asserts runLint emits no schema_drift line.
func TestLint_NoSchemaDriftWarning_AllAtActive(t *testing.T) {
	setupLintEnv(t, "")
	active := activeSchema.Hash()
	for _, title := range []string{"PA", "PB", "PC"} {
		seedPage(t, title, active)
	}

	out := captureLintOutput(t)
	if strings.Contains(out, "schema_drift") {
		t.Errorf("lint output should not include 'schema_drift' when all pages are at active hash:\n%s", out)
	}
}

// TestLint_SchemaDrift_PreservesExistingLintOutput confirms that the new
// schema_drift surface does not suppress the existing contradiction
// output: with two pages (one drifted) and a canned contradiction
// response, both surfaces fire in the same run.
func TestLint_SchemaDrift_PreservesExistingLintOutput(t *testing.T) {
	canned := "Possible contradiction: page A says X but page B says Y."
	setupLintEnv(t, canned)
	active := activeSchema.Hash()
	prior := "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	seedPage(t, "PA", active)
	seedPage(t, "PB", prior)

	out := captureLintOutput(t)
	if !strings.Contains(out, canned) {
		t.Errorf("lint output should include the canned contradiction response:\n%s", out)
	}
	if !strings.Contains(out, "schema_drift") {
		t.Errorf("lint output should also include the schema_drift line:\n%s", out)
	}
	if !strings.Contains(out, "1 page(s)") {
		t.Errorf("lint output should report '1 page(s)' on prior schema:\n%s", out)
	}
}
