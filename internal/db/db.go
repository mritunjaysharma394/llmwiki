package db

import (
	"database/sql"
	"fmt"
	"strings"

	_ "modernc.org/sqlite"
)

type DB struct {
	sql *sql.DB
}

func Open(path string) (*DB, error) {
	sqlDB, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("opening sqlite: %w", err)
	}
	d := &DB{sql: sqlDB}
	if err := d.migrate(); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("migration: %w", err)
	}
	return d, nil
}

func (d *DB) Close() error {
	return d.sql.Close()
}

func (d *DB) migrate() error {
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

	if version < 3 {
		v3 := []string{
			`CREATE TABLE IF NOT EXISTS chunks (
				id INTEGER PRIMARY KEY,
				source_id INTEGER NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
				chunk_hash TEXT NOT NULL,
				file_paths TEXT NOT NULL,
				created_at DATETIME DEFAULT CURRENT_TIMESTAMP
			)`,
			`CREATE INDEX IF NOT EXISTS idx_chunks_source ON chunks(source_id)`,
			`PRAGMA user_version = 3`,
		}
		for _, stmt := range v3 {
			if _, err := d.sql.Exec(stmt); err != nil {
				return fmt.Errorf("v3 migration %q: %w", stmt[:min(50, len(stmt))], err)
			}
		}
	}

	if version < 4 {
		// Sub-project 6b (v0.6) — additive only.
		//
		// page_update_log is the audit trail for the cross-page page-update
		// pass. One row per candidate per ingest, written even on `failed`
		// and `skipped` outcomes so a user can run
		//   sqlite3 .llmwiki/wiki.db "SELECT title, outcome, reason
		//                             FROM page_update_log
		//                             JOIN pages ON pages.id = page_update_log.page_id"
		// to debug. Never rotated, never truncated (Q9). Roll-forward only;
		// no down-migration script (matches every prior migration).
		//
		// No ALTER TABLE on existing tables (Q8). pages, evidence, sources,
		// source_files, chunks are byte-identical pre/post v4.
		v4 := []string{
			`CREATE TABLE IF NOT EXISTS page_update_log (
				id INTEGER PRIMARY KEY,
				page_id INTEGER NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
				source_id INTEGER REFERENCES sources(id) ON DELETE SET NULL,
				prior_content_hash TEXT NOT NULL,
				new_content_hash TEXT,
				outcome TEXT NOT NULL,
				reason TEXT,
				evidence_added INTEGER,
				evidence_removed INTEGER,
				created_at DATETIME DEFAULT CURRENT_TIMESTAMP
			)`,
			`CREATE INDEX IF NOT EXISTS idx_page_update_log_page ON page_update_log(page_id)`,
			`CREATE INDEX IF NOT EXISTS idx_page_update_log_source ON page_update_log(source_id)`,
			`PRAGMA user_version = 4`,
		}
		for _, stmt := range v4 {
			if _, err := d.sql.Exec(stmt); err != nil {
				return fmt.Errorf("v4 migration %q: %w", stmt[:min(50, len(stmt))], err)
			}
		}
	}

	if _, err := d.sql.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		return fmt.Errorf("enable foreign_keys: %w", err)
	}
	return nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
