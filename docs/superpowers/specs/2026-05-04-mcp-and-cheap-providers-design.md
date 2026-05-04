# Sub-project 5 — MCP Server, Obsidian Output, Cheap Providers

**Status:** design approved, awaiting implementation plan
**Date:** 2026-05-04
**Author:** Mritunjay Sharma (with Claude)

## Context

Sub-projects 1 (trust the output), 3 (real-world ingestion), and 4 (launch
surface) shipped. The CLI ingests files, URLs, repos, PDFs, RSS/Atom feeds and
sitemaps; every page on disk carries evidence quotes that substring-match the
source file they came from; per-file dedup, co-resident re-chunking and a
GoReleaser-driven release pipeline are in place; `v1.0.0-rc.1` exists. Sub-
project 2 (web UI) is **permanently skipped** — `llmwiki` is, and will remain,
a CLI tool. Anything that needs a graphical surface defers to Obsidian, which
treats a directory of Markdown files as a first-class read/write workspace.

What's left between today's `v1.0.0-rc.1` binary and a wiki the user (and the
broader audience) will actually choose over the alternatives is **strategic
positioning**, not feature parity. Three competitors are live as of the date
of this spec, each occupying an adjacent niche:

- **`nashsu/llm_wiki`** (≈5.7k stars, TypeScript Electron desktop app):
  Louvain-clustered knowledge graph, multimodal image extraction with vision-
  LLM captions, Deep Research auto-ingest, a Chrome clipper, scenario
  templates, vector search via LanceDB, two-step chain-of-thought ingest. It
  out-features us by an order of magnitude on UI and graph polish.
- **`lucasastorian/llmwiki`** (≈795 stars, Python + Next.js + MCP):
  ships an MCP server so users plug their **Claude Pro / Code subscription**
  into the wiki instead of paying API tokens. Web UI. Hosted variant.
  pdf-oxide + Mistral OCR.
- **`Pratiyush/llm-wiki`** (≈217 stars, Python static site):
  archives Claude Code / Codex / Cursor / Gemini session transcripts into an
  Obsidian-native wiki. 16 lint rules, 4-factor confidence scoring, a 5-state
  page lifecycle, a 12-tool MCP server, `llms.txt` exports.

None of them deterministically reject hallucinated quotes. None are headless
single-binary tools. None run cleanly inside CI / containers / cron. Sub-
project 1's substring-validated evidence — quotes that don't appear verbatim
in the named source file are **dropped before disk** — is genuinely better
than anything those three describe. Sub-project 3's per-file anchoring and
sub-project 4's release surface mean we already have the parts that none of
them have. We have not yet **claimed the position** out loud.

This sub-project bundles three concrete moves that compound the existing moat
into one coherent v1.1 story: *"the trustable, headless, scriptable LLM wiki
— works with your Claude subscription via MCP, runs in CI, never lies."*

## Goals

1. **MCP server.** `llmwiki mcp` runs an MCP stdio server that any MCP client
   (Claude Desktop, Claude Code, Cursor, custom agents) can connect to and use
   as a tool. The killer combination Lucas's project gestures at — pay zero
   API tokens, drive a wiki via your Claude subscription — but layered on
   top of **our** trust-validation pass. Pages written via MCP go through the
   same `ValidateAndAttachEvidence` pipeline that `ingest` uses; quotes that
   don't substring-match the named source are rejected back to the MCP
   client with a structured error the client can show, retry, or surface to
   the human. Subscription-priced LLM access **plus** deterministic trust
   enforcement is a combination nobody else offers.
2. **Obsidian-native output.** The Markdown layout becomes Obsidian-shaped
   without anyone building a UI: `[[wikilinks]]` between pages, an
   auto-regenerated `index.md` hub, an append-only `log.md`, frontmatter
   keys that Obsidian's Dataview plugin can query out of the box. Users
   point Obsidian at the wiki dir and get graph view, backlinks, and
   full-text search for free, with no integration code.
3. **Provider abstraction + cheap defaults.** A generic OpenAI-compatible
   provider lets users plug in Groq, OpenRouter, Together, Cerebras, Mistral
   La Plateforme, or any other endpoint that speaks the OpenAI Chat
   Completions schema. Google Gemini becomes a first-class provider in its
   own right (free-tier, structured-output-capable, no credit card
   required). `init` defaults onto Gemini's free tier as the recommended
   onboarding path, with Anthropic and Ollama presented as alternatives
   rather than the only choice. The trust validator's existing "drop quote,
   warn" behaviour means cheap-model wikis end up sparser than Haiku wikis
   but never less honest.
4. **No new web surface.** Sub-project 2 is reaffirmed dropped. Obsidian is
   the UI; the MCP server is the agentic surface; the CLI is the human
   surface. Nothing else.

## Why this sub-project now

Sub-projects 1+3+4 produced a binary that stands on its own technical merits.
They did not produce a binary that beats `nashsu/llm_wiki`'s graph view in a
demo or matches `lucasastorian/llmwiki`'s "use my Claude Pro" pitch. We will
never beat nashsu on UI. We can beat Lucas on his own MCP-subscription pitch
by adding MCP **without losing** our validation invariant — a property his
implementation does not enforce — and we can deflect the "but I want a UI"
objection by leaning into Obsidian rather than building one. The cost-of-use
question (raised explicitly by the user in conversation) is solved at the
same time by killing Anthropic's API-key requirement as the only path to a
working wiki. The three moves rhyme: each one removes a barrier to adoption
without weakening the trust property.

## Non-goals (deferred / dropped)

Most of these are explicit cuts against features that other projects own or
that pull this sub-project's scope past one shippable cycle. Defer to sub-
project 6+ where indicated; otherwise drop permanently.

- **Contradiction flagging on ingest** ("this new source claims X, page Y
  already says ¬X"; Karpathy's "10–15 pages updated per source" pattern). The
  current `lint` command does after-the-fact contradiction batching and
  stays unchanged. **Sub-project 6.**
- **Saved-answer → wiki-page promotion.** `.llmwiki/answers/` exists, FTS
  exists, but the path from a useful answer to a permanent page is still
  manual. **Sub-project 6.**
- **Cross-page integration / link-graph maintenance on ingest.** Right now
  `[[wikilinks]]` are only emitted at write time; there is no "this page now
  exists, retroactively link the 4 older pages that mention it" pass. We add
  the wikilink syntax in this sub-project but **not** the retro-linker.
  **Sub-project 6.**
- **Image handling / multimodal ingest.** Vision-LLM captions, image
  extraction, OCR for scanned PDFs. nashsu owns this; the right time is
  when the user has a real image-heavy backlog. **Sub-project 6 or 7.**
- **Knowledge graph visualization, Louvain clustering, embeddings-based
  similarity.** **Permanent drop.** This is `nashsu/llm_wiki`'s niche. We
  are the headless play. Obsidian's built-in graph view (which we light up
  for free in this sub-project) is sufficient for our positioning.
- **Vector search / semantic embeddings for retrieval.** **Permanent drop.**
  FTS5 over pages + evidence + LLM ranking covers our use cases; embeddings
  add an index, an embedding-API dependency, and a latent-space mismatch
  problem (the `ask` model and the embedding model disagree on what
  "similar" means) for marginal recall improvement on a personal-scale
  corpus. Reconsider only if a user's wiki crosses ~50k pages, which our
  positioning does not target.
- **Web UI / desktop app / `llmwiki serve`.** **Permanent drop.** Sub-project
  2 stays skipped forever. Obsidian IS our UI.
- **Chrome clipper, Deep Research auto-ingest, scenario templates.**
  **Permanent drop** — those are nashsu's features and they fit Electron's
  shape, not a CLI's.
- **Hosted multi-tenant variant.** **Permanent drop.** This is Lucas's bet,
  and it requires a security/operations posture (auth, isolation, billing,
  abuse) that doesn't fit a single-author OSS CLI.
- **`llms.txt` export.** **Drop.** Pratiyush's project owns this; nice to
  have, not load-bearing for our positioning. Re-evaluate post-1.1 if a real
  user asks for it.
- **MCP roots / sampling / prompts beyond tools.** v1 of our MCP server
  exposes **tools only**. Resources, prompts, sampling and roots are part of
  the MCP spec but add scope without addressing the positioning thesis. Add
  later if a concrete client need emerges.
- **Anthropic-specific MCP via the Anthropic SDK.** We use the
  *generic* MCP stdio transport so the server is client-agnostic. Tying the
  server to Anthropic's SDK would re-couple us to Anthropic, which is
  exactly the dependency this sub-project is removing.

## What users see

A reader who has installed `llmwiki v1.1.0` lands on three new flows on top
of the v1.0.0-rc.1 surface.

### Flow 1 — onboarding without an Anthropic key

```bash
llmwiki init                                # interactive provider walkthrough
# Provider [gemini]: <enter>
# Get a free Gemini API key at https://aistudio.google.com/apikey
# Then: export GEMINI_API_KEY=...
# Other options: anthropic | openai-compatible | ollama
#
# Initialized wiki at .llmwiki

export GEMINI_API_KEY=...
llmwiki ingest ./README.md                  # works on the free tier
```

The `init` walkthrough is non-interactive on `--provider=<name>` and falls
back to a recommendation block when stdin isn't a TTY (CI). The recommended
default is `gemini` with `gemini-2.0-flash` because it is free, has 1M
context, and reliably emits structured tool-call output for our existing
`writePagesTool` schema.

### Flow 2 — Obsidian as the read/write UI

The wiki directory after a few ingests:

```
.llmwiki/
  config.toml
  wiki.db
  raw/
  answers/
    2026-05-04-143012-what-deps-llmwiki-uses.md
  wiki/
    index.md                 # auto-regenerated hub, lists every page
    log.md                   # append-only chronological event log
    Database Layer.md
    Ingest Pipeline.md
    Trust Property.md
    ...
```

A user opens `.llmwiki/wiki/` as an Obsidian vault. Backlinks, graph view,
search, Dataview queries against the frontmatter all work without any
plugin-specific integration code in `llmwiki`.

Page bodies use `[[Page Title]]` for cross-page references in place of bare
text. Frontmatter keys are spelled the way Dataview expects.

### Flow 3 — MCP from Claude Desktop / Claude Code

A user adds `llmwiki` to their MCP client config:

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

`llmwiki mcp` runs as an MCP stdio server. The client lists the tools below;
the user's Claude (driven by their Pro subscription, no API tokens spent)
can read and **write** wiki pages on their behalf. Every write goes through
trust validation; rejected writes return a structured error the client
re-renders, so the user sees "this page was rejected because the quotes you
proposed don't appear in the source you named" rather than a silent disk
write of bad content.

The exact CLI surface added by this sub-project:

- `llmwiki mcp` — start the MCP stdio server. No flags in v1; the server
  reads its config from the current working directory's `.llmwiki/` (or
  `LLMWIKI_DIR`).
- `llmwiki init` — provider walkthrough now offers Gemini first, Anthropic
  second, OpenAI-compatible third, Ollama fourth.
- `llmwiki init --provider {gemini,anthropic,openai-compatible,ollama}` — one-
  shot non-interactive variant (existing flag, expanded enum).
- New `[providers.openai_compat]` block in `config.toml` for the generic
  endpoint (`base_url`, `api_key_env`, `model`).
- New env vars: `GEMINI_API_KEY`, `OPENAI_COMPAT_API_KEY` (defaulted; users
  can override the env-var name via config).

`ingest`, `ask`, `lint`, `status`, `version` are unchanged in CLI shape;
they simply drive any of the new providers when `cfg.LLM.Provider` selects
them.

## Architecture overview

Three load-bearing additions, each isolated to its own package, none of
them disturbing the per-file evidence invariant from sub-projects 1 and 3.

### MCP server (`internal/mcp/`, new)

Library: **`github.com/mark3labs/mcp-go`** (MIT, latest release 2026-04, in
active development, clean stdio + tool API). Considered alternatives:
hand-rolling JSON-RPC over stdio (correct but ~600 lines of plumbing for
zero benefit) and Anthropic's MCP SDK (couples us to Anthropic, defeating
the point). `mark3labs/mcp-go` is the smallest defensible thing.

`internal/mcp/server.go` exposes:

```go
func NewServer(cfg *cmd.Config, db *db.DB, llmClient llm.Client) *server.MCPServer
```

…wiring the tool handlers into a single `*server.MCPServer` instance. The
new `cmd/mcp.go` registers the `mcp` cobra command and calls
`server.ServeStdio(srv)`.

#### Tool surface (v1)

The minimum useful set, deliberately small. Each handler reuses an existing
internal package; nothing new gets implemented under the validator.

| Tool         | Inputs                                      | Behaviour                                                                          |
|--------------|---------------------------------------------|------------------------------------------------------------------------------------|
| `ingest`     | `source: string`, optional `force: bool`    | Wraps `cmd.runIngest` machinery. Returns `{pages_written, evidence_quotes, dropped_pages}`. |
| `ask`        | `question: string`, optional `max_pages: int` | Runs the same FTS + evidence retrieval as `cmd/ask.go`, returns the answer string + a list of `(page_title, quote, source_file, line_start, line_end)` tuples. No streaming over MCP. No auto-archive (the client controls saving). |
| `list_pages` | `prefix: string` optional, `limit: int` def 50 | Returns `[{title, path, updated_at, source_files: [...] }]`.                       |
| `read_page`  | `title: string`                              | Returns `{title, body, evidence: [...], links: [...], source_files: [...]}`.       |
| `write_page` | `title: string`, `body: string`, `evidence: [{quote, source_file}], links: [...]` | **Goes through trust validation.** The evidence array is required (min 1). The handler resolves each `source_file` against the existing `source_files` rows for the wiki — the source must already have been ingested — and runs `wiki.ValidateAndAttachEvidence`. On success, writes the page to disk + DB. On validation failure, returns a structured error: `{code: "evidence_invalid", dropped: [{quote, reason}], hint: "quotes must be byte-exact substrings of an already-ingested source_file"}`. **Never** writes a page that fails validation. |
| `lint`       | (no inputs)                                  | Wraps `cmd.runLint`. Returns the contradiction summary as a string.                |

`write_page` is the load-bearing tool — it's where MCP-driven authoring
meets the trust property. Its evidence schema mirrors the
`writePagesTool` schema used by `ingest` so the same validation pipeline
runs.

`ingest` is exposed because the typical agent flow is "ingest a new
source, then write pages from it" — without `ingest`, an MCP client would
have to shell out to the CLI, which defeats the integration story.

`ask` is included because the user wants a working "Claude as the
inference, llmwiki as the knowledge store" loop end-to-end.

`list_pages` and `read_page` exist so the agent can **find and reuse**
existing pages instead of generating duplicates.

`lint` exists so the agent can self-check before declaring a session
done.

#### Concurrency model

`mark3labs/mcp-go`'s stdio server is single-process. Tool handlers can be
called concurrently by the client; we serialize on the underlying
`*db.DB` (sqlite-modernc handles concurrent reads, writes go through the
existing `tx.Begin / Commit` paths in `internal/db/queries.go`). LLM
clients are already safe for parallel use. No new locking layer.

#### Authentication

None in v1. The MCP stdio transport runs as a child process of the MCP
client; trust boundary is the OS user. If/when we add the HTTP/SSE
transport (out of scope here), bearer-token auth and bind-address config
get added then.

### Obsidian-native output (`internal/wiki/obsidian.go`, new)

The existing `WritePage` already produces Obsidian-readable Markdown with
frontmatter. Three additions make the directory feel native to Obsidian
without changing the per-page evidence contract.

#### `[[wikilinks]]` in page bodies

`internal/wiki/ops.go`'s `writePagesTool` description gets a line steering
the model toward `[[Page Title]]` syntax for cross-page references that
match titles in `existingTitles`. The validator does **not** enforce this
— wikilinks are a body-quality concern, not a trust concern, and bare
prose references are still acceptable.

A post-write pass in `internal/wiki/obsidian.go`:

```go
func RewriteBareReferencesAsWikilinks(body string, knownTitles []string) string
```

…runs over each new page's body before disk write. It does case-sensitive
whole-word substitutions of any known title not already wrapped in `[[ ]]`,
guarded against substitutions inside fenced code blocks and inline backticks.
This is conservative — false negatives are fine, false positives (linking
something that wasn't meant to be a wiki reference) are not. Run after
`ValidateAndAttachEvidence`; a wikilink rewrite that touches a quoted
fragment in the body does not invalidate any evidence row, since evidence
is anchored to source-file content, not page-body content.

#### `index.md` hub

`internal/wiki/obsidian.go`:

```go
func RegenerateIndex(wikiDir string, pages []db.PageRecord) error
```

Writes `wikiDir/index.md`, overwriting whatever was there. Body:

```markdown
---
title: index
generated_at: 2026-05-04T14:30:12Z
generator: llmwiki
---

# Wiki index

## Pages (12)

- [[Database Layer]] — updated 2026-05-04
- [[Ingest Pipeline]] — updated 2026-05-04
...

## By source

### internal/
- [[Database Layer]]
- [[Ingest Pipeline]]
...
```

Regenerated at the end of every `ingest` run (after all pages persist) and
after every successful MCP `write_page`. Idempotent: re-running with the
same DB state produces identical output. The user (or Obsidian's editor)
must not edit `index.md` manually — a comment line in the frontmatter says
so. If the file disappears, the next ingest recreates it.

#### `log.md` append-only chronicle

```go
func AppendLog(wikiDir string, entry LogEntry) error
```

Appends one timestamped line per significant event to `wikiDir/log.md`,
matching the Karpathy-style chronological log:

```markdown
- 2026-05-04T14:30:12Z **ingest** ./README.md → 7 pages, 23 evidence quotes, 1 dropped
- 2026-05-04T14:31:45Z **ask** "what does the chunker do?" → 412 chars, 3 sources
- 2026-05-04T14:32:18Z **mcp.write_page** "Trust Property" via MCP → 4 evidence quotes
```

Single timestamp format (RFC3339 UTC) so the file is parseable by other
tools without ambiguity. Never rotated, never truncated by `llmwiki`. If a
user wants a smaller file, they `mv log.md log-2026Q1.md` themselves.

#### Frontmatter for Dataview

The existing frontmatter already carries `title`, `updated_at`,
`content_hash`, `source_ids`, `evidence`, `links`. We add three keys
(opt-in by Obsidian/Dataview convention, ignored by everything else):

- `tags: [llmwiki, ingest]` — fixed, lets users filter on the vault.
- `sources: [internal/db/db.go, internal/db/queries.go]` — distinct list
  of `source_file.relative_path` values backing the page's evidence.
  Already computed for the ingest-progress line; just emit it as a YAML
  array in the page header.
- `created: 2026-05-04` — first-ingest date for the page (date only, not
  time, since Dataview's date queries are tidier without time components).
  `updated: 2026-05-04` is the existing `updated_at` re-spelled as a
  date-only key for parity. The original `updated_at` RFC3339 timestamp
  stays for round-trip fidelity with `ParsePage`.

`ParsePage` is extended to read these keys; `WritePage` emits them. We
keep the YAML-by-hand parser from sub-project 1 — no third-party YAML
library — because the new keys are flat scalars and a flat string array.

### Provider abstraction (`internal/llm/`, refactored)

Today `internal/llm/` has `client.go` (the `Client` interface),
`anthropic.go`, `ollama.go`, `cassette.go`. We add two providers and one
shared HTTP plumbing layer.

#### `internal/llm/openai_compat.go` (new)

A generic OpenAI Chat Completions / function-calling client. Configurable
`base_url`, `api_key`, `model`. Implements `Client` end-to-end (Complete,
CompleteStructured, CompleteStream). For `CompleteStructured` it uses the
OpenAI tool-calling schema (`tools: [{type: "function", function: ...}]`,
`tool_choice: {type: "function", function: {name: ts.Name}}`). For
streaming it consumes the SSE `data: ...` chunked stream.

`base_url` examples documented in `init`'s walkthrough:
- Groq: `https://api.groq.com/openai/v1`
- OpenRouter: `https://openrouter.ai/api/v1`
- Together: `https://api.together.xyz/v1`
- Cerebras: `https://api.cerebras.ai/v1`
- Mistral La Plateforme: `https://api.mistral.ai/v1`

Many free tiers (Groq especially) are *unreliable* at structured
function-calling — the model occasionally returns a top-level text response
when it should have called the tool. The OpenAI-compat client handles this
the same way `OllamaClient.CompleteStructured` does: if no tool call is
present, fall back to JSON-extraction from the text body. Models that emit
neither yield an error; the chunk fails; the existing per-chunk error
isolation means the run continues with the chunks that succeeded.

#### `internal/llm/gemini.go` (new)

Google Gemini via the official `generativelanguage.googleapis.com` v1beta
endpoint, using the SDK-less HTTP API to avoid pulling in
`google.golang.org/api` (a large transitive cost). Authenticates with
`GEMINI_API_KEY`. Default model `gemini-2.0-flash` (free tier, structured
output, 1M context). `CompleteStructured` uses Gemini's
`functionDeclarations` + `toolConfig: {functionCallingConfig: {mode: "ANY",
allowedFunctionNames: [ts.Name]}}` to force a tool call.
Streaming uses the SSE-shaped `streamGenerateContent` endpoint.

Gemini's structured-output reliability is good in practice on Flash, but
when the model fails to call the tool we fall back to JSON-extraction
identically to the OpenAI-compat client. The validator drops bad quotes
the same way it always has.

#### Provider selection in `cmd/root.go`

```go
switch cfg.LLM.Provider {
case "gemini":
    if os.Getenv("GEMINI_API_KEY") == "" { return userErrGemini }
    llmClient = llm.NewGeminiClient(cfg.LLM.Model)
case "anthropic", "":
    // existing
case "openai-compatible":
    keyEnv := cfg.Providers.OpenAICompat.APIKeyEnv
    if keyEnv == "" { keyEnv = "OPENAI_COMPAT_API_KEY" }
    if os.Getenv(keyEnv) == "" { return userErrOAICompat(keyEnv) }
    llmClient = llm.NewOpenAICompatClient(
        cfg.Providers.OpenAICompat.BaseURL,
        os.Getenv(keyEnv),
        cfg.LLM.Model,
    )
case "ollama":
    // existing
}
```

The cassette wrapper keeps wrapping whatever `llmClient` is selected, so
record/replay testing across providers is uniform.

#### Behaviour-degrades-gracefully invariant

The trust validator is provider-agnostic. A weaker model produces fewer
valid evidence quotes; pages without ≥1 valid quote get dropped; the user
sees `"WARN dropped page X — no verifiable evidence"` and ends up with a
sparser wiki. **This is the entire reason cheap providers are safe to
default-on.** Concrete spec language:

> A wiki ingested with Gemini Flash, OpenRouter free-tier models, or
> Ollama may contain fewer pages than the same source ingested with
> Haiku, but every page that lands in the wiki passes the same evidence
> check. There is no failure mode where switching to a cheaper model
> produces a wiki that is more wrong; only one that is more sparse.

The README and `init` walkthrough state this in plain English; the spec
covers it here so reviewers see it called out.

## Schema changes

**None.** This is deliberate. The MCP server reads/writes through the
existing `db.PageRecord`, `db.Evidence`, `db.SourceFile` rows; the
Obsidian additions write to disk only (`index.md`, `log.md`) and use the
existing frontmatter shape with three new flat keys; provider abstraction
is a code-only change. We hold `PRAGMA user_version` at 3 from sub-
project 4. Pre-v3 wikis are unaffected.

## Config additions

New `[providers]` block in `.llmwiki/config.toml`. The `[llm]` block
remains the active-provider selector; `[providers.<name>]` stores per-
provider details so the user can swap providers by changing one line.

```toml
[llm]
provider = "gemini"
model    = "gemini-2.0-flash"

[providers.openai_compat]
# OpenAI-compatible endpoint. Set base_url to point at any provider that
# speaks Chat Completions (Groq, OpenRouter, Together, Cerebras, Mistral
# La Plateforme, etc.).
base_url    = "https://openrouter.ai/api/v1"
api_key_env = "OPENROUTER_API_KEY"

[providers.gemini]
# Default model — override via [llm].model when you want a different one.
default_model = "gemini-2.0-flash"

[providers.anthropic]
default_model = "claude-haiku-4-5"

[providers.ollama]
default_model = "llama3.2"
url           = "http://localhost:11434"
```

`applyIngestDefaults`-style fall-back fills missing values silently so
pre-v1.1 configs keep working — `[providers.openai_compat]` with no
`base_url` is just unusable until the user fills it in. No config
migration tool; users who want the new defaults can re-run
`llmwiki init --force` (existing flag semantics).

The `[mcp]` block is intentionally absent. `llmwiki mcp` has no tunables
in v1.

## CLI surface changes

### `llmwiki mcp` (new command)

```
Usage: llmwiki mcp

Run the MCP stdio server. Reads .llmwiki/config.toml from the current working
directory. The server exposes ingest, ask, list_pages, read_page, write_page
and lint as MCP tools. write_page enforces the same evidence-quote validation
as `llmwiki ingest`.
```

No flags. `LLMWIKI_DIR=<path>` env var changes the working directory. Logs
to stderr (per MCP spec — stdout is the JSON-RPC channel). Exits non-zero on
fatal startup errors (config not found, db migration failed, etc.) so MCP
clients show a useful error.

### `llmwiki init` (expanded)

The `--provider` enum accepts `gemini`, `anthropic`, `openai-compatible`,
`ollama`. Default with no `--provider` is `gemini` (was `anthropic`).
Anthropic users keep working: `llmwiki init --provider anthropic`.

The interactive walkthrough (when stdin is a TTY) prints the recommended-
free-path block before the prompt. Non-TTY runs skip the prompt and behave
exactly like `--provider gemini`.

### Existing commands

`ingest`, `ask`, `lint`, `status`, `version` are unchanged in surface.
Their internals route through whichever `llm.Client` `loadConfig` selected.

## Risks

- **Cheap providers fail to call tools and produce empty pages.** The
  validator drops the page; the user sees fewer pages from the same source
  on Gemini Flash than on Haiku. Mitigation: documented expected behaviour
  in README and `init` walkthrough; users with strict density requirements
  switch back to Anthropic via `llmwiki init --provider anthropic`. The
  graceful-degradation property is the design feature, not a bug to hide.
- **`mark3labs/mcp-go` is under active development.** Pin a specific tag in
  `go.mod` and re-pin during the nightly cassette refresh's PR-review pass.
  If a breaking API change lands, fork or vendor; the wrapper surface in
  `internal/mcp/server.go` is small enough (≈300 LOC) to absorb it.
- **MCP `write_page` becomes a denial-of-evidence vector.** A confused agent
  could rapidly write-then-fail-validation, polluting `log.md`. Mitigation:
  log entries for failed `write_page` calls go to stderr, **not** to
  `log.md`. `log.md` only records validated, written pages.
- **Obsidian's auto-conversion of plain links to wikilinks conflicts with
  our rewriter.** Mitigation: our rewriter is idempotent (a body that
  already has `[[Title]]` is a no-op). If Obsidian writes a page back with
  edits, the next ingest will preserve any user-introduced wikilinks
  because frontmatter `content_hash` is recomputed against the body the
  validator wrote, not against post-Obsidian-edits.
- **Gemini API region restrictions.** Some Google Cloud regions don't have
  Gemini API enabled by default; users in those regions hit 403. The
  `UserError` for that case includes the AI Studio URL and a one-line hint
  to use OpenRouter as a Gemini gateway alternative.
- **OpenAI-compat providers all subtly disagree on tool-calling
  specifics.** `tool_choice` shape varies; some require
  `parallel_tool_calls: false`. Mitigation: send the conservatively-typed
  request that all five we test against (Groq, OpenRouter, Together,
  Cerebras, Mistral) accept; gate per-provider quirks behind small
  conditional blocks rather than a full provider-per-vendor matrix.
- **Performance regression on `index.md` regeneration for very large
  wikis.** Regeneration is O(N) over `pages`; at 50k pages this becomes
  noticeable. Out of v1.1 scope — our positioning targets personal wikis
  in the hundreds-to-low-thousands range.

## Open questions

These need user resolution before plan-pass. Each blocks one specific
implementation task; none block the spec.

1. **Default model for Gemini.** Spec assumes `gemini-2.0-flash` (free,
   structured-output-capable). Confirm or pick a different default. (User
   may have a preferred Flash variant after Gemini's roadmap moved.)
2. **Should `llmwiki mcp` lock the wiki directory while running?**
   Concurrent `llmwiki ingest` from one terminal and `llmwiki mcp` from a
   client on the same wiki can race on FTS triggers. The clean answer is
   an advisory file lock at the directory level. The cheap answer is "the
   user runs one or the other." Spec assumes cheap; the user may want a
   lock.
3. **`write_page` semantics on title collision.** Two options: refuse with
   a structured error (`{code: "title_exists", existing_path: ...}`) and
   force the agent to call `read_page` + supersede explicitly; or upsert
   silently with a `supersedes` link auto-injected. Spec assumes refuse-
   with-error (safer for an agent that hallucinates page titles); confirm.
4. **Provider selection precedence when `--provider` flag and config block
   disagree on the active model.** Existing root flag `--provider` already
   shadows the config; this sub-project adds providers but doesn't change
   that precedence. Confirm we want `--provider gemini --model
   gemini-2.5-pro` to honour both flags rather than reset model to the
   `[providers.gemini].default_model`.
5. **Should we rename `llmwiki ask` to `llmwiki query` now that there's an
   MCP `ask` tool with the same name?** Spec keeps `ask` for backwards
   compat. The MCP tool is also called `ask`; the namespacing comes from
   the MCP server name.

## Test strategy

Sub-project 1's cassette infrastructure carries the LLM-touching surface.
Pure unit tests cover the rest. Three new cassettes cover the new code
paths.

### Pure unit tests (no LLM, no network)

- `internal/wiki/obsidian.go`:
  - `RewriteBareReferencesAsWikilinks` — known titles, unknown titles,
    code-fence and inline-backtick exclusion, idempotency on already-
    linked bodies, case-sensitive title matching, multi-paragraph bodies.
  - `RegenerateIndex` — empty wiki (still writes a valid file), single
    page, dozen pages grouped by source, deterministic byte-identical
    output for identical inputs.
  - `AppendLog` — line format round-trip, RFC3339 timestamp formatting,
    failed-write-from-MCP path doesn't append.
- `internal/wiki/page.go`:
  - `ParsePage` / `WritePage` round-trip with new `tags`, `sources`,
    `created` frontmatter keys; pre-v1.1 page files (without those keys)
    parse without error and round-trip preserves their absence.
- `internal/llm/openai_compat.go`:
  - `httptest.Server` fixtures for: a normal tool-calling response; a
    no-tool-call fall-back-to-text-extraction response; a 4xx error
    response; a streamed SSE response.
- `internal/llm/gemini.go`:
  - same shape, against Gemini's request/response JSON; fixtures captured
    once via the official curl examples.
- `internal/mcp/server.go`:
  - tool registration shape (every tool the server claims to expose is
    actually registered).
  - `write_page` validation path with synthetic `db.DB` and ingested
    `source_files`: valid quote → page written, invalid quote → structured
    error returned, no disk write.
  - `read_page` / `list_pages` / `lint` happy-path round-trips.
- `cmd/init_test.go` (extended): `--provider gemini` writes the new
  config template; default-no-flag walkthrough on a non-TTY stdin
  defaults to gemini and exits 0.

### Cassette tests

Three new tests, each ~2–3K tokens, for nightly cassette-refresh budget
parity with sub-projects 1+3.

7. `TestIngestGemini` — same input as `TestIngestSmall` but routed
   through the Gemini client. Asserts the validator behaves identically
   regardless of provider — same source content yields the same evidence
   substring matches (page count may differ; quote *correctness* must
   not).
8. `TestIngestOpenAICompat` — same input routed through the OpenAI-compat
   client targeting OpenRouter's free `meta-llama-3.1-8b-instruct:free`
   slot. Asserts the fall-back JSON-extraction path is exercised when the
   tool-call response is malformed (we fixture a synthetic malformed
   response in the cassette to force the path).
9. `TestMCPWritePageRoundtrip` — drives the MCP server in-process via
   `mark3labs/mcp-go`'s test client. `ingest` a synthetic source,
   `list_pages` it, `read_page` one back, `write_page` a new page with
   evidence pulled from the ingested source. Asserts the page hits disk
   and the DB. A second `write_page` with a quote that's *not* in the
   source asserts the structured error.

### Integration / smoke

- `make smoke` is unchanged — it still runs the README quickstart against
  the existing smoke cassette.
- A new manual-only check: `claude mcp add llmwiki '/path/to/binary mcp'`
  in Claude Desktop, ask Claude "what pages does my wiki have?" — expect
  the `list_pages` tool call to round-trip. This is documented in the
  contributing section of the README, not automated.

### CI

The sub-project 4 nightly cassette-refresh job covers the new cassettes
the same way it covers the existing ones. Total recurring API spend stays
in the pennies-per-day range.

## Migration / backward compat

- **No schema migration.** Existing v3 DBs work unchanged.
- **Existing pages on disk** parse fine with the new `ParsePage` (the new
  `tags`, `sources`, `created` frontmatter keys are optional). Re-ingest
  will re-emit them populated. Pages users have hand-edited don't break
  because `[[wikilinks]]` rewriting is body-only and idempotent.
- **Existing config files (pre-v1.1)** without a `[providers]` block keep
  working — `cfg.LLM.Provider` is still the active selector, and
  `[providers.<name>]` is only consulted when a provider needs its own
  config knob (currently only `openai_compat` and `ollama`). Pre-v1.1
  configs that pin `provider = "anthropic"` keep using Anthropic with
  zero changes.
- **No `index.md` or `log.md` exists in pre-v1.1 wikis.** First post-
  upgrade `ingest` (or `mcp.write_page`) creates them. Users who
  previously had a hand-written `index.md` (none, in practice — we never
  shipped one) would have it overwritten on first ingest; we mitigate by
  documenting the behaviour in the README and stamping
  `generator: llmwiki` in the frontmatter so it's visually clear which
  files we own.
- **MCP server is purely additive.** Wikis that never run `llmwiki mcp`
  see no behaviour change.

## Implementation order

Plan-pass refines. Rough sequence to keep each step independently
testable:

1. **`internal/llm/openai_compat.go`** — generic Chat Completions client +
   tool-calling, with `httptest`-based unit tests for happy path, tool-call
   fall-back, streaming. Pure addition; does not touch existing providers.
2. **`internal/llm/gemini.go`** — Gemini client with the same API surface,
   fixtures captured from Google's docs.
3. **Provider wiring in `cmd/root.go`** — `[providers]` config block,
   `applyProviderDefaults`, expanded `cfg.LLM.Provider` switch. Update
   `init` templates and the walkthrough copy.
4. **Cassettes for `TestIngestGemini` and `TestIngestOpenAICompat`** —
   record once; nightly refresh keeps them current.
5. **Obsidian frontmatter additions** (`internal/wiki/page.go`) — `tags`,
   `sources`, `created` round-trip. Pure unit tests.
6. **`internal/wiki/obsidian.go`** — `RewriteBareReferencesAsWikilinks`,
   `RegenerateIndex`, `AppendLog`, plus the integration call sites in
   `cmd/ingest.go` and `cmd/ask.go`. Pure unit tests + a new cassette test
   asserting `index.md` and `log.md` exist after a full ingest.
7. **`internal/mcp/server.go`** — server skeleton, tool registry, the six
   handlers in the order: `list_pages`, `read_page`, `lint`, `ask`,
   `ingest`, `write_page` (write_page last because it's the validation-
   heavy one).
8. **`cmd/mcp.go`** — cobra command, stdio server start, signal handling.
9. **`TestMCPWritePageRoundtrip`** cassette test.
10. **README updates** — new install-and-onboard section leading with
    Gemini, MCP-client config snippet for Claude Desktop / Claude Code,
    Obsidian section ("open `.llmwiki/wiki/` in Obsidian, no plugin
    needed"). README rewrite is **last**, after every other task is
    checked in and verified.
11. **CHANGELOG entry** for `1.1.0` covering all three pillars.
12. **Tag `v1.1.0-rc.1`** locally (no push). Promotion to `v1.1.0` is a
    post-launch follow-up after a stability window, matching sub-project
    4's pattern.

## Verification

```bash
# Provider abstraction — Gemini default
unset ANTHROPIC_API_KEY GEMINI_API_KEY
mkdir new-wiki && cd new-wiki
./llmwiki init
# Expect: walkthrough recommends Gemini, prints AI Studio URL, exits 2 with
# UserError pointing the user at GEMINI_API_KEY.

export GEMINI_API_KEY=...
./llmwiki ingest ./README.md
# Expect: ingest succeeds, every page has evidence in frontmatter, the
# validator drops 0–N pages depending on Gemini Flash quote-fidelity.

./llmwiki ask "what does the chunker do?"
# Expect: streamed answer with source quotes, glamour render.

# OpenAI-compat — OpenRouter free tier
./llmwiki init --provider openai-compatible
# Edit .llmwiki/config.toml [providers.openai_compat] base_url to
#   https://openrouter.ai/api/v1 and api_key_env to OPENROUTER_API_KEY.
export OPENROUTER_API_KEY=...
./llmwiki ingest ./README.md
# Expect: validator drops more pages than on Gemini (smaller models miss
# tool-call schema sometimes), but every surviving page has valid
# evidence. No hallucinated pages reach disk.

# Obsidian-native output
ls .llmwiki/wiki/
# Expect: index.md, log.md, plus per-page Markdown files.
grep -l "\[\[" .llmwiki/wiki/*.md
# Expect: at least one page body containing [[Page Title]] references.
head -10 .llmwiki/wiki/index.md
# Expect: frontmatter with title=index, generator=llmwiki, then a list
# of all pages with [[Title]] entries grouped by source.
tail -3 .llmwiki/wiki/log.md
# Expect: append-only RFC3339 log lines for the recent ingest+ask events.

# Open .llmwiki/wiki/ in Obsidian
# Expect: graph view shows pages connected via [[wikilinks]]; backlinks
# panel populated; Dataview query
#   ```dataview
#   table sources, updated FROM "" WHERE contains(tags, "llmwiki")
#   ```
# returns every page.

# MCP server
./llmwiki mcp < /dev/null
# Expect: prints a JSON-RPC initialize-error to stdout (no client speaking)
# and exits cleanly. Logs go to stderr.

# MCP server with the bundled mock client
go test ./internal/mcp/... -run TestMCPWritePageRoundtrip
# Expect: pass.

# Claude Desktop integration (manual)
# 1. Add llmwiki to ~/.config/claude/claude_desktop_config.json
#    (or platform equivalent).
# 2. Restart Claude Desktop.
# 3. In a chat: "list the pages in my llmwiki" — Claude calls list_pages.
# 4. "Write a new page titled 'Foo' with body 'This is foo.' and the
#    evidence quote 'foo' from /tmp/foo.txt." Expect rejection with
#    structured error if /tmp/foo.txt isn't ingested or doesn't contain
#    "foo".

# Tests
go test ./...
# Expect: green in replay mode, including the three new cassettes.

# Status
./llmwiki status
# Expect: existing fields. No new schema, no new status counters in v1.1.
```
