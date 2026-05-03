package db

import (
	"database/sql"
	"encoding/json"
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
}

type Link struct {
	FromPage string
	ToPage   string
	LinkType string
}

type Stats struct {
	TotalPages   int
	TotalSources int
	StalePages   int
	LastIngest   time.Time
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
	row := d.sql.QueryRow(`SELECT id, title, path, body, content_hash, updated_at, source_ids FROM pages WHERE title = ?`, title)
	return scanPage(row)
}

func (d *DB) GetPageByID(id int64) (*PageRecord, error) {
	row := d.sql.QueryRow(`SELECT id, title, path, body, content_hash, updated_at, source_ids FROM pages WHERE id = ?`, id)
	return scanPage(row)
}

func scanPage(row *sql.Row) (*PageRecord, error) {
	var p PageRecord
	var updatedAt, sourceIDs string
	if err := row.Scan(&p.ID, &p.Title, &p.Path, &p.Body, &p.ContentHash, &updatedAt, &sourceIDs); err != nil {
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
		`SELECT p.id, p.title, p.path, p.body, p.content_hash, p.updated_at, p.source_ids
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
	rows, err := d.sql.Query(`SELECT id, title, path, body, content_hash, updated_at, source_ids FROM pages`)
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
		if err := rows.Scan(&p.ID, &p.Title, &p.Path, &p.Body, &p.ContentHash, &updatedAt, &sourceIDs); err != nil {
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
	var lastIngestStr string
	d.sql.QueryRow(`SELECT MAX(ingested_at) FROM sources`).Scan(&lastIngestStr)
	s.LastIngest, _ = time.Parse(time.RFC3339, lastIngestStr)
	return s, nil
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
