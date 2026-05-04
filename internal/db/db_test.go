package db

import (
	"fmt"
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
	if version != 6 {
		t.Errorf("user_version = %d, want 6", version)
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
	if version != 6 {
		t.Errorf("user_version after re-open = %d, want 6", version)
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
	if version != 6 {
		t.Errorf("user_version after upgrade = %d, want 6", version)
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
	if version != 6 {
		t.Errorf("user_version = %d, want 6", version)
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
	if v != 6 {
		t.Errorf("user_version after upgrade = %d, want 6", v)
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
	if v != 6 {
		t.Errorf("user_version after v0->v2 upgrade = %d, want 6", v)
	}
}

func TestOpenAtFreshV3(t *testing.T) {
	d := mustOpen(t)
	var name string
	if err := d.sql.QueryRow(`SELECT name FROM sqlite_master WHERE name = 'chunks'`).Scan(&name); err != nil {
		t.Errorf("chunks table missing: %v", err)
	}
	var version int
	d.sql.QueryRow(`PRAGMA user_version`).Scan(&version)
	if version != 6 {
		t.Errorf("user_version = %d, want 6", version)
	}
}

func TestOpenUpgradesV2ToV3(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wiki.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	d.sql.Exec(`DROP TABLE chunks`)
	d.sql.Exec(`PRAGMA user_version = 2`)
	d.Close()

	d2, err := Open(path)
	if err != nil {
		t.Fatalf("re-open: %v", err)
	}
	defer d2.Close()
	var v int
	d2.sql.QueryRow(`PRAGMA user_version`).Scan(&v)
	if v != 6 {
		t.Errorf("user_version after upgrade = %d, want 6", v)
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

func TestSourceFileCRUD(t *testing.T) {
	d := mustOpen(t)
	srcID, _ := d.UpsertSource("file:///dir", "wholehash")

	id1, err := d.UpsertSourceFile(SourceFile{SourceID: srcID, RelativePath: "a.go", ContentHash: "h1", ByteSize: 10, LineCount: 2})
	if err != nil || id1 == 0 {
		t.Fatalf("UpsertSourceFile: id=%d err=%v", id1, err)
	}
	id2, _ := d.UpsertSourceFile(SourceFile{SourceID: srcID, RelativePath: "b.go", ContentHash: "h2", ByteSize: 20, LineCount: 4})

	// Re-upsert same path → same id, updated hash.
	id1b, _ := d.UpsertSourceFile(SourceFile{SourceID: srcID, RelativePath: "a.go", ContentHash: "h1prime", ByteSize: 11, LineCount: 2})
	if id1b != id1 {
		t.Errorf("upsert returned new id %d, want stable %d", id1b, id1)
	}

	files, err := d.GetSourceFiles(srcID)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("got %d source_files, want 2", len(files))
	}
	byPath := map[string]SourceFile{}
	for _, f := range files {
		byPath[f.RelativePath] = f
	}
	if byPath["a.go"].ContentHash != "h1prime" {
		t.Errorf("a.go hash = %q, want h1prime", byPath["a.go"].ContentHash)
	}

	// Delete one, cascade evidence.
	d.UpsertPage(PageRecord{Title: "P", Path: "p.md", Body: "b", ContentHash: "h", SourceIDs: []int64{srcID}})
	page, _ := d.GetPage("P")
	d.InsertEvidence(page.ID, srcID, []Evidence{{Quote: "q", SourceFileID: &id2}})

	if err := d.DeleteSourceFile(id2); err != nil {
		t.Fatal(err)
	}
	got, _ := d.GetEvidenceForPage(page.ID)
	if len(got) != 0 {
		t.Errorf("evidence not cascade-deleted, got %d rows", len(got))
	}
}

func TestStatsIncludesSourceFiles(t *testing.T) {
	d := mustOpen(t)
	s1, _ := d.UpsertSource("file:///big", "h")
	s2, _ := d.UpsertSource("file:///small", "h")
	for i := 0; i < 5; i++ {
		d.UpsertSourceFile(SourceFile{SourceID: s1, RelativePath: fmt.Sprintf("f%d", i), ContentHash: "h", ByteSize: 1, LineCount: 1})
	}
	d.UpsertSourceFile(SourceFile{SourceID: s2, RelativePath: "f0", ContentHash: "h", ByteSize: 1, LineCount: 1})

	stats, err := d.GetStats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.TotalSourceFiles != 6 {
		t.Errorf("TotalSourceFiles = %d, want 6", stats.TotalSourceFiles)
	}
	if len(stats.LargestSources) == 0 {
		t.Fatal("LargestSources empty")
	}
	if stats.LargestSources[0].URI != "file:///big" || stats.LargestSources[0].FileCount != 5 {
		t.Errorf("largest = %+v, want file:///big/5", stats.LargestSources[0])
	}
}

func TestInsertEvidenceWithSourceFileID(t *testing.T) {
	d := mustOpen(t)
	srcID, _ := d.UpsertSource("u", "h")
	sfID, _ := d.UpsertSourceFile(SourceFile{SourceID: srcID, RelativePath: "x.md", ContentHash: "h", ByteSize: 1, LineCount: 1})
	d.UpsertPage(PageRecord{Title: "P", Path: "p", Body: "b", ContentHash: "h", SourceIDs: []int64{srcID}})
	page, _ := d.GetPage("P")
	if err := d.InsertEvidence(page.ID, srcID, []Evidence{{Quote: "q", SourceFileID: &sfID}}); err != nil {
		t.Fatal(err)
	}
	got, _ := d.GetEvidenceForPage(page.ID)
	if len(got) != 1 || got[0].SourceFileID == nil || *got[0].SourceFileID != sfID {
		t.Errorf("evidence SourceFileID not round-tripped: %+v", got)
	}
}

func TestMigrate_FromFresh_LandsAtV4(t *testing.T) {
	d := mustOpen(t)
	var version int
	if err := d.sql.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("user_version: %v", err)
	}
	if version != 6 {
		t.Errorf("user_version = %d, want 6", version)
	}
	var name string
	if err := d.sql.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='page_update_log'`).Scan(&name); err != nil {
		t.Errorf("page_update_log table missing: %v", err)
	}
}

func TestMigrate_FromV3_AddsPageUpdateLog(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wiki.db")
	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := d.sql.Exec(`PRAGMA user_version = 3`); err != nil {
		t.Fatalf("force v3: %v", err)
	}
	// Drop page_update_log if it exists so we test the migration path.
	d.sql.Exec(`DROP TABLE page_update_log`)
	d.Close()

	d2, err := Open(path)
	if err != nil {
		t.Fatalf("re-open: %v", err)
	}
	defer d2.Close()
	var v int
	if err := d2.sql.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		t.Fatalf("user_version: %v", err)
	}
	if v != 6 {
		t.Errorf("user_version after upgrade = %d, want 6", v)
	}
	expectedCols := map[string]bool{
		"id": false, "page_id": false, "source_id": false,
		"prior_content_hash": false, "new_content_hash": false,
		"outcome": false, "reason": false,
		"evidence_added": false, "evidence_removed": false,
		"created_at": false,
	}
	rows, err := d2.sql.Query(`PRAGMA table_info(page_update_log)`)
	if err != nil {
		t.Fatalf("table_info: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		if _, ok := expectedCols[name]; ok {
			expectedCols[name] = true
		}
	}
	for col, found := range expectedCols {
		if !found {
			t.Errorf("page_update_log missing column %q", col)
		}
	}
}

func TestMigrate_Idempotent_RerunningOnV4_IsNoop(t *testing.T) {
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
	var v int
	if err := d2.sql.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		t.Fatalf("user_version: %v", err)
	}
	if v != 6 {
		t.Errorf("user_version = %d, want 6", v)
	}
	var name string
	if err := d2.sql.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='page_update_log'`).Scan(&name); err != nil {
		t.Errorf("page_update_log missing on idempotent re-open: %v", err)
	}
	// No duplicate indexes — counts must be exactly 1 each.
	for _, idx := range []string{"idx_page_update_log_page", "idx_page_update_log_source"} {
		var n int
		if err := d2.sql.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?`, idx).Scan(&n); err != nil {
			t.Fatalf("count index %q: %v", idx, err)
		}
		if n != 1 {
			t.Errorf("index %q count = %d, want 1", idx, n)
		}
	}
}

func TestMigrate_DoesNotAlterPagesEvidenceSourcesSourceFilesChunks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wiki.db")
	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	preserved := []string{"pages", "evidence", "sources", "source_files", "chunks"}
	before := map[string]string{}
	for _, tbl := range preserved {
		var sqlText string
		if err := d.sql.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, tbl).Scan(&sqlText); err != nil {
			t.Fatalf("get sqlite_master.sql for %q: %v", tbl, err)
		}
		before[tbl] = sqlText
	}
	// Force back to v3 (page_update_log doesn't exist yet at this state).
	d.sql.Exec(`DROP TABLE page_update_log`)
	d.sql.Exec(`PRAGMA user_version = 3`)
	d.Close()

	d2, err := Open(path)
	if err != nil {
		t.Fatalf("re-open: %v", err)
	}
	defer d2.Close()
	for _, tbl := range preserved {
		var after string
		if err := d2.sql.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, tbl).Scan(&after); err != nil {
			t.Fatalf("get sqlite_master.sql for %q post-upgrade: %v", tbl, err)
		}
		if after != before[tbl] {
			t.Errorf("table %q schema changed across v3->v4 upgrade\nbefore: %s\nafter:  %s", tbl, before[tbl], after)
		}
	}
}

func TestMigrate_PageUpdateLogIndexesExist(t *testing.T) {
	d := mustOpen(t)
	for _, idx := range []string{"idx_page_update_log_page", "idx_page_update_log_source"} {
		var name string
		if err := d.sql.QueryRow(`SELECT name FROM sqlite_master WHERE type='index' AND name=?`, idx).Scan(&name); err != nil {
			t.Errorf("index %q missing: %v", idx, err)
		}
	}
}

// --- Sub-project 7 (v0.7) — Phase D / Task 7 — schema_hash column ---

// TestMigrate_FromFresh_LandsAtV5 — open a fresh DB; PRAGMA user_version
// returns 5; pages.schema_hash exists, TEXT, NOT NULL, default ''.
func TestMigrate_FromFresh_LandsAtV5(t *testing.T) {
	d := mustOpen(t)
	var version int
	if err := d.sql.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("user_version: %v", err)
	}
	if version != 6 {
		t.Errorf("user_version = %d, want 6", version)
	}
	rows, err := d.sql.Query(`PRAGMA table_info(pages)`)
	if err != nil {
		t.Fatalf("PRAGMA table_info(pages): %v", err)
	}
	defer rows.Close()
	var found bool
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		if name == "schema_hash" {
			found = true
			if !strings.EqualFold(ctype, "TEXT") {
				t.Errorf("schema_hash type = %q, want TEXT", ctype)
			}
			if notnull != 1 {
				t.Errorf("schema_hash notnull = %d, want 1", notnull)
			}
			// SQLite stores DEFAULT '' as the literal "''" in the dflt
			// column; tolerate both quoting styles.
			s, ok := dflt.(string)
			if !ok || (s != "''" && s != "") {
				t.Errorf("schema_hash default = %v (%T), want '' literal", dflt, dflt)
			}
		}
	}
	if !found {
		t.Error("pages.schema_hash column missing on fresh DB")
	}
}

// TestMigrate_FromV4_AddsSchemaHash — force user_version=4, build the
// v4 schema by hand, insert a v4-shaped pages row, reopen via db.Open,
// assert user_version=5, the existing row's schema_hash is '', and a
// new insert can set schema_hash to a non-empty value.
func TestMigrate_FromV4_AddsSchemaHash(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wiki.db")
	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// Drop the v5 column by recreating pages without schema_hash, then
	// force user_version back to 4. We use sqlite's "create-rename-copy"
	// dance because SQLite has no DROP COLUMN before 3.35 and modernc's
	// shipped version may differ across builds.
	stmts := []string{
		`DROP TRIGGER IF EXISTS pages_ai`,
		`DROP TRIGGER IF EXISTS pages_au`,
		`DROP TRIGGER IF EXISTS pages_ad`,
		`DROP TABLE IF EXISTS pages_fts`,
		`ALTER TABLE pages RENAME TO pages_old`,
		`CREATE TABLE pages (
			id INTEGER PRIMARY KEY,
			title TEXT UNIQUE,
			path TEXT,
			body TEXT,
			content_hash TEXT,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			source_ids TEXT DEFAULT '[]'
		)`,
		`INSERT INTO pages (id, title, path, body, content_hash, updated_at, source_ids)
			SELECT id, title, path, body, content_hash, updated_at, source_ids FROM pages_old`,
		`DROP TABLE pages_old`,
		`PRAGMA user_version = 4`,
	}
	for _, s := range stmts {
		if _, err := d.sql.Exec(s); err != nil {
			t.Fatalf("force v4: %q: %v", s[:min(40, len(s))], err)
		}
	}
	// Insert a v4-shape row directly (no schema_hash column to fill).
	if _, err := d.sql.Exec(`INSERT INTO pages (title, path, body, content_hash, source_ids)
		VALUES (?, ?, ?, ?, ?)`,
		"Pre-V5 Page", "wiki/Pre-V5 Page.md", "body", "h1", "[]"); err != nil {
		t.Fatalf("insert v4 row: %v", err)
	}
	d.Close()

	// Reopen — v5 migration should fire.
	d2, err := Open(path)
	if err != nil {
		t.Fatalf("re-Open: %v", err)
	}
	defer d2.Close()
	var v int
	d2.sql.QueryRow(`PRAGMA user_version`).Scan(&v)
	if v != 6 {
		t.Errorf("user_version after upgrade = %d, want 6", v)
	}
	var got string
	if err := d2.sql.QueryRow(`SELECT schema_hash FROM pages WHERE title = ?`, "Pre-V5 Page").Scan(&got); err != nil {
		t.Fatalf("SELECT schema_hash: %v", err)
	}
	if got != "" {
		t.Errorf("pre-v5 row schema_hash = %q, want empty", got)
	}
	// New insert can set non-empty schema_hash.
	if _, err := d2.sql.Exec(`INSERT INTO pages (title, path, body, content_hash, source_ids, schema_hash)
		VALUES (?, ?, ?, ?, ?, ?)`,
		"Post-V5 Page", "wiki/Post-V5 Page.md", "body", "h2", "[]", "abc123"); err != nil {
		t.Fatalf("insert v5 row: %v", err)
	}
	var got2 string
	if err := d2.sql.QueryRow(`SELECT schema_hash FROM pages WHERE title = ?`, "Post-V5 Page").Scan(&got2); err != nil {
		t.Fatalf("SELECT schema_hash post: %v", err)
	}
	if got2 != "abc123" {
		t.Errorf("post-v5 schema_hash = %q, want abc123", got2)
	}
}

// TestMigrate_Idempotent_RerunningOnV5_IsNoop — open a v5 DB twice;
// no errors, the schema_hash column does not duplicate.
func TestMigrate_Idempotent_RerunningOnV5_IsNoop(t *testing.T) {
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
	var v int
	d2.sql.QueryRow(`PRAGMA user_version`).Scan(&v)
	if v != 6 {
		t.Errorf("user_version = %d, want 6", v)
	}
	rows, err := d2.sql.Query(`PRAGMA table_info(pages)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		if name == "schema_hash" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("schema_hash column appeared %d times, want 1", count)
	}
}

// TestMigrate_PreV5RowsHaveEmptySchemaHash — pre-seed a v4 DB with three
// pages, migrate, assert all three have schema_hash = ''.
func TestMigrate_PreV5RowsHaveEmptySchemaHash(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wiki.db")
	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	stmts := []string{
		`DROP TRIGGER IF EXISTS pages_ai`,
		`DROP TRIGGER IF EXISTS pages_au`,
		`DROP TRIGGER IF EXISTS pages_ad`,
		`DROP TABLE IF EXISTS pages_fts`,
		`ALTER TABLE pages RENAME TO pages_old`,
		`CREATE TABLE pages (
			id INTEGER PRIMARY KEY,
			title TEXT UNIQUE,
			path TEXT,
			body TEXT,
			content_hash TEXT,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			source_ids TEXT DEFAULT '[]'
		)`,
		`INSERT INTO pages (id, title, path, body, content_hash, updated_at, source_ids)
			SELECT id, title, path, body, content_hash, updated_at, source_ids FROM pages_old`,
		`DROP TABLE pages_old`,
		`PRAGMA user_version = 4`,
	}
	for _, s := range stmts {
		if _, err := d.sql.Exec(s); err != nil {
			t.Fatalf("force v4: %v", err)
		}
	}
	for _, title := range []string{"P1", "P2", "P3"} {
		if _, err := d.sql.Exec(`INSERT INTO pages (title, path, body, content_hash, source_ids)
			VALUES (?, ?, ?, ?, ?)`, title, "wiki/"+title+".md", "body", "h", "[]"); err != nil {
			t.Fatalf("seed %s: %v", title, err)
		}
	}
	d.Close()

	d2, err := Open(path)
	if err != nil {
		t.Fatalf("re-Open: %v", err)
	}
	defer d2.Close()
	var n int
	if err := d2.sql.QueryRow(`SELECT COUNT(*) FROM pages WHERE schema_hash = ''`).Scan(&n); err != nil {
		t.Fatalf("count empty schema_hash: %v", err)
	}
	if n != 3 {
		t.Errorf("rows with empty schema_hash = %d, want 3", n)
	}
}

// TestMigrate_DoesNotAlterEvidenceSourcesSourceFilesChunksPageUpdateLog —
// captures sqlite_master rows for the four other tables on a v4 DB,
// reopens at v5, asserts byte-identical for each unchanged table. The
// fixture forces the DB back to v4 first (via the rename dance), then
// captures `before`, then closes + reopens to trigger the v5 migration,
// and finally compares — so the only delta in scope is the v4->v5 step
// itself (the fixture's rename of `pages` is not in the comparison
// window because we capture after it has already run).
func TestMigrate_DoesNotAlterEvidenceSourcesSourceFilesChunksPageUpdateLog(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wiki.db")
	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// Force back to v4 with the v4-shape pages table (no schema_hash). We
	// disable FK during the rename to avoid SQLite rewriting the FK
	// pointers in dependent tables — those rewrites are noise from the
	// fixture, not from the migration under test.
	stmts := []string{
		`PRAGMA foreign_keys = OFF`,
		`PRAGMA legacy_alter_table = ON`,
		`DROP TRIGGER IF EXISTS pages_ai`,
		`DROP TRIGGER IF EXISTS pages_au`,
		`DROP TRIGGER IF EXISTS pages_ad`,
		`DROP TABLE IF EXISTS pages_fts`,
		`ALTER TABLE pages RENAME TO pages_old`,
		`CREATE TABLE pages (
			id INTEGER PRIMARY KEY,
			title TEXT UNIQUE,
			path TEXT,
			body TEXT,
			content_hash TEXT,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			source_ids TEXT DEFAULT '[]'
		)`,
		`INSERT INTO pages (id, title, path, body, content_hash, updated_at, source_ids)
			SELECT id, title, path, body, content_hash, updated_at, source_ids FROM pages_old`,
		`DROP TABLE pages_old`,
		`PRAGMA user_version = 4`,
	}
	for _, s := range stmts {
		if _, err := d.sql.Exec(s); err != nil {
			t.Fatalf("force v4: %v", err)
		}
	}

	// Capture `before` AFTER the fixture has set the DB back to v4.
	preserved := []string{"evidence", "sources", "source_files", "chunks", "page_update_log"}
	before := map[string]string{}
	for _, tbl := range preserved {
		var sqlText string
		if err := d.sql.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, tbl).Scan(&sqlText); err != nil {
			t.Fatalf("get sqlite_master.sql for %q: %v", tbl, err)
		}
		before[tbl] = sqlText
	}
	d.Close()

	d2, err := Open(path)
	if err != nil {
		t.Fatalf("re-open: %v", err)
	}
	defer d2.Close()
	for _, tbl := range preserved {
		var after string
		if err := d2.sql.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, tbl).Scan(&after); err != nil {
			t.Fatalf("get sqlite_master.sql for %q post-upgrade: %v", tbl, err)
		}
		if after != before[tbl] {
			t.Errorf("table %q schema changed across v4->v5 upgrade\nbefore: %s\nafter:  %s", tbl, before[tbl], after)
		}
	}
}

// --- Sub-project 8 (v0.8) — Phase A / Task 2 — ingest_queue table ---

// TestMigrate_FromFresh_LandsAtV6 — open a fresh DB; PRAGMA user_version
// is 6 and the ingest_queue table exists with the documented columns.
func TestMigrate_FromFresh_LandsAtV6(t *testing.T) {
	d := mustOpen(t)
	var version int
	if err := d.sql.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("user_version: %v", err)
	}
	if version != 6 {
		t.Errorf("user_version = %d, want 6", version)
	}
	expectedCols := map[string]bool{
		"id": false, "source_uri": false, "attempts": false,
		"last_error": false, "status": false,
		"enqueued_at": false, "updated_at": false,
		"next_attempt_at": false,
	}
	rows, err := d.sql.Query(`PRAGMA table_info(ingest_queue)`)
	if err != nil {
		t.Fatalf("table_info: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		if _, ok := expectedCols[name]; ok {
			expectedCols[name] = true
		}
	}
	for col, found := range expectedCols {
		if !found {
			t.Errorf("ingest_queue missing column %q", col)
		}
	}
}

// TestMigrate_FromV5_AddsIngestQueue — force user_version=5 with no
// ingest_queue table; reopen via db.Open; assert user_version=6 and
// the table exists.
func TestMigrate_FromV5_AddsIngestQueue(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wiki.db")
	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	d.sql.Exec(`DROP TABLE IF EXISTS ingest_queue`)
	if _, err := d.sql.Exec(`PRAGMA user_version = 5`); err != nil {
		t.Fatalf("force v5: %v", err)
	}
	d.Close()

	d2, err := Open(path)
	if err != nil {
		t.Fatalf("re-Open: %v", err)
	}
	defer d2.Close()
	var v int
	d2.sql.QueryRow(`PRAGMA user_version`).Scan(&v)
	if v != 6 {
		t.Errorf("user_version after upgrade = %d, want 6", v)
	}
	var name string
	if err := d2.sql.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='ingest_queue'`).Scan(&name); err != nil {
		t.Errorf("ingest_queue not created on upgrade: %v", err)
	}
}

// TestMigrate_Idempotent_RerunningOnV6_IsNoop — open a v6 DB twice;
// no errors, ingest_queue present once, idx_ingest_queue_status
// present once.
func TestMigrate_Idempotent_RerunningOnV6_IsNoop(t *testing.T) {
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
	var v int
	d2.sql.QueryRow(`PRAGMA user_version`).Scan(&v)
	if v != 6 {
		t.Errorf("user_version = %d, want 6", v)
	}
	var n int
	if err := d2.sql.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='ingest_queue'`).Scan(&n); err != nil {
		t.Fatalf("count ingest_queue: %v", err)
	}
	if n != 1 {
		t.Errorf("ingest_queue table count = %d, want 1", n)
	}
	if err := d2.sql.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_ingest_queue_status'`).Scan(&n); err != nil {
		t.Fatalf("count idx_ingest_queue_status: %v", err)
	}
	if n != 1 {
		t.Errorf("idx_ingest_queue_status count = %d, want 1", n)
	}
}

// TestSQL_ReturnsUnderlyingHandle — internal/queue uses (*DB).SQL() to
// share one connection pool with the rest of llmwiki.
func TestSQL_ReturnsUnderlyingHandle(t *testing.T) {
	d := mustOpen(t)
	if d.SQL() == nil {
		t.Fatal("SQL() returned nil")
	}
	// Round-trip a trivial query through the exposed handle.
	var n int
	if err := d.SQL().QueryRow(`SELECT COUNT(*) FROM ingest_queue`).Scan(&n); err != nil {
		t.Errorf("query via SQL(): %v", err)
	}
}

func TestChunkCRUD(t *testing.T) {
	d := mustOpen(t)
	srcID, _ := d.UpsertSource("u", "h")
	c1 := Chunk{SourceID: srcID, ChunkHash: "h1", FilePaths: []string{"a.go", "b.go"}}
	c2 := Chunk{SourceID: srcID, ChunkHash: "h2", FilePaths: []string{"c.go"}}
	if err := d.InsertChunks([]Chunk{c1, c2}); err != nil {
		t.Fatal(err)
	}

	got, err := d.GetChunksForFile(srcID, "a.go")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ChunkHash != "h1" {
		t.Errorf("GetChunksForFile a.go = %+v", got)
	}

	if err := d.DeleteChunksForSource(srcID); err != nil {
		t.Fatal(err)
	}
	got, _ = d.GetChunksForFile(srcID, "a.go")
	if len(got) != 0 {
		t.Errorf("post-delete = %+v", got)
	}
}
