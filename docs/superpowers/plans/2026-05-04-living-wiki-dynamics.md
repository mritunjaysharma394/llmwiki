# Sub-project 6a — Living Wiki Dynamics (v1.2) — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship v1.2 of `llmwiki` — three additive, validator-respecting "the wiki notices things" features layered on top of v1.1.0's surface: answer-promotion (`llmwiki promote` + `mcp.promote_answer`) lifts a `.llmwiki/answers/<ts>-<slug>.md` file into a real wiki page through the same trust validator that gates `mcp.write_page`; the retro-linker rewrites `[[wikilinks]]` into existing pages whenever a new title lands (post-`ingest`, post-`promote`, post-`mcp.write_page`); and contradiction-on-ingest surfaces "this new page conflicts with an existing one" inline plus an append to `<wikiDir>/contradictions.md` while context is hot. **6b's cross-page page-update pass and `--update-existing` flag are out of scope for this plan** and ship later as v1.3.

**Architecture:** Three new files under `internal/wiki/` — `promote.go` (with `ParseSavedAnswer` as the inverse of the existing `wiki.FormatSavedAnswer`, and `PromoteAnswer` orchestrating defensive re-validation through `wiki.ValidateAndAttachEvidence`), `retrolink.go` (`RetroLinkPages` reusing `wiki.RewriteBareReferencesAsWikilinks` over an N-existing-pages × M-new-titles input set, body-only and idempotent), and `contradict.go` (`DetectIngestContradictions` taking new pages + existing-page candidates from FTS, returning structured `Contradiction` tuples). One new cobra command `cmd/promote.go` and one new MCP tool `mcp.promote_answer`; the `mcp.ingest` return shape gains two integers (`contradictions_flagged`, `retro_linked_pages`). Three call sites in `internal/wiki/ingest_runner.go` (post-write retro-link), `internal/wiki/promote.go` (post-write retro-link), and `internal/mcp/handlers.go:writePageHandler` (post-write retro-link). Contradiction-on-ingest hooks once into `IngestSource` after the persist loop. **No schema changes** (`PRAGMA user_version` stays at 3).

**Tech Stack:** Go 1.26. **No new direct dependencies.** All new code reuses `mark3labs/mcp-go v0.50.0` (already pinned by sub-project 5), the existing `*sql.DB` over `mattn/go-sqlite3`, and the configured `llm.Client` for the contradiction-detection LLM call. The contradiction call uses `cfg.LLM.Model` — whatever provider the user configured at `init` time, including Gemini Flash.

**Spec:** [`docs/superpowers/specs/2026-05-04-living-wiki-dynamics-design.md`](../specs/2026-05-04-living-wiki-dynamics-design.md)

**Resolved open questions** (the spec lists eleven; six are 6b-only and stay open until v1.3 plan-pass; the five that touch 6a are resolved here so the implementer is unblocked):

1. **Q1 — Split:** **6a as v1.2, 6b as v1.3.** This plan covers 6a only — pillars 1 (warn-only), 2, and 4. Pillar 3 (cross-page page-update) ships in v1.3 against a separate plan.
2. **Q2 — Default model for the contradiction-on-ingest call:** **`cfg.LLM.Model`** — whatever the user configured at `init`. The user already opted into that provider's cost picture; the contradiction call inherits it. Gemini Flash users pay nothing; Anthropic users pay typical-Haiku rates. The spec walks through cost in §Risks #2; we surface it once in the README's Living Wiki section, not per-invocation.
3. **Q3 — Contradiction surface format:** **file form `<wikiDir>/contradictions.md` + inline ingest output.** Spec-default. No DB rows. File is append-only, RFC3339-prefixed, mirrors `log.md`'s shape so Obsidian renders it and other tooling can grep it.
4. **Q4 — Retro-linker scope on `mcp.write_page`:** **yes, retro-link existing pages to the new title** (in addition to the existing rewrite over the new body against existing titles). Adds ~50ms to every `mcp.write_page` call but matches `ingest`'s and `promote`'s behaviour and matches user intuition that "writing a new page makes it linkable everywhere."
5. **Q6 — `llmwiki promote --rewrite` default:** **off (verbatim body).** One fewer LLM call, predictable output, the answer was already written for human consumption. `--rewrite` opt-in for users who want wiki-style prose.

Open questions 5, 7, 8, 9, 10, 11 are 6b-only and not relevant to this plan.

---

## File Structure

| File | Purpose | Action |
|---|---|---|
| `internal/wiki/promote.go` | `ParseSavedAnswer` (inverse of `FormatSavedAnswer`) + `PromoteAnswer` core + `PromoteOptions` / `PromoteResult` | Create |
| `internal/wiki/promote_test.go` | Round-trip parser; `PromoteAnswer` happy path; defensive re-validation drops stale evidence; title collision; `--rewrite` flag fallback | Create |
| `internal/wiki/retrolink.go` | `RetroLinkPages` + `RetroLinkResult` over `db.AllPages()` × new-titles, body-only via `RewriteBareReferencesAsWikilinks` | Create |
| `internal/wiki/retrolink_test.go` | Idempotency, body-only, code-fence skip inherited from `RewriteBareReferencesAsWikilinks`, content_hash + `updated_at` recompute, FTS pre-filter at threshold | Create |
| `internal/wiki/contradict.go` | `DetectIngestContradictions` + `Contradiction` struct + candidate selection over `SearchPages`/`SearchEvidence` | Create |
| `internal/wiki/contradict_test.go` | Candidate selection, contradiction filtering (drops hallucinated quotes), output-formatter for `contradictions.md` | Create |
| `internal/wiki/ingest_runner.go` | Wire retro-linker (post-persist loop) + contradiction detection (post-persist loop, before retro-link) into `IngestSource`; extend `IngestRunResult` with `RetroLinkedPages` and `ContradictionsFlagged` | Modify |
| `cmd/promote.go` | Cobra wrapper: resolve answer-file/slug/abspath, translate `--title`/`--rewrite`/`--no-save` to `PromoteOptions`, delegate to `wiki.PromoteAnswer` | Create |
| `cmd/promote_test.go` | Slug/file/abspath resolution; flag wiring; happy-path through `runPromote` against a synthetic answer file + DB | Create |
| `cmd/root.go` | `rootCmd.AddCommand(promoteCmd)` (one line; no config-block changes — 6a adds no config keys) | Modify |
| `internal/mcp/server.go` | Register `promote_answer` tool; bump `serverVersion` to `1.2.0` | Modify |
| `internal/mcp/handlers.go` | `promoteAnswerHandler` (calls `wiki.PromoteAnswer`); `writePageHandler` runs `RetroLinkPages` after the persist block; `ingestHandler` extends return shape with `contradictions_flagged` + `retro_linked_pages` | Modify |
| `internal/mcp/server_test.go` | `TestPromoteAnswer_HappyPath`, `TestPromoteAnswer_StaleEvidence`, `TestPromoteAnswer_TitleCollision`, `TestWritePage_RetroLinksExistingPages`, `TestIngest_ReturnShapeIncludesNewCounters` | Modify |
| `internal/llm/testdata/cassettes/TestPromoteAnswerHappyPath__*.json` | Recorded cassette for 6a's full `init→ingest→ask→promote` loop | Create |
| `internal/llm/testdata/cassettes/TestPromoteAnswerStaleEvidence__*.json` | Recorded cassette: source mutated between ask and promote → `evidence_invalid` | Create |
| `internal/llm/testdata/cassettes/TestRetroLinkAfterIngest__*.json` | Recorded cassette: pre-seed three pages mentioning "Mutex" in prose, ingest a Mutex page, assert all three get `[[Mutex]]` | Create |
| `internal/llm/testdata/cassettes/TestContradictionFlaggedOnIngest__*.json` | Recorded cassette: pre-seed page claiming X, ingest source claiming ¬X, assert `contradictions.md` entry | Create |
| `internal/llm/testdata/cassettes/TestMCPPromoteAnswerRoundtrip__*.json` | Recorded cassette: full MCP-driven `ingest→ask→promote_answer→read_page` roundtrip | Create |
| `cmd/promote_integration_test.go` | Cassette test `TestPromoteAnswerHappyPath` and `TestPromoteAnswerStaleEvidence` | Create |
| `cmd/ingest_integration_test.go` | Append `TestRetroLinkAfterIngest` and `TestContradictionFlaggedOnIngest` cassette tests | Modify |
| `internal/mcp/integration_test.go` | Append `TestMCPPromoteAnswerRoundtrip` | Modify |
| `README.md` | New "Living Wiki" section: lead with `promote`, then contradictions, then retro-linker; one-paragraph cost note pointing at Gemini Flash for free contradiction calls | Modify |
| `CHANGELOG.md` | `## [1.2.0] — 2026-05-04` entry covering 6a's three pillars; explicit "no schema migration" note | Modify |
| `internal/mcp/server.go` | `serverVersion = "1.2.0"` (already listed above) | Modify |
| (tag) | `v1.2.0-rc.1` annotated tag | Create |

**Total:** 16 tasks across 8 phases (A–H). Each task ends with a single commit; the working tree is green at every commit boundary (`go build ./... && go test ./...` clean in replay mode).

---

## Phase summaries

Each phase below is self-contained: it does not depend on later-phase exports, and its last task leaves the tree compiling and `go test ./...` green so a fresh subagent can pick up the next phase from a clean checkout. Every task that writes a page to disk reaffirms the trust property — no page reaches disk without ≥1 evidence quote that substring-matches its named source file.

- **Phase A — Saved-answer parser (Tasks 1).** Add `wiki.ParseSavedAnswer` as the deterministic inverse of `wiki.FormatSavedAnswer` (`internal/wiki/answer.go`). Pure unit test: round-trip a constructed `SavedAnswerInput` through Format then Parse and compare. Self-contained; no callers yet. Risk: the format has whitespace and line-prefix conventions (`> "quote"  (path:a-b)`) the parser must match exactly — fixture-driven tests cover both Format-then-Parse and a hand-authored fixture from a real `cmd/ask.go:saveAnswer` output.
- **Phase B — `PromoteAnswer` core + `cmd/promote.go` (Tasks 2–3).** `wiki.PromoteAnswer` reads an answer file, looks up source files via the same `byPath` pattern `internal/mcp/handlers.go:writePageHandler` uses, runs `wiki.ValidateAndAttachEvidence` defensively over the parsed quotes, and on success runs the same disk-and-DB write path the MCP handler uses. `cmd/promote.go` is a thin cobra wrapper. The trust property holds: defensive re-validation rejects answers whose source files have changed since the ask. Risk: source files referenced in the answer may live under any of `<RawDir>/<sourceURI>/<rel-path>` patterns or be the source URI itself — reuse `internal/mcp/handlers.go:readSourceFileContent`.
- **Phase C — `RetroLinkPages` (Task 4).** Body-only, idempotent rewriter that walks `db.AllPages()` minus the just-written titles and runs `RewriteBareReferencesAsWikilinks` over each body using only the new titles as the substitution alphabet. Pages whose body changed get `content_hash` and `updated_at` recomputed and persisted via `WritePage` + `db.UpsertPage`. Evidence rows are untouched (the rewriter is body-only). Risk: at N=10000 existing pages × M=20 new titles the naive scan is ~200ms — acceptable; we add an FTS5 pre-filter only at N>500 (gated by a small constant the test can lower). The `RewriteBareReferencesAsWikilinks` helper from sub-project 5 already handles fences/backticks/idempotency.
- **Phase D — Wire retro-linker into ingest, promote, MCP write_page (Task 5).** Three call sites: `IngestSource`'s post-persist tail (just before `RegenerateIndex`), `PromoteAnswer`'s post-write tail, and `writePageHandler`'s post-write tail. Each picks up the new titles emitted by its own write step and runs `RetroLinkPages` over the union of (existing pages minus new titles). The MCP `ingest` return shape gains `retro_linked_pages: int`. Risk: ordering — retro-link must run AFTER the new pages have been persisted (so existing pages refer to a real on-disk neighbour) and BEFORE `RegenerateIndex` (so `index.md` reflects the updated `updated_at` on rewritten existing pages).
- **Phase E — `DetectIngestContradictions` + wiring (Tasks 6–7).** `wiki.DetectIngestContradictions` builds a per-new-page candidate shortlist via `db.SearchPages` and `db.SearchEvidence` (capped at `candidateLimit`, default 5), runs one structured LLM call per (new page, candidate) pair against `cfg.LLM.Model`, filters out LLM-hallucinated quotes that don't match either page's already-validated evidence, and returns deduped `[]Contradiction`. The wiring step into `IngestSource` runs it after the persist loop and after the retro-linker, formats the inline summary, and appends to `<wikiDir>/contradictions.md`. LLM/timeout failures log a WARN to stderr and never fail the ingest — contradiction detection is informational. The `mcp.ingest` return shape gains `contradictions_flagged: int`. Risk: false positives (qualifications flagged as contradictions); spec mitigation is the system-prompt directive plus the validator-style filter that drops hallucinated quotes.
- **Phase F — MCP `promote_answer` + return-shape extensions (Tasks 8–9).** Register `promote_answer` tool in `internal/mcp/server.go`; implement `promoteAnswerHandler` in `internal/mcp/handlers.go` translating MCP args to `wiki.PromoteOptions` and rendering a structured success / structured error. Bump `serverVersion` to `"1.2.0"`. Update `ingestHandler` to surface the two new counters from `IngestRunResult`. Risk: `mcp-go` v0.50.0 schema for nested optional inputs — same shape as `write_page`'s schema in `server.go`.
- **Phase G — Cassettes (Tasks 10–14).** Five new cassettes recorded once via `LLMWIKI_RECORD=1`, replayed deterministically in CI: `TestPromoteAnswerHappyPath`, `TestPromoteAnswerStaleEvidence`, `TestRetroLinkAfterIngest`, `TestContradictionFlaggedOnIngest`, `TestMCPPromoteAnswerRoundtrip`. Risk: the contradiction call uses whichever provider is configured at record time — pick Gemini Flash so cassette refresh stays free.
- **Phase H — Docs + tag (Tasks 15–16).** New "Living Wiki" section in README leading with `promote`, then contradictions, then retro-linker; one-paragraph cost note pointing at Gemini Flash for free contradiction calls. CHANGELOG `[1.2.0]` entry covering all three 6a pillars with explicit "no schema migration" note. Tag `v1.2.0-rc.1` locally (no push). Risk: README must not promise 6b features — the section is explicit that "the wiki updates itself when you ingest contradicting sources" is v1.3.

---

## Phase A — Saved-answer parser

### Task 1: `wiki.ParseSavedAnswer` — deterministic inverse of `FormatSavedAnswer`

**Files:**
- Modify: `internal/wiki/answer.go` (add `ParseSavedAnswer` + `ParsedSavedAnswer` struct)
- Create: `internal/wiki/answer_test.go` (or modify if it exists)

- [ ] **Step 1: Write failing tests**

Append to `internal/wiki/answer_test.go`:

1. `TestParseSavedAnswer_RoundTrip` — build a `wiki.SavedAnswerInput` with question, answer, model, timestamp, and two `wiki.Page`s (each with one `wiki.Evidence` carrying `Quote`, `LineStart`, `LineEnd`, `SourceFilePath`); call `wiki.FormatSavedAnswer(in)`, then `wiki.ParseSavedAnswer(formatted)`. Assert every field round-trips byte-for-byte (timestamp parsed back via `time.RFC3339`, page titles, evidence quote / source_file / line range).
2. `TestParseSavedAnswer_FromHandAuthoredFixture` — feed the parser a hand-authored fixture mirroring exactly what `cmd/ask.go:saveAnswer` writes today: `---\nquestion: how does the validator work?\ncreated_at: 2026-05-04T15:02:08Z\nmodel: gemini-2.0-flash\n---\n\n# Answer\n\nThe validator drops...\n\n## Sources\n\n**[1] Validator Internals**\n\n> "every quote must substring-match"  (internal/wiki/ops.go:215-215)\n\n`. Assert the parsed shape carries all four bullets and the source file path is `internal/wiki/ops.go` with line range `(215, 215)`.
3. `TestParseSavedAnswer_NonRFC3339TimestampReturnsError` — feed `created_at: not-a-date`, assert error wrapping `time.Parse` failure.
4. `TestParseSavedAnswer_LegacyLineAnnotation` — older answers used `(lines a-b)` with no source file (pre-sub-project-3); assert the parser tolerates this form by leaving `SourceFilePath` empty and populating `LineStart`/`LineEnd`. Backward-compat guard.
5. `TestParseSavedAnswer_IgnoresExtraFrontmatterKeys` — fixture with an extra `experimental_key: foo` line in frontmatter; assert successful parse and the known fields populate correctly.

- [ ] **Step 2: Run tests and confirm failure**

Run: `go test ./internal/wiki/ -run TestParseSavedAnswer -v`
Expected: FAIL — `ParseSavedAnswer` does not exist.

- [ ] **Step 3: Implement `wiki.ParseSavedAnswer`**

Add to `internal/wiki/answer.go`:

```go
type ParsedSavedAnswer struct {
    Question  string
    Answer    string
    Model     string
    CreatedAt time.Time
    Pages     []Page
}

// ParseSavedAnswer is the inverse of FormatSavedAnswer. It splits the
// frontmatter, the "# Answer" body, and the "## Sources" section, and
// reconstructs the SavedAnswerInput shape (minus the redundant At, which
// is read from frontmatter as CreatedAt).
//
// The parser is line-oriented and tolerant: extra frontmatter keys are
// ignored; legacy "(lines a-b)" annotations are accepted alongside the
// "(<path>:a-b)" form sub-project 3 introduced.
func ParseSavedAnswer(content string) (ParsedSavedAnswer, error) {
    // 1. Strip leading/trailing whitespace; require leading "---\n".
    // 2. Parse frontmatter line-by-line until closing "---\n":
    //    question: ..., created_at: ..., model: ...
    // 3. Find "# Answer\n\n" anchor; the answer body runs until "\n## Sources\n".
    // 4. Each source block starts with "**[<n>] <title>**\n\n" and contains
    //    one or more "> \"quote\"  (annotation)\n\n" lines. The annotation
    //    is either "<path>:<a>-<b>" or "lines <a>-<b>".
    // 5. Build []Page where each Page has Title and Evidence (no Body — the
    //    answer file doesn't carry per-page bodies).
}
```

The annotation regex is `^([^:]+):(\d+)-(\d+)$` for the path-form and `^lines (\d+)-(\d+)$` for the legacy form. The quote-extractor strips the leading `> ` and the surrounding quotes via `strconv.Unquote` (the format quotes via `%q`).

- [ ] **Step 4: Run tests and confirm pass**

Run: `go test ./internal/wiki/ -run TestParseSavedAnswer -v`
Expected: PASS — five subtests green.

Run: `go test ./...`
Expected: green (no callers yet; pure addition).

- [ ] **Step 5: Commit**

```bash
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
feat(wiki): ParseSavedAnswer — deterministic inverse of FormatSavedAnswer

Reads a .llmwiki/answers/<ts>-<slug>.md file as written by cmd/ask.go's
saveAnswer and reconstructs the SavedAnswerInput shape: question,
created_at, model, answer text, plus a []Page where each Page carries
Title and Evidence. Tolerates extra frontmatter keys and the legacy
"(lines a-b)" annotation form (pre sub-project 3). Exists so Phase B's
PromoteAnswer can defensively re-validate every quote against its named
source_file before lifting the answer into a real wiki page.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase B — `PromoteAnswer` core + `cmd/promote.go`

### Task 2: `wiki.PromoteAnswer` — defensive re-validation + disk-and-DB write

**Files:**
- Create: `internal/wiki/promote.go`
- Create: `internal/wiki/promote_test.go`

- [ ] **Step 1: Write failing tests**

Create `internal/wiki/promote_test.go`:

1. `TestPromoteAnswer_HappyPath` — set up an in-memory `*db.DB`, ingest a synthetic source whose content includes the literal substring `"the validator drops unverified quotes"`, hand-author a `.llmwiki/answers/<ts>-foo.md` file whose Sources section quotes that substring with `(<path>:1-1)`. Call `wiki.PromoteAnswer(ctx, cfg, db, client, answerPath, PromoteOptions{Title: "Validator Internals"})`. Assert: page lands at `<wikiDir>/Validator Internals.md`, page row in DB, evidence rows linked to the original `source_file_id`, log.md got a `**promote**` line. Verify the trust property: the on-disk page's evidence quote is byte-identical to a substring of the source file.
2. `TestPromoteAnswer_StaleEvidenceReturnsErrEvidenceInvalid` — same setup, then mutate the source file on disk between answer-write and promote so the substring no longer matches. Assert `wiki.PromoteAnswer` returns an `ErrEvidenceInvalid` whose payload lists the dropped quote and reason. Assert NO disk write and NO `log.md` entry.
3. `TestPromoteAnswer_TitleCollisionReturnsErrTitleExists` — pre-seed a page titled `"Validator Internals"`, then call `PromoteAnswer` with `--title "Validator Internals"`. Assert `ErrTitleExists` returned with `existing_path` populated.
4. `TestPromoteAnswer_DefaultTitleFromQuestion` — answer's frontmatter has `question: how does the validator work?`; call `PromoteAnswer` with `Title=""`. Assert resulting page title is the slugify-then-Title-Case form of the question (e.g. `"How Does The Validator Work"`).
5. `TestPromoteAnswer_RewriteFlagFallsBackOnValidationFailure` — `--rewrite` causes one extra LLM call; if the rewritten body's quotes can't all be re-validated against the answer's parsed quotes, fall back to verbatim body and log a WARN. Assert page lands with verbatim body and a stderr line containing `"WARN rewrite produced unverifiable body"`.
6. `TestPromoteAnswer_NoSaveSkipsLogEntry` — `PromoteOptions.NoSave = true`; assert page lands but `log.md` is unchanged.
7. `TestPromoteAnswer_MissingAnswerFileReturnsError` — `answerPath = "/nonexistent.md"`; assert error wrapping `os.ErrNotExist`.

The fixture writes a Page row (`db.UpsertPage`) for the existing source file's evidence so the lookup-by-path resolution can find it, mirroring how `mcp.write_page` resolves source files.

- [ ] **Step 2: Run tests and confirm failure**

Run: `go test ./internal/wiki/ -run TestPromoteAnswer -v`
Expected: FAIL.

- [ ] **Step 3: Implement `internal/wiki/promote.go`**

```go
package wiki

import (
    "context"
    "errors"
    "fmt"
    "os"
    "path/filepath"
    "strings"
    "time"

    "github.com/mritunjaysharma394/llmwiki/internal/db"
    "github.com/mritunjaysharma394/llmwiki/internal/ingest"
    "github.com/mritunjaysharma394/llmwiki/internal/llm"
)

// ErrEvidenceInvalid is returned when defensive re-validation of an answer's
// quotes drops every quote (typically because source files changed between
// ask-time and promote-time). The error carries a structured payload with
// the dropped quotes and the reason for each.
var ErrEvidenceInvalid = errors.New("evidence_invalid")

// ErrTitleExists is returned when the promoted page would collide with a
// pre-existing page title. Mirrors mcp.write_page's title_exists code.
var ErrTitleExists = errors.New("title_exists")

type PromoteOptions struct {
    Title   string  // optional override; "" derives from answer's question via slugify-then-Title-Case
    Rewrite bool    // when true, run one LLM call to rewrite answer body into wiki prose; falls back on validation failure
    NoSave  bool    // when true, skip the log.md entry (debug only)
}

type PromoteResult struct {
    Title           string
    Path            string
    EvidenceQuotes  int
    DroppedQuotes   []DroppedQuote
    RewriteApplied  bool
    RetroLinkedTitles []string // populated by Phase D; empty in standalone PromoteAnswer
}

type DroppedQuote struct {
    Quote      string
    SourceFile string
    Reason     string
}

func PromoteAnswer(ctx context.Context, cfg IngestSourceConfig, database *db.DB, client llm.Client, answerPath string, opts PromoteOptions) (PromoteResult, error) {
    // 1. Read + parse the answer file.
    raw, err := os.ReadFile(answerPath)
    if err != nil { return PromoteResult{}, fmt.Errorf("reading answer: %w", err) }
    parsed, err := ParseSavedAnswer(string(raw))
    if err != nil { return PromoteResult{}, fmt.Errorf("parsing answer: %w", err) }

    // 2. Resolve title.
    title := opts.Title
    if title == "" { title = titleFromQuestion(parsed.Question) }

    // 3. Title collision check.
    if existing, _ := database.GetPage(title); existing != nil {
        return PromoteResult{Title: title, Path: existing.Path}, ErrTitleExists
    }

    // 4. Build []ingest.SourceFile from every distinct source_file referenced
    //    in parsed evidence. Look up each via byPath lookup mirroring
    //    internal/mcp/handlers.go:writePageHandler. Read content via
    //    readSourceFileContent (lifted to internal/wiki for reuse).
    // 5. Build candidate Page{Title, Body, Evidence (parsed)}.
    // 6. Run wiki.ValidateAndAttachEvidence([]Page{candidate}, files).
    // 7. If validation drops every quote: return ErrEvidenceInvalid wrapped
    //    with DroppedQuote payload (computed by walking the inputs and
    //    seeing which quotes failed strings.Contains against their files).
    // 8. (Optional) opts.Rewrite: one LLM call ingestSystemPrompt-style,
    //    re-validate; on failure log WARN and use verbatim body.
    // 9. Stamp tags=["llmwiki","promote"], sources=distinct, created=now,
    //    UpdatedAt=now, ContentHash=HashContent(body).
    //10. Wikilink rewrite over (existing titles + this title).
    //11. WritePage + UpsertPage + InsertEvidence + UpsertLinks (mirrors
    //    cmd/ingest's persist loop).
    //12. Append log.md unless opts.NoSave: kind="promote", payload=
    //    fmt.Sprintf("%s → %d evidence", title, len(page.Evidence)).
    return PromoteResult{...}, nil
}

// titleFromQuestion turns "how does the validator work?" into "How Does The Validator Work".
func titleFromQuestion(q string) string { /* slugify + Title-Case */ }
```

The `readSourceFileContent` helper currently lives in `internal/mcp/handlers.go`. To avoid `internal/wiki` importing `internal/mcp` (which would create a cycle: handlers.go already imports `internal/wiki`), **extract it to `internal/wiki/promote.go`** as an exported lowercase helper and have the MCP handler call the new wiki location. This refactor is part of Task 2's commit.

- [ ] **Step 4: Run tests and confirm pass**

Run: `go test ./internal/wiki/ -run TestPromoteAnswer -v`
Expected: PASS — seven subtests green.

Run: `go test ./...`
Expected: green (the readSourceFileContent move is internal; MCP handler delegates to the new location).

- [ ] **Step 5: Commit**

```bash
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
feat(wiki): PromoteAnswer — defensively re-validates answer quotes before disk write

PromoteAnswer reads a .llmwiki/answers/<ts>-<slug>.md file via
ParseSavedAnswer, resolves every named source_file against the DB,
runs wiki.ValidateAndAttachEvidence over the parsed quotes, and on
success lands the result as a real wiki page through the same disk +
DB + log path that cmd/ingest and mcp.write_page use. Pages whose
quotes no longer substring-match (because the source changed since
the ask) get rejected via ErrEvidenceInvalid with a structured
DroppedQuote payload — no disk write, no log entry, never silently
downgrades the wiki. Title collisions return ErrTitleExists matching
mcp.write_page's title_exists code. --rewrite is opt-in and falls
back to verbatim body when the rewritten body fails validation.
readSourceFileContent moves from internal/mcp/handlers.go to here so
both consumers share one implementation.

Trust property reaffirmed: every page reaching disk via PromoteAnswer
has ≥1 evidence quote that substring-matches its source_file —
wiki.ValidateAndAttachEvidence is the single gatekeeper.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: `cmd/promote.go` — cobra wrapper + slug/file/abspath resolution

**Files:**
- Create: `cmd/promote.go`
- Create: `cmd/promote_test.go`
- Modify: `cmd/root.go` (add `rootCmd.AddCommand(promoteCmd)`)

- [ ] **Step 1: Write failing tests**

Create `cmd/promote_test.go`:

1. `TestPromote_ResolvesAbsolutePath` — `args[0]` is an absolute path to an existing answer file; assert `runPromote` reads it and delegates to `wiki.PromoteAnswer`.
2. `TestPromote_ResolvesBaseFilename` — `args[0]` is a bare filename like `2026-05-04-150208-foo.md`; assert it's resolved against `<wikiDir>/../answers/`.
3. `TestPromote_ResolvesSlug` — `args[0]` is a slug like `how-does-validator-work` with no extension; assert it matches the most recent `.llmwiki/answers/*-how-does-validator-work.md` file by glob.
4. `TestPromote_ResolvesSlug_AmbiguityErrors` — two answer files share the same slug; assert error names both candidates and asks the user to pass an explicit filename.
5. `TestPromote_TitleFlagOverrides` — `--title "Custom Title"` is passed through to `PromoteOptions.Title`.
6. `TestPromote_RewriteFlagOff` — assert default `--rewrite` is false.
7. `TestPromote_NoSaveFlag` — assert `--no-save` maps to `PromoteOptions.NoSave = true`.
8. `TestPromote_PrintsHumanReadableSummary` — capture stdout; on success print `wrote page "<title>" (<n> evidence, <m> sources)\nretro-linked <k> existing page(s)\nsaved: <path>`.

- [ ] **Step 2: Run tests and confirm failure**

Run: `go test ./cmd/ -run TestPromote -v`
Expected: FAIL.

- [ ] **Step 3: Implement `cmd/promote.go`**

```go
package cmd

import (
    "fmt"
    "os"
    "path/filepath"
    "strings"

    "github.com/mritunjaysharma394/llmwiki/internal/cliutil"
    "github.com/mritunjaysharma394/llmwiki/internal/wiki"
    "github.com/spf13/cobra"
)

var promoteCmd = &cobra.Command{
    Use:   "promote <answer-file-or-slug>",
    Short: "Promote a saved answer into a permanent wiki page",
    Long:  `Lift a .llmwiki/answers/<ts>-<slug>.md file into a real wiki page.

Defensive re-validation runs every evidence quote through the same
substring-match validator that gates cmd/ingest and mcp.write_page —
quotes whose source files have changed since the ask are rejected with
a structured error and no disk write happens.`,
    Args:  cobra.ExactArgs(1),
    RunE:  runPromote,
}

func init() {
    promoteCmd.Flags().String("title", "", "override title (defaults to Title-Cased question)")
    promoteCmd.Flags().Bool("rewrite", false, "LLM-rewrite the answer body into wiki prose (default off)")
    promoteCmd.Flags().Bool("no-save", false, "skip the log.md entry (debug only)")
}

func runPromote(cmd *cobra.Command, args []string) error {
    answerPath, err := resolveAnswerArg(args[0], cfg.Wiki.WikiDir)
    if err != nil {
        return cliutil.Wrap("resolving answer file", err,
            "pass an absolute path, a bare filename in .llmwiki/answers/, or a slug that matches one file")
    }
    title, _ := cmd.Flags().GetString("title")
    rewrite, _ := cmd.Flags().GetBool("rewrite")
    noSave, _ := cmd.Flags().GetBool("no-save")

    res, err := wiki.PromoteAnswer(cmd.Context(), toWikiIngestConfig(cfg), database, llmClient, answerPath, wiki.PromoteOptions{
        Title: title, Rewrite: rewrite, NoSave: noSave,
    })
    if err != nil {
        // Render ErrEvidenceInvalid / ErrTitleExists with cliutil.Wrap so
        // the user gets a structured remediation hint mirroring mcp.write_page's
        // structured-error payload semantics.
        switch {
        case errors.Is(err, wiki.ErrEvidenceInvalid):
            return cliutil.Wrap("evidence_invalid: defensive re-validation dropped every quote", err,
                "the source files referenced by this answer have changed since the ask; re-run 'llmwiki ask <question>' against the current wiki and promote the fresh answer")
        case errors.Is(err, wiki.ErrTitleExists):
            return cliutil.Wrap(fmt.Sprintf("title_exists: %q is taken (at %s)", res.Title, res.Path), err,
                "pass --title with a different title, or supersede manually in Obsidian")
        default:
            return err
        }
    }
    fmt.Printf("wrote page %q (%d evidence, %d sources)\n", res.Title, res.EvidenceQuotes, len(distinctSourceFilesFromResult(res)))
    if len(res.RetroLinkedTitles) > 0 {
        fmt.Printf("retro-linked %d existing page(s)\n", len(res.RetroLinkedTitles))
    }
    fmt.Printf("saved: %s\n", res.Path)
    return nil
}

// resolveAnswerArg accepts an absolute path, a bare filename in
// <wikiDir>/../answers/, or a slug that uniquely matches one
// <ts>-<slug>.md file. Ambiguous slugs return an error naming candidates.
func resolveAnswerArg(arg, wikiDir string) (string, error) { /* ... */ }
```

Add `rootCmd.AddCommand(promoteCmd)` in `cmd/root.go`'s `init()` (one-line addition next to the other `AddCommand` calls).

- [ ] **Step 4: Run tests and confirm pass**

Run: `go test ./cmd/ -run TestPromote -v`
Expected: PASS — eight subtests green.

Run: `go test ./...`
Expected: green.

- [ ] **Step 5: Commit**

```bash
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
feat(cmd): promote subcommand — lifts saved answers into wiki pages

llmwiki promote <answer-file-or-slug> resolves the argument as
absolute path, bare filename in .llmwiki/answers/, or a unique slug
match, then delegates to wiki.PromoteAnswer. --title overrides the
slug-derived title; --rewrite opts into the LLM body rewrite (default
off); --no-save skips the log.md entry. ErrEvidenceInvalid and
ErrTitleExists render via cliutil.Wrap with remediation hints
matching mcp.write_page's structured-error vocabulary.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase C — `RetroLinkPages`

### Task 4: `internal/wiki/retrolink.go` — body-only, idempotent rewriter over existing pages

**Files:**
- Create: `internal/wiki/retrolink.go`
- Create: `internal/wiki/retrolink_test.go`

- [ ] **Step 1: Write failing tests**

Create `internal/wiki/retrolink_test.go`:

1. `TestRetroLinkPages_RewritesPagesMatchingNewTitles` — pre-seed three existing pages whose bodies mention `"Mutex Implementation"` in bare prose; call `wiki.RetroLinkPages(db, wikiDir, []string{"Mutex Implementation"})`; assert all three page bodies on disk now contain `[[Mutex Implementation]]` and the result lists all three titles in `UpdatedTitles`.
2. `TestRetroLinkPages_Idempotent` — second call with same inputs is a byte-identical no-op (no disk writes, `UpdatedTitles` empty).
3. `TestRetroLinkPages_SkipsPagesWhoseTitlesAreInNewSet` — when an existing page's title is also in `newTitles` (it was just written this batch), it's not rewritten — the new-page write step already handled it.
4. `TestRetroLinkPages_BodyOnly_EvidenceUntouched` — assert evidence rows for rewritten pages are byte-identical pre/post call.
5. `TestRetroLinkPages_RecomputesContentHashAndUpdatedAt` — assert `pages.content_hash` and `pages.updated_at` change on disk and in DB for rewritten pages.
6. `TestRetroLinkPages_SkipsPagesWithNoMention` — pre-seed two pages, only one mentions the new title; assert exactly one disk write.
7. `TestRetroLinkPages_FTSPreFilterAtThreshold` — set the package-level threshold (`retroLinkFTSThreshold`) to 2 in the test; pre-seed 5 pages; assert `db.SearchPages` is consulted to narrow the candidate set to only pages whose FTS row matches the new title (mock by reading `db.SearchPages` results). Validates the spec's "FTS pre-filter at large N" provision.
8. `TestRetroLinkPages_CodeFenceStillSkipped` — inherits `RewriteBareReferencesAsWikilinks` semantics; pre-seed a page whose body has `\`\`\`go\nMutex Implementation := struct{}\n\`\`\`` — that occurrence stays unrewritten.

- [ ] **Step 2: Run tests and confirm failure**

Run: `go test ./internal/wiki/ -run TestRetroLinkPages -v`
Expected: FAIL.

- [ ] **Step 3: Implement `internal/wiki/retrolink.go`**

```go
package wiki

import (
    "fmt"
    "os"
    "time"

    "github.com/mritunjaysharma394/llmwiki/internal/db"
)

// retroLinkFTSThreshold gates whether RetroLinkPages pre-filters
// candidates via db.SearchPages. Below this many pages we scan all
// (cheap O(N×M) substring run); at or above we narrow first via FTS.
// Tests set this to a small value to exercise both paths.
var retroLinkFTSThreshold = 500

type RetroLinkResult struct {
    UpdatedTitles []string
}

// RetroLinkPages rewrites every existing page body to include
// [[wikilinks]] for any newTitle that appears in it as bare prose.
// Body-only and idempotent: pages already containing [[NewTitle]]
// are no-ops; evidence rows are untouched. Pages whose body changed
// get their content_hash + updated_at recomputed and persisted via
// WritePage + db.UpsertPage.
//
// Pages whose title is in newTitles are skipped (they were just
// written by the caller's own write step with the full title set).
func RetroLinkPages(database *db.DB, wikiDir string, newTitles []string) (RetroLinkResult, error) {
    var res RetroLinkResult
    if len(newTitles) == 0 { return res, nil }

    newSet := make(map[string]bool, len(newTitles))
    for _, t := range newTitles { newSet[t] = true }

    var candidates []db.PageRecord
    all, err := database.AllPages()
    if err != nil { return res, fmt.Errorf("loading pages: %w", err) }

    if len(all) >= retroLinkFTSThreshold {
        // Narrow via FTS5 on each new title; union the candidate IDs.
        seen := map[int64]bool{}
        for _, t := range newTitles {
            hits, err := database.SearchPages(t, len(all))
            if err != nil { continue } // FTS error is non-fatal; fall back to full scan
            for _, h := range hits {
                if seen[h.ID] || newSet[h.Title] { continue }
                seen[h.ID] = true
                candidates = append(candidates, h)
            }
        }
    } else {
        for _, p := range all {
            if newSet[p.Title] { continue }
            candidates = append(candidates, p)
        }
    }

    now := time.Now().UTC()
    for _, p := range candidates {
        original := p.Body
        rewritten := RewriteBareReferencesAsWikilinks(p.Body, newTitles)
        if rewritten == original { continue }
        // Re-read full Page from disk so we preserve evidence/links/tags
        // (db.PageRecord doesn't carry evidence; ParsePage does).
        full, err := ReadPage(p.Path)
        if err != nil {
            fmt.Fprintf(os.Stderr, "  WARN reading %s for retro-link: %v\n", p.Path, err)
            continue
        }
        full.Body = rewritten
        full.ContentHash = HashContent(rewritten)
        full.UpdatedAt = now
        if err := WritePage(full, wikiDir); err != nil {
            fmt.Fprintf(os.Stderr, "  WARN writing %s after retro-link: %v\n", p.Path, err)
            continue
        }
        rec := db.PageRecord{
            Title:       full.Title,
            Path:        p.Path,
            Body:        rewritten,
            ContentHash: full.ContentHash,
            SourceIDs:   p.SourceIDs,
        }
        if err := database.UpsertPage(rec); err != nil {
            fmt.Fprintf(os.Stderr, "  WARN db upsert for retro-linked %s: %v\n", full.Title, err)
            continue
        }
        res.UpdatedTitles = append(res.UpdatedTitles, full.Title)
    }
    return res, nil
}
```

- [ ] **Step 4: Run tests and confirm pass**

Run: `go test ./internal/wiki/ -run TestRetroLinkPages -v`
Expected: PASS — eight subtests green.

Run: `go test ./...`
Expected: green.

- [ ] **Step 5: Commit**

```bash
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
feat(wiki): RetroLinkPages — body-only [[wikilink]] backfill on existing pages

When ingest, promote, or mcp.write_page lands a new page, every
existing page that mentions the new title in bare prose gets its
body rewritten to include [[Title]]. Reuses
RewriteBareReferencesAsWikilinks (idempotent, case-sensitive,
whole-word, skips fences/backticks). Body-only — evidence rows,
source_ids, and links are untouched, so the trust validator never
needs to run in this path; no claim is being made, only a link is
being drawn.

content_hash and updated_at are recomputed for rewritten pages and
persisted via WritePage + db.UpsertPage. At N>=500 existing pages
the candidate set is pre-filtered via db.SearchPages (FTS5) per new
title; below that we scan all. Idempotent: a second call with the
same newTitles is a no-op for every already-linked page.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase D — Wire retro-linker into ingest, promote, MCP write_page

### Task 5: Three call sites + `mcp.ingest` return shape extension

**Files:**
- Modify: `internal/wiki/ingest_runner.go`
- Modify: `internal/wiki/promote.go`
- Modify: `internal/mcp/handlers.go`
- Modify: `internal/wiki/ingest_runner.go` (also: extend `IngestRunResult`)
- Modify: `internal/mcp/server_test.go`

- [ ] **Step 1: Write failing tests**

Append to `internal/wiki/promote_test.go` (uses already-built fixture from Task 2):

1. `TestPromoteAnswer_RetroLinksExistingPages` — pre-seed two pages mentioning the new title `"Validator Internals"` in bare prose, then promote an answer whose title resolves to `"Validator Internals"`. Assert `PromoteResult.RetroLinkedTitles` contains both pre-existing titles and their bodies on disk now contain `[[Validator Internals]]`.

Append to `cmd/ingest_test.go` (existing pattern):

2. `TestIngest_RetroLinksExistingPages` — pre-seed two pages mentioning `"Mutex"`, run `runIngest` against a synthetic source that produces a `"Mutex"` page; assert post-ingest both pre-existing pages contain `[[Mutex]]` and `IngestRunResult.RetroLinkedPages` is 2.

Append to `internal/mcp/server_test.go`:

3. `TestWritePage_RetroLinksExistingPages` — pre-seed two pages mentioning `"FooBar"`, then `mcp.write_page` a new page titled `"FooBar"` with valid evidence. Assert response payload includes `retro_linked_pages: 2` and the two existing pages on disk now contain `[[FooBar]]`.
4. `TestIngest_ReturnShapeIncludesRetroLinkedPages` — drive `mcp.ingest` (cassette-backed via existing pattern); assert response payload includes `retro_linked_pages` integer key.

- [ ] **Step 2: Run tests and confirm failure**

Run: `go test ./internal/wiki/ ./cmd/ ./internal/mcp/ -run "TestPromoteAnswer_RetroLinksExistingPages|TestIngest_RetroLinks|TestWritePage_RetroLinks|TestIngest_ReturnShapeIncludesRetroLinked" -v`
Expected: FAIL.

- [ ] **Step 3: Wire retro-linker into `IngestSource`**

In `internal/wiki/ingest_runner.go`, **after** the `for i := range allPages { ... }` persist loop and **before** `RegenerateIndex`:

```go
// Phase D (sub-project 6a): retro-link existing pages whose bodies
// mention any of the just-written titles. Body-only, idempotent,
// never touches evidence — the validator does not run here. Spec
// risk #4: at N>=500 existing pages the candidate set is FTS-filtered.
newTitles := make([]string, 0, len(allPages))
for _, p := range allPages { newTitles = append(newTitles, p.Title) }
retroRes, err := RetroLinkPages(database, cfg.WikiDir, newTitles)
if err != nil {
    fmt.Fprintf(os.Stderr, "  WARN retro-linking existing pages: %v\n", err)
}
out.RetroLinkedPages = len(retroRes.UpdatedTitles)
if len(retroRes.UpdatedTitles) > 0 {
    logf("Retro-linked %d existing page(s) that now reference [[%s]]:\n",
         len(retroRes.UpdatedTitles), joinComma(newTitles))
    for _, t := range retroRes.UpdatedTitles { logf("  - %s\n", t) }
}
```

Extend `IngestRunResult`:

```go
type IngestRunResult struct {
    Source                string
    PagesWritten          int
    EvidenceQuotes        int
    DroppedPages          int
    Skipped               bool
    // sub-project 6a additions:
    RetroLinkedPages      int
    ContradictionsFlagged int  // populated by Phase E
}
```

- [ ] **Step 4: Wire retro-linker into `PromoteAnswer`**

In `internal/wiki/promote.go`, after the disk-and-DB write block and before the `log.md` append:

```go
retroRes, _ := RetroLinkPages(database, cfg.WikiDir, []string{title})
res.RetroLinkedTitles = retroRes.UpdatedTitles
```

Surface `RetroLinkedTitles` in the existing `PromoteResult` (already declared in Task 2 with the field; Task 5 fills it).

- [ ] **Step 5: Wire retro-linker into MCP `writePageHandler`**

In `internal/mcp/handlers.go`'s `writePageHandler`, after the `_ = wiki.AppendLog(...)` call and before the `return jsonResult(...)` block:

```go
// sub-project 6a: retro-link existing pages to the new title.
retroRes, _ := wiki.RetroLinkPages(d.DB, d.Cfg.WikiDir, []string{page.Title})
return jsonResult(map[string]any{
    "title":              page.Title,
    "path":               path,
    "evidence_quotes":    evidenceCount,
    "sources":            sourcesList,
    "retro_linked_pages": len(retroRes.UpdatedTitles),
})
```

In `ingestHandler`, surface the new counter from `IngestRunResult`:

```go
return jsonResult(map[string]any{
    "source":             res.Source,
    "pages_written":      res.PagesWritten,
    "evidence_quotes":    res.EvidenceQuotes,
    "dropped_pages":      res.DroppedPages,
    "skipped":            res.Skipped,
    "retro_linked_pages": res.RetroLinkedPages,
    // contradictions_flagged added in Phase E
})
```

- [ ] **Step 6: Run tests and confirm pass**

Run: `go test ./...`
Expected: green.

- [ ] **Step 7: Commit**

```bash
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
feat(wiki,mcp): wire RetroLinkPages into ingest, promote, mcp.write_page

Three call sites all run RetroLinkPages with the new-this-batch title
list after the persist step, before RegenerateIndex (so index.md
reflects the bumped updated_at on retro-linked pages). Body-only,
idempotent, evidence untouched — the trust validator does not run in
this path because no new claim is being made.

IngestRunResult gains RetroLinkedPages; mcp.ingest's return payload
gains retro_linked_pages; mcp.write_page's return payload gains
retro_linked_pages. Backwards-compatible at the JSON-RPC layer:
existing keys are unchanged, clients ignoring unknown keys (the
JSON-RPC default) keep working.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase E — `DetectIngestContradictions` + wiring

### Task 6: `internal/wiki/contradict.go` — candidate selection + LLM call + filtering

**Files:**
- Create: `internal/wiki/contradict.go`
- Create: `internal/wiki/contradict_test.go`

- [ ] **Step 1: Write failing tests**

Create `internal/wiki/contradict_test.go`:

1. `TestDetectIngestContradictions_CandidateSelection` — pre-seed five existing pages with distinct titles and evidence; provide one new page whose body shares keywords with two of them. Assert the candidate list (an exposed test hook returning the `(newPage, candidate)` pairs that would be sent to the LLM) names exactly the two candidates, capped at the configured `candidateLimit`.
2. `TestDetectIngestContradictions_FiltersHallucinatedQuotes` — fixture LLM response (via a stub `llm.Client` returning a canned `Complete` body) names a contradicting quote that doesn't appear in either page's evidence. Assert the result drops it (no `Contradiction` returned).
3. `TestDetectIngestContradictions_HappyPath` — fixture LLM response (stub client) names two valid contradicting quotes from each page's evidence; assert one `Contradiction` returned with both quotes, both source files, both line ranges, and the LLM-supplied `Description`.
4. `TestDetectIngestContradictions_LLMErrorReturnsEmptyAndLogs` — stub client returns an error; assert the function returns `nil, nil` (informational, never blocks ingest) and a WARN line goes to stderr.
5. `TestDetectIngestContradictions_DedupsBidirectionalPairs` — when (newPage A, existing B) and (newPage B, existing A) both surface (shouldn't happen at v1.2 since `existingPages` are pre-filtered to exclude `newPages`, but defensively guard); assert the result is deduped by `(newPageTitle, existingTitle)` ordered pair.
6. `TestFormatContradictionMarkdown` — given a `[]Contradiction`, format an entry block matching the spec's `contradictions.md` format:
   ```
   - 2026-05-04T14:30:12Z **ingest** <source>
     - new page "<NewTitle>" vs existing [[<ExistingTitle>]]:
       - new claim: > "<quote>" (<path>:<a>-<b>)
       - existing claim: > "<quote>" (<path>:<a>-<b>)
   ```

- [ ] **Step 2: Run tests and confirm failure**

Run: `go test ./internal/wiki/ -run TestDetectIngestContradictions -v`
Expected: FAIL.

- [ ] **Step 3: Implement `internal/wiki/contradict.go`**

```go
package wiki

import (
    "context"
    "encoding/json"
    "fmt"
    "io"
    "os"
    "strings"
    "time"

    "github.com/mritunjaysharma394/llmwiki/internal/db"
    "github.com/mritunjaysharma394/llmwiki/internal/llm"
)

type Contradiction struct {
    NewPageTitle       string
    NewPageQuote       string
    NewPageSourceFile  string
    NewPageLines       [2]int
    ExistingTitle      string
    ExistingQuote      string
    ExistingSourceFile string
    ExistingLines      [2]int
    Description        string
}

const defaultContradictionCandidateLimit = 5

// contradictionSystemPrompt is the spec's directive: only direct factual
// contradictions; qualifications and additions are NOT contradictions.
const contradictionSystemPrompt = `You are a contradiction detector. Given page A and page B with their already-validated evidence quotes, output a JSON array of objects: [{"a_quote_index": <int>, "b_quote_index": <int>, "description": "<one sentence>"}]. Only flag direct factual contradictions — qualifications, additions, version-specific claims (e.g. "X applies in Go 1.21" vs "X applies in Go 1.22") are NOT contradictions. If none, return [].`

// DetectIngestContradictions builds candidate (newPage, existingPage)
// pairs by FTS-search over each newPage's body, runs one LLM call per
// pair against cfg.LLM.Model, filters out LLM-hallucinated quotes that
// don't appear in either page's already-validated evidence, and returns
// deduped Contradiction tuples. Caller decides what to do with them
// (typical: append to <wikiDir>/contradictions.md and print inline).
//
// LLM errors and timeouts log a WARN to stderr and produce no
// contradictions for that pair — contradiction detection is
// informational and MUST NEVER block trust-validated ingest writes.
func DetectIngestContradictions(ctx context.Context, client llm.Client, newPages []Page, existingPages []db.PageRecord, candidateLimit int, database *db.DB) ([]Contradiction, error) {
    if candidateLimit <= 0 { candidateLimit = defaultContradictionCandidateLimit }
    var out []Contradiction
    seen := map[string]bool{}
    for _, np := range newPages {
        candidates := selectCandidates(np, existingPages, candidateLimit, database)
        for _, ep := range candidates {
            key := np.Title + "\x00" + ep.Title
            if seen[key] { continue }
            seen[key] = true
            // Build user prompt: page A (new) and page B (existing) each
            // labeled with their evidence quotes by index. Tell the LLM
            // to refer to quotes by (a_quote_index, b_quote_index).
            user := buildContradictionPrompt(np, ep, database)
            raw, err := client.Complete(ctx, contradictionSystemPrompt, user)
            if err != nil {
                fmt.Fprintf(os.Stderr, "  WARN contradiction check failed for %q vs %q: %v\n", np.Title, ep.Title, err)
                continue
            }
            // Parse the JSON array; for each tuple, look up the named
            // quote by index in each page's evidence; drop tuples whose
            // quote indices are out of range (LLM hallucination).
            for _, c := range parseContradictionResponse(raw, np, ep) {
                out = append(out, c)
            }
        }
    }
    return out, nil
}

// FormatContradictionMarkdown renders contradictions to the
// <wikiDir>/contradictions.md format the spec describes (Flow 1).
func FormatContradictionMarkdown(contras []Contradiction, source string, at time.Time) string { /* ... */ }

// AppendContradictions opens <wikiDir>/contradictions.md with O_APPEND and
// writes one block per call. Mirrors AppendLog's atomicity guarantee.
func AppendContradictions(wikiDir string, contras []Contradiction, source string, at time.Time) error { /* ... */ }
```

The `selectCandidates` helper unions per-new-page hits from `database.SearchPages(np.Body, candidateLimit)` and `database.SearchEvidence(np.Body, candidateLimit)`, dedupes by page ID, excludes pages whose title matches any new-page title, and caps at `candidateLimit`.

- [ ] **Step 4: Run tests and confirm pass**

Run: `go test ./internal/wiki/ -run TestDetectIngestContradictions -v`
Expected: PASS — six subtests green.

Run: `go test ./...`
Expected: green.

- [ ] **Step 5: Commit**

```bash
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
feat(wiki): DetectIngestContradictions — structured contradiction surface for ingest

Per new page, selects up to candidateLimit (default 5) existing
candidate pages via db.SearchPages + db.SearchEvidence union; runs
one structured LLM call per (newPage, candidate) pair against the
configured cfg.LLM.Model; filters out LLM-hallucinated quotes that
don't appear in either page's already-validated evidence
(validator-style); returns a deduped []Contradiction.

LLM errors and timeouts log WARN to stderr and produce no
contradictions for that pair — contradiction detection is
informational and MUST NEVER block trust-validated ingest writes.
The system prompt explicitly says qualifications and additions are
NOT contradictions, mitigating the spec's #6 risk.

FormatContradictionMarkdown + AppendContradictions render the
results to <wikiDir>/contradictions.md in the spec's append-only
format (mirrors AppendLog's atomicity).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 7: Wire contradiction detection into `IngestSource`

**Files:**
- Modify: `internal/wiki/ingest_runner.go`
- Modify: `internal/mcp/handlers.go` (extend `ingest` return shape with `contradictions_flagged`)

- [ ] **Step 1: Write failing tests**

Append to `cmd/ingest_test.go`:

1. `TestIngest_RunsContradictionPassAndAppendsToContradictionsMD` — pre-seed an existing page claiming X with valid evidence; ingest a synthetic source whose generated page claims ¬X (use a stub LLM client that returns hand-crafted `[]Page` for the ingest call AND a hand-crafted contradiction LLM response). Assert: page lands; `<wikiDir>/contradictions.md` is created with the expected timestamped entry; `IngestRunResult.ContradictionsFlagged == 1`; the inline log output contains `!! 1 contradiction(s) flagged`.
2. `TestIngest_ContradictionLLMFailureDoesNotBlockIngest` — same setup, but stub the contradiction LLM call to return `error`. Assert: page still lands; `IngestRunResult.ContradictionsFlagged == 0`; a WARN line on stderr; `<wikiDir>/contradictions.md` is NOT created.

- [ ] **Step 2: Run tests and confirm failure**

Run: `go test ./cmd/ -run "TestIngest_RunsContradictionPass|TestIngest_ContradictionLLMFailure" -v`
Expected: FAIL.

- [ ] **Step 3: Wire into `IngestSource`**

In `internal/wiki/ingest_runner.go`, **after** the retro-link block from Phase D and **before** `RegenerateIndex`:

```go
// Phase E (sub-project 6a): contradiction-on-ingest. Builds the
// (new pages, existing-page candidate) pairs via FTS, runs one LLM
// call per pair, filters hallucinated quotes, appends matches to
// <wikiDir>/contradictions.md, prints an inline summary. LLM errors
// log WARN; ingest writes already happened above so a contradiction
// failure cannot revoke them.
existingPageRecs, _ := database.AllPages()
existingNonNew := existingPageRecs[:0]
{
    newSet := map[string]bool{}
    for _, p := range allPages { newSet[p.Title] = true }
    for _, p := range existingPageRecs {
        if !newSet[p.Title] { existingNonNew = append(existingNonNew, p) }
    }
}
contras, _ := DetectIngestContradictions(ctx, client, allPages, existingNonNew, defaultContradictionCandidateLimit, database)
if len(contras) > 0 {
    out.ContradictionsFlagged = len(contras)
    logf("\n!! %d contradiction(s) flagged against the new pages:\n", len(contras))
    for _, c := range contras {
        logf("   - new page %q claims:\n     > %q\n     conflicts with existing page [[%s]]:\n     > %q\n     both quotes are validated against their own sources; resolve manually.\n",
             c.NewPageTitle, c.NewPageQuote, c.ExistingTitle, c.ExistingQuote)
    }
    if err := AppendContradictions(cfg.WikiDir, contras, source, time.Now().UTC()); err != nil {
        fmt.Fprintf(os.Stderr, "  WARN appending to contradictions.md: %v\n", err)
    } else {
        logf("logged to: %s/contradictions.md\n", cfg.WikiDir)
    }
}
```

- [ ] **Step 4: Surface `ContradictionsFlagged` in MCP return shape**

In `internal/mcp/handlers.go`'s `ingestHandler`, add the key:

```go
return jsonResult(map[string]any{
    "source":                 res.Source,
    "pages_written":          res.PagesWritten,
    "evidence_quotes":        res.EvidenceQuotes,
    "dropped_pages":          res.DroppedPages,
    "skipped":                res.Skipped,
    "retro_linked_pages":     res.RetroLinkedPages,
    "contradictions_flagged": res.ContradictionsFlagged,
})
```

- [ ] **Step 5: Run tests and confirm pass**

Run: `go test ./...`
Expected: green.

- [ ] **Step 6: Commit**

```bash
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
feat(wiki,mcp): wire contradiction-on-ingest into IngestSource

After the retro-link pass, IngestSource builds the (allPages,
existing-pages-minus-new-titles) pair list and runs
DetectIngestContradictions against the configured LLM. Matches are
printed inline ("!! N contradiction(s) flagged") and appended to
<wikiDir>/contradictions.md in the spec's append-only timestamped
format. LLM/timeout failures log WARN and do NOT fail the ingest —
the new pages already landed, the trust property already holds, the
contradiction surface is informational.

mcp.ingest's return payload gains contradictions_flagged: int. The
trust property is reaffirmed: contradiction detection runs AFTER
the validator-gated persist loop, so a contradiction failure can
never roll back a trust-validated write.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase F — MCP `promote_answer` + return-shape extensions

### Task 8: Register `promote_answer` tool + handler

**Files:**
- Modify: `internal/mcp/server.go`
- Modify: `internal/mcp/handlers.go`
- Modify: `internal/mcp/server_test.go`

- [ ] **Step 1: Write failing tests**

Append to `internal/mcp/server_test.go`:

1. `TestPromoteAnswer_RegisteredAsTool` — list registered tool names; assert `promote_answer` is in the set.
2. `TestPromoteAnswer_HappyPath` — ingest a synthetic source via `mcp.ingest`; hand-author a `.llmwiki/answers/<ts>-foo.md` file with quotes drawn from the ingested source; call `mcp.promote_answer({answer_path: "...", title: "Validator Internals"})`; assert response contains `title`, `path`, `evidence_quotes`, `retro_linked_pages` keys; assert page lands on disk.
3. `TestPromoteAnswer_StaleEvidenceReturnsStructuredError` — same setup but mutate the source file between `ingest` and `promote_answer`; assert structured error `{code: "evidence_invalid", dropped: [...]}`.
4. `TestPromoteAnswer_TitleCollisionReturnsStructuredError` — pre-seed a page with the target title; assert structured error `{code: "title_exists", existing_path: "..."}`.
5. `TestPromoteAnswer_RewriteFlagPassesThrough` — call with `rewrite: true`; assert the handler delegates to `wiki.PromoteAnswer` with `PromoteOptions.Rewrite == true` (use a fake `wiki.PromoteAnswer` indirection via a package-level seam, OR observe via the response's `rewrite_applied` boolean).
6. `TestPromoteAnswer_DefaultsRewriteOff` — no `rewrite` arg; assert default is off.

- [ ] **Step 2: Run tests and confirm failure**

Run: `go test ./internal/mcp/ -run TestPromoteAnswer -v`
Expected: FAIL — tool does not exist.

- [ ] **Step 3: Register the tool in `internal/mcp/server.go`**

```go
const (
    serverName    = "llmwiki"
    serverVersion = "1.2.0" // bumped from 1.1.0 for sub-project 6a
)

func NewServer(d Deps) *mcpsrv.MCPServer {
    s := mcpsrv.NewMCPServer(serverName, serverVersion)
    s.AddTool(listPagesTool(), listPagesHandler(d))
    s.AddTool(readPageTool(), readPageHandler(d))
    s.AddTool(lintTool(), lintHandler(d))
    s.AddTool(askTool(), askHandler(d))
    s.AddTool(writePageTool(), writePageHandler(d))
    s.AddTool(ingestTool(), ingestHandler(d))
    s.AddTool(promoteAnswerTool(), promoteAnswerHandler(d)) // sub-project 6a
    return s
}

func promoteAnswerTool() mcpgo.Tool {
    return mcpgo.NewTool(
        "promote_answer",
        mcpgo.WithDescription(
            "Lift a saved answer (.llmwiki/answers/<ts>-<slug>.md) into a real wiki page. Defensive re-validation runs every evidence quote through the same byte-exact substring-match validator that gates write_page; quotes whose source files have changed since the ask are rejected with code: \"evidence_invalid\". Title collisions return code: \"title_exists\"."),
        mcpgo.WithString("answer_path", mcpgo.Description("Absolute path to the answer file. One of answer_path or answer_slug is required."), mcpgo.Required()),
        mcpgo.WithString("title", mcpgo.Description("Override title; defaults to Title-Cased question.")),
        mcpgo.WithBoolean("rewrite", mcpgo.Description("LLM-rewrite answer body into wiki prose; default off.")),
    )
}
```

- [ ] **Step 4: Implement `promoteAnswerHandler` in `internal/mcp/handlers.go`**

```go
func promoteAnswerHandler(d Deps) mcpsrv.ToolHandlerFunc {
    return func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
        answerPath, err := req.RequireString("answer_path")
        if err != nil {
            return errorResult("bad_request", err.Error(), nil), nil
        }
        title := req.GetString("title", "")
        rewrite := req.GetBool("rewrite", false)

        // Slim IngestSourceConfig from MCP Config — same shape ingestHandler uses.
        wcfg := wiki.IngestSourceConfig{
            WikiDir:          d.Cfg.WikiDir,
            RawDir:           d.Cfg.RawDir,
            RespectGitignore: true,
        }
        res, err := wiki.PromoteAnswer(ctx, wcfg, d.DB, d.Client, answerPath, wiki.PromoteOptions{
            Title: title, Rewrite: rewrite,
        })
        if err != nil {
            switch {
            case errors.Is(err, wiki.ErrEvidenceInvalid):
                return errorResult("evidence_invalid",
                    "every evidence quote failed defensive re-validation; nothing was written",
                    map[string]any{"dropped": res.DroppedQuotes,
                                   "hint": "the source files referenced by this answer have changed since the ask; re-run ask + promote against the current wiki"}), nil
            case errors.Is(err, wiki.ErrTitleExists):
                return errorResult("title_exists",
                    fmt.Sprintf("a page titled %q already exists", res.Title),
                    map[string]any{"existing_path": res.Path}), nil
            default:
                return errorResult("promote_failed", err.Error(), nil), nil
            }
        }
        return jsonResult(map[string]any{
            "title":              res.Title,
            "path":               res.Path,
            "evidence_quotes":    res.EvidenceQuotes,
            "retro_linked_pages": len(res.RetroLinkedTitles),
            "rewrite_applied":    res.RewriteApplied,
        })
    }
}
```

- [ ] **Step 5: Run tests and confirm pass**

Run: `go test ./internal/mcp/ -run TestPromoteAnswer -v`
Expected: PASS — six subtests green.

Run: `go test ./...`
Expected: green.

- [ ] **Step 6: Commit**

```bash
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
feat(mcp): promote_answer tool + version bump to 1.2.0

internal/mcp registers a seventh tool, promote_answer, that lifts a
.llmwiki/answers/<ts>-<slug>.md file into a real wiki page. The
handler delegates to wiki.PromoteAnswer; ErrEvidenceInvalid and
ErrTitleExists translate to code: "evidence_invalid" and code:
"title_exists" structured errors matching write_page's vocabulary.
serverVersion bumps from "1.1.0" to "1.2.0" so MCP clients see the
correct release.

Trust property reaffirmed over MCP: every page reaching disk via
promote_answer has ≥1 evidence quote that substring-matches its
source_file — defensive re-validation through
wiki.ValidateAndAttachEvidence is the single gatekeeper.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 9: Verify `mcp.ingest` and `mcp.write_page` return-shape extensions are documented in handler comments

**Files:**
- Modify: `internal/mcp/handlers.go` (doc-comment additions only)

This is a small documentation-only commit folding all the return-shape changes from Phases D, E, and F into the handler doc comments so a fresh reader knows what each tool returns. No behaviour change.

- [ ] **Step 1: Update `ingestHandler` doc comment**

Above `func ingestHandler(...)`, add:

```go
// ingestHandler returns:
//   {
//     "source":                 string,
//     "pages_written":          int,
//     "evidence_quotes":        int,
//     "dropped_pages":          int,
//     "skipped":                bool,
//     "retro_linked_pages":     int,    // sub-project 6a
//     "contradictions_flagged": int,    // sub-project 6a
//   }
//
// retro_linked_pages counts existing pages whose body was rewritten to
// include [[NewTitle]] for any of the new-this-batch titles (body-only,
// idempotent; evidence rows untouched). contradictions_flagged counts
// (newPage, existingPage) tuples where the contradiction-detection LLM
// call returned a direct factual contradiction backed by validated
// quotes on both sides; details append to <wikiDir>/contradictions.md.
```

- [ ] **Step 2: Update `writePageHandler` doc comment** with the new `retro_linked_pages` key.

- [ ] **Step 3: Update `promoteAnswerHandler` doc comment** with the full return shape.

- [ ] **Step 4: Run build**

Run: `go build ./... && go vet ./...`
Expected: clean.

- [ ] **Step 5: Commit**

```bash
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
docs(mcp): document v1.2 return-shape extensions in handler comments

ingestHandler, writePageHandler, and promoteAnswerHandler all gained
new keys in their JSON return shape this release. Add explicit
return-shape blocks to each handler's doc comment so readers know
the full contract without grepping through the body. No behaviour
change.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase G — Cassettes

### Task 10: `TestPromoteAnswerHappyPath` cassette

**Files:**
- Create: `cmd/promote_integration_test.go`
- Create: `internal/llm/testdata/cassettes/TestPromoteAnswerHappyPath__*.json`

- [ ] **Step 1: Write failing test**

Create `cmd/promote_integration_test.go`:

```go
func TestPromoteAnswerHappyPath(t *testing.T) {
    if testing.Short() { t.Skip("skipping cassette test in -short mode") }
    if _, err := os.Stat("../internal/llm/testdata/cassettes/TestPromoteAnswerHappyPath__001.json"); os.IsNotExist(err) {
        t.Skip("cassette not recorded; run with LLMWIKI_RECORD=1 GEMINI_API_KEY=... to record")
    }
    setEnv(t, "LLMWIKI_CASSETTE", "TestPromoteAnswerHappyPath")
    setEnv(t, "GEMINI_API_KEY", "test-key-for-replay")
    // 1. Set up tmp wiki + provider=gemini config.
    // 2. ingest a synthetic source.
    // 3. ask a question (one cassette segment) → answer file lands in .llmwiki/answers/.
    // 4. promote the answer file (no LLM call needed for verbatim body).
    // 5. Assert page lands at <wikiDir>/<title>.md, evidence rows present,
    //    log.md got a **promote** line.
    // 6. Verify trust property: read the on-disk page and assert every
    //    evidence quote substring-matches the originally-ingested source.
}
```

- [ ] **Step 2: Record the cassette**

```bash
export GEMINI_API_KEY=...
LLMWIKI_RECORD=1 go test ./cmd/ -run TestPromoteAnswerHappyPath -v
```

- [ ] **Step 3: Run replay and confirm pass**

Run: `unset GEMINI_API_KEY && go test ./cmd/ -run TestPromoteAnswerHappyPath -v`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
test(cassette): TestPromoteAnswerHappyPath — full ingest→ask→promote loop

Drives the v1.2 promote command end-to-end against a recorded
Gemini Flash cassette. Asserts: page lands on disk, evidence rows
substring-match the originally-ingested source, log.md got the
**promote** line. Verifies the trust property holds for the
v1.2 promote path the same way it does for v1.0 ingest.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 11: `TestPromoteAnswerStaleEvidence` cassette

**Files:**
- Modify: `cmd/promote_integration_test.go`
- Create: `internal/llm/testdata/cassettes/TestPromoteAnswerStaleEvidence__*.json`

- [ ] **Step 1: Write failing test**

Append:

```go
func TestPromoteAnswerStaleEvidence(t *testing.T) {
    // Same setup as TestPromoteAnswerHappyPath through step 3 (answer landed).
    // Then mutate the source file on disk so the answer's quotes no longer match.
    // Then call runPromote and assert the returned error is wiki.ErrEvidenceInvalid
    // (rendered via cliutil.Wrap with the structured-error message).
    // Assert: NO new wiki page on disk; NO log.md "promote" line.
}
```

- [ ] **Step 2: Record + commit cassette** (record may share segments with Task 10's cassette; record fresh to keep tests independent).

- [ ] **Step 3: Run replay and confirm pass**

Run: `unset GEMINI_API_KEY && go test ./cmd/ -run TestPromoteAnswerStaleEvidence -v`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
test(cassette): TestPromoteAnswerStaleEvidence — defensive re-validation drops mutated source

Mutates the originally-ingested source file between ask and promote.
Asserts wiki.PromoteAnswer returns ErrEvidenceInvalid, NO disk write,
NO log.md entry — the same trust-property guarantee that gates
ingest and mcp.write_page also gates promote. This is the v1.2
analogue of TestMCPWritePageRoundtrip's invalid-evidence assertion.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 12: `TestRetroLinkAfterIngest` cassette

**Files:**
- Modify: `cmd/ingest_integration_test.go`
- Create: `internal/llm/testdata/cassettes/TestRetroLinkAfterIngest__*.json`

- [ ] **Step 1: Write failing test**

Append:

```go
func TestRetroLinkAfterIngest(t *testing.T) {
    // 1. Pre-seed three existing pages (manual db.UpsertPage + WritePage)
    //    whose bodies mention "Mutex" in bare prose.
    // 2. Ingest a synthetic source whose generated page is titled "Mutex".
    // 3. Assert all three pre-existing page bodies on disk now contain
    //    [[Mutex]]; assert their content_hash on disk matches the new body
    //    hash; assert IngestRunResult.RetroLinkedPages == 3.
}
```

- [ ] **Step 2: Record + commit cassette** with `GEMINI_API_KEY` set.

- [ ] **Step 3: Run replay and confirm pass**

- [ ] **Step 4: Commit**

```bash
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
test(cassette): TestRetroLinkAfterIngest — body-only backfill of [[wikilinks]]

Pre-seeds three existing pages mentioning "Mutex" in bare prose,
ingests a synthetic source that produces a Mutex page, asserts all
three existing pages got their bodies rewritten to include
[[Mutex]] and their content_hash + updated_at recomputed.
Validates the retro-linker is body-only (evidence rows untouched)
and idempotent (running ingest twice yields no further rewrites).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 13: `TestContradictionFlaggedOnIngest` cassette

**Files:**
- Modify: `cmd/ingest_integration_test.go`
- Create: `internal/llm/testdata/cassettes/TestContradictionFlaggedOnIngest__*.json`

- [ ] **Step 1: Write failing test**

```go
func TestContradictionFlaggedOnIngest(t *testing.T) {
    // 1. Pre-seed an existing page with body claiming "X is true" and
    //    a validated evidence quote pinned to a synthetic source file.
    // 2. Ingest a second synthetic source whose generated page claims
    //    "X is false" with its own validated evidence quote.
    // 3. Assert: <wikiDir>/contradictions.md exists; its content matches
    //    the spec's append-only format with both quote sides; the inline
    //    log output contained "!! 1 contradiction(s) flagged"; the
    //    IngestRunResult.ContradictionsFlagged == 1.
}
```

This cassette has TWO LLM call types: the ingest call (write_pages tool) and the contradiction-detection call (free-form Complete). Both segments record under one cassette name.

- [ ] **Step 2: Record + commit cassette** with `GEMINI_API_KEY` set.

- [ ] **Step 3: Run replay and confirm pass**

- [ ] **Step 4: Commit**

```bash
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
test(cassette): TestContradictionFlaggedOnIngest — inline + file-form surface

Pre-seeds an existing page claiming X with valid evidence; ingests
a synthetic source whose generated page claims ¬X with valid
evidence. Asserts: page lands; <wikiDir>/contradictions.md is
written with the spec's append-only format; the inline log output
contains the "!! N contradiction(s) flagged" block; the LLM
contradiction-detection call hits the configured cfg.LLM.Model
(Gemini Flash for the cassette). Confirms the contradiction
surface is informational — the new page lands regardless of what
contradiction detection says, the trust property is upheld.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 14: `TestMCPPromoteAnswerRoundtrip` cassette

**Files:**
- Modify: `internal/mcp/integration_test.go`
- Create: `internal/llm/testdata/cassettes/TestMCPPromoteAnswerRoundtrip__*.json`

- [ ] **Step 1: Write failing test**

```go
func TestMCPPromoteAnswerRoundtrip(t *testing.T) {
    if _, err := os.Stat("../llm/testdata/cassettes/TestMCPPromoteAnswerRoundtrip__001.json"); os.IsNotExist(err) {
        t.Skip("cassette not recorded; run with LLMWIKI_RECORD=1 GEMINI_API_KEY=... to record")
    }
    setEnv(t, "LLMWIKI_CASSETTE", "TestMCPPromoteAnswerRoundtrip")
    setEnv(t, "GEMINI_API_KEY", "test-key-for-replay")
    // 1. Spin up MCP server in-process.
    // 2. mcp.ingest(source: <synthetic>) — assert pages_written > 0.
    // 3. mcp.ask(question: "...") via the in-process MCP client; capture
    //    the returned answer + sources. (The handler doesn't write an
    //    answer file — that's cmd/ask's saveAnswer job. So we hand-author
    //    a fixture answer file at .llmwiki/answers/<ts>-foo.md after
    //    ask, populated from the captured response.)
    // 4. mcp.promote_answer(answer_path: "...", title: "Foo Page") —
    //    assert response payload has title, path, evidence_quotes,
    //    retro_linked_pages keys.
    // 5. mcp.read_page(title: "Foo Page") — assert page body is the
    //    answer text and evidence array is non-empty.
    // 6. Negative: call promote_answer again with the same title;
    //    assert structured error code: "title_exists", existing_path
    //    populated.
}
```

- [ ] **Step 2: Record + commit cassette** with `GEMINI_API_KEY` set.

- [ ] **Step 3: Run replay and confirm pass**

- [ ] **Step 4: Commit**

```bash
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
test(mcp): TestMCPPromoteAnswerRoundtrip — full MCP-driven promote flow

Drives the MCP server in-process via mark3labs/mcp-go's in-process
client through the v1.2 ingest→ask→promote_answer→read_page loop.
Asserts evidence_quotes > 0 on the promoted page, retro_linked_pages
key is present, title_exists structured error fires on a second
promote with the same title. The cassette wraps Gemini Flash so the
test runs in CI without an API key. Sister test to v1.1.0's
TestMCPWritePageRoundtrip; together they cover both v1.1 (write_page)
and v1.2 (promote_answer) MCP write paths against the same trust
validator.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase H — Docs + tag

### Task 15: README "Living Wiki" section + CHANGELOG `[1.2.0]` entry

**Files:**
- Modify: `README.md`
- Modify: `CHANGELOG.md`

- [ ] **Step 1: Add the "Living Wiki" section to README**

Sections to layer onto the existing v1.1 README structure:

1. **New "Living Wiki" section** placed just below the existing Trust Property section. Lead order per the spec:
   - **Promote a saved answer** — the most user-visible feature. Show `llmwiki ask` then `ls .llmwiki/answers/` then `llmwiki promote <file> --title "..."`. One paragraph on `--rewrite` (off by default; opt-in for wiki-prose rewrite).
   - **Contradictions surface inline** — show the `!! N contradiction(s) flagged` block from Flow 1; explain that `<wikiDir>/contradictions.md` accumulates them in append-only form; one sentence on cost ("uses your configured provider — Gemini Flash users pay nothing").
   - **Retro-linker keeps the graph current** — one paragraph: "every new page automatically gets backlinks added to existing pages that mention it; open `.llmwiki/wiki/` in Obsidian and the graph view lights up the new connections."
2. **Existing "Use your Claude subscription via MCP" section** gains one bullet: "`promote_answer` lifts a saved answer into a real page with the same trust validation."
3. **Existing Trust Property section** unchanged, but add one sentence: "v1.2's three new behaviours (promote, retro-link, contradictions) all preserve the validator: every page reaching disk has ≥1 evidence quote that substring-matches its source — promote defensively re-validates because source files may have changed since the ask."

Explicitly do NOT promise 6b features. The section closes with: "v1.3 will add cross-page page-update — when a new source updates earlier pages, those pages get their bodies refined under the same validator. Opt-in, on the way."

- [ ] **Step 2: Add `[1.2.0]` CHANGELOG entry**

```markdown
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
- Retro-linker — every new page (from `ingest`, `promote`, or
  `mcp.write_page`) automatically gets `[[Title]]` backlinks added
  to existing pages whose bodies mention it in bare prose. Body-only,
  idempotent, evidence rows untouched. Surfaces in the `ingest`
  summary line as "Retro-linked N existing page(s)".
- Contradiction-on-ingest — when a new page's claim conflicts with
  an existing page's claim, the conflict prints inline ("!! N
  contradiction(s) flagged") and appends to
  `<wikiDir>/contradictions.md` in an Obsidian-friendly append-only
  format. Uses whatever provider you configured at `init` (Gemini
  Flash users pay nothing). Failures are non-fatal — the new pages
  still land.
- `mcp.ingest` return shape extended: adds `retro_linked_pages: int`
  and `contradictions_flagged: int`. `mcp.write_page` gains
  `retro_linked_pages: int`.

### Changed
- `internal/mcp` server version bumped to `1.2.0`.

### Notes
- **No schema migration.** `PRAGMA user_version` stays at 3.
- The existing `lint` command's whole-wiki contradiction batcher is
  unchanged. Live contradiction-on-ingest is a sibling, not a
  replacement.
- v1.3 will add the cross-page page-update pass under a default-off
  `--update-existing` flag. Out of scope for v1.2.
```

Move any `[Unreleased]` content into `[1.2.0]`; leave a fresh empty `[Unreleased]` at the top.

- [ ] **Step 3: Commit**

```bash
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
docs(readme,changelog): [1.2.0] — promote, retro-linker, contradiction-on-ingest

README gains a Living Wiki section that leads with promote (the
most user-visible feature), then contradictions, then the
retro-linker. Trust Property section gets one new sentence on how
v1.2's three behaviours preserve the validator. CHANGELOG [1.2.0]
covers all three pillars with explicit "no schema migration" note.
README is honest about what 6b adds in v1.3 and what 6a does NOT
do (no cross-page page-update pass; no --update-existing flag).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 16: Tag `v1.2.0-rc.1` locally (no push)

**Files:** none (tag only)

- [ ] **Step 1: Final pre-tag verification**

Run, top to bottom:

- [ ] `go test ./...` is green in replay mode (no API keys exported).
- [ ] `go build ./... && go vet ./...` clean.
- [ ] Spot-check the spec's verification block (`docs/superpowers/specs/2026-05-04-living-wiki-dynamics-design.md`, the v1.2 portion of `## Verification`):
  ```bash
  unset GEMINI_API_KEY ANTHROPIC_API_KEY
  rm -rf /tmp/test-6a-wiki && mkdir /tmp/test-6a-wiki && cd /tmp/test-6a-wiki
  llmwiki init                                  # gemini default
  export GEMINI_API_KEY=...                     # real key for the smoke
  llmwiki ingest README.md                      # writes pages
  llmwiki ask "what does this README cover?"    # writes .llmwiki/answers/...
  ls .llmwiki/answers/                          # one timestamped file
  llmwiki promote .llmwiki/answers/<ts>-*.md --title "README Overview"
  ls .llmwiki/wiki/ | grep "README Overview"    # file exists
  cat .llmwiki/wiki/log.md | tail -3            # **promote** line
  # Now mutate README.md and re-run promote on the same answer:
  echo "totally rewritten" >> README.md
  llmwiki promote .llmwiki/answers/<ts>-*.md    # expect evidence_invalid
  ```
- [ ] Inspect `.llmwiki/contradictions.md` if any sources contradicted; expected to be absent if README is the only ingested source.

- [ ] **Step 2: Tag**

```bash
git -c commit.gpgsign=false tag -a v1.2.0-rc.1 -m "$(cat <<'EOF'
v1.2.0-rc.1 — Living Wiki Dynamics (sub-project 6a)

Three additive, validator-respecting features atop v1.1.0:

  - llmwiki promote (+ mcp.promote_answer): defensively re-validates
    a saved answer's quotes against current source files before
    landing it as a wiki page; rejects with evidence_invalid if
    sources have changed since the ask.
  - Retro-linker: every new page (ingest/promote/mcp.write_page)
    automatically gets [[Title]] backlinks added to existing pages
    that mention it in bare prose. Body-only, idempotent.
  - Contradiction-on-ingest (warn-only): inline "!! N flagged" plus
    append to <wikiDir>/contradictions.md when a new page's claim
    conflicts with an existing page's claim. Informational; never
    blocks trust-validated writes.

mcp.ingest return shape extended (retro_linked_pages,
contradictions_flagged); mcp.write_page gains retro_linked_pages.
No schema migration — user_version stays at 3.

v1.3 will add the cross-page page-update pass under a default-off
--update-existing flag (sub-project 6b). Out of scope for v1.2.
Promotion to v1.2.0 is a post-launch follow-up after the spec's
1-week stability window.
EOF
)"
```

- [ ] **Step 3: Verify**

Run: `git tag -l "v1.2*"`
Expected: prints `v1.2.0-rc.1`.

Do **not** `git push --tags`. Promotion to a real release is a manual step matching v1.0 / v1.1's pattern.

---

## Done criteria

- All 16 tasks have a green checkbox.
- `go test ./...` is green in replay mode (no API keys required).
- `go build ./... && go vet ./...` clean.
- A fresh `mkdir wiki && cd wiki && llmwiki init && llmwiki ingest <source> && llmwiki ask "..." && llmwiki promote .llmwiki/answers/<ts>-<slug>.md` walks through end-to-end and lands a wiki page on disk whose evidence quotes substring-match the original source.
- A second `llmwiki ingest <source>` against a source whose pages share titles or claims with existing pages produces a `Retro-linked N existing page(s)` summary line and (if contradictions exist) a `!! N contradiction(s) flagged` block plus a `<wikiDir>/contradictions.md` entry.
- `llmwiki mcp` exposes seven tools (`ingest`, `ask`, `list_pages`, `read_page`, `write_page`, `lint`, `promote_answer`); `mcp.promote_answer` rejects a stale-evidence answer with a structured `evidence_invalid` error visible in the client UI.
- The tag `v1.2.0-rc.1` exists locally.
- The README's Living Wiki section leads with `promote`, documents contradictions and the retro-linker, and is explicit that v1.3 (not v1.2) adds cross-page page-updates.
- Trust property holds at every disk-write site: `ingest`, `promote`, `mcp.write_page`, `mcp.promote_answer`, and the retro-linker (which is body-only and never writes new evidence).
