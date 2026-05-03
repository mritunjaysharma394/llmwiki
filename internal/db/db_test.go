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
	if version != 1 {
		t.Errorf("user_version = %d, want 1", version)
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
	if version != 1 {
		t.Errorf("user_version after re-open = %d, want 1", version)
	}
}

func TestOpenUpgradesLegacyDB(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wiki.db")
	// Simulate a legacy db: open, drop new tables manually, set version to 0
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
	if version != 1 {
		t.Errorf("user_version after upgrade = %d, want 1", version)
	}
}

func mustOpen(t *testing.T) *DB {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "wiki.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	// Force a stress-test by clearing the pool so subsequent calls open new conns.
	d.sql.SetMaxIdleConns(0)
	return d
}

func mustExec(t *testing.T, d *DB, q string, args ...any) {
	t.Helper()
	if _, err := d.sql.Exec(q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}

func mustInsert(t *testing.T, d *DB, q string, args ...any) int64 {
	t.Helper()
	var id int64
	if err := d.sql.QueryRow(q, args...).Scan(&id); err != nil {
		t.Fatalf("insert %q: %v", q, err)
	}
	return id
}

func TestEvidenceCascadesOnPageDelete(t *testing.T) {
	d := mustOpen(t)
	srcID := mustInsert(t, d, `INSERT INTO sources (uri, content_hash) VALUES ('u', 'h') RETURNING id`)
	pageID := mustInsert(t, d, `INSERT INTO pages (title, body, content_hash) VALUES ('P', 'b', 'h') RETURNING id`)
	mustExec(t, d, `INSERT INTO evidence (page_id, source_id, quote) VALUES (?, ?, 'q')`, pageID, srcID)

	mustExec(t, d, `DELETE FROM pages WHERE id = ?`, pageID)
	var count int
	d.sql.QueryRow(`SELECT COUNT(*) FROM evidence WHERE page_id = ?`, pageID).Scan(&count)
	if count != 0 {
		t.Errorf("evidence not cascade-deleted on page delete: got %d rows, want 0", count)
	}
}

func TestEvidenceCascadesOnSourceDelete(t *testing.T) {
	d := mustOpen(t)
	srcID := mustInsert(t, d, `INSERT INTO sources (uri, content_hash) VALUES ('u2', 'h') RETURNING id`)
	pageID := mustInsert(t, d, `INSERT INTO pages (title, body, content_hash) VALUES ('P2', 'b', 'h') RETURNING id`)
	mustExec(t, d, `INSERT INTO evidence (page_id, source_id, quote) VALUES (?, ?, 'q')`, pageID, srcID)

	mustExec(t, d, `DELETE FROM sources WHERE id = ?`, srcID)
	var count int
	d.sql.QueryRow(`SELECT COUNT(*) FROM evidence WHERE source_id = ?`, srcID).Scan(&count)
	if count != 0 {
		t.Errorf("evidence not cascade-deleted on source delete: got %d rows, want 0", count)
	}
}

func TestEvidenceFTSTriggers(t *testing.T) {
	d := mustOpen(t)
	srcID := mustInsert(t, d, `INSERT INTO sources (uri, content_hash) VALUES ('u', 'h') RETURNING id`)
	pageID := mustInsert(t, d, `INSERT INTO pages (title, body, content_hash) VALUES ('P', 'b', 'h') RETURNING id`)
	evID := mustInsert(t, d, `INSERT INTO evidence (page_id, source_id, quote) VALUES (?, ?, 'kafka consumer group') RETURNING id`, pageID, srcID)

	// Insert trigger fired?
	var rowid int64
	if err := d.sql.QueryRow(`SELECT rowid FROM evidence_fts WHERE evidence_fts MATCH 'kafka'`).Scan(&rowid); err != nil {
		t.Fatalf("FTS match after insert: %v", err)
	}
	if rowid != evID {
		t.Errorf("rowid = %d, want %d", rowid, evID)
	}

	// Delete trigger fired?
	mustExec(t, d, `DELETE FROM evidence WHERE id = ?`, evID)
	var count int
	d.sql.QueryRow(`SELECT COUNT(*) FROM evidence_fts WHERE evidence_fts MATCH 'kafka'`).Scan(&count)
	if count != 0 {
		t.Errorf("FTS row not removed after delete: got %d", count)
	}
}

func TestSavedAnswersFTSTrigger(t *testing.T) {
	d := mustOpen(t)
	id := mustInsert(t, d, `INSERT INTO saved_answers (question, answer, file_path) VALUES ('what is X', 'X is Y', 'p.md') RETURNING id`)
	var got int64
	if err := d.sql.QueryRow(`SELECT rowid FROM saved_answers_fts WHERE saved_answers_fts MATCH 'what'`).Scan(&got); err != nil {
		t.Fatalf("FTS match: %v", err)
	}
	if got != id {
		t.Errorf("rowid = %d, want %d", got, id)
	}
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
