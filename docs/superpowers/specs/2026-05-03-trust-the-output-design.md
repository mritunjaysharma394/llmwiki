# Sub-project 1 — Trust the Output

**Status:** design approved, awaiting implementation plan
**Date:** 2026-05-03
**Author:** Mritunjay Sharma (with Claude)

## Context

`llmwiki` is a Go CLI implementing Karpathy's LLM-wiki concept: ingest sources (files/URLs/repos), have an LLM synthesize them into linked wiki pages, and answer questions over the result. The CLI works end-to-end today, but a smoke test ingesting `go.mod` produced 7 pages — 3–4 of which were not in the source ("JSON Processing in Go", "Go Concurrency"). For the project to be daily-driveable and worth recommending, ingest must be trustworthy: every page must be grounded in real source content, and every answer must be traceable back to verbatim source quotes.

This is the first of four planned sub-projects (1: trust, 3: real-world ingestion, 2: web UI, 4: launch surface). Each gets its own spec → plan → implementation cycle.

## Goals

1. Every page written to disk or DB is grounded in verifiable text from the ingested source — no hallucinations.
2. `ask` produces answers traceable to verbatim source quotes, not just page titles.
3. First-run experience uses Anthropic + Haiku, fails fast with a helpful error if no API key.
4. Ingest handles real-world doc sizes (200KB+) without dropping content or thrashing the laptop.
5. Every `ask` answer is a permanent searchable artifact (auto-archived to `.llmwiki/answers/`).
6. CI-runnable test suite covers every LLM-touching code path (cassette pattern).

## Non-goals (deferred)

- PDF / improved URL / GitHub repo ingestion → sub-project 3
- Web UI → sub-project 2
- Contradiction detection upgrades — leave `lint` mostly as-is
- Ollama parity for streaming and prompt caching (Ollama keeps current non-streaming path; degraded but functional)
- `llmwiki history` browsing command — sub-project 2 covers this in the UI

## Architecture overview

The trust property is enforced by a single invariant at one place:

> Every `Page` written to disk or DB has at least one `Evidence` whose `Quote` is a substring of the source content it came from.

Validation happens in `internal/wiki/ops.go` immediately after the LLM tool-call result is parsed, before pages return to `cmd/ingest.go`. Pages failing validation are dropped with a `WARN` log. Downstream code can trust that anything it sees was actually in a source.

`ask` becomes a two-table retrieval: FTS5 over `pages_fts` (synthesized) and `evidence_fts` (verbatim source spans). Both result sets are passed to the LLM with explicit roles ("here are wiki pages, here are the source quotes that ground them"), so answers can cite both the wiki's interpretation and the underlying verbatim source.

## Schema changes

Two new tables + one new FTS5 virtual table. Existing `pages`, `pages_fts`, `sources`, `links` are unchanged.

```sql
CREATE TABLE evidence (
  id INTEGER PRIMARY KEY,
  page_id INTEGER NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
  source_id INTEGER NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
  quote TEXT NOT NULL,        -- verbatim substring of the source content
  line_start INTEGER,         -- 1-indexed line in source, computed best-effort
  line_end INTEGER,
  created_at DATETIME
);
CREATE INDEX idx_evidence_page ON evidence(page_id);
CREATE VIRTUAL TABLE evidence_fts USING fts5(quote, content=evidence, content_rowid=id);

CREATE TABLE saved_answers (
  id INTEGER PRIMARY KEY,
  question TEXT NOT NULL,
  answer TEXT NOT NULL,
  model TEXT,
  cited_page_ids TEXT,        -- JSON array
  created_at DATETIME,
  file_path TEXT NOT NULL     -- relative path under .llmwiki/answers/
);
CREATE VIRTUAL TABLE saved_answers_fts USING fts5(question, answer, content=saved_answers, content_rowid=id);
```

Schema migrations run in `db.Open()` against `PRAGMA user_version`. Initial migration to v1 creates the tables above and sets version. Existing wikis upgrade silently — old pages stay, with no evidence rows attached.

Page markdown frontmatter gains an `evidence:` block (so the file is self-contained for git diffs and Obsidian).

## Ingest flow

### Tool schema

`internal/wiki/ops.go` `writePagesTool` adds a required `evidence` field per page:

```jsonc
{
  "pages": [{
    "title": "string",
    "body":  "markdown string",
    "links": [{"to": "...", "type": "supports|contradicts|supersedes|related"}],
    "evidence": [{
      "quote":       "verbatim substring of SOURCE",
      "explanation": "(optional) why this quote supports the page"
    }]   // required, min 1 item — enforced server-side after parse
  }]
}
```

Note: the model only emits `quote`. Line numbers are computed server-side.

### System prompt rewrite

```
You write wiki pages strictly grounded in the SOURCE provided.
Every page MUST include `evidence` quotes — verbatim spans copied character-for-character from SOURCE that justify the page's claims.
Do not include general knowledge that is not in SOURCE.
If the SOURCE doesn't contain enough material for a high-quality page on a topic, do not create that page.
Better to return one solid page than five thin ones. Aim for 1–4 pages per chunk.
```

### Validation pass (`wiki.ValidateAndAttachEvidence`)

For each page returned by the LLM:
1. For each evidence quote: check `strings.Contains(sourceContent, quote)`. If false → drop the quote, log `WARN dropped quote in page "X" — not present in source`.
2. If a page ends with zero valid evidence quotes → drop the entire page, log `WARN dropped page "X" — no verifiable evidence`.
3. For surviving quotes, compute `line_start`/`line_end` by counting newlines up to the match index.
4. Return surviving pages with attached evidence list.

### Chunking and concurrency (`cmd/ingest.go`)

- Chunk size raised from 6 KB to **16 KB** (Haiku has 200K context; small chunks are wasteful).
- The `maxChunks = 3` cap is **removed** — process all chunks.
- Replace the unbounded goroutine fan-out with a buffered semaphore: `sem := make(chan struct{}, 5)`. Max 5 inflight LLM calls.
- Progress display: `[3/13] processed, 4 pages, 11 evidence quotes` (rewritten on the same line).

### Anthropic prompt caching

In `internal/llm/anthropic.go`, the system prompt block in `CompleteStructured` gets `cache_control: {type: "ephemeral"}`. With identical system text across N parallel chunks, calls 2..N hit the cache, cutting cost by ~70% on multi-chunk runs. Ollama path is unchanged (no caching).

### Idempotent re-ingest

Existing source-hash dedup stays. New: when re-ingesting a changed source, the page rows tied to the old `source_id` get their `evidence` rows cascade-deleted before the new ingest writes new rows. No stale evidence pointing at superseded text.

## Ask flow

### Retrieval (`cmd/ask.go`)

1. FTS5 search `pages_fts` for top 5 page hits.
2. FTS5 search `evidence_fts` for top 10 quote hits, joined back to their pages.
3. Union: if a page hit also has matching quotes, attach them to that page; orphan quote hits bring in their parent page too.
4. Fallback: if both searches return zero, fall back to "load all pages, take first 5" (current behavior preserved).

### Prompt structure (`wiki.AnswerQuestion`)

```
SYSTEM:
You answer using the provided wiki pages and source quotes.
Cite pages inline with [Page Title]. When using a verbatim quote
from a source, render it as a markdown blockquote and label it.
If pages and quotes are insufficient, say so plainly.

USER:
## Wiki pages
### {title}
{body}
**Source quotes for this page:**
> "{quote 1}"  (lines {a}-{b})
> "{quote 2}"  (lines {c}-{d})
---
Question: {question}
```

### Streaming and rendering (TTY-aware)

- **stdout is a TTY**: stream tokens raw to stdout as they arrive (alive feeling, raw markdown). When the stream completes, clear the streamed lines (carriage returns + ANSI clear) and re-render the full answer with `charmbracelet/glamour` (pretty, colored, properly formatted). Final terminal state is the pretty version. Then append a `── Sources ──` block (also glamour-rendered) listing cited pages with the source-quote spans + line ranges that backed each citation.
- **stdout is piped or redirected** (`> file.md`, `| pbcopy`, `| jq`): no streaming, no glamour, no spinner. Buffer the full answer and emit raw markdown when the LLM is done. The Sources block is plain text.
- **`--no-stream` flag**: forces buffered behavior even on TTY (slow connections, debugging).
- **Ollama path**: non-streaming for now (degraded but functional).

### Auto-archive

Every successful `ask` invocation also writes the answer to `.llmwiki/answers/YYYY-MM-DD-HHMMSS-<question-slug>.md`. File contains question, answer, sources block (with verbatim quotes + line ranges), model name, and timestamp — frontmatter + body, same Obsidian-friendly format as wiki pages. The answer is also indexed in `saved_answers` + `saved_answers_fts` for future retrieval. One terminal line at the end of `ask`: `saved: .llmwiki/answers/2026-05-03-143012-what-deps-llmwiki-uses.md`.

Disabled by `--no-save` flag or `[ask] auto_save = false` in config.

### New flags on `ask`

- `--out PATH` — also write the answer to a specific file (independent of auto-archive).
- `--no-save` — skip auto-archive.
- `--no-stream` — force buffered output.

## CLI surface changes

### `init` — first-run fix

- Writes `provider = "anthropic"`, `model = "claude-haiku-4-5"` (was `ollama` / `llama3.2`).
- After writing config, validates the API key. If Anthropic and `ANTHROPIC_API_KEY` is empty:
  ```
  Error: ANTHROPIC_API_KEY is not set.
    Get a key at https://console.anthropic.com/settings/keys
    Then: export ANTHROPIC_API_KEY=sk-ant-...
    Or use Ollama instead: llmwiki init --provider ollama
  ```
  Exit 2. The config file IS still written so re-run after exporting the key works.

### Root flags

- `--provider {anthropic,ollama}` — one-off override.
- `--model NAME` — one-off override.

Both flags shadow the config file values.

### Key validation

Every command that needs an LLM (`ingest`, `ask`, `lint`) validates the API key in `loadConfig` after provider selection — same error message as `init`. No silent failures.

### Error formatting

Replace bare `fmt.Fprintln(os.Stderr, err)` in `Execute()` with a colored `Error:` prefix using `fatih/color`. Same dep we'd want anyway for richer CLI output later.

### `status` updates

Output gains:
- `evidence_quotes: N` — total verifiable quotes
- `legacy_pages: N` — pages without evidence (pre-migration or failed validation)
- `saved_answers: N` — total auto-archived answers
- `last_ingest: timestamp`

## Testing strategy

### Cassette infrastructure (`internal/llm/cassette.go`, ~150 lines)

- `recordingClient` wraps any `llm.Client`. Modes:
  - `replay` (default in tests): looks up cassette JSON on disk, returns it.
  - `record` (`LLMWIKI_RECORD=1`): calls real client, writes response to disk.
  - `live` (`LLMWIKI_LIVE=1`): bypasses cassette entirely (useful for debugging).
- Cassette files: `internal/llm/testdata/cassettes/<test_name>__<call_index>.json`. Per-test-named (not hash-based) so they're git-diffable and human-readable.
- Format:
  ```json
  {
    "system": "...",
    "user": "...",
    "tool_schema_name": "write_pages",
    "response": { "pages": [...] }
  }
  ```
- Replay matches on (system, user, tool_schema_name) tuple. Mismatch → test failure with a unified diff showing the drift, telling you to re-record.
- Re-record with: `LLMWIKI_RECORD=1 ANTHROPIC_API_KEY=... go test ./...`

### Pure unit tests (no LLM)

- `chunkContent` — boundaries: empty, exact-size, sub-chunk, multi-chunk, no-newlines, trailing-newline.
- `wiki.ParsePage` / `WritePage` round-trip with evidence frontmatter.
- `wiki.ValidateAndAttachEvidence` — quote present, quote absent, partial overlap, multi-byte unicode, line-number computation correctness, all-quotes-invalid drops page.
- `db.Open` migration — fresh DB and "old DB without evidence table" both work; idempotent on re-run.
- `db.SearchPages` and new `db.SearchEvidence` — FTS5 hit/miss/ranking.
- `ingest.IsGitHubURL` — pattern matching.
- `cmd/ask` slug generation for auto-archive filenames.

### Integration tests with cassettes

Four cassettes total, kept small:

1. `TestIngestSmall` — single chunk (~2 KB synthetic source), expects ≥1 page with valid evidence, all quotes substring-matched.
2. `TestIngestMultiChunk` — 50 KB synthetic source, expects all chunks processed, evidence aggregated across pages.
3. `TestAskWithHits` — pre-seeded DB, ask question, assert answer references seeded page titles AND a quoted blockquote appears.
4. `TestAskNoHits` — empty FTS results path, asserts fallback to all-pages works without crashing.

### CI

GitHub Actions, minimal: `go test ./...` on push. Cassette tests run in replay mode (no API key needed). A nightly `LLMWIKI_RECORD=1` job to detect upstream API drift is deferred to sub-project 4.

## Migration / backward compat

- `db.Open` reads `PRAGMA user_version`. If 0, runs migration to v1: creates `evidence`, `evidence_fts`, `saved_answers`, `saved_answers_fts` tables, sets version.
- Existing pages survive — they just have no evidence rows. They show up in `ask` results with a `(no source quotes attached)` annotation in the LLM prompt, which the system prompt instructs the model to handle (treat as wiki-only context).
- `status` surfaces `legacy_pages: N` so the user knows what to re-`ingest`.
- No destructive auto-purge. User upgrades by re-`ingest`-ing the original sources at their pace.

## Implementation order

A rough sequence to keep each step independently testable. The plan-writing pass will refine this further.

1. **Schema + migration** (`internal/db/`): new tables, FTS5 virtual tables, `PRAGMA user_version` migration logic, queries for evidence + saved_answers. Pure unit tests.
2. **Page + evidence types** (`internal/wiki/page.go`): extend `Page` struct with `[]Evidence`; update `WritePage`/`ParsePage` round-trip for new frontmatter. Pure unit tests.
3. **Validation pass** (`internal/wiki/ops.go`): `ValidateAndAttachEvidence` function with line-number computation. Pure unit tests with synthetic sources.
4. **Tool schema + system prompt rewrite** (`internal/wiki/ops.go`): updated `writePagesTool`, new system prompt. Wire through `IngestToPages`.
5. **Cassette infrastructure** (`internal/llm/cassette.go`): record/replay client. First two integration tests (`TestIngestSmall`, `TestIngestMultiChunk`) use this — record real cassettes once.
6. **Chunking + concurrency** (`cmd/ingest.go`): 16 KB chunks, semaphore-bounded fan-out, progress display, no max cap.
7. **Anthropic prompt caching** (`internal/llm/anthropic.go`): `cache_control: ephemeral` on system prompt for `CompleteStructured`.
8. **Idempotent re-ingest** (`internal/db/queries.go` + `cmd/ingest.go`): cascade-delete old evidence before writing new, on source content-hash change.
9. **Ask retrieval** (`cmd/ask.go` + `internal/db/queries.go`): two-table FTS5 union, attach quotes to pages, fallback path. Cassette tests.
10. **Ask streaming + glamour** (`internal/llm/anthropic.go`, `cmd/ask.go`): TTY detection (`go-isatty`), streaming Anthropic calls, glamour render on completion, `--no-stream` flag.
11. **Auto-archive** (`cmd/ask.go`, `internal/wiki/answer.go`): write to `.llmwiki/answers/`, index in `saved_answers`, `--out` and `--no-save` flags.
12. **`init` defaults + key validation** (`cmd/init.go`, `cmd/root.go`): switch defaults to Anthropic/Haiku, validate key in `loadConfig`, helpful error message.
13. **Error formatting** (`cmd/root.go`): `fatih/color`-prefixed `Error:` output in `Execute`.
14. **`status` updates** (`cmd/status.go`): new fields.
15. **CI workflow** (`.github/workflows/test.yml`): `go test ./...` on push.
16. **README** (just for this sub-project's surface — full launch README is sub-project 4).

## Verification

```bash
# Fresh first-run from a clean checkout
unset ANTHROPIC_API_KEY
./llmwiki init           # exits 2 with helpful error
export ANTHROPIC_API_KEY=sk-ant-...
./llmwiki init           # writes config, succeeds

# Trustworthy ingest
./llmwiki ingest ./README.md
# Expect: progress display, every page has evidence in frontmatter,
# no "JSON Processing in Go"-style hallucinated pages.

# Multi-chunk ingest
./llmwiki ingest ./internal/                 # 30+ files, ~80 KB total
# Expect: all chunks processed, [N/M] progress, no silent drops.

# Streaming ask, terminal
./llmwiki ask "what does the validation pass do?"
# Expect: streamed tokens, then re-rendered pretty answer with sources block,
# with verbatim quotes from the source.

# Piped ask
./llmwiki ask "what does the validation pass do?" > answer.md
# Expect: raw markdown, no ANSI codes, no spinner.

# Auto-archive
ls .llmwiki/answers/
# Expect: timestamped markdown files, one per successful ask.

# Status
./llmwiki status
# Expect: evidence_quotes, legacy_pages, saved_answers fields populated.

# Tests
go test ./...
# Expect: all green in replay mode, no API key needed.
```
