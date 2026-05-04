# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [1.2.0] — 2026-05-04

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
- `internal/mcp` `serverVersion` bumped to `1.2.0`.

### Notes
- **No schema migration.** `PRAGMA user_version` stays at 3.
- The existing `lint` command's whole-wiki contradiction batcher is
  unchanged. Live contradiction-on-ingest is a sibling, not a
  replacement.
- v1.3 (sub-project 6b) will add the cross-page page-update pass
  under a default-off `--update-existing` flag, plus further
  `mcp.ingest` return-shape extensions (`pages_updated`,
  `pages_update_failed`). Out of scope for v1.2.

## [1.1.0] — 2026-05-04

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

[Unreleased]: https://github.com/mritunjaysharma394/llmwiki/compare/v1.2.0...HEAD
[1.2.0]: https://github.com/mritunjaysharma394/llmwiki/releases/tag/v1.2.0
[1.1.0]: https://github.com/mritunjaysharma394/llmwiki/releases/tag/v1.1.0
[1.0.0-rc.1]: https://github.com/mritunjaysharma394/llmwiki/releases/tag/v1.0.0-rc.1
[0.2.0]: https://github.com/mritunjaysharma394/llmwiki/releases/tag/v0.2.0
[0.1.0]: https://github.com/mritunjaysharma394/llmwiki/releases/tag/v0.1.0
