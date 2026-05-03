package db

import (
	"database/sql"
	"fmt"

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
	stmts := []string{
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
	for _, stmt := range stmts {
		if _, err := d.sql.Exec(stmt); err != nil {
			return fmt.Errorf("exec %q: %w", stmt[:min(50, len(stmt))], err)
		}
	}
	return nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
