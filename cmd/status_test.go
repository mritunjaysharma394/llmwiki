package cmd

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mritunjaysharma394/llmwiki/internal/db"
)

// validSchemaDoc is reused from cmd/root_test.go; the schema-line tests
// below write it to AGENTS.md / CLAUDE.md at the wiki root so loadConfig
// resolves activeSchema to the user-edited document.

// setupStatusEnv prepares a fresh tmp wiki + opens the package-level
// database singleton so runStatus can call database.GetStats /
// CountPageUpdateLogByOutcome. Returns the page ID for tests that want
// to seed page_update_log rows.
func setupStatusEnv(t *testing.T) int64 {
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
	if err := database.UpsertPage(db.PageRecord{
		Title:       "Seed",
		Path:        ".llmwiki/wiki/seed.md",
		Body:        "body",
		ContentHash: "h0",
	}); err != nil {
		t.Fatalf("upsert page: %v", err)
	}
	rec, err := database.GetPage("Seed")
	if err != nil || rec == nil {
		t.Fatalf("get seed page: %v", err)
	}
	return rec.ID
}

func captureStatusOutput(t *testing.T) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	prev := os.Stdout
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = prev })
	if err := runStatus(statusCmd, nil); err != nil {
		t.Fatalf("runStatus: %v", err)
	}
	w.Close()
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("copy: %v", err)
	}
	return buf.String()
}

// TestStatus_OmitsPageUpdateLogLinesWhenZero: a fresh DB with no
// page_update_log rows should not surface "pages updated total" or
// "pages update failed" lines.
func TestStatus_OmitsPageUpdateLogLinesWhenZero(t *testing.T) {
	setupStatusEnv(t)
	out := captureStatusOutput(t)
	if strings.Contains(out, "pages updated total") {
		t.Errorf("status output mentions pages_updated_total with zero rows:\n%s", out)
	}
	if strings.Contains(out, "pages update failed") {
		t.Errorf("status output mentions pages_update_failed with zero rows:\n%s", out)
	}
}

// TestStatus_ShowsPagesUpdatedTotalWhenNonZero: with three updated
// rows, status surfaces "pages updated total: 3".
func TestStatus_ShowsPagesUpdatedTotalWhenNonZero(t *testing.T) {
	pageID := setupStatusEnv(t)
	for i := 0; i < 3; i++ {
		if err := database.InsertPageUpdateLog(db.PageUpdateLogEntry{
			PageID:           pageID,
			PriorContentHash: "h0",
			NewContentHash:   "h1",
			Outcome:          "updated",
			EvidenceAdded:    1,
		}); err != nil {
			t.Fatalf("insert log: %v", err)
		}
	}
	out := captureStatusOutput(t)
	if !strings.Contains(out, "pages updated total: 3") {
		t.Errorf("status output missing 'pages updated total: 3':\n%s", out)
	}
}

// TestStatus_ShowsPagesUpdateFailedTotalWhenNonZero: with one failed
// row, status surfaces "pages update failed: 1".
func TestStatus_ShowsPagesUpdateFailedTotalWhenNonZero(t *testing.T) {
	pageID := setupStatusEnv(t)
	if err := database.InsertPageUpdateLog(db.PageUpdateLogEntry{
		PageID:           pageID,
		PriorContentHash: "h0",
		Outcome:          "failed",
		Reason:           "zero-quotes-matched",
	}); err != nil {
		t.Fatalf("insert log: %v", err)
	}
	out := captureStatusOutput(t)
	if !strings.Contains(out, "pages update failed: 1") {
		t.Errorf("status output missing 'pages update failed: 1':\n%s", out)
	}
}

// TestStatus_BodyOnlyAndSkippedNotInTotal: body_only and skipped rows
// must not be counted into pages_updated_total — they are not "updates"
// in the user-facing sense (spec line 191).
func TestStatus_BodyOnlyAndSkippedNotInTotal(t *testing.T) {
	pageID := setupStatusEnv(t)
	mix := []db.PageUpdateLogEntry{
		{PageID: pageID, PriorContentHash: "h0", NewContentHash: "h1", Outcome: "updated"},
		{PageID: pageID, PriorContentHash: "h0", NewContentHash: "h0", Outcome: "body_only"},
		{PageID: pageID, PriorContentHash: "h0", Outcome: "failed"},
		{PageID: pageID, PriorContentHash: "h0", Outcome: "skipped"},
	}
	for _, e := range mix {
		if err := database.InsertPageUpdateLog(e); err != nil {
			t.Fatalf("insert log: %v", err)
		}
	}
	out := captureStatusOutput(t)
	if !strings.Contains(out, "pages updated total: 1") {
		t.Errorf("status output should surface 'pages updated total: 1' (body_only and skipped excluded):\n%s", out)
	}
	if !strings.Contains(out, "pages update failed: 1") {
		t.Errorf("status output should surface 'pages update failed: 1':\n%s", out)
	}
}

// --- Phase H / Task 13: schema: line ---

// setupStatusEnvWithSchemaDoc is setupStatusEnv but writes a schema doc
// at <name> in the wiki root *before* loadConfig runs, so activeSchema
// resolves to the user-edited document instead of schema.Bundled().
// Returns the page ID for callers that want to seed page_update_log
// rows. When name is "", no schema doc is written (caller exercises the
// "fell back to bundled" branch).
func setupStatusEnvWithSchemaDoc(t *testing.T, name string) int64 {
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
	if name != "" {
		if err := os.WriteFile(filepath.Join(".", name), []byte(validSchemaDoc), 0644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if err := loadConfig(); err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if err := database.UpsertPage(db.PageRecord{
		Title:       "Seed",
		Path:        ".llmwiki/wiki/seed.md",
		Body:        "body",
		ContentHash: "h0",
	}); err != nil {
		t.Fatalf("upsert page: %v", err)
	}
	rec, err := database.GetPage("Seed")
	if err != nil || rec == nil {
		t.Fatalf("get seed page: %v", err)
	}
	return rec.ID
}

// TestStatus_ShowsSchemaLine_WithAGENTSMd: with AGENTS.md present at
// the wiki root, status surfaces "schema: AGENTS.md (hash <prefix>...,
// N pages on ...)".
func TestStatus_ShowsSchemaLine_WithAGENTSMd(t *testing.T) {
	setupStatusEnvWithSchemaDoc(t, "AGENTS.md")
	out := captureStatusOutput(t)
	if !strings.Contains(out, "schema:") {
		t.Errorf("status output missing 'schema:' line:\n%s", out)
	}
	if !strings.Contains(out, "AGENTS.md") {
		t.Errorf("status output should label the schema 'AGENTS.md':\n%s", out)
	}
	hashPrefix := activeSchema.Hash()[:8]
	if !strings.Contains(out, "hash "+hashPrefix+"...") {
		t.Errorf("status output missing 8-char hash prefix %q:\n%s", hashPrefix, out)
	}
	// Seed page was upserted with no schema_hash stamp -> "" -> not
	// at active hash, so the line must surface drift wording.
	if !strings.Contains(out, "on prior hash") {
		t.Errorf("status output should mention 'on prior hash' when seed page is unstamped:\n%s", out)
	}
}

// TestStatus_ShowsSchemaLine_WithCLAUDEMd: with only CLAUDE.md present,
// the schema line labels the active doc 'CLAUDE.md'.
func TestStatus_ShowsSchemaLine_WithCLAUDEMd(t *testing.T) {
	setupStatusEnvWithSchemaDoc(t, "CLAUDE.md")
	out := captureStatusOutput(t)
	if !strings.Contains(out, "schema:") {
		t.Errorf("status output missing 'schema:' line:\n%s", out)
	}
	if !strings.Contains(out, "CLAUDE.md") {
		t.Errorf("status output should label the schema 'CLAUDE.md':\n%s", out)
	}
	if strings.Contains(out, "AGENTS.md") {
		t.Errorf("status output should not name AGENTS.md when only CLAUDE.md is present:\n%s", out)
	}
}

// TestStatus_ShowsSchemaLine_NoSchemaDoc: with no schema doc at the
// wiki root, status falls back to the bundled label.
func TestStatus_ShowsSchemaLine_NoSchemaDoc(t *testing.T) {
	setupStatusEnvWithSchemaDoc(t, "")
	out := captureStatusOutput(t)
	if !strings.Contains(out, "schema:") {
		t.Errorf("status output missing 'schema:' line:\n%s", out)
	}
	if !strings.Contains(out, "bundled (no AGENTS.md or CLAUDE.md)") {
		t.Errorf("status output should fall back to 'bundled (no AGENTS.md or CLAUDE.md)':\n%s", out)
	}
}

// TestStatus_ShowsSchemaLine_WithDriftedPages: pre-seed pages stamped
// with a non-active hash and assert the drift count appears with the
// "on prior hash" wording.
func TestStatus_ShowsSchemaLine_WithDriftedPages(t *testing.T) {
	pageID := setupStatusEnvWithSchemaDoc(t, "AGENTS.md")
	// Stamp the seed page with a non-active hash so it counts toward
	// the prior bucket; add four more drifted pages so the count is
	// distinctive.
	priorHash := "deadbeef" + strings.Repeat("0", 56)
	if err := database.UpdateSchemaHash(pageID, priorHash); err != nil {
		t.Fatalf("UpdateSchemaHash seed: %v", err)
	}
	for i := 0; i < 4; i++ {
		title := "Drifted" + string(rune('A'+i))
		if err := database.UpsertPage(db.PageRecord{
			Title:       title,
			Path:        ".llmwiki/wiki/" + title + ".md",
			Body:        "body",
			ContentHash: "h" + title,
		}); err != nil {
			t.Fatalf("upsert %s: %v", title, err)
		}
		rec, err := database.GetPage(title)
		if err != nil || rec == nil {
			t.Fatalf("get %s: %v", title, err)
		}
		if err := database.UpdateSchemaHash(rec.ID, priorHash); err != nil {
			t.Fatalf("UpdateSchemaHash %s: %v", title, err)
		}
	}
	out := captureStatusOutput(t)
	if !strings.Contains(out, "5 pages on prior hash") {
		t.Errorf("status output should report '5 pages on prior hash':\n%s", out)
	}
}
