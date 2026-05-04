---
schema_version: 1
generator: llmwiki
---

# llmwiki schema

This document defines how `llmwiki` shapes pages and prompts the LLM.
It is YOUR document — edit it to fit your domain. The bundled defaults
match `v0.7.0-rc.1`'s behaviour byte-for-byte: a v0.6 wiki opening under
v0.7 with no AGENTS.md sees zero behaviour change.

The trust property is bundled and not configurable here: every evidence
quote on disk substring-matches its named source file, byte-for-byte.
This document controls what the LLM is *asked* and how the page is
*shaped*. It does NOT control what counts as valid evidence.

## Domain



## Page ontology

  - title         (string)         the page's primary key; unique per wiki
  - updated_at    (RFC3339 ts)     last-write timestamp; date-only `updated:` twin emitted alongside
  - content_hash  (sha256)         body hash; recomputed at every write
  - source_ids    (list of int)    DB row IDs backing this page
  - tags          (list of strings) Obsidian/Dataview-friendly
  - sources       (list of paths)  derived from evidence; emitted by WritePage
  - created       (date)           first-ingest date
  - links         (list)           Obsidian wikilinks declared structurally
  - evidence      (list of quotes) verbatim spans from sources; required, >= 1
  - body          (markdown)       the page's narrative; lives below the closing ---

## Ingest prompt

You write wiki pages strictly grounded in the SOURCE provided.

The SOURCE may contain multiple files, each delimited by a header line:
    === path/to/file.ext ===
For every evidence quote, set "source_file" to the exact path shown in the
header above the file the quote was copied from. Quotes from different files
must each have their own evidence entry naming the correct file.

RULES:
1. Every page MUST include "evidence" — verbatim spans copied character-for-character from one of the files in SOURCE that justify the page's claims.
2. Each evidence entry SHOULD set "source_file" to the path from the "=== path ===" marker above its quote.
3. Do NOT include general knowledge that is not in SOURCE.
4. If SOURCE doesn't contain enough material for a high-quality page on a topic, do NOT create that page.
5. Better to return one solid page than five thin ones. Aim for 1-4 pages per call.
6. Page bodies should synthesize and organize, but every claim must be defensible from the evidence quotes you provide.
7. When linking pages, only reference existing pages or pages you are creating in this same call.{{domain}}

Existing wiki pages (titles only):
{{existing_titles}}

## Update-existing prompt

You update an EXISTING wiki page in light of a NEW SOURCE.
Output a single page with the same title; the body should incorporate
information from NEW SOURCE that refines, qualifies, or extends the
existing page. Every evidence quote must verbatim-substring-match
either the NEW SOURCE files OR the existing page's already-validated
quotes (those are listed under EXISTING EVIDENCE). Do not invent
quotes. If NEW SOURCE does not actually update this page, respond
with {"pages": []} and we will keep the page unchanged.{{domain}}{{existing_page_body}}{{existing_evidence}}

## Ask prompt

You answer using the provided wiki pages and source quotes.
Cite pages inline using [Page Title] notation.
When using a verbatim quote from a source, render it as a markdown blockquote and label it as (file:lines), e.g.:
> "channels block when full" (internal/sync/chan.go:4-4)
For PDF pages the file becomes "page-N":
> "the answer is 42" (page-3:2-2)

If pages and quotes are insufficient, say so plainly. Do not fabricate.{{domain}}

## Contradiction prompt

You are a contradiction detector for two wiki pages, A (newly written) and B (pre-existing). Each page has already-validated evidence quotes copied verbatim from real sources.

Output a JSON array of contradiction tuples. Each tuple is:
  {"a_quote": "<verbatim quote from page A's evidence>", "b_quote": "<verbatim quote from page B's evidence>", "description": "<one-sentence rationale>"}

ONLY flag direct factual contradictions where the two quotes assert mutually exclusive facts. The following are NOT contradictions and MUST be excluded:
  - Qualifications or additions (one page elaborates on the other).
  - Version-specific claims ("X applies in Go 1.21" vs "X applies in Go 1.22").
  - Different scopes (one page describes the general case, the other a special case).

Quote each side VERBATIM from the evidence list shown. If you would need to paraphrase, the pages are not contradicting; emit nothing for that pair.

If there are no contradictions, output the empty array: [].

## Promote rewrite prompt

You rewrite an LLM-generated answer into a polished wiki page body.

Preserve every verbatim source quote that appears in the input verbatim — they are
the load-bearing evidence the wiki's trust validator will re-check. You may
restructure prose, add headings, and tighten paragraphs; you may NOT alter,
shorten, or paraphrase any quoted span.

Return Markdown only — no preamble, no closing remarks, just the page body.{{question}}{{answer_body}}{{evidence_quotes}}

## Lint contradictions prompt

You are a wiki consistency checker. Identify factual contradictions between wiki pages.
List each contradiction as: "Page A vs Page B: <description>". If no contradictions, say "No contradictions found."

## Glossary

(empty by default; add domain-specific terms here, one per line, e.g.:
  - term: one-sentence definition)
