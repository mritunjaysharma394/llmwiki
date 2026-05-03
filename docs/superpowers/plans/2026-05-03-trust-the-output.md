# Trust the Output — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `llmwiki` ingest hallucination-free and `ask` traceable to verbatim source quotes, so the tool is daily-driveable and trustworthy.

**Architecture:** Add an evidence requirement to the LLM tool schema (every page must include verbatim source quotes), validate quotes server-side as substrings of the source, store evidence in a new SQLite table + FTS5 index + page frontmatter, retrieve both pages and evidence at `ask` time, render answers with TTY-aware streaming + glamour, and auto-archive every answer.

**Tech Stack:** Go 1.26, cobra, anthropic-sdk-go v1.37, modernc.org/sqlite (FTS5), BurntSushi/toml, charmbracelet/glamour (new), fatih/color (new), mattn/go-isatty (already indirect).

**Spec:** [`docs/superpowers/specs/2026-05-03-trust-the-output-design.md`](../specs/2026-05-03-trust-the-output-design.md)

---

## File Structure

| File | Purpose | Action |
|---|---|---|
| `internal/db/db.go` | Add `PRAGMA user_version` migration for new tables | Modify |
| `internal/db/queries.go` | Evidence + saved_answers CRUD, two-table FTS, cascade-delete | Modify |
| `internal/db/db_test.go` | Migration + queries unit tests | Create |
| `internal/wiki/page.go` | Add `Evidence` struct, frontmatter round-trip | Modify |
| `internal/wiki/page_test.go` | Page+evidence round-trip tests | Create |
| `internal/wiki/ops.go` | New tool schema, system prompt, `ValidateAndAttachEvidence` | Modify |
| `internal/wiki/ops_test.go` | Validation pass tests | Create |
| `internal/wiki/answer.go` | Answer formatting + auto-archive helpers | Create |
| `internal/wiki/answer_test.go` | Slug + archive format tests | Create |
| `internal/llm/client.go` | Add streaming method to `Client` interface | Modify |
| `internal/llm/anthropic.go` | Streaming, prompt caching | Modify |
| `internal/llm/ollama.go` | Stub streaming as buffered fallback | Modify |
| `internal/llm/cassette.go` | Cassette record/replay wrapper | Create |
| `internal/llm/cassette_test.go` | Cassette unit tests | Create |
| `internal/llm/testdata/cassettes/` | Cassette JSON fixtures | Create dir |
| `cmd/ingest.go` | 16KB chunks, semaphore, progress, no cap, idempotent re-ingest | Modify |
| `cmd/ingest_test.go` | Chunking unit tests | Create |
| `cmd/ask.go` | Two-table retrieval, streaming, glamour, auto-archive, flags | Modify |
| `cmd/ask_test.go` | Slug + integration tests with cassette | Create |
| `cmd/init.go` | Anthropic+Haiku defaults, key validation, helpful error | Modify |
| `cmd/root.go` | `--provider`/`--model` flags, key validation in PreRunE, color errors | Modify |
| `cmd/status.go` | New fields: evidence_quotes, legacy_pages, saved_answers | Modify |
| `.github/workflows/test.yml` | CI: `go test ./...` on push | Create |
| `go.mod` / `go.sum` | New deps: glamour, fatih/color | Modify |

**Total:** 22 tasks across 5 phases. Each task ends with a commit.

---

## Phase A — Foundations (Schema, types, queries)

### Task 1: SQLite schema migration with `PRAGMA user_version`

**Files:**
- Modify: `internal/db/db.go`
- Create: `internal/db/db_test.go`

- [ ] **Step 1: Write failing migration test**

Create `internal/db/db_test.go`:

```go
package db

import (
	"os"
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
	for _, t := range []string{"evidence_fts", "evidence", "saved_answers_fts", "saved_answers"} {
		d.sql.Exec(`DROP TABLE ` + t)
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
```

- [ ] **Step 2: Run test and confirm failure**

Run: `go test ./internal/db/ -run TestOpen -v`
Expected: FAIL — tables missing.

- [ ] **Step 3: Implement migration in `db.go`**

Replace the `migrate()` function in `internal/db/db.go`:

```go
func (d *DB) migrate() error {
	// Idempotent legacy schema (predates versioning).
	legacyStmts := []string{
		`CREATE TABLE IF NOT EXISTS sources (
			id INTEGER PRIMARY KEY,
			uri TEXT UNIQUE,
			content_hash TEXT,
			ingested_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS pages (
			id INTEGER PRIMARY KEY,
			title TEXT UNIQUE,
			path TEXT,
			body TEXT,
			content_hash TEXT,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			source_ids TEXT DEFAULT '[]'
		)`,
		`CREATE TABLE IF NOT EXISTS links (
			from_page TEXT,
			to_page TEXT,
			link_type TEXT
		)`,
		`CREATE VIRTUAL TABLE IF NOT EXISTS pages_fts USING fts5(title, body, content=pages, content_rowid=id)`,
		`CREATE TRIGGER IF NOT EXISTS pages_ai AFTER INSERT ON pages BEGIN
			INSERT INTO pages_fts(rowid, title, body) VALUES (new.id, new.title, new.body);
		END`,
		`CREATE TRIGGER IF NOT EXISTS pages_au AFTER UPDATE ON pages BEGIN
			INSERT INTO pages_fts(pages_fts, rowid, title, body) VALUES ('delete', old.id, old.title, old.body);
			INSERT INTO pages_fts(rowid, title, body) VALUES (new.id, new.title, new.body);
		END`,
		`CREATE TRIGGER IF NOT EXISTS pages_ad AFTER DELETE ON pages BEGIN
			INSERT INTO pages_fts(pages_fts, rowid, title, body) VALUES ('delete', old.id, old.title, old.body);
		END`,
	}
	for _, stmt := range legacyStmts {
		if _, err := d.sql.Exec(stmt); err != nil {
			return fmt.Errorf("legacy schema %q: %w", stmt[:min(50, len(stmt))], err)
		}
	}

	var version int
	if err := d.sql.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("read user_version: %w", err)
	}

	if version < 1 {
		v1 := []string{
			`CREATE TABLE IF NOT EXISTS evidence (
				id INTEGER PRIMARY KEY,
				page_id INTEGER NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
				source_id INTEGER NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
				quote TEXT NOT NULL,
				line_start INTEGER,
				line_end INTEGER,
				created_at DATETIME DEFAULT CURRENT_TIMESTAMP
			)`,
			`CREATE INDEX IF NOT EXISTS idx_evidence_page ON evidence(page_id)`,
			`CREATE VIRTUAL TABLE IF NOT EXISTS evidence_fts USING fts5(quote, content=evidence, content_rowid=id)`,
			`CREATE TRIGGER IF NOT EXISTS evidence_ai AFTER INSERT ON evidence BEGIN
				INSERT INTO evidence_fts(rowid, quote) VALUES (new.id, new.quote);
			END`,
			`CREATE TRIGGER IF NOT EXISTS evidence_ad AFTER DELETE ON evidence BEGIN
				INSERT INTO evidence_fts(evidence_fts, rowid, quote) VALUES ('delete', old.id, old.quote);
			END`,
			`CREATE TABLE IF NOT EXISTS saved_answers (
				id INTEGER PRIMARY KEY,
				question TEXT NOT NULL,
				answer TEXT NOT NULL,
				model TEXT,
				cited_page_ids TEXT DEFAULT '[]',
				created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
				file_path TEXT NOT NULL
			)`,
			`CREATE VIRTUAL TABLE IF NOT EXISTS saved_answers_fts USING fts5(question, answer, content=saved_answers, content_rowid=id)`,
			`CREATE TRIGGER IF NOT EXISTS saved_answers_ai AFTER INSERT ON saved_answers BEGIN
				INSERT INTO saved_answers_fts(rowid, question, answer) VALUES (new.id, new.question, new.answer);
			END`,
			`PRAGMA user_version = 1`,
		}
		for _, stmt := range v1 {
			if _, err := d.sql.Exec(stmt); err != nil {
				return fmt.Errorf("v1 migration %q: %w", stmt[:min(50, len(stmt))], err)
			}
		}
	}

	if _, err := d.sql.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		return fmt.Errorf("enable foreign_keys: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Run test and confirm pass**

Run: `go test ./internal/db/ -run TestOpen -v`
Expected: PASS — three subtests green.

- [ ] **Step 5: Commit**

```bash
git add internal/db/db.go internal/db/db_test.go
git commit -m "feat(db): add evidence, evidence_fts, saved_answers schema with PRAGMA user_version migration"
```

---

### Task 2: Evidence + saved_answers queries

**Files:**
- Modify: `internal/db/queries.go`
- Modify: `internal/db/db_test.go`

- [ ] **Step 1: Write failing query tests**

Append to `internal/db/db_test.go`:

```go
import (
	"strings"
	"time"
)

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
```

- [ ] **Step 2: Run tests, confirm failure**

Run: `go test ./internal/db/ -v`
Expected: FAIL — `Evidence`, `InsertEvidence`, `GetEvidenceForPage`, `SearchEvidence`, `DeleteEvidenceForSource`, `SavedAnswer`, `InsertSavedAnswer`, `SearchSavedAnswers` undefined; `Stats.EvidenceQuotes/LegacyPages/SavedAnswers` missing.

- [ ] **Step 3: Add types and queries**

Append to `internal/db/queries.go`:

```go
type Evidence struct {
	ID        int64
	PageID    int64
	SourceID  int64
	Quote     string
	LineStart int
	LineEnd   int
	CreatedAt time.Time
}

type EvidenceHit struct {
	Evidence
	PageTitle string
}

type SavedAnswer struct {
	ID           int64
	Question     string
	Answer       string
	Model        string
	CitedPageIDs []int64
	CreatedAt    time.Time
	FilePath     string
}

func (d *DB) InsertEvidence(pageID, sourceID int64, items []Evidence) error {
	if len(items) == 0 {
		return nil
	}
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT INTO evidence (page_id, source_id, quote, line_start, line_end) VALUES (?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, e := range items {
		var ls, le interface{}
		if e.LineStart > 0 {
			ls = e.LineStart
		}
		if e.LineEnd > 0 {
			le = e.LineEnd
		}
		if _, err := stmt.Exec(pageID, sourceID, e.Quote, ls, le); err != nil {
			return fmt.Errorf("insert evidence: %w", err)
		}
	}
	return tx.Commit()
}

func (d *DB) GetEvidenceForPage(pageID int64) ([]Evidence, error) {
	rows, err := d.sql.Query(`SELECT id, page_id, source_id, quote, COALESCE(line_start, 0), COALESCE(line_end, 0), created_at FROM evidence WHERE page_id = ? ORDER BY id`, pageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Evidence
	for rows.Next() {
		var e Evidence
		var created string
		if err := rows.Scan(&e.ID, &e.PageID, &e.SourceID, &e.Quote, &e.LineStart, &e.LineEnd, &created); err != nil {
			return nil, err
		}
		e.CreatedAt, _ = time.Parse(time.RFC3339, created)
		out = append(out, e)
	}
	return out, rows.Err()
}

func (d *DB) SearchEvidence(query string, limit int) ([]EvidenceHit, error) {
	rows, err := d.sql.Query(
		`SELECT e.id, e.page_id, e.source_id, e.quote, COALESCE(e.line_start, 0), COALESCE(e.line_end, 0), e.created_at, p.title
		FROM evidence e
		JOIN pages p ON p.id = e.page_id
		WHERE e.id IN (SELECT rowid FROM evidence_fts WHERE evidence_fts MATCH ?)
		LIMIT ?`,
		ftsQuery(query), limit,
	)
	if err != nil {
		return nil, fmt.Errorf("search evidence: %w", err)
	}
	defer rows.Close()
	var out []EvidenceHit
	for rows.Next() {
		var h EvidenceHit
		var created string
		if err := rows.Scan(&h.ID, &h.PageID, &h.SourceID, &h.Quote, &h.LineStart, &h.LineEnd, &created, &h.PageTitle); err != nil {
			return nil, err
		}
		h.CreatedAt, _ = time.Parse(time.RFC3339, created)
		out = append(out, h)
	}
	return out, rows.Err()
}

func (d *DB) DeleteEvidenceForSource(sourceID int64) error {
	_, err := d.sql.Exec(`DELETE FROM evidence WHERE source_id = ?`, sourceID)
	return err
}

func (d *DB) InsertSavedAnswer(a SavedAnswer) (int64, error) {
	ids, _ := json.Marshal(a.CitedPageIDs)
	res, err := d.sql.Exec(
		`INSERT INTO saved_answers (question, answer, model, cited_page_ids, created_at, file_path) VALUES (?, ?, ?, ?, ?, ?)`,
		a.Question, a.Answer, a.Model, string(ids), a.CreatedAt.UTC().Format(time.RFC3339), a.FilePath,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (d *DB) SearchSavedAnswers(query string, limit int) ([]SavedAnswer, error) {
	rows, err := d.sql.Query(
		`SELECT s.id, s.question, s.answer, s.model, s.cited_page_ids, s.created_at, s.file_path
		FROM saved_answers s
		WHERE s.id IN (SELECT rowid FROM saved_answers_fts WHERE saved_answers_fts MATCH ?)
		ORDER BY s.created_at DESC
		LIMIT ?`,
		ftsQuery(query), limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SavedAnswer
	for rows.Next() {
		var a SavedAnswer
		var created, ids string
		if err := rows.Scan(&a.ID, &a.Question, &a.Answer, &a.Model, &ids, &created, &a.FilePath); err != nil {
			return nil, err
		}
		a.CreatedAt, _ = time.Parse(time.RFC3339, created)
		json.Unmarshal([]byte(ids), &a.CitedPageIDs)
		out = append(out, a)
	}
	return out, rows.Err()
}

func (d *DB) PagesWithoutEvidence() ([]PageRecord, error) {
	rows, err := d.sql.Query(`SELECT p.id, p.title, p.path, p.body, p.content_hash, p.updated_at, p.source_ids
		FROM pages p
		LEFT JOIN evidence e ON e.page_id = p.id
		WHERE e.id IS NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPages(rows)
}
```

Modify the `Stats` struct and `GetStats` function:

```go
type Stats struct {
	TotalPages     int
	TotalSources   int
	StalePages     int
	EvidenceQuotes int
	LegacyPages    int
	SavedAnswers   int
	LastIngest     time.Time
}

func (d *DB) GetStats() (Stats, error) {
	var s Stats
	d.sql.QueryRow(`SELECT COUNT(*) FROM pages`).Scan(&s.TotalPages)
	d.sql.QueryRow(`SELECT COUNT(*) FROM sources`).Scan(&s.TotalSources)
	d.sql.QueryRow(`SELECT COUNT(*) FROM evidence`).Scan(&s.EvidenceQuotes)
	d.sql.QueryRow(`SELECT COUNT(*) FROM pages p LEFT JOIN evidence e ON e.page_id = p.id WHERE e.id IS NULL`).Scan(&s.LegacyPages)
	d.sql.QueryRow(`SELECT COUNT(*) FROM saved_answers`).Scan(&s.SavedAnswers)
	var lastIngestStr string
	d.sql.QueryRow(`SELECT MAX(ingested_at) FROM sources`).Scan(&lastIngestStr)
	s.LastIngest, _ = time.Parse(time.RFC3339, lastIngestStr)
	return s, nil
}
```

- [ ] **Step 4: Run tests, confirm pass**

Run: `go test ./internal/db/ -v`
Expected: PASS — all subtests green.

- [ ] **Step 5: Commit**

```bash
git add internal/db/queries.go internal/db/db_test.go
git commit -m "feat(db): evidence + saved_answers queries, stats include legacy_pages"
```

---

### Task 3: `Page` struct gets `Evidence`, frontmatter round-trip

**Files:**
- Modify: `internal/wiki/page.go`
- Create: `internal/wiki/page_test.go`

- [ ] **Step 1: Write failing round-trip test**

Create `internal/wiki/page_test.go`:

```go
package wiki

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWriteReadPageWithEvidence(t *testing.T) {
	dir := t.TempDir()
	original := Page{
		Title:       "Test Page",
		Body:        "# Heading\n\nSome body text.\n",
		Links:       []Link{{To: "Other Page", Type: "supports"}},
		SourceIDs:   []int64{1, 2},
		ContentHash: "abc",
		UpdatedAt:   time.Date(2026, 5, 3, 10, 0, 0, 0, time.UTC),
		Evidence: []Evidence{
			{Quote: "first verbatim quote", LineStart: 3, LineEnd: 3},
			{Quote: "second one\nspans two lines", LineStart: 7, LineEnd: 8},
		},
	}
	if err := WritePage(original, dir); err != nil {
		t.Fatalf("WritePage: %v", err)
	}
	read, err := ReadPage(filepath.Join(dir, "Test Page.md"))
	if err != nil {
		t.Fatalf("ReadPage: %v", err)
	}
	if read.Title != original.Title {
		t.Errorf("title: got %q want %q", read.Title, original.Title)
	}
	if read.Body != original.Body {
		t.Errorf("body mismatch:\ngot:  %q\nwant: %q", read.Body, original.Body)
	}
	if len(read.Evidence) != 2 {
		t.Fatalf("evidence count: got %d want 2", len(read.Evidence))
	}
	if read.Evidence[0].Quote != "first verbatim quote" {
		t.Errorf("ev[0].Quote = %q", read.Evidence[0].Quote)
	}
	if read.Evidence[1].LineStart != 7 || read.Evidence[1].LineEnd != 8 {
		t.Errorf("ev[1] lines: got %d-%d want 7-8", read.Evidence[1].LineStart, read.Evidence[1].LineEnd)
	}
}

func TestParsePageBackwardCompatible(t *testing.T) {
	// Pages written before evidence support should still parse.
	content := `---
title: Old Page
updated_at: 2026-04-01T10:00:00Z
content_hash: deadbeef
source_ids: [1]
---

Body content.
`
	p, err := ParsePage(content)
	if err != nil {
		t.Fatalf("ParsePage: %v", err)
	}
	if p.Title != "Old Page" || len(p.Evidence) != 0 {
		t.Errorf("got %+v", p)
	}
}

func TestPagePathSanitizes(t *testing.T) {
	got := PagePath("/wiki", "Foo / Bar : Baz")
	want := "/wiki/Foo _ Bar _ Baz.md"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

// Reach into Page to confirm Evidence field exists at compile time.
var _ = Page{Evidence: []Evidence{{}}}

// Ensure WritePage doesn't choke on missing optional dirs.
func TestWritePageCreatesDir(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "nested", "wiki")
	if err := WritePage(Page{Title: "X", Body: "b", UpdatedAt: time.Now()}, nested); err != nil {
		t.Fatalf("WritePage: %v", err)
	}
	if _, err := os.Stat(filepath.Join(nested, "X.md")); err != nil {
		t.Errorf("file not created: %v", err)
	}
}
```

- [ ] **Step 2: Run test, confirm failure**

Run: `go test ./internal/wiki/ -run TestWriteReadPageWithEvidence -v`
Expected: FAIL — `Evidence` undefined, `Page.Evidence` field missing.

- [ ] **Step 3: Add Evidence type and update WritePage/ParsePage**

Modify `internal/wiki/page.go`:

```go
package wiki

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Link struct {
	To   string
	Type string
}

type Evidence struct {
	Quote     string
	LineStart int
	LineEnd   int
}

type Page struct {
	Title       string
	Body        string
	Links       []Link
	SourceIDs   []int64
	ContentHash string
	UpdatedAt   time.Time
	Evidence    []Evidence
}

func HashContent(content string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(content)))
}

func PagePath(wikiDir, title string) string {
	safe := strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == ':' || r == '*' || r == '?' || r == '"' || r == '<' || r == '>' || r == '|' {
			return '_'
		}
		return r
	}, title)
	return filepath.Join(wikiDir, safe+".md")
}

func WritePage(p Page, wikiDir string) error {
	path := PagePath(wikiDir, p.Title)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	var sb strings.Builder
	sb.WriteString("---\n")
	sb.WriteString(fmt.Sprintf("title: %s\n", p.Title))
	sb.WriteString(fmt.Sprintf("updated_at: %s\n", p.UpdatedAt.UTC().Format(time.RFC3339)))
	sb.WriteString(fmt.Sprintf("content_hash: %s\n", p.ContentHash))
	if len(p.SourceIDs) > 0 {
		ids := make([]string, len(p.SourceIDs))
		for i, id := range p.SourceIDs {
			ids[i] = strconv.FormatInt(id, 10)
		}
		sb.WriteString(fmt.Sprintf("source_ids: [%s]\n", strings.Join(ids, ", ")))
	} else {
		sb.WriteString("source_ids: []\n")
	}
	if len(p.Links) > 0 {
		sb.WriteString("links:\n")
		for _, l := range p.Links {
			sb.WriteString(fmt.Sprintf("  - to: %s\n    type: %s\n", l.To, l.Type))
		}
	}
	if len(p.Evidence) > 0 {
		sb.WriteString("evidence:\n")
		for _, e := range p.Evidence {
			// Encode quote on a single YAML-safe line by escaping quotes and newlines.
			esc := strings.ReplaceAll(e.Quote, `\`, `\\`)
			esc = strings.ReplaceAll(esc, `"`, `\"`)
			esc = strings.ReplaceAll(esc, "\n", `\n`)
			esc = strings.ReplaceAll(esc, "\r", `\r`)
			sb.WriteString(fmt.Sprintf("  - quote: \"%s\"\n", esc))
			sb.WriteString(fmt.Sprintf("    line_start: %d\n", e.LineStart))
			sb.WriteString(fmt.Sprintf("    line_end: %d\n", e.LineEnd))
		}
	}
	sb.WriteString("---\n\n")
	sb.WriteString(p.Body)
	return os.WriteFile(path, []byte(sb.String()), 0644)
}

func ReadPage(path string) (Page, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Page{}, err
	}
	return ParsePage(string(data))
}

func ParsePage(content string) (Page, error) {
	var p Page
	if !strings.HasPrefix(content, "---\n") {
		p.Body = content
		p.ContentHash = HashContent(content)
		return p, nil
	}
	rest := content[4:]
	end := strings.Index(rest, "\n---\n")
	if end == -1 {
		p.Body = content
		p.ContentHash = HashContent(content)
		return p, nil
	}
	frontmatter := rest[:end]
	p.Body = strings.TrimPrefix(rest[end+5:], "\n")

	var inLinks, inEvidence bool
	var curEv Evidence
	flushEv := func() {
		if curEv.Quote != "" {
			p.Evidence = append(p.Evidence, curEv)
			curEv = Evidence{}
		}
	}
	for _, line := range strings.Split(frontmatter, "\n") {
		switch {
		case strings.HasPrefix(line, "title: "):
			p.Title = strings.TrimSpace(line[7:])
			inLinks, inEvidence = false, false
		case strings.HasPrefix(line, "updated_at: "):
			p.UpdatedAt, _ = time.Parse(time.RFC3339, strings.TrimSpace(line[12:]))
		case strings.HasPrefix(line, "content_hash: "):
			p.ContentHash = strings.TrimSpace(line[14:])
		case strings.HasPrefix(line, "source_ids: "):
			p.SourceIDs = parseIntArray(strings.TrimSpace(line[12:]))
		case strings.HasPrefix(line, "links:"):
			inLinks, inEvidence = true, false
		case strings.HasPrefix(line, "evidence:"):
			flushEv()
			inLinks, inEvidence = false, true
		case inLinks && strings.HasPrefix(line, "  - to: "):
			p.Links = append(p.Links, Link{To: strings.TrimSpace(line[8:])})
		case inLinks && strings.HasPrefix(line, "    type: ") && len(p.Links) > 0:
			p.Links[len(p.Links)-1].Type = strings.TrimSpace(line[10:])
		case inEvidence && strings.HasPrefix(line, "  - quote: "):
			flushEv()
			curEv.Quote = unescapeQuote(strings.TrimSpace(strings.TrimPrefix(line, "  - quote: ")))
		case inEvidence && strings.HasPrefix(line, "    line_start: "):
			curEv.LineStart, _ = strconv.Atoi(strings.TrimSpace(line[16:]))
		case inEvidence && strings.HasPrefix(line, "    line_end: "):
			curEv.LineEnd, _ = strconv.Atoi(strings.TrimSpace(line[14:]))
		}
	}
	flushEv()
	return p, nil
}

func unescapeQuote(s string) string {
	s = strings.TrimPrefix(s, `"`)
	s = strings.TrimSuffix(s, `"`)
	s = strings.ReplaceAll(s, `\n`, "\n")
	s = strings.ReplaceAll(s, `\r`, "\r")
	s = strings.ReplaceAll(s, `\"`, `"`)
	s = strings.ReplaceAll(s, `\\`, `\`)
	return s
}

func parseIntArray(s string) []int64 {
	s = strings.Trim(s, "[]")
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	var ids []int64
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		var id int64
		fmt.Sscanf(p, "%d", &id)
		ids = append(ids, id)
	}
	return ids
}
```

- [ ] **Step 2: Run test, confirm pass**

Run: `go test ./internal/wiki/ -v`
Expected: PASS — round-trip + backward-compat tests green.

- [ ] **Step 3: Commit**

```bash
git add internal/wiki/page.go internal/wiki/page_test.go
git commit -m "feat(wiki): Page.Evidence with markdown frontmatter round-trip"
```

---

## Phase B — Trustworthy Ingest

### Task 4: `ValidateAndAttachEvidence` server-side check

**Files:**
- Create: `internal/wiki/ops_test.go`
- Modify: `internal/wiki/ops.go`

- [ ] **Step 1: Write failing validation test**

Create `internal/wiki/ops_test.go`:

```go
package wiki

import (
	"strings"
	"testing"
)

func TestValidateAndAttachEvidence(t *testing.T) {
	source := `Line one of source.
Line two contains the quick brown fox.
Line three.
Line four mentions kafka consumer group offset.
Line five.`

	pages := []Page{
		{
			Title: "Good page",
			Body:  "About the fox",
			Evidence: []Evidence{
				{Quote: "quick brown fox"},
				{Quote: "this string is NOT in source"},
			},
		},
		{
			Title: "Another good page",
			Body:  "Kafka",
			Evidence: []Evidence{{Quote: "kafka consumer group offset"}},
		},
		{
			Title: "Hallucinated page",
			Body:  "Made up",
			Evidence: []Evidence{{Quote: "absolutely not present anywhere"}},
		},
		{
			Title: "Empty evidence page",
			Body:  "Nope",
		},
	}

	got, dropped := ValidateAndAttachEvidence(pages, source)

	if len(got) != 2 {
		t.Fatalf("got %d valid pages, want 2 (titles: %v)", len(got), pageTitles(got))
	}
	if got[0].Title != "Good page" {
		t.Errorf("got[0].Title = %q", got[0].Title)
	}
	if len(got[0].Evidence) != 1 {
		t.Errorf("good page kept %d evidence, want 1", len(got[0].Evidence))
	}
	if got[0].Evidence[0].LineStart != 2 || got[0].Evidence[0].LineEnd != 2 {
		t.Errorf("good page line range = %d-%d, want 2-2", got[0].Evidence[0].LineStart, got[0].Evidence[0].LineEnd)
	}
	if got[1].Evidence[0].LineStart != 4 {
		t.Errorf("kafka quote line_start = %d, want 4", got[1].Evidence[0].LineStart)
	}
	if dropped != 2 {
		t.Errorf("dropped = %d, want 2", dropped)
	}
}

func TestValidateAndAttachEvidenceMultilineQuote(t *testing.T) {
	source := "alpha\nbeta\ngamma\ndelta\n"
	pages := []Page{{
		Title:    "T",
		Body:     "b",
		Evidence: []Evidence{{Quote: "beta\ngamma"}},
	}}
	got, _ := ValidateAndAttachEvidence(pages, source)
	if len(got) != 1 {
		t.Fatal("page dropped")
	}
	if got[0].Evidence[0].LineStart != 2 || got[0].Evidence[0].LineEnd != 3 {
		t.Errorf("multiline lines = %d-%d, want 2-3", got[0].Evidence[0].LineStart, got[0].Evidence[0].LineEnd)
	}
}

func TestValidateAndAttachEvidenceUnicode(t *testing.T) {
	source := "héllo wörld\nsecond line\n"
	pages := []Page{{
		Title:    "T",
		Body:     "b",
		Evidence: []Evidence{{Quote: "héllo wörld"}},
	}}
	got, _ := ValidateAndAttachEvidence(pages, source)
	if len(got) != 1 {
		t.Fatalf("unicode quote dropped (page count=%d)", len(got))
	}
}

func pageTitles(ps []Page) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Title
	}
	return out
}

// helper used to suppress unused-import in case strings import is dropped above
var _ = strings.Index
```

- [ ] **Step 2: Run test, confirm failure**

Run: `go test ./internal/wiki/ -run TestValidateAndAttachEvidence -v`
Expected: FAIL — `ValidateAndAttachEvidence` undefined.

- [ ] **Step 3: Implement `ValidateAndAttachEvidence`**

Append to `internal/wiki/ops.go`:

```go
// ValidateAndAttachEvidence drops evidence quotes that are not verbatim
// substrings of source, drops pages that have zero valid evidence after that,
// and computes line_start/line_end for surviving quotes (1-indexed).
//
// Returns (kept pages, count of pages dropped).
func ValidateAndAttachEvidence(pages []Page, source string) ([]Page, int) {
	var kept []Page
	dropped := 0
	for _, p := range pages {
		var valid []Evidence
		for _, e := range p.Evidence {
			if e.Quote == "" {
				continue
			}
			idx := strings.Index(source, e.Quote)
			if idx < 0 {
				fmt.Fprintf(os.Stderr, "  WARN dropped quote in page %q — not present in source\n", p.Title)
				continue
			}
			start, end := lineRange(source, idx, len(e.Quote))
			e.LineStart = start
			e.LineEnd = end
			valid = append(valid, e)
		}
		if len(valid) == 0 {
			fmt.Fprintf(os.Stderr, "  WARN dropped page %q — no verifiable evidence\n", p.Title)
			dropped++
			continue
		}
		p.Evidence = valid
		kept = append(kept, p)
	}
	return kept, dropped
}

// lineRange returns 1-indexed (start, end) line numbers for a substring at
// byte offset `idx` of length `n` in source. Both start and end are inclusive.
func lineRange(source string, idx, n int) (int, int) {
	start := 1 + strings.Count(source[:idx], "\n")
	end := start + strings.Count(source[idx:idx+n], "\n")
	return start, end
}
```

Add the `os` import at the top of `ops.go` if not already present (`fmt` is already imported).

- [ ] **Step 4: Run test, confirm pass**

Run: `go test ./internal/wiki/ -v`
Expected: PASS — three subtests green.

- [ ] **Step 5: Commit**

```bash
git add internal/wiki/ops.go internal/wiki/ops_test.go
git commit -m "feat(wiki): ValidateAndAttachEvidence — drop hallucinated pages, compute line ranges"
```

---

### Task 5: Tool schema and system prompt rewrite

**Files:**
- Modify: `internal/wiki/ops.go`
- Modify: `internal/wiki/ops_test.go`

- [ ] **Step 1: Write failing parse test for new schema**

Append to `internal/wiki/ops_test.go`:

```go
func TestExtractPagesFromToolResult(t *testing.T) {
	raw := map[string]any{
		"pages": []any{
			map[string]any{
				"title": "P1",
				"body":  "body 1",
				"links": []any{
					map[string]any{"to": "P2", "type": "supports"},
				},
				"evidence": []any{
					map[string]any{"quote": "first quote"},
					map[string]any{"quote": "second", "explanation": "ignored"},
				},
			},
			map[string]any{
				"title":    "P2",
				"body":     "body 2",
				"evidence": []any{map[string]any{"quote": "another"}},
			},
		},
	}
	pages, err := ExtractPagesFromToolResult(raw)
	if err != nil {
		t.Fatalf("ExtractPagesFromToolResult: %v", err)
	}
	if len(pages) != 2 {
		t.Fatalf("got %d pages, want 2", len(pages))
	}
	if len(pages[0].Evidence) != 2 {
		t.Errorf("page 0 evidence count = %d, want 2", len(pages[0].Evidence))
	}
	if len(pages[0].Links) != 1 || pages[0].Links[0].To != "P2" {
		t.Errorf("page 0 links = %+v", pages[0].Links)
	}
}

func TestExtractPagesMissingPagesKey(t *testing.T) {
	_, err := ExtractPagesFromToolResult(map[string]any{"foo": "bar"})
	if err == nil {
		t.Fatal("expected error for missing 'pages' key")
	}
}
```

- [ ] **Step 2: Run test, confirm failure**

Run: `go test ./internal/wiki/ -run TestExtractPagesFromToolResult -v`
Expected: FAIL — `ExtractPagesFromToolResult` undefined.

- [ ] **Step 3: Refactor `IngestToPages` to extract a pure function**

Replace the body of `IngestToPages` and add `ExtractPagesFromToolResult` in `internal/wiki/ops.go`:

```go
var writePagesTool = llm.ToolSchema{
	Name:        "write_pages",
	Description: "Write wiki pages synthesized from the ingested source content. Every page MUST include verbatim evidence quotes from the source.",
	Properties: map[string]any{
		"pages": map[string]any{
			"type":        "array",
			"description": "Wiki pages. Aim for 1–4 pages per call. Better to return one solid page than five thin ones.",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"title": map[string]any{"type": "string", "description": "Concise page title"},
					"body":  map[string]any{"type": "string", "description": "Markdown body, well-structured"},
					"links": map[string]any{
						"type": "array",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"to":   map[string]any{"type": "string"},
								"type": map[string]any{"type": "string", "enum": []string{"supports", "contradicts", "supersedes", "related"}},
							},
							"required": []string{"to", "type"},
						},
					},
					"evidence": map[string]any{
						"type":        "array",
						"description": "Verbatim quotes copied character-for-character from SOURCE. At least one required per page.",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"quote":       map[string]any{"type": "string", "description": "Verbatim substring of SOURCE"},
								"explanation": map[string]any{"type": "string", "description": "Optional: why this quote supports the page"},
							},
							"required": []string{"quote"},
						},
					},
				},
				"required": []string{"title", "body", "evidence"},
			},
		},
	},
	Required: []string{"pages"},
}

const ingestSystemPrompt = `You write wiki pages strictly grounded in the SOURCE provided.

RULES:
1. Every page MUST include "evidence" — verbatim spans copied character-for-character from SOURCE that justify the page's claims.
2. Do NOT include general knowledge that is not in SOURCE.
3. If SOURCE doesn't contain enough material for a high-quality page on a topic, do NOT create that page.
4. Better to return one solid page than five thin ones. Aim for 1–4 pages per call.
5. Page bodies should synthesize and organize, but every claim must be defensible from the evidence quotes you provide.
6. When linking pages, only reference existing pages or pages you are creating in this same call.`

func IngestToPages(ctx context.Context, client llm.Client, sourceContent string, existingTitles []string) ([]Page, error) {
	var sb strings.Builder
	sb.WriteString("Existing wiki pages (titles only):\n")
	if len(existingTitles) == 0 {
		sb.WriteString("(none yet)\n")
	} else {
		for _, t := range existingTitles {
			sb.WriteString("- " + t + "\n")
		}
	}
	sb.WriteString("\n---\nSOURCE to ingest:\n\n")
	sb.WriteString(sourceContent)

	result, err := client.CompleteStructured(ctx, ingestSystemPrompt, sb.String(), writePagesTool)
	if err != nil {
		return nil, fmt.Errorf("llm structured call: %w", err)
	}

	pages, err := ExtractPagesFromToolResult(result)
	if err != nil {
		return nil, err
	}
	pages, _ = ValidateAndAttachEvidence(pages, sourceContent)
	now := time.Now().UTC()
	for i := range pages {
		pages[i].UpdatedAt = now
		pages[i].ContentHash = HashContent(pages[i].Body)
	}
	return pages, nil
}

// ExtractPagesFromToolResult parses the LLM tool-call result into Page structs.
// It does not validate evidence — call ValidateAndAttachEvidence next.
func ExtractPagesFromToolResult(result map[string]any) ([]Page, error) {
	pagesRaw, ok := result["pages"]
	if !ok {
		return nil, fmt.Errorf("no 'pages' in llm response (keys: %v)", keys(result))
	}
	pagesSlice, ok := toSlice(pagesRaw)
	if !ok {
		return nil, fmt.Errorf("'pages' has unexpected type %T", pagesRaw)
	}
	var pages []Page
	for _, raw := range pagesSlice {
		pm, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		var p Page
		if t, ok := pm["title"].(string); ok {
			p.Title = t
		}
		if b, ok := pm["body"].(string); ok {
			p.Body = b
		}
		if linksRaw, ok := pm["links"].([]any); ok {
			for _, lr := range linksRaw {
				if lm, ok := lr.(map[string]any); ok {
					to, _ := lm["to"].(string)
					typ, _ := lm["type"].(string)
					if to != "" {
						p.Links = append(p.Links, Link{To: to, Type: typ})
					}
				}
			}
		}
		if evRaw, ok := pm["evidence"].([]any); ok {
			for _, er := range evRaw {
				if em, ok := er.(map[string]any); ok {
					q, _ := em["quote"].(string)
					if q != "" {
						p.Evidence = append(p.Evidence, Evidence{Quote: q})
					}
				}
			}
		}
		if p.Title != "" && p.Body != "" {
			pages = append(pages, p)
		}
	}
	return pages, nil
}
```

- [ ] **Step 4: Run test, confirm pass**

Run: `go test ./internal/wiki/ -v`
Expected: PASS — including new extract tests.

- [ ] **Step 5: Commit**

```bash
git add internal/wiki/ops.go internal/wiki/ops_test.go
git commit -m "feat(wiki): require evidence in tool schema, tighten ingest system prompt"
```

---

### Task 6: Cassette infrastructure (record/replay LLM client)

**Files:**
- Create: `internal/llm/cassette.go`
- Create: `internal/llm/cassette_test.go`

- [ ] **Step 1: Write failing cassette test**

Create `internal/llm/cassette_test.go`:

```go
package llm

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

type stubClient struct {
	completeFn   func(system, user string) (string, error)
	structuredFn func(system, user string, ts ToolSchema) (map[string]any, error)
	calls        int
}

func (s *stubClient) Complete(ctx context.Context, system, user string) (string, error) {
	s.calls++
	return s.completeFn(system, user)
}

func (s *stubClient) CompleteStructured(ctx context.Context, system, user string, ts ToolSchema) (map[string]any, error) {
	s.calls++
	return s.structuredFn(system, user, ts)
}

func TestCassetteRecordThenReplay(t *testing.T) {
	dir := t.TempDir()
	stub := &stubClient{
		completeFn: func(system, user string) (string, error) {
			return "live response: " + user, nil
		},
	}
	rec := NewCassetteClient(stub, dir, "test_record_then_replay", ModeRecord)

	got, err := rec.Complete(context.Background(), "sys", "hello")
	if err != nil {
		t.Fatalf("record Complete: %v", err)
	}
	if got != "live response: hello" {
		t.Errorf("record got %q", got)
	}

	files, _ := filepath.Glob(dir + "/test_record_then_replay__*.json")
	if len(files) != 1 {
		t.Fatalf("expected 1 cassette file, got %d", len(files))
	}

	stub2 := &stubClient{}
	rep := NewCassetteClient(stub2, dir, "test_record_then_replay", ModeReplay)
	got2, err := rep.Complete(context.Background(), "sys", "hello")
	if err != nil {
		t.Fatalf("replay Complete: %v", err)
	}
	if got2 != "live response: hello" {
		t.Errorf("replay got %q", got2)
	}
	if stub2.calls != 0 {
		t.Errorf("replay should not call upstream, got %d calls", stub2.calls)
	}
}

func TestCassetteReplayMismatchFails(t *testing.T) {
	dir := t.TempDir()
	stub := &stubClient{
		completeFn: func(system, user string) (string, error) {
			return "ok", nil
		},
	}
	rec := NewCassetteClient(stub, dir, "test_mismatch", ModeRecord)
	rec.Complete(context.Background(), "sys", "first")
	rep := NewCassetteClient(&stubClient{}, dir, "test_mismatch", ModeReplay)
	_, err := rep.Complete(context.Background(), "sys", "DIFFERENT")
	if err == nil {
		t.Fatal("expected mismatch error")
	}
	if !errors.Is(err, ErrCassetteMismatch) {
		t.Errorf("err = %v, want ErrCassetteMismatch", err)
	}
}

func TestCassetteStructuredRoundTrip(t *testing.T) {
	dir := t.TempDir()
	stub := &stubClient{
		structuredFn: func(system, user string, ts ToolSchema) (map[string]any, error) {
			return map[string]any{"pages": []any{map[string]any{"title": "X"}}}, nil
		},
	}
	ts := ToolSchema{Name: "t"}
	rec := NewCassetteClient(stub, dir, "test_structured", ModeRecord)
	rec.CompleteStructured(context.Background(), "s", "u", ts)

	rep := NewCassetteClient(&stubClient{}, dir, "test_structured", ModeReplay)
	got, err := rep.CompleteStructured(context.Background(), "s", "u", ts)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if _, ok := got["pages"]; !ok {
		t.Errorf("got %+v", got)
	}
}
```

- [ ] **Step 2: Run test, confirm failure**

Run: `go test ./internal/llm/ -run TestCassette -v`
Expected: FAIL — `NewCassetteClient`, `ModeRecord`, `ModeReplay`, `ErrCassetteMismatch` undefined.

- [ ] **Step 3: Add `Streaming` to interface (forward declaration for later tasks)**

First update `internal/llm/client.go`:

```go
package llm

import (
	"context"
	"io"
)

type Client interface {
	Complete(ctx context.Context, system, user string) (string, error)
	CompleteStructured(ctx context.Context, system, user string, ts ToolSchema) (map[string]any, error)
	CompleteStream(ctx context.Context, system, user string, w io.Writer) (string, error)
}

type ToolSchema struct {
	Name        string
	Description string
	Properties  map[string]any
	Required    []string
}
```

Now update `stubClient` in the test to also implement `CompleteStream` — append at the bottom of the test file's `stubClient` definition (modify Step 1's test file before re-running):

```go
func (s *stubClient) CompleteStream(ctx context.Context, system, user string, w io.Writer) (string, error) {
	s.calls++
	resp, err := s.completeFn(system, user)
	if err != nil {
		return "", err
	}
	w.Write([]byte(resp))
	return resp, nil
}
```

Add `"io"` import to the test file.

Add stub `CompleteStream` to existing `AnthropicClient` and `OllamaClient` in their respective files (real implementations come in later tasks). Append to `internal/llm/anthropic.go`:

```go
func (c *AnthropicClient) CompleteStream(ctx context.Context, system, user string, w io.Writer) (string, error) {
	// Replaced with real streaming in a later task.
	resp, err := c.Complete(ctx, system, user)
	if err != nil {
		return "", err
	}
	_, _ = w.Write([]byte(resp))
	return resp, nil
}
```

Add `"io"` import to `internal/llm/anthropic.go`.

Append the same stub to `internal/llm/ollama.go`:

```go
func (c *OllamaClient) CompleteStream(ctx context.Context, system, user string, w io.Writer) (string, error) {
	resp, err := c.Complete(ctx, system, user)
	if err != nil {
		return "", err
	}
	_, _ = w.Write([]byte(resp))
	return resp, nil
}
```

Add `"io"` import to `internal/llm/ollama.go`.

- [ ] **Step 4: Implement cassette client**

Create `internal/llm/cassette.go`:

```go
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
)

type Mode int

const (
	ModeReplay Mode = iota
	ModeRecord
	ModeLive
)

var ErrCassetteMismatch = errors.New("cassette: request did not match recorded fixture")

type cassetteEntry struct {
	System         string         `json:"system"`
	User           string         `json:"user"`
	ToolSchemaName string         `json:"tool_schema_name,omitempty"`
	Method         string         `json:"method"`
	Response       any            `json:"response"`
	ResponseText   string         `json:"response_text,omitempty"`
}

type CassetteClient struct {
	upstream Client
	dir      string
	name     string
	mode     Mode
	idx      int64
}

func NewCassetteClient(upstream Client, dir, name string, mode Mode) *CassetteClient {
	if envMode := modeFromEnv(); envMode != ModeReplay {
		mode = envMode
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		panic(fmt.Sprintf("cassette dir: %v", err))
	}
	return &CassetteClient{upstream: upstream, dir: dir, name: name, mode: mode}
}

func modeFromEnv() Mode {
	switch os.Getenv("LLMWIKI_CASSETTE_MODE") {
	case "record":
		return ModeRecord
	case "live":
		return ModeLive
	}
	if os.Getenv("LLMWIKI_RECORD") != "" {
		return ModeRecord
	}
	if os.Getenv("LLMWIKI_LIVE") != "" {
		return ModeLive
	}
	return ModeReplay
}

func (c *CassetteClient) nextPath() string {
	i := atomic.AddInt64(&c.idx, 1)
	return filepath.Join(c.dir, fmt.Sprintf("%s__%03d.json", c.name, i))
}

func (c *CassetteClient) Complete(ctx context.Context, system, user string) (string, error) {
	path := c.nextPath()
	switch c.mode {
	case ModeLive:
		return c.upstream.Complete(ctx, system, user)
	case ModeRecord:
		resp, err := c.upstream.Complete(ctx, system, user)
		if err != nil {
			return "", err
		}
		entry := cassetteEntry{System: system, User: user, Method: "Complete", ResponseText: resp}
		if err := writeEntry(path, entry); err != nil {
			return "", err
		}
		return resp, nil
	default: // Replay
		entry, err := readEntry(path)
		if err != nil {
			return "", err
		}
		if entry.Method != "Complete" || entry.System != system || entry.User != user {
			return "", fmt.Errorf("%w: %s\n  system match: %v\n  user match: %v",
				ErrCassetteMismatch, path, entry.System == system, entry.User == user)
		}
		return entry.ResponseText, nil
	}
}

func (c *CassetteClient) CompleteStructured(ctx context.Context, system, user string, ts ToolSchema) (map[string]any, error) {
	path := c.nextPath()
	switch c.mode {
	case ModeLive:
		return c.upstream.CompleteStructured(ctx, system, user, ts)
	case ModeRecord:
		resp, err := c.upstream.CompleteStructured(ctx, system, user, ts)
		if err != nil {
			return nil, err
		}
		entry := cassetteEntry{System: system, User: user, ToolSchemaName: ts.Name, Method: "CompleteStructured", Response: resp}
		if err := writeEntry(path, entry); err != nil {
			return nil, err
		}
		return resp, nil
	default:
		entry, err := readEntry(path)
		if err != nil {
			return nil, err
		}
		if entry.Method != "CompleteStructured" || entry.System != system || entry.User != user || entry.ToolSchemaName != ts.Name {
			return nil, fmt.Errorf("%w: %s", ErrCassetteMismatch, path)
		}
		m, ok := entry.Response.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("cassette response not a map: %T", entry.Response)
		}
		return m, nil
	}
}

func (c *CassetteClient) CompleteStream(ctx context.Context, system, user string, w io.Writer) (string, error) {
	// Cassette streaming returns the recorded full response in one write — sufficient for tests.
	resp, err := c.Complete(ctx, system, user)
	if err != nil {
		return "", err
	}
	_, _ = w.Write([]byte(resp))
	return resp, nil
}

func writeEntry(path string, entry cassetteEntry) error {
	data, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func readEntry(path string) (cassetteEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return cassetteEntry{}, fmt.Errorf("read cassette %s: %w", path, err)
	}
	var entry cassetteEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return cassetteEntry{}, fmt.Errorf("parse cassette %s: %w", path, err)
	}
	return entry, nil
}
```

- [ ] **Step 5: Run tests, confirm pass**

Run: `go test ./internal/llm/ -v`
Expected: PASS — three cassette subtests green.

- [ ] **Step 6: Commit**

```bash
git add internal/llm/client.go internal/llm/cassette.go internal/llm/cassette_test.go internal/llm/anthropic.go internal/llm/ollama.go
git commit -m "feat(llm): cassette record/replay client and Streaming interface stubs"
```

---

### Task 7: Chunking unit tests + 16KB/no-cap behavior

**Files:**
- Modify: `cmd/ingest.go`
- Create: `cmd/ingest_test.go`

- [ ] **Step 1: Write failing chunk tests**

Create `cmd/ingest_test.go`:

```go
package cmd

import (
	"strings"
	"testing"
)

func TestChunkContentSmallReturnsSingle(t *testing.T) {
	got := chunkContent("hello world", 100)
	if len(got) != 1 || got[0] != "hello world" {
		t.Errorf("got %q", got)
	}
}

func TestChunkContentEmpty(t *testing.T) {
	got := chunkContent("", 100)
	if len(got) != 1 || got[0] != "" {
		t.Errorf("got %q", got)
	}
}

func TestChunkContentSplitsAtNewline(t *testing.T) {
	src := "aaaa\nbbbb\ncccc\ndddd\n"  // 20 chars
	got := chunkContent(src, 10)
	if len(got) < 2 {
		t.Fatalf("got %d chunks", len(got))
	}
	// Every chunk except possibly the last should end with newline boundary.
	for i, c := range got[:len(got)-1] {
		if !strings.HasSuffix(c, "\n") {
			t.Errorf("chunk %d does not end at newline: %q", i, c)
		}
	}
	// Reassembly equals original.
	if strings.Join(got, "") != src {
		t.Errorf("reassembly mismatch")
	}
}

func TestChunkContentNoCapAt16k(t *testing.T) {
	// 50 KB of text should produce ≥3 chunks at 16KB each.
	src := strings.Repeat("a\n", 25000)
	got := chunkContent(src, 16*1024)
	if len(got) < 3 {
		t.Errorf("expected ≥3 chunks at 16k, got %d", len(got))
	}
	if strings.Join(got, "") != src {
		t.Errorf("reassembly mismatch")
	}
}

func TestSlugifyForArchive(t *testing.T) {
	tests := []struct{ in, want string }{
		{"What dependencies?", "what-dependencies"},
		{"Hello,  World!", "hello-world"},
		{"a/b\\c", "a-b-c"},
		{"   ", ""},
	}
	for _, tc := range tests {
		if got := slugify(tc.in); got != tc.want {
			t.Errorf("slugify(%q) = %q want %q", tc.in, got, tc.want)
		}
	}
}
```

- [ ] **Step 2: Run test, confirm failure**

Run: `go test ./cmd/ -run TestChunkContent -v`
Expected: PASS for the first three (chunkContent already exists), FAIL for `TestSlugifyForArchive` (`slugify` undefined). The 16k cap test depends on calling code not the chunk function — we'll address the silent-drop behavior in `runIngest` shortly.

- [ ] **Step 3: Add `slugify` helper, update chunk constant**

Add to `cmd/ingest.go` (anywhere near the chunk helper):

```go
const ingestChunkSize = 16 * 1024
const ingestMaxInflight = 5

func slugify(s string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range s {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevDash = false
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + 32)
			prevDash = false
		default:
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	out := strings.TrimRight(b.String(), "-")
	if len(out) > 60 {
		out = out[:60]
	}
	return out
}
```

- [ ] **Step 4: Run tests, confirm pass**

Run: `go test ./cmd/ -v`
Expected: PASS for slugify and chunk tests.

- [ ] **Step 5: Commit**

```bash
git add cmd/ingest.go cmd/ingest_test.go
git commit -m "feat(cmd): slugify helper, 16KB chunk + 5-inflight constants"
```

---

### Task 8: Replace ingest fan-out — semaphore, no cap, progress

**Files:**
- Modify: `cmd/ingest.go`

- [ ] **Step 1: Rewrite `runIngest` chunk processing**

In `cmd/ingest.go`, replace the section starting with `// Split large content into chunks` through the `wg.Wait()` block (currently lines ~77–106) with:

```go
	// Split into chunks; process all chunks with a bounded-concurrency semaphore.
	chunks := chunkContent(content, ingestChunkSize)
	if len(chunks) > 1 {
		fmt.Printf("  Content is %d bytes, processing %d chunks (max %d in flight)...\n",
			len(content), len(chunks), ingestMaxInflight)
	}

	type result struct {
		pages []wiki.Page
		err   error
		idx   int
	}
	results := make([]result, len(chunks))

	sem := make(chan struct{}, ingestMaxInflight)
	var wg sync.WaitGroup
	var done int64

	for i, chunk := range chunks {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, chunk string) {
			defer wg.Done()
			defer func() { <-sem }()
			got, err := wiki.IngestToPages(ctx, llmClient, chunk, titles)
			results[i] = result{pages: got, err: err, idx: i}
			n := atomic.AddInt64(&done, 1)
			fmt.Printf("\r  [%d/%d] processed", n, len(chunks))
		}(i, chunk)
	}
	wg.Wait()
	if len(chunks) > 1 {
		fmt.Println()
	}

	var pages []wiki.Page
	for _, r := range results {
		if r.err != nil {
			fmt.Printf("  WARN chunk %d failed: %v\n", r.idx+1, r.err)
			continue
		}
		pages = append(pages, r.pages...)
	}
```

Add `"sync/atomic"` to the imports at the top of `cmd/ingest.go` (alongside the existing `"sync"`).

Remove the now-unused `spin := startSpinner(...)` and `spin.Stop()` calls in the same block. The chunkContent old `maxChunks` cap and inline comment are deleted.

- [ ] **Step 2: Build to confirm it compiles**

Run: `go build ./...`
Expected: clean build.

- [ ] **Step 3: Re-run all tests**

Run: `go test ./...`
Expected: all green (no new tests yet for ingest fan-out behavior — it's covered in Task 9's integration tests).

- [ ] **Step 4: Commit**

```bash
git add cmd/ingest.go
git commit -m "feat(cmd): semaphore-bounded ingest fan-out, no chunk cap, [N/M] progress"
```

---

### Task 9: Integration cassettes for ingest

**Files:**
- Create: `internal/llm/testdata/cassettes/.gitkeep` (so dir exists)
- Create: `cmd/ingest_integration_test.go`
- Modify: `internal/wiki/ops.go` (only if a wiring tweak is needed; test will tell)

- [ ] **Step 1: Write failing integration test (skipped without cassette)**

Create `cmd/ingest_integration_test.go`:

```go
package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mritunjaysharma394/llmwiki/internal/llm"
	"github.com/mritunjaysharma394/llmwiki/internal/wiki"
)

// integrationClient builds a cassette wrapping a real Anthropic client when
// ANTHROPIC_API_KEY is set and LLMWIKI_RECORD=1; otherwise pure replay.
func integrationClient(t *testing.T, name string) llm.Client {
	t.Helper()
	cassetteDir := filepath.Join("..", "internal", "llm", "testdata", "cassettes")
	var upstream llm.Client
	if os.Getenv("ANTHROPIC_API_KEY") != "" {
		upstream = llm.NewAnthropicClient("claude-haiku-4-5")
	} else {
		upstream = nil // not used in replay
	}
	mode := llm.ModeReplay
	if os.Getenv("LLMWIKI_RECORD") != "" {
		if upstream == nil {
			t.Fatal("LLMWIKI_RECORD set but ANTHROPIC_API_KEY missing")
		}
		mode = llm.ModeRecord
	}
	return llm.NewCassetteClient(upstream, cassetteDir, name, mode)
}

func TestIngestSmall(t *testing.T) {
	source := "Goroutines are lightweight threads of execution managed by the Go runtime.\nThe `go` keyword starts a goroutine.\nGoroutines communicate via channels.\n"
	client := integrationClient(t, "ingest_small")

	pages, err := wiki.IngestToPages(context.Background(), client, source, nil)
	if err != nil {
		t.Fatalf("IngestToPages: %v", err)
	}
	if len(pages) == 0 {
		t.Fatal("got 0 pages")
	}
	for _, p := range pages {
		if len(p.Evidence) == 0 {
			t.Errorf("page %q has no evidence", p.Title)
		}
		for _, e := range p.Evidence {
			if !strings.Contains(source, e.Quote) {
				t.Errorf("page %q evidence quote not in source: %q", p.Title, e.Quote)
			}
			if e.LineStart < 1 || e.LineEnd < e.LineStart {
				t.Errorf("page %q bad line range %d-%d", p.Title, e.LineStart, e.LineEnd)
			}
		}
	}
}

func TestIngestMultiChunk(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping multi-chunk integration test in -short mode")
	}
	// 50KB synthetic source — three roughly-equal sections.
	var sb strings.Builder
	sb.WriteString("# Section A: HTTP servers in Go\n\n")
	for i := 0; i < 600; i++ {
		sb.WriteString("HTTP servers in Go are built around the net/http package.\n")
	}
	sb.WriteString("\n# Section B: SQL drivers\n\n")
	for i := 0; i < 600; i++ {
		sb.WriteString("Database/sql provides a thin abstraction over driver implementations.\n")
	}
	sb.WriteString("\n# Section C: Context propagation\n\n")
	for i := 0; i < 600; i++ {
		sb.WriteString("context.Context propagates deadlines and cancellation signals.\n")
	}
	source := sb.String()

	client := integrationClient(t, "ingest_multichunk")
	chunks := chunkContent(source, ingestChunkSize)
	if len(chunks) < 3 {
		t.Fatalf("expected ≥3 chunks, got %d", len(chunks))
	}
	var allPages []wiki.Page
	for i, c := range chunks {
		pages, err := wiki.IngestToPages(context.Background(), client, c, nil)
		if err != nil {
			t.Fatalf("chunk %d: %v", i, err)
		}
		allPages = append(allPages, pages...)
	}
	if len(allPages) == 0 {
		t.Fatal("no pages")
	}
	for _, p := range allPages {
		if len(p.Evidence) == 0 {
			t.Errorf("page %q has no evidence", p.Title)
		}
	}
}
```

Create the cassette directory placeholder:

```bash
mkdir -p internal/llm/testdata/cassettes
touch internal/llm/testdata/cassettes/.gitkeep
```

- [ ] **Step 2: Record cassettes (one-time, requires API key)**

Run: `LLMWIKI_RECORD=1 go test ./cmd/ -run "TestIngestSmall|TestIngestMultiChunk" -v`
Expected: PASS, with new files written under `internal/llm/testdata/cassettes/ingest_small__001.json` and `ingest_multichunk__00{1,2,3,...}.json`.

- [ ] **Step 3: Verify replay works (no API key needed)**

Run: `unset ANTHROPIC_API_KEY && go test ./cmd/ -run "TestIngestSmall|TestIngestMultiChunk" -v`
Expected: PASS in replay mode.

- [ ] **Step 4: Commit**

```bash
git add cmd/ingest_integration_test.go internal/llm/testdata/cassettes/
git commit -m "test(ingest): cassette-based integration tests for small + multi-chunk ingest"
```

---

### Task 10: Anthropic prompt caching

**Files:**
- Modify: `internal/llm/anthropic.go`

- [ ] **Step 1: Confirm SDK cache-control parameter shape**

Run: `go doc github.com/anthropics/anthropic-sdk-go.CacheControlEphemeralParam`
Note the exact constructor or struct shape. As of `anthropic-sdk-go v1.37`, it's typically `anthropic.NewCacheControlEphemeralParam()` and a `CacheControl` field on `TextBlockParam`. If the API differs, adapt accordingly.

- [ ] **Step 2: Add cache_control to system prompt in `CompleteStructured`**

In `internal/llm/anthropic.go`, modify the `System` slice in `CompleteStructured`:

```go
		System: []anthropic.TextBlockParam{
			{
				Text:         system,
				CacheControl: anthropic.NewCacheControlEphemeralParam(),
			},
		},
```

If the SDK uses a different field name (e.g., `Cache` or `Type`), use what `go doc` shows. Apply the same change to `Complete` only if you want caching there too — for sub-project 1, ingest is the high-fanout path, so caching `CompleteStructured` alone is the priority.

- [ ] **Step 3: Build, run all tests**

Run: `go build ./... && go test ./...`
Expected: clean build, all green.

- [ ] **Step 4: Smoke-test caching behavior live (optional but recommended)**

Manually re-ingest a multi-chunk source and inspect Anthropic API console for cache_read_input_tokens > 0 on calls 2..N.

- [ ] **Step 5: Commit**

```bash
git add internal/llm/anthropic.go
git commit -m "perf(llm): cache_control ephemeral on ingest system prompt for fan-out reuse"
```

---

### Task 11: Idempotent re-ingest (cascade-delete old evidence)

**Files:**
- Modify: `cmd/ingest.go`
- Modify: `internal/db/db_test.go` (add cascade test)

- [ ] **Step 1: Write failing cascade test**

Append to `internal/db/db_test.go`:

```go
func TestEvidenceCascadeDeleteOnSourceDelete(t *testing.T) {
	d := mustOpen(t)
	srcID, _ := d.UpsertSource("u1", "h1")
	d.UpsertPage(PageRecord{Title: "P1", Path: "p.md", Body: "b", ContentHash: "h", SourceIDs: []int64{srcID}})
	p, _ := d.GetPage("P1")
	d.InsertEvidence(p.ID, srcID, []Evidence{{Quote: "q"}})

	// Delete source — evidence should cascade.
	if _, err := d.sql.Exec(`DELETE FROM sources WHERE id = ?`, srcID); err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	got, _ := d.GetEvidenceForPage(p.ID)
	if len(got) != 0 {
		t.Errorf("evidence not cascade-deleted: got %d rows", len(got))
	}
}
```

- [ ] **Step 2: Run test, confirm pass**

Run: `go test ./internal/db/ -run TestEvidenceCascadeDeleteOnSourceDelete -v`
Expected: PASS — cascade is enforced because Task 1 enabled `PRAGMA foreign_keys = ON` and the `evidence` table has `ON DELETE CASCADE`.

- [ ] **Step 3: Wire cascade into runIngest**

In `cmd/ingest.go`, after successful chunk processing and before writing pages, when `existing != nil && existing.ContentHash != hash` (re-ingest), explicitly delete old evidence for that source. Replace the section that calls `database.UpsertSource` and the page-write loop with:

```go
	// Record source — same UpsertSource call as before, returns sourceID.
	sourceID, err := database.UpsertSource(source, hash)
	if err != nil {
		return fmt.Errorf("recording source: %w", err)
	}

	// On re-ingest with changed content, drop old evidence for this source first.
	if existing != nil && existing.ContentHash != hash {
		if err := database.DeleteEvidenceForSource(sourceID); err != nil {
			return fmt.Errorf("clearing old evidence: %w", err)
		}
	}

	// Write pages to disk and DB
	if err := os.MkdirAll(cfg.Wiki.WikiDir, 0755); err != nil {
		return err
	}
	for i := range pages {
		pages[i].SourceIDs = []int64{sourceID}
		path := wiki.PagePath(cfg.Wiki.WikiDir, pages[i].Title)
		if err := wiki.WritePage(pages[i], cfg.Wiki.WikiDir); err != nil {
			return fmt.Errorf("writing page %q: %w", pages[i].Title, err)
		}
		rec := db.PageRecord{
			Title:       pages[i].Title,
			Path:        path,
			Body:        pages[i].Body,
			ContentHash: pages[i].ContentHash,
			SourceIDs:   pages[i].SourceIDs,
		}
		if err := database.UpsertPage(rec); err != nil {
			return fmt.Errorf("db upsert %q: %w", pages[i].Title, err)
		}

		// Lookup new page ID and insert evidence
		stored, err := database.GetPage(pages[i].Title)
		if err != nil || stored == nil {
			return fmt.Errorf("re-fetch page %q: %v", pages[i].Title, err)
		}
		var dbEv []db.Evidence
		for _, e := range pages[i].Evidence {
			dbEv = append(dbEv, db.Evidence{Quote: e.Quote, LineStart: e.LineStart, LineEnd: e.LineEnd})
		}
		if err := database.InsertEvidence(stored.ID, sourceID, dbEv); err != nil {
			return fmt.Errorf("insert evidence for %q: %w", pages[i].Title, err)
		}

		// Upsert links (unchanged)
		var links []db.Link
		for _, l := range pages[i].Links {
			links = append(links, db.Link{FromPage: pages[i].Title, ToPage: l.To, LinkType: l.Type})
		}
		if len(links) > 0 {
			database.UpsertLinks(pages[i].Title, links)
		}
		fmt.Printf("  ✓ %s (%d evidence)\n", pages[i].Title, len(pages[i].Evidence))
	}
	fmt.Printf("Ingested %d page(s) from %s\n", len(pages), source)
	return nil
}
```

- [ ] **Step 4: Build and run all tests**

Run: `go build ./... && go test ./...`
Expected: clean build, all green.

- [ ] **Step 5: Commit**

```bash
git add cmd/ingest.go internal/db/db_test.go
git commit -m "feat(ingest): persist evidence to DB, cascade-delete old evidence on re-ingest"
```

---

## Phase C — Trustworthy Ask

### Task 12: Two-table FTS retrieval

**Files:**
- Modify: `cmd/ask.go`
- Modify: `internal/db/db_test.go`

- [ ] **Step 1: Write failing retrieval test**

Append to `internal/db/db_test.go`:

```go
func TestSearchPagesAndEvidenceUnion(t *testing.T) {
	d := mustOpen(t)
	srcID, _ := d.UpsertSource("u", "h")
	d.UpsertPage(PageRecord{Title: "Goroutines", Path: "g.md", Body: "lightweight", ContentHash: "h", SourceIDs: []int64{srcID}})
	d.UpsertPage(PageRecord{Title: "Channels", Path: "c.md", Body: "communication primitive", ContentHash: "h", SourceIDs: []int64{srcID}})
	g, _ := d.GetPage("Goroutines")
	c, _ := d.GetPage("Channels")
	d.InsertEvidence(g.ID, srcID, []Evidence{{Quote: "scheduler picks runnable goroutines"}})
	d.InsertEvidence(c.ID, srcID, []Evidence{{Quote: "channels block when full"}})

	// Page FTS hit on body.
	pages, _ := d.SearchPages("communication", 5)
	if len(pages) != 1 || pages[0].Title != "Channels" {
		t.Errorf("page search: %+v", pages)
	}
	// Evidence FTS hit.
	hits, _ := d.SearchEvidence("scheduler", 5)
	if len(hits) != 1 || hits[0].PageID != g.ID {
		t.Errorf("evidence search: %+v", hits)
	}
}
```

- [ ] **Step 2: Run test, confirm pass**

Run: `go test ./internal/db/ -run TestSearchPagesAndEvidenceUnion -v`
Expected: PASS — both query paths work.

- [ ] **Step 3: Rewrite `runAsk` retrieval section**

Replace the FTS-search block in `cmd/ask.go` (lines ~22–37) with:

```go
	// Two-table retrieval: pages_fts and evidence_fts, unioned.
	pageHits, err := database.SearchPages(question, 5)
	if err != nil {
		fmt.Println("(page FTS unavailable; scanning all pages)")
		pageHits, _ = database.AllPages()
		if len(pageHits) > 5 {
			pageHits = pageHits[:5]
		}
	}
	evHits, _ := database.SearchEvidence(question, 10)

	type pageBundle struct {
		page     db.PageRecord
		evidence []db.Evidence
	}
	bundles := map[int64]*pageBundle{}
	order := []int64{}
	for _, p := range pageHits {
		bundles[p.ID] = &pageBundle{page: p}
		order = append(order, p.ID)
	}
	for _, h := range evHits {
		if _, ok := bundles[h.PageID]; !ok {
			page, _ := database.GetPageByID(h.PageID)
			if page == nil {
				continue
			}
			bundles[h.PageID] = &pageBundle{page: *page}
			order = append(order, h.PageID)
		}
		bundles[h.PageID].evidence = append(bundles[h.PageID].evidence, h.Evidence)
	}

	if len(bundles) == 0 {
		all, err := database.AllPages()
		if err != nil {
			return fmt.Errorf("loading pages: %w", err)
		}
		if len(all) == 0 {
			fmt.Println("No pages in wiki yet. Run `llmwiki ingest <source>` first.")
			return nil
		}
		if len(all) > 5 {
			all = all[:5]
		}
		for _, p := range all {
			bundles[p.ID] = &pageBundle{page: p}
			order = append(order, p.ID)
		}
	}

	// Convert to wiki.Page (with evidence attached) preserving discovery order.
	var pages []wiki.Page
	for _, id := range order {
		b := bundles[id]
		var ev []wiki.Evidence
		for _, e := range b.evidence {
			ev = append(ev, wiki.Evidence{Quote: e.Quote, LineStart: e.LineStart, LineEnd: e.LineEnd})
		}
		// If no evidence hits but page has DB evidence, optionally pull a few:
		if len(ev) == 0 {
			dbEv, _ := database.GetEvidenceForPage(b.page.ID)
			for _, e := range dbEv {
				ev = append(ev, wiki.Evidence{Quote: e.Quote, LineStart: e.LineStart, LineEnd: e.LineEnd})
				if len(ev) >= 3 {
					break
				}
			}
		}
		pages = append(pages, wiki.Page{
			Title:    b.page.Title,
			Body:     b.page.Body,
			Evidence: ev,
		})
	}
```

Add `db` to the imports of `cmd/ask.go`:

```go
import (
	"fmt"
	"strings"

	"github.com/mritunjaysharma394/llmwiki/internal/db"
	"github.com/mritunjaysharma394/llmwiki/internal/wiki"
	"github.com/spf13/cobra"
)
```

- [ ] **Step 4: Build and test**

Run: `go build ./... && go test ./...`
Expected: clean build, all green.

- [ ] **Step 5: Commit**

```bash
git add cmd/ask.go internal/db/db_test.go
git commit -m "feat(ask): two-table FTS retrieval — pages + evidence, unioned with attached quotes"
```

---

### Task 13: AnswerQuestion prompt with evidence quotes

**Files:**
- Modify: `internal/wiki/ops.go`
- Modify: `internal/wiki/ops_test.go`

- [ ] **Step 1: Write failing prompt-shape test**

Append to `internal/wiki/ops_test.go`:

```go
func TestBuildAnswerPromptIncludesEvidence(t *testing.T) {
	pages := []Page{{
		Title: "Channels",
		Body:  "channels coordinate goroutines",
		Evidence: []Evidence{
			{Quote: "channels block when full", LineStart: 4, LineEnd: 4},
		},
	}}
	prompt := buildAnswerUserPrompt("how do channels work?", pages)
	if !strings.Contains(prompt, "Channels") {
		t.Error("prompt missing page title")
	}
	if !strings.Contains(prompt, "channels block when full") {
		t.Error("prompt missing evidence quote")
	}
	if !strings.Contains(prompt, "(lines 4-4)") {
		t.Error("prompt missing line range")
	}
	if !strings.Contains(prompt, "Question: how do channels work?") {
		t.Error("prompt missing question")
	}
}
```

- [ ] **Step 2: Run test, confirm failure**

Run: `go test ./internal/wiki/ -run TestBuildAnswerPromptIncludesEvidence -v`
Expected: FAIL — `buildAnswerUserPrompt` undefined.

- [ ] **Step 3: Replace `AnswerQuestion` with evidence-aware prompt**

In `internal/wiki/ops.go`, replace `AnswerQuestion`:

```go
const answerSystemPrompt = `You answer using the provided wiki pages and source quotes.

Cite pages inline using [Page Title] notation.
When using a verbatim quote from a source, render it as a markdown blockquote and label the line range, e.g.:
> "channels block when full" (lines 4-4)

If pages and quotes are insufficient, say so plainly. Do not fabricate.`

func AnswerQuestion(ctx context.Context, client llm.Client, question string, contextPages []Page) (string, error) {
	return client.Complete(ctx, answerSystemPrompt, buildAnswerUserPrompt(question, contextPages))
}

func StreamAnswer(ctx context.Context, client llm.Client, question string, contextPages []Page, w io.Writer) (string, error) {
	return client.CompleteStream(ctx, answerSystemPrompt, buildAnswerUserPrompt(question, contextPages), w)
}

func buildAnswerUserPrompt(question string, pages []Page) string {
	var sb strings.Builder
	sb.WriteString("## Wiki pages\n\n")
	for _, p := range pages {
		sb.WriteString(fmt.Sprintf("### %s\n\n%s\n", p.Title, p.Body))
		if len(p.Evidence) > 0 {
			sb.WriteString("\n**Source quotes for this page:**\n")
			for _, e := range p.Evidence {
				sb.WriteString(fmt.Sprintf("> %q  (lines %d-%d)\n", e.Quote, e.LineStart, e.LineEnd))
			}
		} else {
			sb.WriteString("\n*(no source quotes attached — legacy page)*\n")
		}
		sb.WriteString("\n---\n\n")
	}
	sb.WriteString(fmt.Sprintf("Question: %s", question))
	return sb.String()
}
```

Add `"io"` to the imports of `internal/wiki/ops.go`.

- [ ] **Step 4: Run test, confirm pass**

Run: `go test ./internal/wiki/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/wiki/ops.go internal/wiki/ops_test.go
git commit -m "feat(wiki): evidence-aware answer prompt with verbatim quotes and line ranges"
```

---

### Task 14: Streaming + glamour rendering (TTY-aware)

**Files:**
- Modify: `internal/llm/anthropic.go`
- Modify: `cmd/ask.go`
- Modify: `go.mod` / `go.sum` (add `glamour`)

- [ ] **Step 1: Add glamour dependency**

Run:
```bash
go get github.com/charmbracelet/glamour@latest
```

- [ ] **Step 2: Implement real Anthropic streaming**

Replace the stub `CompleteStream` in `internal/llm/anthropic.go`:

```go
func (c *AnthropicClient) CompleteStream(ctx context.Context, system, user string, w io.Writer) (string, error) {
	stream := c.client.Messages.NewStreaming(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(c.model),
		MaxTokens: 4096,
		System: []anthropic.TextBlockParam{
			{Text: system},
		},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(user)),
		},
	})
	var full strings.Builder
	for stream.Next() {
		event := stream.Current()
		// Defensive: type-switch on event kind. The SDK shape may evolve;
		// for v1.37 the text-delta path is via ContentBlockDeltaEvent.
		switch ev := event.AsAny().(type) {
		case anthropic.ContentBlockDeltaEvent:
			if td, ok := ev.Delta.AsAny().(anthropic.TextDelta); ok {
				if _, err := io.WriteString(w, td.Text); err != nil {
					return "", err
				}
				full.WriteString(td.Text)
			}
		}
	}
	if err := stream.Err(); err != nil {
		return "", err
	}
	return full.String(), nil
}
```

If the SDK uses different event/type names (e.g., the field is `Type`-tagged or there's an `Accumulate()` helper), adapt — `go doc github.com/anthropics/anthropic-sdk-go.MessageStreamEvent` will show the shape. The contract: write text deltas to `w` as they arrive, return the full text when done.

Add `"strings"` to imports of `internal/llm/anthropic.go` if not present.

- [ ] **Step 3: Add TTY-aware render in `cmd/ask.go`**

Replace the `spin := startSpinner("Thinking...")` block and everything after through the existing `Sources:` line in `cmd/ask.go` with:

```go
	isTTY := isatty.IsTerminal(os.Stdout.Fd())
	noStream, _ := cmd.Flags().GetBool("no-stream")

	var answer string
	if isTTY && !noStream {
		// Stream raw markdown tokens to stdout, then re-render with glamour.
		var buf strings.Builder
		mw := io.MultiWriter(os.Stdout, &buf)
		fmt.Println()
		ans, err := wiki.StreamAnswer(ctx, llmClient, question, pages, mw)
		fmt.Println()
		if err != nil {
			return fmt.Errorf("streaming answer: %w", err)
		}
		answer = ans

		// Best-effort clear of the streamed area, then re-render pretty.
		// We don't know the exact line count due to wrapping, so just print a separator.
		rendered, rerr := glamour.Render(answer, glamourStyle())
		if rerr == nil {
			fmt.Println("\n──────")
			fmt.Print(rendered)
		}
	} else {
		spin := startSpinner("Thinking...")
		ans, err := wiki.AnswerQuestion(ctx, llmClient, question, pages)
		spin.Stop()
		if err != nil {
			return fmt.Errorf("llm answer: %w", err)
		}
		answer = ans
		if isTTY {
			rendered, _ := glamour.Render(answer, glamourStyle())
			fmt.Print(rendered)
		} else {
			fmt.Println(answer)
		}
	}

	// Sources block
	printSources(pages, isTTY)
	_ = answer
	return nil
}

func glamourStyle() string {
	if os.Getenv("NO_COLOR") != "" {
		return "notty"
	}
	return "auto"
}

func printSources(pages []wiki.Page, isTTY bool) {
	if len(pages) == 0 {
		return
	}
	var sb strings.Builder
	sb.WriteString("\n── Sources ──\n")
	for i, p := range pages {
		sb.WriteString(fmt.Sprintf("[%d] %s\n", i+1, p.Title))
		for _, e := range p.Evidence {
			sb.WriteString(fmt.Sprintf("    > %q  (lines %d-%d)\n", e.Quote, e.LineStart, e.LineEnd))
		}
	}
	out := sb.String()
	if isTTY {
		rendered, err := glamour.Render(out, glamourStyle())
		if err == nil {
			fmt.Print(rendered)
			return
		}
	}
	fmt.Print(out)
}
```

Add imports to `cmd/ask.go`:

```go
import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/charmbracelet/glamour"
	"github.com/mattn/go-isatty"
	"github.com/mritunjaysharma394/llmwiki/internal/db"
	"github.com/mritunjaysharma394/llmwiki/internal/wiki"
	"github.com/spf13/cobra"
)
```

Register the `--no-stream` flag in `cmd/ask.go`'s `init()` (add one if missing):

```go
func init() {
	askCmd.Flags().Bool("no-stream", false, "force buffered output (no streaming)")
}
```

- [ ] **Step 4: Build, test**

Run: `go build ./... && go test ./...`
Expected: clean build, all green.

- [ ] **Step 5: Manual smoke test (optional but recommended)**

```bash
./llmwiki ingest README.md  # or any small text file
./llmwiki ask "what is this project about?"          # streamed + glamour-rendered
./llmwiki ask "what is this project about?" > /tmp/a.md  # buffered, raw markdown
cat /tmp/a.md
```

- [ ] **Step 6: Commit**

```bash
git add internal/llm/anthropic.go cmd/ask.go go.mod go.sum
git commit -m "feat(ask): TTY-aware streaming with glamour rendering and sources block"
```

---

### Task 15: Auto-archive answers + `--out` / `--no-save`

**Files:**
- Create: `internal/wiki/answer.go`
- Create: `internal/wiki/answer_test.go`
- Modify: `cmd/ask.go`
- Modify: `cmd/root.go` (config)

- [ ] **Step 1: Write failing answer-format test**

Create `internal/wiki/answer_test.go`:

```go
package wiki

import (
	"strings"
	"testing"
	"time"
)

func TestFormatSavedAnswer(t *testing.T) {
	out := FormatSavedAnswer(SavedAnswerInput{
		Question: "what is X?",
		Answer:   "X is Y.",
		Model:    "claude-haiku-4-5",
		Pages: []Page{{
			Title:    "X",
			Evidence: []Evidence{{Quote: "X is short for Y", LineStart: 2, LineEnd: 2}},
		}},
		At: time.Date(2026, 5, 3, 10, 0, 0, 0, time.UTC),
	})
	if !strings.Contains(out, "question: what is X?") {
		t.Errorf("missing question frontmatter:\n%s", out)
	}
	if !strings.Contains(out, "X is Y.") {
		t.Errorf("missing answer body")
	}
	if !strings.Contains(out, `> "X is short for Y"`) {
		t.Errorf("missing source quote")
	}
}
```

- [ ] **Step 2: Run test, confirm failure**

Run: `go test ./internal/wiki/ -run TestFormatSavedAnswer -v`
Expected: FAIL — `FormatSavedAnswer`, `SavedAnswerInput` undefined.

- [ ] **Step 3: Implement answer formatter**

Create `internal/wiki/answer.go`:

```go
package wiki

import (
	"fmt"
	"strings"
	"time"
)

type SavedAnswerInput struct {
	Question string
	Answer   string
	Model    string
	Pages    []Page
	At       time.Time
}

func FormatSavedAnswer(in SavedAnswerInput) string {
	var sb strings.Builder
	sb.WriteString("---\n")
	sb.WriteString(fmt.Sprintf("question: %s\n", strings.ReplaceAll(in.Question, "\n", " ")))
	sb.WriteString(fmt.Sprintf("created_at: %s\n", in.At.UTC().Format(time.RFC3339)))
	sb.WriteString(fmt.Sprintf("model: %s\n", in.Model))
	sb.WriteString("---\n\n")
	sb.WriteString("# Answer\n\n")
	sb.WriteString(in.Answer)
	sb.WriteString("\n\n## Sources\n\n")
	for i, p := range in.Pages {
		sb.WriteString(fmt.Sprintf("**[%d] %s**\n\n", i+1, p.Title))
		for _, e := range p.Evidence {
			sb.WriteString(fmt.Sprintf("> %q  (lines %d-%d)\n\n", e.Quote, e.LineStart, e.LineEnd))
		}
	}
	return sb.String()
}
```

- [ ] **Step 4: Run test, confirm pass**

Run: `go test ./internal/wiki/ -v`
Expected: PASS.

- [ ] **Step 5: Wire auto-archive into `cmd/ask.go`**

Append to `cmd/ask.go` after the `printSources(...)` call inside `runAsk`:

```go
	if !shouldSkipSave(cmd, cfg) {
		filePath, err := saveAnswer(cmd, cfg, question, answer, pages)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  WARN failed to save answer: %v\n", err)
		} else if filePath != "" {
			fmt.Printf("\nsaved: %s\n", filePath)
		}
	}
	return nil
}

func shouldSkipSave(cmd *cobra.Command, c *Config) bool {
	noSave, _ := cmd.Flags().GetBool("no-save")
	if noSave {
		return true
	}
	if c.Ask.AutoSave != nil && !*c.Ask.AutoSave {
		return true
	}
	return false
}

func saveAnswer(cmd *cobra.Command, c *Config, question, answer string, pages []wiki.Page) (string, error) {
	now := time.Now().UTC()
	dir := filepath.Join(filepath.Dir(c.Wiki.WikiDir), "answers")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	slug := slugify(question)
	if slug == "" {
		slug = "question"
	}
	filename := fmt.Sprintf("%s-%s.md", now.Format("2006-01-02-150405"), slug)
	path := filepath.Join(dir, filename)
	body := wiki.FormatSavedAnswer(wiki.SavedAnswerInput{
		Question: question,
		Answer:   answer,
		Model:    c.LLM.Model,
		Pages:    pages,
		At:       now,
	})
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		return "", err
	}

	// Index in DB.
	var pageIDs []int64
	for _, p := range pages {
		stored, _ := database.GetPage(p.Title)
		if stored != nil {
			pageIDs = append(pageIDs, stored.ID)
		}
	}
	_, _ = database.InsertSavedAnswer(db.SavedAnswer{
		Question:     question,
		Answer:       answer,
		Model:        c.LLM.Model,
		CitedPageIDs: pageIDs,
		FilePath:     path,
		CreatedAt:    now,
	})

	// --out duplicate write
	if outPath, _ := cmd.Flags().GetString("out"); outPath != "" {
		if err := os.WriteFile(outPath, []byte(body), 0644); err != nil {
			return "", fmt.Errorf("--out %s: %w", outPath, err)
		}
	}
	return path, nil
}
```

Add imports `path/filepath` and `time` to `cmd/ask.go`. Register the new flags in `init()`:

```go
func init() {
	askCmd.Flags().Bool("no-stream", false, "force buffered output (no streaming)")
	askCmd.Flags().Bool("no-save", false, "skip auto-archiving the answer")
	askCmd.Flags().String("out", "", "also write the answer to this path")
}
```

- [ ] **Step 6: Add `[ask]` config block**

In `cmd/root.go`, extend the config types:

```go
type AskConfig struct {
	AutoSave *bool `toml:"auto_save"`
}

type Config struct {
	LLM  LLMConfig  `toml:"llm"`
	Wiki WikiConfig `toml:"wiki"`
	Ask  AskConfig  `toml:"ask"`
}
```

- [ ] **Step 7: Build and run all tests**

Run: `go build ./... && go test ./...`
Expected: clean build, all green.

- [ ] **Step 8: Commit**

```bash
git add internal/wiki/answer.go internal/wiki/answer_test.go cmd/ask.go cmd/root.go
git commit -m "feat(ask): auto-archive answers to .llmwiki/answers/, --out and --no-save flags"
```

---

### Task 16: Cassettes for ask flow

**Files:**
- Modify: `cmd/ingest_integration_test.go` (rename to a more general file is optional; here we add to it)

- [ ] **Step 1: Write failing ask integration tests**

Append to `cmd/ingest_integration_test.go`:

```go
import "github.com/mritunjaysharma394/llmwiki/internal/db"

func TestAskWithHits(t *testing.T) {
	pages := []wiki.Page{{
		Title: "Goroutines",
		Body:  "Goroutines are lightweight threads of execution managed by the Go runtime.",
		Evidence: []wiki.Evidence{
			{Quote: "lightweight threads of execution", LineStart: 1, LineEnd: 1},
		},
	}}
	client := integrationClient(t, "ask_with_hits")
	answer, err := wiki.AnswerQuestion(context.Background(), client, "what are goroutines?", pages)
	if err != nil {
		t.Fatalf("AnswerQuestion: %v", err)
	}
	if !strings.Contains(strings.ToLower(answer), "goroutine") {
		t.Errorf("answer doesn't mention goroutines: %s", answer)
	}
}

func TestAskNoHits(t *testing.T) {
	client := integrationClient(t, "ask_no_hits")
	answer, err := wiki.AnswerQuestion(context.Background(), client, "what is etcd?", nil)
	if err != nil {
		t.Fatalf("AnswerQuestion: %v", err)
	}
	if answer == "" {
		t.Error("empty answer")
	}
	// Suppress unused-import warning for db when no other test in this file uses it.
	_ = db.PageRecord{}
}
```

- [ ] **Step 2: Record cassettes**

Run: `LLMWIKI_RECORD=1 go test ./cmd/ -run "TestAsk" -v`
Expected: PASS, new cassette files written.

- [ ] **Step 3: Replay verifies**

Run: `unset ANTHROPIC_API_KEY && go test ./cmd/ -run "TestAsk" -v`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add cmd/ingest_integration_test.go internal/llm/testdata/cassettes/
git commit -m "test(ask): cassette tests for ask with-hits and no-hits paths"
```

---

## Phase D — First-run polish

### Task 17: `init` Anthropic+Haiku defaults, key validation

**Files:**
- Modify: `cmd/init.go`

- [ ] **Step 1: Read current init implementation**

Run: `cat cmd/init.go`

- [ ] **Step 2: Replace the default config and add key validation**

Replace the default config string and runInit body in `cmd/init.go`. Final content:

```go
package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

const defaultConfigToml = `[llm]
provider = "anthropic"
model = "claude-haiku-4-5"
ollama_url = "http://localhost:11434"

[wiki]
wiki_dir = ".llmwiki/wiki"
raw_dir  = ".llmwiki/raw"
db_path  = ".llmwiki/wiki.db"

[ask]
auto_save = true
`

const defaultConfigOllamaToml = `[llm]
provider = "ollama"
model = "llama3.2"
ollama_url = "http://localhost:11434"

[wiki]
wiki_dir = ".llmwiki/wiki"
raw_dir  = ".llmwiki/raw"
db_path  = ".llmwiki/wiki.db"

[ask]
auto_save = true
`

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize a new wiki in the current directory",
	RunE:  runInit,
}

func init() {
	initCmd.Flags().String("provider", "anthropic", "default LLM provider: anthropic or ollama")
}

func runInit(cmd *cobra.Command, args []string) error {
	provider, _ := cmd.Flags().GetString("provider")

	dir := ".llmwiki"
	for _, sub := range []string{"", "wiki", "raw", "answers"} {
		p := filepath.Join(dir, sub)
		if err := os.MkdirAll(p, 0755); err != nil {
			return fmt.Errorf("mkdir %s: %w", p, err)
		}
	}

	cfgPath := filepath.Join(dir, "config.toml")
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		var content string
		switch provider {
		case "ollama":
			content = defaultConfigOllamaToml
		default:
			content = defaultConfigToml
		}
		if err := os.WriteFile(cfgPath, []byte(content), 0644); err != nil {
			return fmt.Errorf("writing config: %w", err)
		}
	}
	fmt.Printf("Initialized wiki at %s\n", dir)

	if provider == "anthropic" {
		if os.Getenv("ANTHROPIC_API_KEY") == "" {
			return fmt.Errorf(`ANTHROPIC_API_KEY is not set.
  Get a key at https://console.anthropic.com/settings/keys
  Then: export ANTHROPIC_API_KEY=sk-ant-...
  Or use Ollama instead: llmwiki init --provider ollama`)
		}
	}
	return nil
}
```

- [ ] **Step 3: Build, test**

Run: `go build ./... && go test ./...`
Expected: clean build, all green.

- [ ] **Step 4: Manual smoke test**

```bash
mkdir /tmp/init-test && cd /tmp/init-test
unset ANTHROPIC_API_KEY
~/projects/llmwiki/llmwiki init       # exits 1 with helpful message
cat .llmwiki/config.toml              # provider = "anthropic"
export ANTHROPIC_API_KEY=sk-ant-test  # placeholder, just to clear validation
~/projects/llmwiki/llmwiki init       # succeeds
```

- [ ] **Step 5: Commit**

```bash
git add cmd/init.go
git commit -m "feat(init): default to Anthropic+Haiku, validate ANTHROPIC_API_KEY with helpful error"
```

---

### Task 18: Root flags `--provider` / `--model`, key validation in PreRunE, color errors

**Files:**
- Modify: `cmd/root.go`
- Modify: `go.mod` / `go.sum` (add `fatih/color`)

- [ ] **Step 1: Add color dependency**

Run: `go get github.com/fatih/color@latest`

- [ ] **Step 2: Modify `cmd/root.go`**

Replace `cmd/root.go`:

```go
package cmd

import (
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
	"github.com/fatih/color"
	"github.com/mritunjaysharma394/llmwiki/internal/db"
	"github.com/mritunjaysharma394/llmwiki/internal/llm"
	"github.com/spf13/cobra"
)

type LLMConfig struct {
	Provider  string `toml:"provider"`
	Model     string `toml:"model"`
	OllamaURL string `toml:"ollama_url"`
}

type WikiConfig struct {
	WikiDir string `toml:"wiki_dir"`
	RawDir  string `toml:"raw_dir"`
	DBPath  string `toml:"db_path"`
}

type AskConfig struct {
	AutoSave *bool `toml:"auto_save"`
}

type Config struct {
	LLM  LLMConfig  `toml:"llm"`
	Wiki WikiConfig `toml:"wiki"`
	Ask  AskConfig  `toml:"ask"`
}

var (
	cfg            *Config
	llmClient      llm.Client
	database       *db.DB
	overrideProvider string
	overrideModel    string
)

var rootCmd = &cobra.Command{
	Use:   "llmwiki",
	Short: "LLM-powered personal wiki",
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		// init doesn't need a loaded config / live LLM client
		if cmd.Name() == "init" || cmd.Name() == "help" {
			return nil
		}
		return loadConfig()
	},
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		color.New(color.FgRed, color.Bold).Fprint(os.Stderr, "Error: ")
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func loadConfig() error {
	cfg = &Config{}
	if _, err := toml.DecodeFile(".llmwiki/config.toml", cfg); err != nil {
		return fmt.Errorf("config not found — run 'llmwiki init' first: %w", err)
	}
	if overrideProvider != "" {
		cfg.LLM.Provider = overrideProvider
	}
	if overrideModel != "" {
		cfg.LLM.Model = overrideModel
	}
	if cfg.LLM.OllamaURL == "" {
		cfg.LLM.OllamaURL = "http://localhost:11434"
	}
	var err error
	database, err = db.Open(cfg.Wiki.DBPath)
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}
	switch cfg.LLM.Provider {
	case "anthropic", "":
		if os.Getenv("ANTHROPIC_API_KEY") == "" {
			return fmt.Errorf(`ANTHROPIC_API_KEY is not set.
  Get a key at https://console.anthropic.com/settings/keys
  Then: export ANTHROPIC_API_KEY=sk-ant-...
  Or use Ollama: llmwiki --provider ollama <command>`)
		}
		llmClient = llm.NewAnthropicClient(cfg.LLM.Model)
	case "ollama":
		llmClient = llm.NewOllamaClient(cfg.LLM.Model, cfg.LLM.OllamaURL)
	default:
		return fmt.Errorf("unknown provider %q", cfg.LLM.Provider)
	}
	return nil
}

func init() {
	rootCmd.PersistentFlags().StringVar(&overrideProvider, "provider", "", "override LLM provider (anthropic|ollama)")
	rootCmd.PersistentFlags().StringVar(&overrideModel, "model", "", "override LLM model")
	rootCmd.AddCommand(initCmd)
	rootCmd.AddCommand(ingestCmd)
	rootCmd.AddCommand(askCmd)
	rootCmd.AddCommand(lintCmd)
	rootCmd.AddCommand(statusCmd)
}
```

- [ ] **Step 3: Build and run all tests**

Run: `go build ./... && go test ./...`
Expected: clean build, all green.

- [ ] **Step 4: Smoke test**

```bash
cd /home/mritunjay/projects/llmwiki
unset ANTHROPIC_API_KEY
./llmwiki status      # red "Error:" with helpful key message
export ANTHROPIC_API_KEY=...
./llmwiki status      # works
./llmwiki --provider ollama status  # uses ollama if ollama running, else error
```

- [ ] **Step 5: Commit**

```bash
git add cmd/root.go go.mod go.sum
git commit -m "feat(cli): --provider/--model flags, key validation in PreRunE, colored error prefix"
```

---

### Task 19: Status command shows new fields

**Files:**
- Modify: `cmd/status.go`

- [ ] **Step 1: Replace runStatus**

Replace `cmd/status.go`:

```go
package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Print wiki statistics",
	RunE:  runStatus,
}

func runStatus(cmd *cobra.Command, args []string) error {
	stats, err := database.GetStats()
	if err != nil {
		return fmt.Errorf("get stats: %w", err)
	}
	fmt.Printf("pages:           %d\n", stats.TotalPages)
	fmt.Printf("sources:         %d\n", stats.TotalSources)
	fmt.Printf("evidence quotes: %d\n", stats.EvidenceQuotes)
	fmt.Printf("legacy pages:    %d  (run 'llmwiki ingest' on the original sources to upgrade)\n", stats.LegacyPages)
	fmt.Printf("saved answers:   %d\n", stats.SavedAnswers)
	if !stats.LastIngest.IsZero() {
		fmt.Printf("last ingest:     %s\n", stats.LastIngest.Format("2006-01-02 15:04:05 MST"))
	}
	return nil
}
```

- [ ] **Step 2: Build and run all tests**

Run: `go build ./... && go test ./...`
Expected: clean build, all green.

- [ ] **Step 3: Commit**

```bash
git add cmd/status.go
git commit -m "feat(status): show evidence_quotes, legacy_pages, saved_answers"
```

---

### Task 20: Ollama parity stub for streaming (degraded but functional)

**Files:**
- Modify: `internal/llm/ollama.go`

The cassette plumbing relies on every `Client` implementing `CompleteStream`. Ollama's existing stub from Task 6 already wraps `Complete`. Confirm no further work is needed.

- [ ] **Step 1: Verify Ollama still passes interface assertion**

Run: `go vet ./...`
Expected: clean.

- [ ] **Step 2: Verify `Client` interface assertion**

Add to the bottom of `internal/llm/ollama.go`:

```go
var _ Client = (*OllamaClient)(nil)
```

And to `internal/llm/anthropic.go`:

```go
var _ Client = (*AnthropicClient)(nil)
```

- [ ] **Step 3: Build, test**

Run: `go build ./... && go test ./...`
Expected: clean.

- [ ] **Step 4: Commit**

```bash
git add internal/llm/ollama.go internal/llm/anthropic.go
git commit -m "chore(llm): compile-time Client interface assertions for both providers"
```

---

## Phase E — Wrap

### Task 21: GitHub Actions CI

**Files:**
- Create: `.github/workflows/test.yml`

- [ ] **Step 1: Write workflow**

Create `.github/workflows/test.yml`:

```yaml
name: test

on:
  push:
    branches: [master, main]
  pull_request:

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: "1.26"
      - name: Build
        run: go build ./...
      - name: Vet
        run: go vet ./...
      - name: Test (cassette replay, no API key)
        run: go test ./...
```

- [ ] **Step 2: Commit**

```bash
git add .github/workflows/test.yml
git commit -m "ci: go build + vet + test on push (cassette replay mode)"
```

---

### Task 22: Final verification — fresh end-to-end test

**Files:** none (verification only)

- [ ] **Step 1: Wipe local wiki state**

```bash
rm -rf .llmwiki
go build -o llmwiki .
```

- [ ] **Step 2: Verify init defaults**

```bash
unset ANTHROPIC_API_KEY
./llmwiki init
# Expected: red "Error:" + helpful key message, exit 1.
# Config should still be written.
cat .llmwiki/config.toml
# Expected: provider = "anthropic", model = "claude-haiku-4-5", [ask] auto_save = true
```

- [ ] **Step 3: Verify ingest with evidence**

```bash
export ANTHROPIC_API_KEY=sk-ant-...
./llmwiki init                 # succeeds now
./llmwiki ingest README.md     # or any small text file
# Expected: progress lines, every "✓ {title} ({N} evidence)" shows N ≥ 1
ls .llmwiki/wiki/
# Expected: page files exist
head -30 .llmwiki/wiki/*.md | grep evidence
# Expected: evidence: yaml block present
```

- [ ] **Step 4: Verify multi-chunk no silent drop**

```bash
./llmwiki ingest internal/  # ingests all .go files concatenated through ReadLocal
# Expected: "[N/M] processed" progress, no "first 3 of N chunks" message
```

- [ ] **Step 5: Verify ask streaming + glamour**

```bash
./llmwiki ask "what does the validation pass do?"
# Expected: streamed tokens, then re-rendered pretty answer + Sources block with quotes
```

- [ ] **Step 6: Verify ask piped**

```bash
./llmwiki ask "what does the validation pass do?" > /tmp/answer.md
cat /tmp/answer.md
# Expected: clean markdown, no ANSI codes
```

- [ ] **Step 7: Verify auto-archive**

```bash
ls .llmwiki/answers/
# Expected: timestamped markdown files, one per ask
```

- [ ] **Step 8: Verify status**

```bash
./llmwiki status
# Expected: evidence_quotes > 0, saved_answers ≥ 2, legacy_pages = 0
```

- [ ] **Step 9: Verify all tests**

```bash
go test ./...
# Expected: all green in cassette replay mode
```

- [ ] **Step 10: Tag the milestone (optional)**

```bash
git tag -a v0.1-trust -m "Sub-project 1 complete: trustworthy ingest + answers"
```

---

## Done criteria

When all 22 tasks are complete and the verification commands above all pass, sub-project 1 is done. Sub-project 3 (real-world ingestion) becomes the next plan.
