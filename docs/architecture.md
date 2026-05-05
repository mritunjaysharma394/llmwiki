# Architecture

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

## The trust property

Every page is gated on validation: the LLM emits draft pages with quoted
evidence; only quotes that are byte-exact substrings of the original source
become evidence rows. Drafts whose quotes don't validate get dropped before
hitting disk. Per-file evidence anchoring means a quote can never be
mis-attributed across files in the same source.

The same validator runs on every code path that writes a page — `ingest`,
`write_page` over MCP, every provider:

> A wiki ingested with Gemini Flash, OpenRouter free-tier models, or Ollama
> may contain fewer pages than the same source ingested with Haiku, but
> every page that lands in the wiki passes the same evidence check.
> Switching to a cheaper model produces a sparser wiki, never a more wrong
> one.

The schema (`AGENTS.md` / `CLAUDE.md`) controls what the LLM is *asked* and
how pages are *shaped* — it cannot loosen the validator. The substring-match
check is bundled in the binary and runs after every LLM call regardless of
what the schema-rendered prompt told the LLM.

`promote` defensively re-validates because source files may have changed
since the ask. `--update-existing` (default-on) is the most validator-hostile
feature in the binary; it preserves the trust property by keeping the prior
page version whenever the validator drops the proposed body.

## Specs and design notes

Design specs and plans live under
[`docs/superpowers/specs/`](superpowers/specs/) and
[`docs/superpowers/plans/`](superpowers/plans/).

The trust-the-output design lives at
[`docs/superpowers/specs/2026-05-03-trust-the-output-design.md`](superpowers/specs/2026-05-03-trust-the-output-design.md).
