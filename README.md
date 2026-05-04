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

The fastest path uses Google Gemini's free tier — no credit card, 1M-token
context, generous daily quota. Grab a key at
[aistudio.google.com/apikey](https://aistudio.google.com/apikey) and:

```bash
export GEMINI_API_KEY=...
mkdir my-wiki && cd my-wiki
llmwiki init                        # default provider is gemini
llmwiki ingest https://github.com/golang/example
llmwiki ask "what does the gotypes example do?"
```

Run `llmwiki status` at any time to see what's been ingested.

Already on Anthropic? Pass `--provider anthropic` (or drive llmwiki via your
Claude subscription with no API spend at all — see the MCP section below):

```bash
export ANTHROPIC_API_KEY=sk-ant-...
llmwiki init --provider anthropic
```

To run fully offline against a local model:

```bash
llmwiki init --provider ollama       # writes a config that points at Ollama
```

## Providers

`llmwiki init --provider <name>` picks one of four backends. Pages from any
provider go through the same evidence validator (see Trust model below) — a
quote that doesn't byte-exact substring-match its named source file is
dropped before disk.

| Provider              | Cost                          | Setup                                                                                              | Notes                                                                                       |
| --------------------- | ----------------------------- | -------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------- |
| `gemini` *(default)*  | Free tier; 1M context         | `export GEMINI_API_KEY=...` (get one at https://aistudio.google.com/apikey, no card)               | Default model `gemini-2.0-flash`. Recommended onboarding path.                              |
| `anthropic`           | Pay-per-token API, **or** free via MCP + Pro subscription | `export ANTHROPIC_API_KEY=sk-ant-...`, or skip the API entirely and use the MCP server below       | Default model `claude-haiku-4-5`. Highest quote-fidelity in our cassette tests.             |
| `openai-compatible`   | Many free or near-free tiers  | Edit `[providers.openai_compat]` in `.llmwiki/config.toml` to point at your provider's `/v1` endpoint | Tested against Groq, OpenRouter, Together, Cerebras, Mistral La Plateforme. Free tiers may rate-limit and produce flakier structured output; the validator drops bad pages either way. |
| `ollama`              | Free, fully offline           | `ollama pull llama3.2`, then `llmwiki init --provider ollama`                                      | Runs against `http://localhost:11434` by default. Source content never leaves your machine. |

## Use your Claude subscription via MCP

If you already pay for Claude Pro/Max, you can drive `llmwiki` from Claude
Desktop or Claude Code with **zero API spend** — your subscription token
budget is the only budget. `llmwiki mcp` runs as a Model Context Protocol
stdio server exposing seven tools to the client:

- `list_pages` — list pages (optional title prefix and limit).
- `read_page` — fetch one page's body, frontmatter, evidence, and links.
- `lint` — staleness + contradiction report across the wiki.
- `ask` — grounded Q&A with source quotes.
- `write_page` — propose a new page; **rejected unless every evidence quote
  is a byte-exact substring of the named, already-ingested source file**.
- `ingest` — pull a new source into the wiki.
- `promote_answer` — lifts a saved answer into a real page with the same
  trust validation as `write_page`. See "Living Wiki" below.

### Claude Desktop

Add this to `~/Library/Application Support/Claude/claude_desktop_config.json`
(macOS) or the platform-equivalent path:

```json
{
  "mcpServers": {
    "llmwiki": {
      "command": "llmwiki",
      "args": ["mcp"],
      "env": { "LLMWIKI_DIR": "/Users/me/my-wiki" }
    }
  }
}
```

Restart Claude Desktop. The six tools appear in the tool picker.

### Claude Code

The fastest path is the `claude mcp add` CLI:

```bash
claude mcp add llmwiki -- llmwiki mcp
# or, with an explicit working directory:
claude mcp add llmwiki --env LLMWIKI_DIR=/Users/me/my-wiki -- llmwiki mcp
```

Or hand-edit `~/.config/claude-code/mcp_servers.json` with the same JSON
shape as the Claude Desktop block above.

### What `write_page` actually guarantees

When Claude proposes a new page over MCP, the server runs the proposal
through the same validation pipeline as `llmwiki ingest`:

1. The named `source_file` must already be ingested into this wiki.
2. Every quote in `evidence[]` must be a byte-exact substring of that
   source file on disk.
3. If either check fails, the tool returns a structured error with one of
   `title_exists | evidence_required | source_not_ingested |
   source_not_readable | evidence_invalid | write_failed | bad_request |
   db_error` — the client re-renders the error so you see *why* the write
   was rejected, instead of silently writing bad content.

The MCP server logs to stderr; stdout is reserved for JSON-RPC, so it can
be safely piped from any client.

## Use Obsidian as the UI

`.llmwiki/wiki/` is a plain folder of Markdown files with YAML frontmatter
and `[[wikilinks]]` between pages — i.e. an Obsidian vault. Open the folder
in Obsidian (no plugin required) and you get backlinks, the graph view,
search, and Dataview queries for free.

The vault layout:

- `index.md` — auto-regenerated hub listing every page grouped by source.
  **Don't hand-edit it**; the frontmatter says so. Re-run `llmwiki ingest`
  or `llmwiki write_page` and the index regenerates.
- `log.md` — append-only RFC3339 chronicle of ingest/ask/write events.
- `<Page Title>.md` — one file per page, with `tags`, `sources`, `created`,
  and `updated` keys spelled the way Dataview expects.

For example, this Dataview query lists every page Dataview can see, with
its sources and last-update time:

````markdown
```dataview
table sources, updated FROM "" WHERE contains(tags, "llmwiki")
```
````

Cross-page references in page bodies are written as `[[Page Title]]`;
Obsidian's link autocomplete, backlinks panel, and graph view all pick them
up without configuration.

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
skipped with a warning — OCR is not supported in v0.3.

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

The same validator runs on every code path that writes a page — `ingest`,
`write_page` over MCP, every provider. That gives a single trust property
across the matrix:

> A wiki ingested with Gemini Flash, OpenRouter free-tier models, or Ollama
> may contain fewer pages than the same source ingested with Haiku, but
> every page that lands in the wiki passes the same evidence check.
> Switching to a cheaper model produces a sparser wiki, never a more wrong
> one.

v0.5's three new behaviours (promote, retro-link, contradictions) all
preserve the validator: every page reaching disk has at least one evidence
quote that substring-matches its source — `promote` defensively re-validates
because source files may have changed since the ask.

## Living Wiki

Three additive behaviours layered on v0.4 that keep the wiki current as you
use it. None of them weaken the trust property; all of them are cheap.

### Promote a saved answer into a permanent page

Every `llmwiki ask` archives its transcript under `.llmwiki/answers/`. When
an answer is good enough to keep, lift it into a real wiki page:

```bash
llmwiki ask "how does the validator work?"
ls .llmwiki/answers/
# 2026-05-04-150208-how-does-validator-work.md

llmwiki promote .llmwiki/answers/2026-05-04-150208-how-does-validator-work.md \
                --title "Validator Internals"
# Loaded answer: how does validator work
# Re-validating 4 evidence quote(s)...
#   ✓ all 4 quotes still substring-match their source files
#   ✓ wrote page "Validator Internals" (4 evidence, 1 source)
#   ✓ retro-linked 2 existing page(s) to [[Validator Internals]]
# saved: .llmwiki/wiki/Validator Internals.md
```

`promote` defensively re-runs every quote in the answer through the same
substring-match validator that gates `ingest` and `mcp.write_page`. If a
source file changed since the ask, the promote is rejected with
`evidence_invalid` — never silently writing stale content. Flags:
`--title` (otherwise derived from the answer's question), `--rewrite`
(off by default; opt-in for an LLM rewrite into wiki-style prose),
`--no-save` (skip the `log.md` entry, debug only). Same shape over MCP via
the new `promote_answer` tool.

### Contradictions surface inline at ingest

When a new page's claim conflicts with an existing page's claim, the
conflict prints inline and appends to `<wikiDir>/contradictions.md`:

```bash
llmwiki ingest https://example.com/blog/2026-go-channels-rewrite.html
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
```

`contradictions.md` is plain Markdown, append-only, Obsidian-readable.
Detection uses your configured provider — Gemini Flash users pay nothing,
Anthropic users pay typical Haiku rates per ingest (~$0.05–0.15 depending
on the wiki's size). For the heaviest ingests we recommend Gemini Flash.
Failures of the contradiction call never fail the ingest — the new pages
still land.

### Retro-linker keeps the graph current

Every new page (from `ingest`, `promote`, or `mcp.write_page`) automatically
gets `[[Title]]` backlinks added to existing pages whose bodies mention it
in bare prose:

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

Body-only, idempotent, no LLM call. Evidence rows are untouched. Open
`.llmwiki/wiki/` in Obsidian and the graph view lights up the new
connections immediately.

### Coming in v1.3

Sub-project 6b adds a cross-page page-update pass under a default-off
`--update-existing` flag — when a new source updates earlier pages, those
pages get their bodies refined under the same validator. Out of scope for
v0.5.

## Privacy

- **Anthropic provider**: source content is sent to the Anthropic API at
  ingest and at ask time.
- **Gemini provider**: source content is sent to the Google Gemini API.
- **OpenAI-compatible provider**: source content is sent to whichever
  endpoint you configured (`base_url` in `[providers.openai_compat]`).
- **Ollama provider**: everything stays on your machine.
- **MCP server**: when driven by Claude Desktop / Claude Code, your
  Anthropic Pro/Max subscription handles the model calls — `llmwiki mcp`
  itself does not call any LLM API. Source content reaches whichever model
  the client is configured to use.
- **`.llmwiki/`** holds the wiki, the SQLite database, the saved answer
  archive, and `config.toml`. It's local and `.gitignore`d by convention.
- No telemetry, ever.

## Configuration

Configuration lives at `.llmwiki/config.toml`, written by `llmwiki init`.
Pre-existing configs missing newer keys silently inherit defaults.

### `[llm]`

| Key          | Default                    | Description                                                                       |
| ------------ | -------------------------- | --------------------------------------------------------------------------------- |
| `provider`   | `"gemini"`                 | LLM provider: `"gemini"`, `"anthropic"`, `"openai-compatible"`, or `"ollama"`     |
| `model`      | provider-dependent         | Model identifier passed to the provider (defaults: `gemini-2.0-flash`, `claude-haiku-4-5`, provider-config `default_model`, `llama3.2`) |
| `ollama_url` | `"http://localhost:11434"` | Base URL of the Ollama server                                                     |

### `[providers.openai_compat]`

| Key             | Default                  | Description                                                            |
| --------------- | ------------------------ | ---------------------------------------------------------------------- |
| `base_url`      | `""`                     | OpenAI-compatible endpoint (e.g. `https://openrouter.ai/api/v1`)       |
| `api_key_env`   | `"OPENAI_COMPAT_API_KEY"` | Name of the environment variable holding the API key                   |
| `default_model` | `""`                     | Model passed to `/chat/completions` (override per-call with `--model`) |

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

| Variable                | Description                                                                       |
| ----------------------- | --------------------------------------------------------------------------------- |
| `GEMINI_API_KEY`        | Required when `provider = "gemini"`. Free at https://aistudio.google.com/apikey   |
| `ANTHROPIC_API_KEY`     | Required when `provider = "anthropic"`. https://console.anthropic.com/settings/keys |
| `OPENAI_COMPAT_API_KEY` | Default env var name for `provider = "openai-compatible"`. Override via `[providers.openai_compat].api_key_env`. |
| `LLMWIKI_DIR`           | Override the wiki directory `llmwiki mcp` operates against (defaults to `$PWD`).  |
| `LLMWIKI_CASSETTE`      | When set, the LLM client replays from `internal/llm/testdata/cassettes/<name>__*.json` instead of calling the live API. Used by `make smoke`. |
| `NO_COLOR`              | Disable ANSI colors in CLI output                                                 |

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

                              MCP client ----> llmwiki mcp ----> same validate path
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
everything against `secrets.ANTHROPIC_API_KEY` / `secrets.GEMINI_API_KEY` /
`secrets.OPENROUTER_API_KEY` and opens a PR if the cassettes drifted.

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
[`mark3labs/mcp-go`](https://github.com/mark3labs/mcp-go),
[`mmcdole/gofeed`](https://github.com/mmcdole/gofeed),
[`go-shiori/go-readability`](https://github.com/go-shiori/go-readability),
[`JohannesKaufmann/html-to-markdown`](https://github.com/JohannesKaufmann/html-to-markdown),
the [`spf13/cobra`](https://github.com/spf13/cobra) family, and
[`mattn/go-sqlite3`](https://github.com/mattn/go-sqlite3).
