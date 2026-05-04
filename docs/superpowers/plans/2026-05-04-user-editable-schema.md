# Sub-project 7 — User-editable Schema (Karpathy AGENTS.md alignment) (v0.7) — Implementation Plan

> **Version note:** v0.6.0-rc.1 ships sub-project 6b (cross-page page-update pass). This plan ships as `v0.7.0-rc.1` — the third Karpathy layer, the user-owned `AGENTS.md` schema doc that lifts the bundled prompts and page ontology out of the binary into a document the user reads and edits.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship v0.7 of `llmwiki` — sub-project 7, the user-editable schema layer. The six load-bearing prompts and the page ontology, today hard-coded into the Go binary across `internal/wiki/{ops,contradict,update_existing,promote}.go`, become *defaults*; the user-owned `AGENTS.md` at the wiki root overrides them. Editing prompts becomes a text-editor operation, not a fork-and-rebuild operation. The trust property — every evidence quote on disk substring-matches its named source file, byte-for-byte — stays bundled and unreachable from the schema. The schema controls what the LLM is *asked* and how the page is *shaped*; it does NOT control what counts as valid evidence. A v0.6 wiki opening under v0.7 with no `AGENTS.md` produces byte-identical output. Users opt in by creating the file, by `llmwiki init` (which now writes `AGENTS.md` alongside `.llmwiki/config.toml`), or by `llmwiki init --rewrite-schema`.

**Architecture:** One new package — `internal/schema/` — with `Schema`, `Prompts`, `Ontology`, `GlossaryTerm` types, a thin `Parse` (frontmatter + H2-section split, no third-party Markdown library), `Bundled()` (parses the embedded `default.md` once and caches), `Render(prompt, vars)` (regex `{{name}}` replacer, leaves unknown placeholders intact, warns once per unknown), and `Validate()` (required prompts, required placeholders, required ontology fields, with structured `{section, line, problem}` errors). The default doc lives at `internal/schema/default.md` embedded via `//go:embed`; byte-equality unit tests pin "no behaviour change vs v0.6" against the existing prompt strings at `internal/wiki/ops.go:63`, `:268`, `:344`, `internal/wiki/contradict.go:72`, `internal/wiki/update_existing.go:39`, `internal/wiki/promote.go:471`. Each of those six prompt-using sites takes a new `schema.Schema` parameter and replaces its hard-coded constant with a `sch.Render(sch.Prompts.X, vars)` call. The `Schema` value is loaded once per process in `cmd/root.go` after config load, stored on the `Config` carrier (mirroring how `database` and `llmClient` already live as package-level globals), and threaded into every wiki entrypoint that previously did not take a schema. One additive DB migration to `user_version = 5` adds a `schema_hash TEXT NOT NULL DEFAULT ''` column on `pages`; every `WritePage` write site stamps `schema_hash = sch.Hash`. New `cmd/schema.go` ships three subcommands (`schema show [--bundled|--doc]`, `schema validate`, `schema migrate [--yes] [--dry-run]`); `cmd/init.go` extends to write `AGENTS.md` alongside `config.toml` (idempotent: an existing schema doc is left alone unless `--rewrite-schema`); `cmd/lint.go` and `cmd/status.go` surface `schema_drift` counters; `internal/mcp/handlers.go` adds a read-only `mcp.get_schema` tool; `internal/mcp/server.go`'s `serverVersion` bumps from `"0.6.0-rc.1"` to `"0.7.0-rc.1"`.

**Tech Stack:** Go 1.26. **No new direct dependencies.** The schema parser is pure stdlib (`bufio`, `regexp`, `strings`, `crypto/sha256`); placeholder rendering is one regex (`\{\{(\w+)\}\}`); frontmatter is line-by-line `key: value`. We deliberately do NOT use Go's `text/template` (Resolved Q3: `{{name}}` is documented mustache-like, not Go-template-like; coupling the doc the user reads to a Go internal would be the wrong trade). The embedded default lives at `internal/schema/default.md` via `//go:embed default.md` so it round-trips byte-for-byte with the file `init` writes.

**Spec:** [`docs/superpowers/specs/2026-05-04-user-editable-schema-design.md`](../specs/2026-05-04-user-editable-schema-design.md), §Architecture (lines 278–563), §Trust-property reaffirmation (lines 565–589), §Risks (lines 591–615), §Implementation order (lines 667–704), §Verification (lines 760–828).

**Resolved open questions** (the spec lists fifteen; all resolved per the user's directive on 2026-05-04 — "user-friendly, fast, Karpathy-aligned, no compromise on quality"; this plan carries them in):

1. **Q1 — Schema doc filename:** **`AGENTS.md` at the wiki root**, not `.llmwiki/schema.md`. Karpathy alignment + multi-vendor convention (Cursor, OpenAI Codex, Claude Code all read AGENTS.md). The spec body still says `.llmwiki/schema.md` in places; the plan uses `AGENTS.md` throughout.
2. **Q2 — Format:** **structured Markdown with H2 sections** (`## Page ontology`, `## Ingest prompt`, `## Update-existing prompt`, `## Ask prompt`, `## Contradiction prompt`, `## Promote rewrite prompt`, `## Lint contradictions prompt`, `## Glossary`) with YAML-style frontmatter (`---` … `---`) for `schema_version` and `generator`. Same approach `internal/wiki/page.go` uses for page YAML.
3. **Q3 — Placeholder syntax:** **`{{name}}`**, regex-replaced. NOT `text/template` — we are not coupling the doc the user reads to a Go internal.
4. **Q4 — Required vs optional placeholders:** **errors on missing required, allows extras (forward-compat).** Removing `{{existing_titles}}` from the ingest prompt is a `schema validate` failure with `file:line`; introducing `{{my_extra}}` is silently passed through unfilled.
5. **Q5 — Migration strategy:** **lazy + opt-in `llmwiki schema migrate`.** New `schema_hash TEXT NOT NULL DEFAULT ''` column on `pages` at `user_version = 5`. The cross-page update pass (sub-project 6b) naturally re-stamps pages it touches; `schema migrate` is the "I want everything rebased now" opt-in that walks the whole wiki under per-page hash check (resumable for free).
6. **Q6 — Backwards compat:** **bundled defaults are byte-identical to v0.6 prompts.** A v0.6 wiki opening under v0.7 with no `AGENTS.md` sees zero behaviour change. Pre-v0.7 pages get `schema_hash = ''`, which the lint surface treats as "prior schema."
7. **Q7 — MCP exposure:** **dedicated `mcp.get_schema` read-only tool.** No `mcp.set_schema`, no per-call overrides — an agent that can rewrite the system prompts an agent runs against is a confused-deputy surface.
8. **Q8 — Schema vs trust property:** **the validator is bundled and unreachable from the schema.** `wiki.ValidateAndAttachEvidence` stays bundled Go; the schema cannot rewrite, swap, weaken, or skip it. Worst case from a malicious schema is degraded quality (more `update_failed`, fewer pages land); it cannot ground a false claim. Hard line.
9. **Q9 — Ontology extensibility:** **rename + reorder + extra-frontmatter pass-through.** Truly new structured fields with their own validation are a v0.8+ question.
10. **Q10 — Domain schema library** (`--schema=research-papers`, etc.): **out of scope for v0.7.** Default ships "general-purpose wiki matching v0.6 behaviour"; users hand-edit. v0.8+ question whether to ship bundled domain schemas (likely answered by community contributions).
11. **Q11 — Schema versioning:** **`schema_version: 1` in frontmatter.** v0.7 only knows version 1; future format changes bump the integer with a "this schema declares version 2; upgrade `llmwiki`" error.
12. **Q12 — `schema diff`:** **deferred to v0.8.** `git diff` over `AGENTS.md` (recommend `.llmwiki/` + `AGENTS.md` under source control in the README) does the same job.
13. **Q13 — Glossary placement:** **in `AGENTS.md` under `## Glossary`.** Single source of truth — a separate `.llmwiki/glossary.md` would make `schema validate` confusing about which file's hash determines drift.
14. **Q14 — `schema migrate` resumability:** **automatic via per-page hash check.** `migrate` walks pages whose `schema_hash != activeHash`; succeeded pages get the new hash; a `Ctrl-C` mid-run leaves a sound state.
15. **Q15 — MCP per-call schema overrides:** **no.** The MCP server loads the schema once at start; callers cannot override per-call. Per-call overrides re-introduce the agent-edits-the-system-prompts confused-deputy surface.

---

## File Structure

| File | Purpose | Action |
|---|---|---|
| `internal/schema/schema.go` | `Schema`, `Prompts`, `Ontology`, `OntologyField`, `GlossaryTerm`, `ValidationError` types; `Bundled()`, `Load(wikiRoot string)`, `Parse([]byte)` entrypoints; the `(s Schema) Hash() string` accessor; the `(s Schema) Validate() error` and `(s Schema) Render(prompt string, vars map[string]string) string` methods. Pure stdlib; no `wiki/` or `db/` import. | Create |
| `internal/schema/parser.go` | The frontmatter-then-H2-section splitter; the bullet-list ontology parser; the regex placeholder extractor; the structured-error ladder. | Create |
| `internal/schema/default.md` | The bundled-default schema doc. Embedded via `//go:embed`. Byte-identical content to the existing v0.6 prompts at the six call sites + v0.6 ontology field set. | Create |
| `internal/schema/default.go` | `//go:embed default.md` line; the package-level `var DefaultDoc []byte`; the cached `Bundled()` wrapper. | Create |
| `internal/schema/schema_test.go` | Round-trip on `default.md`; required-section / required-placeholder / malformed-frontmatter / unknown-version errors; `Render` correctness on `{{name}}`, unknown placeholders, empty maps; ontology rename map round-trip. | Create |
| `internal/schema/byte_equality_test.go` | Byte-equality test: `Bundled().Prompts.Ingest` equals `wiki.IngestSystemPromptForTests()`, similarly for the other five sites. Pins "no behaviour change." | Create |
| `internal/wiki/ops.go` | `IngestSourceFilesToPages` and `AnswerQuestion` / `StreamAnswer` and `DetectContradictions` gain a `sch schema.Schema` parameter; the three `const` system prompts (`ingestSystemPrompt`, `answerSystemPrompt`, the inline lint prompt) become `sch.Render(sch.Prompts.X, vars)` calls. The constants stay as exported `IngestSystemPromptForTests` / `AnswerSystemPromptForTests` / `LintContradictionsSystemPromptForTests` test-only mirrors so the byte-equality tests have something to pin against during transition. | Modify |
| `internal/wiki/contradict.go` | `DetectIngestContradictions` gains `sch schema.Schema`; `contradictionSystemPrompt` becomes `sch.Render(sch.Prompts.Contradiction, ...)`. | Modify |
| `internal/wiki/update_existing.go` | `UpdateExistingPagesFromSource` gains `sch schema.Schema` (passed via `UpdateExistingOptions.Schema`); `updateExistingSystemPrompt` becomes `sch.Render(sch.Prompts.UpdateExisting, vars)`. | Modify |
| `internal/wiki/promote.go` | `rewritePromoteBody` gains `sch schema.Schema`; the inline rewrite prompt becomes `sch.Render(sch.Prompts.PromoteRewrite, vars)`. | Modify |
| `internal/wiki/page.go` | `WritePage` reads field-name overrides from `sch.Ontology` (when non-default); `ParsePage` consults the ontology for declared field names + falls back to canonical field names for pre-v0.7 pages on disk. New helpers `WritePageWithSchema(p Page, wikiDir string, sch schema.Schema)` and `ParsePageWithSchema(content string, sch schema.Schema)`; `WritePage` and `ParsePage` keep their existing signatures and delegate with `schema.Bundled()` for backwards compat. | Modify |
| `internal/wiki/page_test.go` | Round-trip across a renamed schema (`evidence` → `citations`, `body` → `summary`); pre-v0.7 page (canonical field names on disk) parses fine under a renamed schema; extra-frontmatter pass-through for declared-but-unvalidated fields. | Modify |
| `cmd/root.go` | Globals gain `activeSchema schema.Schema`; `loadConfig` calls `schema.Load(wikiRoot)` after `applyIngestDefaults`; surfaces `schema validate` errors with file:line if the user-edited doc fails validation (returns a `cliutil.Wrap` so the CLI exits nonzero). Threads `activeSchema` into every command's call sites. | Modify |
| `cmd/root_test.go` | `TestLoadConfig_LoadsAGENTSMdWhenPresent`, `TestLoadConfig_FallsBackToBundledWhenAGENTSMdAbsent`, `TestLoadConfig_AGENTSMdValidationFails_ErrorsLoudly`. | Modify |
| `cmd/ingest.go` | `runIngest` reads `activeSchema` and passes it into `wiki.IngestSource` / `wiki.IngestRunResult` (the `IngestOptions` struct gains a `Schema schema.Schema` field). | Modify |
| `cmd/ask.go` | `runAsk` reads `activeSchema` and passes it into `wiki.AnswerQuestion` / `wiki.StreamAnswer`. | Modify |
| `cmd/promote.go` | `runPromote` reads `activeSchema` and passes it into `wiki.PromoteAnswer`. | Modify |
| `cmd/lint.go` | `runLint` reads `activeSchema`; surfaces `schema_drift: <n> pages on prior schema` warning when `db.CountPagesByHashState(activeSchema.Hash())` returns non-zero. | Modify |
| `cmd/status.go` | `runStatus` adds a `schema:` line: `schema: AGENTS.md (hash 91e..., 47 pages on prior hash a3f...)` or `schema: bundled (no AGENTS.md), N pages on prior hash` for pre-v0.7 wikis. | Modify |
| `cmd/init.go` | Adds `--rewrite-schema` flag; writes `AGENTS.md` alongside `.llmwiki/config.toml` at the wiki root (NOT inside `.llmwiki/`). Idempotent: an existing `AGENTS.md` is left alone unless `--rewrite-schema`. Output gains a "Wrote default schema at ./AGENTS.md" line. | Modify |
| `cmd/init_test.go` | `TestInit_WritesAGENTSMdAtWikiRoot`, `TestInit_LeavesExistingAGENTSMdAlone`, `TestInit_RewriteSchemaFlagOverwrites`. | Modify |
| `cmd/schema.go` | New file. `schemaCmd`, `schemaShowCmd`, `schemaValidateCmd`, `schemaMigrateCmd`. `show` reads `Load(wikiRoot)`, prints merged-effective / `--bundled` / `--doc` modes. `validate` calls `(s Schema).Validate()`, prints structured `file:line` errors, exits 0 on success / 1 on failure. `migrate` walks pages with non-matching hash, re-ingests under the active schema, supports `--yes` + `--dry-run`. | Create |
| `cmd/schema_test.go` | `TestSchemaShow_BundledByDefault`, `TestSchemaShow_DocFlagPrintsAGENTSMd`, `TestSchemaShow_BundledFlagIgnoresAGENTSMd`, `TestSchemaValidate_OK`, `TestSchemaValidate_MissingRequiredSection_Errors`, `TestSchemaValidate_MissingRequiredPlaceholder_Errors`, `TestSchemaMigrate_DryRunDoesNotWriteDisk`, `TestSchemaMigrate_SkipsPagesAtActiveHash` (resumability). | Create |
| `internal/db/db.go` | v5 migration: `ALTER TABLE pages ADD COLUMN schema_hash TEXT NOT NULL DEFAULT ''`. Wrapped in a `PRAGMA table_info(pages)` check (defensive shape matching the v3 / v4 migrations). Bumps `PRAGMA user_version` to 5. | Modify |
| `internal/db/db_test.go` | `TestMigrate_FromV4_AddsSchemaHash`, `TestMigrate_FromFresh_LandsAtV5`, `TestMigrate_Idempotent_RerunningOnV5_IsNoop`, `TestMigrate_PreV5RowsHaveEmptySchemaHash`. | Modify |
| `internal/db/queries.go` | `UpdateSchemaHash(pageID, hash string)`, `CountPagesByHashState(activeHash string) (current, prior int, err error)`, `ListPagesByHash(hash string, limit int) ([]PageRecord, error)`. | Modify |
| `internal/db/queries_test.go` | Per-method unit tests against an in-memory v5 DB. | Modify |
| `internal/wiki/ingest_runner.go` | Every `WritePage` call site (the persist loop after `IngestSourceFilesToPages`, the second retro-link write site, the cross-page update write site already lives in `update_existing.go`) calls `database.UpdateSchemaHash(pageID, opts.Schema.Hash())` after a successful write. | Modify |
| `internal/wiki/promote.go` | `PromoteAnswer`'s final `WritePage` write also calls `database.UpdateSchemaHash`. | Modify |
| `internal/wiki/update_existing.go` | The "step 8 disk + DB write" block stamps `schema_hash = sch.Hash()` alongside the existing `DeleteEvidenceForPage` + `InsertEvidence` operations. | Modify |
| `internal/mcp/server.go` | `serverVersion` bumps from `"0.6.0-rc.1"` to `"0.7.0-rc.1"`; `getSchemaTool()` registered alongside the existing seven tools. | Modify |
| `internal/mcp/handlers.go` | `getSchemaHandler` returns the active schema as a structured payload; `Deps` gains a `Schema schema.Schema` field; existing handlers (`ingestHandler`, `askHandler`, `promoteAnswerHandler`, `writePageHandler`) thread the schema into wiki entrypoints; `writePageHandler`'s post-write path calls `UpdateSchemaHash`. | Modify |
| `internal/mcp/server_test.go` | `TestServerVersionIs070`, `TestGetSchema_BundledByDefault`, `TestGetSchema_ReturnsActivePromptsAndOntology`, `TestGetSchema_ReadOnly_NoSetSchemaTool`. | Modify |
| `internal/llm/testdata/cassettes/TestSchemaRenameRoundtrip__*.json` | Recorded cassette: pre-seed a wiki with bundled defaults; install a renamed schema (`evidence` → `citations`, `body` → `summary`); ingest one source; assert the renamed frontmatter keys appear on disk and `schema_hash` matches. | Create |
| `internal/llm/testdata/cassettes/TestSchemaMigrate__*.json` | Recorded cassette: pre-seed 5 pages under bundled defaults; cosmetically edit `AGENTS.md`'s `## Domain` section (changes hash); run `llmwiki schema migrate --yes`; assert all 5 pages reach the new hash and a `**schema_migrate**` log entry is appended. | Create |
| `internal/llm/testdata/cassettes/TestMCPGetSchema__*.json` | Recorded cassette (or pure in-process: this one needs no LLM): drive the MCP server in-process, call `get_schema`, assert the structured payload includes the active prompts, ontology, hash, and doc path. | Create |
| `cmd/schema_integration_test.go` | `TestSchemaRenameRoundtrip` and `TestSchemaMigrate` cassette tests. Skip-when-no-cassette pattern from sub-project 6b's Phase G. | Create |
| `internal/mcp/server_integration_test.go` (or extend) | `TestMCPGetSchema` — pure in-process, no LLM. | Modify |
| `README.md` | New "Customising your wiki" section pointing at `AGENTS.md`; references Karpathy's gist; reaffirms trust property is bundled. Updates the existing "Trust Property" and "MCP" sections to mention the schema layer. | Modify |
| `CHANGELOG.md` | `## [0.7.0-rc.1] — 2026-05-04` entry covering the schema lift, the v5 migration, the new commands, the MCP `get_schema` tool, the resolved Q1 (AGENTS.md filename). | Modify |
| (tag) | `v0.7.0-rc.1` annotated tag, local only — do NOT push. | Create |

**Total:** 21 tasks across 12 phases (A–L). Each task ends with a single commit; the working tree is green at every commit boundary (`go build ./... && go vet ./... && go test ./...` clean in replay mode).

---

## Phase summaries

Each phase is self-contained: it does not depend on later-phase exports, and its last task leaves the tree compiling and `go test ./...` green so a fresh subagent can pick up the next phase from a clean checkout. **The trust-property reaffirmation is loud at every phase that touches a write site (Phases B, D, F, J).** The spec's hard line is "the schema cannot loosen the validator"; each affected commit message says so.

- **Phase A — `internal/schema/` package (Tasks 1–3).** New package with zero dependencies on `wiki/` or `db/`. Task 1 ships the types + `Parse` + structured `ValidationError`; Task 2 ships the embedded `default.md` + `Bundled()` + cache; Task 3 ships `Render` + `Validate`. Pure unit tests against the embedded default and against fixture schema docs (round-trip, missing required section, missing required placeholder, malformed frontmatter, unknown `schema_version`).

- **Phase B — Bundled-default extraction (Tasks 4–5).** Carve the six existing prompt strings out of `wiki/` into `internal/schema/default.md`. Each prompt-using site gains a `sch schema.Schema` parameter and replaces its hard-coded constant with a `sch.Render(...)` call. The constants stay as test-only `*PromptForTests` exports for the byte-equality tests in Phase A's Task 3 to pin against. **TRUST PROPERTY REAFFIRMED:** the validator (`ValidateAndAttachEvidence`) receives no schema input and is not parameterised by the schema in any way; this is deliberate and the commit message says so.

- **Phase C — Wire `Schema` through `cmd/` (Task 6).** Single `schema.Load` call at startup in `cmd/root.go`'s `loadConfig`, after `applyIngestDefaults`. Stored on the existing `cfg` carrier as a sibling to the package-level `database` and `llmClient` globals. `cmd/ingest.go`, `cmd/ask.go`, `cmd/promote.go`, `cmd/lint.go` all gain a one-line read of `activeSchema` and a one-line pass through to the wiki entrypoint. ~30 mechanical call-site edits.

- **Phase D — `db` v5 migration + `schema_hash` (Tasks 7–8).** Additive `ALTER TABLE pages ADD COLUMN schema_hash TEXT NOT NULL DEFAULT ''`. Wrapped in a `PRAGMA table_info(pages)` check so re-running on v5 is a no-op (matches the v3 / v4 migrations' shape). Three new queries: `UpdateSchemaHash(pageID, hash)`, `CountPagesByHashState(activeHash)` (returns `(current, prior, err)` tuple), `ListPagesByHash(hash, limit)` for `schema migrate`'s resumability check. Every `WritePage` write site stamps `schema_hash = sch.Hash()` after a successful write. **TRUST PROPERTY REAFFIRMED:** `schema_hash` is a metadata stamp; the validator runs first and gates the write; failed validation means the row is not written and `UpdateSchemaHash` is not called.

- **Phase E — `cmd/schema.go` (Tasks 9–10).** Task 9 ships `schema show [--bundled|--doc]` + `schema validate`; Task 10 is a pure-CLI / pure-DB task with no LLM dependency. Pure unit tests via `cobra.Command.SetArgs` + `bytes.Buffer` capture.

- **Phase F — `schema migrate` (Task 11).** Walks pages with `schema_hash != activeSchema.Hash()` (resumable for free); per page, reads its `source_ids` via the existing helper, re-runs `IngestSourceFilesToPages` under the active schema, runs `ValidateAndAttachEvidence` as usual; pages whose proposed body fails validation **stay at their prior version** (the same `update_failed` shape sub-project 6b uses). Supports `--yes` + `--dry-run`. **TRUST PROPERTY REAFFIRMED:** `schema migrate` does NOT touch the validator, the trust property, or the `evidence` rows on pages it cannot improve. Opt-in, expensive, reversible by `git restore .llmwiki/wiki/`.

- **Phase G — `cmd/init.go` extension (Task 12).** Writes `AGENTS.md` alongside `.llmwiki/config.toml` at the wiki root (NOT inside `.llmwiki/`). Output gains a "Wrote default schema at ./AGENTS.md" line. New `--rewrite-schema` flag overwrites an existing schema file (idempotency: by default `init` leaves an existing schema doc alone).

- **Phase H — Lint + status surfaces (Task 13).** `cmd/lint.go` and `cmd/status.go` query `db.CountPagesByHashState(activeSchema.Hash())` and surface drift counters: `schema_drift: <n> pages on prior schema` warning in lint output; `schema: AGENTS.md (hash 91e..., N pages on prior hash a3f...)` line in status output.

- **Phase I — MCP `get_schema` (Task 14).** New read-only `mcp.get_schema` tool registered alongside the existing seven; `Deps` gains a `Schema schema.Schema` field; existing handlers (`ingestHandler`, `askHandler`, `promoteAnswerHandler`, `writePageHandler`) thread `d.Schema` into the wiki entrypoints; `writePageHandler`'s post-write path stamps `schema_hash`. `serverVersion` bumps from `"0.6.0-rc.1"` to `"0.7.0-rc.1"`. **No `mcp.set_schema`, no per-call overrides** (Q15) — an agent that can rewrite the system prompts an agent runs against is a confused-deputy surface.

- **Phase J — Ontology rename plumbing (Tasks 15–16).** Task 15: `WritePage` reads field-name overrides from `sch.Ontology` and emits the user's chosen names in the user's chosen order; round-trip tests across a renamed schema. Task 16: `ParsePage` reads back via the same map; pre-v0.7 pages (canonical field names on disk) parse fine under a renamed schema via the bundled-default name set as a fallback; extra-frontmatter pass-through for fields the schema declares but the bundled validator ignores. **TRUST PROPERTY REAFFIRMED:** the canonical struct field carrying evidence quotes is fixed; the rename is a name-string mapping over `WritePage` / `ParsePage` only; the validator pins the *check* to the canonical struct field regardless of what the user named it.

- **Phase K — Cassettes (Tasks 17–19).** Three cassettes per spec: `TestSchemaRenameRoundtrip` (Task 17), `TestSchemaMigrate` (Task 18), `TestMCPGetSchema` (Task 19, no LLM — pure in-process). Skip-when-no-cassette pattern matching sub-project 6b's Phase G. Recording target is Gemini Flash for the heavy fan-out (cassette refresh stays free).

- **Phase L — Docs + tag (Tasks 20–21).** README adds a "Customising your wiki" section leading with `AGENTS.md`, references Karpathy's gist, reaffirms trust property is bundled, cites `schema show --bundled` as the way to discover defaults. The existing "Trust Property" and "MCP" sections gain one-paragraph updates on the schema layer. CHANGELOG `[0.7.0-rc.1]` covers the lift. Tag `v0.7.0-rc.1` locally — no push.

---

## Phase A — `internal/schema/` package

### Task 1: `schema.Schema` types + `Parse` + structured `ValidationError`

**Files:**
- Create: `internal/schema/schema.go`
- Create: `internal/schema/parser.go`
- Create: `internal/schema/schema_test.go`

- [ ] **Step 1: Write failing tests**

Create `internal/schema/schema_test.go` with fixture schema docs as Go string constants (no testdata files yet; the embedded `default.md` lands in Task 2).

1. `TestParse_FrontmatterRoundTrip` — parse a fixture with `---\nschema_version: 1\ngenerator: llmwiki\n---\n# title\n\n## Domain\nA test wiki.\n` and assert `Schema{Version: 1, Generator: "llmwiki"}`.
2. `TestParse_ExtractsAllRequiredSections` — fixture with all eight required sections (`Domain`, `Page ontology`, `Ingest prompt`, `Update-existing prompt`, `Ask prompt`, `Contradiction prompt`, `Promote rewrite prompt`, `Lint contradictions prompt`); assert each is populated.
3. `TestParse_GlossaryOptional_AbsentParsesEmpty` — fixture without `## Glossary`; assert `len(s.Glossary) == 0`, no error.
4. `TestParse_GlossaryPresent_ParsesBulletList` — `## Glossary\n  - foo: a foo thing\n  - bar: a bar thing\n`; assert two `GlossaryTerm` entries.
5. `TestParse_OntologyParsesBulletList` — `## Page ontology\n  - title (string)  the page's primary key\n  - body (markdown) the page's narrative\n  - evidence (list of quotes) required\n`; assert three `OntologyField` entries with `Name`, `Type`, `Description` fields populated.
6. `TestParse_MissingRequiredSection_Errors_WithLineNumber` — fixture missing `## Ingest prompt`; assert error of type `ValidationError` with `Section: "Ingest prompt"`, `Problem: "required section missing"`, exit code 1 path.
7. `TestParse_UnknownSchemaVersion_Errors` — `schema_version: 2`; assert `ValidationError` with `Problem: "unknown schema_version: 2 (this binary supports version 1)"`.
8. `TestParse_MalformedFrontmatter_Errors` — `---\nschema_version not_an_int\n---\n`; assert `ValidationError` with line number pointing at the bad line.
9. `TestParse_NoFrontmatter_Errors` — content without leading `---\n`; assert `ValidationError{Problem: "schema doc must begin with frontmatter"}`.
10. `TestParse_DuplicateSection_Errors` — two `## Domain` headings; assert `ValidationError{Problem: "duplicate section"}`.

- [ ] **Step 2: Run tests and confirm failure**

Run: `go test ./internal/schema/ -run TestParse -v`
Expected: FAIL — package does not exist.

- [ ] **Step 3: Implement `internal/schema/schema.go` and `parser.go`**

`schema.go` — types and constructors:

```go
// Package schema implements the user-editable wiki schema doc — the
// third Karpathy layer (after raw sources and the wiki itself). The
// schema is a Markdown document the user reads and edits at AGENTS.md
// in the wiki root; it controls what the LLM is *asked* and how the
// page is *shaped*. It does NOT control what counts as valid evidence —
// wiki.ValidateAndAttachEvidence is bundled and runs after every LLM
// call regardless of what the schema-rendered prompt told the LLM.
//
// The trust property holds at the schema boundary: a malicious or
// compromised schema can degrade page quality (more drops, fewer
// pages land), but it cannot ground a false claim, because the
// substring-match validator gates every page that reaches disk.
package schema

import (
    "crypto/sha256"
    "fmt"
    "regexp"
    "strings"
)

const SchemaFormatVersion = 1

type Schema struct {
    Version   int
    Generator string
    Domain    string
    Ontology  Ontology
    Prompts   Prompts
    Glossary  []GlossaryTerm

    // raw is the on-disk bytes (or the embedded default for Bundled()).
    // Hash() returns sha256(raw) so two semantically-identical docs
    // with whitespace differences are treated as distinct schemas —
    // matches user intuition (re-saved file = new hash = drift surface).
    raw []byte

    // DocPath is "AGENTS.md" when Load read the file, "" for Bundled().
    // Surfaced in `schema show` and `mcp.get_schema`.
    DocPath string
}

type Prompts struct {
    Ingest               string // {{domain}}, {{existing_titles}}, optional {{glossary}}
    UpdateExisting       string // {{domain}}, {{existing_page_body}}, {{existing_evidence}}, optional {{glossary}}
    Ask                  string // {{domain}}, optional {{glossary}}
    Contradiction        string // (no required placeholders)
    PromoteRewrite       string // {{question}}, {{answer_body}}, {{evidence_quotes}}
    LintContradictions   string // (no required placeholders)
}

type Ontology struct {
    Fields []OntologyField
}

type OntologyField struct {
    // CanonicalName is the bundled struct field name (e.g. "evidence",
    // "title"). Maps via position to the bundled canonical list:
    // [title, body, evidence, links, sources, tags, created,
    //  updated_at, content_hash, source_ids]. The mapping is
    // position-stable across renames.
    CanonicalName string
    // DeclaredName is what the user wrote (e.g. "citations" if they
    // renamed `evidence`). Equal to CanonicalName for an unrenamed
    // schema. Read on WritePage; consulted on ParsePage.
    DeclaredName string
    Type         string
    Description  string
}

type GlossaryTerm struct {
    Term       string
    Definition string
}

type ValidationError struct {
    Section string
    Line    int
    Problem string
}

func (e ValidationError) Error() string {
    if e.Line > 0 {
        return fmt.Sprintf("schema validation: %s (line %d): %s", e.Section, e.Line, e.Problem)
    }
    return fmt.Sprintf("schema validation: %s: %s", e.Section, e.Problem)
}

// Hash returns the sha256 of the on-disk doc bytes. Used by
// db.schema_hash to gate the lint/status drift surface and by
// `schema migrate` to skip already-migrated pages.
func (s Schema) Hash() string {
    if len(s.raw) == 0 {
        return "bundled"
    }
    return fmt.Sprintf("%x", sha256.Sum256(s.raw))
}

// Raw returns the on-disk bytes; used by `schema show --doc`.
func (s Schema) Raw() []byte { return s.raw }
```

`parser.go` — the section splitter and the line-by-line frontmatter:

```go
// canonical ontology order — fixed, bundled. Rename is a name-string
// mapping over this list; reorder reorders the slice in the parsed
// schema.Ontology.Fields without changing the canonical mapping.
var canonicalOntologyFields = []string{
    "title", "body", "evidence", "links", "sources",
    "tags", "created", "updated_at", "content_hash", "source_ids",
}

var requiredSections = []string{
    "Domain",
    "Page ontology",
    "Ingest prompt",
    "Update-existing prompt",
    "Ask prompt",
    "Contradiction prompt",
    "Promote rewrite prompt",
    "Lint contradictions prompt",
}

// Parse splits the doc into frontmatter + sections, validates required
// sections are present, populates Schema fields. Returns ValidationError
// (typed) on structural failures so callers can render file:line.
func Parse(raw []byte) (Schema, error) {
    s := Schema{raw: raw}
    // 1. frontmatter: ---\nkey: value\nkey: value\n---\n
    // 2. body: split on ^## headers, map section name -> body text
    // 3. populate Domain, Ontology, Prompts, Glossary from sections
    // 4. enforce required sections present
    // 5. enforce schema_version is 1
    // ... ~150 LOC, pure stdlib (bufio.Scanner + strings.HasPrefix).
}
```

The implementer fills in the body following the test fixtures' shapes; the test surface in Step 1 pins the contract.

- [ ] **Step 4: Run tests and confirm pass**

Run: `go test ./internal/schema/ -run TestParse -v`
Expected: PASS — ten subtests green.

Run: `go test ./...`
Expected: green (no callers yet).

- [ ] **Step 5: Commit**

```bash
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
feat(schema): internal/schema package — types + Parse + structured ValidationError

The first Karpathy-third-layer commit. New package internal/schema
ships pure stdlib types (Schema, Prompts, Ontology, OntologyField,
GlossaryTerm) and a thin Parse that splits a schema doc into
frontmatter + H2-section body, enforces eight required sections
(Domain, Page ontology, Ingest prompt, Update-existing prompt, Ask
prompt, Contradiction prompt, Promote rewrite prompt, Lint
contradictions prompt), and returns structured ValidationError with
section + line + problem so cmd/schema.go's `schema validate` can
render file:line columns.

Zero dependencies on wiki/ or db/. The package can be imported by
both. Render and Validate land in Task 3; Bundled() lands in Task
2 alongside the embedded default.md.

The canonical ontology field order is fixed and bundled
([title, body, evidence, links, sources, tags, created, updated_at,
content_hash, source_ids]) — rename is a name-string mapping over
this list; reorder is an order-of-Fields reordering. Q9 ships
rename + reorder + extra-frontmatter pass-through; truly new
structured fields with their own validation are a v0.8+ question.

TRUST PROPERTY (pre-emptive). The schema package has zero references
to ValidateAndAttachEvidence and zero pathways into wiki/. The
schema controls what the LLM is asked; the bundled validator
controls what counts as valid evidence. The two layers are
deliberately uncoupled.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: `default.md` + `Bundled()` + cache

**Files:**
- Create: `internal/schema/default.md`
- Create: `internal/schema/default.go`
- Modify: `internal/schema/schema_test.go`

- [ ] **Step 1: Write `internal/schema/default.md`**

The file is the byte-identical canonical schema doc. It MUST embed the v0.6 prompts byte-for-byte from their current homes (`internal/wiki/ops.go:63`, `:268`, `:344`, `internal/wiki/contradict.go:72`, `internal/wiki/update_existing.go:39`, `internal/wiki/promote.go:471`) so the byte-equality test in Task 5 passes without re-recording any cassette.

Skeleton (full content lands in this commit; the implementer copy-pastes from each source-of-truth):

```markdown
---
schema_version: 1
generator: llmwiki
---

# llmwiki schema

This document defines how `llmwiki` shapes pages and prompts the LLM.
It is YOUR document — edit it to fit your domain. The bundled defaults
match `v0.7.0-rc.1`'s behaviour.

The trust property is bundled and not configurable here: every evidence
quote on disk substring-matches its named source file, byte-for-byte.
This document controls what the LLM is *asked* and how the page is
*shaped*. It does NOT control what counts as valid evidence.

## Domain

A general-purpose wiki. Pages capture concepts, components, decisions,
and the relationships between them.

## Page ontology

  - title         (string)         the page's primary key; unique per wiki
  - body          (markdown)       the page's narrative
  - evidence      (list of quotes) verbatim spans from sources; required, ≥ 1
  - links         (list)           Obsidian wikilinks declared structurally
  - sources       (list of paths)  derived from evidence; emitted by WritePage
  - tags          (list of strings) Obsidian/Dataview-friendly
  - created       (date)           first-ingest date
  - updated_at    (RFC3339 ts)     last-write timestamp
  - content_hash  (sha256)         body hash; recomputed at every write
  - source_ids    (list of int)    DB row IDs backing this page

## Ingest prompt

[byte-identical content from internal/wiki/ops.go:63's ingestSystemPrompt,
 with {{domain}} interpolated where the v0.6 prompt currently has no
 domain string and {{existing_titles}} where it currently has the
 inline "Existing wiki pages (titles only):" block. The byte-equality
 test in Task 5 pins this — run it, see the diff, fix until it passes.]

## Update-existing prompt

[byte-identical content from internal/wiki/update_existing.go:39's
 updateExistingSystemPrompt, with {{domain}}, {{existing_page_body}},
 {{existing_evidence}} interpolated where the v0.6 prompt currently
 builds those strings inline.]

## Ask prompt

[byte-identical content from internal/wiki/ops.go:268's
 answerSystemPrompt, with {{domain}} interpolated.]

## Contradiction prompt

[byte-identical content from internal/wiki/contradict.go:72's
 contradictionSystemPrompt. No placeholders.]

## Promote rewrite prompt

[byte-identical content from internal/wiki/promote.go:471's inline
 system prompt, with {{question}}, {{answer_body}}, {{evidence_quotes}}
 interpolated.]

## Lint contradictions prompt

[byte-identical content from internal/wiki/ops.go:344's inline lint
 system prompt. No placeholders.]

## Glossary

(empty by default; add domain-specific terms here, one per line:
  - <term>: <one-sentence definition>)
```

**The byte-equality contract is enforced by Phase B's tests (Task 5), not by Task 2.** Task 2 ships the embedded-default machinery; Task 5 verifies the bytes match and adjusts `default.md` if Task 2's first cut diverges.

- [ ] **Step 2: Write `internal/schema/default.go`**

```go
package schema

import (
    _ "embed"
    "sync"
)

//go:embed default.md
var DefaultDoc []byte

var (
    bundledOnce sync.Once
    bundled     Schema
    bundledErr  error
)

// Bundled returns the parsed embedded default schema. Cached after
// the first call. Used by the cmd/wiki entrypoints when AGENTS.md
// is absent (a v0.6 wiki opening under v0.7).
func Bundled() Schema {
    bundledOnce.Do(func() {
        bundled, bundledErr = Parse(DefaultDoc)
    })
    if bundledErr != nil {
        // The embedded default is checked into the binary; a parse
        // error here is a programmer bug, not a runtime condition.
        // Panic at first use is the right shape — a CI build that
        // breaks the embedded doc fails the whole package's tests.
        panic("internal/schema: bundled default fails to parse: " + bundledErr.Error())
    }
    return bundled
}

// Load reads <wikiRoot>/AGENTS.md and parses it; falls back to
// Bundled() when the file is absent. Validates structure on success;
// callers (cmd/root.go's loadConfig) bubble ValidationError up so the
// CLI exits with file:line on failure.
func Load(wikiRoot string) (Schema, error) {
    path := filepath.Join(wikiRoot, "AGENTS.md")
    data, err := os.ReadFile(path)
    if errors.Is(err, os.ErrNotExist) {
        return Bundled(), nil
    }
    if err != nil { return Schema{}, err }
    s, err := Parse(data)
    if err != nil { return Schema{}, err }
    s.DocPath = "AGENTS.md"
    return s, nil
}
```

- [ ] **Step 3: Append tests to `schema_test.go`**

1. `TestBundled_Parses_NoError` — call `Bundled()`; assert no panic, all required sections populated, version 1.
2. `TestBundled_HashIsLiteralString` — `Bundled().Hash() == "bundled"` (the sentinel for `len(raw) == 0`; rationale: the embedded doc *does* have raw bytes, so this is wrong — fix: `Bundled()` sets `s.raw = DefaultDoc` and `Hash()` computes `sha256(DefaultDoc)` like any other schema). Adjust the test to assert a non-empty hex hash that is stable across runs.
3. `TestBundled_AllRequiredPromptsNonEmpty` — assert `Bundled().Prompts.Ingest != ""`, etc., for all six prompts.
4. `TestLoad_FallsBackToBundledWhenAGENTSMdAbsent` — `tmpDir := t.TempDir()` (no AGENTS.md); `Load(tmpDir)` returns `Bundled()`-equivalent (same hash) with `DocPath == ""`.
5. `TestLoad_ParsesAGENTSMdWhenPresent` — write a fixture `AGENTS.md` to tmp; `Load(tmpDir)` returns parsed schema with `DocPath == "AGENTS.md"`, hash is `sha256(<the file's bytes>)`.

**Decision point for `Hash()` on Bundled:** the simpler shape is `s.raw = DefaultDoc` for `Bundled()` and `Hash()` always computes `sha256(s.raw)`. This means `Bundled().Hash()` is a real hex hash, equal to `sha256(DefaultDoc)`. That's better than a sentinel — `db.schema_hash` rows for pages ingested under bundled defaults all carry the same hex hash, the lint surface treats it like any other hash, and a user who later writes an `AGENTS.md` byte-identical to the bundled default sees zero drift. **Adopt this shape; remove the `"bundled"` sentinel from Task 1's `Hash()` body.**

- [ ] **Step 4: Run tests and confirm pass**

Run: `go test ./internal/schema/ -v`
Expected: PASS — all subtests green.

- [ ] **Step 5: Commit**

```bash
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
feat(schema): default.md + Bundled() + Load(wikiRoot)

internal/schema/default.md is the embedded canonical schema doc.
Bundled() parses it once (sync.Once-cached) and is the entrypoint
when AGENTS.md is absent — pre-v0.7 wikis opening under v0.7 see
zero behaviour change because the bundled prompts are the v0.6
prompts byte-for-byte (Phase B's byte-equality test pins this).

Load(wikiRoot) reads <wikiRoot>/AGENTS.md (Q1 — the schema doc lives
at the wiki root, not inside .llmwiki/, so it's discoverable on `ls`
without a hidden-dir traversal and matches the multi-vendor AGENTS.md
convention Cursor / OpenAI Codex / Claude Code all read). Falls back
to Bundled() when the file is absent. Validates structure on success;
ValidationError bubbles up so cmd/root.go's loadConfig can render
file:line on failure.

Hash() computes sha256 of the raw bytes — same shape for Bundled()
and for a user-edited AGENTS.md, so db.schema_hash rows get treated
uniformly (no sentinel-vs-real-hash branching at every read site).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: `Render` + `Validate`

**Files:**
- Modify: `internal/schema/schema.go`
- Modify: `internal/schema/parser.go`
- Modify: `internal/schema/schema_test.go`

- [ ] **Step 1: Write failing tests**

Append to `schema_test.go`:

1. `TestRender_InterpolatesKnownPlaceholders` — `Render("hello {{name}}", {"name": "world"})` returns `"hello world"`.
2. `TestRender_LeavesUnknownPlaceholdersIntact` — `Render("hello {{name}}", {})` returns `"hello {{name}}"` (forward-compat: a future binary may interpolate them).
3. `TestRender_WarnsOncePerUnknownPlaceholder` — capture stderr; call `Render` twice with the same unknown placeholder; assert exactly one WARN line.
4. `TestRender_HandlesEmptyVarsMap` — `Render("no placeholders", nil)` returns `"no placeholders"`.
5. `TestRender_MultiplePlaceholders` — `Render("{{a}} {{b}} {{a}}", {"a":"X","b":"Y"})` returns `"X Y X"`.
6. `TestValidate_BundledIsValid` — `Bundled().Validate()` returns nil.
7. `TestValidate_MissingRequiredPlaceholder_Errors` — fixture with `## Ingest prompt` text but missing `{{domain}}`; `Validate()` returns `ValidationError{Section: "Ingest prompt", Problem: "missing required placeholder {{domain}}"}`.
8. `TestValidate_MissingRequiredOntologyField_Errors` — fixture with `## Page ontology` missing `evidence`; `Validate()` returns `ValidationError{Section: "Page ontology", Problem: "missing required field: evidence"}`.
9. `TestValidate_OntologyRenameAllowed` — fixture renaming `evidence` to `citations`; `Validate()` returns nil (rename is fine, the canonical field still exists at position 3).
10. `TestValidate_OntologyDropRequiredField_Errors` — fixture without title/body/evidence at all; one error per missing required field, all surfaced at once (don't bail on the first).

- [ ] **Step 2: Run tests and confirm failure**

Run: `go test ./internal/schema/ -run "TestRender|TestValidate" -v`
Expected: FAIL.

- [ ] **Step 3: Implement `Render` and `Validate`**

```go
var placeholderRE = regexp.MustCompile(`\{\{(\w+)\}\}`)

// Render replaces every {{name}} in prompt with vars[name]. Unknown
// names are left intact (forward-compat: a future binary may
// interpolate them). Emits one WARN per unknown name per process
// (warnedOnce sync.Map keyed on prompt hash + name).
func (s Schema) Render(prompt string, vars map[string]string) string {
    return placeholderRE.ReplaceAllStringFunc(prompt, func(m string) string {
        name := m[2 : len(m)-2]
        if v, ok := vars[name]; ok { return v }
        warnUnknownPlaceholderOnce(prompt, name)
        return m
    })
}

// Validate enforces the required-prompt + required-placeholder +
// required-ontology-field contracts. Returns the FIRST ValidationError
// for cmd/root.go's loadConfig (which exits 1 on any failure) but
// surfaces ALL errors via the (multierr-style) Errors slice for
// `schema validate` to render every problem at once.
//
// Required prompts: all six (Ingest, UpdateExisting, Ask, Contradiction,
// PromoteRewrite, LintContradictions) — Q4 plus the Karpathy-aligned
// PromoteRewrite + LintContradictions sites. Optional prompts: none.
//
// Required placeholders per prompt:
//   Ingest:           {{domain}}, {{existing_titles}}
//   UpdateExisting:   {{domain}}, {{existing_page_body}}, {{existing_evidence}}
//   Ask:              {{domain}}
//   Contradiction:    (none)
//   PromoteRewrite:   {{question}}, {{answer_body}}, {{evidence_quotes}}
//   LintContradictions: (none)
//
// Required ontology fields: title, body, evidence (the bundled
// validator pins the *check* to the canonical struct field at position
// 3, regardless of what the user named it — Q8).
func (s Schema) Validate() error {
    var errs []ValidationError
    // ... walks prompts, applies per-prompt placeholder requirements,
    // walks ontology, applies required-field requirements.
    if len(errs) == 0 { return nil }
    return MultiError{Errors: errs}
}

type MultiError struct{ Errors []ValidationError }

func (m MultiError) Error() string {
    var sb strings.Builder
    for i, e := range m.Errors {
        if i > 0 { sb.WriteByte('\n') }
        sb.WriteString(e.Error())
    }
    return sb.String()
}
```

- [ ] **Step 4: Run tests and confirm pass**

Run: `go test ./internal/schema/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
feat(schema): Render(prompt, vars) + Validate() + MultiError surface

Render walks {{name}} placeholders via regex (Q3 — not text/template;
the schema is a doc the user reads, not a Go-template-coupled
artefact); unknown placeholders are left intact for forward-compat
and emit one WARN per process. Validate enforces required prompts
(six: Ingest, UpdateExisting, Ask, Contradiction, PromoteRewrite,
LintContradictions), required placeholders per prompt (Q4 — errors
on missing required, allows extras), and required ontology fields
(title, body, evidence — the canonical struct field carrying
evidence pins the *check* to position 3 regardless of what the
user renamed it to, Q8).

MultiError surfaces all problems at once so `schema validate` can
render every file:line column in one shot rather than the user
fixing one error, re-running, fixing the next. cmd/root.go's
loadConfig bubbles the first error up via cliutil.Wrap.

TRUST PROPERTY UNCHANGED. Validate is purely structural. It does
NOT verify the prompt is *good* — a user can write a schema that
validates but produces awful pages. The validator (bundled,
unreachable from this package) is the gate that protects the
trust property. spec risk #8: validate's output explicitly says
"structural validation only — quality is on you."

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase B — Bundled-default extraction

### Task 4: Carve the six prompt sites; expose `*PromptForTests` mirrors

**Files:**
- Modify: `internal/wiki/ops.go`
- Modify: `internal/wiki/contradict.go`
- Modify: `internal/wiki/update_existing.go`
- Modify: `internal/wiki/promote.go`

- [ ] **Step 1: Write failing tests (a single byte-equality file in `internal/schema/byte_equality_test.go`)**

Create `internal/schema/byte_equality_test.go`. Imports `internal/wiki` (this *does* couple the test package back to wiki, but the production `internal/schema` package keeps its zero-import-of-wiki property — the test file is a deliberate test-only coupling, gated by `_test.go`).

```go
package schema_test

import (
    "testing"
    "github.com/mritunjaysharma394/llmwiki/internal/schema"
    "github.com/mritunjaysharma394/llmwiki/internal/wiki"
)

func TestBundledPrompts_ByteEqualV06_Ingest(t *testing.T) {
    got := schema.Bundled().Render(schema.Bundled().Prompts.Ingest, map[string]string{
        "domain":          schema.Bundled().Domain,
        "existing_titles": "(none yet)",
    })
    want := wiki.IngestSystemPromptForTests() + "\n\nExisting wiki pages (titles only):\n(none yet)\n..."
    if got != want { t.Fatalf("ingest prompt drifted: diff=%q", diff(got, want)) }
}
// ... same shape for UpdateExisting, Ask, Contradiction, PromoteRewrite, LintContradictions.
```

The test-only `*PromptForTests` exports below are what makes this work — the test reads the bundled v0.6 prompt string AND the schema-rendered v0.7 prompt string, and asserts byte-equality.

- [ ] **Step 2: Run tests and confirm failure**

Run: `go test ./internal/schema/ -run TestBundledPrompts -v`
Expected: FAIL — exports do not exist.

- [ ] **Step 3: Add the test-only exports**

In `internal/wiki/ops.go`:

```go
// IngestSystemPromptForTests exposes the v0.6 hard-coded prompt for
// internal/schema's byte-equality test. Removed in v0.8 once the
// schema-driven path is the only path.
func IngestSystemPromptForTests() string { return ingestSystemPrompt }

// AnswerSystemPromptForTests likewise.
func AnswerSystemPromptForTests() string { return answerSystemPrompt }

// LintContradictionsSystemPromptForTests exposes the inline lint
// prompt at line 344 (no const today; the function returns the
// hard-coded string verbatim until Task 5 carves it out).
func LintContradictionsSystemPromptForTests() string {
    return `You are a wiki consistency checker. Identify factual contradictions between wiki pages.
List each contradiction as: "Page A vs Page B: <description>". If no contradictions, say "No contradictions found."`
}
```

Same shape in `contradict.go`, `update_existing.go`, `promote.go` (the latter exposes the inline rewrite prompt).

- [ ] **Step 4: Update `default.md` to make the byte-equality tests pass**

Iterate: run the byte-equality tests, see the diff, adjust `default.md` until each prompt's `Render(...)` output equals the v0.6 string byte-for-byte. The placeholders introduced (`{{domain}}`, `{{existing_titles}}`, etc.) must produce zero bytes when rendered with the bundled-default `Domain` field's value AND the bundled "(none yet)" sentinel for `existing_titles` AND the empty `glossary`. This is the load-bearing property of "byte-identical to v0.6."

For prompts that the v0.6 code currently builds inline by string-concatenation (e.g. `IngestSourceFilesToPages` writes "Existing wiki pages (titles only):\n" then loops the existing titles), the schema-rendered prompt must reproduce that surrounding structure. Either (a) include the surrounding lines in `default.md` as part of the prompt template, with `{{existing_titles}}` interpolating just the bullet list, OR (b) keep the surrounding lines in Go and pass only the prompt body through `Render`. **Adopt (a)** — the prompt-as-doc-the-user-edits framing demands the user can rewrite the surrounding "Existing wiki pages (titles only):" line if they want.

- [ ] **Step 5: Run tests and confirm pass**

Run: `go test ./internal/schema/ -v`
Expected: PASS — all six byte-equality tests green plus all Phase A tests.

Run: `go test ./...`
Expected: green (no callers wired through schema yet — the prompt sites still use the hard-coded constants; Task 5 carves over).

- [ ] **Step 6: Commit**

```bash
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
feat(schema): byte-equality tests pin v0.7 bundled defaults to v0.6 prompts

The load-bearing backwards-compat contract: a v0.6 wiki opening
under v0.7 with no AGENTS.md sees zero behaviour change because
schema.Bundled().Render(Prompts.X, vars) produces the v0.6
hard-coded prompt string byte-for-byte for all six prompt sites
(Ingest, Ask, Contradiction, UpdateExisting, PromoteRewrite,
LintContradictions). internal/schema/byte_equality_test.go pins
the contract.

internal/wiki gains six test-only exports (*PromptForTests) that
expose the hard-coded constants verbatim — coupling the test back
to wiki without coupling production internal/schema to wiki. The
exports come out in v0.8 once the schema-driven path is the only
path.

internal/schema/default.md is now byte-aligned: surrounding
structure (e.g. "Existing wiki pages (titles only):\n" before the
ingest prompt's existing-titles bullet list) lives in default.md
so the user can rewrite that surrounding line if they want — the
prompt-as-doc-the-user-edits framing demands it.

Cassettes recorded under v0.6 will continue to replay because the
prompt strings reaching the LLM are byte-identical (spec risk
#12).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 5: Replace each prompt site with `sch.Render(...)`

**Files:**
- Modify: `internal/wiki/ops.go`
- Modify: `internal/wiki/contradict.go`
- Modify: `internal/wiki/update_existing.go`
- Modify: `internal/wiki/promote.go`
- Modify: corresponding `_test.go` files

- [ ] **Step 1: Write failing tests**

For each of the four files, append a unit test that constructs the function's input fixtures and a `schema.Bundled()` and asserts the function still returns the same result it did pre-refactor (no LLM call — these are call-site refactor tests, the LLM client is a stub recorder).

1. `TestIngestSourceFilesToPages_AcceptsSchemaParam_StubLLM` — fixtures + stub client; assert pages parse out, no error. (Pure compile-time + call-site shape test.)
2. `TestUpdateExistingPagesFromSource_AcceptsSchemaParam_StubLLM` — same shape.
3. `TestAnswerQuestion_AcceptsSchemaParam_StubLLM` — same.
4. `TestStreamAnswer_AcceptsSchemaParam_StubLLM` — same.
5. `TestDetectIngestContradictions_AcceptsSchemaParam_StubLLM` — same.
6. `TestRewritePromoteBody_AcceptsSchemaParam_StubLLM` — same.
7. `TestDetectContradictions_AcceptsSchemaParam_StubLLM` — same (the lint-batcher prompt at ops.go:344).

The byte-equality tests from Task 4 also continue to pass — they're the regression guard.

- [ ] **Step 2: Run tests and confirm failure**

Run: `go test ./internal/wiki/ -v`
Expected: FAIL — function signatures changed.

- [ ] **Step 3: Refactor each call site**

`internal/wiki/ops.go`'s `IngestSourceFilesToPages`:

```go
func IngestSourceFilesToPages(ctx context.Context, client llm.Client, files []ingest.SourceFile, existingTitles []string, sch schema.Schema) ([]Page, error) {
    sysPrompt := sch.Render(sch.Prompts.Ingest, map[string]string{
        "domain":          sch.Domain,
        "existing_titles": formatExistingTitles(existingTitles),
        "glossary":        formatGlossary(sch.Glossary),
    })
    // build the user prompt as before — the SOURCE concatenation
    var sb strings.Builder
    // ... (existing user-prompt construction, unchanged)
    result, err := client.CompleteStructured(ctx, sysPrompt, sb.String(), writePagesTool)
    // ... rest unchanged: ExtractPagesFromToolResult, ValidateAndAttachEvidence, ContentHash + UpdatedAt stamping.
}
```

Same shape for the other six entrypoints. The validator (`ValidateAndAttachEvidence`) takes no schema input; the `ContentHash + UpdatedAt` stamping is identical; the `writePagesTool` schema is identical.

The hard-coded `const ingestSystemPrompt = "..."` line stays in the file (consumed by `IngestSystemPromptForTests`); it is no longer referenced from production code paths.

- [ ] **Step 4: Update callers transitively**

Every caller of these seven functions in `internal/wiki/` (notably `ingest_runner.go`'s `IngestSource`, `update_existing.go`'s `UpdateExistingPagesFromSource`, `promote.go`'s `PromoteAnswer`) gains a `sch schema.Schema` parameter or an `IngestOptions.Schema schema.Schema` field. The `cmd/` callers in Phase C (Task 6) will pass `activeSchema` through. For Task 5, callers within `internal/wiki/` get the schema from a new `IngestOptions.Schema` field (they already take an `IngestOptions`); other in-package callers thread it as a direct parameter.

- [ ] **Step 5: Run tests and confirm pass**

Run: `go test ./internal/wiki/ -v`
Expected: PASS.

Run: `go test ./...`
Expected: cmd/ tests fail (Task 6 fixes them); internal/schema, internal/wiki, internal/db, internal/mcp green.

For Task 5's commit boundary, **temporarily set `cmd/` calls to pass `schema.Bundled()`** so `go test ./cmd/...` is green. Phase C (Task 6) replaces the `Bundled()` placeholder with a real `activeSchema` global.

- [ ] **Step 6: Commit**

```bash
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
refactor(wiki): six prompt sites take sch schema.Schema; render via sch.Render

Mechanical refactor of the seven prompt-using sites
(IngestSourceFilesToPages, AnswerQuestion, StreamAnswer,
DetectContradictions, DetectIngestContradictions,
UpdateExistingPagesFromSource, rewritePromoteBody) to take a
schema.Schema parameter and render their system prompts via
sch.Render(sch.Prompts.X, vars).

The hard-coded const strings stay in place — consumed only by the
*PromptForTests test-only exports from Phase B Task 4 — and come
out in v0.8 once the schema-driven path is the only path. The
production code paths now read prompts from the schema layer.

cmd/ callers temporarily pass schema.Bundled() until Phase C Task
6 wires the activeSchema global through cmd/root.go's loadConfig.
The temporary Bundled() pass-through is byte-identical to v0.6
behaviour (Phase B Task 4's byte-equality test pins this).

TRUST PROPERTY REAFFIRMED. Each refactored entrypoint still calls
ValidateAndAttachEvidence (or its caller does) with zero schema
input. The validator is bundled and unreachable from the schema —
that is the hard line, and these refactors keep it intact.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase C — Wire `Schema` through `cmd/`

### Task 6: `activeSchema` global; thread through every command

**Files:**
- Modify: `cmd/root.go`
- Modify: `cmd/root_test.go`
- Modify: `cmd/ingest.go`
- Modify: `cmd/ask.go`
- Modify: `cmd/promote.go`
- Modify: `cmd/lint.go`

- [ ] **Step 1: Write failing tests**

Append to `cmd/root_test.go`:

1. `TestLoadConfig_LoadsAGENTSMdWhenPresent` — write a fixture `AGENTS.md` to a tmp wiki root; chdir; `loadConfig()`; assert `activeSchema.DocPath == "AGENTS.md"` and `activeSchema.Hash() == sha256(<file bytes>)`.
2. `TestLoadConfig_FallsBackToBundledWhenAGENTSMdAbsent` — fresh tmp wiki, no AGENTS.md; `loadConfig()`; assert `activeSchema.DocPath == ""` and `activeSchema.Hash() == schema.Bundled().Hash()`.
3. `TestLoadConfig_AGENTSMdValidationFails_ErrorsLoudly` — write a malformed `AGENTS.md` (missing `## Ingest prompt`); `loadConfig()` returns a `cliutil.Wrap`'d error mentioning the file:line of the missing section.
4. `TestLoadConfig_AGENTSMdHashStable_AcrossReads` — call `loadConfig()` twice in succession on the same fixture; assert `activeSchema.Hash()` is the same string (stability guard for `db.schema_hash` queries).

- [ ] **Step 2: Run tests and confirm failure**

Run: `go test ./cmd/ -run TestLoadConfig -v`
Expected: FAIL — `activeSchema` global does not exist.

- [ ] **Step 3: Wire `activeSchema` in `cmd/root.go`**

```go
import "github.com/mritunjaysharma394/llmwiki/internal/schema"

var (
    cfg              *Config
    llmClient        llm.Client
    database         *db.DB
    activeSchema     schema.Schema  // sub-project 7: loaded by loadConfig.
    overrideProvider string
    overrideModel    string
)

func loadConfig() error {
    // ... existing config + provider + db.Open code ...

    // Load the user-editable schema doc, falling back to the bundled
    // default when AGENTS.md is absent (a v0.6 wiki opening under
    // v0.7). Validate the user's AGENTS.md immediately — a malformed
    // schema fails at load-time with file:line, not at first ingest.
    sch, err := schema.Load(".") // wiki root is cwd
    if err != nil {
        return cliutil.Wrap(
            "loading AGENTS.md schema doc",
            err,
            "run `llmwiki schema validate` to see the structured error; or `llmwiki init --rewrite-schema` to overwrite with the bundled default")
    }
    if verr := sch.Validate(); verr != nil {
        return cliutil.Wrap(
            "validating AGENTS.md schema doc",
            verr,
            "run `llmwiki schema validate` for the structured file:line errors")
    }
    activeSchema = sch
    return nil
}
```

- [ ] **Step 4: Thread `activeSchema` into every command**

`cmd/ingest.go`'s `runIngest`: `opts.Schema = activeSchema`.
`cmd/ask.go`'s `runAsk`: pass `activeSchema` to `wiki.AnswerQuestion` / `wiki.StreamAnswer`.
`cmd/promote.go`'s `runPromote`: pass `activeSchema` to `wiki.PromoteAnswer`.
`cmd/lint.go`'s `runLint`: pass `activeSchema` to `wiki.DetectContradictions`.

For each, replace the temporary `schema.Bundled()` placeholder from Task 5 with `activeSchema`.

- [ ] **Step 5: Run tests and confirm pass**

Run: `go test ./cmd/ -v`
Expected: PASS.

Run: `go test ./...`
Expected: green.

- [ ] **Step 6: Commit**

```bash
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
feat(cmd): activeSchema global; loadConfig calls schema.Load(wikiRoot)

cmd/root.go gains an activeSchema package-level global, populated
by loadConfig() after applyIngestDefaults via schema.Load("."). When
AGENTS.md is absent at the wiki root, schema.Bundled() is the
fallback — pre-v0.7 wikis see zero behaviour change. When AGENTS.md
is present and well-formed, the user's prompts and ontology drive
every wiki entrypoint. When AGENTS.md is present and malformed, the
CLI exits with cliutil.Wrap'd file:line errors and a "run llmwiki
schema validate" suggestion — the schema is validated at load-time,
not at first ingest, so the user gets the error before the LLM call
happens.

cmd/ingest.go, cmd/ask.go, cmd/promote.go, cmd/lint.go thread
activeSchema into the wiki entrypoints (the schema.Bundled()
placeholders from Phase B Task 5 come out, replaced with the real
global). ~30 mechanical call-site edits.

TRUST PROPERTY UNCHANGED. activeSchema is a Go value that flows
into prompt strings via Render; it never reaches the validator.
ValidateAndAttachEvidence's signature does not include a schema
parameter and is not parameterised by the schema in any way.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase D — `db` v5 migration + `schema_hash`

### Task 7: v5 migration adds `schema_hash` column

**Files:**
- Modify: `internal/db/db.go`
- Modify: `internal/db/db_test.go`

- [ ] **Step 1: Write failing tests**

Append to `internal/db/db_test.go`:

1. `TestMigrate_FromFresh_LandsAtV5` — open a fresh DB; `PRAGMA user_version` returns 5; `pages` table has a `schema_hash` column (verify via `PRAGMA table_info(pages)`); column type is `TEXT`, NOT NULL, default `''`.
2. `TestMigrate_FromV4_AddsSchemaHash` — open a DB, force `user_version = 4` and create the v4 schema by hand (mirror the fixtures Phase A of sub-project 6b uses), insert one `pages` row with the v4 columns; reopen via `db.Open`; assert `user_version` is 5, the existing `pages` row's `schema_hash` is `''`, and a new insert can set `schema_hash` to a non-empty value.
3. `TestMigrate_Idempotent_RerunningOnV5_IsNoop` — open at v5 twice; assert no error, no duplicate column.
4. `TestMigrate_PreV5RowsHaveEmptySchemaHash` — pre-seed v4 with three pages; migrate; assert all three have `schema_hash = ''`.
5. `TestMigrate_DoesNotAlterEvidenceSourcesSourceFilesChunksPageUpdateLog` — capture `sqlite_master` schema rows for the four other tables on a v4 DB; reopen at v5; assert byte-identical (no surprise `ALTER TABLE`).

- [ ] **Step 2: Run tests and confirm failure**

Run: `go test ./internal/db/ -run TestMigrate -v`
Expected: FAIL — v5 block does not exist.

- [ ] **Step 3: Add the v5 migration block**

In `internal/db/db.go`, after the v4 block:

```go
if version < 5 {
    // Sub-project 7 (v0.7) — additive, schema_hash column on pages.
    //
    // Stamps the active schema's hash on every page-write so the lint
    // and status surfaces can surface schema_drift counters and
    // `llmwiki schema migrate` can resume by skipping pages already at
    // the active hash.
    //
    // PRAGMA table_info(pages) check makes the migration idempotent
    // without ALTER TABLE IF NOT EXISTS (which SQLite doesn't have).
    var hasCol bool
    rows, err := d.sql.Query(`PRAGMA table_info(pages)`)
    if err != nil { return err }
    for rows.Next() {
        var cid int; var name, ctype string; var notnull, pk int; var dflt sql.NullString
        if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
            rows.Close(); return err
        }
        if name == "schema_hash" { hasCol = true }
    }
    rows.Close()
    if !hasCol {
        if _, err := d.sql.Exec(`ALTER TABLE pages ADD COLUMN schema_hash TEXT NOT NULL DEFAULT ''`); err != nil {
            return fmt.Errorf("v5 migration: %w", err)
        }
    }
    if _, err := d.sql.Exec(`PRAGMA user_version = 5`); err != nil {
        return fmt.Errorf("v5 migration user_version bump: %w", err)
    }
}
```

- [ ] **Step 4: Run tests and confirm pass**

Run: `go test ./internal/db/ -v`
Expected: PASS.

Run: `go test ./...`
Expected: green (no callers of `schema_hash` yet — Task 8 wires them).

- [ ] **Step 5: Commit**

```bash
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
feat(db): v5 migration — additive schema_hash column on pages

ALTER TABLE pages ADD COLUMN schema_hash TEXT NOT NULL DEFAULT ''.
Wrapped in a PRAGMA table_info(pages) check so re-running on v5 is
a no-op (matches the v3 / v4 migrations' defensive shape — SQLite
has no ALTER TABLE IF NOT EXISTS).

Pre-v5 rows get schema_hash = ''. The lint surface (Phase H Task
13) treats '' as "prior schema" relative to the active hash, so a
v0.6 wiki opening under v0.7 sees `schema_drift: <n> pages on
prior schema` for its existing pages until those pages are
re-ingested or `schema migrate` is run.

Roll-forward only — no down-migration script. Matches every prior
migration. A user who downgrades from v0.7 to v0.6 sees the column
ignored (SQLite tolerates extra columns at read time).

PRAGMA user_version bumps from 4 to 5.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 8: `UpdateSchemaHash`, `CountPagesByHashState`, `ListPagesByHash`; wire into every WritePage site

**Files:**
- Modify: `internal/db/queries.go`
- Modify: `internal/db/queries_test.go`
- Modify: `internal/wiki/ingest_runner.go`
- Modify: `internal/wiki/promote.go`
- Modify: `internal/wiki/update_existing.go`
- Modify: `internal/mcp/handlers.go` (write_page handler)

- [ ] **Step 1: Write failing tests**

Append to `internal/db/queries_test.go`:

1. `TestUpdateSchemaHash_StampsRow` — seed a page with `schema_hash = ''`; call `UpdateSchemaHash(pageID, "abc123")`; assert the row's `schema_hash` is now `"abc123"`.
2. `TestUpdateSchemaHash_IdempotentOnSameHash` — call twice with the same hash; assert no error, value unchanged.
3. `TestUpdateSchemaHash_NonexistentPageID_ReturnsErr` — call with a page ID that doesn't exist; assert error wrapping a sentinel `ErrPageNotFound`.
4. `TestCountPagesByHashState_ReturnsCurrentAndPriorTuple` — seed 5 pages: 3 at hash `"X"`, 2 at hash `"Y"`. Call `CountPagesByHashState("X")`; assert `current=3, prior=2`.
5. `TestCountPagesByHashState_AllAtActive_PriorIsZero` — all 5 at `"X"`; `CountPagesByHashState("X")` returns `current=5, prior=0`.
6. `TestCountPagesByHashState_NoneAtActive_CurrentIsZero` — all 5 at `"X"`; `CountPagesByHashState("Z")` returns `current=0, prior=5`.
7. `TestListPagesByHash_ReturnsMatchingRows` — 3 pages at `"X"`, 2 at `"Y"`; `ListPagesByHash("X", 10)` returns 3 rows; `ListPagesByHash("Y", 10)` returns 2 rows.
8. `TestListPagesByHash_RespectsLimit` — 10 pages at `"X"`; `ListPagesByHash("X", 3)` returns 3 rows.

Append to `internal/wiki/ingest_runner_test.go`:

9. `TestIngestSource_StampsSchemaHashOnEveryWrittenPage` — synthetic ingest writes 3 pages under a fixture schema with hash `"ABC"`; assert all 3 rows have `schema_hash = "ABC"` post-ingest.
10. `TestIngestSource_ValidatorDropsPage_NoSchemaHashStamp` — synthetic ingest with one page that fails validation; assert no row exists for the dropped page (so trivially no `schema_hash` to check), the trust-property reaffirmation that bad pages don't reach disk.

Append to `internal/wiki/promote_test.go`:

11. `TestPromoteAnswer_StampsSchemaHash` — synthetic promote writes one page; assert `schema_hash` matches the active schema.

Append to `internal/wiki/update_existing_test.go`:

12. `TestUpdateExistingPagesFromSource_StampsSchemaHashOnUpdated` — happy-path update; assert `schema_hash` post-update equals the active schema's hash.
13. `TestUpdateExistingPagesFromSource_FailedUpdate_LeavesPriorSchemaHashUntouched` — validator-drop case; assert the page's `schema_hash` is whatever it was before the update (not stamped to the active hash, because the body wasn't updated).

- [ ] **Step 2: Run tests and confirm failure**

Run: `go test ./internal/db/ ./internal/wiki/ -v`
Expected: FAIL.

- [ ] **Step 3: Add queries and wire write sites**

`internal/db/queries.go`:

```go
var ErrPageNotFound = errors.New("page not found")

func (d *DB) UpdateSchemaHash(pageID int64, hash string) error {
    res, err := d.sql.Exec(`UPDATE pages SET schema_hash = ? WHERE id = ?`, hash, pageID)
    if err != nil { return err }
    n, err := res.RowsAffected()
    if err != nil { return err }
    if n == 0 { return fmt.Errorf("%w: id=%d", ErrPageNotFound, pageID) }
    return nil
}

// CountPagesByHashState returns (current, prior) where current is
// the count of pages whose schema_hash equals activeHash and prior
// is everything else (including the empty-string default for pre-v5
// rows). Used by cmd/lint.go and cmd/status.go.
func (d *DB) CountPagesByHashState(activeHash string) (current, prior int, err error) {
    err = d.sql.QueryRow(`
        SELECT
          SUM(CASE WHEN schema_hash = ? THEN 1 ELSE 0 END),
          SUM(CASE WHEN schema_hash = ? THEN 0 ELSE 1 END)
        FROM pages`, activeHash, activeHash).Scan(&current, &prior)
    return
}

// ListPagesByHash returns up to `limit` PageRecords whose schema_hash
// equals the given hash. Used by `schema migrate` to skip
// already-migrated pages (resumability via per-page hash check, Q14).
func (d *DB) ListPagesByHash(hash string, limit int) ([]PageRecord, error) { /* ... */ }
```

`internal/wiki/ingest_runner.go`'s persist loop, after a successful `WritePage` + `UpsertPage`:

```go
if err := database.UpdateSchemaHash(rec.ID, opts.Schema.Hash()); err != nil {
    fmt.Fprintf(os.Stderr, "  WARN stamping schema_hash for %q: %v\n", p.Title, err)
    // non-fatal: the page reached disk; the hash stamp is metadata.
}
```

Same pattern in `update_existing.go` (step 8 disk + DB write block) and `promote.go` (`PromoteAnswer`'s final `WritePage`). The `mcp.write_page` handler in `internal/mcp/handlers.go` does the same.

- [ ] **Step 4: Run tests and confirm pass**

Run: `go test ./...`
Expected: green.

- [ ] **Step 5: Commit**

```bash
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
feat(db,wiki,mcp): UpdateSchemaHash + CountPagesByHashState + ListPagesByHash; stamp at every write site

Three new queries on db.DB:
  - UpdateSchemaHash(pageID, hash): stamps the row post-write,
    surfaces ErrPageNotFound on a missing ID.
  - CountPagesByHashState(activeHash): returns (current, prior)
    tuple for cmd/lint and cmd/status's drift surface.
  - ListPagesByHash(hash, limit): cmd/schema migrate's resumability
    seam (Q14 — succeeded pages get the new hash, a Ctrl-C mid-run
    leaves a sound state, resuming is just re-running migrate).

Every WritePage write site stamps schema_hash post-write:
  - internal/wiki/ingest_runner.go's persist loop
  - internal/wiki/update_existing.go's step 8 disk+DB block
  - internal/wiki/promote.go's PromoteAnswer
  - internal/mcp/handlers.go's writePageHandler

Stamp failures are non-fatal — the page already reached disk and
the validator already gated it; the schema_hash is metadata. The
warning surfaces on stderr.

TRUST PROPERTY REAFFIRMED. The schema_hash stamp happens AFTER
ValidateAndAttachEvidence has gated the write. A page that fails
validation never reaches WritePage and never gets a schema_hash —
so a v0.7 wiki's pages with schema_hash != '' all carry validated
evidence, and pages with schema_hash == '' are either pre-v0.7 or
were stamped before the column existed. The drift surface treats
both classes as "prior schema."

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase E — `cmd/schema.go`

### Task 9: `schema show` + `schema validate`

**Files:**
- Create: `cmd/schema.go`
- Create: `cmd/schema_test.go`

- [ ] **Step 1: Write failing tests**

Create `cmd/schema_test.go`:

1. `TestSchemaShow_PrintsMergedEffective_ByDefault` — fresh wiki, no AGENTS.md; `schema show` (captured stdout); assert output contains the bundled `## Domain`, `## Ingest prompt`, etc., AND a leading "schema: bundled (no AGENTS.md)" line.
2. `TestSchemaShow_DocFlag_PrintsAGENTSMdVerbatim` — write fixture AGENTS.md; `schema show --doc`; assert stdout is byte-identical to the file.
3. `TestSchemaShow_DocFlag_NoAGENTSMd_PrintsBundledNotice` — no AGENTS.md; `schema show --doc`; assert stdout is "no AGENTS.md present; bundled defaults are in effect (run `llmwiki init --rewrite-schema` to write one)".
4. `TestSchemaShow_BundledFlag_IgnoresAGENTSMd` — write a custom AGENTS.md with a non-default `## Domain`; `schema show --bundled`; assert the printed `## Domain` is the bundled-default text, not the custom one.
5. `TestSchemaValidate_OK_ExitZero` — fresh wiki (bundled defaults); `schema validate`; assert exit 0 and stdout has the spec's success block ("✓ all 6 required prompts present", "✓ all required placeholders present in each prompt", "✓ page ontology has required fields: title, body, evidence", "✓ glossary has 0 terms (optional)", "trust property: enforced by bundled validator", "OK").
6. `TestSchemaValidate_MissingRequiredSection_ExitOne_FileLineError` — fixture AGENTS.md missing `## Ingest prompt`; `schema validate`; assert exit 1 and stderr contains "AGENTS.md: section 'Ingest prompt': required section missing".
7. `TestSchemaValidate_MissingRequiredPlaceholder_ExitOne_FileLineError` — fixture with `## Ingest prompt` text but missing `{{domain}}`; assert exit 1 with structured error pointing at the section.
8. `TestSchemaValidate_AllErrorsAtOnce` — fixture missing two required sections + missing one placeholder; assert all three errors surface in one run (the MultiError from Phase A Task 3).

- [ ] **Step 2: Run tests and confirm failure**

Run: `go test ./cmd/ -run TestSchema -v`
Expected: FAIL.

- [ ] **Step 3: Implement `cmd/schema.go`**

```go
package cmd

import (
    "fmt"
    "os"
    "github.com/spf13/cobra"
    "github.com/mritunjaysharma394/llmwiki/internal/cliutil"
    "github.com/mritunjaysharma394/llmwiki/internal/schema"
)

var schemaCmd = &cobra.Command{
    Use:   "schema",
    Short: "Inspect, validate, or migrate the wiki's schema",
    Long: `The wiki's schema lives at AGENTS.md in the wiki root. It defines
the page ontology, the prompts that drive ingest / ask / contradiction
detection / cross-page updates / promote rewrite / lint, and an
optional glossary. The bundled default is byte-identical to v0.6
behaviour; user-edited AGENTS.md overrides it.

Trust property: the schema controls what the LLM is *asked*, not
what counts as valid evidence. The bundled substring-match validator
runs after every LLM call regardless of what the schema-rendered
prompt told the LLM.`,
}

var schemaShowCmd = &cobra.Command{
    Use:   "show",
    Short: "Print the effective schema (merged: bundled + user)",
    RunE:  runSchemaShow,
}
var schemaValidateCmd = &cobra.Command{
    Use:   "validate",
    Short: "Validate AGENTS.md against the bundled schema-format contract",
    RunE:  runSchemaValidate,
}

func init() {
    schemaShowCmd.Flags().Bool("bundled", false, "ignore AGENTS.md and print the bundled default")
    schemaShowCmd.Flags().Bool("doc", false, "print AGENTS.md verbatim (or notice if absent)")
    schemaCmd.AddCommand(schemaShowCmd)
    schemaCmd.AddCommand(schemaValidateCmd)
    rootCmd.AddCommand(schemaCmd)
}

func runSchemaShow(cmd *cobra.Command, args []string) error {
    bundled, _ := cmd.Flags().GetBool("bundled")
    doc, _ := cmd.Flags().GetBool("doc")
    switch {
    case bundled:
        os.Stdout.Write(schema.DefaultDoc)
    case doc:
        if activeSchema.DocPath == "" {
            fmt.Println("no AGENTS.md present; bundled defaults are in effect (run `llmwiki init --rewrite-schema` to write one)")
            return nil
        }
        os.Stdout.Write(activeSchema.Raw())
    default:
        // merged-effective: print activeSchema's content, with a leading
        // header naming hash + doc path or "bundled".
        if activeSchema.DocPath == "" {
            fmt.Println("schema: bundled (no AGENTS.md)")
        } else {
            fmt.Printf("schema: %s (hash %s)\n", activeSchema.DocPath, activeSchema.Hash())
        }
        fmt.Println()
        os.Stdout.Write(activeSchema.Raw())
    }
    return nil
}

func runSchemaValidate(cmd *cobra.Command, args []string) error {
    if err := activeSchema.Validate(); err != nil {
        // surface every ValidationError on its own line; exit 1 via cliutil.
        return cliutil.Wrap("validating schema", err, "edit AGENTS.md to fix the listed problems, then re-run")
    }
    fmt.Printf("%s (schema_version %d)\n", schemaPathLabel(activeSchema), activeSchema.Version)
    fmt.Println("  ✓ all 6 required prompts present")
    fmt.Println("  ✓ all required placeholders present in each prompt")
    fmt.Println("  ✓ page ontology has required fields: title, body, evidence")
    fmt.Printf("  ✓ glossary has %d terms (optional)\n", len(activeSchema.Glossary))
    fmt.Println()
    fmt.Println("  trust property: enforced by bundled validator")
    fmt.Println("  (substring-match against source files; not configurable from this doc)")
    fmt.Println("OK")
    return nil
}
```

- [ ] **Step 4: Run tests and confirm pass**

Run: `go test ./cmd/ -run TestSchema -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
feat(cmd): llmwiki schema show + schema validate

Two subcommands under llmwiki schema:

  - schema show: prints the effective schema (the merged
    bundled+user content). --bundled ignores AGENTS.md and prints
    the embedded default; --doc prints AGENTS.md verbatim (or a
    notice if absent).

  - schema validate: runs activeSchema.Validate() and prints the
    structured success block (six required prompts, required
    placeholders, required ontology fields, glossary count) plus
    the trust-property reaffirmation. Exits 1 with file:line
    errors on failure (the MultiError from internal/schema
    surfaces every problem at once).

Both commands read activeSchema, populated by cmd/root.go's
loadConfig — same path the implicit ingest/ask/promote/lint use,
so `schema validate` is the same check that runs at the start of
every other command. The dedicated subcommand exists so users can
iterate on AGENTS.md quickly without doing a real ingest.

The success block is verbatim from the spec's §Trust-property
reaffirmation. The "trust property: enforced by bundled validator"
line is load-bearing — the user's schema cannot reach the
validator, and the success output says so on every run.

schema migrate lands in Phase F Task 11.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 10: `schema show --hash` (small extension; Risk #3 mitigation)

**Files:**
- Modify: `cmd/schema.go`
- Modify: `cmd/schema_test.go`

- [ ] **Step 1: Write failing tests**

Append to `cmd/schema_test.go`:

1. `TestSchemaShow_HashFlag_PrintsActiveHashOnly` — write fixture AGENTS.md; `schema show --hash`; assert stdout is exactly the active hex hash + newline (so users can scriptably compare across wikis sharing a schema, per spec Risk #3).
2. `TestSchemaShow_HashFlag_NoAGENTSMd_PrintsBundledHash` — no AGENTS.md; `schema show --hash`; assert stdout is `Bundled().Hash()` + newline.

- [ ] **Step 2: Run tests and confirm failure**

Run: `go test ./cmd/ -run TestSchemaShow_HashFlag -v`
Expected: FAIL.

- [ ] **Step 3: Add the flag**

```go
schemaShowCmd.Flags().Bool("hash", false, "print only the active schema's sha256 hex hash (scriptable; useful for comparing across wikis sharing a schema)")
```

In `runSchemaShow`:

```go
hash, _ := cmd.Flags().GetBool("hash")
if hash {
    fmt.Println(activeSchema.Hash())
    return nil
}
```

- [ ] **Step 4: Run tests and confirm pass**

Run: `go test ./cmd/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
feat(cmd): schema show --hash — scriptable hash extraction

One-liner that prints the active schema's sha256 hex hash. Mitigates
spec Risk #3 (a team co-edits a single schema doc and copies it
across N wikis; divergence is the user's problem, but at least we
let them script the comparison: `for d in wiki1 wiki2; do (cd $d &&
llmwiki schema show --hash); done`).

schema diff stays deferred to v0.8 (Q12 — git diff over AGENTS.md
does the same job for any user with .llmwiki + AGENTS.md under
source control, which we recommend in the README).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase F — `schema migrate`

### Task 11: `schema migrate` + `--yes` + `--dry-run`

**Files:**
- Modify: `cmd/schema.go`
- Modify: `cmd/schema_test.go`

- [ ] **Step 1: Write failing tests**

Append to `cmd/schema_test.go`:

1. `TestSchemaMigrate_NoDriftedPages_NoOp` — pre-seed a wiki with all pages at the active hash; `schema migrate --yes`; assert "no pages on prior schema; nothing to do" on stdout, no LLM calls, no disk writes.
2. `TestSchemaMigrate_DryRunDoesNotWriteDisk` — pre-seed a wiki with 5 pages at a prior hash; capture each page's body bytes; run `schema migrate --dry-run --yes`; assert LLM was called 5 times (the cassette/stub records this), but every page's body bytes on disk are byte-identical to the snapshot. No `schema_hash` updates either.
3. `TestSchemaMigrate_HappyPath_RemapsAllPagesToActiveHash` — synthetic 3-page wiki at prior hash; stub LLM to return valid updated bodies for each; `schema migrate --yes`; assert all 3 pages' `schema_hash` is now the active hash and bodies were rewritten.
4. `TestSchemaMigrate_ResumabilityViaPerPageHashCheck` — synthetic 5-page wiki; pre-stamp 2 pages at the active hash already, 3 at a prior hash; `schema migrate --yes`; assert only 3 LLM calls (the 2 already-migrated pages are skipped via the hash check).
5. `TestSchemaMigrate_ValidatorDropsPage_KeepsPriorVersion` — synthetic 1-page wiki at prior hash; stub LLM returns a body whose evidence quotes don't substring-match the source files; assert the page on disk is byte-identical to its prior version, the page's `schema_hash` is unchanged (still the prior hash), and the summary line says "1 page(s) update FAILED — kept at prior version" (matching sub-project 6b's `update_failed` shape).
6. `TestSchemaMigrate_PromptsForConfirmation_Without_Yes` — interactive (set up a fake stdin); without `--yes`, the command prompts "Continue? [y/N]"; on `n`, exits with "aborted" message and no LLM calls.
7. `TestSchemaMigrate_AppendsLogEntry` — happy-path; assert `.llmwiki/log.md` gains a `**schema_migrate**` entry naming the page count and the hash transition.
8. `TestSchemaMigrate_TrustPropertyReaffirmed` — happy-path; for each migrated page, re-read it from disk, walk its evidence, assert every quote substring-matches some file in the union of (page's source_files). The validator is the gatekeeper over the migrate path too.

- [ ] **Step 2: Run tests and confirm failure**

Run: `go test ./cmd/ -run TestSchemaMigrate -v`
Expected: FAIL.

- [ ] **Step 3: Implement `schema migrate`**

```go
var schemaMigrateCmd = &cobra.Command{
    Use:   "migrate",
    Short: "Re-ingest pages on a prior schema hash under the active schema",
    Long: `Walks every page whose schema_hash differs from the active schema's
hash, re-reads its source files, re-runs IngestSourceFilesToPages
under the active schema, and runs ValidateAndAttachEvidence as
usual. Pages whose proposed body fails validation STAY AT THEIR
PRIOR VERSION — the trust property holds.

Resumable: succeeded pages get stamped with the active hash, so
re-running after a Ctrl-C skips the already-migrated pages
naturally (Q14).

Cost: roughly one LLM call per migrated page. On Gemini Flash
(free tier), comfortable for any wiki size; on Anthropic Haiku
~$0.005/page; on Ollama, expect more update_failed because small
models often miss the structured-output schema. --dry-run runs
the LLM calls but skips disk + DB writes so users can preview
the cost picture before committing.`,
    RunE: runSchemaMigrate,
}

func init() {
    schemaMigrateCmd.Flags().Bool("yes", false, "skip the confirmation prompt")
    schemaMigrateCmd.Flags().Bool("dry-run", false, "run LLM calls but do not write to disk or DB")
    schemaCmd.AddCommand(schemaMigrateCmd)
}

func runSchemaMigrate(cmd *cobra.Command, args []string) error {
    yes, _ := cmd.Flags().GetBool("yes")
    dryRun, _ := cmd.Flags().GetBool("dry-run")

    activeHash := activeSchema.Hash()
    drifted, err := database.ListPagesByHash("", 1<<31-1) // all rows where schema_hash != activeHash
    // (revise: ListPagesByHash takes the *active* hash and the query
    //  is "WHERE schema_hash != ?". Adjust the query name if needed.)
    if err != nil { return err }
    if len(drifted) == 0 {
        fmt.Println("no pages on prior schema; nothing to do")
        return nil
    }

    fmt.Printf("Re-ingesting %d page(s) under active schema (hash %s).\n", len(drifted), activeHash[:8]+"...")
    fmt.Println("This walks every page's source_files and re-runs IngestSourceFilesToPages")
    fmt.Println("under the active schema, then runs ValidateAndAttachEvidence as usual.")
    fmt.Println("Pages whose proposed body fails validation STAY AT THEIR PRIOR VERSION.")
    if dryRun {
        fmt.Println("DRY RUN: LLM calls will fire, but no disk or DB writes will happen.")
    }
    if !yes {
        if !confirm("Continue? [y/N] ") {
            fmt.Println("aborted")
            return nil
        }
    }

    var migrated, failed, unchanged int
    for i, p := range drifted {
        fmt.Printf("[%d/%d] %s\n", i+1, len(drifted), p.Title)
        // 1. Read p.SourceIDs -> source files via wiki.readSourceFileContent.
        // 2. Run wiki.IngestSourceFilesToPages with sch=activeSchema, expecting one updated page.
        // 3. wiki.ValidateAndAttachEvidence over the read source files.
        // 4. If valid >= 1 quote: WritePage + database.UpsertPage + database.UpdateSchemaHash(p.ID, activeHash).
        //    Else: keep prior version, increment failed.
        // 5. content_hash unchanged: increment unchanged.
        // 6. dry-run: skip step 4's disk + DB writes, only count.
    }

    fmt.Println()
    fmt.Printf("%d page(s) brought to active schema.\n", migrated)
    fmt.Printf("%d page(s) unchanged (proposed body identical to prior body).\n", unchanged)
    if failed > 0 {
        fmt.Printf("%d page(s) update FAILED — kept at prior version:\n", failed)
        // ... per-page failure list
    }

    if !dryRun {
        // Append a log entry to .llmwiki/log.md naming the page count + hash transition.
        wiki.AppendLog(cfg.Wiki.WikiDir, wiki.LogEntry{
            Kind: "schema_migrate",
            // ...
        })
    }
    return nil
}
```

- [ ] **Step 4: Run tests and confirm pass**

Run: `go test ./cmd/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
feat(cmd): llmwiki schema migrate — eager re-ingest under active schema

Walks every page whose schema_hash differs from the active schema
and re-runs IngestSourceFilesToPages under the active schema. Per
page, the validator runs as usual; pages whose proposed body fails
validation STAY AT THEIR PRIOR VERSION (the same update_failed
shape sub-project 6b uses). Resumable for free via the per-page
hash check (Q14): succeeded pages get stamped, re-running after a
Ctrl-C skips them.

--yes skips the confirmation prompt. --dry-run fires the LLM calls
but skips disk + DB writes so users can preview the cost picture
before committing — important for Anthropic Haiku users where
~$0.005/page becomes ~$2.50 on a 500-page wiki (spec Risk #9).

A log entry (.llmwiki/log.md `**schema_migrate**`) records the
page count and the hash transition so users can audit the
migration after the fact.

TRUST PROPERTY REAFFIRMED. schema migrate runs through the same
ValidateAndAttachEvidence gate as every other write path. Pages
that the validator drops keep their prior version — never silently
downgraded. The schema is the user's; the validator is the
binary's; the trust property holds across the boundary.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase G — `cmd/init.go` extension

### Task 12: `llmwiki init` writes `AGENTS.md`; `--rewrite-schema` overwrites

**Files:**
- Modify: `cmd/init.go`
- Modify: `cmd/init_test.go`

- [ ] **Step 1: Write failing tests**

Append to `cmd/init_test.go`:

1. `TestInit_WritesAGENTSMdAtWikiRoot` — fresh tmp dir; run `init`; assert `<tmpDir>/AGENTS.md` exists, byte-equal to `schema.DefaultDoc`.
2. `TestInit_LeavesExistingAGENTSMdAlone` — tmp dir with custom AGENTS.md; run `init`; assert AGENTS.md byte-identical to the pre-init custom version (idempotency).
3. `TestInit_RewriteSchemaFlag_Overwrites` — tmp dir with custom AGENTS.md; run `init --rewrite-schema`; assert AGENTS.md is now byte-equal to `schema.DefaultDoc`.
4. `TestInit_OutputMentionsAGENTSMd_OnFirstWrite` — fresh init; capture stdout; assert it contains "Wrote default schema at AGENTS.md (defines page shape and prompts; edit to fit your domain)".
5. `TestInit_OutputDoesNotMentionAGENTSMd_OnIdempotentRun` — second init in the same dir; capture stdout; assert it does NOT mention "Wrote default schema" (the file already existed; we say nothing new).

- [ ] **Step 2: Run tests and confirm failure**

Run: `go test ./cmd/ -run TestInit -v`
Expected: FAIL.

- [ ] **Step 3: Extend `runInit`**

In `cmd/init.go`:

```go
func init() {
    initCmd.Flags().String("provider", "", "Provider preset: gemini | anthropic | ollama | openai-compatible.")
    initCmd.Flags().Bool("rewrite-schema", false, "Overwrite an existing AGENTS.md with the bundled default. By default `init` leaves an existing schema doc alone.")
    rootCmd.AddCommand(initCmd)
}

func runInit(cmd *cobra.Command, args []string) error {
    // ... existing provider banner + .llmwiki dir creation ...

    // Write AGENTS.md at the wiki root (NOT inside .llmwiki/).
    rewriteSchema, _ := cmd.Flags().GetBool("rewrite-schema")
    schemaPath := "AGENTS.md"
    _, schemaErr := os.Stat(schemaPath)
    schemaExists := schemaErr == nil
    if !schemaExists || rewriteSchema {
        if err := os.WriteFile(schemaPath, schema.DefaultDoc, 0644); err != nil {
            return fmt.Errorf("writing AGENTS.md: %w", err)
        }
        fmt.Println("Wrote default schema at AGENTS.md")
        fmt.Println("  (defines page shape and prompts; edit to fit your domain)")
    }

    // ... existing config.toml write + provider key check ...
}
```

- [ ] **Step 4: Run tests and confirm pass**

Run: `go test ./cmd/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
feat(init): write AGENTS.md alongside .llmwiki/config.toml; --rewrite-schema flag

llmwiki init now writes AGENTS.md at the wiki root (NOT inside
.llmwiki/, per Q1 — AGENTS.md is the multi-vendor convention,
discoverable on `ls`). The output gains a "Wrote default schema at
AGENTS.md" line on first write, mirroring the existing "Initialized
wiki at .llmwiki" line.

Idempotency: an existing AGENTS.md is left alone (so users who
already wrote one don't lose their edits when they re-run init for
provider-key fixes). --rewrite-schema overwrites — the explicit
opt-in for "I want the bundled v0.7 default back."

The bundled default is byte-identical to the schema doc the user
would write themselves to reproduce v0.6 behaviour, so a fresh
v0.7 init produces a wiki whose first ingest behaves identically
to a v0.6 ingest. Backwards compat is the easy case and it is
also the loud case.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase H — Lint + status surfaces

### Task 13: `cmd/lint.go` schema_drift warning + `cmd/status.go` schema line

**Files:**
- Modify: `cmd/lint.go`
- Modify: `cmd/lint_test.go` (or `_integration_test.go`)
- Modify: `cmd/status.go`
- Modify: `cmd/status_test.go`

- [ ] **Step 1: Write failing tests**

Append to `cmd/lint_test.go`:

1. `TestLint_SchemaDriftWarning_WhenPriorPagesExist` — synthetic wiki with 5 pages, 3 at active hash and 2 at prior hash; `runLint`; assert stderr contains "schema_drift: 2 pages were ingested under a prior schema" with the spec's full multi-line warning shape (run `llmwiki schema migrate` recommendation, "do nothing" lazy-migration recommendation).
2. `TestLint_NoSchemaDriftWarning_AllAtActive` — all pages at active hash; assert no `schema_drift` line.
3. `TestLint_SchemaDrift_PreservesExistingLintOutput` — synthetic with both schema drift AND a contradiction; assert both surfaces fire (schema drift line AND contradiction list); the new schema line doesn't suppress existing lint output.

Append to `cmd/status_test.go`:

4. `TestStatus_ShowsSchemaLine_WithAGENTSMd` — wiki with custom AGENTS.md; `runStatus`; assert stdout contains `schema: AGENTS.md (hash <8-char prefix>..., 0 pages on prior hash)`.
5. `TestStatus_ShowsSchemaLine_NoAGENTSMd` — wiki without AGENTS.md; assert stdout contains `schema: bundled (no AGENTS.md), 0 pages on prior hash`.
6. `TestStatus_ShowsSchemaLine_WithDriftedPages` — pre-seed 5 pages at prior hash; assert stdout contains `pages on prior hash a3f...)` with the drift count surfaced.

- [ ] **Step 2: Run tests and confirm failure**

Run: `go test ./cmd/ -run "TestLint_Schema|TestStatus_Schema" -v`
Expected: FAIL.

- [ ] **Step 3: Wire `cmd/lint.go`**

In `cmd/lint.go`'s `runLint`:

```go
// After the existing contradiction-detection output, add the
// schema-drift surface:
_, prior, err := database.CountPagesByHashState(activeSchema.Hash())
if err != nil {
    fmt.Fprintf(os.Stderr, "  WARN reading schema_hash counters: %v\n", err)
} else if prior > 0 {
    fmt.Printf("!! schema_drift: %d pages were ingested under a prior schema (hash %s)\n", prior, "..." )
    fmt.Printf("                 The active schema (hash %s) defines a different ontology or prompt set.\n", activeSchema.Hash()[:8]+"...")
    fmt.Println()
    fmt.Println("                 To bring all pages up to the new schema:")
    fmt.Println("                   llmwiki schema migrate")
    fmt.Println("                 (runs cross-page page-update on every page; expensive;")
    fmt.Println("                  see `llmwiki schema migrate --help`)")
    fmt.Println()
    fmt.Println("                 To bring pages up lazily as new sources arrive: do nothing.")
    fmt.Println("                 The next `ingest` that touches a given page via the")
    fmt.Println("                 cross-page update pass will bring it to schema.")
    fmt.Println()
}
```

- [ ] **Step 4: Wire `cmd/status.go`**

In `cmd/status.go`'s `runStatus`:

```go
// After the existing "database: ..." line:
current, prior, err := database.CountPagesByHashState(activeSchema.Hash())
if err != nil {
    fmt.Fprintf(os.Stderr, "  WARN reading schema_hash counters: %v\n", err)
} else {
    label := "AGENTS.md"
    if activeSchema.DocPath == "" { label = "bundled (no AGENTS.md)" }
    if prior > 0 {
        fmt.Printf("schema: %s (hash %s..., %d pages on prior hash)\n", label, activeSchema.Hash()[:8], prior)
    } else {
        fmt.Printf("schema: %s (hash %s..., %d pages on active hash)\n", label, activeSchema.Hash()[:8], current)
    }
}
```

- [ ] **Step 5: Run tests and confirm pass**

Run: `go test ./cmd/ -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
feat(cmd): schema_drift surfaces in lint + status

cmd/lint.go gains a schema_drift warning when CountPagesByHashState
returns a non-zero prior count. The warning is verbose-on-purpose:
names the active hash, names the prior count, recommends both eager
(`llmwiki schema migrate`) and lazy (do nothing — the cross-page
update pass touches pages naturally) remediation, lets the user
decide. The wiki does not auto-rebuild — that decision stays the
user's (per spec §Goals point 6).

cmd/status.go gains a `schema:` line: "AGENTS.md (hash 91e...,
N pages on prior hash)" or "bundled (no AGENTS.md), N pages on
active hash". Surfaces the drift count alongside the existing
pages_updated_total / pages_update_failed_total lines.

Pre-v0.7 wikis (where every page has schema_hash = '') see prior
== total_pages on first lint after upgrade, which is exactly the
right user signal: "your existing pages were ingested before the
schema layer existed; here's how to bring them up if you want."

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase I — MCP `get_schema`

### Task 14: New read-only `mcp.get_schema` tool; `serverVersion` bump to `0.7.0-rc.1`

**Files:**
- Modify: `internal/mcp/server.go`
- Modify: `internal/mcp/handlers.go`
- Modify: `internal/mcp/server_test.go`

- [ ] **Step 1: Write failing tests**

Append to `internal/mcp/server_test.go`:

1. `TestServerVersionIs070` — assert `serverVersion == "0.7.0-rc.1"`.
2. `TestGetSchema_BundledByDefault` — drive the MCP server in-process with no AGENTS.md; call `get_schema`; assert the returned payload's `doc_path == ""`, `hash == schema.Bundled().Hash()`, and `schema_version == 1`.
3. `TestGetSchema_ReturnsActivePromptsAndOntology` — write a custom AGENTS.md fixture; drive the server; call `get_schema`; assert the payload's `prompts.ingest` is the user's text, `ontology_fields == [<field names>]`, `doc_path == "AGENTS.md"`.
4. `TestGetSchema_ReadOnly_NoSetSchemaTool` — list registered tools; assert no tool named `set_schema`, `write_schema`, or similar (Q15: read-only is the contract).
5. `TestGetSchema_ResponseShape` — pin the exact JSON keys of the response (`schema_version`, `domain`, `ontology_fields`, `prompts.{ingest,update_existing,ask,contradiction,promote_rewrite,lint_contradictions}`, `glossary`, `hash`, `doc_path`).

- [ ] **Step 2: Run tests and confirm failure**

Run: `go test ./internal/mcp/ -run "TestServerVersionIs070|TestGetSchema" -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

`internal/mcp/server.go`:

```go
const (
    serverName    = "llmwiki"
    serverVersion = "0.7.0-rc.1" // bumped from "0.6.0-rc.1" for sub-project 7 (user-editable schema)
)

type Deps struct {
    Cfg    Config
    DB     *db.DB
    Client llm.Client
    Schema schema.Schema  // sub-project 7: passed in once at server start.
}

func NewServer(d Deps) *mcpsrv.MCPServer {
    s := mcpsrv.NewMCPServer(serverName, serverVersion)
    s.AddTool(listPagesTool(), listPagesHandler(d))
    s.AddTool(readPageTool(), readPageHandler(d))
    s.AddTool(lintTool(), lintHandler(d))
    s.AddTool(askTool(), askHandler(d))
    s.AddTool(writePageTool(), writePageHandler(d))
    s.AddTool(ingestTool(), ingestHandler(d))
    s.AddTool(promoteAnswerTool(), promoteAnswerHandler(d))
    s.AddTool(getSchemaTool(), getSchemaHandler(d))  // sub-project 7
    return s
}

func getSchemaTool() mcpgo.Tool {
    return mcpgo.NewTool(
        "get_schema",
        mcpgo.WithDescription("Return the active wiki schema (the merged bundled+user content). Read-only; no per-call overrides. Agents that introspect the schema before acting (Karpathy-pattern compliant) can adapt their behaviour to 'this wiki is about meeting notes' without out-of-band signalling."),
    )
}
```

`internal/mcp/handlers.go`:

```go
func getSchemaHandler(d Deps) mcpsrv.ToolHandlerFunc {
    return func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
        sch := d.Schema
        ontologyFields := make([]string, len(sch.Ontology.Fields))
        for i, f := range sch.Ontology.Fields { ontologyFields[i] = f.DeclaredName }
        glossary := make([]map[string]string, len(sch.Glossary))
        for i, g := range sch.Glossary {
            glossary[i] = map[string]string{"term": g.Term, "definition": g.Definition}
        }
        return jsonResult(map[string]any{
            "schema_version":  sch.Version,
            "domain":          sch.Domain,
            "ontology_fields": ontologyFields,
            "prompts": map[string]string{
                "ingest":               sch.Prompts.Ingest,
                "update_existing":      sch.Prompts.UpdateExisting,
                "ask":                  sch.Prompts.Ask,
                "contradiction":        sch.Prompts.Contradiction,
                "promote_rewrite":      sch.Prompts.PromoteRewrite,
                "lint_contradictions":  sch.Prompts.LintContradictions,
            },
            "glossary":  glossary,
            "hash":      sch.Hash(),
            "doc_path":  sch.DocPath,
        })
    }
}
```

Existing handlers (`ingestHandler`, `askHandler`, `promoteAnswerHandler`, `writePageHandler`) thread `d.Schema` into the wiki entrypoints. `writePageHandler`'s post-write path adds a `database.UpdateSchemaHash(rec.ID, d.Schema.Hash())` call mirroring Phase D Task 8.

- [ ] **Step 4: Run tests and confirm pass**

Run: `go test ./internal/mcp/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
feat(mcp): mcp.get_schema read-only tool; serverVersion 0.7.0-rc.1

A new MCP tool, mcp.get_schema, returns the active wiki schema as
a structured payload (schema_version, domain, ontology_fields,
prompts.{ingest,update_existing,ask,contradiction,promote_rewrite,
lint_contradictions}, glossary, hash, doc_path). Read-only — no
mcp.set_schema or mcp.write_schema (Q15). An agent that can rewrite
the system prompts an agent runs against is a confused-deputy
surface; agents read the schema to adapt behaviour, they do not
edit it. The schema is the user's; agents introspect; that's it.

Deps gains a Schema field, populated at server start from
cmd/mcp.go (which reads activeSchema). Existing handlers
(ingestHandler, askHandler, promoteAnswerHandler, writePageHandler)
thread d.Schema into the wiki entrypoints. writePageHandler's
post-write path stamps schema_hash via UpdateSchemaHash.

serverVersion bumps from "0.6.0-rc.1" to "0.7.0-rc.1".

The Karpathy-pattern alignment is now complete over MCP: an agent
can introspect the schema to learn the wiki's domain, then ingest
sources tailored to that domain, all in one round-trip. No
out-of-band signalling.

TRUST PROPERTY UNCHANGED OVER MCP. Pages reaching disk via the
MCP write paths still pass through ValidateAndAttachEvidence; the
schema parameterises what is asked, not what is checked.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase J — Ontology rename plumbing

### Task 15: `WritePage` reads field-name overrides from `Schema.Ontology`

**Files:**
- Modify: `internal/wiki/page.go`
- Modify: `internal/wiki/page_test.go`

- [ ] **Step 1: Write failing tests**

Append to `internal/wiki/page_test.go`:

1. `TestWritePage_BundledSchema_ProducesV06FrontmatterByteIdentical` — write a Page with bundled schema; assert the on-disk frontmatter is byte-identical to v0.6's `WritePage` output (the regression guard for "bundled defaults are byte-identical to v0.6").
2. `TestWritePage_RenamedSchema_EmitsRenamedKeys` — schema renames `evidence` → `citations` and `body` → (no rename — body lives in the post-frontmatter region, not in frontmatter); call `WritePageWithSchema(p, dir, sch)`; assert the on-disk frontmatter has `citations:\n  - quote: ...` instead of `evidence:`.
3. `TestWritePage_ReorderedSchema_EmitsInDeclaredOrder` — schema reorders `[title, body, evidence]` to `[evidence, title, body]`; assert the frontmatter has `evidence:` before `title:` (the user's declared order takes effect on disk).
4. `TestWritePage_ExtraFrontmatterPassThrough_DeclaredButUnvalidated` — schema declares a `priority` field (not in the canonical set); the Page struct doesn't carry it; assert the on-disk frontmatter does NOT have `priority:` (we don't fabricate values for declared-but-untyped fields). The pass-through path is for *reading* (Task 16); writing doesn't fabricate.
5. `TestWritePage_RenamedSchema_TitleNotRenamed_StillWritesTitle` — sanity guard: not every field is renamed; assert unrenamed fields use canonical names.
6. `TestWritePage_BackwardsCompatShim_NoSchemaArg_UsesBundled` — call legacy `WritePage(p, dir)`; assert behaviour identical to `WritePageWithSchema(p, dir, schema.Bundled())`.

- [ ] **Step 2: Run tests and confirm failure**

Run: `go test ./internal/wiki/ -run TestWritePage -v`
Expected: FAIL.

- [ ] **Step 3: Refactor `WritePage`**

```go
// WritePageWithSchema is the schema-aware page writer; emits
// frontmatter using the field-name overrides declared in
// sch.Ontology. The legacy WritePage signature delegates with
// schema.Bundled() for backwards compat.
func WritePageWithSchema(p Page, wikiDir string, sch schema.Schema) error {
    // build a name-resolver: canonical -> declared
    declaredFor := make(map[string]string, len(sch.Ontology.Fields))
    for _, f := range sch.Ontology.Fields {
        declaredFor[f.CanonicalName] = f.DeclaredName
    }
    name := func(canonical string) string {
        if d, ok := declaredFor[canonical]; ok && d != "" { return d }
        return canonical
    }

    // build sections in the user's declared order:
    // walk sch.Ontology.Fields in order; emit each canonical-known
    // field's lines using name(canonical). Fields the user reordered
    // come out reordered.

    path := PagePath(wikiDir, p.Title)
    if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil { return err }
    var sb strings.Builder
    sb.WriteString("---\n")
    for _, f := range sch.Ontology.Fields {
        switch f.CanonicalName {
        case "title":
            sb.WriteString(fmt.Sprintf("%s: %s\n", name("title"), p.Title))
        case "updated_at":
            sb.WriteString(fmt.Sprintf("%s: %s\n", name("updated_at"), p.UpdatedAt.UTC().Format(time.RFC3339)))
        case "content_hash":
            sb.WriteString(fmt.Sprintf("%s: %s\n", name("content_hash"), p.ContentHash))
        case "source_ids":
            // ... existing logic with name("source_ids")
        case "tags":
            // ... existing logic with name("tags")
        case "sources":
            // ... existing logic with name("sources")
        case "created":
            // ... existing logic with name("created")
        case "links":
            // ... existing logic with name("links")
        case "evidence":
            // ... existing logic with name("evidence")
        case "body":
            // body lives below the closing --- ; no frontmatter emission for it.
        }
    }
    sb.WriteString("---\n\n")
    sb.WriteString(p.Body)
    return os.WriteFile(path, []byte(sb.String()), 0644)
}

// Backwards-compat shim. Legacy callers see no behaviour change.
func WritePage(p Page, wikiDir string) error {
    return WritePageWithSchema(p, wikiDir, schema.Bundled())
}
```

The bundled-default ontology field order must match the v0.6 emission order exactly (title, updated_at, content_hash, source_ids, tags, sources, created, updated, links, evidence — note the v0.6 file emits both `updated_at` and the date-only `updated:` twin; the bundled ontology declares both as canonical fields, with `updated_at` at canonical position 7 and `updated` as a derived twin emitted alongside). Test 1 pins the byte-identical contract.

- [ ] **Step 4: Run tests and confirm pass**

Run: `go test ./internal/wiki/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
feat(wiki): WritePage reads ontology rename + reorder from schema.Schema

WritePageWithSchema(p, dir, sch) is the new schema-aware writer.
WritePage(p, dir) keeps its legacy signature and delegates with
schema.Bundled() — pre-v0.7 callers see no behaviour change.

The schema-aware path consults sch.Ontology.Fields:
  - field NAMES are renameable (e.g. evidence -> citations); the
    canonical struct field stays "evidence", the on-disk frontmatter
    key is whatever the user declared.
  - field ORDER is determined by the user's declared order in
    `## Page ontology`; reorder is a re-walk of the slice.
  - the bundled default's order matches v0.6 emission byte-for-byte
    (test #1 pins this); a v0.6 wiki opening under v0.7 with no
    AGENTS.md produces frontmatter byte-identical to v0.6.

WritePage does NOT fabricate values for declared-but-untyped fields
(e.g. a schema that declares `priority: string` doesn't get a
`priority: ""` line on disk; the field has to come from the Page
struct, which today doesn't carry it). The extra-frontmatter
pass-through (Q9) is for *reading* pages with such fields; writing
them is a v0.8+ question.

TRUST PROPERTY REAFFIRMED. The canonical struct field carrying
evidence quotes is fixed at position 3 of the canonical list. The
rename is a name-string mapping over WritePage emission only — the
validator pins the *check* to the canonical struct field regardless
of what the user named it. A user who renames evidence -> citations
on disk still has their citations validated against source-file
bytes, byte-for-byte.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 16: `ParsePage` reads back via the same map; pre-v0.7 fallback; extra-frontmatter pass-through

**Files:**
- Modify: `internal/wiki/page.go`
- Modify: `internal/wiki/page_test.go`

- [ ] **Step 1: Write failing tests**

Append to `internal/wiki/page_test.go`:

1. `TestParsePage_RenamedSchema_ReadsRenamedKeys` — write a page with `WritePageWithSchema` under a renamed schema (`evidence` → `citations`); call `ParsePageWithSchema(content, sch)`; assert `Page.Evidence` is populated (the rename is invisible at the struct level).
2. `TestParsePage_PreV07Page_FallsBackToBundledNames` — content with canonical `evidence:` on disk; parse under a renamed schema (`evidence` → `citations`); assert `Page.Evidence` is still populated (the bundled-default name set is the fallback for pre-v0.7 pages).
3. `TestParsePage_BothNamesPresent_PrefersDeclared` — content with both `evidence:` and `citations:` (pathological case); parse under a renamed schema; assert `Page.Evidence` is populated from the *declared* name (`citations:`).
4. `TestParsePage_ExtraFrontmatterPassThrough_DeclaredButUnknown` — schema declares `priority` (extra field); content has `priority: high`; parse; assert `Page.ExtraFrontmatter["priority"] == "high"` (a new map field on Page, populated only by the schema-aware parser).
5. `TestParsePage_BackwardsCompatShim_NoSchemaArg_UsesBundled` — call legacy `ParsePage(content)`; assert behaviour identical to `ParsePageWithSchema(content, schema.Bundled())`.
6. `TestRoundTrip_RenamedSchema` — write a Page with `WritePageWithSchema` → read with `ParsePageWithSchema` (same schema); assert the round-tripped Page is structurally equal to the input.

- [ ] **Step 2: Run tests and confirm failure**

Run: `go test ./internal/wiki/ -run "TestParsePage|TestRoundTrip" -v`
Expected: FAIL.

- [ ] **Step 3: Refactor `ParsePage` and extend `Page`**

```go
type Page struct {
    Title       string
    Body        string
    Links       []Link
    SourceIDs   []int64
    ContentHash string
    UpdatedAt   time.Time
    Evidence    []Evidence
    Tags        []string
    Sources     []string
    Created     time.Time
    // sub-project 7: extra frontmatter declared in the schema's
    // `## Page ontology` but not in the canonical struct set.
    // Round-tripped on Read/Write under the schema-aware path;
    // the validator does not check these values.
    ExtraFrontmatter map[string]string
}

// ParsePageWithSchema parses page content using the schema's
// declared field names; falls back to canonical names for keys
// the schema didn't rename.
func ParsePageWithSchema(content string, sch schema.Schema) (Page, error) {
    declaredFor := make(map[string]string, len(sch.Ontology.Fields))
    for _, f := range sch.Ontology.Fields {
        declaredFor[f.CanonicalName] = f.DeclaredName
    }
    canonicalFor := make(map[string]string, len(sch.Ontology.Fields))
    for _, f := range sch.Ontology.Fields {
        canonicalFor[f.DeclaredName] = f.CanonicalName
        canonicalFor[f.CanonicalName] = f.CanonicalName // pre-v0.7 fallback
    }

    // ... existing parser logic, but every key lookup goes through
    // canonicalFor[<key>] to resolve to the canonical name; unknown
    // keys land in ExtraFrontmatter.
}

func ParsePage(content string) (Page, error) {
    return ParsePageWithSchema(content, schema.Bundled())
}
```

The schema-aware parser builds the bidirectional map at parse-start: declared → canonical AND canonical → canonical (so pre-v0.7 pages with canonical names still parse correctly under a renamed schema). Unknown keys (declared by the schema but with no canonical mapping, OR present in the file but not declared anywhere) land in `ExtraFrontmatter` for round-tripping.

- [ ] **Step 4: Run tests and confirm pass**

Run: `go test ./internal/wiki/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
feat(wiki): ParsePage reads ontology renames; pre-v0.7 fallback; extra-frontmatter pass-through

ParsePageWithSchema(content, sch) is the schema-aware reader that
matches WritePageWithSchema's emission. The bidirectional map
declared→canonical + canonical→canonical (built at parse-start)
makes the parser agnostic to whether a page on disk was written
under the active schema, a prior schema, or pre-v0.7 (canonical
names only) — every case parses to the same Page struct.

When both the renamed and the canonical key are present
(pathological case from a botched migration), the declared name
wins. The page_update_log surface from sub-project 6b plus the
schema_drift surface from Phase H Task 13 will surface this
condition for the user to investigate.

Page.ExtraFrontmatter (new map[string]string field) holds keys
that the schema declares but the canonical struct doesn't carry
(Q9 — extra-frontmatter pass-through for declared-but-unvalidated
fields). Round-tripped on Read/Write under the schema-aware path.
The validator does NOT check these values — that's exactly the
extension point for v0.8's "truly new structured fields with
their own validation."

Backwards-compat shim: legacy ParsePage(content) delegates with
schema.Bundled(). All existing callers see zero behaviour change.

TRUST PROPERTY REAFFIRMED. The canonical "Evidence" struct field
is fixed at position 3. The validator reads p.Evidence
unconditionally, regardless of whether the user named it
"evidence", "citations", or "quotes" on disk. The substring-match
check is bundled and unreachable from the schema; the rename is a
naming convenience over disk emission, never a check loosener.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase K — Cassettes

### Task 17: `TestSchemaRenameRoundtrip` cassette

**Files:**
- Modify: `cmd/schema_integration_test.go` (create if absent)
- Create: `internal/llm/testdata/cassettes/TestSchemaRenameRoundtrip__*.json`

- [ ] **Step 1: Write failing test**

```go
func TestSchemaRenameRoundtrip(t *testing.T) {
    if testing.Short() { t.Skip("skipping cassette test in -short mode") }
    if _, err := os.Stat("../internal/llm/testdata/cassettes/TestSchemaRenameRoundtrip__001.json"); os.IsNotExist(err) {
        t.Skip("cassette not recorded; run with LLMWIKI_RECORD=1 GEMINI_API_KEY=... to record")
    }
    setEnv(t, "LLMWIKI_CASSETTE", "TestSchemaRenameRoundtrip")
    setEnv(t, "GEMINI_API_KEY", "test-key-for-replay")

    // 1. mkdir tmp wiki + provider=gemini config.toml.
    // 2. Write AGENTS.md renaming evidence -> citations and body -> summary.
    // 3. Run llmwiki ingest ./fixture-source.md (synthetic small markdown).
    // 4. Read the produced page from disk; assert frontmatter has
    //    `citations:` (not `evidence:`) and the body lives below
    //    the frontmatter as usual.
    // 5. Re-parse via ParsePage; assert Page.Evidence is populated
    //    (the rename is invisible at the Page struct level).
    // 6. Assert db row has schema_hash = sha256(AGENTS.md bytes).
    // 7. Trust property check: walk evidence, assert every quote
    //    substring-matches the source file.
}
```

- [ ] **Step 2: Record the cassette**

```bash
export GEMINI_API_KEY=...
LLMWIKI_RECORD=1 go test ./cmd/ -run TestSchemaRenameRoundtrip -v
```

- [ ] **Step 3: Run replay and confirm pass**

Run: `unset GEMINI_API_KEY && go test ./cmd/ -run TestSchemaRenameRoundtrip -v`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
test(cassette): TestSchemaRenameRoundtrip — schema rename ingest end-to-end

Drives v0.7's ontology rename surface end-to-end against a recorded
Gemini Flash cassette. Pre-seeds an AGENTS.md renaming evidence ->
citations and body -> summary; ingests one source; asserts the
produced page on disk carries the renamed frontmatter keys; asserts
the round-trip via ParsePage repopulates Page.Evidence (rename
invisible at the struct level); asserts schema_hash on the DB row
equals sha256(AGENTS.md); asserts every evidence quote
substring-matches the source.

Recording target: Gemini Flash for the heavy fan-out (cassette
refresh stays free per spec risk #2 follow-on).

TRUST PROPERTY HOLDS UNDER RENAME. The validator is bundled and
pinned to the canonical struct field; "evidence" on the struct is
"citations" on disk, but the substring-match check sees source
file bytes either way.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 18: `TestSchemaMigrate` cassette

**Files:**
- Modify: `cmd/schema_integration_test.go`
- Create: `internal/llm/testdata/cassettes/TestSchemaMigrate__*.json`

- [ ] **Step 1: Write failing test**

```go
func TestSchemaMigrate(t *testing.T) {
    if _, err := os.Stat("../internal/llm/testdata/cassettes/TestSchemaMigrate__001.json"); os.IsNotExist(err) {
        t.Skip("cassette not recorded; run with LLMWIKI_RECORD=1 GEMINI_API_KEY=... to record")
    }
    setEnv(t, "LLMWIKI_CASSETTE", "TestSchemaMigrate")
    setEnv(t, "GEMINI_API_KEY", "test-key-for-replay")

    // 1. Set up tmp wiki + provider=gemini config.
    // 2. Pre-seed 5 pages under bundled defaults (5 ingest calls).
    // 3. Write a custom AGENTS.md (cosmetically edit `## Domain`).
    // 4. Verify llmwiki status reports 5 pages on prior hash.
    // 5. Run llmwiki schema migrate --yes (5 LLM calls — one per page).
    // 6. Verify status now reports 0 pages on prior hash; all 5 at active hash.
    // 7. Verify .llmwiki/log.md has a `**schema_migrate**` entry.
    // 8. Trust property check: re-walk every page's evidence on disk;
    //    every quote substring-matches its source.
}
```

- [ ] **Step 2: Record the cassette**

```bash
export GEMINI_API_KEY=...
LLMWIKI_RECORD=1 go test ./cmd/ -run TestSchemaMigrate -v
```

- [ ] **Step 3: Run replay and confirm pass**

Run: `unset GEMINI_API_KEY && go test ./cmd/ -run TestSchemaMigrate -v`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
test(cassette): TestSchemaMigrate — eager schema migrate end-to-end

Pre-seeds 5 pages under bundled defaults; cosmetically edits
AGENTS.md to bump the hash; runs llmwiki schema migrate --yes;
asserts all 5 pages reach the active hash; asserts the log.md
schema_migrate entry; trust-property checks every quote on disk.

Resumability is exercised implicitly: the second cassette run
through replay finds all 5 pages already at the active hash
and reports "no pages on prior schema; nothing to do" — so the
test re-stages the prior-hash state in step 4 to keep the
cassette deterministic.

TRUST PROPERTY HOLDS UNDER MIGRATE. ValidateAndAttachEvidence
is the gate over the migrate path too; pages whose proposed
body fails validation stay at their prior version.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 19: `TestMCPGetSchema` (no LLM — pure in-process)

**Files:**
- Modify: `internal/mcp/server_integration_test.go` (create if absent)

- [ ] **Step 1: Write the test (no recording — pure in-process)**

```go
func TestMCPGetSchema_BundledByDefault(t *testing.T) {
    // 1. Set up tmp wiki, no AGENTS.md.
    // 2. Build mcp.Deps with schema.Bundled().
    // 3. Spin up the in-process server (matching sub-project 5's
    //    TestMCPWritePageRoundtrip pattern).
    // 4. Call get_schema via the MCP request shape.
    // 5. Assert the response payload's keys + values:
    //    - schema_version == 1
    //    - domain == "A general-purpose wiki. ..." (bundled domain text)
    //    - ontology_fields == [title, body, evidence, links, sources,
    //                          tags, created, updated_at, content_hash, source_ids]
    //    - prompts.ingest contains "{{domain}}" interpolated to the
    //      bundled domain string (rendered)
    //    - hash == schema.Bundled().Hash()
    //    - doc_path == ""
}

func TestMCPGetSchema_WithCustomAGENTSMd(t *testing.T) {
    // Same shape, but write a custom AGENTS.md first; assert
    // the response reflects the user's content (not the bundled).
}

func TestMCPServer_NoSetSchemaTool(t *testing.T) {
    // Call ListTools; assert no tool name contains "set_schema"
    // or "write_schema". Q15 — read-only is the contract.
}
```

- [ ] **Step 2: Run tests and confirm pass**

Run: `go test ./internal/mcp/ -run TestMCPGetSchema -v`
Expected: PASS (no recording needed; pure in-process).

- [ ] **Step 3: Commit**

```bash
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
test(mcp): TestMCPGetSchema — read-only schema introspection over MCP

Pure in-process tests (no LLM, no cassette) covering the new
mcp.get_schema tool. Verifies the structured response payload's
keys (schema_version, domain, ontology_fields, prompts.{...},
glossary, hash, doc_path) under both Bundled() and a
user-supplied AGENTS.md; verifies the absence of any
set_schema / write_schema tool (Q15 — read-only is the
contract; agents introspect, they do not edit).

The Karpathy-pattern alignment is now exercised under MCP: an
agent could fetch get_schema, learn this is a "Distributed-systems
papers reading log" wiki, and ingest sources with that context in
mind, all in one round-trip. No out-of-band signalling.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase L — Docs + tag

### Task 20: README "Customising your wiki" section + CHANGELOG `[0.7.0-rc.1]`

**Files:**
- Modify: `README.md`
- Modify: `CHANGELOG.md`

- [ ] **Step 1: Add the "Customising your wiki" section to README**

Place AFTER the existing "Living Wiki" section (which describes promote / retro-link / contradiction surfaces / cross-page updates). Heading: `## Customising your wiki (AGENTS.md)`.

Required content:

1. **Lead with Karpathy.** First paragraph: "Andrej Karpathy's [LLM-wiki gist](https://gist.github.com/karpathy/442a6bf555914893e9891c11519de94f) describes a three-layer architecture: raw sources, the wiki, and the schema. As of v0.7, llmwiki ships the third layer: a user-owned `AGENTS.md` document at your wiki root that defines the page ontology, the prompts that drive every LLM call, and an optional glossary. The bundled defaults match v0.6 behaviour byte-for-byte, so an existing wiki sees zero behaviour change until you create the file."
2. **`llmwiki init` writes it.** "Running `llmwiki init` now writes a default `AGENTS.md` alongside `.llmwiki/config.toml`. Open it in your editor of choice (Obsidian renders it natively); rewrite the `## Domain` section to describe your actual wiki; tweak the `## Ingest prompt` to bias toward 'one comprehensive page per concept' or whatever shape suits you. The bundled doc walks you through every section."
3. **The trust property is bundled.** **Bold** the trust-property statement: "**The schema controls what the LLM is *asked*, not what counts as valid evidence.** llmwiki's substring-match validator is bundled in the binary and runs after every LLM call regardless of what the schema-rendered prompt told the LLM. The worst a malicious or compromised schema can do is degrade quality (more pages get dropped, fewer pages land); it cannot ground a false claim."
4. **`llmwiki schema show` to discover defaults.** "Run `llmwiki schema show --bundled` to print the bundled-default doc; `llmwiki schema show --doc` prints your `AGENTS.md` verbatim; `llmwiki schema show` (no flag) prints the active merged content."
5. **`llmwiki schema validate` to iterate quickly.** "After editing `AGENTS.md`, run `llmwiki schema validate`. Errors out with file:line on missing required sections (`## Ingest prompt`, `## Domain`, ...) or missing required placeholders (`{{domain}}`, `{{existing_titles}}`, ...). Structural validation only — quality is still on you."
6. **`llmwiki schema migrate` for eager migration.** "Changing the schema doesn't auto-rebuild your wiki. Existing pages keep their prior frontmatter and prose; the new `schema:` line in `llmwiki status` and `schema_drift:` warning in `llmwiki lint` surface the count. To bring everything to the new schema in one go, run `llmwiki schema migrate` (one LLM call per page; cost depends on provider). To bring pages up lazily, do nothing — the next `ingest` that touches a given page (via `--update-existing` from v0.6) brings it to schema naturally."
7. **MCP introspection.** "Agents over MCP can call `mcp.get_schema` to introspect the active schema before acting — Karpathy-pattern compliant. Read-only; no per-call overrides; the schema is the user's, not the agent's."
8. **Source-control your schema.** "Check `AGENTS.md` and `.llmwiki/config.toml` into git. `llmwiki schema show --hash` is a scriptable way to compare schemas across wikis sharing a doc."

Update the existing **Trust Property** section: add one paragraph noting v0.7's schema layer cannot loosen the validator — link back to the new "Customising" section's bold statement.

Update the existing **MCP** section: add one paragraph on the new `mcp.get_schema` tool.

- [ ] **Step 2: Add `[0.7.0-rc.1]` CHANGELOG entry**

```markdown
## [0.7.0-rc.1] — 2026-05-04

### Added
- `AGENTS.md` at the wiki root — the user-editable schema doc.
  Defines the page ontology and the six prompts driving ingest /
  ask / update-existing / contradiction / promote-rewrite / lint.
  Bundled defaults are byte-identical to v0.6 behaviour; user
  edits override per section. Karpathy's third layer.
- `llmwiki schema show [--bundled|--doc|--hash]` — inspect the
  effective schema (merged), the bundled default, the user doc
  verbatim, or just the active hash for scripting.
- `llmwiki schema validate` — structural validation with
  file:line errors; surfaces every problem at once via
  MultiError.
- `llmwiki schema migrate [--yes] [--dry-run]` — eager re-ingest
  of every page on a prior schema hash under the active schema.
  Resumable for free via per-page hash check.
- `llmwiki init` writes `AGENTS.md` alongside `.llmwiki/config.toml`.
  New `--rewrite-schema` flag overwrites an existing schema file
  (idempotent: by default `init` leaves an existing schema alone).
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

### Notes
- **Schema migration v4 → v5 is additive only.** New
  `schema_hash` column on `pages`; no `ALTER TABLE` on other
  tables; `evidence`, `sources`, `source_files`, `chunks`,
  `page_update_log` are byte-identical pre/post v5. Roll-forward
  only — no down-migration script.
- **Q1 — schema doc filename: `AGENTS.md` at wiki root**, not
  `.llmwiki/schema.md`. Karpathy alignment + multi-vendor
  convention. The original first cut was `.llmwiki/schema.md`;
  the user's directive prioritised Karpathy alignment +
  user-friendliness, and AGENTS.md won.
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
  over AGENTS.md does the same job for any user with `.llmwiki/`
  + `AGENTS.md` under source control (recommended).
- **No per-call schema overrides over MCP** (Q15). Per-call
  overrides re-introduce the agent-edits-the-system-prompts
  confused-deputy surface. The schema is loaded once at server
  start.
```

Move any `[Unreleased]` content into `[0.7.0-rc.1]`; leave a fresh empty `[Unreleased]` at the top.

- [ ] **Step 3: Commit**

```bash
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
docs(readme,changelog): [0.7.0-rc.1] — user-editable schema (AGENTS.md)

README gains a "Customising your wiki (AGENTS.md)" section that
leads with Karpathy's three-layer framing, walks the user through
init's new AGENTS.md, BOLD-CAPS the trust-property reaffirmation
("the schema controls what the LLM is *asked*, not what counts
as valid evidence"), points at schema show / validate / migrate
for the per-command surface, mentions mcp.get_schema for agent
introspection, and recommends git-tracking the schema doc. The
existing Trust Property section gets one paragraph linking back;
the existing MCP section names the new get_schema tool.

CHANGELOG [0.7.0-rc.1] covers the schema layer end-to-end:
AGENTS.md at wiki root (Q1), the four user-visible commands
(init writes it; schema show/validate/migrate inspect/iterate/
re-ingest), the v5 schema_hash migration (additive only), the
new mcp.get_schema tool (read-only, Q15), the lint+status drift
surfaces, the ontology rename + reorder + extra-frontmatter
pass-through (Q9), and the Q11/Q12/Q15 deferrals. The
trust-property reaffirmation is loud and final.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 21: Tag `v0.7.0-rc.1` locally (no push)

**Files:** none (tag only)

- [ ] **Step 1: Final pre-tag verification**

Run, top to bottom:

- [ ] `go test ./...` is green in replay mode (no API keys exported).
- [ ] `go build ./... && go vet ./...` clean.
- [ ] Manual smoke walks the spec's verification block (lines 760–828):

  ```bash
  unset GEMINI_API_KEY ANTHROPIC_API_KEY
  rm -rf /tmp/test-7-wiki && mkdir /tmp/test-7-wiki && cd /tmp/test-7-wiki
  llmwiki init                                              # gemini default
  ls AGENTS.md                                              # exists
  ls .llmwiki/                                              # config.toml, wiki/, raw/, answers/
  export GEMINI_API_KEY=...
  llmwiki schema show --doc | head -5                       # frontmatter
  llmwiki schema validate                                   # all ✓, exit 0
  llmwiki ingest ./README.md                                # writes pages, schema_hash stamped
  llmwiki status                                            # schema: AGENTS.md (hash X..., 0 prior)
  ${EDITOR} AGENTS.md                                       # edit `## Domain`
  llmwiki schema validate                                   # still ✓
  llmwiki status                                            # schema: AGENTS.md (hash Y..., N prior)
  llmwiki schema migrate --yes                              # bring all to active
  llmwiki status                                            # 0 pages on prior hash
  go test ./internal/mcp/... -run TestMCPGetSchema          # green
  ```
- [ ] Verify the trust property by hand: pick one page, walk its `evidence:` block in frontmatter, confirm every quote is byte-identical to a substring of the source file.
- [ ] Verify pre-v0.7 backwards compat: open a `v0.6` wiki under v0.7, no AGENTS.md; `llmwiki ingest` produces the same output as v0.6.
- [ ] Confirm `internal/mcp/server.go`'s `serverVersion == "0.7.0-rc.1"`.

- [ ] **Step 2: Tag**

```bash
git -c commit.gpgsign=false tag -a v0.7.0-rc.1 -m "$(cat <<'EOF'
v0.7.0-rc.1 — User-editable Schema (Karpathy AGENTS.md alignment) (sub-project 7)

The third Karpathy layer ships. The six load-bearing prompts and
the page ontology, hard-coded in the binary through v0.6, are now
bundled defaults; the user-owned AGENTS.md at the wiki root
overrides them per section.

  - AGENTS.md at the wiki root: structured Markdown with
    frontmatter (schema_version: 1, generator: llmwiki) and
    H2 sections (Domain, Page ontology, Ingest prompt,
    Update-existing prompt, Ask prompt, Contradiction prompt,
    Promote rewrite prompt, Lint contradictions prompt,
    Glossary). Bundled defaults are byte-identical to v0.6.
  - llmwiki init writes it; --rewrite-schema overwrites an
    existing one.
  - llmwiki schema show / validate / migrate inspect, iterate,
    and re-ingest. schema migrate is resumable for free via
    per-page hash check.
  - pages.schema_hash column at user_version = 5 (additive).
    Stamped on every WritePage write site. The lint and status
    surfaces report drift counters.
  - Ontology rename + reorder + extra-frontmatter pass-through.
    Truly new structured fields with their own validation
    deferred to v0.8+.
  - mcp.get_schema (read-only). No mcp.set_schema. Agents
    introspect; they do not edit.

TRUST PROPERTY HOLDS. The schema controls what the LLM is *asked*
and how the page is *shaped*. It does NOT control what counts as
valid evidence. wiki.ValidateAndAttachEvidence is bundled and
unreachable from the schema layer. A malicious or compromised
schema can degrade quality (fewer pages land, more drops); it
cannot ground a false claim. The pairing — your schema, validated
grounding, sources you trust — is the differentiator and v0.7
makes it complete.

Backwards compatibility: a v0.6 wiki opening under v0.7 with no
AGENTS.md sees zero behaviour change. Pre-v0.7 pages with
schema_hash = '' surface as "prior schema" in lint and status;
the user decides whether to migrate or let lazy migration through
the cross-page update pass bring them up over time.

Promotion to v0.7.0 is a manual follow-up after a 1-week
stability window — same shape as v0.5, shorter than v0.6's
2-week window because the schema layer is read-side-heavy
(prompt rendering + frontmatter naming) and lower-risk than
v0.6's validator-hostile cross-page updates.
EOF
)"
```

- [ ] **Step 3: Verify**

Run: `git tag -l "v0.7*"`
Expected: prints `v0.7.0-rc.1`.

Do **not** `git push --tags`. Promotion to a real release is a manual step matching v0.3 / v0.4 / v0.5 / v0.6's pattern.

---

## Done criteria

- All 21 tasks have a green checkbox.
- `go test ./...` is green in replay mode (no API keys required).
- `go build ./... && go vet ./...` clean.
- A fresh `mkdir wiki && cd wiki && llmwiki init` writes `AGENTS.md` at the wiki root and `.llmwiki/config.toml` alongside; `cat AGENTS.md` shows the bundled-default doc; `llmwiki schema validate` exits 0.
- Editing `AGENTS.md`'s `## Domain` and re-running `llmwiki schema validate` still exits 0; running `llmwiki ingest <source>` produces pages whose body reflects the custom domain text.
- `llmwiki status` shows `schema: AGENTS.md (hash <8-char prefix>..., N pages on active/prior hash)`.
- `sqlite3 .llmwiki/wiki.db "PRAGMA user_version"` returns `5` on a fresh wiki and on a v4-upgraded wiki; `pages.schema_hash` column exists; pre-v5 rows have `schema_hash = ''`.
- `llmwiki schema migrate --yes` walks every page on a prior hash, re-ingests under the active schema, ends with all pages at the active hash, and appends a `**schema_migrate**` log.md entry. Pages whose proposed body fails validation stay at their prior version.
- `llmwiki schema show --hash` prints exactly the active hex hash + newline.
- `llmwiki mcp` exposes the same seven tools as v0.6 plus `get_schema` (eight total). `mcp.get_schema` returns the structured payload with `schema_version`, `domain`, `ontology_fields`, `prompts.{ingest,update_existing,ask,contradiction,promote_rewrite,lint_contradictions}`, `glossary`, `hash`, `doc_path` keys. There is no `mcp.set_schema` or `mcp.write_schema` tool.
- The tag `v0.7.0-rc.1` exists locally; not pushed.
- README's "Customising your wiki (AGENTS.md)" section leads with Karpathy's three-layer framing, BOLD-bolds the trust-property reaffirmation, walks through `schema show / validate / migrate`, mentions `mcp.get_schema`, and recommends git-tracking AGENTS.md.
- CHANGELOG `[0.7.0-rc.1]` is explicit about: AGENTS.md filename (Q1), additive-only schema migration (Q5/migration), the four user-visible commands, the read-only MCP surface (Q7/Q15), the rename + reorder + extra-frontmatter scope (Q9), the v0.8+ deferrals (Q10/Q12), the schema-version single-integer (Q11), and the trust-property reaffirmation.
- **TRUST PROPERTY REAFFIRMED at every disk-write site:** `ingest`, `promote`, `mcp.write_page`, `mcp.promote_answer`, retro-linker (body-only), `update_existing`, **`schema migrate`** (this plan's new path), all stamp `schema_hash` AFTER `ValidateAndAttachEvidence` has gated the write. The schema layer is read-only over the validator; no schema-rendered prompt and no schema-declared ontology rename can loosen the substring-match check. v0.7 ships the third Karpathy layer without ceding the trust property — that pairing is the differentiator.
