# Living wiki

Four behaviours that keep the wiki current as you use it. None of them weaken
the trust property; all of them are cheap.

## Cross-page updates at ingest

Default-on as of v0.8. When you ingest a new source, llmwiki edits existing
pages in light of it — folding the new source's claims into the pages whose
claims it refines, qualifies, contradicts, or extends. This matches
Karpathy's "modify 10–15 relevant pages in one pass" framing.

```text
llmwiki ingest ./CHANGELOG-1.2.md
# Ingested 3 page(s) from ./CHANGELOG-1.2.md
#
# Scanning 47 candidate page(s) for updates...
# 7 page(s) updated:
#   ~ Trust Property Validator   (+1 evidence)
#   ~ Ingest Pipeline            (+2 evidence)
#   ~ MCP write_page             (+1 evidence)
#
# 2 page(s) update FAILED — kept at previous version:
#   ✗ Database Layer
#       proposed body had 0 quotes that substring-matched any source.
```

The validator drops any proposed body that fails byte-exact substring-match,
so the trust property holds: every page on disk has ≥1 evidence quote that
substring-matches its source. Pages whose proposed update body fails
validation stay at their previous version — never silently downgraded.

Opt out persistently with `[ingest] update_existing = false` in
`.llmwiki/config.toml`, or for a single ingest with
`llmwiki ingest <source> --update-existing=false`.

### Cost shape

A 50-page repo ingest with `--update-existing` is roughly 5–10 ingest calls
+ up to 50 update calls + up to 5 contradiction calls = up to 65 LLM calls
per ingest. Tune the candidate caps via
`update_existing_max_candidates_per_source` (default 20),
`update_existing_max_candidates_total` (default 50), and
`update_existing_quote_floor` (default 2).

### Inspecting update outcomes

Every candidate considered — `updated`, `body_only`, `failed`, `skipped` —
appends one row to `page_update_log` in `.llmwiki/wiki.db`:

```bash
sqlite3 .llmwiki/wiki.db "SELECT pages.title, outcome, reason
                         FROM page_update_log
                         JOIN pages ON pages.id = page_update_log.page_id
                         ORDER BY created_at DESC LIMIT 20"
```

When a `~ Title (update_failed)` line appears, re-run with
`--debug-updates` to see why each candidate's quotes didn't match.
`llmwiki status` surfaces `pages updated total` and `pages update failed`
counters.

## Promote a saved answer into a permanent page

Every `llmwiki ask` archives its transcript under `.llmwiki/answers/`. When
an answer is good enough to keep, lift it into a real wiki page:

```bash
llmwiki ask "how does the validator work?"
ls .llmwiki/answers/
# 2026-05-04-150208-how-does-validator-work.md

llmwiki promote .llmwiki/answers/2026-05-04-150208-how-does-validator-work.md \
                --title "Validator Internals"
# ✓ all 4 quotes still substring-match their source files
# ✓ wrote page "Validator Internals" (4 evidence, 1 source)
# ✓ retro-linked 2 existing page(s) to [[Validator Internals]]
```

`promote` defensively re-runs every quote through the same substring-match
validator that gates `ingest` and `mcp.write_page`. If a source file
changed since the ask, the promote is rejected with `evidence_invalid` —
never silently writing stale content.

Flags: `--title` (otherwise derived from the answer's question),
`--rewrite` (off by default; opt-in for an LLM rewrite into wiki-style
prose), `--no-save` (skip the `log.md` entry, debug only). Same shape over
MCP via `promote_answer`.

### Auto-promote

Default-on as of v0.8. Every `llmwiki ask` runs a four-signal heuristic
gate (≥ 2 cited pages, ≥ 3 evidence quotes, length 100–3000 words, no
hedging phrases, no near-duplicate page); on pass, the saved answer is
promoted to a permanent page automatically — subject to the same byte-exact
validator. Output is one line: `→ filed as [[Title]]` or
`→ saved to <path> (<reason>)`.

Two locks guard the wiki: gate-fail or validator-fail leaves the answer in
`.llmwiki/answers/` for manual review (never silently dropped). Opt out
with `[ask] auto_promote = false`.

## Contradictions surface inline at ingest

When a new page's claim conflicts with an existing page's claim, the
conflict prints inline and appends to `<wikiDir>/contradictions.md`:

```text
!! 1 contradiction(s) flagged against the new pages:
   - new page "Channel Internals" claims:
       > "channel sends are now never lock-free"
     conflicts with existing page [[Go Concurrency]]:
       > "channel sends on uncontended channels remain lock-free"
     both quotes are validated against their own sources; resolve manually.
     logged to: .llmwiki/contradictions.md
```

`contradictions.md` is plain Markdown, append-only, Obsidian-readable.
Detection uses your configured provider. Failures of the contradiction call
never fail the ingest — the new pages still land.

## Retro-linker keeps the graph current

Every new page (from `ingest`, `promote`, or `mcp.write_page`) automatically
gets `[[Title]]` backlinks added to existing pages whose bodies mention it
in bare prose:

```text
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
