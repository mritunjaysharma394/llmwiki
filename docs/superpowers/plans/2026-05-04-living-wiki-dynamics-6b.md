# Sub-project 6b — Living Wiki Dynamics — Cross-page Updates (v0.6) — Implementation Plan

> **Version-numbering note:** the spec for sub-project 6 was authored when the release line was v1.x and split sub-project 6 into 6a (v1.2) and 6b (v1.3); the line was renumbered to pre-1.0 on 2026-05-04. Where this plan refers to v0.5 read the spec's v1.2; where this plan refers to v0.6 read the spec's v1.3. Tags `v0.3.0`, `v0.4.0`, `v0.5.0-rc.1` already exist; this plan ships as `v0.6.0-rc.1`.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship v0.6 of `llmwiki` — pillar 3 of sub-project 6, the cross-page page-update pass, the validator-hostile half of "Living Wiki Dynamics" that sub-project 6a deferred. v0.5 made the wiki *notice* things (promote, retro-link, contradiction warn-only); v0.6 makes the wiki *update itself* — a new source's claims get folded into the existing pages whose claims it refines, qualifies, contradicts, or extends, with every disk write gated by the same byte-exact substring-match validator that gates `ingest` and `mcp.write_page`. The pillar is opt-in (`--update-existing` defaults off) because it can drop previously-valid content if the new source's quotes don't match — a deliberate trust-property guarantee, not a bug, but one users must understand. v0.5's three pillars (promote, retro-linker, contradiction warn-only) are out of scope for this plan; they shipped in `v0.5.0-rc.1`.

**Architecture:** One new file under `internal/wiki/` — `update_existing.go` (`UpdateExistingPagesFromSource` orchestrating candidate selection via `db.SearchPages` + `db.SearchEvidence`, per-candidate LLM call against the same `writePagesTool` schema `IngestSourceFilesToPages` uses, defensive `ValidateAndAttachEvidence` over the union of new + original source files, quote-floor `min(2, len(originalEvidence))`, content_hash skip, audit trail to `page_update_log`, concurrency capped at `ingestMaxInflight = 5` matching the chunk fan-out). One additive schema migration to v4 (`page_update_log` table only — no `ALTER TABLE`); three new queries (`DeleteEvidenceForPage`, `InsertPageUpdateLog`, `GetPageUpdateLog`). One new wire-in inside `IngestSource` between the contradiction-detection pass and `RegenerateIndex`, gated by `IngestOptions.UpdateExisting`. Two new `cmd/ingest` flags (`--update-existing`, `--debug-updates`); one new config key `[ingest] update_existing` plus three tunables; two new counters in `cmd/status`. `mcp.ingest` accepts new optional `update_existing: bool` and the return shape gains `pages_updated` + `pages_update_failed` (alongside v0.5's `retro_linked_pages` and `contradictions_flagged`); no new MCP tool. The contradiction-on-ingest pass is upgraded so that when `--update-existing` is on AND a contradiction is detected, the existing page becomes a forced candidate for the update pass (rather than only an FTS hit).

**Tech Stack:** Go 1.26. **No new direct dependencies.** All new code reuses `mark3labs/mcp-go v0.50.0` (pinned by sub-project 5), the existing `*sql.DB` over `modernc.org/sqlite`, and the configured `llm.Client` for the per-candidate update call. The update call uses `cfg.LLM.Model` — whatever provider the user configured at `init`. Spec recommends Gemini Flash for the heavy fan-out (free tier, 1500 requests/day comfortably absorbs 50 candidate updates per ingest); Anthropic Haiku tolerable at ~$0.30/ingest with prompt caching of the EXISTING-PAGE-EVIDENCE block.

**Spec:** [`docs/superpowers/specs/2026-05-04-living-wiki-dynamics-design.md`](../specs/2026-05-04-living-wiki-dynamics-design.md), pillar 3 (lines 285–362), schema (lines 343–369), CLI surface (lines 414–419), risks (lines 423–448), implementation order for v1.3 (lines 540–551), verification (lines 596–622).

**Resolved open questions** (the spec lists eleven; six are 6b-specific; v0.5's plan resolved Q1, Q2, Q3, Q4, Q6 — this plan resolves the remaining six):

1. **Q5 — Cross-page update candidate cap:** **20 per source, 50 per ingest.** Hard-coded as `defaultUpdateExistingMaxCandidatesPerSource = 20` and `defaultUpdateExistingMaxCandidatesTotal = 50` constants in `internal/wiki/update_existing.go`; tunable via `[ingest] update_existing_max_candidates_per_source` and `[ingest] update_existing_max_candidates_total` config keys. First-guess numbers; spec's risk #2 cost analysis assumes the 50 ceiling for Gemini Flash quota math.
2. **Q7 — Quote floor:** **`min(2, len(originalEvidence))`** as a constant, not a fraction. Lets pages that originally had only 1 quote not get held to a higher bar than they started at, while preventing a single weak update from replacing a 5-quote page. Configurable upper-bound via `[ingest] update_existing_quote_floor = 2` (the constant in the `min` expression).
3. **Q8 — Schema migration v3 → v4:** **additive only, no `ALTER TABLE` on existing tables.** New `page_update_log` table, two indexes. `pages`, `evidence`, `sources`, `source_files`, `chunks` are untouched. Roll-forward only — no down-migration script (matches every prior migration in the binary).
4. **Q9 — `page_update_log` retention:** **never rotated, never truncated.** At 100 ingests/year × 50 candidates × ~1 row each = 5000 rows/year. Even at 10× this is fine. Documented in CHANGELOG and in the migration's doc-comment.
5. **Q10 — MCP surface for cross-page updates:** **extend `mcp.ingest` return shape; no new `mcp.update_pages_from_source` tool.** Single round-trip — the agent gets the full ingest+update result in one call. `mcp.ingest` gains optional `update_existing: bool` arg; return shape gains `pages_updated: int` and `pages_update_failed: int`.
6. **Q11 — `--update-existing` default in v0.6:** **default off everywhere.** README's Living Wiki section is loud about the cost picture and the validator-interaction risk. CHANGELOG note: "consider flipping the default in v0.7 once we have real-world numbers from opt-in users."

Open questions Q1, Q2, Q3, Q4, Q6 are 6a-only and were resolved in `docs/superpowers/plans/2026-05-04-living-wiki-dynamics.md`.

---

## File Structure

| File | Purpose | Action |
|---|---|---|
| `internal/db/db.go` | v4 migration: additive `page_update_log` table + two indexes; bump `PRAGMA user_version` to 4. No `ALTER TABLE` on existing tables. | Modify |
| `internal/db/db_test.go` | `TestMigrate_FromV3_AddsPageUpdateLog`, `TestMigrate_FromFresh_LandsAtV4`, `TestMigrate_Idempotent_RerunningOnV4_IsNoop` | Modify |
| `internal/db/queries.go` | `DeleteEvidenceForPage(pageID)`, `InsertPageUpdateLog(entry)`, `GetPageUpdateLog(...)`, `CountPageUpdateLogByOutcome(...)` | Modify |
| `internal/db/queries_test.go` | Per-method unit tests against an in-memory v4 DB | Modify |
| `internal/wiki/update_existing.go` | `UpdateExistingPagesFromSource`, `UpdateExistingOptions`, `UpdateResult`, `UpdateFailure`, `DroppedQuote` (reuse from `promote.go`); the per-candidate prompt builder + parser; the quote-floor + content_hash-skip + audit-trail logic | Create |
| `internal/wiki/update_existing_test.go` | Candidate selection (FTS shortlist + per-source cap + global cap + dedup against new titles + contradiction-forced candidates); validator-drop → `update_failed`; quote-floor (`min(2, len(originalEvidence))` both arms); content_hash skip → `body_only`; concurrency under `ingestMaxInflight`; `page_update_log` rows on every outcome | Create |
| `internal/wiki/ingest_runner.go` | Wire `UpdateExistingPagesFromSource` into `IngestSource` after the contradiction-detection block and before `RegenerateIndex`; gate behind `opts.UpdateExisting`; extend `IngestRunResult` with `PagesUpdated`, `PagesUpdateFailed`, `UpdatedTitles`, `UpdateFailures`; pass `Contradictions` into the update pass as forced candidates | Modify |
| `internal/wiki/ingest_runner.go` (`IngestOptions`) | Add `UpdateExisting bool`, `DebugUpdates bool`, `UpdateExistingMaxCandidatesPerSource int`, `UpdateExistingMaxCandidatesTotal int`, `UpdateExistingQuoteFloor int` | Modify |
| `cmd/ingest.go` | `--update-existing` and `--debug-updates` flags; flag → `IngestOptions` translation; `[ingest] update_existing` config key precedence; per-tunable config-key fall-throughs | Modify |
| `cmd/ingest_test.go` | `TestIngest_UpdateExistingFlagDefaultsOff`, `TestIngest_UpdateExistingFlagOverridesConfigOff`, `TestIngest_UpdateExistingFlagFromConfig`, `TestIngest_DebugUpdatesFlag`, `TestIngest_TunablesFromConfig` | Modify |
| `cmd/root.go` | `IngestConfig` gains `UpdateExisting *bool` (the same `*bool` pattern `RespectGitignore` uses to disambiguate "absent" from "explicitly false") and three integer tunables; `applyIngestDefaults` fills them | Modify |
| `cmd/root_test.go` | `TestApplyIngestDefaults_UpdateExistingDefaultsFalse`, `TestApplyIngestDefaults_TunableDefaults` | Modify |
| `cmd/status.go` | Read `pages_updated_total` and `pages_update_failed_total` from `page_update_log` via new `db.CountPageUpdateLogByOutcome`; surface in the existing `runStatus` output | Modify |
| `cmd/status_test.go` | `TestStatus_ShowsPagesUpdatedTotal`, `TestStatus_ShowsPagesUpdateFailedTotal_WhenNonZero`, `TestStatus_OmitsLineWhenZero` | Create or modify |
| `internal/mcp/handlers.go` | `ingestHandler`: accept new optional `update_existing: bool` arg; extend return shape with `pages_updated: int` and `pages_update_failed: int`; update tool description and doc-comment return-shape block | Modify |
| `internal/mcp/server.go` | Bump `serverVersion` from `"0.5.0-rc.1"` to `"0.6.0-rc.1"`; extend `ingestTool()` schema with `update_existing` boolean argument | Modify |
| `internal/mcp/server_test.go` | `TestIngest_AcceptsUpdateExistingArg`, `TestIngest_ReturnShapeIncludesPagesUpdated`, `TestIngest_DefaultsUpdateExistingOff`, `TestServerVersionIs060` | Modify |
| `internal/llm/testdata/cassettes/TestUpdateExistingHappyPath__*.json` | Recorded cassette: pre-seed five existing pages, ingest a source overlapping three of them with `--update-existing`, assert `pages_updated == 3` | Create |
| `internal/llm/testdata/cassettes/TestUpdateExistingValidationDrop__*.json` | Recorded cassette: pre-seed page with 5 quotes, ingest a poorly-quoting source, assert `update_failed`, page body unchanged | Create |
| `cmd/ingest_integration_test.go` | Append `TestUpdateExistingHappyPath` and `TestUpdateExistingValidationDrop` cassette tests | Modify |
| `README.md` | New "Cross-page updates (opt-in)" subsection inside the existing "Living Wiki" section; opt-in framing, cost paragraph (Gemini Flash recommended), validator-interaction caveat, debug-flag guidance, links to `page_update_log` for audit | Modify |
| `CHANGELOG.md` | `## [0.6.0-rc.1] — 2026-05-04` entry: pillar 3, the contradiction-on-ingest upgrade, the schema migration, the `--update-existing` default-off note ("consider flipping in v0.7"), the no-down-migration note, the never-truncated `page_update_log` note | Modify |
| (tag) | `v0.6.0-rc.1` annotated tag, local only — do NOT push | Create |

**Total:** 17 tasks across 8 phases (A–H). Each task ends with a single commit; the working tree is green at every commit boundary (`go build ./... && go test ./...` clean in replay mode).

---

## Phase summaries

Each phase is self-contained: it does not depend on later-phase exports, and its last task leaves the tree compiling and `go test ./...` green so a fresh subagent can pick up the next phase from a clean checkout. **Pillar 3 is the most validator-hostile change in the binary's history.** Every task that writes a page body to disk reaffirms the trust property — no page reaches disk without ≥1 evidence quote that substring-matches its named source file, gated through `wiki.ValidateAndAttachEvidence`. The plan calls this out at every disk-write step.

- **Phase A — Schema migration to v4 + new queries (Tasks 1–2).** Additive `page_update_log` table at `PRAGMA user_version = 4`; two indexes (`idx_page_update_log_page` on `page_id`, `idx_page_update_log_source` on `source_id`). Roll-forward only, idempotent (`CREATE TABLE IF NOT EXISTS`). Three new queries: `DeleteEvidenceForPage(pageID)` to wipe a page's evidence rows before writing the updated set; `InsertPageUpdateLog(entry)` to append an audit row on every outcome (`updated` / `body_only` / `failed` / `skipped`); `GetPageUpdateLog(pageID, limit)` for `--debug-updates` and future `lint` integration; `CountPageUpdateLogByOutcome()` for `cmd/status`. Pure unit tests against fresh + v3 DBs. Risk: a v3 wiki opening for the first time after v0.6 silently runs the migration; the migration must not require any existing rows.

- **Phase B — `wiki.UpdateExistingPagesFromSource` core (Tasks 3–4).** Two-task split: B1 is candidate selection + plumbing (no LLM yet — fixture the `llm.Client` response); B2 is the per-candidate update loop, validator integration, quote-floor, content_hash skip, and `page_update_log` writes on every outcome. Candidate selection unions `db.SearchPages(newSourceFile.Content, perSourceCap)` with `db.SearchEvidence(newSourceFile.Content, perSourceCap)` per new source file, dedupes by page ID, excludes pages whose title is in `newPageTitles`, accepts a `forcedCandidateIDs` list (the contradiction-on-ingest bridge from Phase F), and caps at the global `maxCandidatesTotal`. The per-candidate call reuses `writePagesTool` and `ExtractPagesFromToolResult` exactly the way `IngestSourceFilesToPages` does — same schema, same parser, only the system prompt changes to the EXISTING-PAGE-aware variant. Validator pass calls `ValidateAndAttachEvidence` against the union of (new source files) + (synthesized `ingest.SourceFile` rows for the candidate's existing source files, read via `wiki.readSourceFileContent`). The trust property holds: every quote on the updated page substring-matches *some* file in the union. Risk: the spec's #1 risk — silent downgrades. The plan covers it via (a) quote-floor, (b) content_hash skip, (c) explicit `update_failed` outcome that *keeps* the prior version, (d) `page_update_log` row on every outcome.

- **Phase C — Wire pillar 3 into `IngestSource` (Task 5).** Insert the call between the contradiction-detection pass (which already ran in v0.5) and `RegenerateIndex`. Gated by `opts.UpdateExisting`. When the gate is open AND there are contradictions from Phase E of v0.5, the existing-page IDs from those contradictions feed in as `forcedCandidateIDs` (the contradiction-on-ingest upgrade — spec line 60: "upgraded to 'edit existing page' once 6b lands"). Order matters: (1) write new pages, (2) retro-link existing pages to new titles (already in v0.5), (3) detect contradictions (already in v0.5), (4) **update existing pages** (new in v0.6), (5) re-run retro-link over the union of (new titles + updated titles)? — see Task 5 step 4 for the resolution; spec line 448 prefers retro-link AFTER the update pass so retro-link sees the updated bodies, but v0.5 already runs retro-link before. The pragmatic choice for v0.6: a *second* retro-link pass after the update pass, scoped to only the updated titles, so newly-rewritten bodies pick up `[[wikilinks]]` to titles introduced this batch. `IngestRunResult` gains `PagesUpdated`, `PagesUpdateFailed`, `UpdatedTitles`, `UpdateFailures`. The summary line gains the per-page `~ Title (+N evidence)` block from Flow 4 of the spec.

- **Phase D — Config + CLI flags (Tasks 6–7).** D1 adds `[ingest] update_existing = false` plus three tunables to `IngestConfig`; `applyIngestDefaults` fills them. D2 adds `--update-existing` and `--debug-updates` flags to `cmd/ingest.go`; flag precedence is the existing layered shape (package defaults → `[ingest]` config → CLI flag, CLI wins). `cmd/status.go` reads `pages_updated_total` and `pages_update_failed_total` from `page_update_log` via the new query — pure read, no migration of `pages_total` / `evidence_quotes` etc. Default-off everywhere: a fresh wiki, a fresh ingest, no flags, no config block sees zero behaviour change vs v0.5.

- **Phase E — MCP surface (Task 8).** `mcp.ingest` accepts new optional `update_existing: bool` argument (defaults to false on the MCP boundary too). Return shape gains `pages_updated: int` and `pages_update_failed: int` alongside v0.5's `retro_linked_pages` and `contradictions_flagged`. No new MCP tool — single round-trip semantics per Q10. `serverVersion` bumps from `"0.5.0-rc.1"` to `"0.6.0-rc.1"`. The `ingestHandler` doc-comment return-shape block grows to document all six v0.6 keys.

- **Phase F — Contradiction → update bridge (Task 9).** When `--update-existing` is on AND `DetectIngestContradictions` returned non-empty, every existing page that appears as `Contradiction.ExistingTitle` becomes a *forced* candidate for the update pass: its page ID is passed in `forcedCandidateIDs` and bypasses the FTS shortlist + global cap (a contradiction is the strongest possible signal that this page is touched by the new source). Implementation: small wiring change inside `IngestSource` between Phase C's existing wire-in and the actual `UpdateExistingPagesFromSource` call. Test the bridge with a forced contradiction fixture — the existing page lands in the update pass even when its FTS score is zero. This task is intentionally small (one wiring change, two tests) so it can land on its own; doing it in Phase C would have entangled it with the larger plumbing change.

- **Phase G — Cassettes (Tasks 10–11).** Two new cassettes recorded once via `LLMWIKI_RECORD=1`, replayed deterministically in CI: `TestUpdateExistingHappyPath` (pre-seed five pages with valid evidence; ingest a source overlapping three of them; assert `pages_updated == 3`, two unchanged, `page_update_log` rows present), `TestUpdateExistingValidationDrop` (pre-seed a page with 5 valid quotes; ingest a source whose update proposal has 0 substring-matching quotes; assert `update_failed` outcome, page body unchanged on disk, `page_update_log` row with `outcome='failed'`). Same skip-when-no-cassette pattern as v0.5's Phase G. Recording target: Gemini Flash for the heavy fan-out; Anthropic Haiku acceptable for `TestUpdateExistingValidationDrop` if cheaper to pin (one call, deterministic prompt).

- **Phase H — Docs + tag (Tasks 12–13).** README adds a "Cross-page updates (opt-in)" subsection inside the existing Living Wiki section; opt-in framing is loud, cost paragraph points at Gemini Flash, validator-interaction caveat is explicit, debug-flag guidance, link to `page_update_log` for audit. CHANGELOG `[0.6.0-rc.1]` covers pillar 3, the contradiction upgrade, the schema migration, the default-off rationale, the no-down-migration note. Tag `v0.6.0-rc.1` locally — no push.

---

## Phase A — Schema migration to v4 + new queries

### Task 1: v4 migration adds `page_update_log` (additive, no `ALTER TABLE`)

**Files:**
- Modify: `internal/db/db.go`
- Modify: `internal/db/db_test.go`

- [ ] **Step 1: Write failing tests**

Append to `internal/db/db_test.go`:

1. `TestMigrate_FromFresh_LandsAtV4` — open a fresh DB at a temp path; query `PRAGMA user_version`; assert `4`. Query `SELECT name FROM sqlite_master WHERE type='table' AND name='page_update_log'`; assert one row.
2. `TestMigrate_FromV3_AddsPageUpdateLog` — open a DB, force `PRAGMA user_version = 3`, close; reopen via `db.Open`; assert `user_version` is now `4` and `page_update_log` exists with the expected columns (`id`, `page_id`, `source_id`, `prior_content_hash`, `new_content_hash`, `outcome`, `reason`, `evidence_added`, `evidence_removed`, `created_at`).
3. `TestMigrate_Idempotent_RerunningOnV4_IsNoop` — open at v4 twice; assert no error, `user_version` still 4, `page_update_log` still exists, no duplicate indexes.
4. `TestMigrate_DoesNotAlterPagesEvidenceSourcesSourceFilesChunks` — capture `sqlite_master` schema row for each pre-existing table on a v3 DB; reopen at v4; assert the schema rows are byte-identical (no surprise `ALTER TABLE`).
5. `TestMigrate_PageUpdateLogIndexesExist` — assert `idx_page_update_log_page` and `idx_page_update_log_source` are present in `sqlite_master`.

- [ ] **Step 2: Run tests and confirm failure**

Run: `go test ./internal/db/ -run TestMigrate -v`
Expected: FAIL — v4 block does not exist yet.

- [ ] **Step 3: Add the v4 migration block to `internal/db/db.go`**

Insert after the existing `if version < 3 { ... }` block, before the `PRAGMA foreign_keys = ON` line:

```go
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
```

- [ ] **Step 4: Run tests and confirm pass**

Run: `go test ./internal/db/ -run TestMigrate -v`
Expected: PASS — five subtests green.

Run: `go test ./...`
Expected: green (no callers of `page_update_log` yet).

- [ ] **Step 5: Commit**

```bash
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
feat(db): v4 migration — additive page_update_log table

Adds the audit trail for sub-project 6b's cross-page page-update
pass. One row per candidate per ingest, written even on `failed` and
`skipped` outcomes so users can run sqlite3 to debug. Roll-forward
only — no down-migration script. Idempotent (CREATE TABLE IF NOT
EXISTS). No ALTER TABLE on existing tables: pages, evidence,
sources, source_files, chunks are byte-identical pre/post v4 (Q8).

Two indexes (page_id, source_id) cover the queries Phase A's new
queries.go methods will issue. Never rotated, never truncated (Q9).

PRAGMA user_version bumps from 3 to 4. Pre-v4 wikis upgrade silently
on first open via db.Open's existing migration runner.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: `DeleteEvidenceForPage`, `InsertPageUpdateLog`, `GetPageUpdateLog`, `CountPageUpdateLogByOutcome`

**Files:**
- Modify: `internal/db/queries.go`
- Modify: `internal/db/queries_test.go`

- [ ] **Step 1: Write failing tests**

Append to `internal/db/queries_test.go`:

1. `TestDeleteEvidenceForPage_RemovesAllRowsForThatPage` — seed two pages with two evidence rows each; call `DeleteEvidenceForPage(page1.ID)`; assert the two rows for page1 are gone, the two rows for page2 are unchanged.
2. `TestDeleteEvidenceForPage_NoErrorOnEmptyPage` — call against a page with no evidence; assert no error, no row count change anywhere.
3. `TestDeleteEvidenceForPage_DeletesFTSRowsToo` — seed evidence; call delete; query `evidence_fts` directly; assert the AFTER DELETE trigger fired and the FTS row is gone (regression guard for the existing `evidence_ad` trigger).
4. `TestInsertPageUpdateLog_HappyPath` — `InsertPageUpdateLog(PageUpdateLogEntry{PageID: ..., SourceID: ..., PriorContentHash: "abc", NewContentHash: "def", Outcome: "updated", EvidenceAdded: 2, EvidenceRemoved: 1})`; assert one row exists with the expected fields and `created_at` is non-zero.
5. `TestInsertPageUpdateLog_NullableSourceID` — `SourceID = 0` (sentinel for "no source"); assert the row writes with `NULL` in `source_id`.
6. `TestInsertPageUpdateLog_NullableNewContentHash` — `Outcome = "failed"`, `NewContentHash = ""`; assert `new_content_hash IS NULL` on disk.
7. `TestInsertPageUpdateLog_RejectsUnknownOutcome` — `Outcome = "wat"`; assert error wrapping a sentinel `ErrInvalidOutcome` (validation in the query method, not in SQL — keeps SQLite portable). Valid outcomes: `updated`, `body_only`, `failed`, `skipped`.
8. `TestGetPageUpdateLog_ReturnsRowsOrderedByCreatedAtDesc` — seed three log rows for one page (sleep 1ms between each to force distinct timestamps); call `GetPageUpdateLog(pageID, 10)`; assert three rows in reverse chronological order.
9. `TestGetPageUpdateLog_RespectsLimit` — seed 10 rows; call with limit 3; assert exactly 3 rows.
10. `TestCountPageUpdateLogByOutcome_BucketsCorrectly` — seed rows with outcomes `updated`, `updated`, `failed`, `body_only`, `skipped`; call; assert the returned `map[string]int` has `{"updated": 2, "failed": 1, "body_only": 1, "skipped": 1}`.

- [ ] **Step 2: Run tests and confirm failure**

Run: `go test ./internal/db/ -run "TestDeleteEvidenceForPage|TestInsertPageUpdateLog|TestGetPageUpdateLog|TestCountPageUpdateLogByOutcome" -v`
Expected: FAIL — methods do not exist.

- [ ] **Step 3: Add the queries to `internal/db/queries.go`**

```go
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
    if e.SourceID != 0 { sourceID = e.SourceID }
    var newHash any
    if e.NewContentHash != "" { newHash = e.NewContentHash }
    var reason any
    if e.Reason != "" { reason = e.Reason }
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
    if err != nil { return nil, err }
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
    if err != nil { return nil, err }
    defer rows.Close()
    out := map[string]int{}
    for rows.Next() {
        var oc string; var n int
        if err := rows.Scan(&oc, &n); err != nil { return nil, err }
        out[oc] = n
    }
    return out, rows.Err()
}
```

- [ ] **Step 4: Run tests and confirm pass**

Run: `go test ./internal/db/ -run "TestDeleteEvidenceForPage|TestInsertPageUpdateLog|TestGetPageUpdateLog|TestCountPageUpdateLogByOutcome" -v`
Expected: PASS — ten subtests green.

Run: `go test ./...`
Expected: green.

- [ ] **Step 5: Commit**

```bash
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
feat(db): DeleteEvidenceForPage + InsertPageUpdateLog + GetPageUpdateLog + CountPageUpdateLogByOutcome

Four queries that back sub-project 6b's cross-page page-update pass:

  - DeleteEvidenceForPage(pageID): wipes a page's evidence rows so
    the update path can swap the row set atomically (delete-old +
    insert-new). Existing AFTER DELETE trigger mirrors the cleanup
    into evidence_fts.

  - InsertPageUpdateLog(entry): appends one audit-trail row on every
    outcome (updated / body_only / failed / skipped). Validates
    outcome against a whitelist (returns ErrInvalidOutcome on
    unknown values) — keeps SQLite portable without CHECK
    constraints.

  - GetPageUpdateLog(pageID, limit): newest-first per-page scan for
    --debug-updates and future lint integrations.

  - CountPageUpdateLogByOutcome(): GROUP BY outcome scan for
    cmd/status to surface pages_updated_total and
    pages_update_failed_total counters.

Every method is a pure additive over the v4 schema; no behaviour
change for existing call sites.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase B — `wiki.UpdateExistingPagesFromSource` core

### Task 3 (B1): Candidate selection + plumbing scaffold (no LLM body yet)

**Files:**
- Create: `internal/wiki/update_existing.go`
- Create: `internal/wiki/update_existing_test.go`

- [ ] **Step 1: Write failing tests**

Create `internal/wiki/update_existing_test.go`:

1. `TestUpdateExistingPagesFromSource_CandidateSelection_FTSShortlist` — seed five existing pages with distinct titles, evidence, and bodies; pass one new source file whose content shares keywords with two of them. Assert the (test-exposed) candidate list names exactly the two FTS-matching candidates.
2. `TestUpdateExistingPagesFromSource_CandidateSelection_RespectsPerSourceCap` — seed 30 existing pages that all match the new source file's keywords; pass `MaxCandidatesPerSource = 5`; assert only 5 candidates are walked.
3. `TestUpdateExistingPagesFromSource_CandidateSelection_RespectsGlobalCap` — pass three new source files, each FTS-matching 25 distinct pages; pass `MaxCandidatesTotal = 30` and `MaxCandidatesPerSource = 25`; assert the union is capped at 30.
4. `TestUpdateExistingPagesFromSource_CandidateSelection_DedupsAcrossSources` — two new source files both FTS-match the same existing page; assert that page appears only once in the candidate list.
5. `TestUpdateExistingPagesFromSource_CandidateSelection_ExcludesNewPageTitles` — `newPageTitles = ["Foo"]`; an existing page titled "Foo" exists (carry-over from a prior ingest); assert "Foo" is NOT a candidate.
6. `TestUpdateExistingPagesFromSource_CandidateSelection_HonoursForcedCandidateIDs` — pass an existing page that does NOT FTS-match any new source file; pass its page ID in `ForcedCandidateIDs`; assert it appears in the candidate list (the contradiction-on-ingest bridge from Phase F).
7. `TestUpdateExistingPagesFromSource_NoCandidates_ReturnsEmptyResult` — no candidates after FTS + dedup; assert `UpdateResult{}` with all-zero counters and no LLM calls.
8. `TestUpdateExistingPagesFromSource_DefaultsApplyWhenZero` — `UpdateExistingOptions{MaxCandidatesPerSource: 0, MaxCandidatesTotal: 0}` → fall back to the package defaults (20, 50).

For B1's tests, the LLM client is a stub that records calls but returns `{"pages": []}` (the "no change" path) — Task 4 (B2) exercises the real per-candidate update logic.

- [ ] **Step 2: Run tests and confirm failure**

Run: `go test ./internal/wiki/ -run TestUpdateExistingPagesFromSource_CandidateSelection -v`
Expected: FAIL — function does not exist.

- [ ] **Step 3: Implement `internal/wiki/update_existing.go` (scaffold)**

```go
package wiki

import (
    "context"
    "fmt"
    "io"
    "os"
    "sync"
    "time"

    "github.com/mritunjaysharma394/llmwiki/internal/db"
    "github.com/mritunjaysharma394/llmwiki/internal/ingest"
    "github.com/mritunjaysharma394/llmwiki/internal/llm"
)

// Sub-project 6b — pillar 3 — cross-page page-update pass.
//
// IMPORTANT — TRUST PROPERTY. This is the most validator-hostile change
// in the binary's history. Every code path that writes a page body
// reaches disk only via wiki.ValidateAndAttachEvidence. The validator
// can drop the proposed body — that's the design (Q11 / spec risk #1).
// Pages whose proposed body fails validation STAY AT THEIR PRIOR
// VERSION; we never silently downgrade. The page_update_log audit
// trail records every outcome (updated / body_only / failed /
// skipped) so the user can sqlite3-grep to debug.

const (
    defaultUpdateExistingMaxCandidatesPerSource = 20
    defaultUpdateExistingMaxCandidatesTotal     = 50
    defaultUpdateExistingQuoteFloor             = 2
)

type UpdateExistingOptions struct {
    MaxCandidatesPerSource int
    MaxCandidatesTotal     int
    QuoteFloor             int
    DebugUpdates           bool
    Logger                 io.Writer

    // ForcedCandidateIDs are page IDs that bypass the FTS shortlist
    // and the global cap; appended to the candidate list directly.
    // The contradiction-on-ingest bridge (Phase F) feeds these in
    // when --update-existing is on AND DetectIngestContradictions
    // returned non-empty: a contradiction is the strongest possible
    // signal that this page is touched by the new source.
    ForcedCandidateIDs []int64
}

type UpdateResult struct {
    Updated        []string
    BodyOnly       []string
    Failed         []UpdateFailure
    Skipped        []string
    // Mirror counts, for the cmd/MCP summary lines.
    PagesUpdated      int
    PagesUpdateFailed int
}

type UpdateFailure struct {
    Title         string
    Reason        string
    DroppedQuotes []DroppedQuote // reused from promote.go
}

// UpdateExistingPagesFromSource is the pillar 3 entrypoint. Called
// from IngestSource between the contradiction-detection pass and
// RegenerateIndex, gated by IngestOptions.UpdateExisting.
//
// newSourceFiles are the source files for the source just ingested;
// newPageTitles are the page titles written by that ingest in this
// run (excluded from candidate selection — they were just authored
// with the full title set already in mind).
func UpdateExistingPagesFromSource(
    ctx context.Context,
    cfg IngestSourceConfig,
    database *db.DB,
    client llm.Client,
    sourceID int64,
    newSourceFiles []ingest.SourceFile,
    newPageTitles []string,
    opts UpdateExistingOptions,
) (UpdateResult, error) {
    if opts.MaxCandidatesPerSource == 0 {
        opts.MaxCandidatesPerSource = defaultUpdateExistingMaxCandidatesPerSource
    }
    if opts.MaxCandidatesTotal == 0 {
        opts.MaxCandidatesTotal = defaultUpdateExistingMaxCandidatesTotal
    }
    if opts.QuoteFloor == 0 {
        opts.QuoteFloor = defaultUpdateExistingQuoteFloor
    }
    candidates, err := selectUpdateCandidates(database, newSourceFiles, newPageTitles, opts)
    if err != nil { return UpdateResult{}, err }
    if len(candidates) == 0 { return UpdateResult{}, nil }

    // Task 4 (B2) plugs the per-candidate LLM call + validator + audit
    // here. For B1, walk candidates with a no-op LLM and return zero.
    return UpdateResult{}, nil
}

// selectUpdateCandidates is the test-exposed shortlist builder.
// Per new source file, unions db.SearchPages and db.SearchEvidence
// hits (keyed by page ID); excludes pages whose title is in
// newPageTitles; appends opts.ForcedCandidateIDs; caps at
// MaxCandidatesTotal; preserves ForcedCandidateIDs even past the
// cap (forced > FTS-shortlisted).
func selectUpdateCandidates(
    database *db.DB,
    newSourceFiles []ingest.SourceFile,
    newPageTitles []string,
    opts UpdateExistingOptions,
) ([]db.PageRecord, error) { /* implementation */ }
```

The system prompt, the per-candidate update body, the validator pass, the quote floor, the content_hash skip, and the audit trail land in Task 4. B1 stops at the candidate selection so the test surface is clean.

- [ ] **Step 4: Run tests and confirm pass**

Run: `go test ./internal/wiki/ -run TestUpdateExistingPagesFromSource_CandidateSelection -v`
Expected: PASS — eight subtests green.

Run: `go test ./...`
Expected: green (no callers yet).

- [ ] **Step 5: Commit**

```bash
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
feat(wiki): UpdateExistingPagesFromSource scaffold — candidate selection

Phase B1 of sub-project 6b's pillar 3: the candidate-selection
shortlist builder, the UpdateExistingOptions / UpdateResult /
UpdateFailure types, and the entrypoint signature. Per new source
file, unions db.SearchPages + db.SearchEvidence hits (keyed by page
ID); excludes pages whose title is in newPageTitles; appends
ForcedCandidateIDs (the contradiction-on-ingest bridge will populate
these); caps at MaxCandidatesTotal (default 50, Q5).

Per-source cap is 20, global cap is 50 (Q5 spec defaults; tunable
via [ingest] update_existing_max_candidates_per_source and
update_existing_max_candidates_total config keys in Phase D). Quote
floor defaults to 2 (Q7); the min(2, len(originalEvidence)) form
lands in Phase B2 alongside the per-candidate LLM call.

The per-candidate update loop, validator pass, quote floor, and
audit trail land in Task 4 (B2). This commit is intentionally a
scaffold — the test surface is clean against synthetic FTS hits and
the LLM is a no-op stub.

TRUST PROPERTY (pre-emptive): Phase B2 will reaffirm that every
candidate's proposed body passes through ValidateAndAttachEvidence
before reaching disk. Pillar 3 is opt-in (--update-existing default
off, Q11) precisely because the validator may drop a proposed body;
when that happens we keep the prior version and log update_failed.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 4 (B2): Per-candidate update — LLM call + validator + quote floor + content_hash skip + audit trail

**Files:**
- Modify: `internal/wiki/update_existing.go`
- Modify: `internal/wiki/update_existing_test.go`

- [ ] **Step 1: Write failing tests**

Append to `internal/wiki/update_existing_test.go`:

1. `TestUpdateExistingPagesFromSource_HappyPath_OneCandidate` — pre-seed one existing page with 3 valid evidence rows and content_hash `H0`; pass a new source file whose content shares keywords. Stub the LLM to return one `{title: <existing>, body: <new>, evidence: [4 quotes, all matching either the new source file or the existing source file]}`. Assert: page body on disk replaced with the new body; 4 evidence rows in DB (old 3 deleted via `DeleteEvidenceForPage`, then `InsertEvidence` of new 4); page row's `content_hash` updated; `page_update_log` row with `outcome='updated'`, `prior_content_hash='H0'`, `new_content_hash=<new>`, `evidence_added=4`, `evidence_removed=3`, `source_id=<sourceID>`. Result: `Updated == [title]`, `PagesUpdated == 1`.
2. `TestUpdateExistingPagesFromSource_LLMSaysNoChange_LogsSkipped` — stub LLM returns `{"pages": []}`; assert no disk write, no evidence touched; `page_update_log` row with `outcome='skipped'`, `reason='llm-no-change'`.
3. `TestUpdateExistingPagesFromSource_ValidationDropsAllQuotes_KeepsPriorVersion` — stub LLM returns a body with 3 evidence quotes; mutate the new-source file content so 0 of the 3 substring-match either the new or existing sources. Assert: page body on disk UNCHANGED (byte-for-byte match against the prior body); evidence rows UNCHANGED (no `DeleteEvidenceForPage` fired); `page_update_log` row with `outcome='failed'`, `reason='zero-quotes-matched'`, `new_content_hash=NULL`; `UpdateFailure` populated with `DroppedQuotes` listing all 3 dropped quotes; `PagesUpdateFailed == 1`. **Trust property reaffirmed.**
4. `TestUpdateExistingPagesFromSource_QuoteFloor_OriginalHad5_NewHas1_KeptAtPrior` — pre-seed page with 5 valid quotes; LLM returns a body with 1 quote that *does* validate. Floor is `min(2, 5) = 2`. 1 < 2 → `update_failed` with `reason='below-quote-floor: 1/2'`. Page body unchanged.
5. `TestUpdateExistingPagesFromSource_QuoteFloor_OriginalHad1_NewHas1_Updated` — pre-seed page with 1 valid quote; LLM returns a body with 1 quote that validates. Floor is `min(2, 1) = 1`. 1 >= 1 → `outcome='updated'`. Page body replaced. **The "lets pages that originally had only 1 quote not get held to a higher bar than they started at" arm of Q7.**
6. `TestUpdateExistingPagesFromSource_ContentHashSkip_NoOpUpdate_LogsBodyOnly` — pre-seed page with body `B0`; LLM returns a body that hashes to the same `B0` (e.g. semantic no-op — same prose, maybe whitespace-different but `HashContent`-identical). Assert: no disk write of body; no `DeleteEvidenceForPage`; `page_update_log` row with `outcome='body_only'`, `reason='content_hash-unchanged'`. The "single-step oscillation guard" of Q11 / spec risk #3.
7. `TestUpdateExistingPagesFromSource_LLMError_LogsFailedAndContinues` — pre-seed two candidates; LLM errors on the first, succeeds on the second. Assert: candidate 1 has `page_update_log` row with `outcome='failed'`, `reason='llm-error: <msg>'`, prior body kept; candidate 2 succeeds normally. The pass does NOT abort on a per-candidate LLM error.
8. `TestUpdateExistingPagesFromSource_ConcurrencyCappedAt5` — pre-seed 10 candidates; instrument the stub LLM to record max-in-flight count; assert the count never exceeds `ingestMaxInflight = 5` (matching `IngestSource`'s chunk fan-out semaphore pattern).
9. `TestUpdateExistingPagesFromSource_AuditTrailRowOnEveryOutcome` — set up a mix of outcomes (1 updated, 1 failed, 1 body_only, 1 skipped); after the call assert exactly 4 `page_update_log` rows with the expected outcome distribution. Confirms the audit trail is written for *every* candidate, not just success.
10. `TestUpdateExistingPagesFromSource_DebugUpdates_LogsPerCandidateVerdicts` — pass `DebugUpdates: true` and a `bytes.Buffer` Logger; run a 3-candidate batch; assert the buffer contains one line per candidate naming the outcome and (if failed) the reason.
11. `TestUpdateExistingPagesFromSource_TrustProperty_EveryUpdatedPageHasGroundedEvidence` — happy path with 3 candidates; for each updated page on disk, re-read it via `ReadPage`, walk its evidence, and assert every quote substring-matches *some* file in the union of `(newSourceFiles + originalSourceFilesForThatPage)`. The validator is the single gatekeeper; this test pins that contract over the update path.

- [ ] **Step 2: Run tests and confirm failure**

Run: `go test ./internal/wiki/ -run TestUpdateExistingPagesFromSource_ -v`
Expected: FAIL — per-candidate body not yet implemented.

- [ ] **Step 3: Implement the per-candidate body**

Extend `UpdateExistingPagesFromSource` to walk `candidates` under a `sem := make(chan struct{}, ingestMaxInflight)` with a `sync.WaitGroup` and a `sync.Mutex`-guarded result accumulator. Per candidate:

```go
const updateExistingSystemPrompt = `You update an EXISTING wiki page in light of a NEW SOURCE.
Output a single page with the same title; the body should incorporate
information from NEW SOURCE that refines, qualifies, or extends the
existing page. Every evidence quote must verbatim-substring-match
either the NEW SOURCE files OR the existing page's already-validated
quotes (those are listed under EXISTING EVIDENCE). Do not invent
quotes. If NEW SOURCE does not actually update this page, respond
with {"pages": []} and we will keep the page unchanged.`

// Per candidate (under the semaphore):
//
// 1. Build the prompt: SOURCE = full bodies of newSourceFiles +
//    "=== EXISTING PAGE ===\n" + candidate title + body +
//    "=== EXISTING EVIDENCE ===\n" + each existing quote with
//    its source_file annotation.
//
// 2. Call client.CompleteStructured(ctx, updateExistingSystemPrompt,
//    user, writePagesTool). Reuse the same writePagesTool schema
//    IngestSourceFilesToPages uses — same {pages: [{title, body,
//    evidence: [{quote, source_file}]}]} shape.
//
// 3. ExtractPagesFromToolResult. If empty: log skipped row,
//    reason="llm-no-change", continue.
//
// 4. Build the union sourceFiles set: newSourceFiles + synthesized
//    ingest.SourceFile rows for every distinct source_file referenced
//    by candidate's existing evidence (read via wiki.readSourceFileContent
//    helper that already lives in promote.go).
//
// 5. validated, dropped := ValidateAndAttachEvidence(extracted, union).
//    --- TRUST GATE ---
//    If len(validated) == 0 OR validated[0] has 0 evidence:
//      log failed, reason="zero-quotes-matched", record DroppedQuotes
//      from `dropped`. KEEP PRIOR VERSION. Continue.
//
// 6. Quote floor: floor := min(opts.QuoteFloor, len(originalEvidence)).
//    If len(validated[0].Evidence) < floor:
//      log failed, reason=fmt.Sprintf("below-quote-floor: %d/%d",
//                                     len(validated[0].Evidence), floor).
//      KEEP PRIOR VERSION. Continue.
//
// 7. Compute newHash := HashContent(validated[0].Body).
//    If newHash == candidate.ContentHash:
//      log body_only, reason="content_hash-unchanged".
//      No disk write of body. Continue. (The single-step oscillation
//      guard of Q11 / spec risk #3.)
//
// 8. Disk + DB write — atomic-ish via DeleteEvidenceForPage + InsertEvidence:
//    a. WritePage(updatedPage, cfg.WikiDir).
//    b. database.UpsertPage(updatedPageRecord) — bumps content_hash + updated_at.
//    c. database.DeleteEvidenceForPage(candidate.ID).
//    d. database.InsertEvidence(candidate.ID, sourceID, validated[0].Evidence).
//    e. log updated, evidence_added=len(new), evidence_removed=len(old).
//
// 9. AppendLog(cfg.WikiDir, LogEntry{Kind: "update_existing", ...}).
```

Concurrency: `sem := make(chan struct{}, ingestMaxInflight)` exactly like `IngestSource`'s chunk fan-out. The result accumulator is `sync.Mutex`-guarded.

The candidate's existing source files are looked up via `database.GetEvidenceForPage(candidate.ID)` (or equivalent — confirm the existing query name during implementation; spec line 339 mentions both `DeleteEvidenceForSource` and `DeleteEvidenceForSourceFile` exist, and the v0.5 plan references `db.GetEvidenceForPage`). Each unique `source_file_id` is read via `wiki.readSourceFileContent` (already extracted from MCP handlers in v0.5's Task 2).

**TRUST PROPERTY REAFFIRMED.** Step 5 is the single trust gate. Every code path that writes an updated body to disk passes through it. Steps 6 (quote floor) and 7 (content_hash skip) are policy gates *on top of* the trust gate, not replacements for it. A page whose proposed body fails the trust gate stays at its prior version, period.

- [ ] **Step 4: Run tests and confirm pass**

Run: `go test ./internal/wiki/ -run TestUpdateExistingPagesFromSource_ -v`
Expected: PASS — eleven subtests green (eight from B1 + new B2 set).

Run: `go test ./...`
Expected: green.

- [ ] **Step 5: Commit**

```bash
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
feat(wiki): UpdateExistingPagesFromSource — per-candidate update + validator gate + audit trail

Phase B2 lights up the per-candidate update path: prompt build via
EXISTING PAGE + EXISTING EVIDENCE markers; one structured LLM call
per candidate against the same writePagesTool schema as
IngestSourceFilesToPages; ExtractPagesFromToolResult parses the
output; ValidateAndAttachEvidence over the union of (newSourceFiles
+ candidate's existing source files) is the single trust gate;
quote floor min(QuoteFloor, len(originalEvidence)) (default
QuoteFloor=2, Q7) blocks single-weak-quote replacements without
holding originally-1-quote pages to an artificially higher bar;
content_hash skip catches the single-step oscillation case (Q11 /
spec risk #3) and records body_only without a disk write of the
body.

On every outcome (updated / body_only / failed / skipped) we append
one row to page_update_log via db.InsertPageUpdateLog — the audit
trail is permanent (Q9) and lets the user sqlite3-grep to debug.
On `failed`, the page STAYS AT ITS PRIOR VERSION; we never silently
downgrade a previously-valid page.

Concurrency follows IngestSource's chunk fan-out shape: sem :=
make(chan struct{}, ingestMaxInflight) caps in-flight LLM calls at
5, sync.Mutex guards the result accumulator.

TRUST PROPERTY REAFFIRMED. Every updated page reaching disk has >=1
evidence quote that substring-matches some file in the (new + old)
union — ValidateAndAttachEvidence is the single gatekeeper, the
quote-floor and content_hash-skip are policy gates on top of it,
not replacements for it. Pillar 3 is opt-in
(--update-existing default off, Q11) precisely because the
validator may drop a proposed body; the cost of that conservatism
is `update_failed` outcomes and inert pages, never wrong-content
pages.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase C — Wire pillar 3 into `IngestSource`

### Task 5: Plug `UpdateExistingPagesFromSource` into the runner; extend `IngestRunResult`; second retro-link pass

**Files:**
- Modify: `internal/wiki/ingest_runner.go`
- Modify: `internal/wiki/ingest_runner_test.go`

- [ ] **Step 1: Write failing tests**

Append to `internal/wiki/ingest_runner_test.go`:

1. `TestIngestSource_UpdateExistingFlagOff_DoesNotCallUpdate` — instrument a wrapper around `UpdateExistingPagesFromSource` (via a package-level seam) that records calls; run `IngestSource(opts: IngestOptions{UpdateExisting: false})`; assert the seam is never invoked.
2. `TestIngestSource_UpdateExistingFlagOn_CallsUpdateBetweenContradictionsAndIndex` — same seam; assert the call fires AFTER `DetectIngestContradictions` and BEFORE `RegenerateIndex` (use call-order recording).
3. `TestIngestSource_UpdateExistingPropagatesIntoIngestRunResult` — synthetic 2-candidate update batch (1 updated, 1 failed); assert `IngestRunResult.PagesUpdated == 1`, `PagesUpdateFailed == 1`, `UpdatedTitles == ["..."]`, `UpdateFailures` has one entry.
4. `TestIngestSource_UpdateExistingTunablesPropagated` — pass `IngestOptions{UpdateExistingMaxCandidatesPerSource: 3, UpdateExistingMaxCandidatesTotal: 7, UpdateExistingQuoteFloor: 4}`; assert the `UpdateExistingOptions` reaching the seam carries those values.
5. `TestIngestSource_UpdateExistingSecondRetroLinkPass_SeesUpdatedBodies` — pre-seed an existing page A whose body mentions a *future* updated title (i.e. a new title that the cross-page update will inject into page B's body via a wikilink); after `IngestSource` with `UpdateExisting: true`, assert page A's body now contains `[[<NewTitle>]]` if the updated B body's content reasonably triggers it. (Spec line 448: ordering — retro-link must run AFTER the update pass so retro-link sees the updated bodies.)
6. `TestIngestSource_UpdateExistingPassesSourceID` — assert the `sourceID` parameter to `UpdateExistingPagesFromSource` matches the source row created earlier in `IngestSource` (so `page_update_log.source_id` is populated correctly).
7. `TestIngestSource_UpdateExistingLogsSummaryLines` — happy path with 2 updated, 1 failed; capture `IngestOptions.Logger`'s output; assert it contains the spec's Flow 4 summary shape: `"N page(s) updated:"`, `"~ <Title> (+M evidence)"` lines, `"K page(s) update FAILED"`, `"✗ <Title>"` lines with reason annotation.

- [ ] **Step 2: Run tests and confirm failure**

Run: `go test ./internal/wiki/ -run TestIngestSource_UpdateExisting -v`
Expected: FAIL — wire-in not yet implemented.

- [ ] **Step 3: Extend `IngestOptions` and `IngestRunResult`**

In `internal/wiki/ingest_runner.go`:

```go
type IngestOptions struct {
    Force        bool
    NoRechunk    bool
    Feed         bool
    Sitemap      bool
    MaxPages     int
    Include      []string
    Exclude      []string
    NoGitignore  bool
    MaxFileBytes int64

    // Sub-project 6b — pillar 3 — cross-page page-update pass.
    UpdateExisting                       bool
    DebugUpdates                         bool
    UpdateExistingMaxCandidatesPerSource int
    UpdateExistingMaxCandidatesTotal     int
    UpdateExistingQuoteFloor             int

    Logger io.Writer
}

type IngestRunResult struct {
    Source                string
    PagesWritten          int
    EvidenceQuotes        int
    DroppedPages          int
    Skipped               bool
    RetroLinkedPages      int
    RetroLinkedTitles     []string
    ContradictionsFlagged int
    // Sub-project 6b additions:
    PagesUpdated      int
    PagesUpdateFailed int
    UpdatedTitles     []string
    UpdateFailures    []UpdateFailure
}
```

- [ ] **Step 4: Wire the call site**

In `IngestSource`, after the existing Phase E contradiction-detection block (the `existingPageRecs, _ := database.AllPages()` ... `AppendContradictions(...)` chunk near line 457–481) and BEFORE the `allPageRecs, err := database.AllPages()` block that feeds `RegenerateIndex` (line 483), insert:

```go
// Phase C (sub-project 6b, v0.6): cross-page page-update pass.
// Gated by opts.UpdateExisting. Order matters:
//   (1) write new pages — ABOVE
//   (2) retro-link existing pages to new titles — ABOVE (Phase D of 6a)
//   (3) detect contradictions — ABOVE (Phase E of 6a)
//   (4) UPDATE EXISTING PAGES — HERE (new in 6b)
//   (5) re-run retro-link over (new + updated) titles — BELOW
//   (6) regenerate index — BELOW
//
// The trust property holds at this call: UpdateExistingPagesFromSource
// runs ValidateAndAttachEvidence over every proposed body before any
// disk write; pages whose proposed body fails validation stay at
// their prior version (we never silently downgrade).
if opts.UpdateExisting {
    // Forced candidates: any existing page that surfaced as a
    // contradiction is auto-promoted into the candidate pool, even
    // if its FTS score against the new source is zero. (Phase F's
    // contradiction → update bridge — spec line 60.)
    forcedIDs := forcedCandidatesFromContradictions(database, contras)
    upRes, upErr := UpdateExistingPagesFromSource(ctx, cfg, database, client, sourceID,
        sourceFiles, newTitles, UpdateExistingOptions{
            MaxCandidatesPerSource: opts.UpdateExistingMaxCandidatesPerSource,
            MaxCandidatesTotal:     opts.UpdateExistingMaxCandidatesTotal,
            QuoteFloor:             opts.UpdateExistingQuoteFloor,
            DebugUpdates:           opts.DebugUpdates,
            Logger:                 opts.Logger,
            ForcedCandidateIDs:     forcedIDs,
        })
    if upErr != nil {
        fmt.Fprintf(os.Stderr, "  WARN cross-page update pass: %v\n", upErr)
    }
    out.PagesUpdated = upRes.PagesUpdated
    out.PagesUpdateFailed = upRes.PagesUpdateFailed
    out.UpdatedTitles = upRes.Updated
    out.UpdateFailures = upRes.Failed
    if upRes.PagesUpdated > 0 {
        logf("\n%d page(s) updated:\n", upRes.PagesUpdated)
        for _, t := range upRes.Updated { logf("  ~ %s\n", t) }
    }
    if upRes.PagesUpdateFailed > 0 {
        logf("\n%d page(s) update FAILED — kept at previous version:\n", upRes.PagesUpdateFailed)
        for _, f := range upRes.Failed {
            logf("  ✗ %s\n      %s\n", f.Title, f.Reason)
        }
    }
    // (5) Second retro-link pass scoped to updated titles, so
    // newly-rewritten bodies pick up [[wikilinks]] back to the
    // titles introduced this batch and the existing pages that
    // were just rewritten get linked to the same set. The pass
    // is idempotent — pages already containing the link no-op.
    if len(upRes.Updated) > 0 {
        _, _ = RetroLinkPages(database, cfg.WikiDir, append(append([]string{}, newTitles...), upRes.Updated...))
    }
}
// forcedCandidatesFromContradictions is a small helper: walks
// contras for ExistingTitle, looks up each via database.GetPage,
// returns the deduped page IDs. Phase F covers the test surface.
```

The `forcedCandidatesFromContradictions` helper is a stub in this task (returns nil); Phase F (Task 9) wires the contradiction → update bridge fully and adds the dedicated test.

- [ ] **Step 5: Run tests and confirm pass**

Run: `go test ./internal/wiki/ -run TestIngestSource_UpdateExisting -v`
Expected: PASS — seven subtests green.

Run: `go test ./...`
Expected: green.

- [ ] **Step 6: Commit**

```bash
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
feat(wiki): wire UpdateExistingPagesFromSource into IngestSource

Inserts the cross-page page-update pass between the contradiction-
detection block (v0.5 Phase E) and RegenerateIndex. Gated by
opts.UpdateExisting (default off, Q11). On success, summary lines
match spec Flow 4: "N page(s) updated:" with per-page "~ Title"
list, "K page(s) update FAILED — kept at previous version:" with
per-page "✗ Title" + reason. A second retro-link pass runs over
(new + updated) titles after the update pass so the retro-linker
sees the updated bodies and the existing pages that were just
rewritten get linked back to titles introduced this batch (spec
line 448 ordering rationale).

IngestOptions gains UpdateExisting / DebugUpdates / three tunables
(per-source cap, global cap, quote floor); IngestRunResult gains
PagesUpdated, PagesUpdateFailed, UpdatedTitles, UpdateFailures —
the cmd/MCP boundaries (Phases D, E) read these directly.

forcedCandidatesFromContradictions is a stub here (returns nil);
Phase F (Task 9) wires the bridge and lights up the dedicated
test surface.

TRUST PROPERTY REAFFIRMED. The update pass runs after the
validator-gated persist loop, so a failed update never revokes
a trust-validated write. Pages whose proposed body fails the
update validator stay at their prior version; we never silently
downgrade.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase D — Config + CLI flags + status counters

### Task 6: `IngestConfig` gains `update_existing` + tunables; `applyIngestDefaults` fills them

**Files:**
- Modify: `cmd/root.go`
- Modify: `cmd/root_test.go`

- [ ] **Step 1: Write failing tests**

Append to `cmd/root_test.go`:

1. `TestApplyIngestDefaults_UpdateExistingDefaultsFalse` — `IngestConfig{}` → `applyIngestDefaults` → assert `*c.UpdateExisting == false` (the `*bool` "absent or explicit" pattern, mirroring `RespectGitignore`). Default-off is the contract (Q11).
2. `TestApplyIngestDefaults_UpdateExistingHonoursExplicitTrue` — `IngestConfig{UpdateExisting: ptr(true)}` → defaults → assert pointer still `true` (no clobbering).
3. `TestApplyIngestDefaults_UpdateExistingHonoursExplicitFalse` — `IngestConfig{UpdateExisting: ptr(false)}` → defaults → assert still `false`.
4. `TestApplyIngestDefaults_TunablesDefaults` — fresh `IngestConfig{}` → defaults → assert `MaxCandidatesPerSource == 20`, `MaxCandidatesTotal == 50`, `QuoteFloor == 2` (Q5/Q7).
5. `TestApplyIngestDefaults_TunablesHonourExplicit` — set each to a non-default; assert defaults don't clobber.
6. `TestLoadConfig_DecodesUpdateExistingTOML` — write `[ingest]\nupdate_existing = true\nupdate_existing_max_candidates_per_source = 7\n` to a tmp config; load; assert decoded values.

- [ ] **Step 2: Run tests and confirm failure**

Run: `go test ./cmd/ -run "TestApplyIngestDefaults|TestLoadConfig_DecodesUpdateExisting" -v`
Expected: FAIL — fields not yet on `IngestConfig`.

- [ ] **Step 3: Extend `IngestConfig` and `applyIngestDefaults`**

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
    RespectGitignore    *bool    `toml:"respect_gitignore"`

    // Sub-project 4 launch surface.
    FeedRequestsPerSecond float64 `toml:"feed_request_per_second"`
    FeedMaxEntries        int     `toml:"feed_max_entries"`
    SitemapMaxPages       int     `toml:"sitemap_max_pages"`

    // Sub-project 6b — pillar 3 — cross-page page-update pass (v0.6).
    // UpdateExisting is *bool to disambiguate "absent" (-> default
    // false, Q11) from "explicitly false" — same shape RespectGitignore
    // uses. Three tunables for per-source / global candidate caps and
    // the quote floor (Q5, Q7). Default off everywhere; users opt in.
    UpdateExisting                       *bool `toml:"update_existing"`
    UpdateExistingMaxCandidatesPerSource int   `toml:"update_existing_max_candidates_per_source"`
    UpdateExistingMaxCandidatesTotal     int   `toml:"update_existing_max_candidates_total"`
    UpdateExistingQuoteFloor             int   `toml:"update_existing_quote_floor"`
}

// UpdateExistingOrDefault returns the configured value, defaulting to
// false when the config left it unset. Mirrors RespectGitignoreOrDefault.
func (c IngestConfig) UpdateExistingOrDefault() bool {
    if c.UpdateExisting == nil { return false }
    return *c.UpdateExisting
}
```

In `applyIngestDefaults`:

```go
if c.UpdateExisting == nil {
    f := false
    c.UpdateExisting = &f
}
if c.UpdateExistingMaxCandidatesPerSource == 0 {
    c.UpdateExistingMaxCandidatesPerSource = 20
}
if c.UpdateExistingMaxCandidatesTotal == 0 {
    c.UpdateExistingMaxCandidatesTotal = 50
}
if c.UpdateExistingQuoteFloor == 0 {
    c.UpdateExistingQuoteFloor = 2
}
```

- [ ] **Step 4: Run tests and confirm pass**

Run: `go test ./cmd/ -run "TestApplyIngestDefaults|TestLoadConfig_DecodesUpdateExisting" -v`
Expected: PASS — six subtests green.

Run: `go test ./...`
Expected: green.

- [ ] **Step 5: Commit**

```bash
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
feat(cmd): [ingest] update_existing config key + three tunables

IngestConfig gains UpdateExisting (*bool, defaults to false per Q11
— same "absent vs explicit" shape RespectGitignore uses) and three
integer tunables: update_existing_max_candidates_per_source = 20
(Q5), update_existing_max_candidates_total = 50 (Q5),
update_existing_quote_floor = 2 (Q7). applyIngestDefaults fills
them when absent. Pre-v0.6 configs without these keys decode to
zero and pick up the defaults silently — backwards-compatible.

Default-off everywhere is the v0.6 contract. CHANGELOG note will
say "consider flipping the default in v0.7 once we have real-world
numbers from opt-in users" (Q11).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 7: `--update-existing` and `--debug-updates` flags + `cmd/status` counters

**Files:**
- Modify: `cmd/ingest.go`
- Modify: `cmd/ingest_test.go`
- Modify: `cmd/status.go`
- Modify: `cmd/status_test.go` (create if absent)

- [ ] **Step 1: Write failing tests**

Append to `cmd/ingest_test.go`:

1. `TestIngest_UpdateExistingFlagDefaultsOff` — no flag, no config; assert the `IngestOptions` reaching `wiki.IngestSource` has `UpdateExisting == false`.
2. `TestIngest_UpdateExistingFlagOverridesConfigOff` — config has `update_existing = false`; CLI passes `--update-existing`; assert `IngestOptions.UpdateExisting == true` (CLI wins).
3. `TestIngest_UpdateExistingFlagOverridesConfigOn` — config has `update_existing = true`; CLI passes `--update-existing=false`; assert `IngestOptions.UpdateExisting == false`.
4. `TestIngest_UpdateExistingFlagFromConfig` — config has `update_existing = true`; no CLI flag; assert `IngestOptions.UpdateExisting == true`.
5. `TestIngest_DebugUpdatesFlag` — `--debug-updates`; assert `IngestOptions.DebugUpdates == true`.
6. `TestIngest_TunablesPropagateFromConfig` — config has all three tunables non-default; assert they reach `IngestOptions` unchanged.

Append to `cmd/status_test.go` (or create the file — match existing `cmd/*_test.go` shape):

7. `TestStatus_OmitsPageUpdateLogLinesWhenZero` — fresh DB, no `page_update_log` rows; `runStatus` output does NOT contain `pages updated total` (or similar) lines.
8. `TestStatus_ShowsPagesUpdatedTotalWhenNonZero` — seed 3 `page_update_log` rows with `outcome='updated'`; `runStatus` output contains `pages updated total:    3`.
9. `TestStatus_ShowsPagesUpdateFailedTotalWhenNonZero` — seed 1 with `outcome='failed'`; `runStatus` output contains `pages update failed:    1`.
10. `TestStatus_BodyOnlyAndSkippedNotInTotal` — seed mix (1 updated, 1 body_only, 1 failed, 1 skipped); assert `pages updated total: 1` (body_only and skipped excluded — they're not "updates" in the user-facing sense; spec line 191 specifies `pages_updated_total` as updated outcome only).

- [ ] **Step 2: Run tests and confirm failure**

Run: `go test ./cmd/ -run "TestIngest_UpdateExisting|TestIngest_DebugUpdates|TestIngest_Tunables|TestStatus_" -v`
Expected: FAIL.

- [ ] **Step 3: Wire the flags in `cmd/ingest.go`**

In `cmd/ingest.go`'s `init()`:

```go
ingestCmd.Flags().Bool("update-existing", false,
    "after writing new pages, propose updates to existing pages whose claims this source touches; off by default. Spec risk #1: the validator can drop previously-valid pages if the new source's quotes don't match — pages that fail validation stay at their previous version.")
ingestCmd.Flags().Bool("debug-updates", false,
    "print per-candidate verdicts from --update-existing to stderr (LLM proposed body, validator kept N quotes, content_hash drift); useful when an update_failed line appears in the summary.")
```

In `runIngest`, where `IngestOptions` is constructed:

```go
opts := wiki.IngestOptions{
    Force:    forceFlag(cmd),
    Feed:     /* existing */,
    Sitemap:  /* existing */,
    MaxPages: /* existing */,
    Include:  /* existing */,
    Exclude:  /* existing */,
    NoGitignore: /* existing */,

    // Sub-project 6b: cross-page page-update pass.
    UpdateExisting: resolveUpdateExisting(cmd, cfg),
    DebugUpdates:   getBoolFlag(cmd, "debug-updates"),
    UpdateExistingMaxCandidatesPerSource: cfg.Ingest.UpdateExistingMaxCandidatesPerSource,
    UpdateExistingMaxCandidatesTotal:     cfg.Ingest.UpdateExistingMaxCandidatesTotal,
    UpdateExistingQuoteFloor:             cfg.Ingest.UpdateExistingQuoteFloor,

    Logger: os.Stdout,
}

// resolveUpdateExisting layers package default → [ingest] config → CLI
// flag, CLI wins when explicitly set. Mirrors the existing
// resolveRespectGitignore-style helpers.
func resolveUpdateExisting(cmd *cobra.Command, c *Config) bool {
    if cmd.Flags().Changed("update-existing") {
        v, _ := cmd.Flags().GetBool("update-existing")
        return v
    }
    if c != nil { return c.Ingest.UpdateExistingOrDefault() }
    return false
}
```

- [ ] **Step 4: Wire `cmd/status.go`**

In `cmd/status.go`'s `runStatus`, after the existing `last ingest` line:

```go
counts, err := database.CountPageUpdateLogByOutcome()
if err != nil {
    fmt.Fprintf(os.Stderr, "  WARN reading page_update_log: %v\n", err)
} else {
    if updated := counts["updated"]; updated > 0 {
        fmt.Printf("pages updated total:%s%d\n", strings.Repeat(" ", widthGapAt("pages updated total:")), updated)
    }
    if failed := counts["failed"]; failed > 0 {
        fmt.Printf("pages update failed:%s%d\n", strings.Repeat(" ", widthGapAt("pages update failed:")), failed)
    }
}
```

(Use the existing `cmd/status.go` formatting idiom — match its column-aligned `pages:`, `sources:`, `evidence quotes:` style. The exact gap-computation helper is implementer's choice; tests pin the output text, not the spacing.)

- [ ] **Step 5: Run tests and confirm pass**

Run: `go test ./cmd/ -run "TestIngest_UpdateExisting|TestIngest_DebugUpdates|TestIngest_Tunables|TestStatus_" -v`
Expected: PASS — ten subtests green.

Run: `go test ./...`
Expected: green.

- [ ] **Step 6: Commit**

```bash
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
feat(cmd): --update-existing + --debug-updates flags + status counters

cmd/ingest gains two flags: --update-existing (default off, Q11)
opts into the cross-page page-update pass; --debug-updates prints
per-candidate verdicts to stderr for diagnosing update_failed lines.
Flag precedence is the existing layered shape: package defaults →
[ingest] config → CLI flag, CLI wins when explicitly set
(cmd.Flags().Changed handles the disambiguation).

cmd/status reads counts from page_update_log via the new
db.CountPageUpdateLogByOutcome query and surfaces pages_updated_total
and pages_update_failed_total counters when non-zero. Pure read; no
schema migration of pages_total / evidence_quotes etc. The body_only
and skipped outcomes are excluded from these totals — they're not
"updates" in the user-facing sense (spec line 191).

Default-off everywhere — a fresh wiki, no flags, no config sees
zero behaviour change vs v0.5.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase E — MCP surface

### Task 8: `mcp.ingest` accepts `update_existing`; return shape extended; `serverVersion` bump to `0.6.0-rc.1`

**Files:**
- Modify: `internal/mcp/server.go`
- Modify: `internal/mcp/handlers.go`
- Modify: `internal/mcp/server_test.go`

- [ ] **Step 1: Write failing tests**

Append to `internal/mcp/server_test.go`:

1. `TestServerVersionIs060` — assert `serverVersion == "0.6.0-rc.1"`. Pinned constant.
2. `TestIngest_AcceptsUpdateExistingArg` — call `mcp.ingest({source: "...", update_existing: true})`; instrument the seam to `wiki.IngestSource`; assert the propagated `IngestOptions.UpdateExisting == true`.
3. `TestIngest_DefaultsUpdateExistingOff` — `mcp.ingest({source: "..."})` with no `update_existing` arg; assert propagated `IngestOptions.UpdateExisting == false`.
4. `TestIngest_ReturnShapeIncludesPagesUpdated` — drive `mcp.ingest` through a synthetic happy-path with 2 updated, 1 failed; assert response payload contains `pages_updated: 2` and `pages_update_failed: 1` keys (alongside v0.5's `retro_linked_pages` and `contradictions_flagged`).
5. `TestIngest_ReturnShapePreservesV05Keys` — assert all v0.5 keys (`source`, `pages_written`, `evidence_quotes`, `dropped_pages`, `skipped`, `retro_linked_pages`, `contradictions_flagged`) are still present and unchanged in semantics. Backwards-compat guard.
6. `TestIngestTool_DescriptionMentionsUpdateExisting` — list the `ingest` tool's schema; assert the `update_existing` argument is described and marked optional.

- [ ] **Step 2: Run tests and confirm failure**

Run: `go test ./internal/mcp/ -run "TestServerVersionIs060|TestIngest_AcceptsUpdateExisting|TestIngest_DefaultsUpdateExisting|TestIngest_ReturnShape|TestIngestTool_Description" -v`
Expected: FAIL — version mismatch + new arg not wired.

- [ ] **Step 3: Bump `serverVersion` and extend `ingestTool()`**

In `internal/mcp/server.go`:

```go
const (
    serverName    = "llmwiki"
    serverVersion = "0.6.0-rc.1" // bumped from "0.5.0-rc.1" for sub-project 6b (cross-page page-update pass)
)
```

Find `ingestTool()` and add the new optional boolean arg (mirror the existing `force`/`feed`/`sitemap` boolean shape):

```go
func ingestTool() mcpgo.Tool {
    return mcpgo.NewTool(
        "ingest",
        mcpgo.WithDescription("..."),
        mcpgo.WithString("source", mcpgo.Required(), mcpgo.Description("...")),
        mcpgo.WithBoolean("force", mcpgo.Description("...")),
        mcpgo.WithBoolean("feed", mcpgo.Description("...")),
        mcpgo.WithBoolean("sitemap", mcpgo.Description("...")),
        mcpgo.WithNumber("max_pages", mcpgo.Description("...")),
        // Sub-project 6b (v0.6): cross-page page-update pass.
        mcpgo.WithBoolean("update_existing",
            mcpgo.Description("after writing new pages, propose updates to existing pages whose claims this source touches; off by default. Pages whose proposed body fails byte-exact substring-match validation against the union of (new + existing) source files stay at their previous version. Returns pages_updated and pages_update_failed counters in the response.")),
    )
}
```

- [ ] **Step 4: Wire the arg + return shape in `ingestHandler`**

In `internal/mcp/handlers.go`'s `ingestHandler`:

```go
opts := wiki.IngestOptions{
    Force:    req.GetBool("force", false),
    Feed:     req.GetBool("feed", false),
    Sitemap:  req.GetBool("sitemap", false),
    // Sub-project 6b: opt-in cross-page page-update pass over MCP.
    UpdateExisting: req.GetBool("update_existing", false),
    // The MCP boundary doesn't expose --debug-updates or the
    // tunables; an MCP client that wants those should set them
    // in [ingest] config.
}
if mp := req.GetInt("max_pages", 0); mp > 0 { opts.MaxPages = mp }
// ... existing wcfg + IngestSource call ...
return jsonResult(map[string]any{
    "source":                 res.Source,
    "pages_written":          res.PagesWritten,
    "evidence_quotes":        res.EvidenceQuotes,
    "dropped_pages":          res.DroppedPages,
    "skipped":                res.Skipped,
    "retro_linked_pages":     res.RetroLinkedPages,
    "contradictions_flagged": res.ContradictionsFlagged,
    // Sub-project 6b additions:
    "pages_updated":        res.PagesUpdated,
    "pages_update_failed":  res.PagesUpdateFailed,
})
```

Update the `ingestHandler` doc-comment return-shape block to include the two new keys with one-paragraph explanation:

```go
// Return JSON shape on success (sub-project 6b / v0.6.0-rc.1):
//
//   {
//     "source":                 string,
//     "pages_written":          int,
//     "evidence_quotes":        int,
//     "dropped_pages":          int,
//     "skipped":                bool,
//     "retro_linked_pages":     int,    // sub-project 6a Phase D
//     "contradictions_flagged": int,    // sub-project 6a Phase E
//     "pages_updated":          int,    // sub-project 6b — count of
//                                       //   existing pages whose body
//                                       //   was rewritten and re-validated
//     "pages_update_failed":    int,    // sub-project 6b — count of
//                                       //   candidates whose proposed body
//                                       //   failed validation; those pages
//                                       //   stay at their previous version
//   }
//
// pages_updated and pages_update_failed are non-zero only when the
// caller passed update_existing: true (default false, Q11). The
// trust property holds: every page with pages_updated++ has >=1
// evidence quote that substring-matches some file in the union of
// (this source + that page's prior sources); pages with
// pages_update_failed++ are byte-identical on disk to their prior
// version — we never silently downgrade.
```

- [ ] **Step 5: Run tests and confirm pass**

Run: `go test ./internal/mcp/ -run "TestServerVersionIs060|TestIngest_" -v`
Expected: PASS — six subtests green.

Run: `go test ./...`
Expected: green.

- [ ] **Step 6: Commit**

```bash
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
feat(mcp): mcp.ingest accepts update_existing + return shape adds pages_updated, pages_update_failed

mcp.ingest gains an optional update_existing: bool argument
(default false, Q11), and the response shape gains two integer
keys: pages_updated and pages_update_failed. v0.5's keys
(retro_linked_pages, contradictions_flagged) and v0.4's keys are
unchanged — backwards-compatible at the JSON-RPC layer (clients
ignoring unknown keys keep working; clients that destructured the
prior keys keep working).

No new MCP tool (Q10): the agent gets the full ingest+update result
in one round-trip, simpler surface, fewer concepts. A future
mcp.update_pages_from_source standalone tool stays open as a v0.7+
question if real users need to fire pillar 3 without an ingest.

serverVersion bumps from "0.5.0-rc.1" to "0.6.0-rc.1".

TRUST PROPERTY REAFFIRMED OVER MCP. Pages with pages_updated++ have
>=1 validated evidence quote substring-matching some file in the
(new + existing) union; pages with pages_update_failed++ are
byte-identical on disk to their prior version.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase F — Contradiction → update bridge

### Task 9: Forced candidates from contradictions

**Files:**
- Modify: `internal/wiki/ingest_runner.go`
- Modify: `internal/wiki/ingest_runner_test.go`

- [ ] **Step 1: Write failing tests**

Append to `internal/wiki/ingest_runner_test.go`:

1. `TestIngestSource_ContradictionForcesCandidate_WhenUpdateExistingOn` — pre-seed an existing page P that does NOT FTS-match the new source's content (zero overlap by keywords); have `DetectIngestContradictions` return one `Contradiction{ExistingTitle: P.Title, ...}`; run `IngestSource(opts: IngestOptions{UpdateExisting: true})`. Assert: P appears in `UpdateExistingOptions.ForcedCandidateIDs` reaching `UpdateExistingPagesFromSource`. Without the bridge, P would never be a candidate (FTS misses it); with the bridge, the contradiction promotes it.
2. `TestIngestSource_ContradictionDoesNOTForceCandidate_WhenUpdateExistingOff` — same setup, but `UpdateExisting: false`. Assert: `UpdateExistingPagesFromSource` is never called (gate from Phase C). The contradiction surface still fires (warn-only), but no forced candidate is computed because there's nowhere to feed it. The contradiction warn-only behaviour from v0.5 is unchanged.
3. `TestIngestSource_ContradictionForcedCandidate_BypassesGlobalCap` — set `MaxCandidatesTotal: 0` (effectively no FTS shortlist); assert the contradiction-forced candidate still reaches the candidate list. Forced > capped (spec rationale: a contradiction is the strongest possible signal).
4. `TestIngestSource_ContradictionForcedCandidate_DedupesWithFTSHit` — pre-seed an existing page that BOTH FTS-matches AND is contradicted; assert it appears exactly once in the candidate list (no double-walk).
5. `TestIngestSource_NoContradiction_NoForcedCandidates` — `DetectIngestContradictions` returns empty; assert `ForcedCandidateIDs` is `nil`/empty.

- [ ] **Step 2: Run tests and confirm failure**

Run: `go test ./internal/wiki/ -run TestIngestSource_Contradiction -v`
Expected: FAIL — `forcedCandidatesFromContradictions` is a stub returning nil.

- [ ] **Step 3: Implement `forcedCandidatesFromContradictions`**

In `internal/wiki/ingest_runner.go`:

```go
// forcedCandidatesFromContradictions walks the contradiction list
// from DetectIngestContradictions and returns the deduped page IDs
// of every existing page that surfaced as a contradiction. Used by
// Phase C's pillar-3 wire-in: when --update-existing is on AND
// contradictions were detected, those existing pages bypass the
// FTS shortlist + global cap and become forced candidates for the
// update pass.
//
// Rationale (spec line 60): "[contradiction-on-ingest] upgraded to
// 'edit existing page' once 6b lands". A contradiction is the
// strongest possible signal that a new source touches an existing
// page; if the FTS shortlist somehow missed it (rare but possible
// with terse claims that don't share keywords), the contradiction
// surface is the safety net.
func forcedCandidatesFromContradictions(database *db.DB, contras []Contradiction) []int64 {
    if len(contras) == 0 { return nil }
    seen := map[int64]bool{}
    var out []int64
    for _, c := range contras {
        rec, err := database.GetPage(c.ExistingTitle)
        if err != nil || rec == nil || seen[rec.ID] { continue }
        seen[rec.ID] = true
        out = append(out, rec.ID)
    }
    return out
}
```

The Phase C call site already uses this helper (`forcedIDs := forcedCandidatesFromContradictions(database, contras)`); Task 9 just lights up the body.

`selectUpdateCandidates` in `update_existing.go` must also confirm it preserves `ForcedCandidateIDs` past the global cap — if the test surface in Task 3 didn't already pin this, add that assertion now and adjust the implementation accordingly.

- [ ] **Step 4: Run tests and confirm pass**

Run: `go test ./internal/wiki/ -run TestIngestSource_Contradiction -v`
Expected: PASS — five subtests green.

Run: `go test ./...`
Expected: green.

- [ ] **Step 5: Commit**

```bash
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
feat(wiki): contradiction → update bridge — contradicted pages become forced candidates

Spec line 60 — when --update-existing is on AND
DetectIngestContradictions returned non-empty, every existing page
that surfaced as Contradiction.ExistingTitle becomes a forced
candidate for the cross-page page-update pass: bypasses the FTS
shortlist + global cap, gets walked alongside FTS-shortlisted
candidates. Rationale: a contradiction is the strongest possible
signal that a new source touches an existing page; if FTS missed
it (rare but possible with terse claims), the contradiction
surface is the safety net.

When --update-existing is OFF, this bridge does nothing — the
contradiction warn-only behaviour from v0.5 is unchanged. The
upgrade is opt-in alongside pillar 3 itself.

The bridge is small (one helper, one call-site wire-in already
stubbed in Phase C) but landed as its own task so the test
surface is clean and the contradiction → update semantics have
their own commit boundary in the git log.

TRUST PROPERTY UNCHANGED. Forced candidates flow through the same
ValidateAndAttachEvidence gate as FTS-shortlisted candidates;
their proposed bodies can fail validation just like any other
candidate's, and on failure they keep their prior version.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase G — Cassettes

### Task 10: `TestUpdateExistingHappyPath` cassette

**Files:**
- Modify: `cmd/ingest_integration_test.go`
- Create: `internal/llm/testdata/cassettes/TestUpdateExistingHappyPath__*.json`

- [ ] **Step 1: Write failing test**

Append to `cmd/ingest_integration_test.go`:

```go
func TestUpdateExistingHappyPath(t *testing.T) {
    if testing.Short() { t.Skip("skipping cassette test in -short mode") }
    if _, err := os.Stat("../internal/llm/testdata/cassettes/TestUpdateExistingHappyPath__001.json"); os.IsNotExist(err) {
        t.Skip("cassette not recorded; run with LLMWIKI_RECORD=1 GEMINI_API_KEY=... to record")
    }
    setEnv(t, "LLMWIKI_CASSETTE", "TestUpdateExistingHappyPath")
    setEnv(t, "GEMINI_API_KEY", "test-key-for-replay")
    // 1. Set up tmp wiki + provider=gemini config.
    // 2. Pre-seed five existing pages with valid evidence (3 small
    //    synthetic sources; ingest each as its own source).
    // 3. Ingest a sixth source whose content overlaps three of the
    //    five pre-existing pages with --update-existing.
    // 4. Assert IngestRunResult.PagesUpdated == 3, PagesUpdateFailed == 0,
    //    UpdatedTitles names exactly the three overlapping pages.
    // 5. Assert page_update_log has 3 rows with outcome='updated' for the
    //    three overlapping pages, and 0 rows with outcome='failed'.
    // 6. Trust property check: read each updated page from disk and
    //    walk its evidence; assert every quote substring-matches some
    //    file in the union of (sixth-source files + the page's prior
    //    source files). The validator is the gatekeeper.
}
```

- [ ] **Step 2: Record the cassette**

```bash
export GEMINI_API_KEY=...
LLMWIKI_RECORD=1 go test ./cmd/ -run TestUpdateExistingHappyPath -v
```

The cassette captures: 3 ingest LLM calls (for the pre-seed sources), 1 ingest LLM call (for the new source's new pages), 3 cross-page update LLM calls (one per FTS-shortlisted candidate, all succeed). Spec recommends Gemini Flash for the heavy fan-out; the cassette wraps Gemini Flash so CI replays free.

- [ ] **Step 3: Run replay and confirm pass**

Run: `unset GEMINI_API_KEY && go test ./cmd/ -run TestUpdateExistingHappyPath -v`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
test(cassette): TestUpdateExistingHappyPath — full pillar 3 happy path

Drives v0.6's --update-existing flag end-to-end against a recorded
Gemini Flash cassette. Pre-seeds five pages with valid evidence,
ingests a new source overlapping three of them, asserts 3 updates
landed, 0 failed, page_update_log has the right outcome rows, and
the trust property holds on every updated page (every quote
substring-matches some file in the (new + existing) source union).
Recording target is Gemini Flash for the heavy fan-out (cassette
refresh stays free per spec risk #2).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 11: `TestUpdateExistingValidationDrop` cassette

**Files:**
- Modify: `cmd/ingest_integration_test.go`
- Create: `internal/llm/testdata/cassettes/TestUpdateExistingValidationDrop__*.json`

- [ ] **Step 1: Write failing test**

```go
func TestUpdateExistingValidationDrop(t *testing.T) {
    if _, err := os.Stat("../internal/llm/testdata/cassettes/TestUpdateExistingValidationDrop__001.json"); os.IsNotExist(err) {
        t.Skip("cassette not recorded; run with LLMWIKI_RECORD=1 GEMINI_API_KEY=... to record")
    }
    setEnv(t, "LLMWIKI_CASSETTE", "TestUpdateExistingValidationDrop")
    setEnv(t, "GEMINI_API_KEY", "test-key-for-replay")
    // 1. Pre-seed a single page P with 5 valid evidence quotes from a
    //    synthetic source S0.
    // 2. Snapshot P's body bytes from disk.
    // 3. Ingest a new "poorly-quoting" synthetic source S1 whose
    //    content overlaps P's keywords (so P is FTS-shortlisted) but
    //    the LLM's proposed update body produces evidence quotes that
    //    don't substring-match either S0 or S1 (the cassette pins
    //    this — the LLM hallucinates plausible-sounding quotes).
    // 4. Run ingest with --update-existing.
    // 5. Assert: PagesUpdated == 0, PagesUpdateFailed == 1; UpdateFailures
    //    contains P with reason zero-quotes-matched or below-quote-floor.
    // 6. Re-read P from disk; assert byte-identical to step 2's snapshot
    //    (the trust property: prior version preserved).
    // 7. Assert page_update_log has one row for P with outcome='failed'.
}
```

- [ ] **Step 2: Record the cassette**

```bash
export GEMINI_API_KEY=...
LLMWIKI_RECORD=1 go test ./cmd/ -run TestUpdateExistingValidationDrop -v
```

If pinning a deterministic "LLM hallucinates" output via Gemini Flash proves flaky across re-records, swap the recording target to Anthropic Haiku for this cassette only — the spec allows mixing. Document the choice in the cassette's commit message.

- [ ] **Step 3: Run replay and confirm pass**

Run: `unset GEMINI_API_KEY ANTHROPIC_API_KEY && go test ./cmd/ -run TestUpdateExistingValidationDrop -v`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
test(cassette): TestUpdateExistingValidationDrop — trust property under update_failed

Pre-seeds a 5-quote page; ingests a poorly-quoting source whose
LLM-proposed update body has 0 substring-matching quotes; asserts
update_failed outcome, page body byte-identical on disk to its
prior version, page_update_log row with outcome='failed'. This is
the v0.6 analogue of v0.5's TestPromoteAnswerStaleEvidence: the
trust property holds even (especially) on the validator-hostile
update path. A previously-valid page is never silently downgraded —
when the validator drops every proposed quote, we keep the prior
version.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase H — Docs + tag

### Task 12: README "Cross-page updates (opt-in)" subsection + CHANGELOG `[0.6.0-rc.1]` entry

**Files:**
- Modify: `README.md`
- Modify: `CHANGELOG.md`

- [ ] **Step 1: Add the "Cross-page updates (opt-in)" subsection to README**

Add to the existing "Living Wiki" section (introduced in v0.5). Place AFTER the existing "Promote a saved answer" / "Contradictions surface inline" / "Retro-linker" subsections, with a clear `### Cross-page updates (opt-in)` heading.

Required content:

1. **Lead with the opt-in framing.** First sentence: "v0.6 adds a default-off `--update-existing` flag that, when enabled, edits existing pages in light of a new source — folding the new source's claims into the pages whose claims it refines, qualifies, contradicts, or extends. Off by default because it is the most validator-hostile operation in the binary." Bold the "off by default" clause.
2. **Show Flow 4 from the spec verbatim** as a code block — the `~ Trust Property Validator (+1 evidence)` style summary lines plus the `✗ Database Layer` failure block. Anchor the user's expectation about what the feature looks like in practice.
3. **Cost paragraph.** "A 50-page repo ingest with `--update-existing` is roughly 5–10 ingest calls + up to 50 update calls + up to 5 contradiction calls = up to 65 LLM calls per ingest. On Gemini Flash (recommended for this flag) the daily 1500-call free tier comfortably absorbs this. On Anthropic Haiku, ~$0.30/ingest. On Ollama (local, 7B-class), expect most updates to be `update_failed` because small models often miss the structured-output schema; consider keeping `update_existing = false` on Ollama."
4. **Validator-interaction caveat.** "Pages whose proposed update body fails validation stay at their previous version — never silently downgraded. The trust property holds: every page on disk has ≥1 evidence quote that substring-matches its source. When a `~ Title (update_failed)` line appears, re-run with `--debug-updates` to see why each candidate's quotes didn't match." Link to the `--debug-updates` flag's doc.
5. **Audit trail.** "Every candidate considered — `updated`, `body_only`, `failed`, `skipped` — appends one row to `page_update_log` in `.llmwiki/wiki.db`. Run `sqlite3 .llmwiki/wiki.db 'SELECT pages.title, outcome, reason FROM page_update_log JOIN pages ON pages.id = page_update_log.page_id ORDER BY created_at DESC LIMIT 20'` to inspect."
6. **`llmwiki status` exposure.** "After enabling `--update-existing`, `llmwiki status` surfaces `pages updated total` and `pages update failed` counters."
7. **Configuration.** "Persist the opt-in by setting `update_existing = true` in the `[ingest]` block of `.llmwiki/config.toml`. Tune the candidate caps via `update_existing_max_candidates_per_source` (default 20), `update_existing_max_candidates_total` (default 50), and `update_existing_quote_floor` (default 2)."
8. **Forward-pointer.** "We're starting v0.6 with `--update-existing` default-off. Once we have real-world numbers from opt-in users, we may flip the default in v0.7 — track [Q11 in the spec](docs/superpowers/specs/2026-05-04-living-wiki-dynamics-design.md) for the discussion."

Update the existing **Trust Property** section: add one sentence noting that "v0.6's `--update-existing` flag is the most validator-hostile feature in the binary; it preserves the trust property by keeping the prior page version whenever the validator drops the proposed body."

Update the existing **MCP** section: add one sentence noting `mcp.ingest` accepts `update_existing: bool` and the response has gained `pages_updated` and `pages_update_failed` keys.

- [ ] **Step 2: Add `[0.6.0-rc.1]` CHANGELOG entry**

```markdown
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
```

Move any `[Unreleased]` content into `[0.6.0-rc.1]`; leave a fresh empty `[Unreleased]` at the top.

- [ ] **Step 3: Commit**

```bash
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
docs(readme,changelog): [0.6.0-rc.1] — cross-page page-update pass

README's Living Wiki section gains a "Cross-page updates (opt-in)"
subsection that leads with the default-off framing, shows Flow 4
verbatim, walks through the cost picture (Gemini Flash recommended
for this flag), spells out the validator-interaction caveat
("pages whose proposed body fails validation stay at their previous
version"), describes the page_update_log audit trail with a sqlite3
example, points at --debug-updates for diagnostics, and is honest
about Q11 ("consider flipping the default in v0.7"). Trust Property
section gets one sentence on how --update-existing preserves it
even though it's the most validator-hostile feature in the binary.

CHANGELOG [0.6.0-rc.1] covers pillar 3, --update-existing,
--debug-updates, the [ingest] config keys, the MCP return-shape
extensions, the contradiction → update bridge, the v4 schema
migration (additive only, Q8), the never-truncated audit-trail
note (Q9), the default-off rationale (Q11). All v0.5 keys are
explicitly preserved (no breaking change to mcp.ingest or
mcp.write_page).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 13: Tag `v0.6.0-rc.1` locally (no push)

**Files:** none (tag only)

- [ ] **Step 1: Final pre-tag verification**

Run, top to bottom:

- [ ] `go test ./...` is green in replay mode (no API keys exported).
- [ ] `go build ./... && go vet ./...` clean.
- [ ] Manual smoke against the spec's verification block (`docs/superpowers/specs/2026-05-04-living-wiki-dynamics-design.md`, the v1.3 portion of `## Verification` — lines 597–620 — read v0.6 in place of v1.3):

  ```bash
  unset GEMINI_API_KEY ANTHROPIC_API_KEY
  rm -rf /tmp/test-6b-wiki && mkdir /tmp/test-6b-wiki && cd /tmp/test-6b-wiki
  llmwiki init                                             # gemini default
  export GEMINI_API_KEY=...
  llmwiki ingest ./fixture-source-1.md                     # writes pages
  llmwiki ingest ./fixture-source-2.md                     # more pages
  llmwiki ingest ./fixture-source-overlapping.md --update-existing
  # Expect: "N page(s) updated:" and possibly "K page(s) update FAILED"
  llmwiki status
  # Expect: "pages updated total: N" and (if any failed) "pages update failed: K"
  sqlite3 .llmwiki/wiki.db "SELECT pages.title, outcome, reason
                            FROM page_update_log
                            JOIN pages ON pages.id = page_update_log.page_id
                            ORDER BY created_at DESC LIMIT 10"
  # Expect: rows for every candidate considered, with outcome and reason populated
  ```
- [ ] Verify the trust property by hand: pick one updated page, open it in `.llmwiki/wiki/`, confirm every `> "..."` evidence quote is byte-identical to a substring of either the new source's content or the page's original source's content.
- [ ] Re-run the same overlapping ingest with `--debug-updates` and confirm the per-candidate verdict lines on stderr are informative (the "to debug: re-run with --update-existing --debug-updates" promise from the failure-summary line).
- [ ] Spot-check `mcp.ingest` over MCP via `go test ./internal/mcp/... -run TestIngest_AcceptsUpdateExistingArg` — passes.
- [ ] Confirm `internal/mcp/server.go`'s `serverVersion == "0.6.0-rc.1"` (one-liner pin).

- [ ] **Step 2: Tag**

```bash
git -c commit.gpgsign=false tag -a v0.6.0-rc.1 -m "$(cat <<'EOF'
v0.6.0-rc.1 — Living Wiki Dynamics — cross-page page-update pass (sub-project 6b)

The validator-hostile half of sub-project 6 lands behind a
default-off opt-in flag.

  - llmwiki ingest --update-existing: after writing new pages
    from a source, runs the cross-page page-update pass over
    every existing page the source touches. Per candidate, one
    LLM call proposes an updated body; ValidateAndAttachEvidence
    is the trust gate; pages whose proposed body fails validation
    stay at their previous version — we never silently downgrade.
  - --debug-updates: per-candidate verdicts to stderr.
  - [ingest] update_existing = false config key + three tunables
    (per-source / global candidate caps, quote floor).
  - mcp.ingest accepts update_existing: bool; return shape adds
    pages_updated and pages_update_failed (alongside v0.5's
    retro_linked_pages and contradictions_flagged).
  - llmwiki status surfaces pages_updated_total and
    pages_update_failed_total counters from page_update_log.
  - Contradiction → update bridge: when --update-existing is on
    AND a contradiction was detected, the contradicting existing
    page becomes a forced candidate for the update pass.
  - v4 schema migration: additive page_update_log table only.
    No ALTER TABLE on existing tables. Roll-forward only.

TRUST PROPERTY HOLDS. Every page on disk has >=1 evidence quote
that substring-matches its source — including pages updated via
pillar 3 (against the union of new + prior source files). Pages
with update_failed are byte-identical on disk to their previous
version.

Default-off everywhere (Q11). Consider flipping in v0.7 once we
have real-world numbers from opt-in users.

Promotion to v0.6.0 is a manual follow-up after the spec's
2-week stability window — longer than v0.5's 1-week window
because pillar 3's failure modes need a longer soak.
EOF
)"
```

- [ ] **Step 3: Verify**

Run: `git tag -l "v0.6*"`
Expected: prints `v0.6.0-rc.1` (alongside any other v0.6-prefixed tags, of which there should be none).

Do **not** `git push --tags`. Promotion to a real release is a manual step matching v0.3 / v0.4 / v0.5's pattern.

---

## Done criteria

- All 13 tasks have a green checkbox.
- `go test ./...` is green in replay mode (no API keys required).
- `go build ./... && go vet ./...` clean.
- A fresh `mkdir wiki && cd wiki && llmwiki init && llmwiki ingest <s1> && llmwiki ingest <s2> && llmwiki ingest <s3-overlapping> --update-existing` walks through end-to-end, lands at least one `~ Title` "updated" line in the summary, and the on-disk evidence for that updated page substring-matches the (new + prior) source union.
- `llmwiki status` shows non-zero `pages updated total` after a successful `--update-existing` ingest.
- `sqlite3 .llmwiki/wiki.db "PRAGMA user_version"` returns `4` on a fresh wiki and on a v3-upgraded wiki; `page_update_log` exists; existing tables (`pages`, `evidence`, `sources`, `source_files`, `chunks`) are byte-identical pre/post v4 in `sqlite_master`.
- `llmwiki mcp` exposes the same seven tools as v0.5 (`ingest`, `ask`, `list_pages`, `read_page`, `write_page`, `lint`, `promote_answer`); `mcp.ingest` accepts `update_existing` and returns `pages_updated` + `pages_update_failed` keys.
- The tag `v0.6.0-rc.1` exists locally; not pushed.
- README's "Cross-page updates (opt-in)" subsection leads with the default-off framing, walks through the cost picture, names Gemini Flash as the recommended provider for the flag, and references `page_update_log` for the audit trail.
- CHANGELOG `[0.6.0-rc.1]` is explicit about: additive-only schema migration (Q8), never-truncated audit log (Q9), MCP single-round-trip surface (Q10), default-off rationale + v0.7 follow-up (Q11).
- **TRUST PROPERTY REAFFIRMED at every disk-write site:** `ingest`, `promote`, `mcp.write_page`, `mcp.promote_answer`, the retro-linker (body-only — no new evidence), the new `wiki.UpdateExistingPagesFromSource` (validator-gated, quote-floor-protected, content_hash-skip-guarded, audit-trail-recorded). Pages with `update_failed` are byte-identical on disk to their previous version. Pillar 3 is the most validator-hostile feature in the binary; the trust property holds anyway.
