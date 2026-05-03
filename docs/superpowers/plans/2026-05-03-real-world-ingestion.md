# Real-world Ingestion — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `llmwiki ingest` actually usable on real-world inputs — PDFs, real URLs (with article extraction), real repos and directories (with `.gitignore`/deny-list/size-cap respect) — while strengthening the trust property to per-file granularity (every `Evidence` row is anchored to a specific `SourceFile`, not a 500KB concatenation).

**Architecture:** Replace the "fetch a string, chunk it, validate against the string" pipeline with `fetch → normalize → chunk-with-anchors → validate-against-anchors`. `fetch` returns `[]SourceFile` (path + content + page metadata) instead of `string`. `chunk-with-anchors` packs SourceFiles into LLM-sized chunks while preserving `=== path ===` headers; chunks never split a file unless the file alone exceeds chunk size. The validator looks each quote up against its named file. A new `source_files` table stores per-file content hashes, enabling per-file incremental re-ingest.

**Tech Stack:** Go 1.26, plus four new direct deps: `github.com/ledongthuc/pdf` (PDF text extraction, BSD-3, pure Go), `github.com/JohannesKaufmann/html-to-markdown/v2` (HTML→MD, MIT), `github.com/go-shiori/go-readability` (article extraction, MIT), `github.com/sabhiram/go-gitignore` (`.gitignore` matching, MIT).

**Spec:** [`docs/superpowers/specs/2026-05-03-real-world-ingestion-design.md`](../specs/2026-05-03-real-world-ingestion-design.md)

---

## File Structure

| File | Purpose | Action |
|---|---|---|
| `internal/db/db.go` | v2 migration: `source_files` table + `evidence.source_file_id` column | Modify |
| `internal/db/queries.go` | `UpsertSourceFile`/`GetSourceFiles`/`DeleteSourceFile`, evidence with `source_file_id`, stats fields | Modify |
| `internal/db/db_test.go` | v0/v1/v2 migration tests, source-files CRUD, cascade tests | Modify |
| `internal/ingest/types.go` | `SourceFile` struct, `HashSourceFile`, `LineCount` helpers | Create |
| `internal/ingest/types_test.go` | Hash and line-count tests | Create |
| `internal/ingest/chunk.go` | `Chunk`, `ChunkSourceFiles` greedy bin-packer with line-range splits | Create |
| `internal/ingest/chunk_test.go` | Bin-packing tests, oversized-file split tests | Create |
| `internal/ingest/local.go` | New file replacing `file.go`: walker w/ deny list, gitignore, size cap, PDF dispatch, returns `[]SourceFile` | Create |
| `internal/ingest/file.go` | Old single-string reader, deleted | Delete |
| `internal/ingest/local_test.go` | Walker tests against `testdata/dirs/sample/` fixtures | Create |
| `internal/ingest/pdf.go` | `ReadPDF` per-page text extraction, scanned-page heuristic | Create |
| `internal/ingest/pdf_test.go` | PDF fixture tests (text + scanned) | Create |
| `internal/ingest/url.go` | Rewrite: `http.Client` w/ timeout/UA/limit, content-type sniffing, Readability+HTML-to-MD, PDF route | Modify |
| `internal/ingest/url_test.go` | `httptest.Server` tests for HTML/PDF/text/error branches | Create |
| `internal/ingest/github.go` | Rewrite: clone → delegate to local-dir walker, drop docs preference | Modify |
| `internal/ingest/testdata/dirs/sample/` | Fixture: realistic dir layout for walker tests | Create |
| `internal/ingest/testdata/dirs/minirepo/` | Fixture: tiny "repo" for cassette test | Create |
| `internal/ingest/testdata/pdfs/simple.pdf` | 2-page text PDF fixture | Create |
| `internal/ingest/testdata/pdfs/scanned.pdf` | 1-page image-only PDF fixture | Create |
| `internal/wiki/page.go` | `Evidence.SourceFilePath` field + frontmatter round-trip | Modify |
| `internal/wiki/page_test.go` | Round-trip test for `source_file` field | Modify |
| `internal/wiki/ops.go` | Tool schema gains `source_file`; `ValidateAndAttachEvidence([]Page, []SourceFile)`; system prompt addendum; `IngestToPages` signature change; ask prompt formatter | Modify |
| `internal/wiki/ops_test.go` | Per-file validator tests, fallback-when-source-file-missing test | Modify |
| `cmd/ingest.go` | Consume `[]SourceFile`, run new chunker, per-file dedup, write `source_files` rows, new flags | Modify |
| `cmd/ingest_test.go` | Flag-plumbing tests, dedup partition test | Modify |
| `cmd/ingest_integration_test.go` | New cassette tests: `TestIngestPDF`, `TestIngestRepo` | Modify |
| `cmd/root.go` | `IngestConfig` struct, defaults applied in `loadConfig` | Modify |
| `cmd/init.go` | `[ingest]` block in default config templates | Modify |
| `cmd/ask.go` | Sources block uses `(file:a-b)` and `(page-N:a-b)` formatting | Modify |
| `cmd/status.go` | Print `total_source_files` and `largest_source` | Modify |
| `go.mod` / `go.sum` | New direct deps | Modify |
| `README.md` | Brief notes on PDFs, URLs, repo skip rules | Modify |

**Total:** 22 tasks across 10 phases. Each task ends with a commit.

---

## Phase A — Schema and types

### Task 1: schema migration v2 — `source_files` table + `evidence.source_file_id`

**Files:**
- Modify: `internal/db/db.go`
- Modify: `internal/db/db_test.go`

- [ ] **Step 1: Write failing v2 migration tests**

Append to `internal/db/db_test.go`:

```go
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
```

Update `TestOpenCreatesEvidenceAndSavedAnswers` (existing) — change the `if version != 1` assertion to `if version != 2`. Same for `TestOpenIsIdempotent` and `TestOpenUpgradesLegacyDB`.

- [ ] **Step 2: Run tests, confirm failure**

Run: `go test ./internal/db/ -run TestOpen -v`
Expected: FAIL — `source_files` table missing, `source_file_id` column missing, `user_version` is 1.

- [ ] **Step 3: Add v2 migration block in `db.go`**

In `internal/db/db.go`'s `migrate()`, after the `if version < 1` block and before the `PRAGMA foreign_keys = ON` exec, insert:

```go
if version < 2 {
    v2 := []string{
        `CREATE TABLE IF NOT EXISTS source_files (
            id INTEGER PRIMARY KEY,
            source_id INTEGER NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
            relative_path TEXT NOT NULL,
            content_hash TEXT NOT NULL,
            byte_size INTEGER NOT NULL,
            line_count INTEGER NOT NULL,
            ingested_at DATETIME DEFAULT CURRENT_TIMESTAMP,
            UNIQUE(source_id, relative_path)
        )`,
        `CREATE INDEX IF NOT EXISTS idx_source_files_source ON source_files(source_id)`,
        // ALTER TABLE ADD COLUMN is idempotent-friendly via a check.
        `ALTER TABLE evidence ADD COLUMN source_file_id INTEGER REFERENCES source_files(id) ON DELETE CASCADE`,
        `CREATE INDEX IF NOT EXISTS idx_evidence_source_file ON evidence(source_file_id)`,
        `PRAGMA user_version = 2`,
    }
    for _, stmt := range v2 {
        if _, err := d.sql.Exec(stmt); err != nil {
            // ALTER TABLE ADD COLUMN errors with "duplicate column" if re-run on a
            // half-migrated db; tolerate that one specific error and keep going.
            if !strings.Contains(err.Error(), "duplicate column") {
                return fmt.Errorf("v2 migration %q: %w", stmt[:min(50, len(stmt))], err)
            }
        }
    }
}
```

Add `"strings"` to the imports at the top of `db.go`.

- [ ] **Step 4: Run tests, confirm pass**

Run: `go test ./internal/db/ -v`
Expected: PASS — all migration tests green; idempotent re-open keeps `user_version = 2`.

- [ ] **Step 5: Commit**

```bash
git add internal/db/db.go internal/db/db_test.go
git commit -m "feat(db): schema v2 — source_files table + evidence.source_file_id"
```

---

### Task 2: `source_files` queries + `Stats` extensions

**Files:**
- Modify: `internal/db/queries.go`
- Modify: `internal/db/db_test.go`

- [ ] **Step 1: Write failing CRUD tests**

Append to `internal/db/db_test.go`:

```go
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
```

Add `"fmt"` import to `db_test.go` if not already present.

- [ ] **Step 2: Run tests, confirm failure**

Run: `go test ./internal/db/ -v`
Expected: FAIL — `SourceFile`, `UpsertSourceFile`, `GetSourceFiles`, `DeleteSourceFile`, `Stats.TotalSourceFiles`, `Stats.LargestSources`, `Evidence.SourceFileID` undefined.

- [ ] **Step 3: Add types and queries**

In `internal/db/queries.go`:

1. Extend the `Evidence` struct:

```go
type Evidence struct {
    ID           int64
    PageID       int64
    SourceID     int64
    SourceFileID *int64
    Quote        string
    LineStart    int
    LineEnd      int
    CreatedAt    time.Time
}
```

2. Add `SourceFile` type and a `LargestSource` helper:

```go
type SourceFile struct {
    ID           int64
    SourceID     int64
    RelativePath string
    ContentHash  string
    ByteSize     int64
    LineCount    int
    IngestedAt   time.Time
}

type LargestSource struct {
    SourceID  int64
    URI       string
    FileCount int
}
```

3. Extend `Stats`:

```go
type Stats struct {
    TotalPages       int
    TotalSources     int
    TotalSourceFiles int
    StalePages       int
    EvidenceQuotes   int
    LegacyPages      int
    SavedAnswers     int
    LastIngest       time.Time
    LargestSources   []LargestSource
}
```

4. Add CRUD:

```go
func (d *DB) UpsertSourceFile(f SourceFile) (int64, error) {
    var id int64
    err := d.sql.QueryRow(
        `INSERT INTO source_files (source_id, relative_path, content_hash, byte_size, line_count, ingested_at)
        VALUES (?, ?, ?, ?, ?, ?)
        ON CONFLICT(source_id, relative_path) DO UPDATE SET
            content_hash=excluded.content_hash,
            byte_size=excluded.byte_size,
            line_count=excluded.line_count,
            ingested_at=excluded.ingested_at
        RETURNING id`,
        f.SourceID, f.RelativePath, f.ContentHash, f.ByteSize, f.LineCount,
        time.Now().UTC().Format(time.RFC3339),
    ).Scan(&id)
    return id, err
}

func (d *DB) GetSourceFiles(sourceID int64) ([]SourceFile, error) {
    rows, err := d.sql.Query(
        `SELECT id, source_id, relative_path, content_hash, byte_size, line_count, ingested_at
        FROM source_files WHERE source_id = ? ORDER BY relative_path`,
        sourceID,
    )
    if err != nil {
        return nil, err
    }
    defer rows.Close()
    var out []SourceFile
    for rows.Next() {
        var f SourceFile
        var ts string
        if err := rows.Scan(&f.ID, &f.SourceID, &f.RelativePath, &f.ContentHash, &f.ByteSize, &f.LineCount, &ts); err != nil {
            return nil, err
        }
        f.IngestedAt, _ = time.Parse(time.RFC3339, ts)
        out = append(out, f)
    }
    return out, rows.Err()
}

func (d *DB) DeleteSourceFile(id int64) error {
    _, err := d.sql.Exec(`DELETE FROM source_files WHERE id = ?`, id)
    return err
}

func (d *DB) DeleteEvidenceForSourceFile(sourceFileID int64) error {
    _, err := d.sql.Exec(`DELETE FROM evidence WHERE source_file_id = ?`, sourceFileID)
    return err
}
```

5. Update `InsertEvidence` to write `source_file_id`:

```go
func (d *DB) InsertEvidence(pageID, sourceID int64, items []Evidence) error {
    if len(items) == 0 {
        return nil
    }
    tx, err := d.sql.Begin()
    if err != nil {
        return err
    }
    defer tx.Rollback()
    stmt, err := tx.Prepare(`INSERT INTO evidence (page_id, source_id, source_file_id, quote, line_start, line_end) VALUES (?, ?, ?, ?, ?, ?)`)
    if err != nil {
        return err
    }
    defer stmt.Close()
    for _, e := range items {
        var ls, le, sfid interface{}
        if e.LineStart > 0 {
            ls = e.LineStart
        }
        if e.LineEnd > 0 {
            le = e.LineEnd
        }
        if e.SourceFileID != nil {
            sfid = *e.SourceFileID
        }
        if _, err := stmt.Exec(pageID, sourceID, sfid, e.Quote, ls, le); err != nil {
            return fmt.Errorf("insert evidence: %w", err)
        }
    }
    return tx.Commit()
}
```

6. Update `GetEvidenceForPage`, `SearchEvidence`, and the `EvidenceHit` scan paths to read the new column:

```go
func (d *DB) GetEvidenceForPage(pageID int64) ([]Evidence, error) {
    rows, err := d.sql.Query(`SELECT id, page_id, source_id, source_file_id, quote, COALESCE(line_start, 0), COALESCE(line_end, 0), created_at FROM evidence WHERE page_id = ? ORDER BY id`, pageID)
    if err != nil {
        return nil, err
    }
    defer rows.Close()
    var out []Evidence
    for rows.Next() {
        var e Evidence
        var sfid sql.NullInt64
        var created string
        if err := rows.Scan(&e.ID, &e.PageID, &e.SourceID, &sfid, &e.Quote, &e.LineStart, &e.LineEnd, &created); err != nil {
            return nil, err
        }
        if sfid.Valid {
            v := sfid.Int64
            e.SourceFileID = &v
        }
        e.CreatedAt, _ = time.Parse(time.RFC3339, created)
        out = append(out, e)
    }
    return out, rows.Err()
}
```

Apply the matching `source_file_id` scan in `SearchEvidence`. Update its `SELECT` column list accordingly.

7. Update `GetStats` to populate the new fields:

```go
func (d *DB) GetStats() (Stats, error) {
    var s Stats
    d.sql.QueryRow(`SELECT COUNT(*) FROM pages`).Scan(&s.TotalPages)
    d.sql.QueryRow(`SELECT COUNT(*) FROM sources`).Scan(&s.TotalSources)
    d.sql.QueryRow(`SELECT COUNT(*) FROM source_files`).Scan(&s.TotalSourceFiles)
    d.sql.QueryRow(`SELECT COUNT(*) FROM evidence`).Scan(&s.EvidenceQuotes)
    d.sql.QueryRow(`SELECT COUNT(*) FROM pages p LEFT JOIN evidence e ON e.page_id = p.id WHERE e.id IS NULL`).Scan(&s.LegacyPages)
    d.sql.QueryRow(`SELECT COUNT(*) FROM saved_answers`).Scan(&s.SavedAnswers)
    var lastIngestStr string
    d.sql.QueryRow(`SELECT MAX(ingested_at) FROM sources`).Scan(&lastIngestStr)
    s.LastIngest, _ = time.Parse(time.RFC3339, lastIngestStr)

    rows, err := d.sql.Query(`SELECT s.id, s.uri, COUNT(sf.id) AS n
        FROM sources s LEFT JOIN source_files sf ON sf.source_id = s.id
        GROUP BY s.id ORDER BY n DESC LIMIT 3`)
    if err == nil {
        defer rows.Close()
        for rows.Next() {
            var ls LargestSource
            if err := rows.Scan(&ls.SourceID, &ls.URI, &ls.FileCount); err == nil {
                s.LargestSources = append(s.LargestSources, ls)
            }
        }
    }
    return s, nil
}
```

- [ ] **Step 4: Run tests, confirm pass**

Run: `go test ./internal/db/ -v`
Expected: PASS — all source-files CRUD subtests + cascade test + stats subtest green.

- [ ] **Step 5: Commit**

```bash
git add internal/db/queries.go internal/db/db_test.go
git commit -m "feat(db): source_files CRUD, evidence.source_file_id round-trip, stats fields"
```

---

### Task 3: `internal/ingest/types.go` — `SourceFile` struct + helpers

**Files:**
- Create: `internal/ingest/types.go`
- Create: `internal/ingest/types_test.go`

- [ ] **Step 1: Write failing test for hash + line count**

Create `internal/ingest/types_test.go`:

```go
package ingest

import (
    "testing"
)

func TestNewSourceFileHashesAndCountsLines(t *testing.T) {
    sf := NewSourceFile("readme.md", []byte("alpha\nbeta\ngamma"))
    if sf.RelativePath != "readme.md" {
        t.Errorf("path = %q", sf.RelativePath)
    }
    if sf.ByteSize != 16 {
        t.Errorf("byte size = %d, want 16", sf.ByteSize)
    }
    if sf.LineCount != 3 {
        t.Errorf("line count = %d, want 3", sf.LineCount)
    }
    if len(sf.ContentHash) != 64 {
        t.Errorf("content hash length = %d, want 64 (sha256 hex)", len(sf.ContentHash))
    }
}

func TestNewSourceFileTrailingNewline(t *testing.T) {
    sf := NewSourceFile("x", []byte("a\nb\n"))
    if sf.LineCount != 2 {
        t.Errorf("trailing-newline line count = %d, want 2", sf.LineCount)
    }
}

func TestNewSourceFileEmpty(t *testing.T) {
    sf := NewSourceFile("x", []byte{})
    if sf.LineCount != 0 {
        t.Errorf("empty line count = %d, want 0", sf.LineCount)
    }
    if sf.ByteSize != 0 {
        t.Errorf("empty byte size = %d", sf.ByteSize)
    }
}
```

- [ ] **Step 2: Run test, confirm failure**

Run: `go test ./internal/ingest/ -run NewSourceFile -v`
Expected: FAIL — `SourceFile`, `NewSourceFile` undefined.

- [ ] **Step 3: Implement**

Create `internal/ingest/types.go`:

```go
package ingest

import (
    "crypto/sha256"
    "encoding/hex"
    "strings"
)

// SourceFile is one logical "file" inside a source: a single file inside a
// directory/repo, a single page inside a PDF (relative_path = "page-N"), or
// the whole document for an HTML/text source (relative_path = "index.html").
//
// Every Evidence row in the DB is anchored to one SourceFile. Quote line
// numbers are within Content (1-indexed).
type SourceFile struct {
    RelativePath string
    Content      string
    ContentHash  string
    ByteSize     int64
    LineCount    int
}

// NewSourceFile populates Content/Hash/ByteSize/LineCount from raw bytes.
func NewSourceFile(relPath string, content []byte) SourceFile {
    sum := sha256.Sum256(content)
    return SourceFile{
        RelativePath: relPath,
        Content:      string(content),
        ContentHash:  hex.EncodeToString(sum[:]),
        ByteSize:     int64(len(content)),
        LineCount:    countLines(string(content)),
    }
}

// countLines returns 1-indexed-aware line count: "" -> 0, "a" -> 1, "a\n" -> 1,
// "a\nb" -> 2, "a\nb\n" -> 2. Matches what the user perceives as "N lines".
func countLines(s string) int {
    if s == "" {
        return 0
    }
    n := strings.Count(s, "\n")
    if !strings.HasSuffix(s, "\n") {
        n++
    }
    return n
}
```

- [ ] **Step 4: Run test, confirm pass**

Run: `go test ./internal/ingest/ -run NewSourceFile -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/ingest/types.go internal/ingest/types_test.go
git commit -m "feat(ingest): SourceFile type with content hash and line count"
```

---

## Phase B — Chunker

### Task 4: `ChunkSourceFiles` greedy bin-packer

**Files:**
- Create: `internal/ingest/chunk.go`
- Create: `internal/ingest/chunk_test.go`

- [ ] **Step 1: Write failing chunker tests**

Create `internal/ingest/chunk_test.go`:

```go
package ingest

import (
    "strings"
    "testing"
)

func TestChunkSourceFilesEmpty(t *testing.T) {
    got := ChunkSourceFiles(nil, 1024)
    if len(got) != 0 {
        t.Errorf("empty input → %d chunks, want 0", len(got))
    }
}

func TestChunkSourceFilesSingleSmall(t *testing.T) {
    f := NewSourceFile("a.md", []byte("hello"))
    got := ChunkSourceFiles([]SourceFile{f}, 1024)
    if len(got) != 1 {
        t.Fatalf("got %d chunks, want 1", len(got))
    }
    if !strings.Contains(got[0].Text, "=== a.md ===") {
        t.Errorf("missing header: %q", got[0].Text)
    }
    if !strings.Contains(got[0].Text, "hello") {
        t.Errorf("missing body: %q", got[0].Text)
    }
    if len(got[0].Files) != 1 || got[0].Files[0].RelativePath != "a.md" {
        t.Errorf("Files anchor = %+v", got[0].Files)
    }
}

func TestChunkSourceFilesPacksMultiple(t *testing.T) {
    files := []SourceFile{
        NewSourceFile("a.md", []byte(strings.Repeat("a", 30))),
        NewSourceFile("b.md", []byte(strings.Repeat("b", 30))),
        NewSourceFile("c.md", []byte(strings.Repeat("c", 30))),
    }
    // Budget enough for two file blocks, not three.
    got := ChunkSourceFiles(files, 100)
    if len(got) != 2 {
        t.Fatalf("got %d chunks, want 2 (texts: %v)", len(got), chunkTexts(got))
    }
    if len(got[0].Files) != 2 {
        t.Errorf("chunk[0] anchors = %d, want 2", len(got[0].Files))
    }
    if len(got[1].Files) != 1 {
        t.Errorf("chunk[1] anchors = %d, want 1", len(got[1].Files))
    }
}

func TestChunkSourceFilesSplitsOversizedOnLineBoundary(t *testing.T) {
    var sb strings.Builder
    for i := 0; i < 100; i++ {
        sb.WriteString("line ")
        sb.WriteString(strings.Repeat("x", 20))
        sb.WriteString("\n")
    }
    f := NewSourceFile("big.txt", []byte(sb.String()))
    got := ChunkSourceFiles([]SourceFile{f}, 200)
    if len(got) < 3 {
        t.Fatalf("oversized file produced %d chunks, want ≥3", len(got))
    }
    for i, c := range got {
        if !strings.Contains(c.Text, "=== big.txt") {
            t.Errorf("chunk %d missing big.txt header: %q", i, c.Text[:min(60, len(c.Text))])
        }
        if !strings.Contains(c.Text, "(lines ") {
            t.Errorf("chunk %d missing line-range annotation", i)
        }
    }
}

func TestChunkSourceFilesFileEqualsBudget(t *testing.T) {
    body := strings.Repeat("x", 80)
    f := NewSourceFile("a.md", []byte(body))
    got := ChunkSourceFiles([]SourceFile{f}, 100)
    if len(got) != 1 {
        t.Errorf("file just below budget → %d chunks, want 1", len(got))
    }
}

func chunkTexts(cs []Chunk) []string {
    out := make([]string, len(cs))
    for i, c := range cs {
        out[i] = c.Header
    }
    return out
}

func min(a, b int) int { if a < b { return a }; return b }
```

- [ ] **Step 2: Run tests, confirm failure**

Run: `go test ./internal/ingest/ -run ChunkSourceFiles -v`
Expected: FAIL — `Chunk`, `ChunkSourceFiles` undefined.

- [ ] **Step 3: Implement**

Create `internal/ingest/chunk.go`:

```go
package ingest

import (
    "fmt"
    "strings"
)

// Chunk is a single LLM-call payload made of one or more SourceFile excerpts.
type Chunk struct {
    Header string       // human-readable description for progress display
    Text   string       // payload sent to LLM, includes "=== path ===" file headers
    Files  []SourceFile // SourceFiles included (or partially included) in this chunk
}

// ChunkSourceFiles greedily bin-packs files into chunks under maxBytes.
// File boundaries are preserved with "=== path ===" headers. If a single file
// exceeds maxBytes, it is split on line boundaries; each split chunk's header
// includes "(lines a-b)" so the LLM and a human reviewer know which slice they
// are seeing. The validator still uses (quote, source_file) and computes line
// numbers within the original full file content — split annotations are
// advisory only.
func ChunkSourceFiles(files []SourceFile, maxBytes int) []Chunk {
    if len(files) == 0 {
        return nil
    }
    if maxBytes <= 0 {
        maxBytes = 16 * 1024
    }

    var out []Chunk
    var cur strings.Builder
    var curFiles []SourceFile

    flush := func() {
        if cur.Len() == 0 {
            return
        }
        text := cur.String()
        out = append(out, Chunk{
            Header: chunkHeaderFor(curFiles),
            Text:   text,
            Files:  curFiles,
        })
        cur.Reset()
        curFiles = nil
    }

    for _, f := range files {
        block := fmt.Sprintf("=== %s ===\n%s\n\n", f.RelativePath, f.Content)
        if len(block) <= maxBytes {
            // Fits whole; flush current if it would overflow.
            if cur.Len()+len(block) > maxBytes {
                flush()
            }
            cur.WriteString(block)
            curFiles = append(curFiles, f)
            continue
        }
        // Oversized — split on line boundaries.
        flush()
        for _, sub := range splitFileOnLineBoundaries(f, maxBytes) {
            out = append(out, sub)
        }
    }
    flush()
    return out
}

func chunkHeaderFor(files []SourceFile) string {
    if len(files) == 1 {
        return files[0].RelativePath
    }
    if len(files) == 0 {
        return "(empty)"
    }
    return fmt.Sprintf("%s + %d more", files[0].RelativePath, len(files)-1)
}

// splitFileOnLineBoundaries produces consecutive Chunks each containing one
// slice of f.Content. Headers carry "(lines a-b)" annotations.
func splitFileOnLineBoundaries(f SourceFile, maxBytes int) []Chunk {
    lines := strings.SplitAfter(f.Content, "\n") // keeps \n
    var out []Chunk

    var buf strings.Builder
    startLine := 1
    curLine := 1
    for _, ln := range lines {
        // Header overhead per chunk is bounded; account for a generous 64 bytes.
        if buf.Len()+len(ln)+64 > maxBytes && buf.Len() > 0 {
            endLine := curLine - 1
            if endLine < startLine {
                endLine = startLine
            }
            out = append(out, makeSplitChunk(f, buf.String(), startLine, endLine))
            buf.Reset()
            startLine = curLine
        }
        buf.WriteString(ln)
        curLine++
    }
    if buf.Len() > 0 {
        endLine := curLine - 1
        if endLine < startLine {
            endLine = startLine
        }
        out = append(out, makeSplitChunk(f, buf.String(), startLine, endLine))
    }
    return out
}

func makeSplitChunk(f SourceFile, body string, startLine, endLine int) Chunk {
    text := fmt.Sprintf("=== %s (lines %d-%d) ===\n%s\n\n", f.RelativePath, startLine, endLine, body)
    return Chunk{
        Header: fmt.Sprintf("%s (lines %d-%d)", f.RelativePath, startLine, endLine),
        Text:   text,
        Files:  []SourceFile{f}, // entire SourceFile is the validation anchor; line numbers are within the full Content
    }
}
```

- [ ] **Step 4: Run tests, confirm pass**

Run: `go test ./internal/ingest/ -v`
Expected: PASS — empty/single/multi/oversized cases green.

- [ ] **Step 5: Commit**

```bash
git add internal/ingest/chunk.go internal/ingest/chunk_test.go
git commit -m "feat(ingest): ChunkSourceFiles greedy bin-packer with line-boundary splits"
```

---

## Phase C — Local walker (and GitHub delegation)

### Task 5: `internal/ingest/local.go` — directory walker with skip rules

**Files:**
- Create: `internal/ingest/local.go`
- Delete: `internal/ingest/file.go`
- Create: `internal/ingest/local_test.go`
- Create: `internal/ingest/testdata/dirs/sample/` fixture tree
- Modify: `go.mod` (add `github.com/sabhiram/go-gitignore`)

- [ ] **Step 1: Build the testdata fixture**

Create the following layout under `internal/ingest/testdata/dirs/sample/`:

```
sample/
├── .git/HEAD                      # 1 byte; should be skipped (.git denylist)
├── .gitignore                     # contents: "ignored.txt\nbuild/\n"
├── README.md                      # "# Sample\n\nReadme body.\n"
├── src/main.go                    # "package main\n\nfunc main() {}\n"
├── vendor/foo.go                  # "package foo\n"  (should be skipped)
├── node_modules/foo.js            # "module.exports = 1;\n" (skipped)
├── package-lock.json              # "{}"  (skipped via lockfile pattern)
├── image.png                      # 4 bytes "\x89PNG"  (skipped via ext)
├── ignored.txt                    # "ignored\n"  (skipped via .gitignore when honored)
├── build/output.bin               # any bytes  (skipped via .gitignore dir)
├── huge.txt                       # 300 KB of text   (skipped via size cap when default 256KB)
└── nested/deep/file.md            # "# Deep\n"
```

Use small files; commit the directory verbatim.

- [ ] **Step 2: Write failing walker tests**

Create `internal/ingest/local_test.go`:

```go
package ingest

import (
    "path/filepath"
    "sort"
    "testing"
)

const sampleDir = "testdata/dirs/sample"

func walkPaths(files []SourceFile) []string {
    out := make([]string, len(files))
    for i, f := range files {
        out[i] = f.RelativePath
    }
    sort.Strings(out)
    return out
}

func TestReadLocalSingleFile(t *testing.T) {
    files, err := ReadLocal(filepath.Join(sampleDir, "README.md"), DefaultLocalOptions())
    if err != nil {
        t.Fatal(err)
    }
    if len(files) != 1 || files[0].RelativePath != "README.md" {
        t.Errorf("got %+v, want 1 file at README.md", walkPaths(files))
    }
}

func TestReadLocalDirectoryAppliesSkipRules(t *testing.T) {
    opts := DefaultLocalOptions()
    opts.MaxFileBytes = 256 * 1024
    files, err := ReadLocal(sampleDir, opts)
    if err != nil {
        t.Fatal(err)
    }
    got := walkPaths(files)

    // What we expect:
    // - README.md       kept
    // - src/main.go     kept
    // - nested/deep/file.md kept
    // skipped: .git/* (deny dir), vendor/* (deny dir), node_modules/* (deny dir),
    //          package-lock.json (deny file), image.png (deny ext),
    //          ignored.txt + build/output.bin (gitignore), huge.txt (size cap).
    want := []string{
        "README.md",
        "nested/deep/file.md",
        "src/main.go",
    }
    if !equalSorted(got, want) {
        t.Errorf("got %v\nwant %v", got, want)
    }
}

func TestReadLocalNoGitignoreRespectsExtraFiles(t *testing.T) {
    opts := DefaultLocalOptions()
    opts.RespectGitignore = false
    files, _ := ReadLocal(sampleDir, opts)
    got := walkPaths(files)
    found := false
    for _, p := range got {
        if p == "ignored.txt" {
            found = true
        }
    }
    if !found {
        t.Errorf("ignored.txt should appear when RespectGitignore=false; got %v", got)
    }
}

func TestReadLocalSizeCapSkips(t *testing.T) {
    opts := DefaultLocalOptions()
    opts.MaxFileBytes = 100 // huge.txt is ~300KB
    files, _ := ReadLocal(sampleDir, opts)
    for _, f := range files {
        if f.RelativePath == "huge.txt" {
            t.Errorf("huge.txt should be skipped under tiny MaxFileBytes")
        }
    }
}

func equalSorted(a, b []string) bool {
    if len(a) != len(b) {
        return false
    }
    for i := range a {
        if a[i] != b[i] {
            return false
        }
    }
    return true
}
```

- [ ] **Step 3: Run test, confirm failure**

Run: `go test ./internal/ingest/ -run ReadLocal -v`
Expected: FAIL — `ReadLocal` (new shape), `LocalOptions`, `DefaultLocalOptions` undefined; old `ReadLocal` returns `string`, not `[]SourceFile`.

- [ ] **Step 4: Add `sabhiram/go-gitignore` to `go.mod`**

```bash
go get github.com/sabhiram/go-gitignore
```

- [ ] **Step 5: Implement `local.go`, delete `file.go`**

Delete `internal/ingest/file.go`. Create `internal/ingest/local.go`:

```go
package ingest

import (
    "fmt"
    "io/fs"
    "os"
    "path/filepath"
    "strings"

    gitignore "github.com/sabhiram/go-gitignore"
)

// Built-in directory denylist — never recurse into these.
var denyDirs = map[string]bool{
    ".git": true, "node_modules": true, "vendor": true, "target": true,
    "dist": true, "build": true, ".venv": true, "venv": true,
    "__pycache__": true, ".cache": true, ".next": true,
    "coverage": true, ".pytest_cache": true,
}

// Built-in extension denylist — skip these even if "looks like text".
var denyExt = map[string]bool{
    ".lock": true, ".min.js": true, ".min.css": true, ".map": true,
    ".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true,
    ".ico": true, ".zip": true, ".tar": true, ".gz": true,
    ".exe": true, ".dll": true, ".so": true, ".dylib": true,
    ".wasm": true, ".class": true, ".jar": true,
}

// Built-in file-name denylist (exact match on basename).
var denyBasenames = map[string]bool{
    "package-lock.json": true,
    "yarn.lock":         true,
    "Cargo.lock":        true,
    "go.sum":            true, // opt-in via ExtraTextExtensions if desired
}

type LocalOptions struct {
    MaxFileBytes        int64
    RespectGitignore    bool
    ExtraSkipGlobs      []string
    ExtraTextExtensions []string
    IncludeOnly         []string // if non-empty, only files matching these extensions
}

func DefaultLocalOptions() LocalOptions {
    return LocalOptions{
        MaxFileBytes:     256 * 1024,
        RespectGitignore: true,
    }
}

// ReadLocal reads a single file or recursively walks a directory, applying
// skip rules. PDFs encountered (by extension or %PDF magic) are dispatched
// to ReadPDF and contribute one SourceFile per page.
func ReadLocal(path string, opts LocalOptions) ([]SourceFile, error) {
    info, err := os.Stat(path)
    if err != nil {
        return nil, fmt.Errorf("stat %s: %w", path, err)
    }
    if !info.IsDir() {
        return readOne(path, filepath.Base(path))
    }
    return walkDirectory(path, opts)
}

func readOne(path, relPath string) ([]SourceFile, error) {
    // PDF dispatch.
    if strings.EqualFold(filepath.Ext(path), ".pdf") || hasPDFMagic(path) {
        return ReadPDF(path)
    }
    data, err := os.ReadFile(path)
    if err != nil {
        return nil, fmt.Errorf("read %s: %w", path, err)
    }
    if !isText(data) {
        return nil, nil
    }
    return []SourceFile{NewSourceFile(relPath, data)}, nil
}

func hasPDFMagic(path string) bool {
    f, err := os.Open(path)
    if err != nil {
        return false
    }
    defer f.Close()
    var head [4]byte
    n, _ := f.Read(head[:])
    return n >= 4 && string(head[:4]) == "%PDF"
}

func walkDirectory(root string, opts LocalOptions) ([]SourceFile, error) {
    var ig *gitignore.GitIgnore
    if opts.RespectGitignore {
        if data, err := os.ReadFile(filepath.Join(root, ".gitignore")); err == nil {
            ig = gitignore.CompileIgnoreLines(strings.Split(string(data), "\n")...)
        }
    }

    var out []SourceFile
    err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
        if walkErr != nil {
            return nil // tolerate stat errors mid-walk
        }
        rel, _ := filepath.Rel(root, path)
        if rel == "." {
            return nil
        }
        // Normalize to forward slashes for matching consistency.
        relSlash := filepath.ToSlash(rel)

        if d.IsDir() {
            base := d.Name()
            if denyDirs[base] {
                return filepath.SkipDir
            }
            if ig != nil && ig.MatchesPath(relSlash+"/") {
                return filepath.SkipDir
            }
            return nil
        }

        base := d.Name()
        ext := strings.ToLower(filepath.Ext(base))
        if denyBasenames[base] || denyExt[ext] {
            return nil
        }
        if ig != nil && ig.MatchesPath(relSlash) {
            return nil
        }
        for _, glob := range opts.ExtraSkipGlobs {
            if matched, _ := filepath.Match(glob, base); matched {
                return nil
            }
        }
        if len(opts.IncludeOnly) > 0 {
            ok := false
            for _, want := range opts.IncludeOnly {
                if strings.EqualFold(want, ext) {
                    ok = true
                    break
                }
            }
            if !ok {
                return nil
            }
        }

        info, err := d.Info()
        if err != nil {
            return nil
        }
        if opts.MaxFileBytes > 0 && info.Size() > opts.MaxFileBytes {
            fmt.Fprintf(os.Stderr, "  WARN skipping %s: %d > max_file_bytes %d\n", relSlash, info.Size(), opts.MaxFileBytes)
            return nil
        }

        // PDF dispatch by extension or magic — pages become individual SourceFiles.
        if ext == ".pdf" || hasPDFMagic(path) {
            pdfFiles, err := ReadPDF(path)
            if err != nil {
                fmt.Fprintf(os.Stderr, "  WARN PDF read failed for %s: %v\n", relSlash, err)
                return nil
            }
            // Prefix page paths with the file's relative path so quotes attribute
            // to "docs/paper.pdf#page-3" not just "page-3".
            for i := range pdfFiles {
                pdfFiles[i].RelativePath = relSlash + "#" + pdfFiles[i].RelativePath
            }
            out = append(out, pdfFiles...)
            return nil
        }

        data, err := os.ReadFile(path)
        if err != nil {
            return nil
        }
        if !isText(data) {
            return nil
        }
        out = append(out, NewSourceFile(relSlash, data))
        return nil
    })
    if err != nil {
        return nil, err
    }
    return out, nil
}

// isText is the existing 512-byte null-byte heuristic — keep as last-resort.
func isText(data []byte) bool {
    if len(data) == 0 {
        return true
    }
    check := data
    if len(check) > 512 {
        check = check[:512]
    }
    for _, b := range check {
        if b == 0 {
            return false
        }
    }
    return true
}
```

Note: `ReadPDF` is referenced but not yet implemented — Task 7 fills it in. To allow tests to pass before that, add a temporary stub:

```go
// Temporary stub for compilation until Task 7. ReadPDF is implemented in pdf.go.
```

Actually, to keep TDD honest: the local-walker tests use no PDFs, so removing PDF references from `local.go` for this task is cleanest. **Replace** the two PDF dispatch blocks above with a TODO comment (`// TODO Task 7: dispatch to ReadPDF`) that simply skips `.pdf` files for now. Re-introduce PDF dispatch in Task 7 when `pdf.go` lands.

- [ ] **Step 6: Run tests, confirm pass**

Run: `go test ./internal/ingest/ -v`
Expected: PASS — all walker subtests green; chunker + types tests still green.

- [ ] **Step 7: Commit**

```bash
git add internal/ingest/local.go internal/ingest/local_test.go internal/ingest/testdata/dirs/sample/ go.mod go.sum
git rm internal/ingest/file.go
git commit -m "feat(ingest): directory walker with deny list, gitignore, size cap; returns []SourceFile"
```

---

### Task 6: GitHub repo ingest — clone then delegate

**Files:**
- Modify: `internal/ingest/github.go`

- [ ] **Step 1: Read the new walker contract and adjust**

There is no easy unit test for `FetchGitHub` (network/exec dependency). Skip the failing-test step for this task; the walker tests already exercise the post-clone path. Verification is the cassette test in Task 17 plus manual smoke in Task 22.

- [ ] **Step 2: Rewrite `github.go`**

Replace the body of `FetchGitHub`:

```go
package ingest

import (
    "fmt"
    "os"
    "os/exec"
    "strings"
)

func FetchGitHub(repoURL string, opts LocalOptions) ([]SourceFile, error) {
    tmpDir, err := os.MkdirTemp("", "llmwiki-github-*")
    if err != nil {
        return nil, fmt.Errorf("creating temp dir: %w", err)
    }
    defer os.RemoveAll(tmpDir)

    cmd := exec.Command("git", "clone", "--depth", "1", "--filter=blob:none", repoURL, tmpDir)
    cmd.Stderr = os.Stderr
    if err := cmd.Run(); err != nil {
        return nil, fmt.Errorf("git clone %s: %w", repoURL, err)
    }
    return ReadLocal(tmpDir, opts)
}

func IsGitHubURL(s string) bool {
    return strings.Contains(s, "github.com") && !strings.HasSuffix(s, ".git") ||
        strings.HasSuffix(s, ".git")
}
```

The `docs/`-preference branch is gone; the unified walker handles it.

- [ ] **Step 3: Run package tests for compilation**

Run: `go build ./...`
Expected: PASS — but `cmd/ingest.go` and any other caller of the old `FetchGitHub`/`ReadLocal`/`FetchURL` shapes are now broken. That's expected. Tasks 8 (URL) and 12 (ingest pipeline) re-wire them.

The repo will not build cleanly until Task 12. Hold the commit until then? **No** — make the build green by also temporarily stubbing the old `cmd/ingest.go` calls in this same task. The cleanest move is to defer this commit until Phase G when `cmd/ingest.go` is rewritten. But the spec wants commits per task.

Resolution: in this task, commit `github.go` only. Add a `//go:build legacy` build tag temporarily? Cleaner: in this commit also adjust `cmd/ingest.go`'s three calls to compile against the new signatures returning `[]SourceFile` — concatenate `.Content` back into a single string for now to preserve old behavior. Phase G will rewrite the orchestration properly.

Add this throw-away adapter in `cmd/ingest.go` (replaces only the three calls, lines ~38–47):

```go
// Temporary glue: until Phase G, flatten []SourceFile back to a single string
// so the rest of cmd/ingest.go keeps compiling.
var sourceFiles []ingest.SourceFile
switch {
case ingest.IsGitHubURL(source):
    fmt.Printf("Cloning GitHub repo %s...\n", source)
    sourceFiles, err = ingest.FetchGitHub(source, ingest.DefaultLocalOptions())
case strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://"):
    fmt.Printf("Fetching URL %s...\n", source)
    sourceFiles, err = ingest.FetchURL(source, ingest.DefaultURLOptions())
default:
    fmt.Printf("Reading local path %s...\n", source)
    sourceFiles, err = ingest.ReadLocal(source, ingest.DefaultLocalOptions())
}
if err != nil {
    return fmt.Errorf("reading source: %w", err)
}
var sb strings.Builder
for _, f := range sourceFiles {
    fmt.Fprintf(&sb, "=== %s ===\n%s\n\n", f.RelativePath, f.Content)
}
content := sb.String()
```

This adapter is removed in Task 11. `FetchURL` may not yet exist with the new signature — leave the URL branch broken in this task, or add a minimal wrapper around the old `FetchURL` returning a single `SourceFile`. The simplest: keep the URL path on the OLD signature for now (`content, err = ingest.FetchURL(source)` followed by wrapping in `SourceFile{}`). Phase E (Task 8) rewires it.

- [ ] **Step 4: Build green**

Run: `go build ./... && go test ./internal/ingest/...`
Expected: build green, ingest tests green.

- [ ] **Step 5: Commit**

```bash
git add internal/ingest/github.go cmd/ingest.go
git commit -m "feat(ingest): GitHub fetch delegates to unified directory walker"
```

---

## Phase D — PDF

### Task 7: `internal/ingest/pdf.go` — per-page text extraction with scanned-page heuristic

**Files:**
- Modify: `go.mod` (add `github.com/ledongthuc/pdf`)
- Create: `internal/ingest/pdf.go`
- Create: `internal/ingest/pdf_test.go`
- Create: `internal/ingest/testdata/pdfs/simple.pdf`
- Create: `internal/ingest/testdata/pdfs/scanned.pdf`

- [ ] **Step 1: Generate fixture PDFs**

Generate fixtures using `pandoc` (or any handy tool) and commit them:

- `simple.pdf`: 2 pages of plain text. Page 1 contains "alpha beta gamma", page 2 contains "delta epsilon zeta". Aim for ≥80 chars per page so the heuristic does not kick in.
- `scanned.pdf`: 1 page that is purely an embedded raster image (no text layer). Easiest path: take a small PNG, place it on a page via `pandoc --pdf-engine=xelatex` with no caption, or use any "image to PDF" tool.

Commit both under `internal/ingest/testdata/pdfs/`.

- [ ] **Step 2: Add the dependency**

```bash
go get github.com/ledongthuc/pdf
```

- [ ] **Step 3: Write failing PDF tests**

Create `internal/ingest/pdf_test.go`:

```go
package ingest

import (
    "strings"
    "testing"
)

func TestReadPDFTextual(t *testing.T) {
    files, err := ReadPDF("testdata/pdfs/simple.pdf")
    if err != nil {
        t.Fatalf("ReadPDF: %v", err)
    }
    if len(files) != 2 {
        t.Fatalf("got %d pages, want 2", len(files))
    }
    if files[0].RelativePath != "page-1" || files[1].RelativePath != "page-2" {
        t.Errorf("page paths = %q, %q", files[0].RelativePath, files[1].RelativePath)
    }
    if !strings.Contains(strings.ToLower(files[0].Content), "alpha") {
        t.Errorf("page 1 missing 'alpha': %q", files[0].Content)
    }
}

func TestReadPDFScannedSkipsAndErrorsIfAllSkipped(t *testing.T) {
    _, err := ReadPDF("testdata/pdfs/scanned.pdf")
    if err == nil {
        t.Error("expected error for fully scanned PDF")
    }
    if !strings.Contains(err.Error(), "scanned") {
        t.Errorf("error message should mention scanned: %v", err)
    }
}
```

- [ ] **Step 4: Run test, confirm failure**

Run: `go test ./internal/ingest/ -run ReadPDF -v`
Expected: FAIL — `ReadPDF` undefined.

- [ ] **Step 5: Implement**

Create `internal/ingest/pdf.go`:

```go
package ingest

import (
    "fmt"
    "os"
    "strings"

    "github.com/ledongthuc/pdf"
)

// PDFMinTextPerPage is the minimum extracted text length below which a page is
// treated as scanned/OCR-only and skipped with a warning. Override via config.
const PDFMinTextPerPage = 50

// ReadPDF extracts text per page using ledongthuc/pdf's GetTextByRow API.
// Each page becomes one SourceFile with RelativePath "page-N". Pages with
// fewer than PDFMinTextPerPage characters of extractable text (likely scanned
// images) are skipped with a warning. If every page is skipped, returns an
// error explaining the PDF is likely scanned/OCR-only.
func ReadPDF(path string) ([]SourceFile, error) {
    f, r, err := pdf.Open(path)
    if err != nil {
        return nil, fmt.Errorf("open pdf %s: %w", path, err)
    }
    defer f.Close()

    n := r.NumPage()
    var out []SourceFile
    for i := 1; i <= n; i++ {
        page := r.Page(i)
        if page.V.IsNull() {
            continue
        }
        rows, err := page.GetTextByRow()
        if err != nil {
            fmt.Fprintf(os.Stderr, "  WARN page %d of %s: text extraction error: %v\n", i, path, err)
            continue
        }
        var sb strings.Builder
        for _, row := range rows {
            for _, w := range row.Content {
                sb.WriteString(w.S)
            }
            sb.WriteString("\n")
        }
        text := strings.TrimSpace(sb.String())
        if len(text) < PDFMinTextPerPage {
            fmt.Fprintf(os.Stderr, "  WARN page %d of %s: appears scanned/OCR-only (%d chars), skipping\n", i, path, len(text))
            continue
        }
        out = append(out, NewSourceFile(fmt.Sprintf("page-%d", i), []byte(text)))
    }
    if len(out) == 0 {
        return nil, fmt.Errorf("no extractable text in PDF (likely scanned): %s", path)
    }
    return out, nil
}
```

- [ ] **Step 6: Re-enable PDF dispatch in `local.go`**

Replace the `// TODO Task 7` placeholders left in `local.go` with the actual PDF dispatch (the two blocks shown in Task 5 Step 5). Both `readOne` and `walkDirectory` call `ReadPDF` and prefix page paths.

- [ ] **Step 7: Run tests, confirm pass**

Run: `go test ./internal/ingest/ -v`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/ingest/pdf.go internal/ingest/pdf_test.go internal/ingest/local.go internal/ingest/testdata/pdfs/ go.mod go.sum
git commit -m "feat(ingest): per-page PDF extraction with scanned-page heuristic"
```

---

## Phase E — URL

### Task 8: URL ingest rewrite — sniffing, Readability + HTML→MD, PDF route

**Files:**
- Modify: `go.mod` (add `JohannesKaufmann/html-to-markdown/v2`, `go-shiori/go-readability`)
- Modify: `internal/ingest/url.go`
- Create: `internal/ingest/url_test.go`

- [ ] **Step 1: Add dependencies**

```bash
go get github.com/JohannesKaufmann/html-to-markdown/v2
go get github.com/go-shiori/go-readability
```

- [ ] **Step 2: Write failing tests via `httptest.Server`**

Create `internal/ingest/url_test.go`:

```go
package ingest

import (
    "net/http"
    "net/http/httptest"
    "os"
    "strings"
    "testing"
)

func TestFetchURLHTMLArticleExtraction(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Content-Type", "text/html; charset=utf-8")
        w.Write([]byte(`<html><head><title>Hello</title></head><body>
<nav>navvy navvy</nav>
<header>headery</header>
<main><article>
<h1>Real Title</h1>
<p>Body paragraph that should survive readability.</p>
</article></main>
<footer>footery</footer>
<script>var noise = 1;</script>
</body></html>`))
    }))
    defer srv.Close()

    files, err := FetchURL(srv.URL, DefaultURLOptions())
    if err != nil {
        t.Fatalf("FetchURL: %v", err)
    }
    if len(files) != 1 {
        t.Fatalf("got %d files, want 1", len(files))
    }
    body := files[0].Content
    if !strings.Contains(body, "Body paragraph") {
        t.Errorf("article body missing: %q", body)
    }
    for _, noise := range []string{"navvy navvy", "headery", "footery", "var noise"} {
        if strings.Contains(body, noise) {
            t.Errorf("noise %q leaked into article: %q", noise, body)
        }
    }
}

func TestFetchURLPDFRoute(t *testing.T) {
    pdfBytes, err := os.ReadFile("testdata/pdfs/simple.pdf")
    if err != nil {
        t.Fatal(err)
    }
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Content-Type", "application/pdf")
        w.Write(pdfBytes)
    }))
    defer srv.Close()

    files, err := FetchURL(srv.URL, DefaultURLOptions())
    if err != nil {
        t.Fatalf("FetchURL: %v", err)
    }
    if len(files) < 1 {
        t.Fatal("got 0 pages from PDF URL")
    }
    if !strings.HasPrefix(files[0].RelativePath, "page-") {
        t.Errorf("expected page-N relative path, got %q", files[0].RelativePath)
    }
}

func TestFetchURLPlainText(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Content-Type", "text/plain; charset=utf-8")
        w.Write([]byte("just a plain note\n"))
    }))
    defer srv.Close()
    files, err := FetchURL(srv.URL, DefaultURLOptions())
    if err != nil {
        t.Fatal(err)
    }
    if len(files) != 1 || !strings.Contains(files[0].Content, "just a plain note") {
        t.Errorf("got %+v", files)
    }
}

func TestFetchURL5xxErrors(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(503)
    }))
    defer srv.Close()
    if _, err := FetchURL(srv.URL, DefaultURLOptions()); err == nil {
        t.Error("expected error for 503")
    }
}

func TestFetchURLBodyLimitTruncates(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Content-Type", "text/plain")
        w.Write(make([]byte, 10*1024*1024)) // 10 MB
    }))
    defer srv.Close()
    opts := DefaultURLOptions()
    opts.MaxBodyBytes = 5 * 1024 * 1024
    files, err := FetchURL(srv.URL, opts)
    if err != nil {
        // Some implementations return an error on overflow; both are acceptable.
        return
    }
    if files[0].ByteSize > opts.MaxBodyBytes {
        t.Errorf("body not capped: got %d bytes", files[0].ByteSize)
    }
}

func TestFetchURLUnsupportedContentType(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Content-Type", "application/octet-stream")
        w.Write([]byte{0, 1, 2})
    }))
    defer srv.Close()
    if _, err := FetchURL(srv.URL, DefaultURLOptions()); err == nil {
        t.Error("expected error for unsupported content-type")
    }
}
```

- [ ] **Step 3: Run tests, confirm failure**

Run: `go test ./internal/ingest/ -run FetchURL -v`
Expected: FAIL — `URLOptions`, `DefaultURLOptions`, new `FetchURL` signature undefined.

- [ ] **Step 4: Implement**

Replace `internal/ingest/url.go`:

```go
package ingest

import (
    "bytes"
    "fmt"
    "io"
    "net/http"
    "net/url"
    "os"
    "path/filepath"
    "strings"
    "time"

    htmltomd "github.com/JohannesKaufmann/html-to-markdown/v2"
    "github.com/go-shiori/go-readability"
)

const userAgentVersion = "0.3"

type URLOptions struct {
    Timeout      time.Duration
    MaxBodyBytes int64
}

func DefaultURLOptions() URLOptions {
    return URLOptions{
        Timeout:      30 * time.Second,
        MaxBodyBytes: 5 * 1024 * 1024,
    }
}

// FetchURL fetches a URL and dispatches by content-type:
//   application/pdf       -> ReadPDF on a temp file, returns one SourceFile per page
//   text/html, xhtml      -> Readability + html-to-markdown, returns one "index.html" SourceFile
//   text/plain, text/md   -> raw passthrough as one SourceFile
//   anything else         -> error
func FetchURL(rawURL string, opts URLOptions) ([]SourceFile, error) {
    if opts.Timeout == 0 {
        opts.Timeout = 30 * time.Second
    }
    if opts.MaxBodyBytes == 0 {
        opts.MaxBodyBytes = 5 * 1024 * 1024
    }

    client := &http.Client{Timeout: opts.Timeout}
    req, err := http.NewRequest("GET", rawURL, nil)
    if err != nil {
        return nil, fmt.Errorf("building request: %w", err)
    }
    req.Header.Set("User-Agent", "llmwiki/"+userAgentVersion)

    resp, err := client.Do(req)
    if err != nil {
        return nil, fmt.Errorf("fetching %s: %w", rawURL, err)
    }
    defer resp.Body.Close()
    if resp.StatusCode >= 400 {
        return nil, fmt.Errorf("HTTP %d fetching %s", resp.StatusCode, rawURL)
    }

    body, err := io.ReadAll(io.LimitReader(resp.Body, opts.MaxBodyBytes))
    if err != nil {
        return nil, err
    }

    ct := strings.SplitN(resp.Header.Get("Content-Type"), ";", 2)[0]
    ct = strings.TrimSpace(strings.ToLower(ct))
    parsed, _ := url.Parse(rawURL)
    extLower := ""
    if parsed != nil {
        extLower = strings.ToLower(filepath.Ext(parsed.Path))
    }

    switch {
    case ct == "application/pdf" || extLower == ".pdf":
        return fetchPDFViaTempFile(body)
    case ct == "text/html", ct == "application/xhtml+xml":
        return fetchHTMLAsMarkdown(body, rawURL)
    case strings.HasPrefix(ct, "text/"):
        return []SourceFile{NewSourceFile("body.txt", body)}, nil
    default:
        return nil, fmt.Errorf("unsupported content-type %q for URL ingestion", ct)
    }
}

func fetchPDFViaTempFile(body []byte) ([]SourceFile, error) {
    tmp, err := os.CreateTemp("", "llmwiki-url-*.pdf")
    if err != nil {
        return nil, err
    }
    defer os.Remove(tmp.Name())
    if _, err := tmp.Write(body); err != nil {
        return nil, err
    }
    tmp.Close()
    return ReadPDF(tmp.Name())
}

func fetchHTMLAsMarkdown(body []byte, srcURL string) ([]SourceFile, error) {
    parsed, _ := url.Parse(srcURL)
    article, err := readability.FromReader(bytes.NewReader(body), parsed)
    var html string
    if err == nil && strings.TrimSpace(article.Content) != "" {
        html = article.Content
    } else {
        // Fallback: pass full body through html-to-markdown which strips
        // <script>/<style>/<nav>/<footer>/<aside>/<header> by default rules.
        html = string(body)
    }
    md, err := htmltomd.ConvertString(html)
    if err != nil {
        return nil, fmt.Errorf("html→markdown: %w", err)
    }
    return []SourceFile{NewSourceFile("index.html", []byte(md))}, nil
}
```

> **Library shape note for the implementer:** `JohannesKaufmann/html-to-markdown/v2` exposes `ConvertString(html string) (string, error)` as of its v2 line; if the actual current API differs (e.g. requires a `Converter` builder), substitute the canonical call. Same for `go-shiori/go-readability`'s `FromReader(r io.Reader, u *url.URL) (Article, error)` — the function lives in `readability` package and returns a struct with a `.Content` HTML string. Verify against the upstream README before implementing; if the API has shifted, adapt the calls but keep the same input/output contract.

- [ ] **Step 5: Run tests, confirm pass**

Run: `go test ./internal/ingest/ -v`
Expected: PASS — HTML extraction strips nav/footer/script, PDF URL dispatches, text passthrough works, 5xx errors, body-limit applied, unsupported content-type errors.

- [ ] **Step 6: Update the temporary glue in `cmd/ingest.go`**

The Task 6 adapter passed `ingest.DefaultURLOptions()` to `FetchURL` already; nothing more is needed for compilation. Run `go build ./...` to confirm.

- [ ] **Step 7: Commit**

```bash
git add internal/ingest/url.go internal/ingest/url_test.go go.mod go.sum
git commit -m "feat(ingest): URL fetch with content-type sniffing, Readability + html-to-markdown, PDF route"
```

---

## Phase F — Validator + tool schema

### Task 9: `Evidence.SourceFilePath` field + frontmatter round-trip

**Files:**
- Modify: `internal/wiki/page.go`
- Modify: `internal/wiki/page_test.go`

- [ ] **Step 1: Write failing round-trip test**

Append to `internal/wiki/page_test.go`:

```go
func TestEvidenceSourceFilePathRoundTrip(t *testing.T) {
    dir := t.TempDir()
    p := Page{
        Title:     "T",
        Body:      "b",
        UpdatedAt: time.Now().UTC(),
        Evidence: []Evidence{
            {Quote: "q1", LineStart: 1, LineEnd: 2, SourceFilePath: "internal/db/db.go"},
            {Quote: "q2", LineStart: 3, LineEnd: 3, SourceFilePath: "page-4"},
        },
    }
    if err := WritePage(p, dir); err != nil {
        t.Fatal(err)
    }
    got, err := ReadPage(filepath.Join(dir, "T.md"))
    if err != nil {
        t.Fatal(err)
    }
    if len(got.Evidence) != 2 {
        t.Fatalf("got %d evidence rows, want 2", len(got.Evidence))
    }
    if got.Evidence[0].SourceFilePath != "internal/db/db.go" {
        t.Errorf("ev[0].SourceFilePath = %q", got.Evidence[0].SourceFilePath)
    }
    if got.Evidence[1].SourceFilePath != "page-4" {
        t.Errorf("ev[1].SourceFilePath = %q", got.Evidence[1].SourceFilePath)
    }
}
```

- [ ] **Step 2: Run test, confirm failure**

Run: `go test ./internal/wiki/ -run TestEvidenceSourceFilePath -v`
Expected: FAIL — `Evidence.SourceFilePath` field missing.

- [ ] **Step 3: Add field, update `WritePage`/`ParsePage`**

In `internal/wiki/page.go`, extend `Evidence`:

```go
type Evidence struct {
    Quote          string
    LineStart      int
    LineEnd        int
    SourceFilePath string
}
```

In `WritePage`, inside the evidence loop, after writing `line_end`, emit:

```go
if e.SourceFilePath != "" {
    sb.WriteString(fmt.Sprintf("    source_file: %s\n", yamlEscapeScalar(e.SourceFilePath)))
}
```

Add `yamlEscapeScalar` helper (quotes only when needed):

```go
func yamlEscapeScalar(s string) string {
    if s == "" || strings.ContainsAny(s, ":#[]{},&*!|>'\"%@`\n") {
        esc := strings.ReplaceAll(s, `\`, `\\`)
        esc = strings.ReplaceAll(esc, `"`, `\"`)
        esc = strings.ReplaceAll(esc, "\n", `\n`)
        return `"` + esc + `"`
    }
    return s
}
```

In `ParsePage`'s evidence-state branch, add:

```go
case inEvidence && strings.HasPrefix(line, "    source_file: "):
    raw := strings.TrimSpace(line[len("    source_file: "):])
    if strings.HasPrefix(raw, `"`) && strings.HasSuffix(raw, `"`) {
        curEv.SourceFilePath = unescapeQuote(raw)
    } else {
        curEv.SourceFilePath = raw
    }
```

- [ ] **Step 4: Run tests, confirm pass**

Run: `go test ./internal/wiki/ -v`
Expected: PASS — round-trip green; existing `TestParsePageBackwardCompatible` still green (missing `source_file` is fine).

- [ ] **Step 5: Commit**

```bash
git add internal/wiki/page.go internal/wiki/page_test.go
git commit -m "feat(wiki): Evidence.SourceFilePath field with frontmatter round-trip"
```

---

### Task 10: Tool schema + `ValidateAndAttachEvidence([]Page, []SourceFile)`

**Files:**
- Modify: `internal/wiki/ops.go`
- Modify: `internal/wiki/ops_test.go`

- [ ] **Step 1: Write failing tests for the new validator signature**

Replace `TestValidateAndAttachEvidence` in `internal/wiki/ops_test.go` with the new shape, and add a fallback test:

```go
import (
    "github.com/mritunjaysharma394/llmwiki/internal/ingest"
)

func TestValidateAndAttachEvidencePerFile(t *testing.T) {
    files := []ingest.SourceFile{
        ingest.NewSourceFile("a.md", []byte("alpha line\nbeta line\n")),
        ingest.NewSourceFile("b.md", []byte("gamma\ndelta\n")),
    }
    pages := []Page{
        {
            Title: "Found-correctly",
            Body:  "x",
            Evidence: []Evidence{
                {Quote: "alpha line", SourceFilePath: "a.md"},
                {Quote: "delta", SourceFilePath: "b.md"},
            },
        },
        {
            Title: "Wrong-file",
            Body:  "x",
            Evidence: []Evidence{{Quote: "alpha line", SourceFilePath: "b.md"}}, // not in b.md
        },
        {
            Title: "Unknown-file",
            Body:  "x",
            Evidence: []Evidence{{Quote: "alpha line", SourceFilePath: "z.md"}},
        },
    }
    kept, dropped := ValidateAndAttachEvidence(pages, files)
    if len(kept) != 1 || kept[0].Title != "Found-correctly" {
        t.Fatalf("kept = %v", pageTitles(kept))
    }
    if dropped != 2 {
        t.Errorf("dropped = %d, want 2", dropped)
    }
    e0 := kept[0].Evidence[0]
    if e0.SourceFilePath != "a.md" || e0.LineStart != 1 || e0.LineEnd != 1 {
        t.Errorf("ev[0] = %+v", e0)
    }
    e1 := kept[0].Evidence[1]
    if e1.SourceFilePath != "b.md" || e1.LineStart != 2 {
        t.Errorf("ev[1] = %+v", e1)
    }
}

func TestValidateAndAttachEvidenceFallbackWhenSourceFileMissing(t *testing.T) {
    files := []ingest.SourceFile{
        ingest.NewSourceFile("only.md", []byte("the answer is 42\n")),
    }
    pages := []Page{{
        Title:    "T",
        Body:     "b",
        Evidence: []Evidence{{Quote: "the answer is 42"}}, // no SourceFilePath
    }}
    kept, _ := ValidateAndAttachEvidence(pages, files)
    if len(kept) != 1 {
        t.Fatal("page dropped")
    }
    if kept[0].Evidence[0].SourceFilePath != "only.md" {
        t.Errorf("fallback didn't attribute: %+v", kept[0].Evidence[0])
    }
}
```

Existing `TestValidateAndAttachEvidence`, `TestValidateAndAttachEvidenceMultilineQuote`, `TestValidateAndAttachEvidenceUnicode` were written against `(pages, source string)` — update them to wrap their source string in a single-element `[]ingest.SourceFile{ingest.NewSourceFile("doc", []byte(source))}` and call the new signature.

- [ ] **Step 2: Run tests, confirm failure**

Run: `go test ./internal/wiki/ -v`
Expected: FAIL — signature mismatch.

- [ ] **Step 3: Update tool schema, system prompt, validator**

In `internal/wiki/ops.go`:

1. Tool schema — add `source_file` property and require it:

```go
"evidence": map[string]any{
    "type":        "array",
    "description": "Verbatim quotes copied character-for-character from SOURCE. At least one required per page.",
    "items": map[string]any{
        "type": "object",
        "properties": map[string]any{
            "quote":       map[string]any{"type": "string", "description": "Verbatim substring of the named source_file's content"},
            "source_file": map[string]any{"type": "string", "description": "Exact path shown in the === path === marker above the file the quote was copied from"},
            "explanation": map[string]any{"type": "string"},
        },
        "required": []string{"quote", "source_file"},
    },
},
```

2. System prompt addendum — replace `ingestSystemPrompt`:

```go
const ingestSystemPrompt = `You write wiki pages strictly grounded in the SOURCE provided.

The SOURCE may contain multiple files, each delimited by a header line:
    === path/to/file.ext ===
For every evidence quote, set "source_file" to the exact path shown in the
header above the file the quote was copied from. Quotes from different files
must each have their own evidence entry naming the correct file.

RULES:
1. Every page MUST include "evidence" — verbatim spans copied character-for-character from one of the files in SOURCE that justify the page's claims.
2. Each evidence entry MUST set "source_file" to the path from the "=== path ===" marker above its quote.
3. Do NOT include general knowledge that is not in SOURCE.
4. If SOURCE doesn't contain enough material for a high-quality page on a topic, do NOT create that page.
5. Better to return one solid page than five thin ones. Aim for 1-4 pages per call.
6. Page bodies should synthesize and organize, but every claim must be defensible from the evidence quotes you provide.
7. When linking pages, only reference existing pages or pages you are creating in this same call.`
```

3. New `IngestToPages` signature:

```go
func IngestToPages(ctx context.Context, client llm.Client, files []ingest.SourceFile, chunkText string, existingTitles []string) ([]Page, error) {
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
    sb.WriteString(chunkText)

    result, err := client.CompleteStructured(ctx, ingestSystemPrompt, sb.String(), writePagesTool)
    if err != nil {
        return nil, fmt.Errorf("llm structured call: %w", err)
    }
    pages, err := ExtractPagesFromToolResult(result)
    if err != nil {
        return nil, err
    }
    pages, _ = ValidateAndAttachEvidence(pages, files)
    now := time.Now().UTC()
    for i := range pages {
        pages[i].UpdatedAt = now
        pages[i].ContentHash = HashContent(pages[i].Body)
    }
    return pages, nil
}
```

4. New `ValidateAndAttachEvidence`:

```go
func ValidateAndAttachEvidence(pages []Page, files []ingest.SourceFile) ([]Page, int) {
    byPath := make(map[string]*ingest.SourceFile, len(files))
    for i := range files {
        byPath[files[i].RelativePath] = &files[i]
    }
    var kept []Page
    dropped := 0
    for _, p := range pages {
        var valid []Evidence
        for _, e := range p.Evidence {
            if e.Quote == "" {
                continue
            }
            file, attributedBy := lookupOrFallback(e, byPath, files)
            if file == nil {
                fmt.Fprintf(os.Stderr, "  WARN dropped quote in page %q: source_file %q not in this chunk\n", p.Title, e.SourceFilePath)
                continue
            }
            idx := strings.Index(file.Content, e.Quote)
            if idx < 0 {
                fmt.Fprintf(os.Stderr, "  WARN dropped quote in page %q: not present in %s\n", p.Title, file.RelativePath)
                continue
            }
            start, end := lineRange(file.Content, idx, len(e.Quote))
            e.LineStart = start
            e.LineEnd = end
            e.SourceFilePath = file.RelativePath
            if attributedBy == "fallback" {
                fmt.Fprintf(os.Stderr, "  WARN quote in page %q missing source_file, attributed to %s by content match\n", p.Title, file.RelativePath)
            }
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

// lookupOrFallback returns the named SourceFile if e.SourceFilePath is non-empty
// and known. If empty, scans all files for the quote (first match wins) and
// reports "fallback" so the caller can warn.
func lookupOrFallback(e Evidence, byPath map[string]*ingest.SourceFile, files []ingest.SourceFile) (*ingest.SourceFile, string) {
    if e.SourceFilePath != "" {
        if f, ok := byPath[e.SourceFilePath]; ok {
            return f, "named"
        }
        return nil, "named"
    }
    for i := range files {
        if strings.Contains(files[i].Content, e.Quote) {
            return &files[i], "fallback"
        }
    }
    return nil, "fallback"
}
```

5. Update `ExtractPagesFromToolResult` to capture `source_file`:

```go
if em, ok := er.(map[string]any); ok {
    q, _ := em["quote"].(string)
    sf, _ := em["source_file"].(string)
    if q != "" {
        p.Evidence = append(p.Evidence, Evidence{Quote: q, SourceFilePath: sf})
    }
}
```

6. Add `"github.com/mritunjaysharma394/llmwiki/internal/ingest"` to imports.

- [ ] **Step 4: Run tests, confirm pass**

Run: `go test ./internal/wiki/ -v`
Expected: PASS — per-file lookup works, fallback fires when `source_file` is empty.

- [ ] **Step 5: Commit**

```bash
git add internal/wiki/ops.go internal/wiki/ops_test.go
git commit -m "feat(wiki): per-file evidence validation; tool schema gains source_file; fallback when omitted"
```

---

## Phase G — Pipeline orchestration + per-file dedup

### Task 11: Rewrite `cmd/ingest.go` to consume `[]SourceFile`

**Files:**
- Modify: `cmd/ingest.go`
- Modify: `cmd/ingest_test.go`

- [ ] **Step 1: Write failing dedup-partition test**

Add to `cmd/ingest_test.go`:

```go
import (
    "github.com/mritunjaysharma394/llmwiki/internal/db"
    "github.com/mritunjaysharma394/llmwiki/internal/ingest"
)

func TestPartitionByFileHash(t *testing.T) {
    incoming := []ingest.SourceFile{
        ingest.NewSourceFile("unchanged.go", []byte("u\n")),
        ingest.NewSourceFile("changed.go", []byte("new\n")),
        ingest.NewSourceFile("new.go", []byte("n\n")),
    }
    existing := map[string]db.SourceFile{
        "unchanged.go": {RelativePath: "unchanged.go", ContentHash: incoming[0].ContentHash},
        "changed.go":   {RelativePath: "changed.go", ContentHash: "old"},
        "gone.go":      {RelativePath: "gone.go", ContentHash: "irrelevant"},
    }
    p := partitionByFileHash(incoming, existing)
    if len(p.unchanged) != 1 || p.unchanged[0].RelativePath != "unchanged.go" {
        t.Errorf("unchanged = %v", p.unchanged)
    }
    if len(p.changed) != 1 || p.changed[0].RelativePath != "changed.go" {
        t.Errorf("changed = %v", p.changed)
    }
    if len(p.newFiles) != 1 || p.newFiles[0].RelativePath != "new.go" {
        t.Errorf("new = %v", p.newFiles)
    }
    if len(p.gone) != 1 || p.gone[0].RelativePath != "gone.go" {
        t.Errorf("gone = %v", p.gone)
    }
}
```

The old `chunkContent`/`TestChunkContent*` and `TestSlugifyForArchive` tests must be migrated. `chunkContent` is dead — replace its tests with unused-import-clean stubs or delete them.

- [ ] **Step 2: Run test, confirm failure**

Run: `go test ./cmd/ -run TestPartitionByFileHash -v`
Expected: FAIL — `partitionByFileHash` undefined.

- [ ] **Step 3: Rewrite `cmd/ingest.go`**

Skeleton (full file is large; key shape):

```go
type filePartition struct {
    unchanged []ingest.SourceFile
    changed   []ingest.SourceFile
    newFiles  []ingest.SourceFile
    gone      []db.SourceFile
}

func partitionByFileHash(incoming []ingest.SourceFile, existing map[string]db.SourceFile) filePartition {
    var p filePartition
    seen := map[string]bool{}
    for _, f := range incoming {
        seen[f.RelativePath] = true
        ex, ok := existing[f.RelativePath]
        switch {
        case !ok:
            p.newFiles = append(p.newFiles, f)
        case ex.ContentHash == f.ContentHash:
            p.unchanged = append(p.unchanged, f)
        default:
            p.changed = append(p.changed, f)
        }
    }
    for path, ex := range existing {
        if !seen[path] {
            p.gone = append(p.gone, ex)
        }
    }
    return p
}

func runIngest(cmd *cobra.Command, args []string) error {
    source := args[0]
    ctx := cmd.Context()

    localOpts, urlOpts := buildIngestOptions(cmd, cfg)

    var sourceFiles []ingest.SourceFile
    var err error
    switch {
    case ingest.IsGitHubURL(source):
        fmt.Printf("Cloning GitHub repo %s...\n", source)
        sourceFiles, err = ingest.FetchGitHub(source, localOpts)
    case strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://"):
        fmt.Printf("Fetching URL %s...\n", source)
        sourceFiles, err = ingest.FetchURL(source, urlOpts)
    default:
        fmt.Printf("Reading local path %s...\n", source)
        sourceFiles, err = ingest.ReadLocal(source, localOpts)
    }
    if err != nil {
        return fmt.Errorf("reading source: %w", err)
    }
    if len(sourceFiles) == 0 {
        return fmt.Errorf("no content found in source")
    }

    wholeHash := computeWholeHash(sourceFiles)
    existingSrc, _ := database.GetSource(source)
    if existingSrc != nil && existingSrc.ContentHash == wholeHash && !forceFlag(cmd) {
        fmt.Println("Source unchanged, skipping.")
        return nil
    }

    sourceID, err := database.UpsertSource(source, wholeHash)
    if err != nil {
        return fmt.Errorf("recording source: %w", err)
    }

    existingFiles := map[string]db.SourceFile{}
    if rows, err := database.GetSourceFiles(sourceID); err == nil {
        for _, f := range rows {
            existingFiles[f.RelativePath] = f
        }
    }
    parts := partitionByFileHash(sourceFiles, existingFiles)

    if forceFlag(cmd) {
        // Treat everything as changed.
        parts.changed = append(parts.changed, parts.unchanged...)
        parts.unchanged = nil
    }

    fmt.Printf("Walking %s (%d files: %d new, %d changed, %d unchanged)\n",
        source, len(sourceFiles), len(parts.newFiles), len(parts.changed), len(parts.unchanged))

    // Reap files that disappeared.
    for _, gone := range parts.gone {
        fmt.Printf("  - removing %s (gone)\n", gone.RelativePath)
        database.DeleteSourceFile(gone.ID) // cascades evidence
    }

    toIngest := append([]ingest.SourceFile{}, parts.newFiles...)
    toIngest = append(toIngest, parts.changed...)
    if len(toIngest) == 0 {
        fmt.Println("Source unchanged at file level, skipping.")
        return nil
    }

    chunks := ingest.ChunkSourceFiles(toIngest, ingestChunkSize)
    if len(chunks) > 1 {
        fmt.Printf("  Packing into %d chunks (max %d in flight)\n", len(chunks), ingestMaxInflight)
    }

    titles, _ := database.AllPageTitles()

    type result struct {
        pages []wiki.Page
        files []ingest.SourceFile
        err   error
        idx   int
    }
    results := make([]result, len(chunks))
    sem := make(chan struct{}, ingestMaxInflight)
    var wg sync.WaitGroup
    var done int64
    for i, ch := range chunks {
        wg.Add(1)
        sem <- struct{}{}
        go func(i int, ch ingest.Chunk) {
            defer wg.Done()
            defer func() { <-sem }()
            got, err := wiki.IngestToPages(ctx, llmClient, ch.Files, ch.Text, titles)
            results[i] = result{pages: got, files: ch.Files, err: err, idx: i}
            n := atomic.AddInt64(&done, 1)
            fmt.Printf("\r  [%d/%d] processed", n, len(chunks))
        }(i, ch)
    }
    wg.Wait()
    if len(chunks) > 1 {
        fmt.Println()
    }

    // Write source_files rows for everything we ingested (changed reuses path
    // → upsert keeps id stable but updates hash).
    pathToFileID := map[string]int64{}
    for _, f := range toIngest {
        id, err := database.UpsertSourceFile(db.SourceFile{
            SourceID:     sourceID,
            RelativePath: f.RelativePath,
            ContentHash:  f.ContentHash,
            ByteSize:     f.ByteSize,
            LineCount:    f.LineCount,
        })
        if err != nil {
            return fmt.Errorf("upsert source_file %s: %w", f.RelativePath, err)
        }
        pathToFileID[f.RelativePath] = id
    }
    // For changed files, clear old evidence rows before inserting fresh ones.
    for _, f := range parts.changed {
        if id := pathToFileID[f.RelativePath]; id != 0 {
            database.DeleteEvidenceForSourceFile(id)
        }
    }

    if err := os.MkdirAll(cfg.Wiki.WikiDir, 0755); err != nil {
        return err
    }
    var allPages []wiki.Page
    for _, r := range results {
        if r.err != nil {
            fmt.Printf("  WARN chunk %d failed: %v\n", r.idx+1, r.err)
            continue
        }
        allPages = append(allPages, r.pages...)
    }
    if len(allPages) == 0 {
        fmt.Println("LLM produced no pages with verifiable evidence.")
        return nil
    }

    for i := range allPages {
        allPages[i].SourceIDs = []int64{sourceID}
        path := wiki.PagePath(cfg.Wiki.WikiDir, allPages[i].Title)
        if err := wiki.WritePage(allPages[i], cfg.Wiki.WikiDir); err != nil {
            return fmt.Errorf("writing page %q: %w", allPages[i].Title, err)
        }
        rec := db.PageRecord{
            Title:       allPages[i].Title,
            Path:        path,
            Body:        allPages[i].Body,
            ContentHash: allPages[i].ContentHash,
            SourceIDs:   allPages[i].SourceIDs,
        }
        if err := database.UpsertPage(rec); err != nil {
            return fmt.Errorf("db upsert %q: %w", allPages[i].Title, err)
        }
        stored, _ := database.GetPage(allPages[i].Title)
        var dbEv []db.Evidence
        for _, e := range allPages[i].Evidence {
            sfID := pathToFileID[e.SourceFilePath]
            var sfPtr *int64
            if sfID != 0 {
                v := sfID
                sfPtr = &v
            }
            dbEv = append(dbEv, db.Evidence{
                Quote:        e.Quote,
                LineStart:    e.LineStart,
                LineEnd:      e.LineEnd,
                SourceFileID: sfPtr,
            })
        }
        database.InsertEvidence(stored.ID, sourceID, dbEv)

        // Distinct file annotation per page line.
        seen := map[string]bool{}
        var distinctFiles []string
        for _, e := range allPages[i].Evidence {
            if !seen[e.SourceFilePath] {
                seen[e.SourceFilePath] = true
                distinctFiles = append(distinctFiles, e.SourceFilePath)
            }
        }
        annotation := ""
        if len(distinctFiles) > 0 {
            annotation = fmt.Sprintf(", files: %s", strings.Join(distinctFiles, ", "))
        }
        fmt.Printf("  ✓ %s (%d evidence%s)\n", allPages[i].Title, len(allPages[i].Evidence), annotation)
    }
    fmt.Printf("Ingested %d page(s) from %s\n", len(allPages), source)
    return nil
}

func computeWholeHash(files []ingest.SourceFile) string {
    h := sha256.New()
    // Concat per-file hashes so reordering produces a different whole-hash;
    // sort first so order doesn't matter.
    paths := make([]string, len(files))
    byPath := make(map[string]ingest.SourceFile, len(files))
    for i, f := range files {
        paths[i] = f.RelativePath
        byPath[f.RelativePath] = f
    }
    sort.Strings(paths)
    for _, p := range paths {
        h.Write([]byte(p))
        h.Write([]byte{0})
        h.Write([]byte(byPath[p].ContentHash))
        h.Write([]byte{0})
    }
    return fmt.Sprintf("%x", h.Sum(nil))
}
```

Add the corresponding imports (`sort`). Delete `chunkContent` and the temporary glue from Task 6. Keep `slugify` (used by ask).

- [ ] **Step 4: Run tests, confirm pass**

Run: `go test ./... -v`
Expected: PASS — partition test green, wiki tests green, db tests green. Cassette tests `TestIngestSmall`, `TestIngestMultiChunk` — see Task 18 for any needed adjustment to call sites.

- [ ] **Step 5: Commit**

```bash
git add cmd/ingest.go cmd/ingest_test.go
git commit -m "feat(ingest): rewrite pipeline around []SourceFile + per-file dedup"
```

---

### Task 12: New CLI flags on `ingest`

**Files:**
- Modify: `cmd/ingest.go`
- Modify: `cmd/ingest_test.go`

- [ ] **Step 1: Write failing flag-plumbing test**

Append to `cmd/ingest_test.go`:

```go
func TestBuildIngestOptionsAppliesFlags(t *testing.T) {
    c := &Config{Ingest: IngestConfig{
        MaxFileBytes:        256 * 1024,
        ChunkSizeBytes:      16 * 1024,
        HTTPTimeoutSeconds:  30,
        HTTPMaxBytes:        5 * 1024 * 1024,
        PDFMinTextPerPage:   50,
        RespectGitignore:    true,
    }}
    cmd := ingestCmd
    cmd.ParseFlags([]string{"--max-file-bytes", "1024", "--exclude", "*.foo,*.bar", "--no-gitignore", "--include", ".md,.go"})
    local, _ := buildIngestOptions(cmd, c)
    if local.MaxFileBytes != 1024 {
        t.Errorf("MaxFileBytes = %d", local.MaxFileBytes)
    }
    if local.RespectGitignore {
        t.Error("--no-gitignore should disable")
    }
    if len(local.ExtraSkipGlobs) != 2 {
        t.Errorf("ExtraSkipGlobs = %v", local.ExtraSkipGlobs)
    }
    if len(local.IncludeOnly) != 2 {
        t.Errorf("IncludeOnly = %v", local.IncludeOnly)
    }
}
```

- [ ] **Step 2: Run test, confirm failure**

Run: `go test ./cmd/ -run TestBuildIngestOptions -v`
Expected: FAIL — `IngestConfig`, `buildIngestOptions`, flags undefined.

- [ ] **Step 3: Add flags + plumbing**

In `cmd/ingest.go` — add `init()` registering flags:

```go
func init() {
    ingestCmd.Flags().Int64("max-file-bytes", 0, "per-file size limit; 0 uses [ingest] max_file_bytes from config")
    ingestCmd.Flags().String("include", "", "comma-separated allowlist of extensions (e.g. .md,.go)")
    ingestCmd.Flags().String("exclude", "", "comma-separated extra skip globs (e.g. *.foo,vendor/*)")
    ingestCmd.Flags().Bool("no-gitignore", false, "ignore .gitignore for this run")
    ingestCmd.Flags().Bool("force", false, "ignore per-file unchanged check; re-ingest everything")
}

func buildIngestOptions(cmd *cobra.Command, c *Config) (ingest.LocalOptions, ingest.URLOptions) {
    local := ingest.LocalOptions{
        MaxFileBytes:     c.Ingest.MaxFileBytes,
        RespectGitignore: c.Ingest.RespectGitignore,
        ExtraSkipGlobs:   c.Ingest.ExtraSkipGlobs,
        ExtraTextExtensions: c.Ingest.ExtraTextExtensions,
    }
    if v, _ := cmd.Flags().GetInt64("max-file-bytes"); v > 0 {
        local.MaxFileBytes = v
    }
    if v, _ := cmd.Flags().GetString("include"); v != "" {
        local.IncludeOnly = splitCSV(v)
    }
    if v, _ := cmd.Flags().GetString("exclude"); v != "" {
        local.ExtraSkipGlobs = append(local.ExtraSkipGlobs, splitCSV(v)...)
    }
    if v, _ := cmd.Flags().GetBool("no-gitignore"); v {
        local.RespectGitignore = false
    }
    url := ingest.URLOptions{
        Timeout:      time.Duration(c.Ingest.HTTPTimeoutSeconds) * time.Second,
        MaxBodyBytes: c.Ingest.HTTPMaxBytes,
    }
    return local, url
}

func forceFlag(cmd *cobra.Command) bool {
    v, _ := cmd.Flags().GetBool("force")
    return v
}

func splitCSV(s string) []string {
    parts := strings.Split(s, ",")
    out := parts[:0]
    for _, p := range parts {
        p = strings.TrimSpace(p)
        if p != "" {
            out = append(out, p)
        }
    }
    return out
}
```

`IngestConfig` lands in Task 14 (`cmd/root.go`). For this task, add a placeholder struct in `cmd/ingest.go` if needed — or fold this task's commit with Task 14's. Cleanest: commit `IngestConfig` (struct + defaults) in `root.go` *and* the flag plumbing here in one combined commit. To keep tasks atomic, do Task 14 first, then return to flags. **Reorder:** swap Task 12 and Task 14. The plan continues with the original numbering for reading clarity, but the implementer should run `IngestConfig` (Task 14) before flag plumbing.

- [ ] **Step 4: Run tests, confirm pass**

Run: `go test ./cmd/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/ingest.go cmd/ingest_test.go
git commit -m "feat(ingest): --max-file-bytes, --include, --exclude, --no-gitignore, --force flags"
```

---

## Phase H — Config + Output polish

### Task 13: `[ingest]` config block + defaults wiring

**Files:**
- Modify: `cmd/root.go`
- Modify: `cmd/init.go`

- [ ] **Step 1: Write failing test for default-applied config**

Add `cmd/root_test.go`:

```go
package cmd

import (
    "os"
    "path/filepath"
    "testing"
)

func TestLoadConfigAppliesIngestDefaultsWhenAbsent(t *testing.T) {
    dir := t.TempDir()
    os.Chdir(dir)
    os.MkdirAll(".llmwiki", 0755)
    os.WriteFile(filepath.Join(".llmwiki", "config.toml"), []byte(`
[llm]
provider = "ollama"
model = "x"
[wiki]
wiki_dir = ".llmwiki/wiki"
raw_dir = ".llmwiki/raw"
db_path = ".llmwiki/wiki.db"
`), 0644)
    if err := loadConfig(); err != nil {
        t.Fatal(err)
    }
    if cfg.Ingest.MaxFileBytes == 0 {
        t.Errorf("MaxFileBytes default not applied")
    }
    if cfg.Ingest.ChunkSizeBytes == 0 {
        t.Errorf("ChunkSizeBytes default not applied")
    }
    if !cfg.Ingest.RespectGitignore {
        t.Errorf("RespectGitignore default not applied")
    }
}
```

- [ ] **Step 2: Run test, confirm failure**

Run: `go test ./cmd/ -run TestLoadConfigAppliesIngest -v`
Expected: FAIL — `Config.Ingest` field missing.

- [ ] **Step 3: Add `IngestConfig`, defaults, init template**

In `cmd/root.go`:

```go
type IngestConfig struct {
    MaxFileBytes        int64    `toml:"max_file_bytes"`
    ChunkSizeBytes      int      `toml:"chunk_size_bytes"`
    HTTPTimeoutSeconds  int      `toml:"http_timeout_seconds"`
    HTTPMaxBytes        int64    `toml:"http_max_bytes"`
    PDFMinTextPerPage   int      `toml:"pdf_min_text_per_page"`
    ExtraTextExtensions []string `toml:"extra_text_extensions"`
    ExtraSkipGlobs      []string `toml:"extra_skip_globs"`
    RespectGitignore    bool     `toml:"respect_gitignore"`
}

type Config struct {
    LLM    LLMConfig    `toml:"llm"`
    Wiki   WikiConfig   `toml:"wiki"`
    Ask    AskConfig    `toml:"ask"`
    Ingest IngestConfig `toml:"ingest"`
}
```

In `loadConfig()`, after `toml.DecodeFile` returns success, fill defaults:

```go
if cfg.Ingest.MaxFileBytes == 0 {
    cfg.Ingest.MaxFileBytes = 256 * 1024
}
if cfg.Ingest.ChunkSizeBytes == 0 {
    cfg.Ingest.ChunkSizeBytes = 16 * 1024
}
if cfg.Ingest.HTTPTimeoutSeconds == 0 {
    cfg.Ingest.HTTPTimeoutSeconds = 30
}
if cfg.Ingest.HTTPMaxBytes == 0 {
    cfg.Ingest.HTTPMaxBytes = 5 * 1024 * 1024
}
if cfg.Ingest.PDFMinTextPerPage == 0 {
    cfg.Ingest.PDFMinTextPerPage = 50
}
// RespectGitignore: missing in config → default true (toml zero value is false,
// which is the wrong default; use a sentinel via *bool or detect "missing").
// Simpler: if both ExtraSkipGlobs is nil AND RespectGitignore is false, treat
// as "not configured" and flip to true.
if !cfg.Ingest.RespectGitignore && len(cfg.Ingest.ExtraSkipGlobs) == 0 && len(cfg.Ingest.ExtraTextExtensions) == 0 {
    cfg.Ingest.RespectGitignore = true
}
```

(A cleaner approach uses `*bool`; this heuristic works for v1 and matches the spec's "missing block → defaults applied silently".)

In `cmd/init.go` extend both default config templates with:

```toml

[ingest]
max_file_bytes = 262144
chunk_size_bytes = 16384
http_timeout_seconds = 30
http_max_bytes = 5242880
pdf_min_text_per_page = 50
extra_text_extensions = []
extra_skip_globs = []
respect_gitignore = true
```

In `cmd/ingest.go`, replace the constant `ingestChunkSize` literal with `cfg.Ingest.ChunkSizeBytes` at the call site (`ingest.ChunkSourceFiles(toIngest, cfg.Ingest.ChunkSizeBytes)`). Same for `pdf_min_text_per_page` if/when `pdf.go` consumes it (otherwise Task 7's constant remains and the config field is informational).

- [ ] **Step 4: Run test, confirm pass**

Run: `go test ./cmd/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/root.go cmd/init.go cmd/root_test.go
git commit -m "feat(config): [ingest] block with defaults applied silently for pre-v3 configs"
```

---

### Task 14: `ask` rendering — `(file:a-b)` annotation

**Files:**
- Modify: `internal/wiki/ops.go`
- Modify: `cmd/ask.go`

- [ ] **Step 1: Write failing format test**

Add to `internal/wiki/ops_test.go`:

```go
func TestBuildAnswerUserPromptIncludesSourceFile(t *testing.T) {
    pages := []Page{{
        Title: "T",
        Body:  "b",
        Evidence: []Evidence{
            {Quote: "abc", LineStart: 1, LineEnd: 1, SourceFilePath: "internal/db/db.go"},
            {Quote: "xyz", LineStart: 2, LineEnd: 2, SourceFilePath: "page-3"},
        },
    }}
    out := buildAnswerUserPrompt("q?", pages)
    if !strings.Contains(out, "(internal/db/db.go:1-1)") {
        t.Errorf("missing file annotation: %q", out)
    }
    if !strings.Contains(out, "(page-3:2-2)") {
        t.Errorf("missing pdf page annotation: %q", out)
    }
}
```

- [ ] **Step 2: Run test, confirm failure**

Run: `go test ./internal/wiki/ -run TestBuildAnswerUserPrompt -v`
Expected: FAIL.

- [ ] **Step 3: Update `buildAnswerUserPrompt` and `printSources`**

In `internal/wiki/ops.go`:

```go
func buildAnswerUserPrompt(question string, pages []Page) string {
    var sb strings.Builder
    sb.WriteString("## Wiki pages\n\n")
    for _, p := range pages {
        sb.WriteString(fmt.Sprintf("### %s\n\n%s\n", p.Title, p.Body))
        if len(p.Evidence) > 0 {
            sb.WriteString("\n**Source quotes for this page:**\n")
            for _, e := range p.Evidence {
                annotation := fmt.Sprintf("lines %d-%d", e.LineStart, e.LineEnd)
                if e.SourceFilePath != "" {
                    annotation = fmt.Sprintf("%s:%d-%d", e.SourceFilePath, e.LineStart, e.LineEnd)
                }
                sb.WriteString(fmt.Sprintf("> %q  (%s)\n", e.Quote, annotation))
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

Update `answerSystemPrompt` to instruct the model to render the new format:

```go
const answerSystemPrompt = `You answer using the provided wiki pages and source quotes.
Cite pages inline using [Page Title] notation.
When using a verbatim quote from a source, render it as a markdown blockquote and label it as (file:lines), e.g.:
> "channels block when full" (internal/sync/chan.go:4-4)
For PDF pages the file becomes "page-N":
> "the answer is 42" (page-3:2-2)

If pages and quotes are insufficient, say so plainly. Do not fabricate.`
```

In `cmd/ask.go`, update the `printSources` evidence loop:

```go
for _, e := range p.Evidence {
    annotation := fmt.Sprintf("lines %d-%d", e.LineStart, e.LineEnd)
    if e.SourceFilePath != "" {
        annotation = fmt.Sprintf("%s:%d-%d", e.SourceFilePath, e.LineStart, e.LineEnd)
    }
    sb.WriteString(fmt.Sprintf("    > %q  (%s)\n", e.Quote, annotation))
}
```

The `pageBundle` shaping in `cmd/ask.go` already passes `wiki.Evidence{...}` — extend the conversion to include `SourceFilePath`:

```go
ev = append(ev, wiki.Evidence{
    Quote:          e.Quote,
    LineStart:      e.LineStart,
    LineEnd:        e.LineEnd,
    SourceFilePath: e.SourceFilePath,
})
```

For this to work, `db.Evidence` must expose a `SourceFilePath` string (not just `*int64`). Either:
- Add `SourceFilePath string` to `db.Evidence` and populate it via a JOIN in `GetEvidenceForPage`/`SearchEvidence` (`LEFT JOIN source_files sf ON sf.id = e.source_file_id`), or
- Look up `sf.RelativePath` from `e.SourceFileID` in `cmd/ask.go`.

The JOIN path is cleaner. Update `db.GetEvidenceForPage` and `db.SearchEvidence` SELECT lists to add `COALESCE(sf.relative_path, '')` and a `LEFT JOIN source_files sf ON sf.id = e.source_file_id`. Add `SourceFilePath string` to `db.Evidence` and populate it.

- [ ] **Step 4: Run tests, confirm pass**

Run: `go test ./... -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/wiki/ops.go internal/wiki/ops_test.go cmd/ask.go internal/db/queries.go
git commit -m "feat(ask): render (file:a-b) annotation in source quotes; LEFT JOIN source_files"
```

---

### Task 15: `status` shows `total_source_files` and `largest_source`

**Files:**
- Modify: `cmd/status.go`

- [ ] **Step 1: Write failing test**

There is no test file for `cmd/status.go`. Skip the test step; verify manually in Task 22.

- [ ] **Step 2: Update output**

In `cmd/status.go`:

```go
fmt.Printf("source files:    %d\n", stats.TotalSourceFiles)
if len(stats.LargestSources) > 0 {
    ls := stats.LargestSources[0]
    fmt.Printf("largest source:  %s (%d files)\n", ls.URI, ls.FileCount)
}
```

Insert these lines after the existing `evidence quotes:` print.

- [ ] **Step 3: Build green**

Run: `go build ./... && go test ./...`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add cmd/status.go
git commit -m "feat(status): print total_source_files and largest_source"
```

---

## Phase I — Cassette tests

### Task 16: `TestIngestPDF` cassette

**Files:**
- Modify: `cmd/ingest_integration_test.go`

- [ ] **Step 1: Add the test**

Append:

```go
func TestIngestPDF(t *testing.T) {
    if testing.Short() {
        t.Skip()
    }
    files, err := ingest.ReadPDF("../internal/ingest/testdata/pdfs/simple.pdf")
    if err != nil {
        t.Fatalf("ReadPDF: %v", err)
    }
    chunks := ingest.ChunkSourceFiles(files, 16*1024)
    if len(chunks) == 0 {
        t.Fatal("no chunks")
    }
    client := integrationClient(t, "ingest_pdf")
    pages, err := wiki.IngestToPages(context.Background(), client, chunks[0].Files, chunks[0].Text, nil)
    if err != nil {
        t.Fatalf("IngestToPages: %v", err)
    }
    if len(pages) == 0 {
        t.Fatal("no pages")
    }
    for _, p := range pages {
        for _, e := range p.Evidence {
            if !strings.HasPrefix(e.SourceFilePath, "page-") {
                t.Errorf("evidence source_file %q does not look like page-N", e.SourceFilePath)
            }
        }
    }
}
```

(Wired-in import: `"github.com/mritunjaysharma394/llmwiki/internal/ingest"`.)

- [ ] **Step 2: Without a recorded cassette the test skips**

The existing `integrationClient` skips when `LLMWIKI_RECORD` is unset and no cassette is present.

Run: `go test ./cmd/ -run TestIngestPDF -v`
Expected: SKIP (no cassette, replay mode).

- [ ] **Step 3: Commit**

```bash
git add cmd/ingest_integration_test.go
git commit -m "test(cassette): TestIngestPDF (skips without cassette)"
```

---

### Task 17: `TestIngestRepo` cassette + minirepo fixture

**Files:**
- Modify: `cmd/ingest_integration_test.go`
- Create: `internal/ingest/testdata/dirs/minirepo/`

- [ ] **Step 1: Build the fixture**

Under `internal/ingest/testdata/dirs/minirepo/`:

```
README.md             # "# Mini Repo\n\nA test fixture.\n"
main.go               # "package main\n\nfunc main() { println(\"hi\") }\n"
go.mod                # "module minirepo\n\ngo 1.26\n"
node_modules/foo.js   # "module.exports = 0\n"  (must be skipped by walker)
```

- [ ] **Step 2: Add the test**

```go
func TestIngestRepo(t *testing.T) {
    if testing.Short() {
        t.Skip()
    }
    files, err := ingest.ReadLocal("../internal/ingest/testdata/dirs/minirepo", ingest.DefaultLocalOptions())
    if err != nil {
        t.Fatal(err)
    }
    for _, f := range files {
        if strings.Contains(f.RelativePath, "node_modules") {
            t.Errorf("node_modules leaked: %s", f.RelativePath)
        }
    }
    chunks := ingest.ChunkSourceFiles(files, 16*1024)
    client := integrationClient(t, "ingest_repo")
    var allPages []wiki.Page
    for _, ch := range chunks {
        pages, err := wiki.IngestToPages(context.Background(), client, ch.Files, ch.Text, nil)
        if err != nil {
            t.Fatalf("IngestToPages: %v", err)
        }
        allPages = append(allPages, pages...)
    }
    if len(allPages) == 0 {
        t.Fatal("no pages")
    }
    for _, p := range allPages {
        for _, e := range p.Evidence {
            if e.SourceFilePath == "" {
                t.Errorf("page %q has evidence without SourceFilePath", p.Title)
            }
        }
    }
}
```

- [ ] **Step 3: Verify skip-when-no-cassette**

Run: `go test ./cmd/ -run TestIngestRepo -v`
Expected: SKIP (replay mode, no cassette).

- [ ] **Step 4: Commit**

```bash
git add cmd/ingest_integration_test.go internal/ingest/testdata/dirs/minirepo/
git commit -m "test(cassette): TestIngestRepo with minirepo fixture (skip without cassette)"
```

---

## Phase J — Wrap

### Task 18: Sub-project 1 cassette tests adapt to new signatures

**Files:**
- Modify: `cmd/ingest_integration_test.go`

`TestIngestSmall` and `TestIngestMultiChunk` were written against `wiki.IngestToPages(ctx, client, sourceContent string, ...)`. The new signature is `IngestToPages(ctx, client, files []ingest.SourceFile, chunkText string, ...)`.

- [ ] **Step 1: Update the existing tests**

For `TestIngestSmall`:

```go
source := "Goroutines are lightweight ...\n..."
files := []ingest.SourceFile{ingest.NewSourceFile("doc", []byte(source))}
chunks := ingest.ChunkSourceFiles(files, 16*1024)
pages, err := wiki.IngestToPages(context.Background(), client, chunks[0].Files, chunks[0].Text, nil)
```

For `TestIngestMultiChunk`: build the source string the same way, wrap as a single `SourceFile`, run through `ChunkSourceFiles(files, ingestChunkSize)` (or `cfg.Ingest.ChunkSizeBytes`), iterate.

The validator's fallback (Task 10) is what keeps these recorded cassettes valid: the old recordings emit no `source_file`, so the validator falls back to "search all files for the quote". With one synthetic file, fallback hits 100% and tests pass.

- [ ] **Step 2: Run tests, confirm pass**

Run: `go test ./cmd/ -v`
Expected: PASS — sub-project 1 cassettes still replay green; new cassette-less tests skip.

- [ ] **Step 3: Commit**

```bash
git add cmd/ingest_integration_test.go
git commit -m "test: adapt sub-project 1 cassette tests to []SourceFile signature"
```

---

### Task 19: README updates

**Files:**
- Modify: `README.md`

- [ ] **Step 1: Add a short section**

Append (or insert under an existing "Usage" section):

```markdown
### Real-world ingestion

`llmwiki ingest` accepts:

- **Local files / directories** — text files, Markdown, source code, PDFs.
- **HTTP/HTTPS URLs** — HTML pages are run through a Readability pass (nav/footer/script stripped) and converted to Markdown; PDF URLs are extracted page-by-page; `text/*` is passed through.
- **GitHub repos** — shallow-cloned and walked.

Skip rules (configurable in `[ingest]`):

- Built-in deny list: `.git`, `node_modules`, `vendor`, `target`, `dist`, lockfiles, images, archives, binaries.
- `.gitignore` at the directory root is honored by default (`--no-gitignore` to disable).
- Per-file size cap (`--max-file-bytes` or `[ingest] max_file_bytes`, default 256 KB).
- Scanned/OCR-only PDF pages are detected and skipped with a warning.

Per-file dedup means re-ingesting the same source after a one-line edit only re-processes that file. `--force` overrides this.

Every evidence quote is anchored to a specific file (or PDF page) — `ask` renders sources as `(path/to/file.go:lines)`.
```

- [ ] **Step 2: Commit**

```bash
git add README.md
git commit -m "docs: real-world ingestion section in README"
```

---

### Task 20: Whole-source short-circuit retained, dedup unit-tested end-to-end

**Files:**
- Modify: `cmd/ingest_test.go`

- [ ] **Step 1: Write failing dedup-fast-path test**

```go
func TestComputeWholeHashDeterministic(t *testing.T) {
    a := []ingest.SourceFile{
        ingest.NewSourceFile("a.md", []byte("x")),
        ingest.NewSourceFile("b.md", []byte("y")),
    }
    b := []ingest.SourceFile{a[1], a[0]} // reordered
    if computeWholeHash(a) != computeWholeHash(b) {
        t.Error("hash should be order-independent")
    }
    c := []ingest.SourceFile{
        ingest.NewSourceFile("a.md", []byte("x")),
        ingest.NewSourceFile("b.md", []byte("Y")),
    }
    if computeWholeHash(a) == computeWholeHash(c) {
        t.Error("hash should differ when content differs")
    }
}
```

- [ ] **Step 2: Run test, confirm pass**

`computeWholeHash` was implemented in Task 11. This is a regression test for the "skip entire source on identical hash" fast path described in the spec.

Run: `go test ./cmd/ -run TestComputeWholeHash -v`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add cmd/ingest_test.go
git commit -m "test(ingest): regression test for order-independent whole-source hash"
```

---

### Task 21: `go vet ./... && go test ./...` clean pass

**Files:** none (verification only)

- [ ] **Step 1: Static checks**

```bash
go vet ./...
```
Expected: no warnings.

- [ ] **Step 2: All tests**

```bash
go test ./...
```
Expected: green; cassette-gated tests skip when cassettes are missing.

- [ ] **Step 3: Build the binary**

```bash
go build -o ./llmwiki ./
```
Expected: builds cleanly.

- [ ] **Step 4: Commit (only if any tweaks were needed)**

If any vet finding required adjustment:

```bash
git add -A
git commit -m "chore: vet cleanup before sub-project 3 verification"
```

Otherwise, no commit.

---

### Task 22: Final verification — checklist mirroring the spec's Verification block

**Files:** none (verification only)

- [ ] **Step 1: Local PDF**

```bash
./llmwiki ingest internal/ingest/testdata/pdfs/simple.pdf
# Expect: per-page progress, evidence quotes annotated as page-N, no
# "no content found" error.
```

- [ ] **Step 2: Scanned PDF**

```bash
./llmwiki ingest internal/ingest/testdata/pdfs/scanned.pdf
# Expect: WARN per page, exits with helpful "no extractable text" error.
```

- [ ] **Step 3: PDF over HTTP**

```bash
./llmwiki ingest https://arxiv.org/pdf/2310.06825.pdf
# Expect: content-type sniffed, dispatched to PDF path, pages ingested.
```

- [ ] **Step 4: URL with article extraction**

```bash
./llmwiki ingest https://en.wikipedia.org/wiki/Goroutine
# Expect: clean Markdown body (no nav text), evidence quotes match article.
```

- [ ] **Step 5: Real repo**

```bash
./llmwiki ingest https://github.com/golang/example
# Expect: walks tree, skips .git, no node_modules/vendor noise; pages cite
# specific files by path.
```

- [ ] **Step 6: Personal notes directory (manual)**

```bash
./llmwiki ingest ~/notes/
# Expect: PDFs extracted, images/binaries skipped, large files
# warned-and-skipped, .obsidian/ skipped (matches deny list).
```

- [ ] **Step 7: Incremental re-ingest**

```bash
echo "// new comment" >> ./internal/ingest/local.go
./llmwiki ingest ./internal/
# Expect: "N files: 1 changed, M unchanged" — only one file goes to LLM.

./llmwiki ingest ./internal/   # second run
# Expect: "Source unchanged at file level, skipping." exit 0, zero LLM calls.
```

- [ ] **Step 8: Force re-ingest**

```bash
./llmwiki ingest --force ./internal/
# Expect: all files re-ingested.
```

- [ ] **Step 9: Ask citing files**

```bash
./llmwiki ask "what does the chunker do?"
# Expect: blockquote like
#   > "Greedy bin-packing..." (internal/ingest/chunk.go:8-12)
```

- [ ] **Step 10: Status**

```bash
./llmwiki status
# Expect: source files: N, largest source: <uri> (M files).
```

- [ ] **Step 11: All tests green**

```bash
go test ./...
# Expect: green; new cassette tests skip without recordings.
```

- [ ] **Step 12: Tag (optional)**

```bash
git tag -a v0.3-real-world -m "Sub-project 3: real-world ingestion"
```

---

## Done criteria

- PDF text and URL article extraction working on real inputs.
- Repo and directory walks respect deny list, gitignore, size cap.
- Every evidence row is anchored to a specific `SourceFile`.
- Per-file dedup means a one-line edit re-processes one file.
- `user_version = 2` migration is idempotent and survives v0/v1 → v2.
- `ask` renders `(file:a-b)` annotations.
- `status` reports `total_source_files` and `largest_source`.
- `go test ./...` green; sub-project 1 cassettes still replay.
