# Customising your wiki

Andrej Karpathy's [LLM-wiki gist](https://gist.github.com/karpathy/442a6bf555914893e9891c11519de94f)
describes a three-layer architecture: raw sources, the wiki, and the schema.
llmwiki ships the third layer as a user-owned schema document at your wiki
root that defines the page ontology, the six prompts driving every LLM call,
and an optional glossary.

## Discovery: AGENTS.md or CLAUDE.md

llmwiki looks for `AGENTS.md` first (multi-vendor convention; Cursor, OpenAI
Codex, and Claude Code all read it), then falls back to `CLAUDE.md` (Claude
Code's native filename). If both exist with identical bytes, `AGENTS.md`
wins; if both exist and differ, llmwiki refuses to guess and asks you to
pick one.

## `llmwiki init` writes it

Running `llmwiki init` writes a default `AGENTS.md` alongside
`.llmwiki/config.toml`. Open it in your editor of choice (Obsidian renders
it natively); rewrite the `## Domain` section to describe your actual wiki;
tweak the `## Ingest prompt` to bias toward "one comprehensive page per
concept" or whatever shape suits you.

- `llmwiki init --rewrite-schema` overwrites an existing schema file (by
  default `init` leaves an existing schema alone).
- `llmwiki init --schema-file=CLAUDE.md` writes the file under Claude
  Code's native filename instead of `AGENTS.md`.

The bundled defaults match v0.6 behaviour byte-for-byte, so an existing
wiki sees zero behaviour change until you create the file.

## The trust property is bundled

**The schema controls what the LLM is *asked*, not what counts as valid
evidence.** llmwiki's substring-match validator is bundled in the binary
and runs after every LLM call regardless of what the schema-rendered
prompt told the LLM. The worst a malicious or compromised schema can do is
degrade quality (more pages get dropped, fewer pages land); it cannot
ground a false claim.

## Inspect, validate, and migrate

Three subcommands cover the schema lifecycle:

- `llmwiki schema show` prints the active merged schema content.
  `--bundled` prints the bundled-default doc; `--doc` prints your user
  schema verbatim; `--hash` prints just the active hex hash + newline
  (scriptable, useful for comparing schemas across wikis).
- `llmwiki schema validate` runs structural validation on the user schema
  and errors out with `file:line` on missing required sections
  (`## Ingest prompt`, `## Domain`, ...) or missing required placeholders
  (`{{domain}}`, `{{existing_titles}}`, ...). Errors surface all problems
  at once via MultiError. Structural validation only — quality is still
  on you.
- `llmwiki schema migrate` eagerly re-ingests every page on a prior schema
  hash under the active schema (one LLM call per page; cost depends on
  provider). Resumable for free via per-page hash check. Without `--yes`
  it dry-runs; pass `--yes` to apply. To bring pages up lazily instead,
  do nothing — the next `ingest` that touches a given page (via
  `--update-existing`) brings it to schema naturally.

Changing the schema doesn't auto-rebuild your wiki. The `schema:` line in
`llmwiki status` and `schema_drift:` warning in `llmwiki lint` surface the
count of pages still on a prior schema.

## MCP introspection

Agents over MCP can call `mcp.get_schema` to introspect the active schema
before acting. Read-only; no per-call overrides; no `mcp.set_schema`. The
schema is the user's, not the agent's.

## Source-control your schema

Check `AGENTS.md` (or `CLAUDE.md`) and `.llmwiki/config.toml` into git.
`llmwiki schema show --hash` is a scriptable way to compare schemas across
wikis sharing a doc.

## Multiple wikis, one binary

The same `llmwiki` binary supports any number of independent wikis — one
per topic. Each wiki is its own directory: `~/wikis/distributed-systems/`,
`~/wikis/ml-papers/`, `~/wikis/cooking/`. Run `llmwiki init` inside each;
each gets its own `AGENTS.md`, `.llmwiki/wiki.db`, ingested sources, and
wiki pages.

Editing the per-wiki schema lets you bias each one for its domain —
distributed-systems gets a "favour proof sketches" prompt, ml-papers gets
"extract dataset + result + ablation", cooking gets "preserve unit
conversions". The same binary, the same trust property, the same MCP
surface — different domains, fully isolated.

Sub-topic namespacing *within* one wiki (folders under `wiki/`) is a
post-1.0 design question; today the per-wiki page list is flat.
