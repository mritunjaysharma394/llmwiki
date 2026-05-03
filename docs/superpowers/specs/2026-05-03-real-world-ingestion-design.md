# Sub-project 3 — Real-world Ingestion

**Status:** design approved, awaiting implementation plan
**Date:** 2026-05-03
**Author:** Mritunjay Sharma (with Claude)

## Context

Sub-project 1 made `llmwiki ingest` trustworthy: every page is grounded in verbatim quotes that substring-match the source. But "the source" today is whatever `internal/ingest/{file,url,github}.go` happens to return as a single concatenated string, and those three readers are minimal to the point of being unusable on real inputs.

A smoke test against the kinds of sources the user actually has on their machine reveals the failure modes:

- **PDFs:** completely unsupported. `ReadLocal` opens the file, `isText` sees a null byte in the header, returns `""`. The CLI then errors with `no content found in source`. PDF-served URLs hit `htmlToText` which produces line noise.
- **URLs:** `htmlToText` is regex-based tag stripping (`<[^>]*>` → space). Site nav/footer/ads/cookie banners all end up in the source. No `<article>`/`<main>` extraction. No content-type sniffing beyond `text/html` substring. No redirect tracking, no User-Agent (many sites 403 the default Go UA), no timeout. PDF URLs are not detected.
- **GitHub repos:** `FetchGitHub` shells out to `git clone`, then preferentially reads `docs/`, falling back to `ReadLocal` over the whole tree. The whole-tree path concatenates every text-extension file with `=== path ===` headers and no size cap, no `.gitignore` respect, no skip list for `vendor/`, `node_modules/`, `target/`, generated dirs, lockfiles, minified bundles. A 5MB lockfile becomes 5MB of LLM tokens.
- **Directories:** same `readDir` path — same problem. `internal/` (~80KB) works; the user's `~/notes/` (containing PDFs, images, large logs) does not.
- **Re-ingest granularity:** dedup is whole-source. The user fixes one typo in one file inside a 50-file repo → the entire repo re-ingests, all chunks go to the LLM, all evidence rows for the source get cascade-deleted and rewritten. Per-file change detection is missing.

Sub-project 1's substring validation also creaks here. Today the validator does `strings.Contains(concatenatedSource, quote)`. In a 500KB blob with 50 files, a quote that "happens to appear" in one file gets attributed to the source as a whole, with line numbers computed against the concatenation — useless for the user who wants to know which file backs a claim.

This sub-project makes ingest actually usable on real-world inputs while strengthening the trust property to per-file granularity.

## Goals

1. **PDF ingestion works** for both local files and `application/pdf` URLs, extracting text with usable line/page mapping; OCR-only PDFs are detected and skipped with a clear warning.
2. **URL ingestion produces clean Markdown** — Readability-style article extraction, then HTML→MD conversion. Nav/footer/script/style stripped before the LLM ever sees the content. Content-type sniffing dispatches PDF URLs to the PDF path.
3. **Directory and repo ingestion respects skip rules** — `.gitignore`, a built-in deny list, per-file size cap, configurable extension allow/deny — so re-running on `~/notes/` or a real repo doesn't blow through tokens on `node_modules/`.
4. **Multi-file evidence is anchored to specific files**, not to a 500KB concatenation. The validator and `ask` UX know which file backed every quote.
5. **Per-file incremental re-ingest** — only files whose hash changed get re-processed. One typo doesn't invalidate the whole source.
6. **Existing wikis upgrade silently** — schema migration to v2 is additive; pre-v2 evidence rows keep working at source-level granularity.

## Non-goals (deferred)

- OCR for scanned PDFs — out of scope; warn and skip. Sub-project 4 may revisit if Tesseract bindings stabilize.
- JavaScript-rendered pages (SPA scraping, headless browser) — punted. Sub-project 2's web UI may add a "paste rendered HTML" affordance.
- Twitter/X, YouTube, Notion, Slack, Gmail connectors — punted to sub-project 4 (launch surface).
- Audio/video transcription — out of scope.
- robots.txt enforcement — `llmwiki` is a personal CLI run by the user against URLs they choose; treating it as a crawler is wrong framing. Add a `User-Agent: llmwiki/<version>` header and call it done.
- Re-chunking on file-boundary changes (i.e. detecting that file A grew and now spills into a new chunk). Per-file hashing handles content drift; chunk reshuffling is a sub-project 4 concern.

## Architecture overview

The load-bearing invariant for this sub-project:

> Every `Evidence` row is anchored to a specific `SourceFile` (a single file inside a directory/repo, or a single page inside a PDF, or the whole document for an HTML/text source). Its `Quote` is a verbatim substring of *that file's* content, and its `LineStart`/`LineEnd` are line numbers within that file.

This is a strict generalization of sub-project 1's invariant. A single-file source has exactly one `SourceFile` and behavior is identical to today. A 50-file repo has 50 `SourceFile` rows; a 12-page PDF has 12. The validator now needs `(quote, source_file_path)` from the LLM, not just `quote` — and it looks up that named file's content to substring-match against. Quotes that don't name a known file, or that don't substring-match the named file, are dropped with a warning, same as today.

Two consequences:

- The chunk passed to `IngestToPages` carries explicit file boundaries — file headers like `=== path/to/file.go ===` aren't decoration, they're part of the contract. The system prompt tells the LLM to emit `source_file` along with each `quote`, naming the file the quote was copied from.
- Per-file content hashing is a natural drop-out. We have `SourceFile` rows already; storing each one's hash gives us incremental re-ingest for free.

Ingest itself moves from "fetch a string, chunk it, validate against the string" to a four-stage pipeline:

```
fetch → normalize → chunk-with-anchors → validate-against-anchors
```

`fetch` returns `[]SourceFile` (path + content + page-or-section metadata) instead of `string`. `normalize` is per-source-type (HTML cleaning, PDF text extraction, etc.). `chunk-with-anchors` packs SourceFiles into LLM-sized chunks while preserving file headers; chunks never split a file in half unless the file alone exceeds chunk size (in which case we split on line boundaries and the chunk header notes the line offset). `validate-against-anchors` is sub-project 1's pass with file-aware lookup.

## Schema changes

One new table, one column added to `evidence`. Migration to user_version 2.

```sql
CREATE TABLE source_files (
  id INTEGER PRIMARY KEY,
  source_id INTEGER NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
  relative_path TEXT NOT NULL,    -- "internal/db/db.go" or "page-3" for PDFs
  content_hash TEXT NOT NULL,
  byte_size INTEGER NOT NULL,
  line_count INTEGER NOT NULL,
  ingested_at DATETIME DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(source_id, relative_path)
);
CREATE INDEX idx_source_files_source ON source_files(source_id);

ALTER TABLE evidence ADD COLUMN source_file_id INTEGER
  REFERENCES source_files(id) ON DELETE CASCADE;
CREATE INDEX idx_evidence_source_file ON evidence(source_file_id);

PRAGMA user_version = 2;
```

`source_file_id` is nullable so v1 evidence rows survive untouched (they keep pointing at `source_id` only and are treated as "legacy: file unknown" by `ask`). New evidence rows always populate it.

For PDFs, `relative_path` uses the synthetic form `page-{N}` (e.g. `page-3`) and `line_count` is the line count of the extracted text on that page. For URL sources `relative_path` is `index.html` (single SourceFile per URL).

## Per-source-type design

### Local files (`internal/ingest/local.go`, replacing `file.go`)

Single-file path: identical to today for text files (returns one `SourceFile{relative_path: filepath.Base(path)}`). PDF dispatch added: if `filepath.Ext(path) == ".pdf"` or first 4 bytes are `%PDF`, route through the PDF reader.

Directory path is rewritten:

- Walk with `filepath.WalkDir`.
- For each entry: apply skip rules in order:
  1. Built-in deny list (dirs): `.git`, `node_modules`, `vendor`, `target`, `dist`, `build`, `.venv`, `venv`, `__pycache__`, `.cache`, `.next`, `coverage`, `.pytest_cache`. Skip whole subtree.
  2. Built-in deny list (files): `*.lock`, `package-lock.json`, `yarn.lock`, `Cargo.lock`, `go.sum` (kept on a fence — opt-in via config), `*.min.js`, `*.min.css`, `*.map`, `*.jpg`, `*.jpeg`, `*.png`, `*.gif`, `*.webp`, `*.ico`, `*.pdf` (handled separately), `*.zip`, `*.tar`, `*.gz`, `*.exe`, `*.dll`, `*.so`, `*.dylib`, `*.wasm`, `*.class`, `*.jar`.
  3. `.gitignore` rules from the directory root (using `sabhiram/go-gitignore`). Hierarchical `.gitignore` is out of scope for v1 — root-level only.
  4. Size cap: per-file > `[ingest] max_file_bytes` (default 256 KB) → log `WARN skipping {path}: {size} > max_file_bytes` and skip.
  5. Binary detection: existing `isText` heuristic (null byte in first 512 bytes) — keep as last-resort filter.
- PDFs encountered during the walk get extracted and contribute one `SourceFile` *per page* (path becomes `{relative_path}#page-{N}`).
- Result: `[]SourceFile` with `relative_path` as the path relative to the walked directory root.

### URL (`internal/ingest/url.go`, rewritten)

Single `http.Client` with:
- Timeout: 30s default, configurable as `[ingest] http_timeout_seconds`.
- `User-Agent: llmwiki/<version>` header.
- Follow up to 10 redirects (Go default).
- 5 MB max body via `io.LimitReader`.

Flow:

1. GET. If status >= 400 → error.
2. Sniff `Content-Type`. Strip parameters before matching:
   - `application/pdf` or path ends in `.pdf` → buffer body to temp file → dispatch to PDF reader → return `[]SourceFile` with `relative_path = "page-N"`.
   - `text/html`, `application/xhtml+xml` → HTML pipeline (below).
   - `text/plain`, `text/markdown`, anything else `text/*` → return one `SourceFile` with the raw body.
   - Anything else → error: `unsupported content-type {ct} for URL ingestion`.
3. HTML pipeline:
   a. Run through `go-shiori/go-readability` to extract `<article>`/`<main>` content. If Readability fails (some pages are too dynamic), fall back to the body of `<body>` minus `<script>`/`<style>`/`<nav>`/`<footer>`/`<aside>`/`<header>` (handled by `html-to-markdown`'s default rules anyway).
   b. Run the result through `JohannesKaufmann/html-to-markdown/v2` to produce clean Markdown.
   c. Return one `SourceFile` with `relative_path = "index.html"` (or the URL path, sanitized) and content = the Markdown.

Title and canonical URL from `<title>`/`<link rel=canonical>` are captured into `Source.URI` metadata for nicer `ask` output, but don't affect the SourceFile.

### PDF (`internal/ingest/pdf.go`, new)

Library: **`github.com/ledongthuc/pdf`** (BSD-3-Clause, pure Go, no CGo, ~1.6k stars, last release 2024). Considered alternatives:

- `pdfcpu/pdfcpu`: Apache-2, pure Go, but its primary mission is PDF manipulation (split/merge/encrypt). Text extraction is via the `extract` subcommand and the API surface is heavier than what we need.
- `unidoc/unipdf`: AGPL with commercial license. Disqualifying for a personal-but-MIT project the user might open-source.
- `gen2brain/go-fitz`: bindings to MuPDF — best quality, but CGo-only and would force every contributor onto a working MuPDF dev environment. Not worth it.

`ledongthuc/pdf` is the smallest defensible thing. Its `GetPlainText()` returns the whole document as a string; its `GetTextByRow()` returns rows per page. We use the per-page API.

Flow (`ReadPDF(path string) ([]SourceFile, error)`):

1. Open with `pdf.Open(path)`.
2. For each page 1..N:
   a. Call `page.GetTextByRow()` to get text rows in reading order.
   b. Join rows with `\n`. Strip leading/trailing whitespace.
   c. If extracted text length < `[ingest] pdf_min_text_per_page` (default 50) characters and the page has `> 0` images → almost certainly scanned. Log `WARN page {N} of {path}: appears to be scanned/OCR-only ({len} chars extracted), skipping`.
   d. Otherwise, append `SourceFile{relative_path: fmt.Sprintf("page-%d", N), content: text}`.
3. If *all* pages were skipped → error: `no extractable text in PDF (likely scanned)`.

Validation invariant holds: each PDF page is its own SourceFile, line numbers are within that page, the LLM's `source_file` claim names a `page-N`. The user sees `(page 3, lines 4-8)` in `ask` output rather than line numbers in a 12-page concatenation.

### GitHub repo (`internal/ingest/github.go`, rewritten)

Flow:

1. `git clone --depth=1 --filter=blob:none {url} {tmpDir}` (existing, kept).
2. Delegate to the directory walker on `tmpDir`. The whole skip-list machinery — `.gitignore`, deny list, size cap — runs uniformly.
3. The "prefer `docs/`" hack from the current implementation is **removed**. It was a workaround for the lack of skip rules; with proper rules, walking the whole repo with vendored/generated content excluded gives consistently better results, and `docs/` content is still picked up.
4. `relative_path` is relative to the cloned repo root, so the user sees `internal/db/db.go` in `ask` output.
5. `defer os.RemoveAll(tmpDir)` — same as today.

`IsGitHubURL` is unchanged. The function returns `[]SourceFile` from the directory walker.

### Directory ingest improvements

Already covered above — the directory path inside `local.go` is the same code that GitHub uses post-clone. Skip rules apply uniformly.

## Chunking with file anchors

`internal/ingest/chunk.go` (new, replacing `cmd/ingest.go`'s `chunkContent`).

```go
type Chunk struct {
    Header string         // human-readable description for progress display
    Text   string         // payload sent to LLM, includes "=== path ===" file headers
    Files  []SourceFile   // SourceFiles included (or partially included) in this chunk
}

func ChunkSourceFiles(files []SourceFile, maxBytes int) []Chunk
```

Algorithm:
- Greedy bin-packing. Open a new chunk; for each file, append `=== {relative_path} ===\n{content}\n\n` if it fits in remaining budget; else flush and start a new chunk.
- File larger than `maxBytes`: split on line boundaries, emit consecutive chunks each prefixed with `=== {relative_path} (lines {a}-{b}) ===\n`. The split-line metadata is purely advisory; the validator still uses `(quote, source_file)` and line numbers within the original file content stored in the DB.
- Each chunk's `Text` is what gets passed to the LLM as `SOURCE`. The `=== ... ===` markers become part of the prompt contract: the LLM sees them and the system prompt tells it to use the file path verbatim in the `source_file` field of evidence.

Default `maxBytes` stays at 16 KB (sub-project 1 baseline). New config: `[ingest] chunk_size_bytes`.

## Tool schema and validation updates

`writePagesTool` evidence item gains a required `source_file` field:

```jsonc
"evidence": [{
  "quote":       "verbatim substring of SOURCE_FILE",
  "source_file": "the path shown in the === path === marker the quote came from",
  "explanation": "(optional) why this quote supports the page"
}]
```

System prompt addendum:

```
The SOURCE may contain multiple files, each delimited by a header line:
    === path/to/file.ext ===
For every evidence quote, set "source_file" to the exact path shown in the
header above the file the quote was copied from. Quotes from different files
must each have their own evidence entry naming the correct file.
```

`ValidateAndAttachEvidence` gains a `files []SourceFile` parameter (replacing `source string`):

```go
func ValidateAndAttachEvidence(pages []Page, files []SourceFile) ([]Page, int)
```

Per quote:
1. Look up the named `source_file` in the `files` slice (build a `map[string]*SourceFile` once).
2. If not found → drop, log `WARN dropped quote in page {p}: source_file {f} not in this chunk`.
3. If `strings.Contains(file.Content, quote)` is false → drop, log `WARN dropped quote in page {p}: quote not in {f}`.
4. Compute `LineStart`/`LineEnd` against `file.Content`, not the chunk concat.
5. Attach `SourceFilePath` to the `Evidence` struct (new field) so the writer knows which `source_files.id` to point at.

Backward compat: if the LLM omits `source_file` (older cassettes, or model regression), fall back to "search all files for the quote" — first match wins. Log `WARN quote in page {p} missing source_file, attributed to {f} by content match`. This keeps `TestIngestSmall` green without re-recording cassettes for the trivial single-file case.

## Per-file dedup and incremental re-ingest

`cmd/ingest.go` orchestration changes:

1. After `fetch` returns `[]SourceFile`, compute each file's `content_hash`.
2. Look up existing `source_files` rows for this `source_id`. Build a map `existingByPath`.
3. Partition incoming files into:
   - **unchanged**: hash matches existing row → skip entirely.
   - **changed**: hash differs from existing row → re-ingest; existing evidence rows for that `source_file_id` get deleted before new ones land.
   - **new**: no existing row → ingest.
   - **gone**: in `existingByPath` but not in incoming → delete the `source_files` row (cascades to evidence). The pages those quotes supported lose evidence; if a page ends up with zero evidence rows it's flagged as `legacy_pages` in `status` (existing field, no new schema).
4. Run only changed + new files through chunking and the LLM. If `len(changed) + len(new) == 0` → print `Source unchanged at file level, skipping.` and exit 0.

The existing whole-source `content_hash` on `sources` becomes a fast-path: hash the concatenation of all incoming file hashes (not the content), compare to the stored value; if equal, skip entirely without touching the DB. This keeps the "clone-and-skip" path cheap on unchanged repos.

`UpsertSourceFile`, `DeleteSourceFile`, `GetSourceFiles(sourceID)` queries land in `internal/db/queries.go`.

## Config additions

New `[ingest]` block in `.llmwiki/config.toml`:

```toml
[ingest]
# Per-file size limit. Files larger than this are skipped with a warning.
max_file_bytes = 262144  # 256 KB

# Chunk size for LLM calls. Sub-project 1 default; here for visibility.
chunk_size_bytes = 16384

# HTTP request timeout (URL ingest).
http_timeout_seconds = 30

# Max bytes downloaded per URL.
http_max_bytes = 5242880  # 5 MB

# PDF: minimum extracted text chars per page below which page is treated as scanned.
pdf_min_text_per_page = 50

# Extra extensions to allow (beyond the built-in text-extension allow-list).
# Useful for adding e.g. ".lua", ".tex".
extra_text_extensions = []

# Extra glob patterns to skip (added to the built-in deny list).
extra_skip_globs = []

# Honor .gitignore at the directory root when walking. Default true.
respect_gitignore = true
```

`init` writes these into the default config; `loadConfig` fills zero values with sensible defaults so users with pre-sub-project-3 configs don't break.

## CLI surface changes

### `ingest` flags

- `--max-file-bytes N` — override `[ingest] max_file_bytes` for this run.
- `--include EXT[,EXT...]` — restrict ingest to files with these extensions (one-shot allowlist).
- `--exclude GLOB[,GLOB...]` — extra skip globs (additive to config).
- `--no-gitignore` — ignore `.gitignore` for this run.
- `--force` — ignore the per-file unchanged check; re-ingest everything matching. Useful when prompt or model changes mean you want fresh pages from unchanged content.

### Output format

Multi-file ingest gets a richer progress display:

```
Walking ./internal (47 files, skipped 12: gitignore, 4: size cap)
Hashing 47 files... 47 new, 0 changed, 0 unchanged
Packing into 6 chunks (max 5 in flight)
  [6/6] processed
  ✓ Database Layer (3 evidence, files: db/db.go, db/queries.go)
  ✓ Ingest Pipeline (5 evidence, files: ingest/local.go, ingest/url.go, ...)
Ingested 12 page(s) from ./internal
```

The "files:" annotation in the per-page line lists the distinct `source_file` paths backing that page's evidence. Useful for the user to spot weirdness ("why does this page about HTTP servers cite db.go?").

### `ask` rendering

Sub-project 1 renders evidence as `> "{quote}" (lines a-b)`. Now: `> "{quote}" ({source_file}:a-b)`. PDFs render as `(page-3:4-8)`.

This is a one-line change in `wiki.buildAnswerUserPrompt` and the Sources block in `cmd/ask.go`.

### `status` updates

Add:
- `total_source_files: N`
- `largest_source: {uri} ({N} files)` — diagnostic for "what is taking up space"

## Dependency choices summary

New direct dependencies:

| Package                                      | Why                                | License | Last release |
|----------------------------------------------|------------------------------------|---------|--------------|
| `github.com/ledongthuc/pdf`                  | PDF text extraction                | BSD-3   | 2024         |
| `github.com/JohannesKaufmann/html-to-markdown/v2` | HTML→Markdown conversion       | MIT     | 2024         |
| `github.com/go-shiori/go-readability`        | Readability article extraction     | MIT     | 2024         |
| `github.com/sabhiram/go-gitignore`           | `.gitignore` matching              | MIT     | stable, last touched 2021 (small enough) |

`microcosm-cc/bluemonday` is already in `go.sum` (transitive from glamour) but is a sanitizer, not a converter — we don't promote it to a direct dep.

Total binary size impact: approximately +3-4 MB (mostly `ledongthuc/pdf`'s font tables).

## Testing strategy

### Pure unit tests (no LLM)

- `ingest.ReadPDF` against three fixtures in `internal/ingest/testdata/pdfs/`:
  - `simple.pdf` — 2-page text PDF, asserts 2 SourceFiles, line-count plausible.
  - `scanned.pdf` — one-page image-only scan, asserts skip-with-warning behavior.
  - `mixed.pdf` — 3 pages, 1 scanned + 2 textual, asserts 2 SourceFiles returned.
- `ingest.FetchURL` with `httptest.Server` fixtures in `internal/ingest/testdata/urls/`:
  - HTML page with nav/footer noise → asserts the noise is gone, article body present.
  - `application/pdf` URL serving a PDF fixture → asserts dispatch to PDF path.
  - `text/plain` URL → asserts raw passthrough.
  - 5xx response → asserts error.
  - Body > 5 MB limit → asserts truncation/error.
- `ingest.WalkDir` against `internal/ingest/testdata/dirs/sample/` containing a real-ish layout (`.git/`, `node_modules/`, `package-lock.json`, `src/main.go`, `vendor/foo.go`, `README.md`, `image.png`, `huge.txt` 500KB):
  - asserts `node_modules`/`.git`/`vendor` skipped, lockfile skipped, image skipped, `huge.txt` skipped (size cap), only `src/main.go` + `README.md` returned.
  - With a `.gitignore` adding `src/`, asserts `src/main.go` is also skipped.
- `ingest.ChunkSourceFiles` — bin-packing correctness, oversized file splits with line-range markers, no-files edge case.
- `wiki.ValidateAndAttachEvidence` (extended): per-file lookup, missing-source-file fallback, unicode line numbers, multi-file evidence on one page.
- `db` migrations: v1→v2 idempotent; v0→v2 fresh; `source_files` upsert/delete/cascade.

### Integration tests with cassettes

Two new cassette tests in `cmd/ingest_integration_test.go` (existing helper):

5. `TestIngestPDF` — uses `internal/ingest/testdata/pdfs/simple.pdf` (a 2-page Go cheat-sheet generated once via `pandoc`), asserts ingest produces ≥1 page, all evidence quotes substring-match the named page's text, `source_file` looks like `page-N`.
6. `TestIngestRepo` — small synthetic repo fixture under `internal/ingest/testdata/dirs/minirepo/` containing `README.md`, `main.go`, `go.mod`, plus a `node_modules/foo.js` to assert it's skipped. Asserts evidence is anchored to the right files.

Existing `TestIngestSmall` and `TestIngestMultiChunk` continue to work — the missing-`source_file` fallback in the validator means cassettes don't need re-recording.

### Purely manual

- Run against `~/notes/` (user's actual notes dir) — exercise the deny-list/size-cap with a real input.
- Run against `https://en.wikipedia.org/wiki/Goroutine` (or similar) — exercise the article extractor on a real URL.
- Run against a 30-page arxiv PDF URL — end-to-end PDF-via-HTTP path.
- Run against a non-trivial GitHub repo (e.g. `golang/example`) — exercise the repo walker + chunker.

### CI

`go test ./...` continues to run on push (sub-project 1's workflow). PDF and URL fixture tests are pure unit (no API key). Cassette tests for the new ingest paths run in replay mode.

## Migration / backward compat

- `db.Open` runs the v2 migration: `CREATE TABLE source_files`, `ALTER TABLE evidence ADD COLUMN source_file_id`, `PRAGMA user_version = 2`. Idempotent.
- Existing v1 evidence rows have `source_file_id = NULL`. `GetEvidenceForPage` returns them as-is; `ask` renders them with the old `(lines a-b)` format (no file annotation) — they pre-date the per-file invariant, no information was lost.
- Existing `sources` rows survive. On next `ingest` of a known URI, the per-file dedup path runs against the (empty) `source_files` rows, so the whole source is treated as new — exactly equivalent to the sub-project 1 behavior. The user's first re-ingest does the migration work; subsequent re-ingests are incremental.
- Pre-sub-project-3 config files: missing `[ingest]` block → defaults applied silently. No `init` re-run needed.
- The `--force` flag exists for the case where someone *wants* to invalidate per-file dedup after a prompt change.

## Implementation order

Roughly. Plan-writing pass refines.

1. **Schema migration v2** (`internal/db/db.go`, `queries.go`): `source_files` table, `evidence.source_file_id` column, queries. Pure unit tests.
2. **`SourceFile` type and chunker** (`internal/ingest/types.go`, `chunk.go`): the type, `ChunkSourceFiles`, tests for bin-packing.
3. **Refactor `ReadLocal` to return `[]SourceFile`** (`internal/ingest/local.go`, replacing `file.go`): walker with skip rules, gitignore, size cap, deny list. Tests against testdata fixtures.
4. **Validator update** (`internal/wiki/ops.go`): `ValidateAndAttachEvidence([]Page, []SourceFile)`, system prompt addendum, tool schema gains `source_file`. Update `IngestToPages` signature.
5. **Wire ingest pipeline** (`cmd/ingest.go`): consume `[]SourceFile`, run chunker, pass `[]SourceFile` to validator, write `source_files` + `evidence.source_file_id`. Per-file dedup logic.
6. **PDF ingest** (`internal/ingest/pdf.go`): `ledongthuc/pdf`, page-by-page extraction, scanned-detection heuristic. PDF fixtures + tests.
7. **URL ingest rewrite** (`internal/ingest/url.go`): `http.Client` with timeout/UA/limit, content-type dispatch, PDF route, Readability + html-to-markdown pipeline. `httptest`-based tests.
8. **GitHub ingest delegation** (`internal/ingest/github.go`): clone → delegate to walker. Drop the docs-preference hack.
9. **Config additions** (`cmd/root.go`, `cmd/init.go`): `IngestConfig` struct, defaults, init template.
10. **CLI flags** (`cmd/ingest.go`): `--max-file-bytes`, `--include`, `--exclude`, `--no-gitignore`, `--force`.
11. **Output / `ask` rendering updates** (`internal/wiki/ops.go`, `cmd/ask.go`): `(file:a-b)` annotation, page-line "files:" annotation.
12. **`status` updates** (`cmd/status.go`): `total_source_files`, `largest_source`.
13. **PDF + repo cassette tests** (`cmd/ingest_integration_test.go`).
14. **Manual verification on real inputs** — checklist below.

## Verification

```bash
# Build
go build -o ./llmwiki ./

# PDF ingestion (local)
./llmwiki ingest ~/Downloads/some-paper.pdf
# Expect: per-page progress, evidence quotes annotated with page-N, no
# "no content found" error.

# Scanned PDF
./llmwiki ingest ~/Downloads/scanned-doc.pdf
# Expect: WARN per page about scanned/OCR-only, exits with helpful error
# if every page is scanned.

# PDF over HTTP
./llmwiki ingest https://arxiv.org/pdf/2310.06825.pdf
# Expect: content-type sniffed as application/pdf, dispatched to PDF path,
# pages ingested.

# URL with article extraction
./llmwiki ingest https://en.wikipedia.org/wiki/Goroutine
# Expect: clean Markdown body (no "Main page | Contents | Current events"
# nav text), evidence quotes match article body.

# Real repo
./llmwiki ingest https://github.com/golang/example
# Expect: walks tree, skips .git, no node_modules or vendor noise.
# Pages cite specific files by path.

# Personal notes directory
./llmwiki ingest ~/notes/
# Expect: PDFs in the dir get extracted, images/binaries skipped, large
# files warned-and-skipped, .obsidian/ skipped.

# Incremental re-ingest
echo "// new comment" >> ./internal/ingest/local.go
./llmwiki ingest ./internal/
# Expect: "47 files: 1 changed, 46 unchanged" — only one file goes to LLM.

./llmwiki ingest ./internal/   # second run, no changes
# Expect: "Source unchanged at file level, skipping." exit 0, zero LLM calls.

# Force re-ingest
./llmwiki ingest --force ./internal/
# Expect: all files re-ingested.

# Ask citing files
./llmwiki ask "what does the chunker do?"
# Expect: answer includes blockquote like
#   > "Greedy bin-packing..." (internal/ingest/chunk.go:8-12)

# Status
./llmwiki status
# Expect: total_source_files populated, largest_source named.

# Tests
go test ./...
# Expect: green; pdf/url unit tests pass without API key; cassette tests
# replay; new TestIngestPDF and TestIngestRepo replay if cassettes recorded
# (skip otherwise).
```
