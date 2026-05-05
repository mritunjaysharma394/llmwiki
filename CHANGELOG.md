# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- `llmwiki init --mcp-only` — skip the provider API-key check at init
  time. Use this when driving llmwiki via `llmwiki mcp` from Claude
  Desktop / Claude Code, where the client makes the model calls and
  llmwiki itself never touches a provider API. Without the flag, init
  still surfaces the missing-key UserError it always has.

### Changed
- README rewritten as a 5-minute onboarding doc (~125 lines, was 766).
  Lead path is the two-command MCP quickstart for Claude Pro/Max users.
  Deep content extracted into focused files under `docs/`:
  `docs/mcp.md`, `docs/living-wiki.md`, `docs/schema.md`,
  `docs/ingestion.md`, `docs/configuration.md`, `docs/architecture.md`,
  and a new `CONTRIBUTING.md`. No external doc anchors broke (none of
  the deep README anchors were referenced from CHANGELOG or code).
- Stripped third-party-pricing claims ("free tier", "no credit card",
  "generous daily quota") from README and provider docs. Pricing is
  out of our control; describe what each provider *is* and link to the
  provider's own pricing page.

## [0.8.0-rc.1] — 2026-05-05

### Added
- `llmwiki watch <dir>` — long-lived fsnotify daemon. Drop a file
  in a watched directory; debounce 2s per file (so editor saves
  coalesce); enqueue onto a SQLite-backed crash-resumable queue;
  consumer goroutine drains the queue via the same pipeline
  `llmwiki ingest <source>` uses. Ctrl-C drains the in-flight
  ingest, closes fsnotify, exits 0. Retry policy: 3 attempts with
  5s / 30s / 5min exponential backoff, then `status='failed'`.
  `[watch]` config block exposes `dirs`, `debounce_seconds`, and
  `max_attempts`. The watcher is the single feature that converts
  llmwiki from "CLI tool" to "living wiki" as a UX shift —
  Karpathy's "modify 10–15 pages in one pass" applied to a folder
  of dropped files instead of a one-off command.
- `llmwiki maintain` — umbrella subcommand for cron / launchd /
  GitHub Actions automation. Bare invocation runs `--lint`,
  `--refresh-stale`, `--promote-pending`; pass any flag to run only
  that subset. `--dry-run` composes with all of the above.
  Composable, idempotent, exits non-zero only when an actual
  failure happens (network, DB, crashed promote) — cosmetic
  findings exit 0 so cron doesn't page on a merely drifty wiki.
- `llmwiki capture-session` — Claude Code Stop-hook companion.
  Reads the session JSON from stdin, extracts assistant turns
  that mention `LLMWIKI_DIR` or invoke `llmwiki ` on the CLI,
  files them as a saved answer, and runs the auto-promote gate.
  Robustness contract: empty stdin / malformed JSON / no
  wiki-relevant turns all exit 0 silently — capture must never
  fail the user's Stop hook. Recipe in `docs/automation.md` is
  copy-paste only; we don't auto-install hooks.
- Auto-promote in `llmwiki ask` — every ask runs the four-signal
  heuristic gate; on pass, the saved answer is promoted to a
  permanent page (subject to the byte-exact validator). Output is
  one line: `→ filed as [[Title]]` or `→ saved to <path>
  (<reason>)`. The four signals: ≥ 2 cited pages + ≥ 3 evidence
  quotes, length 100–3000 words, no hedging phrases, no
  near-duplicate page. Default ON; opt out with `[ask]
  auto_promote = false`. The validator drops anything the gate
  somehow lets through; gate-fail or validator-fail leaves the
  answer in `.llmwiki/answers/` for manual review (two locks,
  never silently dropped).
- Ingest-tail FastLint — after every ingest, sub-second pass over
  the just-written wiki surfaces orphan pages, missing
  cross-references, and schema drift counts. Silent when clean.
  The slow checks (URL re-fetch staleness, full-wiki LLM
  contradiction batch) stay in `llmwiki maintain` from cron.
- `internal/queue` — SQLite-backed work queue
  (`Enqueue / NextPending / MarkSuccess / MarkRetrying / MarkFailed`).
  Crash-resumable: a watch restart picks up `pending` and
  window-elapsed `retrying` rows; `running` rows from a crashed
  predecessor are recovered after a stale window. Lives in the
  same `wiki.db` (one DB → one truth).
- DB v6 — `ingest_queue` table (`id, source_uri, status,
  attempts, last_error, enqueued_at, updated_at, next_attempt_at`).
  Additive migration; v0.7 wikis upgrade transparently on first
  v0.8 connection.
- `tools/record-demo.sh` — re-record `docs/assets/demo.gif`. Uses
  vhs (preferred) or asciinema-agg (fallback). Drives the v0.8
  demo: fresh init → drop a file → watcher produces a page → ask
  auto-promotes → contradiction surfaces.

### Changed
- `[ingest] update_existing` defaults to **true**. The Karpathy
  gist describes "modify 10–15 relevant pages in a single pass"
  as the *default* shape ingest takes, not an opt-in. v0.6
  shipped this default-off because we were nervous about LLM
  cost; the recommended provider (Gemini Flash) is free, and on
  Anthropic Haiku ~$0.30/ingest with caching is fine for the
  target user. The validator still drops bad updates — flipping
  the default doesn't change the trust property, only the
  daily-use posture. Anthropic-on-credit-card users opt out via
  `[ingest] update_existing = false` (one config line).
- `internal/mcp` `serverVersion` bumped to `0.8.0-rc.1`.
- `PromoteAnswer` gains `PromoteOptions.Source` (`"auto" |
  "manual" | "session"`). `log.md` `**promote**` entries now
  carry a `src=` prefix so cron / MCP / session-capture promotes
  can be distinguished from manual ones at audit time.
- `cmd/init` config templates (gemini / anthropic / ollama /
  openai-compatible) gain `[watch]` and the `update_existing =
  true` line. Existing wikis without the new keys keep working —
  defaults are filled silently the same way `[ingest]` defaults
  have always been.

### Notes
- **Trust property reaffirmed.** Every page written via
  auto-promote, watch-mode ingest, or session-capture passes
  through `wiki.ValidateAndAttachEvidence`. No new write path
  bypasses the byte-exact substring validator. Auto-promote
  requires *both* the heuristic gate AND the validator: two
  locks; either failure leaves the answer in
  `.llmwiki/answers/` (never silently dropped, never written
  downgraded). The schema is not auto-edited; `AGENTS.md` is
  touched only by `init` and the user's editor.
- **DB v6 migration is additive only.** New `ingest_queue`
  table; `pages`, `evidence`, `sources`, `source_files`,
  `chunks`, `page_update_log`, `saved_answers` are byte-identical
  pre/post v6. Roll-forward only — no down-migration script.
- **URL/feed polling in watch mode is deferred to v0.9.x.** v0.8
  watch is local-fsnotify only; URL polling needs a separate
  goroutine, separate failure mode, and separate config knob set
  that we want to design after seeing how local-only feels.
- **No LLM-judged auto-promote scoring.** kytmanov-style "have
  the LLM grade itself" is unreliable and adds an LLM call per
  ask; the four mechanical signals (cites, evidence, length,
  hedging, BM25 dedup) are good enough.
- **Cursor / Codex session-capture variants deferred.** Claude
  Code is the primary integration today (we already have an MCP
  server for it); other-IDE Stop-hook adapters bolt on if/when
  users ask.

### Known issues / TODO
- `docs/assets/demo.gif` was not recorded in this release —
  neither vhs nor asciinema is installed on the release machine.
  Re-record via `tools/record-demo.sh` and replace the
  placeholder image link in `README.md`. Tracked as
  `TODO(release):` at the top of `docs/automation.md`.

### Inspiration
Builds on Karpathy's "modify 10–15 pages in one pass" default
and "good answers get filed back as new pages" line from the
[LLM-wiki gist](https://gist.github.com/karpathy/442a6bf555914893e9891c11519de94f);
the persistent retry queue with crash recovery borrows
[nashsu/llm_wiki](https://github.com/nashsu/llm_wiki)'s shape;
the auto-promote posture mirrors
[kytmanov/obsidian-llm-wiki-local](https://github.com/kytmanov/obsidian-llm-wiki-local)'s
`--auto-approve` daemon (we go further because we have a
validator they don't).

## [0.7.0-rc.1] — 2026-05-04

### Added
- `AGENTS.md` (or `CLAUDE.md`) at the wiki root — the user-editable
  schema doc. Defines the page ontology and the six prompts driving
  ingest / ask / update-existing / contradiction / promote-rewrite /
  lint. llmwiki looks for `AGENTS.md` first (multi-vendor convention;
  Cursor, OpenAI Codex, and Claude Code all read it), then falls
  back to `CLAUDE.md`. If both exist with identical bytes,
  AGENTS.md wins; if both exist and differ, llmwiki refuses to
  guess. Bundled defaults are byte-identical to v0.6 behaviour;
  user edits override per section. Karpathy's third layer.
- `llmwiki schema show [--bundled|--doc|--hash]` — inspect the
  effective schema (merged), the bundled default, the user doc
  verbatim, or just the active hash for scripting.
- `llmwiki schema validate` — structural validation with
  file:line errors; surfaces every problem at once via
  MultiError.
- `llmwiki schema migrate [--yes] [--dry-run]` — eager re-ingest
  of every page on a prior schema hash under the active schema.
  Resumable for free via per-page hash check.
- `llmwiki init` writes the schema doc alongside `.llmwiki/config.toml`.
  New `--rewrite-schema` flag overwrites an existing schema file
  (idempotent: by default `init` leaves an existing schema alone).
  New `--schema-file=CLAUDE.md` flag selects the dual-vendor filename.
- `mcp.get_schema` — new read-only MCP tool returning the active
  schema as a structured payload (schema_version, domain,
  ontology_fields, prompts.{...}, glossary, hash, doc_path). No
  `mcp.set_schema` — agents introspect, they do not edit.
- `pages.schema_hash TEXT NOT NULL DEFAULT ''` column at
  `user_version = 5`. Stamped on every WritePage write site
  (ingest, promote, cross-page update, mcp.write_page). Pre-v0.7
  rows get '', treated as "prior schema" by lint and status.
- `cmd/lint` surfaces `schema_drift: <n> pages on prior schema`
  warning.
- `cmd/status` surfaces a `schema:` line:
  `schema: AGENTS.md (hash 91e..., N pages on prior hash)` or
  `schema: bundled (no AGENTS.md), N pages on active hash`.
- Page ontology rename + reorder: a user can rename `evidence` to
  `citations`, `body` to `summary`, etc., and reorder fields in
  frontmatter emission. The canonical struct field carrying
  evidence quotes stays fixed; the rename is a naming convenience
  over disk emission only. The validator pins the *check* to the
  canonical struct field regardless of declared name.
- Extra-frontmatter pass-through: schemas can declare fields
  beyond the canonical set (e.g. `priority`); `Page.ExtraFrontmatter`
  round-trips them on Read/Write. The bundled validator does not
  check these values; truly new structured fields with their own
  validation are a v0.8+ question.

### Changed
- `internal/mcp` `serverVersion` bumped to `0.7.0-rc.1`.
- Six prompt sites in `internal/wiki/` (ingest, ask, lint
  contradictions, per-pair contradiction, cross-page update,
  promote rewrite) now render their system prompts via
  `schema.Schema.Render(...)`. The hard-coded `const` strings
  remain in place as test-only `*PromptForTests` exports for the
  byte-equality regression guard; they come out in v0.8.
- `WritePage` and `ParsePage` keep their legacy signatures and
  delegate to `*WithSchema` variants using `schema.Bundled()` —
  pre-v0.7 callers see no behaviour change.

### Fixed
- Default Gemini model bumped from `gemini-2.0-flash` (deprecated by
  Google for new users; HTTP 404 on first ingest) to
  `gemini-2.5-flash`. Affects the `init` config template, the
  `[providers.gemini].default_model` fallback, and the README's
  onboarding table. Existing wikis with `model = "gemini-2.0-flash"`
  pinned in their `config.toml` should update the line by hand.
  Cassette tests are unaffected — replay reads the recorded model
  name from the JSON payloads, not the live default.

### Notes
- **Schema migration v4 → v5 is additive only.** New
  `schema_hash` column on `pages`; no `ALTER TABLE` on other
  tables; `evidence`, `sources`, `source_files`, `chunks`,
  `page_update_log` are byte-identical pre/post v5. Roll-forward
  only — no down-migration script.
- **Q1 — schema doc filename: `AGENTS.md` (or `CLAUDE.md`) at
  wiki root**, not `.llmwiki/schema.md`. Karpathy alignment +
  multi-vendor convention. The original first cut was
  `.llmwiki/schema.md`; the user's directive prioritised Karpathy
  alignment + user-friendliness. AGENTS.md is the primary;
  `CLAUDE.md` is the native-filename fallback for Claude Code
  users who already have one.
- **Trust property reaffirmed.** The schema controls what the
  LLM is asked, not what counts as valid evidence. Substring-match
  validator is bundled and unreachable from the schema. Worst
  case from a malicious schema: degraded quality. Best case:
  better-shaped pages because the prompt fits the user's
  domain. README has the full reaffirmation.
- **Domain schema library** (`--schema=research-papers` etc.) is
  out of scope for v0.7 (Q10). Default ships "general-purpose
  wiki matching v0.6"; users hand-edit. v0.8+ question whether
  to ship bundled domain schemas.
- **Schema versioning** is `schema_version: 1` in frontmatter
  (Q11). v0.7 only knows version 1; future format changes bump
  the integer with a "this schema declares version 2; upgrade
  llmwiki" error.
- **`llmwiki schema diff`** is deferred to v0.8 (Q12). `git diff`
  over the schema doc does the same job for any user with
  `.llmwiki/` + `AGENTS.md` (or `CLAUDE.md`) under source control
  (recommended).
- **No per-call schema overrides over MCP** (Q15). Per-call
  overrides re-introduce the agent-edits-the-system-prompts
  confused-deputy surface. The schema is loaded once at server
  start.

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

[Unreleased]: https://github.com/mritunjaysharma394/llmwiki/compare/v0.7.0-rc.1...HEAD
[0.7.0-rc.1]: https://github.com/mritunjaysharma394/llmwiki/releases/tag/v0.7.0-rc.1
[0.6.0-rc.1]: https://github.com/mritunjaysharma394/llmwiki/releases/tag/v0.6.0-rc.1
[0.5.0-rc.1]: https://github.com/mritunjaysharma394/llmwiki/releases/tag/v0.5.0-rc.1
[0.4.0]: https://github.com/mritunjaysharma394/llmwiki/releases/tag/v0.4.0
[0.3.0]: https://github.com/mritunjaysharma394/llmwiki/releases/tag/v0.3.0
[0.2.0]: https://github.com/mritunjaysharma394/llmwiki/releases/tag/v0.2.0
[0.1.0]: https://github.com/mritunjaysharma394/llmwiki/releases/tag/v0.1.0
