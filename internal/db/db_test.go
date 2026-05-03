package db

import (
	"path/filepath"
	"testing"
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

func mustOpenForCascadeTest(t *testing.T) *DB {
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
	d := mustOpenForCascadeTest(t)
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
	d := mustOpenForCascadeTest(t)
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
	d := mustOpenForCascadeTest(t)
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
	d := mustOpenForCascadeTest(t)
	id := mustInsert(t, d, `INSERT INTO saved_answers (question, answer, file_path) VALUES ('what is X', 'X is Y', 'p.md') RETURNING id`)
	var got int64
	if err := d.sql.QueryRow(`SELECT rowid FROM saved_answers_fts WHERE saved_answers_fts MATCH 'what'`).Scan(&got); err != nil {
		t.Fatalf("FTS match: %v", err)
	}
	if got != id {
		t.Errorf("rowid = %d, want %d", got, id)
	}
}
