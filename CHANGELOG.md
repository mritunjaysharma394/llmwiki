# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [1.0.0-rc.1] — 2026-05-04

### Added

- `internal/version` package with `Version`, `Commit`, `BuildDate` injected via
  `-ldflags` at release time. `llmwiki version` and `llmwiki --version` print
  semver, commit SHA, build date, Go version. `User-Agent` on outgoing HTTP
  fetches reads from the same source.
- `internal/cliutil/errors.go` with `UserError` rendered by `Execute()` as a
  three-line `Error: / cause: / try:` block. Retrofitted across `init`,
  `ingest`, `ask`, `lint` for the high-traffic failure modes.
- Feed and sitemap ingestion: `internal/ingest/feed.go` (gofeed) for
  RSS/Atom/JSON Feed, `internal/ingest/sitemap.go` (encoding/xml) for
  sitemap.xml and one-level sitemap-of-sitemaps. Polite 1-req/sec default,
  configurable caps on entries (`feed_max_entries=50`) and pages
  (`sitemap_max_pages=200`). Content-type dispatch from `url.go`; explicit
  `--feed` / `--sitemap` flags; `--max-pages` override.
- Co-resident re-chunking via a new `chunks` table (db v3). When a file's
  content changes, every neighbour that was packed in the same prior chunk is
  re-included in the new pack so cross-file synthesis stays stable on
  incremental re-ingest. `--no-rechunk` opts out for callers who accept the
  drift risk.
- `make smoke` target running the README quickstart end-to-end against a
  recorded cassette (`smoke__*.json`) — no API key needed.
- GoReleaser configuration producing `linux/amd64`, `linux/arm64`,
  `darwin/amd64`, `darwin/arm64`, `windows/amd64` archives + checksums on
  tag push. Dry-run runs in CI on every PR.
- Nightly cassette-refresh GitHub workflow (cron `17 6 * * *`) running
  `LLMWIKI_RECORD=1 go test ./...` against the project's `ANTHROPIC_API_KEY`
  secret; opens a PR if cassettes diff.
- Apache-2.0 `LICENSE`. README rewrite covering install, quickstart, common
  workflows, configuration table, trust model, privacy, architecture,
  contributing.

### Changed

- `userAgentVersion` constant in `internal/ingest/url.go` is replaced by
  `version.Version` from `internal/version`. Sites filtering on the literal
  `"llmwiki/0.1"` substring will see `"llmwiki/1.0.0-rc.1"` after this release.

## [0.2.0] — 2026-05-03 (sub-project 3)

### Added

- PDF ingest with per-page extraction and a scanned-page heuristic.
- HTTP/HTTPS URL ingest with content-type sniffing, Readability article
  extraction, html-to-markdown pipeline.
- Real GitHub-repo and local-directory walking with a built-in deny list
  (`.git`, `node_modules`, `vendor`, lockfiles, binaries), `.gitignore`
  honoring, and a configurable per-file size cap.
- Per-file evidence: every `Evidence` row is anchored to a specific
  `SourceFile` (file path or PDF page). `ask` renders sources as
  `(path/to/file.go:lines)`.
- Per-file content hashing → incremental re-ingest only re-processes files
  whose own content changed.

## [0.1.0] — 2026-05-03 (sub-project 1)

### Added

- Evidence requirement in the LLM tool schema; server-side validation that
  every quote is a verbatim substring of the source.
- `evidence` and `saved_answers` SQLite tables with FTS5 indexes.
- Streaming `ask` with TTY-aware glamour rendering.
- Auto-archive of every answer to `.llmwiki/answers/` and a row in the
  database.
- Cassette-based LLM client for record/replay testing.

[Unreleased]: https://github.com/mritunjaysharma394/llmwiki/compare/v1.0.0-rc.1...HEAD
[1.0.0-rc.1]: https://github.com/mritunjaysharma394/llmwiki/releases/tag/v1.0.0-rc.1
[0.2.0]: https://github.com/mritunjaysharma394/llmwiki/releases/tag/v0.2.0
[0.1.0]: https://github.com/mritunjaysharma394/llmwiki/releases/tag/v0.1.0
