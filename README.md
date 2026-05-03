# llmwiki

`llmwiki` ingests sources (files, URLs, repos, PDFs, RSS/Atom feeds, sitemaps)
and synthesizes them into a Markdown wiki, with answers grounded in verbatim
source quotes. Trust comes from validation: every page that ships includes
evidence quotes that are byte-exact substrings of the original source —
hallucinated pages are dropped before they hit disk.

![llmwiki demo](docs/assets/demo.png)
<!-- TODO: regenerate via tools/record-demo.sh -->

## Install

Three real install paths:

```bash
# 1. From source via go install (requires Go 1.26+):
go install github.com/mritunjaysharma394/llmwiki@latest

# 2. Pre-built binary from GitHub Releases (replace OS/ARCH as needed):
curl -fsSL https://github.com/mritunjaysharma394/llmwiki/releases/latest/download/llmwiki-darwin-arm64.tar.gz \
  | tar -xz -C /usr/local/bin

# 3. From a clean checkout:
git clone https://github.com/mritunjaysharma394/llmwiki.git
cd llmwiki && make install   # installs to $HOME/.local/bin
```

Verify:

```bash
llmwiki version
# llmwiki <semver> (commit <sha>, built <date>, <go-version>)

llmwiki --version   # same output
```

## Quickstart

```bash
export ANTHROPIC_API_KEY=sk-ant-...
mkdir my-wiki && cd my-wiki
llmwiki init
llmwiki ingest https://github.com/golang/example
llmwiki ask "what does the gotypes example do?"
```

To use a local model instead of the Anthropic API (no key required):

```bash
llmwiki init --provider ollama   # writes a config that points at Ollama
```

Run `llmwiki status` to see the wiki state at any time.

## Ingestion sources

`llmwiki ingest <source>` accepts every shape below. Content-type sniffing
auto-routes URLs; the `--feed` and `--sitemap` flags override the dispatch
when sniffing isn't enough.

### Single file

```bash
llmwiki ingest ./notes.md
llmwiki ingest ./paper.pdf
```

### Directory

```bash
llmwiki ingest ./my-project/
```

The walker honors `.gitignore` by default and skips `.git`, `node_modules`,
`vendor`, lockfiles, and binary blobs. Override with flags:

```bash
llmwiki ingest ./my-project/ --no-gitignore --include=.md,.go --exclude=vendor/*
```

### URL (HTML page or PDF)

```bash
llmwiki ingest https://example.com/article
```

HTML pages are passed through Readability + html-to-markdown; PDF URLs go
through the PDF text-extraction path.

### GitHub repository

```bash
llmwiki ingest https://github.com/golang/example
```

Shallow-cloned to a temp dir, walked with the same per-file size cap and
`.gitignore` rules as a local directory.

### PDF (file or URL)

```bash
llmwiki ingest ./paper.pdf
llmwiki ingest https://example.com/whitepaper.pdf
```

Text PDFs are extracted page-by-page. Scanned/OCR-only pages are detected and
skipped with a warning — OCR is not supported in v1.0.

### RSS/Atom/JSON Feed

```bash
llmwiki ingest --feed https://example.com/feed.atom
llmwiki ingest --feed https://example.com/rss.xml --max-pages 20
```

Each feed entry becomes its own `SourceFile` under one `sources` row. Polite
defaults: 1 request/second, cap of 50 entries (override with `--max-pages`
or via `[ingest]` config).

### Sitemap

```bash
llmwiki ingest --sitemap https://example.com/sitemap.xml --max-pages 100
```

Each URL in the sitemap becomes a `SourceFile` via the URL pipeline.
Sitemap-of-sitemaps recursion is supported one level deep. Default cap: 200
pages.

### Re-ingest behavior

Re-running `ingest` against the same source is incremental: per-file content
hashing skips unchanged files. When a file's content changes, neighbours that
were packed in the same prior chunk are re-processed too, so cross-file
synthesis stays stable. Pass `--no-rechunk` to skip the co-resident pass and
only re-process files whose own content changed; `--force` re-ingests
everything.

## Asking questions

```bash
llmwiki ask "what does the chunker do?"
```

Streams the answer in a TTY; pass `--no-stream` for buffered output. Every
quote in the answer is anchored to a specific file (or PDF page) and
rendered as `(path/to/file.go:lines)`. The full transcript is auto-archived
under `.llmwiki/answers/` (disable with `--no-save`); pass `--out file.md`
to also write the answer to a custom path.

## Trust model

Every page is gated on validation: the LLM emits draft pages with quoted
evidence; only quotes that are byte-exact substrings of the original source
become evidence rows. Drafts whose quotes don't validate get dropped before
hitting disk. Per-file evidence anchoring means a quote can never be
mis-attributed across files in the same source. See
[`docs/superpowers/specs/2026-05-03-trust-the-output-design.md`](docs/superpowers/specs/2026-05-03-trust-the-output-design.md)
for the full design.

## Privacy

- **Anthropic provider**: source content is sent to the Anthropic API at
  ingest and at ask time.
- **Ollama provider**: everything stays on your machine.
- **`.llmwiki/`** holds the wiki, the SQLite database, the saved answer
  archive, and `config.toml`. It's local and `.gitignore`d by convention.
- No telemetry, ever.

## Configuration

Configuration lives at `.llmwiki/config.toml`, written by `llmwiki init`.
Pre-existing configs missing newer keys silently inherit defaults.

### `[llm]`

| Key          | Default                  | Description                                       |
| ------------ | ------------------------ | ------------------------------------------------- |
| `provider`   | `"anthropic"`            | LLM provider: `"anthropic"` or `"ollama"`         |
| `model`      | `"claude-haiku-4-5"`     | Model identifier passed to the provider           |
| `ollama_url` | `"http://localhost:11434"` | Base URL of the Ollama server                   |

### `[wiki]`

| Key        | Default              | Description                                  |
| ---------- | -------------------- | -------------------------------------------- |
| `wiki_dir` | `".llmwiki/wiki"`    | Directory holding generated Markdown pages   |
| `raw_dir`  | `".llmwiki/raw"`     | Cached raw source content                    |
| `db_path`  | `".llmwiki/wiki.db"` | SQLite database path                         |

### `[ask]`

| Key         | Default | Description                                |
| ----------- | ------- | ------------------------------------------ |
| `auto_save` | `true`  | Archive every answer under `.llmwiki/answers/` |

### `[ingest]`

| Key                       | Default   | Description                                                       |
| ------------------------- | --------- | ----------------------------------------------------------------- |
| `max_file_bytes`          | `262144`  | Per-file size limit (256 KiB)                                     |
| `chunk_size_bytes`        | `16384`   | Target packed-chunk size for LLM calls                            |
| `http_timeout_seconds`    | `30`      | Timeout on URL fetches                                            |
| `http_max_bytes`          | `5242880` | Max URL response body size (5 MiB)                                |
| `pdf_min_text_per_page`   | `50`      | Below this text length a PDF page is treated as scanned, skipped  |
| `extra_text_extensions`   | `[]`      | Additional file extensions the walker treats as text              |
| `extra_skip_globs`        | `[]`      | Additional path globs to skip                                     |
| `respect_gitignore`       | `true`    | Honor `.gitignore` in directory and repo walks                    |
| `feed_request_per_second` | `1.0`     | Polite rate limit for feed/sitemap fetches                        |
| `feed_max_entries`        | `50`      | Max feed entries ingested per fetch                               |
| `sitemap_max_pages`       | `200`     | Max URLs crawled from a sitemap                                   |

### Environment variables

| Variable             | Description                                                                       |
| -------------------- | --------------------------------------------------------------------------------- |
| `ANTHROPIC_API_KEY`  | Required when `provider = "anthropic"`. Get one at https://console.anthropic.com/settings/keys |
| `LLMWIKI_CASSETTE`   | When set, the LLM client replays from `internal/llm/testdata/cassettes/<name>__*.json` instead of calling the live API. Used by `make smoke`. |
| `NO_COLOR`           | Disable ANSI colors in CLI output                                                 |

## Architecture

```
                 +----------+        +---------+        +----------+
ingest <source>->|  walk /  |------->|  chunk  |------->|   LLM    |
                 |  fetch   |        |  pack   |        |  draft   |
                 +----------+        +---------+        +----------+
                                                              |
                                                              v
                                                       +-------------+
                                                       |  validate   |
                                                       |  (quote =   |
                                                       |  substring) |
                                                       +-------------+
                                                              |
                                                  +-----------+-----------+
                                                  v                       v
                                            +-----------+           +----------+
                                            |  wiki/    |           |  SQLite  |
                                            |  *.md     |           |  + FTS5  |
                                            +-----------+           +----------+
                                                                          ^
                                                                          |
                              ask <q>  ----> retrieve ----> LLM ---- + render
```

Design specs and plans live under
[`docs/superpowers/specs/`](docs/superpowers/specs/) and
[`docs/superpowers/plans/`](docs/superpowers/plans/).

## Development & contributing

```bash
go build ./...
go test ./...
```

Tests run in cassette **replay** mode by default — no API key required, no
network. To re-record a cassette against the live API:

```bash
LLMWIKI_RECORD=1 ANTHROPIC_API_KEY=sk-ant-... go test ./... -run TestName -v
```

The nightly `cassette-refresh` workflow under `.github/workflows/` re-records
everything against `secrets.ANTHROPIC_API_KEY` and opens a PR if the cassettes
drifted.

`make smoke` runs the README quickstart end-to-end against a tiny synthetic
source, using a recorded cassette so it works without an API key. Note: the
smoke cassette must first be recorded with
`LLMWIKI_RECORD=1 ANTHROPIC_API_KEY=... go test ./cmd/ -run TestSmokeIngestThenAsk -v`
before `make smoke` will pass on a fresh checkout.

## License

Apache-2.0. See [`LICENSE`](LICENSE) and [`CHANGELOG.md`](CHANGELOG.md).

## Acknowledgements

Inspired by Andrej Karpathy's note on building a personal wiki with an LLM.
Thanks to the authors of the dependencies that make this possible —
[`charmbracelet/glamour`](https://github.com/charmbracelet/glamour),
[`mmcdole/gofeed`](https://github.com/mmcdole/gofeed),
[`go-shiori/go-readability`](https://github.com/go-shiori/go-readability),
[`JohannesKaufmann/html-to-markdown`](https://github.com/JohannesKaufmann/html-to-markdown),
the [`spf13/cobra`](https://github.com/spf13/cobra) family, and
[`mattn/go-sqlite3`](https://github.com/mattn/go-sqlite3).
