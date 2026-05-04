# Sub-project 6 — Living Wiki Dynamics

**Status:** design — awaiting user feedback before plan-pass
**Date:** 2026-05-04
**Author:** Mritunjay Sharma (with Claude)

> **Version-numbering note (added 2026-05-04 after the renumber):** this design was authored when the project's release line was v1.x. The line has since been renumbered to pre-1.0. Where this spec says **v1.2** (sub-project 6a), read **v0.5**; **v1.3** (sub-project 6b, still-unshipped) → **v0.6**; **v1.1** → **v0.4**; **v1.0** → **v0.3**. The project is honestly pre-1.0; the renumber reflects that.

## Context

Sub-projects 1, 3, 4, and 5 shipped. As of `e2810f5` (`v1.1.0-rc.1`), `llmwiki` is a write-once accumulator: `llmwiki ingest` reads a source, the LLM proposes pages, `wiki.ValidateAndAttachEvidence` (in `internal/wiki/ops.go`) drops anything whose evidence quotes don't byte-exactly substring-match the named source file, and the survivors land on disk and in `wiki.db`. `[[wikilinks]]` are emitted at write time over the union of (existing titles, this batch's titles) by `wiki.RewriteBareReferencesAsWikilinks` (`internal/wiki/obsidian.go`); `index.md` and `log.md` are regenerated/appended at the end of each `ingest` run; the MCP server (`internal/mcp/handlers.go`) exposes the same flow over `mcp.ingest` and `mcp.write_page`. Cheap providers (Gemini Flash, OpenRouter free tier, Ollama) work because the validator's "drop quote, drop page" behaviour is provider-agnostic.

What the wiki does *not* yet do is the load-bearing thing in Karpathy's gist that made the pattern interesting in the first place: **react to new information**. Today, ingesting a second source that contradicts the first leaves both pages on disk untouched; ingesting a third source that *should* be linked from four earlier pages leaves those four pages without `[[NewPage]]` references; promoting a useful saved answer from `.llmwiki/answers/` into a real wiki page is a manual copy-paste with no validator pass; and `cmd/lint.go`'s contradiction batcher only runs on demand, after the fact, against the whole wiki — not at ingest time when the user's context is hot.

Sub-project 5's non-goals section (lines 99–113 of `2026-05-04-mcp-and-cheap-providers-design.md`) explicitly tagged four behaviours for sub-project 6:

1. **Contradiction flagging on ingest** — surface "this new source contradicts page Y" inline, while context is hot.
2. **Cross-page integration / retro-linker** — when a new page lands, retroactively wikilink the older pages that mention it.
3. **The "10–15 pages updated per source" pattern** — ingesting a source not only writes new pages, it *edits* the existing pages whose claims the new source refines, qualifies, contradicts, or extends.
4. **Saved-answer → wiki-page promotion** — `llmwiki promote <answer-id>` (and `mcp.promote_answer`) lifts a validated answer into a permanent wiki page.

These four together are what make a wiki *living*. None of our competitors (`nashsu/llm_wiki`, `lucasastorian/llmwiki`, `Pratiyush/llm-wiki` per sub-project 5's competitive readout) implement any of them with deterministic evidence validation as the gate. Sub-project 5 won the positioning move ("trustable, headless, scriptable, MCP-driven, Obsidian-friendly, cheap-provider-capable"); sub-project 6 wins the *behavioural* move ("the wiki updates itself when reality updates").

This spec also has to be honest about a thing sub-project 5 hand-waved past: cross-page editing means a single ingest can drop M pages of *previously valid content* if the new source's quotes don't byte-match. That is the validator working as designed — the trust property says "no page on disk lacks an evidence quote that substring-matches its source" — but a user who does not understand it will see pillar 3 as "llmwiki deleted half my wiki when I added a blog post." The spec calls this out at every relevant decision point.

## Goals

1. **Contradiction flagging at ingest time.** When a new source's pages-to-be-written claim X and an existing page claims ¬X, the user sees the conflict before the new page lands — not after the fact via `lint`. The existing post-ingest `cmd/lint.go` flow is preserved unchanged so users who do not opt into the live flow still get the "audit my whole wiki" command they have today.
2. **Retro-linker for cross-page integration.** When `ingest` (or `mcp.write_page`, or `promote`) writes a new page, every *existing* page whose body mentions the new title in bare prose gets `[[wikilinks]]` updated in place. Idempotent, body-only, never touches evidence — `wiki.RewriteBareReferencesAsWikilinks` already has the right shape; we extend it to "scan all pages, not just new ones."
3. **Cross-page page-update pass.** For each new source, identify the M existing pages whose claims the source touches (refines, qualifies, contradicts, or extends), have the LLM propose updated bodies for them, run every updated body through `wiki.ValidateAndAttachEvidence` against the union of (the new source's files) + (the original source files backing the pre-existing evidence on that page), and replace the body on success. Pages whose updated body fails validation **stay at their previous version** with a warning surfaced; the user is never silently downgraded.
4. **Saved-answer → wiki-page promotion.** `llmwiki promote <answer-file-or-slug>` reads a `.llmwiki/answers/<ts>-<slug>.md` file written by `cmd/ask.go`, parses its already-validated evidence quotes, defensively re-runs `ValidateAndAttachEvidence` (source files may have changed since the ask), and lands the result as a real wiki page through the same disk + DB + index + log path that `cmd/ingest.go` and `mcp.write_page` use.
5. **Every new behaviour preserves the trust validator invariant.** No code path introduced by sub-project 6 lets a page reach disk without ≥1 evidence quote that substring-matches its named `source_file`. This is non-negotiable and explicit at every step below.
6. **Every new behaviour works over MCP.** `mcp.ingest` already returns `pages_written, evidence_quotes, dropped_pages`; we extend its return shape with `pages_updated, pages_update_failed, contradictions_flagged`. New `mcp.promote_answer` tool exists. No new MCP tool is added for the cross-page update pass — it runs inline inside `mcp.ingest` (rationale: open question 10 below).
7. **Every new behaviour works on cheap providers.** The cross-page update pass is the most token-hungry change in the binary's history; the spec sizes its cost against Gemini Flash and OpenRouter free tier *first*, Anthropic Haiku second.
8. **Every new on-disk artefact remains Obsidian-friendly Markdown.** No new file types, no new frontmatter keys that Obsidian's Dataview cannot parse with the existing flat-scalar / flat-array conventions from sub-project 5.

## Why this sub-project now

Sub-project 5 closed the *positioning* gap. The CLI now has the MCP surface, the cheap providers, the Obsidian story. Sub-project 6 closes the *behavioural* gap — the thing that makes one wiki actually feel alive vs. another wiki that's a dump of write-once Markdown. Karpathy's gist describes this as the difference between a notebook and a knowledge base: a notebook accumulates entries, a knowledge base reorganises itself when entries arrive. We have the notebook. We need the knowledge base.

Sub-project 5 was also a precondition. Pillar 3's per-source LLM-call multiplier (one ingest call → one cross-page-update call per touched page) makes Anthropic-only billing untenable for typical-sized ingest. The cheap-provider work specifically existed to keep the unit economics of pillar 3 within reach.

## Recommended scoping: split v1.2 into 6a + 6b

The user's first scoping question is whether to ship all four pillars in one v1.2 or split. The honest answer is: **split**, with the boundary drawn around the validator-interaction risk.

The four pillars do not share infrastructure equally:

- **Retro-linker** (pillar 2) reuses `RewriteBareReferencesAsWikilinks` over a wider input set; ~150 LOC of wiring + a SQL pass to find candidate pages. Body-only, idempotent, cannot drop any page or any evidence. **Lowest-risk pillar in the binary's history**, alongside sub-project 5's `index.md` regeneration.
- **Answer-promotion** (pillar 4) reuses `ValidateAndAttachEvidence` over an answer file that *already passed* it once at ask-time. The defensive re-validation either succeeds (cheap, no LLM call needed if the user accepts the answer's prose verbatim as the body) or surfaces the failure to the user before any disk write. **Self-contained.**
- **Contradiction-on-ingest** (pillar 1) needs to look at *what the new source claims* vs. *what existing pages claim* — the same retrieval shortlist the cross-page update pass needs. Sharing infrastructure with pillar 3 is natural but optional: as a flag-only feature ("warn but don't update"), pillar 1 can ship without pillar 3. As a full feature ("update existing pages on contradiction"), it *is* pillar 3.
- **Cross-page page-update** (pillar 3) is the hard one. Each of the M touched pages costs one LLM call. The validator can drop the updated body if quotes don't match. Edit cycles, oscillation, and "I added a source and lost half my wiki" are all real failure modes. ~6–10x the implementation surface of any other pillar.

### Recommendation: **6a as v1.2, 6b as v1.3**

| Pillar                                  | v1.2 (6a)                                                | v1.3 (6b)                                                              |
|-----------------------------------------|----------------------------------------------------------|------------------------------------------------------------------------|
| 4. Answer-promotion                      | full (`promote` CLI + `mcp.promote_answer`)              | —                                                                       |
| 2. Retro-linker                          | full (post-`ingest`, post-`promote`, post-`mcp.write_page`) | —                                                                       |
| 1. Contradiction flagging on ingest      | **warn-only**: surface contradictions inline; no edits.  | upgraded to "edit existing page" once 6b lands.                         |
| 3. Cross-page page-update pass           | —                                                        | full (gated behind `[ingest] update_existing = false` default-off flag). |

**Why split this way.** 6a is three additive, validator-respecting, non-destructive features that compound into a coherent v1.2 story: "the wiki notices things now." 6b is the dangerous one — it edits previously-valid pages, can silently revert improvements, and has the cost picture (§Risks) the user explicitly asked the spec to be honest about. Shipping 6a first means users *have* the contradiction surface, the retro-linking, and the answer-promotion CLI for ~3 weeks before 6b's cross-page-edit pass lands. That gap doubles as the soak window for finding edge cases in the surrounding infrastructure (FTS shortlists, edit-cycle detection, MCP return-shape extensions) before the validator-hostile pillar lights them up.

**Open question:** the user may prefer "all four pillars as v1.2, gated behind a default-off `--update-existing` flag." That's defensible if the user's threat model is "I'll personally test 6b for two weeks before flipping the flag." Spec assumes the default-split — confirm.

## Non-goals (deferred / dropped)

- **Page versioning / snapshots.** Pillar 3 *replaces* page bodies; the prior body is not retained in a `page_versions` table. Users who want history use git over `.llmwiki/wiki/`. Adding versioning is a sub-project 7 question if it ever comes up. **Deferred.**
- **Edit-cycle detection beyond a single-step content_hash check.** The full "did pillar 3 oscillate page P over the last 3 ingests" machinery is out of scope; v1.2 / v1.3 ship a single-step "skip update if proposed body has the same `content_hash` as the current body" check. **Deferred.**
- **Embedding-based candidate-page retrieval for pillar 3.** Sub-project 5 permanently dropped vector search; we re-affirm. The retrieval shortlist for "which existing pages does this new source touch" uses FTS5 over `pages_fts` and `evidence_fts` plus a title-substring scan over `AllPageTitles()`. **Permanent drop.**
- **LLM-judged retro-linking.** The retro-linker uses string-match-only. We considered "send each candidate (existing page body, new title) to the LLM and ask 'should this be a wikilink?'" and rejected it on cost: even at one Gemini Flash call per candidate it adds N×M calls to every ingest. The conservative string-match path is good enough for 95% of cases; the 5% of false negatives manifest as "this older page mentions FooBar but doesn't link to `[[FooBar]]`" and Obsidian's graph view shows the gap, which is fine. **Permanent drop in v1.2; revisit if user feedback says otherwise.**
- **Cross-source contradiction resolution policy** ("if source A and source B disagree, which wins?"). Out of scope. The validator continues to accept any quote that substring-matches *some* ingested source; the contradiction surface flags the conflict and the user resolves it. There is no auto-resolve. **Permanent drop.**
- **`promote` from a *failed* ask** (one with no validated evidence). `cmd/ask.go` does not currently produce evidence-less archived answers — every answer is grounded in retrieved pages whose evidence is already validated. We don't need to handle the case. **Implicit drop.**
- **Web UI for reviewing contradictions or cross-page-update diffs.** Sub-project 2 is permanently dropped. Contradiction surfaces are a textual `.llmwiki/contradictions.md` queue file; cross-page-update diffs print to the ingest log and to `log.md`. Obsidian renders both. **Permanent drop.**
- **Real-time MCP "subscribe to ingest events"** (MCP `progress` notifications). v1 of our MCP server is tools-only per sub-project 5's non-goals. Ingest's new "M pages updated" payload is a final return value, not a stream. **Deferred to whenever we add MCP `progress`.**
- **Image / multimodal handling and OCR for scanned PDFs.** Still nashsu's niche. **Deferred to sub-project 7 or later.**

## What users see

Three flows on top of v1.1.0's surface, plus the upgraded `ingest` summary line.

### Flow 1 — contradiction flagged inline (6a, v1.2)

```bash
llmwiki ingest https://example.com/blog/2026-go-channels-rewrite.html
# Resolved to 1 source file(s)
# [1/1] processed
# Ingested 2 page(s) from https://example.com/blog/2026-go-channels-rewrite.html
#   ✓ Channel Internals (3 evidence, files: blog.html)
#   ✓ Goroutine Scheduling Changes (4 evidence, files: blog.html)
#
# !! 1 contradiction(s) flagged against the new pages:
#    - new page "Channel Internals" claims:
#        > "channel sends are now never lock-free"
#      conflicts with existing page [[Go Concurrency]]:
#        > "channel sends on uncontended channels remain lock-free"
#      both quotes are validated against their own sources; resolve manually.
#      logged to: .llmwiki/contradictions.md
#
# saved: .llmwiki/contradictions.md
```

The contradictions file is plain Markdown, append-only, Obsidian-readable, format mirrors `log.md`:

```markdown
- 2026-05-04T14:30:12Z **ingest** https://example.com/blog/2026-go-channels-rewrite.html
  - new page "Channel Internals" vs existing [[Go Concurrency]]:
    - new claim: > "channel sends are now never lock-free" (blog.html:14-14)
    - existing claim: > "channel sends on uncontended channels remain lock-free" (internal/sync/chan.go:4-4)
```

### Flow 2 — retro-linker fires after a new page lands (6a, v1.2)

```bash
llmwiki ingest ./internal/sync/mutex.go
# Ingested 1 page(s) from ./internal/sync/mutex.go
#   ✓ Mutex Implementation (5 evidence, files: internal/sync/mutex.go)
# Retro-linked 4 existing page(s) that now reference [[Mutex Implementation]]:
#   - Goroutine Scheduling
#   - Channel Internals
#   - Trust Property Validator
#   - Database Layer
```

Each of those four pages has its body re-written through `RewriteBareReferencesAsWikilinks`; their evidence rows, frontmatter `content_hash` (recomputed against the new body), and `updated_at` change. No LLM call. The user opens Obsidian and sees the graph view light up the new connections.

### Flow 3 — answer promotion (6a, v1.2)

```bash
ls .llmwiki/answers/
# 2026-05-04-143012-what-deps-llmwiki-uses.md
# 2026-05-04-150208-how-does-validator-work.md

llmwiki promote 2026-05-04-150208-how-does-validator-work.md \
                --title "Validator Internals"
# Loaded answer: how does validator work
# Re-validating 4 evidence quote(s)...
#   ✓ all 4 quotes still substring-match their source files
#   ✓ wrote page "Validator Internals" (4 evidence, 1 source)
#   ✓ retro-linked 2 existing page(s) to [[Validator Internals]]
# saved: .llmwiki/wiki/Validator Internals.md
```

If `--title` is omitted, the title is derived from the answer's `question` frontmatter via the same slugify-then-Title-Case logic the chat-ingestion answer-saver already uses (`cmd/ask.go:slugify`). If the title collides, the command exits with the same `title_exists` structured-error code the MCP `write_page` handler uses (sub-project 5's resolution of open question 3) — the user must pick a different title or supersede manually.

If `--rewrite` is passed, an LLM call rewrites the answer body into wiki-style prose (see open question 6 below); without the flag, the body is the answer text verbatim. Default is **without** rewrite — cheap, predictable, and the answer was already written for human consumption.

### Flow 4 — cross-page page-update pass (6b, v1.3)

```bash
llmwiki ingest ./CHANGELOG-1.2.md --update-existing
# Resolved to 1 source file(s)
# [1/1] processed
# Ingested 3 page(s) from ./CHANGELOG-1.2.md
#   ✓ Release 1.2 Highlights (5 evidence, files: CHANGELOG-1.2.md)
#   ✓ Living Wiki Dynamics (4 evidence, files: CHANGELOG-1.2.md)
#   ✓ Cross-page Update Pass (6 evidence, files: CHANGELOG-1.2.md)
#
# Scanning 47 candidate page(s) for updates...
#   [12/47] processed
#
# 7 page(s) updated:
#   ~ Trust Property Validator   (+1 evidence)
#   ~ Ingest Pipeline            (+2 evidence)
#   ~ MCP write_page             (+1 evidence)
#   ~ Obsidian Output            (+1 evidence, body rewritten)
#   ~ Provider Abstraction       (+1 evidence)
#   ~ Page Lifecycle             (body rewritten only)
#   ~ Index Hub                  (+1 evidence)
#
# 2 page(s) update FAILED — kept at previous version:
#   ✗ Database Layer
#       proposed body had 0 quotes that substring-matched any source.
#   ✗ Cassette Infrastructure
#       proposed body had 1/3 quotes that substring-matched; below new-quote-floor of 2.
#       to debug: re-run with --update-existing --debug-updates and compare logs.
```

Without the `--update-existing` flag (default), only pillars 1, 2, and 4 fire — pillar 3 stays inert. The flag flips per-invocation; a `[ingest] update_existing = true` in `config.toml` flips it permanently. We default to off because the cost picture (§Risks #2, "Cost") and the validator-interaction picture (§Risks #3, "Update validation drops previously-valid content") both argue strongly against on-by-default.

The exact CLI surface added by sub-project 6:

- **6a:** `llmwiki promote <answer-file-or-id>` — new command. Flags: `--title`, `--rewrite`, `--no-save` (skip log entry, debug only).
- **6a:** `llmwiki ingest` gains no new flags; the contradiction-on-ingest and retro-linker passes run unconditionally. (Both are cheap; neither can degrade existing trust property; gating either feels like ceremony.)
- **6a:** New `mcp.promote_answer` MCP tool. Inputs: `answer_path` (or `answer_slug`), optional `title`, optional `rewrite: bool`. Returns `{title, path, evidence_quotes, retro_linked_pages: [...]}`.
- **6a:** `mcp.ingest` return shape extended: adds `contradictions_flagged: int` and `retro_linked_pages: int`.
- **6b:** `llmwiki ingest --update-existing` — new flag. Default off.
- **6b:** `[ingest] update_existing` — new config key. Default false.
- **6b:** `mcp.ingest` accepts new optional `update_existing: bool`. Return shape extended further: `pages_updated, pages_update_failed`.
- **6b:** `llmwiki status` adds `pages_updated_total` (sum across all ingests, ever) and `pages_update_failed_total`. Pure read; no migration.

`ask`, `lint`, `version`, `init` are unchanged in surface. `lint`'s contradiction-batching path stays as-is; users who want the whole-wiki sweep still run `llmwiki lint`. The on-ingest surface is deliberately complementary, not a replacement.

## Architecture overview

Four load-bearing additions, each isolated to a small new file or to a single existing function. No schema migration in 6a; one additive table (`page_update_log`) in 6b that the runner can write through without disturbing v3 wikis.

### Pillar 4: Answer promotion (6a)

`internal/wiki/promote.go` (new):

```go
func PromoteAnswer(
    ctx context.Context,
    cfg IngestSourceConfig,
    db *db.DB,
    client llm.Client,
    answerPath string,
    opts PromoteOptions,
) (PromoteResult, error)
```

- Reads the answer file via the existing `wiki.ParseSavedAnswer` (need to add — currently `cmd/ask.go:saveAnswer` writes via `wiki.FormatSavedAnswer`; we add the inverse). Frontmatter has `question`, `created_at`, `model`. Body has `# Answer\n\n<text>\n\n## Sources\n\n**[1] Page Title**\n\n> "quote"  (path:a-b)\n...`.
- Parses every `> "quote"  (path:a-b)` line into `wiki.Evidence{Quote, SourceFilePath, LineStart, LineEnd}`. The path-and-lines suffix is the canonical form `cmd/ask.go:printSources` emits via `wiki.evidenceAnnotation`; `wiki.ParseSavedAnswer` is its inverse.
- For each parsed evidence row, looks up the underlying `db.SourceFile` by `relative_path` (using the existing `byPath` lookup pattern from `internal/mcp/handlers.go:writePageHandler`). Reads the source file's bytes via the same `readSourceFileContent(sourceURI, relPath)` helper.
- Runs `wiki.ValidateAndAttachEvidence` against the (synthesized `[]ingest.SourceFile`) for the candidate page. **Pages whose evidence quotes no longer match (because the source file changed since the ask) get dropped here**; the function returns an `evidence_invalid` error with the same shape `mcp.write_page` returns, and no disk write happens. This is the answer of "what about source drift between ask-time and promote-time?": defensive re-validation, fail loud.
- On success, builds a `wiki.Page{Title: opts.Title, Body: opts.Body or answer text, Evidence: validated, ...}` and runs the same disk-and-DB write path the MCP `write_page` handler uses (`WritePage` + `db.UpsertPage` + `db.InsertEvidence` + `db.UpsertLinks`). Then runs the retro-linker over all existing pages.
- Appends `log.md`: `- <ts> **promote** <slug> → <title> (<n> evidence, retro-linked <m> pages)`.

`cmd/promote.go` is a thin cobra wrapper that resolves the answer file (accepts a bare slug, a filename, or an absolute path) and translates flags to `PromoteOptions`. The MCP `promote_answer` handler does the same translation from MCP args.

`PromoteOptions{Title, Body string, Rewrite bool}`. When `Body == ""` and `Rewrite == false`, the body is the answer's `# Answer` section verbatim. When `Rewrite == true`, an LLM call (one structured `write_pages`-style call with the answer text as SOURCE and a single-page hint) rewrites it. The rewritten body still has to pass evidence validation against the same parsed quotes, so this can fail; if it does, the command falls back to the verbatim body and logs a WARN.

### Pillar 2: Retro-linker (6a)

`internal/wiki/retrolink.go` (new):

```go
func RetroLinkPages(
    db *db.DB,
    wikiDir string,
    newTitles []string,
) (RetroLinkResult, error)
```

- Loads `db.AllPageRecords()` (skipping pages whose title is in `newTitles` — they were just rewritten with the full title set).
- For each candidate page, runs `RewriteBareReferencesAsWikilinks(page.Body, newTitles)` (note: only over the **new** titles — we don't re-scan every existing title against every existing page; that's the v1.1 behaviour at write-time, and it's already done). The rewriter is idempotent, so a page that already has `[[NewTitle]]` is a no-op.
- For each page whose body changed, recomputes `content_hash`, updates `updated_at`, runs `WritePage` + `db.UpsertPage`. Evidence rows are untouched (rewriter is body-only).
- Returns `RetroLinkResult{UpdatedTitles []string}` for the caller's summary.

**Performance.** The naive shape is O(N×M) where N = existing pages, M = new titles, per ingest. At N=1000, M=5, this is 5000 substring searches — fast. At N=10000, M=20, this is 200000 — still <1s in Go for our typical body sizes (1–10 KB each). The pre-filter — only consider pages whose FTS5 `pages_fts` row matches *any* of the new titles — keeps it sub-linear at very large N. We add the FTS pre-filter for `N > 500` (cheap to gate, high payoff). Spec guidance: implementer measures on a real 5k-page wiki; if the unfiltered pass is <500ms there, ship it; otherwise wire the FTS pre-filter.

**Trust interaction.** None. `RewriteBareReferencesAsWikilinks` is body-only and idempotent; evidence rows reference source content, not page-body content. The validator never runs in this path because no new claim is being made.

### Pillar 1 (6a, warn-only): Contradiction-on-ingest

The current `wiki.DetectContradictions(ctx, client, pages []Page)` (in `ops.go`) takes a flat slice and returns a free-form text report. For on-ingest use, we need (a) only the *new* pages compared against the *existing* pages whose titles overlap, (b) structured output (page-pair tuples) so the runner can emit them as inline warnings and append to `.llmwiki/contradictions.md`.

`internal/wiki/contradict.go` (new):

```go
func DetectIngestContradictions(
    ctx context.Context,
    client llm.Client,
    newPages []Page,
    existingPages []db.PageRecord,
    candidateLimit int,
) ([]Contradiction, error)

type Contradiction struct {
    NewPageTitle      string
    NewPageQuote      string
    NewPageSourceFile string
    NewPageLines      [2]int
    ExistingTitle     string
    ExistingQuote     string
    ExistingSourceFile string
    ExistingLines     [2]int
    Description       string
}
```

- Builds a candidate shortlist: for each `newPage`, run `db.SearchPages(newPage.Body, candidateLimit)` and `db.SearchEvidence(newPage.Body, candidateLimit)` to find existing pages and existing evidence rows that share keywords. Union the page IDs; cap at `candidateLimit` (default 5 per new page) to bound cost.
- For each (new page, candidate existing page) pair, run one structured LLM call: SYSTEM = "you are a contradiction detector; given page A and page B, output a JSON array of (claim_a_quote_index, claim_b_quote_index, description) tuples for direct factual contradictions only — qualifications and additions are NOT contradictions; if none, return []." USER = the two pages with their evidence quotes labelled.
- Filter the output: a "real" contradiction requires both quotes to come from already-validated evidence. If the LLM hallucinates a quote that isn't in either page's evidence, drop it (validator-style).
- Return the deduped list.

**`cmd/lint.go` does not change.** Its existing `wiki.DetectContradictions` is the whole-wiki batcher; it stays. The new on-ingest function is a sibling, not a replacement. Reusing the same name was tempting but rejected — the on-ingest call has different inputs, different output shape, different cost characteristics. Two functions, both in `internal/wiki/`. Spec recommends keeping them side-by-side and noting the relationship in the package doc-comment.

**Cost.** Per ingest with N new pages, this is at most `N × candidateLimit` LLM calls (default 5 per new page). With 3 new pages, that's 15 calls — comparable to the ingest itself, so this doubles the ingest's call count. At Gemini Flash (free), no cost. At Anthropic Haiku, ~$0.05–0.15 per typical ingest. **Document this in `init`'s walkthrough.**

**Output destinations.** Inline ingest log (the `!! N contradiction(s) flagged` block in Flow 1 above) plus an append to `<wikiDir>/contradictions.md`. Format mirrors `log.md` (RFC3339 timestamp, append-only, never rotated). Failures of the contradiction call (LLM error, timeout) log a WARN to stderr and do NOT fail the ingest — the new pages still land. Contradiction detection is an information feature; it cannot block trust-validated writes.

### Pillar 3 (6b, full): Cross-page page-update pass

This is the spec's hard pillar. Plug-in point is inside `wiki.IngestSource` (`internal/wiki/ingest_runner.go`), after the existing `for i := range allPages { ... }` write loop, gated by `opts.UpdateExisting`.

`internal/wiki/update_existing.go` (new):

```go
func UpdateExistingPagesFromSource(
    ctx context.Context,
    cfg IngestSourceConfig,
    db *db.DB,
    client llm.Client,
    newSourceFiles []ingest.SourceFile,
    newPageTitles []string,
    opts UpdateExistingOptions,
) (UpdateResult, error)

type UpdateResult struct {
    Updated      []string                  // page titles whose body was rewritten and re-validated
    BodyOnly     []string                  // updated for [[wikilinks]] only — no evidence change
    Failed       []UpdateFailure           // proposed body failed validation; page kept at prior version
    Skipped      []string                  // candidates we considered but the LLM said "no change"
}

type UpdateFailure struct {
    Title           string
    Reason          string                  // "zero-quotes-matched", "below-quote-floor", "llm-error", ...
    DroppedQuotes   []DroppedQuote
}
```

**Candidate selection.** For each `newSourceFile`, run `db.SearchPages(newSourceFile.Content, maxCandidatesPerSource)` and `db.SearchEvidence(newSourceFile.Content, maxCandidatesPerSource)`. Union page IDs. Cap at `maxCandidatesPerSource` (default 20) and a global per-ingest cap (default 50) so a single 200-file repo doesn't trigger 1000 update calls. Pages whose ID is in `newPageTitles` are excluded (they were just written).

**Per-candidate update call.** For each candidate page, build SOURCE = the new source files (full bodies, since the LLM needs to know what the new claims are) + a marker `=== EXISTING PAGE ===` followed by the candidate's title and current body and the candidate's existing evidence quotes (so the LLM can preserve old quotes that still match). Run one structured LLM call against the same `writePagesTool` schema the ingest pipeline uses, with a system-prompt variant:

```
You update an EXISTING wiki page in light of a NEW SOURCE.
Output a single page with the same title; the body should incorporate
information from NEW SOURCE that refines, qualifies, or extends the
existing page. Every evidence quote must verbatim-substring-match
either the NEW SOURCE files OR the existing page's already-validated
quotes (those are listed under EXISTING EVIDENCE). Do not invent
quotes. If NEW SOURCE does not actually update this page, respond
with `{"pages": []}` and we will keep the page unchanged.
```

The `writePagesTool` schema is reused unchanged, so the LLM emits the same `{quote, source_file}` pairs and the same `ExtractPagesFromToolResult` parses them.

**Validator pass.** Run `ValidateAndAttachEvidence` against the union of (`newSourceFiles`) + (synthesized SourceFile rows for the candidate's existing source files, read via `readSourceFileContent`). The trust property is unchanged: every surviving quote must substring-match its named source file.

**Quote floor.** If the validated page has fewer than `min(2, len(originalEvidence))` valid quotes, mark as `failed` and **keep the original**. The floor exists because a single weak update — one quote that happens to match — should not replace a page that previously had 5 strong quotes. The "min(2, len(originalEvidence))" form lets pages that originally had only 1 quote not get held to a higher bar than they started at.

**content_hash skip.** If the validated body's `content_hash` equals the current `content_hash`, log "no-op" and skip the disk write (saves us from churning `updated_at` on identity updates). This is the single-step oscillation guard.

**Disk + DB write.** On success, the same path as ingest: `WritePage` + `UpsertPage` + delete-old-evidence-and-`InsertEvidence`. Evidence rows for the *prior* version are deleted via `db.DeleteEvidenceForPage(stored.ID)` (new queries.go method — currently we have `DeleteEvidenceForSource` and `DeleteEvidenceForSourceFile` but not "for page"). The new evidence rows reference whichever `source_file_id`s back the surviving quotes — this can be a mix of the new source's files and the existing source's files.

**On failure.** The page stays at its prior version. We log the failure to stderr, append to `log.md` ("update_failed"), and return the failure in `UpdateResult.Failed` for the caller to surface in the ingest summary. **No silent downgrade.**

**Concurrency.** Same `sem := make(chan struct{}, ingestMaxInflight)` pattern as `IngestSource`'s chunk fan-out. Each candidate update is one LLM call; bound at 5 in flight.

**Audit trail.** New `page_update_log` table (6b schema migration to v4):

```sql
CREATE TABLE page_update_log (
  id INTEGER PRIMARY KEY,
  page_id INTEGER NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
  source_id INTEGER REFERENCES sources(id) ON DELETE SET NULL,
  prior_content_hash TEXT NOT NULL,
  new_content_hash TEXT,
  outcome TEXT NOT NULL,        -- "updated" | "body_only" | "failed" | "skipped"
  reason TEXT,                  -- non-null when outcome IN ('failed','skipped')
  evidence_added INTEGER,
  evidence_removed INTEGER,
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_page_update_log_page ON page_update_log(page_id);
CREATE INDEX idx_page_update_log_source ON page_update_log(source_id);
```

This is the only schema change in sub-project 6 — and it's only in 6b. 6a is purely additive code over the v3 schema.

## Schema changes

- **6a (v1.2): none.** Holds at `PRAGMA user_version = 3` from sub-project 4. Pre-v3 wikis are unaffected. The retro-linker writes through `db.UpsertPage`; the contradiction-on-ingest pass writes only to `<wikiDir>/contradictions.md` (a file, not a row). Answer-promotion writes through `db.UpsertPage` + `db.InsertEvidence` exactly the way `mcp.write_page` already does.
- **6b (v1.3): one additive migration to v4.** Adds `page_update_log` (above). No `ALTER TABLE` against existing tables. Pre-v4 wikis upgrade silently in `db.Open`. Roll-forward only — no down-migration.

The user's spec brief asked whether sub-project 6 needs `source_pages` (mapping table for "which pages does this source contribute to") or `contradictions` (cache table). Neither is needed:

- `source_pages` is *already implicit* in the `evidence` table (`evidence.source_id` + `evidence.page_id`) and the `pages.source_ids` JSON array. The `db.GetEvidenceForPage` and `db.SearchEvidence` queries we already have answer "which pages does source X back?" for free.
- `contradictions` is a file (`<wikiDir>/contradictions.md`), not a DB row, deliberately. Same reasoning as `log.md`: append-only, Obsidian-readable, parseable by other tooling, not a source of truth that needs querying. If `lint` ever wants to render the contradictions queue, it `os.ReadFile`s the file; no SQL needed.

## Config additions

```toml
# 6a (v1.2) — no new keys. Behaviour is unconditional; users who
# don't want it just see no contradictions found and no retro-linking
# triggered (because their wiki has no overlapping titles or claims).

# 6b (v1.3) — one new key under [ingest], default off.
[ingest]
update_existing = false
# When true, every `llmwiki ingest` runs the cross-page page-update
# pass after writing new pages. Costs roughly one LLM call per
# matched candidate page (default cap 50 candidates per ingest).
# Off by default because (a) the cost picture is real and (b) the
# update validator can drop previously-valid pages if the new
# source's quotes don't match.

# Optional tunables, read by wiki.UpdateExistingOptions:
update_existing_max_candidates_per_source = 20
update_existing_max_candidates_total      = 50
update_existing_quote_floor               = 2
```

`applyIngestDefaults`-style fall-back fills missing values (matches the sub-project 5 pattern). Pre-v1.2 configs without these keys keep working.

## CLI surface changes

### 6a (v1.2)

- `llmwiki promote <answer-file-or-id>` — new command (Flow 3 above).
  - `--title TITLE`: override the slug-derived title. Required if the answer's question doesn't yield a unique slug.
  - `--rewrite`: LLM-rewrite the answer body into wiki prose. Default off.
  - `--no-save`: skip the `log.md` entry. Debug only.
- `llmwiki ingest` ingest summary line gains contradiction and retro-link counts (no flag changes).
- `llmwiki status` ingest counters unchanged (6a writes nothing new to the DB).
- `llmwiki mcp` exposes new `promote_answer` tool.
- `mcp.ingest` return shape gains `contradictions_flagged` and `retro_linked_pages`.

### 6b (v1.3)

- `llmwiki ingest --update-existing` — new flag, default off (mirrors `[ingest] update_existing` config key).
- `llmwiki ingest --debug-updates` — new flag, default off. Prints per-candidate verdicts (LLM proposed, validator kept N quotes, body content_hash drift, etc.) to stderr. Useful when an `update_failed` line appears in the summary.
- `llmwiki status` gains `pages_updated_total` and `pages_update_failed_total` counters (read from `page_update_log`).
- `mcp.ingest` accepts new optional `update_existing: bool` argument.

## Risks

- **1. Update-pass validation drops previously-valid content (6b only).** This is the single biggest user-visible risk in sub-project 6. A user with 200 valid pages runs `ingest --update-existing` against a poorly-quoting source; the LLM proposes new bodies for 30 of them; for 8 of those, the new bodies' quotes don't substring-match either the new source or the original sources; those 8 pages are marked `update_failed` and **kept at their prior version**. This is the design — silent drop is forbidden — but the user must understand it. Mitigations:
    - Default-off (`update_existing = false`).
    - The `update_failed` lines in the summary are explicit: "kept at previous version".
    - `--debug-updates` flag for users who want per-page verdicts.
    - The `page_update_log` row records the prior `content_hash` so a sufficiently-motivated user can reconstruct what was almost-replaced.
    - Document in README *prominently* that pillar 3 is opt-in and that the validator continues to enforce the trust property.
- **2. Cost on cheap providers (6b only).** A typical ingest of, say, a 50-page repo today is one chunked ingest call per ~15KB chunk — call it 5–10 LLM calls. With `--update-existing` and a candidate cap of 50, that becomes 5–10 ingest calls + up to 50 update calls + up to 5 contradiction calls = up to 65 calls. Concrete worst-case bills:
    - **Gemini Flash (free):** 0 USD. Daily quota is 1500 requests/day (per `2026-01` AI Studio free-tier limits). 65 calls comfortably fits a single ingest with hundreds left over for `ask` later in the day.
    - **OpenRouter free tier (`meta-llama-3.1-8b-instruct:free`):** 0 USD. Daily quota typically 200 free requests/day. **65 calls is one-third of the daily budget per ingest.** Recommend Gemini Flash, not OpenRouter free, for `--update-existing` users. Document.
    - **Anthropic Haiku (`claude-haiku-4-5`):** at ~$1/$5 per Mtok in/out and ~3KB input + 2KB output per update call, ~$0.005 per call × 65 = ~$0.30 per `--update-existing` ingest. Tolerable for a power user, painful for a weekly ingest of 10 sources ($15/mo just for ingest). Recommend prompt-caching of the EXISTING-PAGE-EVIDENCE block across the per-source candidate fan-out — already wired for sub-project 1's chunked ingest, just extend the pattern.
    - **Ollama (local):** free, but a 7B model usually fails the structured-output schema; the validator will drop most updates. Pillar 3 with Ollama produces lots of `update_failed` lines and few successes. Document as "set update_existing=false on Ollama or expect almost no updates to land."
    - **Recommendation:** README's onboarding flow for `--update-existing` defaults to Gemini Flash. The existing sub-project 5 default of Gemini-on-init was chosen for unrelated reasons; here it pays off again.
- **3. Edit cycles / oscillation (6b only).** Source A is ingested, page P updates. Source B is ingested, page P updates again — and (because the LLM sees both A and B in EXISTING EVIDENCE) sometimes flips back toward A's framing. Source A is re-ingested (`--force`); P flips to A again. The oscillation pattern is real but slow (one cycle per ingest, not per call) and bounded — every step is validator-gated, so it can't introduce false content, only swap valid content for other valid content. Mitigations:
    - The single-step `content_hash` skip catches identity flips.
    - The `page_update_log` is an audit trail the user (or `lint`) can inspect to detect "this page has been updated 5 times in 3 days, something's wrong."
    - Full multi-step oscillation detection (sub-project 7+) is out of scope.
- **4. Performance of the retro-linker on very large wikis.** O(N×M) where N = existing pages, M = new titles. N=10000, M=20 → 200000 substring searches in Go: roughly 200ms in practice. Above N=50000 it becomes >1s; we add the FTS5 pre-filter at that scale. Spec defers the precise cutoff to plan-pass measurement.
- **5. Performance of cross-page candidate selection (6b).** FTS5 over `pages_fts` and `evidence_fts` is fast (<10ms) even on large wikis. The bottleneck is the per-candidate LLM call, mitigated by the candidate cap.
- **6. Contradiction-on-ingest false positives.** The LLM occasionally flags qualifications as contradictions ("page A says X applies in Go 1.21" vs "page B says X applies in Go 1.22" — same fact, different versions, no contradiction). Mitigations:
    - System prompt explicitly says "qualifications and additions are NOT contradictions."
    - The contradictions queue is informational, not blocking. False positives are noise the user can ignore.
    - `lint`'s post-hoc batcher uses the same prompt and has the same false-positive rate today; no regression.
- **7. Answer promotion on a stale answer.** The user runs `ask` today; six months later runs `promote` on the saved answer. In the interim, the source files have changed; some quotes no longer substring-match. Behaviour: defensive `ValidateAndAttachEvidence` drops the unmatching quotes; if no quotes survive, `promote` exits with the same `evidence_invalid` error `mcp.write_page` returns. The user re-runs the `ask` against the current wiki, gets a fresh answer, promotes that. **No promote-with-stale-evidence path exists.**
- **8. MCP write_page interaction with cross-page updates.** Today, `mcp.write_page` writes one page atomically with its own evidence. Sub-project 6b's `mcp.ingest` writes N new pages + updates M existing ones in one call. If the MCP client cancels mid-call (the user clicks stop in Claude Desktop), some of the M updates may have landed and some not. This is *also* true of today's `mcp.ingest` writing N new pages with partial completion — we accept the same semantics. The `page_update_log` records each completed update individually; partial completion is recoverable from the log.
- **9. `--update-existing` interaction with `--force`.** Today, `--force` bypasses the whole-source-hash skip. With `--update-existing --force`, every file in an unchanged source still flows into update-pass candidate selection — the user has explicitly asked for it. Existing `--force` semantics unchanged.
- **10. Obsidian wikilink rewriting interacting with cross-page updates.** When pillar 3 rewrites a page body, then pillar 2 wikilink-rewrites the same body in the next post-write step, the wikilink rewrite is body-only and idempotent so this is safe. The order is: (1) write new pages, (2) run pillar 3 update pass over candidates with their pre-update bodies (LLM-emitted bodies might have wikilinks already, since the system prompt mentions them), (3) post-pass, run the retro-linker over (existing + updated) pages with the new title set. Plan-pass refines the exact ordering.

## Open questions

These need user resolution before plan-pass. Most reflect reversible defaults; #1 and #11 are the load-bearing ones.

1. **Split into 6a + 6b (recommended) or one big v1.2 (alternative)?** The spec assumes split. The user may prefer "all four pillars as v1.2 gated behind a default-off `--update-existing`" if the soak window for 6b is acceptable.
2. **Default model for the contradiction-on-ingest call.** Use `cfg.LLM.Model` (whatever the user picked at init, including Gemini Flash)? Or a fixed cheap model regardless of the user's primary provider? Spec assumes "use the configured provider; the user already opted into its cost." Confirm.
3. **Contradiction surface format.** Spec assumes inline ingest output + append to `<wikiDir>/contradictions.md`. Alternative: structured `db` rows. The file form is more Obsidian-friendly; rows would be queryable but unprintable from the CLI without a new command. Confirm file-form.
4. **Retro-linker scope on `mcp.write_page`.** Today, `mcp.write_page` runs the wikilink rewriter only over the *new* page's body (against existing titles). Should it also run the reverse — retro-link existing pages to the new title? Spec says yes, for consistency with `ingest` and `promote`. Confirm — adds ~50ms to every `mcp.write_page` call but matches user intuition.
5. **Cross-page update candidate cap.** Spec defaults to 20 per source, 50 per ingest. These are first-guess numbers. User may want to tune after seeing real ingests.
6. **Answer-promotion `--rewrite` default.** Spec says default off (verbatim body). Argument for on: wiki pages and answer-pages have different prose conventions; the rewrite produces better-shaped pages. Argument against (which won): one extra LLM call, plus the rewrite can fail validation; verbatim is predictable and the user can always re-edit in Obsidian. Confirm default off.
7. **Quote-floor exactly 2 for cross-page updates.** Spec uses `min(2, len(originalEvidence))`. Alternative: a fraction (e.g. half the original quote count). Two is a magic number; fraction-based has its own failure modes when originals had odd counts. Confirm constant.
8. **Schema migration to v4 in 6b — additive only?** Spec assumes yes. No `ALTER TABLE` on existing tables. Confirm.
9. **`page_update_log` retention.** Spec: never rotated, never truncated. At 100 ingests/year × 50 candidates × ~1 row each = 5000 rows/year. Even at 10×, this is fine for our target wiki sizes. Confirm no auto-truncation.
10. **MCP surface for cross-page updates.** Spec: extend `mcp.ingest` return shape; no new `mcp.update_pages_from_source` tool. Rationale: simpler surface, one tool to learn, the agent gets the full ingest+update result in one round-trip. The user's brief asked for this recommendation; confirm.
11. **`--update-existing` default in v1.3.** Spec: default off. The "living wiki" pitch *wants* it default on, but the cost and validator-interaction risks argue against. We can revisit in v1.4 once we have real-world numbers from opt-in users. Confirm default off in v1.3 with a documented "consider flipping in v1.4" note.

## Test strategy

### Pure unit tests (no LLM, no network)

- `internal/wiki/promote.go`:
  - `ParseSavedAnswer` round-trip with `FormatSavedAnswer` (the format is already deterministic — answer files written by sub-project 1's `cmd/ask.go:saveAnswer`).
  - `PromoteAnswer` happy path: synthetic answer file + synthetic source files in DB → page lands, evidence rows created, log entry appended.
  - Defensive re-validation: source file has been modified since the ask → `evidence_invalid` returned, no disk write.
  - Title collision: existing page exists → `title_exists` error returned.
- `internal/wiki/retrolink.go`:
  - Single new title, three existing pages mention it in bare prose → all three get rewritten.
  - Same call run a second time → no-op (idempotency).
  - Existing page already has `[[NewTitle]]` → no-op for that page.
  - Body inside fenced code block mentions `NewTitle` → not rewritten (inherits `RewriteBareReferencesAsWikilinks` semantics).
- `internal/wiki/contradict.go`:
  - Candidate selection with synthetic FTS hits — only existing pages whose titles or evidence overlap the new pages are returned, capped at `candidateLimit`.
  - Contradiction filtering: LLM-output quotes that don't appear in either page's evidence are dropped (validator-style behaviour, no LLM call needed in unit test — fixture the LLM response).
- `internal/wiki/update_existing.go` (6b):
  - Candidate selection: per-source FTS shortlist, global cap, dedup against `newPageTitles`.
  - Validator interaction: synthetic LLM output with 0 valid quotes → `update_failed`, page kept at prior version.
  - Quote floor: original page had 5 quotes, update produced 1 valid quote → `update_failed` (below `min(2, 5)`).
  - Quote floor: original had 1 quote, update produced 1 valid quote → kept (floor is `min(2, 1) = 1`).
  - content_hash skip: proposed body identical to current → `body_only` outcome, no disk write of body but `page_update_log` row recorded.
  - Concurrency: 5 candidates run with `sem` cap of 2; all complete; results map to right titles.

### Cassette tests (LLM, real)

Three new cassettes for 6a (v1.2), three more for 6b (v1.3). Same per-test-named pattern as sub-project 1's `internal/llm/testdata/cassettes/`.

- `TestPromoteAnswerHappyPath` — synthetic source ingested, `ask` to produce an answer (uses an existing cassette), `promote` the answer file. Asserts page lands with expected evidence.
- `TestPromoteAnswerStaleEvidence` — same setup, but mutate the source file between ask and promote so quotes no longer match. Asserts `evidence_invalid` error, no disk write.
- `TestRetroLinkAfterIngest` — pre-seed three existing pages mentioning "Mutex" in bare prose, ingest a synthetic source that produces a "Mutex" page. Assert all three existing pages now contain `[[Mutex]]` and their `content_hash` changed.
- `TestContradictionFlaggedOnIngest` — pre-seed an existing page claiming X with validated evidence; ingest a synthetic source that produces a page claiming ¬X with its own validated evidence. Assert `<wikiDir>/contradictions.md` contains a structured entry naming both pages.
- `TestUpdateExistingHappyPath` (6b) — pre-seed five existing pages with valid evidence; ingest a new source that overlaps three of them. Assert `pages_updated == 3`, two unchanged. Verify `page_update_log` rows.
- `TestUpdateExistingValidationDrop` (6b) — pre-seed a page with 5 valid quotes; ingest a source whose update proposal has 0 substring-matching quotes. Assert `update_failed`, page body unchanged on disk, `page_update_log` row with `outcome='failed'`.

All cassettes recorded once via `LLMWIKI_RECORD=1`; nightly cassette-refresh job (sub-project 4) keeps them current.

### Integration / smoke

- `make smoke` is updated to verify `index.md` no longer crashes when retro-linking runs against the smoke fixture's existing pages. Mostly mechanical.
- A new manual-only check: ingest the llmwiki repo itself with `--update-existing`, verify the cross-page-update pass produces sensible diffs on a few obvious target pages. Documented in CONTRIBUTING.md, not automated.

### CI

- Nightly cassette-refresh job runs all 6 new cassettes alongside sub-project 5's three. Total recurring API spend stays in single-digit-cents-per-day on Gemini Flash.

## Migration / backward compat

- **6a (v1.2):** no schema migration. Pre-v1.1 wikis upgrade silently. Pre-v1.1 pages without `tags`/`sources`/`created` frontmatter are still readable (sub-project 5's `ParsePage` handles their absence). The retro-linker may rewrite their bodies on the next ingest; their evidence rows are untouched. The contradiction-on-ingest pass is content-only and writes only to `<wikiDir>/contradictions.md`. Answer-promotion creates a new wiki page on disk and a new `pages` row; nothing touches existing rows.
- **6b (v1.3):** additive migration to v4 adds `page_update_log`. Pre-v4 wikis: `db.Open` runs the migration on first open. Idempotent (`CREATE TABLE IF NOT EXISTS`). Roll-forward only; no down-migration script (matches every prior migration). `--update-existing` is opt-in; pre-v1.3 ingests behave exactly as before.
- **Pages updated by pillar 3 in 6b** retain their original `content_hash`'d source attribution: the `pages.source_ids` JSON array gains the new source's ID alongside the original IDs. The `evidence` rows include both old and new source-backed quotes. Pages updated only via the retro-linker (body-only) keep their `source_ids` unchanged.
- **Existing answer files in `.llmwiki/answers/`** parse fine with the new `wiki.ParseSavedAnswer` (introduced in 6a). The format hasn't changed since sub-project 1; we're just adding the inverse parser.
- **Existing MCP clients** continue to work: `mcp.ingest`'s return shape gains new keys but no key is renamed or removed. Clients ignoring unknown keys (the JSON-RPC default) are unaffected. Clients that explicitly destructure `pages_written` continue to work.

## Implementation order

Plan-pass refines. Two distinct shippable cycles.

### v1.2 (6a) — "the wiki notices things"

1. **`internal/wiki/promote.go`** — `ParseSavedAnswer` (inverse of `FormatSavedAnswer`), `PromoteAnswer` core. Pure unit tests.
2. **`cmd/promote.go`** — cobra command, flag wiring, slug/file resolution. Pure unit tests for slug-resolution.
3. **`internal/mcp/handlers.go`** — `promote_answer` handler + tool registration in `internal/mcp/server.go`. Pure unit test of the handler shape.
4. **`internal/wiki/retrolink.go`** — `RetroLinkPages`. Pure unit tests including the "FTS pre-filter at N>500" path (gated behind a counter, easy to unit-test by setting the threshold low).
5. **Wire retro-linker into ingest, promote, and `mcp.write_page`** — three call sites in `internal/wiki/ingest_runner.go`, `internal/wiki/promote.go`, `internal/mcp/handlers.go:writePageHandler`.
6. **`internal/wiki/contradict.go`** — `DetectIngestContradictions`. Pure unit tests for candidate selection and output filtering.
7. **Wire contradiction-on-ingest into `IngestSource`** — one call site, output appended to `<wikiDir>/contradictions.md`.
8. **Cassettes:** `TestPromoteAnswerHappyPath`, `TestPromoteAnswerStaleEvidence`, `TestRetroLinkAfterIngest`, `TestContradictionFlaggedOnIngest`. Record once.
9. **CHANGELOG entry** for 1.2.0 covering 6a's three pillars.
10. **README updates** — the Living Wiki section. Lead with answer-promotion (most user-visible), then contradictions, then retro-linker (mostly invisible — Obsidian graph view is the surface).
11. **Tag `v1.2.0-rc.1`.** Promote to `v1.2.0` after a 1-week stability window.

### v1.3 (6b) — "the wiki updates itself"

12. **Schema migration to v4** (`internal/db/db.go`) — adds `page_update_log`. Pure unit test against a fresh DB and a v3 DB.
13. **`db.DeleteEvidenceForPage`, `db.InsertPageUpdateLog`, `db.GetPageUpdateLog`** — three new queries in `internal/db/queries.go`. Pure unit tests.
14. **`internal/wiki/update_existing.go`** — `UpdateExistingPagesFromSource` core, including candidate selection, per-candidate LLM call, validator integration, quote floor, content_hash skip. Pure unit tests with synthetic LLM output fixtures.
15. **Wire update pass into `IngestSource`** — gated by `opts.UpdateExisting`, runs after the new-page write loop, before the retro-linker re-runs over the union of (new titles, updated titles).
16. **`cmd/ingest.go`** — new `--update-existing` and `--debug-updates` flags. Translate to `IngestOptions`.
17. **`cmd/status.go`** — read counters from `page_update_log`.
18. **`internal/mcp/handlers.go`** — `mcp.ingest` accepts `update_existing: bool`; return shape extended.
19. **Cassettes:** `TestUpdateExistingHappyPath`, `TestUpdateExistingValidationDrop`. Record once.
20. **CHANGELOG entry** for 1.3.0 covering pillar 3 and the upgraded contradiction-flow ("contradiction-on-ingest is now also a candidate trigger for the update pass when `--update-existing` is set").
21. **README updates** — `--update-existing` is documented prominently, with the cost-and-validator caveat.
22. **Tag `v1.3.0-rc.1`.** Promote to `v1.3.0` after a 2-week stability window (longer than v1.2 because pillar 3's failure modes need a longer soak).

## Verification

```bash
# === v1.2 (6a) ===

# Answer promotion — happy path
mkdir my-wiki && cd my-wiki
export GEMINI_API_KEY=...
llmwiki init
llmwiki ingest ./internal/
llmwiki ask "what does the validator do?"
ls .llmwiki/answers/
# Expect: one timestamped .md file
llmwiki promote .llmwiki/answers/2026-05-04-150208-what-does-the-validator-do.md \
                --title "Validator Internals"
# Expect: "wrote page Validator Internals (N evidence, M sources)"
ls .llmwiki/wiki/ | grep "Validator Internals"
# Expect: file exists

# Answer promotion — stale evidence
echo "// totally rewritten" > internal/wiki/ops.go     # break the source
llmwiki promote .llmwiki/answers/2026-05-04-150208-what-does-the-validator-do.md
# Expect: error "evidence_invalid", structured payload listing dropped quotes,
# no new page on disk.

# Retro-linker
llmwiki ingest ./internal/sync/mutex.go
# Expect: "Retro-linked N existing page(s) that now reference [[Mutex Implementation]]"
grep -l "\[\[Mutex Implementation\]\]" .llmwiki/wiki/*.md
# Expect: at least the new page + N older pages now contain the wikilink.

# Contradiction-on-ingest
llmwiki ingest ./test/fixtures/contradictory-source.md
# Expect: "!! N contradiction(s) flagged" inline, plus
cat .llmwiki/contradictions.md
# Expect: structured entries with timestamp, both quote sides, file:lines on each.

# MCP — promote_answer
llmwiki mcp < /dev/null
# Expect: clean exit (no client speaking).
go test ./internal/mcp/... -run TestMCPPromoteAnswerRoundtrip
# Expect: pass.

# === v1.3 (6b) ===

# Cross-page update — happy path
llmwiki ingest ./CHANGELOG.md --update-existing
# Expect: "Scanning N candidate page(s) for updates...", "M page(s) updated"
# section; for each updated page, the diff is visible in `git diff` if
# .llmwiki/wiki/ is git-tracked.
llmwiki status
# Expect: pages_updated_total: M, pages_update_failed_total: K

# Cross-page update — validation drop
llmwiki ingest ./test/fixtures/poorly-quoting-source.md --update-existing
# Expect: "K page(s) update FAILED — kept at previous version" section,
# each line citing the failure reason.
sqlite3 .llmwiki/wiki.db "SELECT title, outcome, reason FROM page_update_log
                         JOIN pages ON pages.id = page_update_log.page_id
                         WHERE outcome IN ('failed','skipped')
                         ORDER BY created_at DESC LIMIT 10"
# Expect: rows with reason populated.

# MCP — ingest with update_existing
go test ./internal/mcp/... -run TestMCPIngestUpdateExisting
# Expect: pass.

# Tests
go test ./...
# Expect: green in replay mode, all 6 new cassettes pass.
```
