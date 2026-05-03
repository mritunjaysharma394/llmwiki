package db

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOpenCreatesEvidenceAndSavedAnswers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wiki.db")
	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	for _, table := range []string{"evidence", "evidence_fts", "saved_answers", "saved_answers_fts"} {
		var name string
		err := d.sql.QueryRow(`SELECT name FROM sqlite_master WHERE name = ?`, table).Scan(&name)
		if err != nil {
			t.Errorf("table %q missing: %v", table, err)
		}
	}

	var version int
	if err := d.sql.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("user_version: %v", err)
	}
	if version != 2 {
		t.Errorf("user_version = %d, want 2", version)
	}
}

func TestOpenIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wiki.db")
	d1, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	d1.Close()
	d2, err := Open(path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer d2.Close()
	var version int
	if err := d2.sql.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("user_version: %v", err)
	}
	if version != 2 {
		t.Errorf("user_version after re-open = %d, want 2", version)
	}
}

func TestOpenUpgradesLegacyDB(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wiki.db")
	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for _, tbl := range []string{"evidence_fts", "evidence", "saved_answers_fts", "saved_answers"} {
		d.sql.Exec(`DROP TABLE ` + tbl)
	}
	d.sql.Exec(`PRAGMA user_version = 0`)
	d.Close()

	d2, err := Open(path)
	if err != nil {
		t.Fatalf("re-Open: %v", err)
	}
	defer d2.Close()
	var version int
	d2.sql.QueryRow(`PRAGMA user_version`).Scan(&version)
	if version != 2 {
		t.Errorf("user_version after upgrade = %d, want 2", version)
	}
}

func TestOpenAtFreshV2(t *testing.T) {
	d := mustOpen(t)
	for _, table := range []string{"source_files"} {
		var name string
		err := d.sql.QueryRow(`SELECT name FROM sqlite_master WHERE name = ?`, table).Scan(&name)
		if err != nil {
			t.Errorf("table %q missing: %v", table, err)
		}
	}
	// evidence.source_file_id column present?
	rows, err := d.sql.Query(`PRAGMA table_info(evidence)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var hasCol bool
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull, pk int
		var dflt any
		rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk)
		if name == "source_file_id" {
			hasCol = true
		}
	}
	if !hasCol {
		t.Error("evidence.source_file_id column missing")
	}
	var version int
	d.sql.QueryRow(`PRAGMA user_version`).Scan(&version)
	if version != 2 {
		t.Errorf("user_version = %d, want 2", version)
	}
}

func TestOpenUpgradesV1ToV2(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wiki.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	// Force back to v1 state.
	d.sql.Exec(`DROP TABLE source_files`)
	// Drop the new column by recreating evidence without it.
	d.sql.Exec(`PRAGMA user_version = 1`)
	d.Close()

	d2, err := Open(path)
	if err != nil {
		t.Fatalf("re-open: %v", err)
	}
	defer d2.Close()
	var v int
	d2.sql.QueryRow(`PRAGMA user_version`).Scan(&v)
	if v != 2 {
		t.Errorf("user_version after upgrade = %d, want 2", v)
	}
	var name string
	if err := d2.sql.QueryRow(`SELECT name FROM sqlite_master WHERE name = 'source_files'`).Scan(&name); err != nil {
		t.Errorf("source_files not recreated on upgrade: %v", err)
	}
}

func TestOpenUpgradesLegacyV0ToV2(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wiki.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, tbl := range []string{"source_files", "evidence_fts", "evidence", "saved_answers_fts", "saved_answers"} {
		d.sql.Exec(`DROP TABLE ` + tbl)
	}
	d.sql.Exec(`PRAGMA user_version = 0`)
	d.Close()

	d2, err := Open(path)
	if err != nil {
		t.Fatalf("re-open: %v", err)
	}
	defer d2.Close()
	var v int
	d2.sql.QueryRow(`PRAGMA user_version`).Scan(&v)
	if v != 2 {
		t.Errorf("user_version after v0->v2 upgrade = %d, want 2", v)
	}
}

func mustOpen(t *testing.T) *DB {
	t.Helper()
	dir := t.TempDir()
	d, err := Open(dir + "/wiki.db")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func TestEvidenceCRUD(t *testing.T) {
	d := mustOpen(t)
	srcID, err := d.UpsertSource("file:///foo.txt", "abc123")
	if err != nil {
		t.Fatalf("UpsertSource: %v", err)
	}
	if err := d.UpsertPage(PageRecord{Title: "Page A", Path: "wiki/page-a.md", Body: "body", ContentHash: "h1", SourceIDs: []int64{srcID}}); err != nil {
		t.Fatalf("UpsertPage: %v", err)
	}
	page, _ := d.GetPage("Page A")
	if page == nil {
		t.Fatal("page not found after upsert")
	}

	if err := d.InsertEvidence(page.ID, srcID, []Evidence{
		{Quote: "the quick brown fox", LineStart: 3, LineEnd: 3},
		{Quote: "lazy dog", LineStart: 5, LineEnd: 5},
	}); err != nil {
		t.Fatalf("InsertEvidence: %v", err)
	}

	got, err := d.GetEvidenceForPage(page.ID)
	if err != nil {
		t.Fatalf("GetEvidenceForPage: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d evidence rows, want 2", len(got))
	}
	if got[0].Quote != "the quick brown fox" {
		t.Errorf("first quote = %q", got[0].Quote)
	}
}

func TestSearchEvidence(t *testing.T) {
	d := mustOpen(t)
	srcID, _ := d.UpsertSource("u", "h")
	d.UpsertPage(PageRecord{Title: "P1", Path: "p1.md", Body: "b1", ContentHash: "h", SourceIDs: []int64{srcID}})
	d.UpsertPage(PageRecord{Title: "P2", Path: "p2.md", Body: "b2", ContentHash: "h", SourceIDs: []int64{srcID}})
	p1, _ := d.GetPage("P1")
	p2, _ := d.GetPage("P2")
	d.InsertEvidence(p1.ID, srcID, []Evidence{{Quote: "kafka consumer group offset"}})
	d.InsertEvidence(p2.ID, srcID, []Evidence{{Quote: "rabbitmq dead letter"}})

	hits, err := d.SearchEvidence("kafka", 10)
	if err != nil {
		t.Fatalf("SearchEvidence: %v", err)
	}
	if len(hits) != 1 || hits[0].PageID != p1.ID {
		t.Errorf("hits = %+v, want one for p1", hits)
	}
}

func TestDeleteEvidenceForSource(t *testing.T) {
	d := mustOpen(t)
	srcID, _ := d.UpsertSource("u", "h")
	d.UpsertPage(PageRecord{Title: "P1", Path: "p.md", Body: "b", ContentHash: "h", SourceIDs: []int64{srcID}})
	p, _ := d.GetPage("P1")
	d.InsertEvidence(p.ID, srcID, []Evidence{{Quote: "to be deleted"}})

	if err := d.DeleteEvidenceForSource(srcID); err != nil {
		t.Fatalf("DeleteEvidenceForSource: %v", err)
	}
	got, _ := d.GetEvidenceForPage(p.ID)
	if len(got) != 0 {
		t.Errorf("got %d evidence after delete, want 0", len(got))
	}
}

func TestSavedAnswerCRUD(t *testing.T) {
	d := mustOpen(t)
	id, err := d.InsertSavedAnswer(SavedAnswer{
		Question:     "what is X?",
		Answer:       "X is Y",
		Model:        "claude-haiku-4-5",
		CitedPageIDs: []int64{1, 2},
		FilePath:     ".llmwiki/answers/2026-05-03-101010-what-is-x.md",
		CreatedAt:    time.Now(),
	})
	if err != nil || id == 0 {
		t.Fatalf("InsertSavedAnswer: id=%d err=%v", id, err)
	}
	hits, err := d.SearchSavedAnswers("what", 10)
	if err != nil {
		t.Fatalf("SearchSavedAnswers: %v", err)
	}
	if len(hits) != 1 || !strings.Contains(hits[0].Question, "what") {
		t.Errorf("hits = %+v", hits)
	}
}

func TestStatsIncludesEvidenceAndAnswers(t *testing.T) {
	d := mustOpen(t)
	srcID, _ := d.UpsertSource("u", "h")
	d.UpsertPage(PageRecord{Title: "P1", Path: "p.md", Body: "b", ContentHash: "h", SourceIDs: []int64{srcID}})
	p, _ := d.GetPage("P1")
	d.InsertEvidence(p.ID, srcID, []Evidence{{Quote: "q"}})
	d.UpsertPage(PageRecord{Title: "Legacy", Path: "l.md", Body: "b", ContentHash: "h", SourceIDs: []int64{srcID}})
	d.InsertSavedAnswer(SavedAnswer{Question: "q", Answer: "a", FilePath: "f.md", CreatedAt: time.Now()})

	stats, err := d.GetStats()
	if err != nil {
		t.Fatalf("GetStats: %v", err)
	}
	if stats.EvidenceQuotes != 1 {
		t.Errorf("EvidenceQuotes=%d, want 1", stats.EvidenceQuotes)
	}
	if stats.LegacyPages != 1 {
		t.Errorf("LegacyPages=%d, want 1", stats.LegacyPages)
	}
	if stats.SavedAnswers != 1 {
		t.Errorf("SavedAnswers=%d, want 1", stats.SavedAnswers)
	}
}

func TestEvidenceCascadeDeleteOnSourceDelete(t *testing.T) {
	d := mustOpen(t)
	srcID, _ := d.UpsertSource("u1", "h1")
	d.UpsertPage(PageRecord{Title: "P1", Path: "p.md", Body: "b", ContentHash: "h", SourceIDs: []int64{srcID}})
	p, _ := d.GetPage("P1")
	d.InsertEvidence(p.ID, srcID, []Evidence{{Quote: "q"}})

	if _, err := d.sql.Exec(`DELETE FROM sources WHERE id = ?`, srcID); err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	got, _ := d.GetEvidenceForPage(p.ID)
	if len(got) != 0 {
		t.Errorf("evidence not cascade-deleted: got %d rows", len(got))
	}
}

func TestSearchPagesAndEvidenceUnion(t *testing.T) {
	d := mustOpen(t)
	srcID, _ := d.UpsertSource("u", "h")
	d.UpsertPage(PageRecord{Title: "Goroutines", Path: "g.md", Body: "lightweight", ContentHash: "h", SourceIDs: []int64{srcID}})
	d.UpsertPage(PageRecord{Title: "Channels", Path: "c.md", Body: "communication primitive", ContentHash: "h", SourceIDs: []int64{srcID}})
	g, _ := d.GetPage("Goroutines")
	c, _ := d.GetPage("Channels")
	d.InsertEvidence(g.ID, srcID, []Evidence{{Quote: "scheduler picks runnable goroutines"}})
	d.InsertEvidence(c.ID, srcID, []Evidence{{Quote: "channels block when full"}})

	pages, _ := d.SearchPages("communication", 5)
	if len(pages) != 1 || pages[0].Title != "Channels" {
		t.Errorf("page search: %+v", pages)
	}
	hits, _ := d.SearchEvidence("scheduler", 5)
	if len(hits) != 1 || hits[0].PageID != g.ID {
		t.Errorf("evidence search: %+v", hits)
	}
}
