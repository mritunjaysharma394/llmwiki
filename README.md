# llmwiki

`llmwiki` ingests sources (files, URLs, repos, PDFs, RSS/Atom feeds, sitemaps)
and synthesizes them into a Markdown wiki, with answers grounded in verbatim
source quotes. Trust comes from validation: every page that ships includes
evidence quotes that are byte-exact substrings of the original source —
hallucinated pages are dropped before they hit disk.

![llmwiki demo](docs/assets/demo.gif)
<!-- TODO(release): asset missing — record via tools/record-demo.sh (requires vhs or asciinema). -->

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

Want llmwiki to keep updating itself? Skip the one-shot `ingest` and
point `llmwiki watch` at a folder — drop a file in, see a page land
in seconds. See the [Always-on](#always-on) section below for the
daemon recipe and the cron / Claude-Code-Stop-hook companions.

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

## Always-on

v0.8 turns llmwiki from a CLI tool into a living wiki. Three
companion commands compose the always-on surface:

- `llmwiki watch <dir>` — fsnotify daemon. Drop a file in the
  watched directory; debounce 2s; ingest into the wiki via a
  SQLite-backed crash-resumable queue. Retries 3 times with
  5s/30s/5min backoff before marking a row failed; Ctrl-C drains
  the in-flight ingest gracefully.
- `llmwiki maintain` — umbrella subcommand for cron / launchd /
  GitHub Actions. Bare invocation runs `--lint`,
  `--refresh-stale`, `--promote-pending`; pass any flag to scope
  the run; `--dry-run` composes with all of the above. Exits
  non-zero only on real failures (network, DB, crashed promote)
  so cron doesn't page on cosmetic drift.
- `llmwiki capture-session` — Claude Code Stop-hook companion.
  Pipe the session JSON into `llmwiki capture-session` and any
  wiki-touching turns are filed back as a saved answer; the
  auto-promote gate decides whether they become a permanent page.

Plus auto-promote in `llmwiki ask` itself: every ask runs a
four-signal heuristic gate (cited pages, evidence quotes, length,
no-hedging, no-near-duplicate) and on pass files the answer as a
permanent page automatically. Default ON; opt out with `[ask]
auto_promote = false`.

The full launchd / systemd / GitHub Actions cron recipes, the
`watch` examples, and the 5-line Claude Code Stop-hook recipe are
in **[`docs/automation.md`](docs/automation.md)**.

## Providers

`llmwiki init --provider <name>` picks one of four backends. Pages from any
provider go through the same evidence validator (see Trust model below) — a
quote that doesn't byte-exact substring-match its named source file is
dropped before disk.

| Provider              | Cost                          | Setup                                                                                              | Notes                                                                                       |
| --------------------- | ----------------------------- | -------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------- |
| `gemini` *(default)*  | Free tier; 1M context         | `export GEMINI_API_KEY=...` (get one at https://aistudio.google.com/apikey, no card)               | Default model `gemini-2.5-flash`. Recommended onboarding path.                              |
| `anthropic`           | Pay-per-token API, **or** free via MCP + Pro subscription | `export ANTHROPIC_API_KEY=sk-ant-...`, or skip the API entirely and use the MCP server below       | Default model `claude-haiku-4-5`. Highest quote-fidelity in our cassette tests.             |
| `openai-compatible`   | Many free or near-free tiers  | Edit `[providers.openai_compat]` in `.llmwiki/config.toml` to point at your provider's `/v1` endpoint | Tested against Groq, OpenRouter, Together, Cerebras, Mistral La Plateforme. Free tiers may rate-limit and produce flakier structured output; the validator drops bad pages either way. |
| `ollama`              | Free, fully offline           | `ollama pull llama3.2`, then `llmwiki init --provider ollama`                                      | Runs against `http://localhost:11434` by default. Source content never leaves your machine. |

## Use your Claude subscription via MCP

If you already pay for Claude Pro/Max, you can drive `llmwiki` from Claude
Desktop or Claude Code with **zero API spend** — your subscription token
budget is the only budget. `llmwiki mcp` runs as a Model Context Protocol
stdio server exposing eight tools to the client:

- `list_pages` — list pages (optional title prefix and limit).
- `read_page` — fetch one page's body, frontmatter, evidence, and links.
- `lint` — staleness + contradiction report across the wiki.
- `ask` — grounded Q&A with source quotes.
- `write_page` — propose a new page; **rejected unless every evidence quote
  is a byte-exact substring of the named, already-ingested source file**.
- `ingest` — pull a new source into the wiki.
- `promote_answer` — lifts a saved answer into a real page with the same
  trust validation as `write_page`. See "Living Wiki" below.
- `get_schema` — read-only introspection of the active schema
  (`schema_version`, `domain`, `ontology_fields`, `prompts.{...}`,
  `glossary`, `hash`, `doc_path`). Karpathy-pattern compliant: an agent
  can fetch the schema, learn this wiki's domain framing, and ingest
  with that context in mind in one round-trip. There is no `set_schema`
  — agents introspect, they do not edit. See ["Customising your wiki"](#customising-your-wiki-agentsmd-or-claudemd)
  below.

`mcp.ingest` accepts an optional `update_existing: bool` argument
(default false) and, when enabled, the response gains `pages_updated` and
`pages_update_failed` keys alongside v0.5's `retro_linked_pages` and
`contradictions_flagged` — see "Cross-page updates (opt-in)" below.

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

v0.6's `--update-existing` (now default-on as of v0.8) is the most
validator-hostile feature in the binary; it preserves the trust
property by keeping the prior page version whenever the validator
drops the proposed body.

v0.7's user-editable schema (`AGENTS.md` or `CLAUDE.md` at the wiki root)
controls what the LLM is *asked* and how pages are *shaped* — it cannot
loosen the validator. The substring-match check is bundled in the binary
and runs after every LLM call regardless of what the schema-rendered
prompt told the LLM. See ["Customising your wiki"](#customising-your-wiki-agentsmd-or-claudemd)
for the full reaffirmation.

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

### Cross-page updates (default-on as of v0.8)

v0.6 introduced the `--update-existing` flag; v0.8 flipped its default
to **on**, matching Karpathy's "modify 10–15 relevant pages in one
pass" framing in the original LLM-wiki gist. When enabled, ingest
edits existing pages in light of the new source — folding the new
source's claims into the pages whose claims it refines, qualifies,
contradicts, or extends. The validator drops any proposed body that
fails byte-exact substring-match, so flipping the default doesn't
weaken the trust property — only the daily-use posture. Opt out
persistently with `[ingest] update_existing = false` in
`.llmwiki/config.toml`, or for a single ingest with
`llmwiki ingest <source> --update-existing=false`.

```text
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

A 50-page repo ingest with `--update-existing` is roughly 5–10 ingest
calls + up to 50 update calls + up to 5 contradiction calls = up to 65
LLM calls per ingest. On Gemini Flash (recommended for this flag) the
daily 1500-call free tier comfortably absorbs this. On Anthropic Haiku,
~$0.30/ingest with caching. On Ollama (local, 7B-class), expect most
updates to be `update_failed` because small models often miss the
structured-output schema; consider keeping `update_existing = false` on
Ollama.

Pages whose proposed update body fails validation stay at their previous
version — never silently downgraded. The trust property holds: every page
on disk has ≥1 evidence quote that substring-matches its source. When a
`~ Title (update_failed)` line appears, re-run with `--debug-updates` to
see why each candidate's quotes didn't match.

Every candidate considered — `updated`, `body_only`, `failed`, `skipped`
— appends one row to `page_update_log` in `.llmwiki/wiki.db`. To inspect:

```bash
sqlite3 .llmwiki/wiki.db "SELECT pages.title, outcome, reason
                         FROM page_update_log
                         JOIN pages ON pages.id = page_update_log.page_id
                         ORDER BY created_at DESC LIMIT 20"
```

After enabling `--update-existing`, `llmwiki status` surfaces `pages
updated total` and `pages update failed` counters.

Persist the opt-in by setting `update_existing = true` in the `[ingest]`
block of `.llmwiki/config.toml`. Tune the candidate caps via
`update_existing_max_candidates_per_source` (default 20),
`update_existing_max_candidates_total` (default 50), and
`update_existing_quote_floor` (default 2).

v0.6 shipped this default-off; v0.8 flipped the default to true.
The Karpathy gist describes "modify 10–15 relevant pages in a single
pass" as the *default* shape ingest takes; the recommended provider
(Gemini Flash) is free, and the validator catches bad updates either
way. Anthropic-on-credit-card users who want the v0.6 posture write
one config line: `[ingest] update_existing = false`.

## Customising your wiki (AGENTS.md or CLAUDE.md)

Andrej Karpathy's [LLM-wiki gist](https://gist.github.com/karpathy/442a6bf555914893e9891c11519de94f)
describes a three-layer architecture: raw sources, the wiki, and the
schema. As of v0.7, llmwiki ships the third layer: a user-owned
schema document at your wiki root that defines the page ontology, the
six prompts driving every LLM call, and an optional glossary. The
bundled defaults match v0.6 behaviour byte-for-byte, so an existing
wiki sees zero behaviour change until you create the file.

### Discovery: AGENTS.md or CLAUDE.md

llmwiki looks for `AGENTS.md` first (multi-vendor convention; Cursor,
OpenAI Codex, and Claude Code all read it), then falls back to
`CLAUDE.md` (Claude Code's native filename). If both exist with
identical bytes, AGENTS.md wins; if both exist and differ, llmwiki
refuses to guess and asks you to pick one.

### `llmwiki init` writes it

Running `llmwiki init` writes a default `AGENTS.md` alongside
`.llmwiki/config.toml`. Open it in your editor of choice (Obsidian
renders it natively); rewrite the `## Domain` section to describe your
actual wiki; tweak the `## Ingest prompt` to bias toward "one
comprehensive page per concept" or whatever shape suits you. Use
`llmwiki init --rewrite-schema` to overwrite an existing schema file
(by default, `init` leaves an existing schema alone). Pass
`llmwiki init --schema-file=CLAUDE.md` to write the file under
Claude Code's native filename instead of `AGENTS.md`.

### The trust property is bundled

**The schema controls what the LLM is *asked*, not what counts as valid
evidence.** llmwiki's substring-match validator is bundled in the
binary and runs after every LLM call regardless of what the
schema-rendered prompt told the LLM. The worst a malicious or
compromised schema can do is degrade quality (more pages get dropped,
fewer pages land); it cannot ground a false claim.

### Inspect, validate, and migrate

Three subcommands cover the schema lifecycle:

- `llmwiki schema show` prints the active merged schema content.
  `--bundled` prints the bundled-default doc; `--doc` prints your
  user schema verbatim; `--hash` prints just the active hex hash +
  newline (scriptable, useful for comparing schemas across wikis).
- `llmwiki schema validate` runs structural validation on the
  user schema and errors out with `file:line` on missing required
  sections (`## Ingest prompt`, `## Domain`, ...) or missing required
  placeholders (`{{domain}}`, `{{existing_titles}}`, ...). Errors
  surface all problems at once via MultiError. Structural validation
  only — quality is still on you.
- `llmwiki schema migrate` eagerly re-ingests every page on a prior
  schema hash under the active schema (one LLM call per page; cost
  depends on provider). Resumable for free via per-page hash check.
  Without `--yes` it dry-runs; pass `--yes` to apply. To bring pages
  up lazily instead, do nothing — the next `ingest` that touches a
  given page (via `--update-existing` from v0.6) brings it to schema
  naturally.

Changing the schema doesn't auto-rebuild your wiki. The new
`schema:` line in `llmwiki status` and `schema_drift:` warning in
`llmwiki lint` surface the count of pages still on a prior schema.

### MCP introspection

Agents over MCP can call `mcp.get_schema` to introspect the active
schema before acting — Karpathy-pattern compliant. Read-only; no
per-call overrides; no `mcp.set_schema`. The schema is the user's,
not the agent's.

### Source-control your schema

Check `AGENTS.md` (or `CLAUDE.md`) and `.llmwiki/config.toml` into
git. `llmwiki schema show --hash` is a scriptable way to compare
schemas across wikis sharing a doc.

### Multiple wikis, one binary

The same `llmwiki` binary supports any number of independent wikis —
one per topic. Each wiki is its own directory:
`~/wikis/distributed-systems/`, `~/wikis/ml-papers/`,
`~/wikis/cooking/`. Run `llmwiki init` inside each; each gets its
own `AGENTS.md`, `.llmwiki/wiki.db`, ingested sources, and wiki
pages. Editing the per-wiki schema lets you bias each one for its
domain — distributed-systems gets a "favour proof sketches" prompt,
ml-papers gets "extract dataset + result + ablation", cooking gets
"preserve unit conversions". The same binary, the same trust
property, the same MCP surface — different domains, fully isolated.

Sub-topic namespacing *within* one wiki (folders under `wiki/`) is a
v0.8+ design question; today the per-wiki page list is flat.

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
| `model`      | provider-dependent         | Model identifier passed to the provider (defaults: `gemini-2.5-flash`, `claude-haiku-4-5`, provider-config `default_model`, `llama3.2`) |
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
