package cmd

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/mritunjaysharma394/llmwiki/internal/db"
)

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
