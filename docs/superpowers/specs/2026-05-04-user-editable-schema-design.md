# Sub-project 7 — User-editable Schema (Karpathy gist alignment)

**Status:** design — awaiting user feedback before plan-pass
**Date:** 2026-05-04
**Author:** Mritunjay Sharma (with Claude)

## Context

Andrej Karpathy's gist on the LLM-wiki pattern (https://gist.github.com/karpathy/442a6bf555914893e9891c11519de94f) describes a three-layer architecture:

1. **Raw sources** — the inputs the user feeds in.
2. **The wiki** — the curated, structured Markdown that comes out.
3. **The schema** — a CLAUDE.md / AGENTS.md document that the user owns, defining what pages should look like, what workflows the wiki supports, and what domain conventions hold.

Through sub-project 6b (`v0.6.0-rc.1`, just shipped) `llmwiki` has the first two layers nailed down: ingest reads files / URLs / repos / PDFs / feeds; the trust-property validator anchors every claim to a substring-matching quote; pages live as Obsidian-friendly Markdown; the cross-page update pass reshapes existing pages when new sources land; saved-answer promotion lifts validated answers into permanent pages; the MCP server exposes the whole surface to subscription-driven agents.

What we *do not* have is the third layer. As of `v0.6.0-rc.1`, every load-bearing prompt and the page ontology are hard-coded into the Go binary. A grep across `internal/wiki/` finds them all baked in:

- `internal/wiki/ops.go:63` — `ingestSystemPrompt` ("You write wiki pages strictly grounded in the SOURCE provided. ... 1. Every page MUST include 'evidence' ... 5. Better to return one solid page than five thin ones. Aim for 1–4 pages per call.").
- `internal/wiki/ops.go:268` — `answerSystemPrompt` ("You answer using the provided wiki pages and source quotes. Cite pages inline using [Page Title] notation. ...").
- `internal/wiki/ops.go:344` — the `lint`-batcher contradiction prompt ("You are a wiki consistency checker. ...").
- `internal/wiki/contradict.go:72` — `contradictionSystemPrompt` (the per-pair contradiction-on-ingest prompt).
- `internal/wiki/update_existing.go:39` — `updateExistingSystemPrompt` (the cross-page update pass prompt; sub-project 6b).
- `internal/wiki/promote.go:471` — the answer-rewrite prompt (sub-project 6a's `--rewrite` path).
- `internal/wiki/page.go:25` — `Page{Title, Body, Links, SourceIDs, ContentHash, UpdatedAt, Evidence, Tags, Sources, Created}`. Frontmatter keys are emitted by `WritePage` (lines 75–139) in a fixed order, with one fixed shape.
- The `pages` table schema at `internal/db/db.go` (migrated to `user_version = 4` by sub-project 6b).

A user who wants pages-shaped-as-people-cards (`Person{Name, Affiliation, Recent Work, Quotes}`), or pages-shaped-as-meeting-notes (`Meeting{Title, Date, Attendees, Decisions, Action Items}`), or who simply wants the ingest prompt to nudge the LLM toward "one comprehensive page per concept" instead of our current "1–4 pages per call" — has to fork the binary. There is no first-class "this is a wiki about my Rust async runtime, not a generic notebook" affordance.

This is the third-layer gap, and it is the largest remaining piece of Karpathy's pattern that `llmwiki` has not yet instantiated.

## Goals

1. **Lift the prompts and the page ontology out of the binary into a user-owned document.** The bundled prompts and ontology become *defaults*; the user-owned `.llmwiki/schema.md` overrides them. Editing prompts becomes a text-editor operation, not a fork-and-rebuild operation.
2. **Match Karpathy's gist conceptually without ceding our trust property.** The schema controls what the LLM is *asked* and what shape the page takes. The schema does NOT control what counts as valid evidence — `wiki.ValidateAndAttachEvidence` stays bundled and substring-matches against actual source-file bytes. The pairing — "your schema, validated grounding, sources you trust" — is the differentiator.
3. **Backwards-compatible by definition.** A `v0.6` wiki opening under `v0.7` with no `.llmwiki/schema.md` runs the bundled defaults and produces byte-identical output to `v0.6`. Users opt in by creating the file (or by `llmwiki init --rewrite-schema`).
4. **Surfaces for inspection and validation.** `llmwiki schema show` prints the effective schema (merged from doc + bundled). `llmwiki schema validate` parses the doc, checks that every required placeholder is present in every prompt, errors loudly. `llmwiki init` writes a default schema doc alongside `config.toml`.
5. **MCP read-only exposure.** A new `mcp.get_schema` tool lets an agent introspect the active schema before ingesting, so agentic workflows can adapt to "this wiki is about meeting notes" without out-of-band signalling.
6. **Lint-time drift surface.** When the schema doc's hash changes after pages have already been ingested under a prior schema, `llmwiki lint` warns and `llmwiki status` surfaces a `schema_drift: <n> pages on prior schema` line. The wiki does not auto-rebuild — that decision stays the user's.
7. **Explicit migration path.** When a user changes the ontology, existing pages obviously don't carry the new shape. We define lazy migration (next ingest touching a page brings it to schema) plus an explicit `llmwiki schema migrate` opt-in for eager re-ingestion across the whole wiki.
8. **No web UI, no executable hooks, no schema-as-code.** Everything is declarative Markdown. The schema is a document the user *reads and edits*, not a program they run. This is a hard line and it preserves our "headless / scriptable / never-lies" positioning.

## Why this sub-project now

Sub-projects 5 and 6 took us from "a CLI that ingests files into validated Markdown" to "a living wiki with cross-page edits, contradiction surfacing, answer promotion, and MCP-driven agent authoring." All of that infrastructure was built against one fixed page shape and one fixed prompt set — appropriate for getting the architecture right, but the friction is now visible. Three signals make sub-project 7 the right next step:

1. **The Karpathy gist remains the load-bearing reference for every adjacent project's pitch.** Lucas's project, Pratiyush's project, and nashsu all gesture at "this is the Karpathy pattern" in their READMEs. None of them ship the third layer either. The first project that does, with deterministic evidence validation underneath, owns "the Karpathy-pattern reference implementation" — a positioning move sub-projects 5 and 6 paid for but did not yet claim.

2. **Every recent feature has had to invent its own prompt out of band.** Sub-project 6a's `contradictionSystemPrompt`, sub-project 6a's `--rewrite` answer-rewrite prompt, sub-project 6b's `updateExistingSystemPrompt`. Each one is hard-coded; each one is a wart the user cannot tune; each one would benefit from "I want the contradiction detector to be stricter about Go-version-specific claims" or "I want the rewrite prompt to preserve my house style." A schema layer turns five prompt files into one user-owned document.

3. **The `init` walkthrough is the natural integration point and we're touching it anyway.** Sub-project 5 already added the provider walkthrough to `init`; sub-project 7 extends it to "and here's where your wiki's ontology lives." Two onboarding choices instead of one, both presented at the same time, both editable later.

There is also a self-pacing consideration: sub-project 7's user-visible surface is small (one new file, two new subcommands, one MCP tool) but the architectural lift is large (every prompt-using path now reads from a config layer, with a hash and a migration story). Shipping it now — while the prompt sites are well-known and the codebase is ~10 KLOC — is materially cheaper than shipping it after another sub-project bakes in three more prompts.

## Recommended scoping

Single shippable cycle: **v0.7**. The work does not factor cleanly into a 7a/7b split the way sub-project 6 did, because the architectural change (lifting prompts + ontology to a config layer) is what makes everything else possible. We considered three split alternatives and rejected each:

- **"Prompts only in 7a, ontology only in 7b."** Rejected. The prompts depend on the ontology — the ingest prompt currently says "every page MUST include evidence" because `Page.Evidence` is a required field; you cannot move the prompt to a doc without also exposing the field set the prompt is talking about. Splitting introduces a meaningless half-state.
- **"Schema doc in 7a, `schema migrate` in 7b."** Possible but unhelpful. The migration command is ~150 LOC of wiring around an existing `IngestSourceFilesToPages` loop; deferring it leaves users with no answer to "I changed my schema, what now?" beyond hand-edits. The two ship together.
- **"Schema doc in 7a, MCP exposure in 7b."** Rejected. `mcp.get_schema` is ~30 LOC; gating it on a future release is ceremony.

So: one cycle, opinionated default, four user-visible surfaces (`.llmwiki/schema.md`, `llmwiki schema show`, `llmwiki schema validate`, `llmwiki schema migrate`), one new MCP tool. Bundled defaults are byte-identical to `v0.6`. No DB schema migration in 7 — we ride at `user_version = 4` from sub-project 6b.

## Non-goals (deferred / dropped)

- **Domain schema library** (`llmwiki init --schema=research-papers`, `--schema=meeting-notes`, `--schema=people-cards`). The default ships "general-purpose wiki matching v0.6 behaviour"; users hand-edit. Pre-built domain schemas with their own quirks and update cadence are a v0.8+ question — better answered by community contributions and a public registry than by a bundled list of three opinionated picks. **Deferred to v0.8+.**
- **Schema-as-code / executable hooks.** The schema is a *document*, not a program. We will not let users specify "run this Go function on every ingest" or "execute this jq filter on every page body." That would re-introduce a fork-and-rebuild dependency and an arbitrary-code-execution surface that is out of step with our headless, never-lies positioning. **Permanent drop.**
- **Frontmatter-driven custom evidence rules.** The schema cannot loosen `ValidateAndAttachEvidence`. We will not let users declare "for this page type, accept fuzzy quotes within edit distance 5" or "for this page type, no evidence required." The substring-match contract is the trust property and is not user-tunable. The schema can rename the *field* (e.g. `Evidence` → `Citations`) but not weaken the *check*. **Permanent drop.**
- **Web-UI schema editor.** Sub-project 2 stays permanently dropped; a web UI for any artefact in the wiki defeats the headless positioning. Users edit `.llmwiki/schema.md` in their editor of choice (Obsidian renders it natively). **Permanent drop.**
- **Truly new structured fields with their own validation** (e.g. "add a `tags: [string]` field whose values must come from a fixed taxonomy"). v0.7 ships rename + reorder + an "extra Markdown frontmatter pass-through" for fields the schema declares but the bundled validator ignores. New structured fields with their own DB columns, their own indices, their own validation rules are a v0.8+ question. **Deferred.**
- **Schema versioning beyond a single integer top-of-doc tag.** Each schema doc carries `schema_version: 1`. Future format changes bump the integer; v0.7 only knows version 1. We will not ship migration tooling between schema-format versions until a real second version exists. **Deferred (will land naturally with v0.8 schema-format changes).**
- **`llmwiki schema diff`** to render the active schema vs. the bundled default. Nice-to-have but `git diff` over the schema file does the same job for any user who has put `.llmwiki/` under source control (which we recommend). **Deferred to v0.8 if user feedback asks.**
- **Schema sharing across wikis** (an `import: <path-or-url>` directive in the schema doc that pulls in another schema). Re-introduces the "loading schemas from URLs" injection surface that we explicitly close in §Risks #2. **Permanent drop.** Users who want to share schemas copy the file.
- **Per-page-type schemas** ("`Person` pages use this prompt set, `Concept` pages use that prompt set"). The schema is a wiki-wide artefact in v0.7. Multi-shape wikis are a v0.8+ question. **Deferred.**

## What users see

Four flows on top of `v0.6.0`'s surface, plus the new diagnostic line in `llmwiki status`.

### Flow 1 — opening a v0.6 wiki under v0.7 (no behaviour change)

```bash
cd ~/my-existing-wiki   # initialised under v0.6
llmwiki version
# llmwiki v0.7.0-rc.1 (commit abc1234, built 2026-05-04T...)
ls .llmwiki/
# config.toml  wiki/  raw/  answers/  wiki.db
llmwiki ingest ./README.md
# Resolved to 1 source file(s)
# [1/1] processed
# Ingested 3 page(s) from ./README.md
#   ✓ Project Overview (4 evidence, files: README.md)
#   ✓ Architecture (3 evidence, files: README.md)
#   ✓ Trust Property (5 evidence, files: README.md)
# Retro-linked 0 existing page(s)
# saved: log.md
```

No `.llmwiki/schema.md` exists; the binary loaded its bundled defaults; the output is byte-identical to what `v0.6.0-rc.1` produced for the same input. Backwards compat is the easy case and it is also the loud case.

### Flow 2 — `llmwiki init` writes a default schema doc

```bash
mkdir my-wiki && cd my-wiki
export GEMINI_API_KEY=...
llmwiki init
# Recommended: Gemini (free tier, 1M context, no credit card required)
#   Get a key at https://aistudio.google.com/apikey, then:
#     export GEMINI_API_KEY=...
#   Other options: anthropic | openai-compatible | ollama
#
# Initialized wiki at .llmwiki
# Wrote default schema at .llmwiki/schema.md
#   (this defines page shape and prompts; edit to fit your domain)

ls .llmwiki/
# config.toml  schema.md  wiki/  raw/  answers/

head -25 .llmwiki/schema.md
# ---
# schema_version: 1
# generator: llmwiki
# ---
#
# # llmwiki schema
#
# This document defines how `llmwiki` shapes pages and prompts the LLM.
# It is YOUR document — edit it to fit your domain. The bundled defaults
# below match `v0.7.0-rc.1`'s behaviour.
#
# ## Domain
#
# A general-purpose wiki. Pages capture concepts, components, decisions,
# and the relationships between them. If your wiki is about something
# narrower (e.g. "an annotated reading log for ML papers", "running
# notes on my Rust async runtime"), edit this section to say so — the
# ingest prompt interpolates {{domain}} into its system message.
#
# ## Page ontology
#
# Each page has the following fields:
#
#   - title         (string)         the page's primary key; unique per wiki
#   - body          (markdown)       the page's narrative
#   - evidence      (list of quotes) verbatim spans from sources; required, ≥ 1
#   - links         (list)           Obsidian wikilinks declared structurally
# ...
```

The full default schema doc is laid out in §Architecture. Critically: it is `~150 lines`, structured-Markdown-with-H2-sections, human-readable, and produces byte-for-byte the same `v0.6` behaviour when used unmodified.

### Flow 3 — editing the schema, validating, then ingesting

The user opens `.llmwiki/schema.md` in their editor and rewrites the `## Domain` and `## Ingest prompt` sections to fit their actual wiki:

```markdown
## Domain

A reading log for distributed-systems papers. Pages should capture one
paper each, plus cross-cutting concepts that recur across multiple
papers. Bias toward fewer, denser pages over many thin ones.

## Ingest prompt

You write wiki pages strictly grounded in the SOURCE provided.

The SOURCE may contain multiple files, each delimited by a header line:
    === path/to/file.ext ===
For every evidence quote, set "source_file" to the exact path shown.

This wiki is: {{domain}}

EXISTING PAGE TITLES (for cross-referencing):
{{existing_titles}}

RULES:
1. Every page MUST include "evidence" — verbatim spans copied from SOURCE.
2. Each evidence entry SHOULD set "source_file" to the path from "=== ... ===".
3. Do NOT include general knowledge that is not in SOURCE.
4. Aim for ONE comprehensive page per paper, not multiple thin pages.
5. Cross-reference recurring concepts via [[Page Title]] wikilinks.
```

Then the user validates and ingests:

```bash
llmwiki schema validate
# .llmwiki/schema.md (schema_version 1)
#   ✓ all 5 required prompts present
#   ✓ all required placeholders present in each prompt
#   ✓ page ontology has required fields: title, body, evidence
#   ✓ glossary has 0 terms (optional)
# OK

llmwiki ingest ./papers/raft.pdf
# Resolved to 1 source file(s)
# [1/1] processed
# Ingested 1 page(s) from ./papers/raft.pdf
#   ✓ Raft Consensus (8 evidence, files: page-1, page-2, ..., page-12)
# saved: log.md
```

Note that the user's "ONE comprehensive page per paper" instruction took effect — the LLM produced 1 page where the bundled prompt would have produced 3-4. The validator still ran identically; every quote in the page substring-matches its named source page.

### Flow 4 — schema drift detected at lint time

The user changes the ontology (adds a `tldr` field to `## Page ontology`) and re-runs lint:

```bash
llmwiki lint
# Linting 47 pages...
# !! schema_drift: 47 pages were ingested under a prior schema (hash a3f...)
#                  The active schema (hash 91e...) declares a `tldr` field
#                  these pages do not carry.
#
#                  To bring all pages up to the new schema:
#                    llmwiki schema migrate
#                  (runs cross-page page-update on every page; expensive;
#                   see `llmwiki schema migrate --help`)
#
#                  To bring pages up lazily as new sources arrive: do nothing.
#                  The next `ingest` that touches a given page via the
#                  cross-page update pass will bring it to schema.
#
# ✓ no contradictions found
```

`llmwiki status` surfaces the same drift count:

```bash
llmwiki status
# wiki: my-wiki (.llmwiki/wiki, 47 pages)
# database: .llmwiki/wiki.db (user_version=4)
# provider: gemini / gemini-2.0-flash
# schema: .llmwiki/schema.md (hash 91e..., 47 pages on prior hash a3f...)
# pages_updated_total: 12
# pages_update_failed_total: 0
```

### Flow 5 — explicit migration

```bash
llmwiki schema migrate
# Re-ingesting 47 page(s) under active schema (hash 91e...).
# This walks every page's source_files and re-runs IngestSourceFilesToPages
# under the active schema, then runs ValidateAndAttachEvidence as usual.
# Pages whose proposed body fails validation STAY AT THEIR PRIOR VERSION.
# Estimated LLM calls: ~47 (one per page); cost on gemini-2.0-flash: free.
# Continue? [y/N] y
#
# [12/47] processed
# [47/47] processed
#
# 41 page(s) brought to active schema.
# 4 page(s) unchanged (proposed body identical to prior body).
# 2 page(s) update FAILED — kept at prior version:
#   ✗ Database Layer (proposed body had 0 substring-matching quotes)
#   ✗ Cassette Infrastructure (below quote floor)
#
# saved: log.md
```

The CLI surface added by sub-project 7:

- `llmwiki schema show` — prints the effective schema (merged: bundled defaults + user overrides) to stdout.
- `llmwiki schema show --bundled` — prints the bundled-default schema, ignoring any `.llmwiki/schema.md`.
- `llmwiki schema show --doc` — prints the user-owned `.llmwiki/schema.md` verbatim (or "not present, bundled defaults in effect").
- `llmwiki schema validate` — parses `.llmwiki/schema.md`, checks every required prompt section is present, every required placeholder is present in each prompt, the ontology has the required fields. Errors loudly with file:line where possible.
- `llmwiki schema migrate` — explicit eager re-ingest under the active schema. Prompts for confirmation; supports `--yes`, `--dry-run`.
- `llmwiki init` writes a default `.llmwiki/schema.md` alongside `config.toml`. New flag `--rewrite-schema` overwrites an existing schema file (idempotency: by default `init` leaves an existing schema doc alone).
- `llmwiki status` adds the `schema:` line shown above.
- `llmwiki lint` adds the `schema_drift:` warning shown above.
- New MCP tool `mcp.get_schema` returns the effective schema as a structured payload (sections + raw doc).

`ingest`, `ask`, `promote`, `version`, `mcp` (server entrypoint) are unchanged in surface — they pick up the schema-driven prompts and ontology automatically.

## Architecture

Three load-bearing additions, each isolated to a small new package or to a single existing function. Zero DB schema migrations in v0.7.

### The schema doc

`.llmwiki/schema.md` is the user-owned document. The bundled defaults are byte-identical to the file `llmwiki init` writes, which in turn produces byte-identical behaviour to `v0.6.0`. Format: structured Markdown with frontmatter and required H2 sections.

The default doc, in full (this is what `llmwiki init` writes verbatim):

```markdown
---
schema_version: 1
generator: llmwiki
---

# llmwiki schema

This document defines how `llmwiki` shapes pages and prompts the LLM.
It is YOUR document — edit it to fit your domain. The bundled defaults
match `v0.7.0-rc.1`'s behaviour.

The trust property is bundled and not configurable here: every evidence
quote on disk substring-matches its named source file, byte-for-byte.
This document controls what the LLM is *asked* and how the page is
*shaped*. It does NOT control what counts as valid evidence.

## Domain

A general-purpose wiki. Pages capture concepts, components, decisions,
and the relationships between them.

## Page ontology

Each page has the following fields. Order is the order they appear in
frontmatter. Field names are renameable; the bundled validator pins
the *check* to the field carrying evidence quotes (today: `evidence`).

  - title         (string)         the page's primary key; unique per wiki
  - body          (markdown)       the page's narrative
  - evidence      (list of quotes) verbatim spans from sources; required, ≥ 1
  - links         (list)           Obsidian wikilinks declared structurally
  - sources       (list of paths)  derived from evidence; emitted by WritePage
  - tags          (list of strings) Obsidian/Dataview-friendly
  - created       (date)           first-ingest date
  - updated_at    (RFC3339 ts)     last-write timestamp
  - content_hash  (sha256)         body hash; recomputed at every write
  - source_ids    (list of int)    DB row IDs backing this page

## Ingest prompt

You write wiki pages strictly grounded in the SOURCE provided.

The SOURCE may contain multiple files, each delimited by a header line:
    === path/to/file.ext ===
For every evidence quote, set "source_file" to the exact path shown.

This wiki is: {{domain}}

EXISTING PAGE TITLES (for cross-referencing):
{{existing_titles}}

RULES:
1. Every page MUST include "evidence" — verbatim spans from SOURCE.
2. Each evidence entry SHOULD set "source_file" to the path from "=== ... ===".
3. Do NOT include general knowledge that is not in SOURCE.
4. If SOURCE doesn't contain enough material for a high-quality page, do NOT create one.
5. Better to return one solid page than five thin ones. Aim for 1-4 pages per call.
6. Page bodies should synthesize and organize, but every claim must be defensible from the evidence quotes you provide.
7. When linking pages, only reference existing pages or pages you are creating in this same call.

## Update-existing prompt

You update an EXISTING wiki page in light of a NEW SOURCE.

This wiki is: {{domain}}

EXISTING PAGE:
{{existing_page_body}}

EXISTING EVIDENCE (already-validated quotes):
{{existing_evidence}}

NEW SOURCE files: see the "=== path ===" markers in the user prompt.

Output a single page with the same title; the body should incorporate
information from NEW SOURCE that refines, qualifies, or extends the
existing page. Every evidence quote must verbatim-substring-match
either the NEW SOURCE files OR the existing page's already-validated
quotes. Do not invent quotes. If NEW SOURCE does not actually update
this page, respond with {"pages": []} and we will keep the page
unchanged.

## Ask prompt

You answer using the provided wiki pages and source quotes.

This wiki is: {{domain}}

Cite pages inline using [Page Title] notation.
When using a verbatim quote from a source, render it as a markdown
blockquote and label it as (file:lines), e.g.:
> "channels block when full" (internal/sync/chan.go:4-4)

If pages and quotes are insufficient, say so plainly. Do not fabricate.

## Contradiction prompt

You are a contradiction detector for two wiki pages, A (newly written)
and B (pre-existing). Each page has already-validated evidence quotes
copied verbatim from real sources.

Output a JSON array of contradiction tuples. Each tuple is:
  {"a_quote": "...", "b_quote": "...", "description": "..."}

ONLY flag direct factual contradictions where the two quotes assert
mutually exclusive facts. The following are NOT contradictions:
  - Qualifications or additions.
  - Version-specific claims.
  - Different scopes (general vs. special case).

If there are no contradictions, output the empty array: [].

## Glossary

(empty by default; add domain-specific terms here, one per line:
  - <term>: <one-sentence definition>)
```

The doc is parsed by a deliberately-thin Markdown sectioner: top-level frontmatter (`---` … `---`) is parsed line-by-line for `schema_version` and `generator`; sections are split on `^## ` headings; required section names are pinned. No third-party Markdown library — same approach `internal/wiki/page.go` uses for the page YAML.

### Section semantics

- **`## Domain`** — free-form prose. The string between the heading and the next H2 is interpolated into prompts as `{{domain}}`.
- **`## Page ontology`** — a bullet list. Each line of form `  - <name>  (<type>)  <description>` declares a field. v0.7 enforces presence of `title`, `body`, `evidence`; treats other fields as informational pass-through (the validator does not check their types).
- **`## Ingest prompt`**, **`## Update-existing prompt`**, **`## Ask prompt`**, **`## Contradiction prompt`** — verbatim prompt template strings, with `{{name}}` placeholders.
- **`## Glossary`** — optional. Bullet list of `- <term>: <definition>`. Interpolated into the ingest and update-existing prompts as `{{glossary}}` when present.

Required placeholders per prompt (validated by `llmwiki schema validate`):

| Prompt                | Required placeholders                                       | Optional                  |
|-----------------------|-------------------------------------------------------------|---------------------------|
| Ingest                | `{{domain}}`, `{{existing_titles}}`                         | `{{glossary}}`            |
| Update-existing       | `{{domain}}`, `{{existing_page_body}}`, `{{existing_evidence}}` | `{{glossary}}`        |
| Ask                   | `{{domain}}`                                                | `{{glossary}}`            |
| Contradiction         | (none — the prompt is symmetric across the two pages)        |                           |

Extra placeholders the user introduces are tolerated and silently passed through unfilled (forward-compat: a future binary may interpolate them).

### Loader

`internal/schema/` (new package):

```go
type Schema struct {
    Version   int
    Domain    string
    Ontology  Ontology
    Prompts   Prompts
    Glossary  []GlossaryTerm
    Hash      string  // sha256 of the on-disk doc (or "bundled" for default-only)
    DocPath   string  // ".llmwiki/schema.md" or "" if bundled
}

type Prompts struct {
    Ingest          string
    UpdateExisting  string
    Ask             string
    Contradiction   string
}

func Load(wikiDir string) (Schema, error)
func Bundled() Schema     // same as Load when no doc exists
func (s Schema) Render(prompt string, vars map[string]string) string
func (s Schema) Validate() error
```

`Load` reads `<wikiDir>/.llmwiki/schema.md` if present and parses it; otherwise returns `Bundled()`. Both go through the same parser; `Bundled()` is `Parse(defaultSchemaMd)`. The default doc lives as a Go string constant in `internal/schema/default.go`, embedded via `//go:embed default.md` so it round-trips byte-for-byte with the file `init` writes.

`Render` walks `prompt`, replaces every `{{<name>}}` token using `vars`, leaves unknown placeholders as-is (forward-compat) and emits a WARN to stderr the first time per ingest run.

`Validate` enforces required prompts, required placeholders, required ontology fields. Errors are structured `{section: "Ingest prompt", line: 42, problem: "missing required placeholder {{existing_titles}}"}` so `cmd/schema_validate.go` can render file:line columns.

### Wiring into existing prompt sites

Each of the five hard-coded prompt sites becomes a `Render` call. Concretely, `internal/wiki/ops.go`'s `IngestSourceFilesToPages` becomes:

```go
func IngestSourceFilesToPages(ctx context.Context, client llm.Client, files []ingest.SourceFile, existingTitles []string, sch schema.Schema) ([]Page, error) {
    sysPrompt := sch.Render(sch.Prompts.Ingest, map[string]string{
        "domain":            sch.Domain,
        "existing_titles":   formatExistingTitles(existingTitles),
        "glossary":          formatGlossary(sch.Glossary),
    })
    // ... rest unchanged: builds user prompt, calls CompleteStructured,
    // runs ValidateAndAttachEvidence (UNCHANGED), returns kept pages.
}
```

Same shape for `UpdateExistingPagesFromSource`, `AnswerQuestion` / `StreamAnswer`, `DetectIngestContradictions`. The validator (`ValidateAndAttachEvidence`) receives no schema input and is not parameterised by the schema in any way; this is deliberate (see §Trust-property reaffirmation).

The `Schema` value is loaded once per process (in `cmd/root.go` after config load), stored on a `*Context`-style carrier alongside the `*db.DB` and the `llm.Client`, and threaded into every wiki entrypoint that previously did not take a schema. `cmd/ingest.go`, `cmd/ask.go`, `cmd/promote.go`, `internal/mcp/handlers.go` all gain a `schema.Schema` parameter — mechanical, ~30 call-site edits.

### Ontology renames

v0.7 supports field rename + reorder, not new structured fields. Concretely, the user can rewrite `## Page ontology` to:

```markdown
## Page ontology

  - name           (string)         (was: title)
  - summary        (markdown)       (was: body)
  - citations      (list of quotes) (was: evidence) — required, ≥ 1
  - related        (list)           (was: links)
  - origins        (list of paths)  (was: sources)
  - tags           (list of strings)
  - first_seen     (date)           (was: created)
  - last_updated   (RFC3339 ts)     (was: updated_at)
  - body_hash      (sha256)         (was: content_hash)
  - source_ids     (list of int)
```

The schema parser maps each declared field to its underlying `Page` struct field by *position in the canonical list*. The canonical order is fixed and bundled: `[title, body, evidence, links, sources, tags, created, updated_at, content_hash, source_ids]`. Reorder is a compile-time-stable mapping; rename is a name-string mapping. `WritePage` then emits the user's chosen names in the user's chosen order; `ParsePage` reads the user's chosen names. Pages on disk written under one schema and read under a different schema (renamed fields) silently miss the renamed values — the `schema_drift` lint surface flags this.

This is intentionally narrow. v0.8+ adds a richer ontology (declared types beyond the bundled set, optional new fields with declared validation rules). v0.7 ships the simplest thing that proves the lift works.

### `schema` subcommand

`cmd/schema.go` (new), with three subcommands:

```go
var schemaCmd = &cobra.Command{Use: "schema", Short: "Inspect, validate, or migrate the wiki's schema"}
var schemaShowCmd = &cobra.Command{...}      // schema show [--bundled|--doc]
var schemaValidateCmd = &cobra.Command{...}  // schema validate
var schemaMigrateCmd = &cobra.Command{...}   // schema migrate [--yes] [--dry-run]
```

`schema show` reads `Load(wikiDir)`, prints either the merged-effective schema, the bundled-only schema (`--bundled`), or the user doc verbatim (`--doc`).

`schema validate` calls `Schema.Validate()`, prints the structured errors with file:line, exits 0 on success / 1 on failure. The same validation runs implicitly at the start of every `ingest` / `ask` / `promote` / `mcp` invocation; `schema validate` exists so users can iterate quickly without doing a real ingest.

`schema migrate` walks every page's `source_ids`, re-reads the source file bytes (via `wiki.readSourceFileContent`, the helper already extracted in sub-project 6a), runs `IngestSourceFilesToPages` against the union of those source files under the active schema, and runs `ValidateAndAttachEvidence` as usual. Pages whose proposed body fails validation stay at their prior version; the same `update_failed` shape sub-project 6b uses surfaces here. `--dry-run` runs the LLM calls but skips disk + DB writes; `--yes` skips the confirmation prompt.

`schema migrate` does NOT touch the validator, the trust property, or the `evidence` rows on pages it cannot improve. It is opt-in, expensive, and reversible by `git restore .llmwiki/wiki/`.

### MCP — `mcp.get_schema`

`internal/mcp/handlers.go` adds:

```go
func getSchemaHandler(d Deps) server.ToolHandler {
    return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
        sch := d.Schema
        return mcp.NewToolResultStructured(map[string]any{
            "schema_version": sch.Version,
            "domain":         sch.Domain,
            "ontology_fields": sch.Ontology.Names(),
            "prompts": map[string]string{
                "ingest":          sch.Prompts.Ingest,
                "update_existing": sch.Prompts.UpdateExisting,
                "ask":             sch.Prompts.Ask,
                "contradiction":   sch.Prompts.Contradiction,
            },
            "glossary": sch.Glossary,
            "hash":     sch.Hash,
            "doc_path": sch.DocPath,
        }), nil
    }
}
```

Read-only. No `mcp.set_schema` or `mcp.write_schema` — the schema is the user's; agents read it to adapt their behaviour, they do not edit it. This is a hard line: an agent that can rewrite the system prompts an agent runs against is a confused-deputy surface. The schema lives on disk; the user opens an editor; that's it.

### Schema drift detection

`db.PageRecord` gains a `schema_hash TEXT` column at `user_version = 5`. (One additive migration. Sub-project 6b's migration to `user_version = 4` is the most recent baseline.)

```sql
ALTER TABLE pages ADD COLUMN schema_hash TEXT NOT NULL DEFAULT '';
```

Default `''` for pre-v0.7 rows means "ingested under unknown / pre-schema-tracking conditions"; the lint warning treats `''` as "prior schema" relative to the active schema.

Every successful `WritePage` write path (`wiki.IngestSource`, `wiki.PromoteAnswer`, `wiki.UpdateExistingPagesFromSource`, `mcp.write_page`) sets `schema_hash = sch.Hash` on the row. `cmd/lint.go` queries `SELECT COUNT(*) FROM pages WHERE schema_hash != ?` with the active hash and emits the warning above when the count > 0. `cmd/status.go` does the same query and surfaces it as the `schema:` line.

This is the only DB schema change in sub-project 7. Migration is `CREATE TABLE IF NOT EXISTS`-shaped (well, `ALTER TABLE` here, but with `IF NOT EXISTS` not available we wrap in a `PRAGMA table_info` check — same defensive shape as sub-project 6b's migration).

## Trust-property reaffirmation

This section is loud on purpose. The schema is a user-editable document; user-editable documents that flow into LLM system prompts are an injection surface. We close that surface with two mechanical properties and one positioning property.

**Mechanical property 1: the validator is not in the schema.** `wiki.ValidateAndAttachEvidence` is bundled Go code. The schema cannot rewrite it, swap it, weaken it, or skip it. Every code path that writes a page to disk goes through the validator unchanged. This includes the schema-rendered ingest path, the schema-rendered update-existing path, the schema-rendered promote path, and the `mcp.write_page` path. The validator's contract — "every surviving evidence quote substring-matches its named source file, byte-for-byte" — holds regardless of what the schema-rendered prompt told the LLM to do.

**Mechanical property 2: the schema controls *what is asked*, not *what is checked*.** A malicious or compromised schema could ask the LLM "ignore the source and write whatever you want." The LLM might comply. The validator drops every quote that does not substring-match. The page reaches disk only if at least one quote survives, and every surviving quote is grounded byte-for-byte in actual source bytes. The worst a malicious schema can do is degrade quality (fewer pages land, more pages get dropped); it cannot ground a false claim.

**Positioning property: the schema is your document.** Same trust level as `config.toml`. We do not load schemas from URLs, we do not import schemas from third parties, we do not have a `schema-registry` we curate. If you check `.llmwiki/` into git, you can see every schema change in your own diff history. If a contributor sends a PR that edits your schema, you review it the same way you review any other code change. Trust boundaries match the user's existing repo trust boundaries; no new ones are introduced.

The Karpathy-gist framing leans into this: the gist's "structured" emphasis pairs cleanly with our trust property. Most projects will read the gist and ship "the LLM proposes a structure, we trust it." We ship "you author the structure, the LLM fills it in, the validator grounds every claim." That's a stronger product, and the schema layer is what makes it complete.

The `llmwiki schema validate` output is explicit on this:

```
.llmwiki/schema.md (schema_version 1)
  ✓ all 5 required prompts present
  ✓ all required placeholders present in each prompt
  ✓ page ontology has required fields: title, body, evidence
  ✓ glossary has 0 terms (optional)

  trust property: enforced by bundled validator
  (substring-match against source files; not configurable from this doc)
OK
```

## Risks

- **1. Prompt-injection at the schema layer.** Addressed in detail in §Trust-property reaffirmation. Mechanical mitigation: the validator is bundled and runs after every LLM call. Worst case: degraded quality. Best case: better-shaped pages because the prompt fits the user's actual domain. We will document this prominently in README.

- **2. Loading schemas from third parties.** Permanently dropped: no `import:` directive, no URL-load, no public registry. The schema is `<wikiDir>/.llmwiki/schema.md` and that is the only path. Sharing schemas is `cp` between repos. This closes an entire class of supply-chain risk before it opens.

- **3. Schema drift across wikis using a shared schema.** If a team co-edits a single schema doc and copies it across N wikis, divergence is the user's problem, not ours. We can ship `llmwiki schema show --hash` (a one-liner that prints `Hash`) so users can scriptably compare; doing more is out of scope.

- **4. Page ontology rename breaking ParsePage round-trip.** A user renames `evidence` → `citations`; pages written under the old name still have `evidence:` in their frontmatter; pages written under the new name have `citations:`. `ParsePage` must read both. Concrete approach: `ParsePage` consults the active `Schema.Ontology` to map declared field names back to the canonical struct fields. Pages on disk with neither name are pre-v0.7 (legacy) and are read with the bundled-default name set. Mitigation: well-tested `page_test.go` cassette that round-trips a renamed schema.

- **5. Migrating a renamed schema across an existing wiki.** When a user renames a field, existing pages on disk still carry the old name. `llmwiki schema migrate` re-ingests them under the new schema, and `WritePage` emits the new field name. This works but is expensive (one LLM call per page). Lazy migration also works — the next ingest touching a page brings it to schema. Both paths produce the right end-state. We document both.

- **6. LLM ignoring `{{glossary}}` in the rendered prompt.** Cheap providers (Gemini Flash, OpenRouter free tier) sometimes ignore long preambles. Mitigation: glossary is opt-in (empty by default); users who add glossary terms see them in `schema show` and can decide whether the LLM is honoring them. The validator catches every concrete failure mode.

- **7. Schema changes silently breaking saved answers.** A user runs `ask` under schema v1, saves the answer, edits the schema, runs `promote` on the saved answer. The answer's body was generated under the prior `## Ask prompt`; the promote re-validates evidence quotes (per sub-project 6a), which still works regardless of schema. Only the *prose* of the body reflects the prior prompt. Mitigation: this is acceptable — the saved answer's prose is the user's to edit if they care. Document in `promote --help` that re-issuing the question (`llmwiki ask`) under the new schema is the way to get prose-aligned answers.

- **8. `schema validate` false confidence.** `validate` checks structural well-formedness (required sections, required placeholders, required ontology fields). It does not check that the prompt is *good*. A user can write a schema that validates but produces awful pages. Mitigation: validate's output explicitly says "structural validation only — quality is on you." A `schema dry-run <source>` that runs an ingest with disk writes disabled is a possible v0.8 follow-up.

- **9. `schema migrate` cost on cheap providers.** A 500-page wiki under `schema migrate` is 500 LLM calls. On Gemini Flash (free), fine. On OpenRouter free tier (200 calls/day), this needs two days. On Anthropic Haiku (~$0.005/call), $2.50 per migration. Document this in `schema migrate --help` and gate on confirmation. We considered making `migrate` resumable (skip pages whose `schema_hash` already matches the active hash); that's a useful feature and we will ship it — `migrate` is naturally idempotent if it sets `schema_hash` per-page on success.

- **10. Bundled-default schema drift across binary versions.** A user upgrades `llmwiki v0.7 → v0.8`; we add a new field to the bundled-default ontology. The user's `.llmwiki/schema.md` (which they wrote under v0.7) doesn't mention it. v0.8's loader should: (a) accept v0.7-shaped docs unchanged (fields the user didn't declare are simply absent from emitted pages); (b) document the new field in v0.8's CHANGELOG; (c) suggest `llmwiki init --rewrite-schema` if the user wants to adopt the v0.8 default. We will not auto-merge bundled-default updates into a user's schema doc — that is the kind of magic that breaks trust.

- **11. Backwards compat with wikis that have hand-edited prompts (none today).** No user can have hand-edited prompts in v0.6 because they were Go source. Backwards compat is therefore trivial: pre-v0.7 wikis open under v0.7 with no `.llmwiki/schema.md` and run on bundled defaults. The only failure mode is "user upgrades to v0.7, ingests under bundled defaults, then writes a `.llmwiki/schema.md`, then sees that pages from before the schema doc was written carry a different `schema_hash` than pages from after." This is the expected behaviour and the lint surface flags it.

- **12. Schema changes mid-cassette-test.** Cassette tests in `internal/llm/testdata/cassettes/` were recorded under bundled-default prompts. If the bundled defaults change byte-for-byte (we are taking care to keep them identical to v0.6), every cassette has to be re-recorded. Mitigation: confirmed via reading `internal/wiki/ops.go:63` etc. that the default schema doc proposed in §Architecture is byte-identical-modulo-formatting to the existing bundled prompts. The cassettes will continue to replay. We will run `LLMWIKI_RECORD=1` once during plan-pass to confirm and re-record any that drift.

## Open questions

Numbered for user feedback. First-cut answers are the spec's working defaults. **Resolved 2026-05-04:** all 15 questions resolved per the user's directive ("user-friendly, fast, Karpathy-aligned, no compromise on quality"). Q1 was overridden from the original first cut; Q2–Q15 hold as drafted.

1. **Schema doc filename: `.llmwiki/schema.md` vs `AGENTS.md` at wiki root vs `CLAUDE.md`?**
   *Resolved: `AGENTS.md` at wiki root.* Karpathy's gist explicitly names AGENTS.md; it's discoverable on `ls` without a hidden-dir traversal; AGENTS.md is no longer single-vendor branded — Cursor, OpenAI Codex, Claude Code, and others all read it as a multi-vendor convention. The original first cut (`.llmwiki/schema.md`) optimized for namespacing alongside `config.toml`, but the user's directive prioritized Karpathy-alignment + user-friendliness. Rest of the spec uses `AGENTS.md` throughout; references to `.llmwiki/schema.md` should be read as `AGENTS.md` at wiki root.
   *Original first cut: `.llmwiki/schema.md`.* Rationale: namespaced alongside `config.toml`; not tied to a specific agent vendor (CLAUDE / AGENTS felt branded at the time of drafting); discoverable via `ls .llmwiki/`.

2. **Format: structured Markdown sections vs YAML frontmatter + Markdown body vs pure TOML?**
   *First cut: structured Markdown with H2 sections per concern (`## Page ontology`, `## Ingest prompt`, `## Update-existing prompt`, `## Ask prompt`, `## Contradiction prompt`, `## Glossary`).* Rationale: matches Karpathy's "AGENTS.md" framing, is human-readable, parseable by simple section split. Pure TOML loses prompt readability (multi-line strings get awkward). YAML frontmatter + Markdown body is a hybrid; we use it on a per-page basis already, but for a doc that is 90% prose, plain Markdown wins.

3. **Placeholder syntax for prompt templates: `{{name}}` (mustache-like) vs `${name}` vs Go-style `{{.Name}}`?**
   *First cut: `{{name}}`.* Rationale: most familiar to LLM-tooling users; not Go-specific (we render with a one-line regex replacer, not `text/template`, so we are not shoe-horning Go template semantics into a doc the user reads). `${name}` collides with shell users' instincts. `{{.Name}}` couples the doc to a Go internal detail.

4. **Required vs optional placeholders, and what happens when a user removes a required one.**
   *First cut: each prompt has a documented required-placeholder set; `llmwiki schema validate` errors if any is missing. Allow extra placeholders to be silently passed through unfilled (forward-compat).* A user who removes `{{existing_titles}}` from the ingest prompt sees a `schema validate` error pointing at the section. We considered "missing placeholders are warnings, not errors" but the failure mode (LLM ingests without knowing the existing-titles set, dupes pages) is bad enough to gate.

5. **Migration strategy for existing pages on schema change.**
   *First cut: lazy + explicit `llmwiki schema migrate`.* The wiki DB stores `schema_hash` per page (new column under `user_version = 5`). Ingest checks if a page's `schema_hash` matches the active schema; if not, the next ingest touching that page brings it up to date naturally (the cross-page update pass already rewrites pages it touches). `schema migrate` is the "I want everything rebased now" opt-in: walks every page, re-runs ingest under the active schema, validator decides what survives. This matches sub-project 6b's "no silent downgrades" stance exactly.

6. **Backwards compat with v0.6 wikis (no schema doc).**
   *First cut: bundled defaults are byte-identical to the v0.6 prompts + ontology.* A v0.6 wiki opening under v0.7 sees zero behavioural change unless the user runs `llmwiki init --rewrite-schema` (which writes the default doc; pages re-ingested afterwards carry the new `schema_hash`). The pre-existing pages have `schema_hash = ''` and are flagged by `lint` and `status` as "ingested under prior schema."

7. **MCP exposure: new `mcp.get_schema` tool, or extend an existing tool?**
   *First cut: yes, dedicated `mcp.get_schema` tool, read-only.* Rationale: agents that introspect the schema before acting (Karpathy-pattern compliant) need a fast, structured surface; bolting it into `mcp.list_pages` or `mcp.lint` would conflate concerns. Not writable: agents do not edit the schema (see §Architecture, MCP section).

8. **Schema changes vs. trust property: is there any path by which a malicious schema can ground a false claim?**
   *First cut: no.* Mechanical reasoning in §Trust-property reaffirmation. The substring-match validator is bundled; the schema cannot reach it; every quote that lands on disk substring-matches an actual source file. Worst case from a malicious schema is degraded quality, not ungrounded claims. We will reaffirm this in the README.

9. **Page ontology extensibility: just rename/reorder fields, or add genuinely new fields (e.g. tags, related_topics)?**
   *First cut: v0.7 ships rename/reorder + an "extra Markdown frontmatter pass-through" pass for fields the schema declares but the bundled validator ignores.* Truly new structured fields with their own validation are a v0.8+ question. The pass-through path means a user who adds `priority: high` to their ontology gets `priority: high` round-tripped in page frontmatter without anything blowing up — but the validator does not know what `priority` means and does not check it.

10. **Domain schema library / templates** (`llmwiki init --schema=research-papers`, etc.).
    *First cut: out of scope for v0.7.* Default schema is "general-purpose wiki" matching v0.6 behaviour; users hand-edit. v0.8 question whether to ship `--schema=research-papers`, `--schema=meeting-notes`, etc. and where they live (in-binary vs. a public repo we curate). The community will likely answer this faster than we can.

11. **Schema versioning.**
    *First cut: each schema doc has a `schema_version: 1` line in frontmatter; v0.7 only knows version 1.* Future format changes bump the integer; the loader errors on unknown versions with a useful "this schema declares version 2; upgrade `llmwiki`" message. We will not ship migration tooling between schema-format versions until v0.8 or later actually has a v2 format.

12. **`llmwiki schema diff` to show active vs bundled default.**
    *First cut: nice-to-have but defer to v0.8.* `git diff` over the schema file (assuming `.llmwiki/` is git-tracked, which we recommend in the README) does the same job. Adding `schema diff` is not load-bearing for v0.7.

13. **Glossary placement: in the schema doc or separate file?**
    *First cut: in the schema doc, under `## Glossary`.* Rationale: glossary terms are domain-shaped (this is a wiki about Rust async; here are the load-bearing terms) and live with the rest of the schema concepts. A separate `.llmwiki/glossary.md` would make `schema validate` confusing ("which file's hash determines drift?"). Single source of truth.

14. **`schema migrate` resumability.**
    *First cut: yes, automatic.* `migrate` walks pages whose `schema_hash != activeHash`; succeeded pages get the new hash; a `Ctrl-C` mid-run leaves the partially-migrated wiki in a sound state (the lint warning still fires for the un-migrated pages). Resuming is just re-running `schema migrate`.

15. **How does `mcp.ingest` interact with schema overrides per-call?**
    *First cut: it does not.* The MCP server loads the schema once at start (same as the CLI). Callers cannot override the schema per-call. Per-call overrides would re-introduce the agent-edits-the-system-prompts confused-deputy surface. If a future user has a use case for per-call overrides, we revisit; v0.7 ships single-schema-per-server.

## Implementation order

Plan-pass refines. Single shippable cycle, ~9–12 phases:

1. **`internal/schema/` package skeleton** — `Schema`, `Prompts`, `Ontology`, `GlossaryTerm` types; `Parse`, `Bundled`, `Render`, `Validate`. Pure unit tests against fixture schema docs (round-trip, missing required section, missing required placeholder, malformed frontmatter).

2. **`internal/schema/default.md`** — embedded via `//go:embed`. Byte-equality test against the existing bundled prompt strings in `internal/wiki/ops.go`, `internal/wiki/contradict.go`, `internal/wiki/update_existing.go` so we are confident the move-to-doc preserves behaviour.

3. **Wire `Schema` through `cmd/root.go`** — single `Load` at startup, stored on the `*RootContext` carrier; `cmd/ingest.go`, `cmd/ask.go`, `cmd/promote.go`, `cmd/lint.go`, `cmd/mcp.go` all receive it.

4. **Replace prompt sites** — `internal/wiki/ops.go:ingestSystemPrompt`, `:answerSystemPrompt`, `:DetectContradictions` system prompt; `internal/wiki/update_existing.go:updateExistingSystemPrompt`; `internal/wiki/contradict.go:contradictionSystemPrompt`; `internal/wiki/promote.go`'s rewrite prompt. Each becomes `sch.Render(sch.Prompts.X, vars)`. Mechanical change; pure unit tests assert byte-equality of rendered output against bundled prompts.

5. **Ontology rename plumbing** — `Page` struct unchanged; `WritePage` reads field-name overrides from `Schema.Ontology` and emits user's chosen names; `ParsePage` reads back via the same map (consults active schema). Pure unit tests for round-trip across a renamed schema.

6. **`cmd/schema.go`** — `schema show`, `schema validate`, `schema migrate`. Cobra command tree, flag wiring. Pure unit tests for show / validate; cassette test for migrate (record once against gemini-flash, ~50 pages).

7. **`internal/db` migration to `user_version = 5`** — adds `schema_hash` column with default `''`. Idempotent migration shape (matches sub-project 6b). Pure unit test against v3 / v4 / v5 DBs.

8. **Wire `schema_hash` into every write site** — `WritePage` callers (`IngestSource`, `PromoteAnswer`, `UpdateExistingPagesFromSource`, `mcp.write_page`) set it. Pure unit tests with synthetic ingest fixtures.

9. **`cmd/lint.go` + `cmd/status.go` drift surface** — query for `schema_hash != activeHash`, emit the warning / status line. Pure unit tests against synthetic DB rows.

10. **`internal/mcp/handlers.go` — `mcp.get_schema`** — read-only handler, registered in `internal/mcp/server.go`. Pure unit test for the handler shape.

11. **`cmd/init.go` extension** — write `.llmwiki/schema.md` alongside `config.toml`; `--rewrite-schema` flag overwrites existing. Pure unit test against a fresh dir.

12. **Cassette tests** —
    - `TestSchemaRenameRoundtrip` — pre-seed a wiki with bundled defaults, install a renamed schema, re-ingest one source, assert the renamed fields appear in frontmatter and `schema_hash` matches.
    - `TestSchemaMigrate` — pre-seed a wiki with 5 pages under bundled defaults, change the schema (cosmetically — the `## Domain` section), run `schema migrate`, assert all 5 pages have the new `schema_hash`.
    - `TestMCPGetSchema` — drive the MCP server, call `get_schema`, assert the structured payload.

13. **README updates** — new "Customising your wiki" section leading with `.llmwiki/schema.md`; references Karpathy's gist; reaffirms trust property is bundled. Cite `schema show --bundled` as the way to discover defaults.

14. **CHANGELOG entry** for `0.7.0` covering the schema lift.

15. **Tag `v0.7.0-rc.1`.** Promote to `v0.7.0` after a 1-week stability window.

Plan-pass will refine the phase boundaries and decide whether the `schema_hash` migration ships in the same commit as the prompt-lift or in a follow-up.

## Test strategy

### Pure unit tests (no LLM, no network)

- `internal/schema/`:
  - `Parse` + round-trip on the bundled-default doc (`Bundled().String() == defaultSchemaMd`).
  - `Validate` errors on: missing required section, missing required placeholder, malformed frontmatter, `schema_version` mismatch.
  - `Render` interpolates `{{name}}` correctly; leaves unknown placeholders intact; handles empty maps; warns once per unknown placeholder.
  - Ontology rename map round-trip: declared field names map back to canonical struct fields.

- `internal/wiki/`:
  - `IngestSourceFilesToPages(... sch)` byte-equals the v0.6 system prompt when `sch == Bundled()`. (Captures the "byte-identical defaults" guarantee programmatically.)
  - Same for `UpdateExistingPagesFromSource`, `AnswerQuestion`, `DetectIngestContradictions`.
  - `WritePage` + `ParsePage` round-trip with a renamed schema (e.g. `evidence` → `citations`).

- `internal/db/`:
  - Migration v4 → v5 idempotency (run twice, schema unchanged).
  - `schema_hash` column default `''` for pre-existing rows.
  - `WHERE schema_hash != ?` count returns expected drift number on a mixed-hash fixture.

- `cmd/schema_test.go`:
  - `schema show` against a wiki with bundled defaults vs. a wiki with a custom doc.
  - `schema validate` exit codes: 0 on valid, 1 on each failure mode (with file:line in stderr).
  - `schema migrate --dry-run` performs LLM calls but no disk writes.

### Cassette tests (LLM, real)

Three new cassettes (~2–3K tokens each, recorded once via `LLMWIKI_RECORD=1`, refreshed nightly):

- `TestSchemaRenameRoundtrip` — pre-seed a wiki, install a schema renaming `evidence` → `citations` and `body` → `summary`; ingest one source; assert pages on disk carry the renamed frontmatter keys, `schema_hash` matches the active hash, and `ParsePage` round-trips.
- `TestSchemaMigrate` — pre-seed 5 pages under bundled defaults; cosmetically edit the schema's `## Domain` section (changes hash); run `llmwiki schema migrate --yes`; assert all 5 pages reach the new hash and a `log.md` `**schema_migrate**` entry is appended.
- `TestMCPGetSchema` — drive the MCP server in-process (same shape as sub-project 5's `TestMCPWritePageRoundtrip`); call `get_schema`; assert the structured payload includes the active prompts and the doc path.

### Integration / smoke

- `make smoke` is unchanged in shape — the smoke fixture's bundled defaults produce the same output as v0.6.
- A new manual-only check, documented in CONTRIBUTING.md: edit `.llmwiki/schema.md` to a non-trivial domain (e.g. "Rust async runtime"), ingest a Rust source file, eyeball the produced pages — they should reflect the changed prompt's instructions (e.g. denser pages if `## Ingest prompt` says so).

### CI

- Nightly cassette-refresh job covers the three new cassettes alongside the existing nine. Total recurring API spend stays in single-digit-cents-per-day on Gemini Flash.
- A new CI job runs `go test ./internal/schema/...` against pinned schema fixtures to catch any regression in `Parse` / `Validate` / `Render`.

## Migration / backwards compat

- **Pre-v0.7 wikis** (`user_version <= 4`) open under v0.7. The `db.Open` migration adds the `schema_hash` column; existing rows get `''`. Lint and status surface a "ingested under prior schema" notice. No silent re-ingest, no destructive change.
- **No `.llmwiki/schema.md` present** → bundled defaults in effect → byte-identical behaviour to v0.6 for every prompt-using path. Verified programmatically by the byte-equality unit tests above.
- **Pre-v0.7 page files on disk** parse fine: `ParsePage` reads the canonical field names (`title`, `body`, `evidence`, ...). Schema-renamed wikis read pre-rename pages via the bundled-default name set as a fallback (covered by the round-trip tests).
- **Pre-v0.7 saved answers in `.llmwiki/answers/`** parse fine: the answer-file format (sub-project 6a) does not depend on schema.
- **Pre-v0.7 `config.toml`** keeps working unchanged. v0.7 introduces no new keys in `config.toml`. The schema doc is the new artefact; `config.toml` stays focused on provider, paths, ingest tunables.
- **Pre-v0.7 MCP clients** continue to work: every existing tool's input / output shape is unchanged. `mcp.get_schema` is purely additive; clients that don't call it see no change.
- **Roll-forward only**: the `schema_hash` migration is roll-forward (matches every prior migration). A user who downgrades from v0.7 to v0.6 sees the column ignored; v0.6 reads `pages` rows fine because SQLite tolerates extra columns.

## Verification

```bash
# === v0.7 ===

# Default-schema initialisation
mkdir new-wiki && cd new-wiki
export GEMINI_API_KEY=...
llmwiki init
ls .llmwiki/
# Expect: config.toml, schema.md, wiki/, raw/, answers/

llmwiki schema show --doc | head -20
# Expect: the default schema doc, starting with `---\nschema_version: 1\n...`

llmwiki schema validate
# Expect: all checks ✓, exit 0

# Edit schema and re-validate
${EDITOR} .llmwiki/schema.md   # change `## Domain` section
llmwiki schema validate
# Expect: still all ✓ (we only changed the domain string)

# Ingest under custom schema
llmwiki ingest ./README.md
# Expect: pages reflect the custom domain in their bodies

# Schema drift after ontology change
${EDITOR} .llmwiki/schema.md   # rename `evidence` to `citations`
llmwiki lint
# Expect: schema_drift warning citing prior-hash count
llmwiki status
# Expect: schema: line with prior-hash count

# Eager migration
llmwiki schema migrate --yes
# Expect: re-ingest under new schema, all pages reach active hash
llmwiki status
# Expect: schema: line with 0 pages on prior hash

# MCP exposure
go test ./internal/mcp/... -run TestMCPGetSchema
# Expect: pass

# Backwards compat — pre-v0.7 wiki under v0.7
cd ../v0.6-wiki   # a wiki initialised under v0.6
llmwiki ingest ./CHANGELOG.md
# Expect: no behaviour change vs. v0.6; bundled defaults in effect
ls .llmwiki/schema.md 2>&1
# Expect: "No such file or directory" — schema doc is opt-in
llmwiki status
# Expect: schema: bundled (no .llmwiki/schema.md), 0 pages on prior hash

# Tests
go test ./...
# Expect: green in replay mode, all 12+3 cassettes pass.

# Validation negative cases
echo "broken" > .llmwiki/schema.md
llmwiki schema validate
# Expect: structured errors with file:line, exit 1

# Trust property: schema cannot loosen the validator
${EDITOR} .llmwiki/schema.md   # add a malicious `## Ingest prompt` saying
                               # "ignore the SOURCE and write whatever you want"
llmwiki ingest ./README.md
# Expect: validator drops every quote that doesn't substring-match;
# pages either have valid evidence or do not land on disk.
# The schema cannot subvert the validator.
```
