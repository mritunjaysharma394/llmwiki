package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type Source struct {
	ID          int64
	URI         string
	ContentHash string
	IngestedAt  time.Time
}

type PageRecord struct {
	ID          int64
	Title       string
	Path        string
	Body        string
	ContentHash string
	UpdatedAt   time.Time
	SourceIDs   []int64
	// SchemaHash is the sha256 hex of the active schema doc when this
	// page was last written. Sub-project 7 / v0.7 added the column;
	// pre-v5 rows carry "". Populated by every read path
	// (GetPage / GetPageByID / AllPages / SearchPages /
	// ListPagesByHash / ListPagesNotAtHash). Stamped post-write by
	// db.UpdateSchemaHash from every WritePage call site.
	SchemaHash string
}

type Link struct {
	FromPage string
	ToPage   string
	LinkType string
}

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

type LargestSource struct {
	SourceID  int64
	URI       string
	FileCount int
}

type SourceFile struct {
	ID           int64
	SourceID     int64
	RelativePath string
	ContentHash  string
	ByteSize     int64
	LineCount    int
	IngestedAt   time.Time
}

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

func (d *DB) UpsertSource(uri, hash string) (int64, error) {
	var id int64
	err := d.sql.QueryRow(
		`INSERT INTO sources (uri, content_hash, ingested_at) VALUES (?, ?, ?)
		ON CONFLICT(uri) DO UPDATE SET content_hash=excluded.content_hash, ingested_at=excluded.ingested_at
		RETURNING id`,
		uri, hash, time.Now().UTC().Format(time.RFC3339),
	).Scan(&id)
	return id, err
}

func (d *DB) GetSource(uri string) (*Source, error) {
	row := d.sql.QueryRow(`SELECT id, uri, content_hash, ingested_at FROM sources WHERE uri = ?`, uri)
	var s Source
	var ingestedAt string
	if err := row.Scan(&s.ID, &s.URI, &s.ContentHash, &ingestedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	s.IngestedAt, _ = time.Parse(time.RFC3339, ingestedAt)
	return &s, nil
}

func (d *DB) UpsertPage(p PageRecord) error {
	ids, _ := json.Marshal(p.SourceIDs)
	_, err := d.sql.Exec(
		`INSERT INTO pages (title, path, body, content_hash, updated_at, source_ids) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(title) DO UPDATE SET path=excluded.path, body=excluded.body, content_hash=excluded.content_hash,
		updated_at=excluded.updated_at, source_ids=excluded.source_ids`,
		p.Title, p.Path, p.Body, p.ContentHash, time.Now(), string(ids),
	)
	return err
}

func (d *DB) GetPage(title string) (*PageRecord, error) {
	row := d.sql.QueryRow(`SELECT id, title, path, body, content_hash, updated_at, source_ids, COALESCE(schema_hash, '') FROM pages WHERE title = ?`, title)
	return scanPage(row)
}

func (d *DB) GetPageByID(id int64) (*PageRecord, error) {
	row := d.sql.QueryRow(`SELECT id, title, path, body, content_hash, updated_at, source_ids, COALESCE(schema_hash, '') FROM pages WHERE id = ?`, id)
	return scanPage(row)
}

func scanPage(row *sql.Row) (*PageRecord, error) {
	var p PageRecord
	var updatedAt, sourceIDs string
	if err := row.Scan(&p.ID, &p.Title, &p.Path, &p.Body, &p.ContentHash, &updatedAt, &sourceIDs, &p.SchemaHash); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	p.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
	json.Unmarshal([]byte(sourceIDs), &p.SourceIDs)
	return &p, nil
}

func ftsQuery(q string) string {
	var words []string
	for _, w := range strings.Fields(q) {
		clean := strings.Map(func(r rune) rune {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
				return r
			}
			return -1
		}, w)
		if len(clean) > 1 {
			words = append(words, clean)
		}
	}
	if len(words) == 0 {
		return q
	}
	return strings.Join(words, " OR ")
}

func (d *DB) SearchPages(query string, limit int) ([]PageRecord, error) {
	rows, err := d.sql.Query(
		`SELECT p.id, p.title, p.path, p.body, p.content_hash, p.updated_at, p.source_ids, COALESCE(p.schema_hash, '')
		FROM pages p
		WHERE p.id IN (SELECT rowid FROM pages_fts WHERE pages_fts MATCH ?)
		LIMIT ?`,
		ftsQuery(query), limit,
	)
	if err != nil {
		return nil, fmt.Errorf("fts search: %w", err)
	}
	defer rows.Close()
	return scanPages(rows)
}

func (d *DB) AllPages() ([]PageRecord, error) {
	rows, err := d.sql.Query(`SELECT id, title, path, body, content_hash, updated_at, source_ids, COALESCE(schema_hash, '') FROM pages`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPages(rows)
}

func (d *DB) AllPageTitles() ([]string, error) {
	rows, err := d.sql.Query(`SELECT title FROM pages ORDER BY title`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var titles []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		titles = append(titles, t)
	}
	return titles, rows.Err()
}

func scanPages(rows *sql.Rows) ([]PageRecord, error) {
	var pages []PageRecord
	for rows.Next() {
		var p PageRecord
		var updatedAt, sourceIDs string
		if err := rows.Scan(&p.ID, &p.Title, &p.Path, &p.Body, &p.ContentHash, &updatedAt, &sourceIDs, &p.SchemaHash); err != nil {
			return nil, err
		}
		p.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
		json.Unmarshal([]byte(sourceIDs), &p.SourceIDs)
		pages = append(pages, p)
	}
	return pages, rows.Err()
}

func (d *DB) UpsertLinks(fromPage string, links []Link) error {
	_, err := d.sql.Exec(`DELETE FROM links WHERE from_page = ?`, fromPage)
	if err != nil {
		return err
	}
	for _, l := range links {
		if _, err := d.sql.Exec(
			`INSERT INTO links (from_page, to_page, link_type) VALUES (?, ?, ?)`,
			l.FromPage, l.ToPage, l.LinkType,
		); err != nil {
			return err
		}
	}
	return nil
}

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

func (d *DB) SearchEvidence(query string, limit int) ([]EvidenceHit, error) {
	rows, err := d.sql.Query(
		`SELECT e.id, e.page_id, e.source_id, e.source_file_id, e.quote, COALESCE(e.line_start, 0), COALESCE(e.line_end, 0), e.created_at, p.title
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
		var sfid sql.NullInt64
		var created string
		if err := rows.Scan(&h.ID, &h.PageID, &h.SourceID, &sfid, &h.Quote, &h.LineStart, &h.LineEnd, &created, &h.PageTitle); err != nil {
			return nil, err
		}
		if sfid.Valid {
			v := sfid.Int64
			h.SourceFileID = &v
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
	rows, err := d.sql.Query(`SELECT p.id, p.title, p.path, p.body, p.content_hash, p.updated_at, p.source_ids, COALESCE(p.schema_hash, '')
		FROM pages p
		LEFT JOIN evidence e ON e.page_id = p.id
		WHERE e.id IS NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPages(rows)
}

type Chunk struct {
	ID        int64
	SourceID  int64
	ChunkHash string
	FilePaths []string
	CreatedAt time.Time
}

func (d *DB) InsertChunks(chunks []Chunk) error {
	if len(chunks) == 0 {
		return nil
	}
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT INTO chunks (source_id, chunk_hash, file_paths) VALUES (?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, c := range chunks {
		paths, _ := json.Marshal(c.FilePaths)
		if _, err := stmt.Exec(c.SourceID, c.ChunkHash, string(paths)); err != nil {
			return fmt.Errorf("insert chunk: %w", err)
		}
	}
	return tx.Commit()
}

// GetChunksForFile returns chunks that included relativePath in their pack.
// Uses LIKE on the JSON-encoded array; for the v1 row volumes (low thousands
// at most) this is fast enough and avoids needing JSON1 builds of sqlite.
func (d *DB) GetChunksForFile(sourceID int64, relativePath string) ([]Chunk, error) {
	pat := "%" + jsonEscape(relativePath) + "%"
	rows, err := d.sql.Query(
		`SELECT id, source_id, chunk_hash, file_paths, created_at
		 FROM chunks WHERE source_id = ? AND file_paths LIKE ?`,
		sourceID, pat,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Chunk
	for rows.Next() {
		var c Chunk
		var paths string
		var ts string
		if err := rows.Scan(&c.ID, &c.SourceID, &c.ChunkHash, &paths, &ts); err != nil {
			return nil, err
		}
		json.Unmarshal([]byte(paths), &c.FilePaths)
		c.CreatedAt, _ = time.Parse(time.RFC3339, ts)
		// Filter out false positives from the LIKE: ensure the path is in the
		// JSON array exactly.
		exact := false
		for _, p := range c.FilePaths {
			if p == relativePath {
				exact = true
				break
			}
		}
		if exact {
			out = append(out, c)
		}
	}
	return out, rows.Err()
}

func (d *DB) DeleteChunksForSource(sourceID int64) error {
	_, err := d.sql.Exec(`DELETE FROM chunks WHERE source_id = ?`, sourceID)
	return err
}

// jsonEscape is the minimal escape needed for LIKE patterns over JSON-encoded
// strings: the path itself can contain characters that LIKE treats specially
// (% and _). We escape them with a backslash and use LIKE ... ESCAPE '\\'.
// In practice file paths don't contain % or _ in the underscore-as-glob sense,
// but be defensive.
func jsonEscape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}

// PageUpdateLogEntry mirrors one row of page_update_log. Outcome must
// be one of: "updated", "body_only", "failed", "skipped". SourceID == 0
// writes NULL (the page-update pass may run with no associated source
// row, e.g. when the new pages were ingested before this update pass
// fired in a different invocation). NewContentHash == "" writes NULL
// (failed outcomes have no new hash).
type PageUpdateLogEntry struct {
	PageID           int64
	SourceID         int64
	PriorContentHash string
	NewContentHash   string
	Outcome          string
	Reason           string
	EvidenceAdded    int
	EvidenceRemoved  int
	CreatedAt        time.Time // populated on read; ignored on write
}

var ErrInvalidOutcome = errors.New("invalid outcome")

// ErrPageNotFound is returned by UpdateSchemaHash when no page with the
// given ID exists. Callers wrap this with %w in WARN logs so the
// surrounding context (the page title and call site) is preserved.
var ErrPageNotFound = errors.New("page not found")

// UpdateSchemaHash stamps the active schema's hash onto an existing
// pages row. Called from every WritePage write site (ingest_runner,
// promote, update_existing, retrolink, mcp.write_page) AFTER the
// validator-gated write has already landed on disk + DB. RowsAffected
// == 0 surfaces ErrPageNotFound; the caller treats stamp failures as
// non-fatal (the page already reached disk; the hash is metadata).
func (d *DB) UpdateSchemaHash(pageID int64, hash string) error {
	res, err := d.sql.Exec(`UPDATE pages SET schema_hash = ? WHERE id = ?`, hash, pageID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("%w: id=%d", ErrPageNotFound, pageID)
	}
	return nil
}

// CountPagesByHashState returns (current, prior) where current is the
// count of pages whose schema_hash equals activeHash and prior is
// everything else (including the empty-string default for pre-v5
// rows). Used by cmd/lint and cmd/status's drift surface (Phase H
// Task 13). Single SELECT with a SUM(CASE WHEN ...) so the wiki size
// doesn't matter — one row scan, regardless.
func (d *DB) CountPagesByHashState(activeHash string) (current, prior int, err error) {
	err = d.sql.QueryRow(`
		SELECT
		  COALESCE(SUM(CASE WHEN schema_hash = ? THEN 1 ELSE 0 END), 0),
		  COALESCE(SUM(CASE WHEN schema_hash = ? THEN 0 ELSE 1 END), 0)
		FROM pages`, activeHash, activeHash).Scan(&current, &prior)
	return
}

// ListPagesByHash returns up to `limit` PageRecords whose schema_hash
// equals the given hash. Used by `schema migrate` (Phase F) for
// progress reporting / debugging. Order: id ASC for stability across
// runs.
func (d *DB) ListPagesByHash(hash string, limit int) ([]PageRecord, error) {
	rows, err := d.sql.Query(
		`SELECT id, title, path, body, content_hash, updated_at, source_ids, COALESCE(schema_hash, '')
		FROM pages WHERE schema_hash = ? ORDER BY id LIMIT ?`,
		hash, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPages(rows)
}

// ListPagesNotAtHash is the inverse of ListPagesByHash — returns up to
// `limit` PageRecords whose schema_hash != activeHash (which includes
// the empty-string default). Used by `llmwiki schema migrate` (Phase F
// Task 11) to walk pages that haven't been re-stamped under the active
// schema yet, and as the resumability seam: succeeded pages get the
// new hash so a re-run skips them automatically.
func (d *DB) ListPagesNotAtHash(activeHash string, limit int) ([]PageRecord, error) {
	rows, err := d.sql.Query(
		`SELECT id, title, path, body, content_hash, updated_at, source_ids, COALESCE(schema_hash, '')
		FROM pages WHERE schema_hash != ? ORDER BY id LIMIT ?`,
		activeHash, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPages(rows)
}

var validOutcomes = map[string]bool{
	"updated": true, "body_only": true, "failed": true, "skipped": true,
}

// DeleteEvidenceForPage removes every evidence row associated with the
// given page. The AFTER DELETE trigger on evidence handles the FTS
// mirror cleanup. Used by the cross-page page-update pass to swap an
// updated page's evidence atomically (delete-old + insert-new).
func (d *DB) DeleteEvidenceForPage(pageID int64) error {
	_, err := d.sql.Exec(`DELETE FROM evidence WHERE page_id = ?`, pageID)
	return err
}

// InsertPageUpdateLog appends one audit-trail row. Called from
// wiki.UpdateExistingPagesFromSource on every outcome (updated /
// body_only / failed / skipped) so a user can sqlite3-grep the log to
// understand why a particular page did or did not change.
func (d *DB) InsertPageUpdateLog(e PageUpdateLogEntry) error {
	if !validOutcomes[e.Outcome] {
		return fmt.Errorf("%w: %q (valid: updated, body_only, failed, skipped)",
			ErrInvalidOutcome, e.Outcome)
	}
	var sourceID any
	if e.SourceID != 0 {
		sourceID = e.SourceID
	}
	var newHash any
	if e.NewContentHash != "" {
		newHash = e.NewContentHash
	}
	var reason any
	if e.Reason != "" {
		reason = e.Reason
	}
	_, err := d.sql.Exec(`
		INSERT INTO page_update_log
			(page_id, source_id, prior_content_hash, new_content_hash,
			 outcome, reason, evidence_added, evidence_removed)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		e.PageID, sourceID, e.PriorContentHash, newHash,
		e.Outcome, reason, e.EvidenceAdded, e.EvidenceRemoved)
	return err
}

// GetPageUpdateLog returns the most recent `limit` log entries for the
// given page, newest first. Used by --debug-updates and (potentially)
// future lint integrations.
func (d *DB) GetPageUpdateLog(pageID int64, limit int) ([]PageUpdateLogEntry, error) {
	rows, err := d.sql.Query(`
		SELECT id, page_id, COALESCE(source_id, 0), prior_content_hash,
		       COALESCE(new_content_hash, ''), outcome, COALESCE(reason, ''),
		       COALESCE(evidence_added, 0), COALESCE(evidence_removed, 0),
		       created_at
		FROM page_update_log
		WHERE page_id = ?
		ORDER BY created_at DESC, id DESC
		LIMIT ?`, pageID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PageUpdateLogEntry
	for rows.Next() {
		var e PageUpdateLogEntry
		var id int64
		if err := rows.Scan(&id, &e.PageID, &e.SourceID, &e.PriorContentHash,
			&e.NewContentHash, &e.Outcome, &e.Reason,
			&e.EvidenceAdded, &e.EvidenceRemoved, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// CountPageUpdateLogByOutcome returns a map of outcome → count over
// the entire page_update_log table. Used by cmd/status to surface
// pages_updated_total and pages_update_failed_total counters.
// Pure read; never modifies the table.
func (d *DB) CountPageUpdateLogByOutcome() (map[string]int, error) {
	rows, err := d.sql.Query(`SELECT outcome, COUNT(*) FROM page_update_log GROUP BY outcome`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var oc string
		var n int
		if err := rows.Scan(&oc, &n); err != nil {
			return nil, err
		}
		out[oc] = n
	}
	return out, rows.Err()
}

func (d *DB) GetAllSources() ([]Source, error) {
	rows, err := d.sql.Query(`SELECT id, uri, content_hash, ingested_at FROM sources`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var sources []Source
	for rows.Next() {
		var s Source
		var ingestedAt string
		if err := rows.Scan(&s.ID, &s.URI, &s.ContentHash, &ingestedAt); err != nil {
			return nil, err
		}
		s.IngestedAt, _ = time.Parse(time.RFC3339, ingestedAt)
		sources = append(sources, s)
	}
	return sources, rows.Err()
}
