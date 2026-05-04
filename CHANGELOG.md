# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.6.0-rc.1] — 2026-05-04

### Added
- `llmwiki ingest --update-existing` — new flag, **default off**.
  When enabled, after writing new pages from the source, runs the
  cross-page page-update pass: per existing page that this source
  touches (FTS-shortlisted, capped at 20 per source / 50 per ingest),
  proposes an updated body via one LLM call, validates the proposed
  evidence through the same byte-exact substring-match validator
  that gates `ingest` and `mcp.write_page`, and replaces the page
  body on success. Pages whose proposed body fails validation stay
  at their previous version — the trust property holds, and we
  never silently downgrade. Costs roughly one LLM call per matched
  candidate page; recommended on Gemini Flash (free tier comfortably
  absorbs the fan-out). Sub-project 6 pillar 3.
- `llmwiki ingest --debug-updates` — new flag, default off. Prints
  per-candidate verdicts (LLM proposed body, validator kept N
  quotes, content_hash drift) to stderr. Useful for diagnosing
  `update_failed` summary lines.
- `[ingest] update_existing` config key (`*bool`, default false).
  Persists the opt-in across invocations. Three tunables alongside:
  `update_existing_max_candidates_per_source` (default 20),
  `update_existing_max_candidates_total` (default 50),
  `update_existing_quote_floor` (default 2).
- `mcp.ingest` accepts new optional `update_existing: bool`
  argument (default false). Return shape gains `pages_updated: int`
  and `pages_update_failed: int` (alongside v0.5's
  `retro_linked_pages` and `contradictions_flagged`). No new MCP
  tool — single round-trip semantics (Q10).
- `llmwiki status` surfaces `pages updated total` and
  `pages update failed` counters from `page_update_log` (when
  non-zero). Pure read; no migration of pages_total / evidence_quotes.
- Contradiction → update bridge: when `--update-existing` is on
  AND `DetectIngestContradictions` returns non-empty, every
  existing page that surfaced as a contradiction becomes a forced
  candidate for the update pass (bypasses FTS shortlist + global
  cap). Spec line 60 — "[contradiction-on-ingest] upgraded to
  'edit existing page' once 6b lands."
- `page_update_log` SQLite table (v4 schema migration): one row
  per candidate per ingest, written on every outcome (`updated` /
  `body_only` / `failed` / `skipped`). The audit trail is
  permanent (never rotated, never truncated, Q9). Indexed on
  `page_id` and `source_id`. New queries:
  `db.DeleteEvidenceForPage`, `db.InsertPageUpdateLog`,
  `db.GetPageUpdateLog`, `db.CountPageUpdateLogByOutcome`.

### Changed
- `internal/mcp` `serverVersion` bumped to `0.6.0-rc.1`.
- Ingest order extended: (1) write new pages, (2) retro-link
  existing pages to new titles (v0.5), (3) detect contradictions
  (v0.5), (4) **update existing pages** (new in v0.6, gated by
  `--update-existing`), (5) re-run retro-link over (new + updated)
  titles, (6) regenerate index, (7) append log.

### Notes
- **Schema migration v3 → v4 is additive only** (Q8). New
  `page_update_log` table + two indexes. No `ALTER TABLE` on
  existing tables; `pages`, `evidence`, `sources`, `source_files`,
  `chunks` are byte-identical pre/post v4. Roll-forward only —
  no down-migration script.
- `--update-existing` defaults to **off** (Q11). The validator
  can drop a proposed page body if quotes don't substring-match
  the (new + existing) source union; the cost picture is real
  (~$0.30/ingest on Anthropic Haiku, free on Gemini Flash).
  Consider flipping the default in v0.7 once we have real-world
  numbers from opt-in users.
- `page_update_log` is **never rotated, never truncated** (Q9).
  At 100 ingests/year × 50 candidates × ~1 row each = 5000
  rows/year — fine for our target wiki sizes.
- TRUST PROPERTY REAFFIRMED. Every page reaching disk via the
  update path has ≥1 evidence quote that substring-matches some
  file in the union of (this source + that page's prior sources).
  Pages with `update_failed` are byte-identical on disk to their
  previous version.

## [0.5.0-rc.1] — 2026-05-04

### Added
- `llmwiki promote <answer-file-or-slug>` — new command that lifts a
  saved answer (`.llmwiki/answers/<ts>-<slug>.md`) into a permanent
  wiki page. Defensive re-validation runs every evidence quote
  through the same byte-exact substring-match validator that gates
  `ingest` and `mcp.write_page` — answers whose source files have
  changed since the ask are rejected with `evidence_invalid`. Flags:
  `--title`, `--rewrite` (default off), `--no-save`.
- `mcp.promote_answer` MCP tool — same defensive validation over MCP.
  Inputs: `answer_path` (required), `title`, `rewrite`, `no_save`.
  Returns `{title, path, evidence_quotes, rewrite_applied,
  retro_linked_pages: []string}`. Seventh MCP tool.
- Retro-linker — every new page (from `ingest`, `promote`, or
  `mcp.write_page`) automatically gets `[[Title]]` backlinks added
  to existing pages whose bodies mention it in bare prose. Body-only,
  idempotent, evidence rows untouched, no LLM call. Surfaces in the
  `ingest` summary line as "Retro-linked N existing page(s)".
- Contradiction-on-ingest — when a new page's claim conflicts with
  an existing page's claim, the conflict prints inline ("!! N
  contradiction(s) flagged") and appends to
  `<wikiDir>/contradictions.md` in an Obsidian-friendly append-only
  format. Uses whatever provider you configured at `init` (Gemini
  Flash users pay nothing; Anthropic users pay typical Haiku rates
  per ingest). Failures are non-fatal — the new pages still land.
- `mcp.ingest` return shape extended: adds `retro_linked_pages: int`
  and `contradictions_flagged: int`. `mcp.write_page` gains
  `retro_linked_pages: int`.

### Changed
- `internal/mcp` `serverVersion` bumped to `0.5.0-rc.1`.

### Notes
- **No schema migration.** `PRAGMA user_version` stays at 3.
- The existing `lint` command's whole-wiki contradiction batcher is
  unchanged. Live contradiction-on-ingest is a sibling, not a
  replacement.
- v0.6 (sub-project 6b) will add the cross-page page-update pass
  under a default-off `--update-existing` flag, plus further
  `mcp.ingest` return-shape extensions (`pages_updated`,
  `pages_update_failed`). Out of scope for v0.5.

## [0.4.0] — 2026-05-04

### Added
- `llmwiki mcp` — MCP stdio server exposing six tools (`ingest`, `ask`,
  `list_pages`, `read_page`, `write_page`, `lint`). `write_page` runs every
  proposed page through the same evidence-validation pipeline as
  `llmwiki ingest`; quotes that don't substring-match the named source are
  rejected with a structured error.
- Google Gemini provider (`--provider gemini`, default `gemini-2.0-flash`).
  Free tier, 1M context, no credit card. Now the recommended onboarding
  default.
- Generic OpenAI-compatible provider (`--provider openai-compatible`).
  Configurable `base_url` + `api_key_env` + `default_model`. Tested against
  Groq, OpenRouter, Together, Cerebras, and Mistral La Plateforme.
- Obsidian-native disk layout: `[[wikilinks]]` between page bodies, an
  auto-regenerated `.llmwiki/wiki/index.md` hub, an append-only
  `.llmwiki/wiki/log.md` chronicle, and `tags` / `sources` / `created`
  frontmatter keys spelled the way Obsidian's Dataview plugin expects.
- `[providers]` config block with per-provider knobs
  (`base_url`, `api_key_env`, `default_model`, `url`).

### Changed
- `llmwiki init` walkthrough recommends Gemini first (was Anthropic).
  Existing users with `provider = "anthropic"` keep working unchanged.
- `writePagesTool` description nudges the model toward `[[Page Title]]`
  syntax for cross-page references.

### Notes
- No schema migration. `PRAGMA user_version` stays at 3.
- Cheap-provider wikis end up sparser than Haiku wikis but never less
  honest — the validator drops unverified quotes on every provider equally.

## [0.3.0] — 2026-05-04

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
  `"llmwiki/0.1"` substring will see `"llmwiki/0.3.0"` after this release.

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

[Unreleased]: https://github.com/mritunjaysharma394/llmwiki/compare/v0.6.0-rc.1...HEAD
[0.6.0-rc.1]: https://github.com/mritunjaysharma394/llmwiki/releases/tag/v0.6.0-rc.1
[0.5.0-rc.1]: https://github.com/mritunjaysharma394/llmwiki/releases/tag/v0.5.0-rc.1
[0.4.0]: https://github.com/mritunjaysharma394/llmwiki/releases/tag/v0.4.0
[0.3.0]: https://github.com/mritunjaysharma394/llmwiki/releases/tag/v0.3.0
[0.2.0]: https://github.com/mritunjaysharma394/llmwiki/releases/tag/v0.2.0
[0.1.0]: https://github.com/mritunjaysharma394/llmwiki/releases/tag/v0.1.0
