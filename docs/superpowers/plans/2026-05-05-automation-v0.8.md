# Sub-project 8 — Automation v0.8 (single-doc plan)

**Status:** ready to implement
**Date:** 2026-05-05
**Author:** Mritunjay Sharma (with Claude)
**Supersedes:** the prior `2026-05-05-automation-v0.8-design.md` and the multi-doc split. One opinionated doc, decisions made.

> **For agentic workers:** REQUIRED SUB-SKILL: `superpowers:subagent-driven-development`. Steps use checkbox (`- [ ]`) syntax.

---

## Context (one paragraph)

llmwiki has the three Karpathy layers (raw, wiki, AGENTS.md schema) and a unique trust property — the byte-exact substring validator that gates every write path. What it lacks is **autonomy**: every operation requires the user to type a command. v0.8 closes that gap by composing the primitives we already have into a continuously-running maintenance loop, plus one new long-running command (`llmwiki watch`) that turns "drop file in folder" into "wiki updates itself." No new philosophical layers, no new validation layer, no new providers — just the bookkeeping the gist says LLMs should be doing for the user.

## After v0.8 ships, llmwiki feels like

- I drop a file in `~/wiki/sources/` → 30 seconds later the wiki has new pages, existing pages were updated, contradictions were flagged.
- I ask `llmwiki ask "..."` → if the answer is good (cites real pages, validator passes, no near-duplicate page exists), it lands as a permanent page automatically. I see one line of output: `→ filed as [[Title]]`.
- A `launchd` job runs `llmwiki maintain` nightly → stale URL sources re-fetch, lint runs, pending answers promote, drift surfaces in the morning's status output.
- I finish a Claude Code session that touched `~/wiki/` → the session compresses into a saved answer, runs through the auto-promote pipeline, and either becomes a page or stays in `.llmwiki/answers/` for later review.

## Six design calls (the decisions you didn't have to make)

### 1. `[ingest] update_existing` defaults to `true`

The gist's ingest behavior — "modify 10–15 relevant pages in a single pass" — is the *default* shape it describes, not an opt-in. v0.6 shipped it default-off because we were nervous about LLM-call cost. The recommended provider is Gemini Flash, which is free; on Anthropic Haiku it's ~$0.30/ingest with caching, which is fine for the target user. Anthropic users who want to opt out write one config line. The validator drops bad updates as before — flipping the default doesn't change the trust property, only the daily-use posture.

**Cut:** the `--update-existing` CLI flag stays for explicit override; `[ingest] update_existing = false` opts out persistently. No other changes to the existing pass.

### 2. Auto-promote good answers, default ON, validator-gated

The gist explicitly says "good answers get filed back as new wiki pages rather than disappearing into chat history." Today `promote` exists but requires the user to `ls .llmwiki/answers/` and decide. v0.8 makes this the default: every `ask` runs a quality check, and answers that pass auto-promote.

**Quality gate (all must hold):**
1. Answer cites ≥ 2 distinct existing wiki pages (via `[Page Title]` notation), AND has ≥ 3 evidence quotes that pass the validator.
2. Answer length 100–3000 words.
3. No hedging phrases. Default list (overridable in config): `"i can't tell from the wiki"`, `"the sources don't cover"`, `"i'm not sure"`, `"insufficient information"`, `"the wiki doesn't say"`, `"unclear from"`. Case-insensitive substring match.
4. No existing page is a near-duplicate of the question. Check: BM25 over page titles + first 500 chars; if top match is within `auto_promote_skip_score = 5.0` (BM25 raw, default tuned to "page exists that already answers this"), skip auto-promote.

**The validator is the safety net.** A page that fails the quality gate is *kept* in `.llmwiki/answers/` for manual `promote` review — we never silently drop. A page that passes the gate but fails the validator (quote no longer substring-matches its source because the source changed since the ask) is also kept, with a `promote_failed` line in the ingest log.

**Cut:** no LLM-judged quality scoring (kytmanov-style "have the LLM grade itself" is unreliable and adds an LLM call per ask). The four signals above are mechanical, fast, and tunable via `[ask]` config.

**Why default-on, against nashsu's stance:** nashsu's "human curates, LLM maintains" rejects auto-promote because *they have no validator*. We do. The validator catches what would have been quality issues; the heuristic gate catches taste issues. Two locks. Default-on respects the gist's intent without compromising trust.

### 3. `llmwiki maintain` — one umbrella subcommand for cron + manual use

```
llmwiki maintain                  # run all sensible maintenance steps
llmwiki maintain --lint           # just lint
llmwiki maintain --refresh-stale  # just re-fetch URL sources whose hash changed
llmwiki maintain --promote-pending # just sweep answers/ for missed auto-promotes
llmwiki maintain --dry-run        # show what would happen, write nothing
```

Bare invocation runs `--lint --refresh-stale --promote-pending` (NOT `--update-existing`; that triggers per-source on ingest, not on a maintenance sweep). Composable, idempotent, exits non-zero if any step actually broke (not on cosmetic drift). Designed to live in a cron line. Mirrors `schema show / validate / migrate` precedent — one umbrella, three obvious verbs.

**Cut:** no `--all`, no `--quick`, no `--full`. Bare is the sensible default; flags are subset overrides. Don't invent flag taxonomy.

### 4. Ingest-tail lint runs FAST lint only

After every `ingest`, run a sub-second lint pass that surfaces *only actionable* issues inline. Fast lint = three checks:
- **Orphan detection**: pages with zero inbound `[[wikilinks]]`. Surface count + first 3 titles.
- **Missing cross-ref scan**: bodies that mention an existing page title in bare prose without a link. Use the existing retro-linker primitive but in scan-mode.
- **Schema-drift counter**: existing v0.7 surface; just include it.

Skip the slow checks (URL re-fetch for staleness, whole-wiki contradiction LLM call) — those run via `llmwiki maintain` from cron, not after every ingest. Fast lint is silent when clean.

### 5. `llmwiki watch <dir>` — fsnotify watcher with persistent queue

```
llmwiki watch ~/wiki/sources/
# watching ~/wiki/sources/ ... (Ctrl-C to stop)
# [+] paper-2024.pdf → queued
# [✓] paper-2024.pdf → 3 pages, 2 retro-links, 0 contradictions
```

- fsnotify on the directory; debounce 2s per file (don't fire on partial writes).
- New SQLite table `ingest_queue` in `wiki.db`: `(id, source_uri, enqueued_at, attempts, last_error, status)`. Crash-resumable: a `watch` restart picks up `status = 'pending' OR status = 'retrying'` rows.
- Retry policy: 3 attempts with exponential backoff (5s, 30s, 5min). After 3, status = `'failed'`, log to stderr, move on. Pattern from nashsu — production-grade safety rail.
- URL/feed polling **deferred to v0.9.x.** v0.8 = local fsnotify only. We add URL polling once we see how local-only feels in real use.
- `[watch]` config block: `dirs = []`, `debounce_seconds = 2`, `max_attempts = 3`. Empty default; user opts in by passing `<dir>` arg or by setting `dirs` in config.

**Why include in v0.8 instead of deferring to v0.9:** the watcher is the single feature that converts llmwiki from "CLI tool" to "living wiki" *as a perceived product*. Without it, "automation v0.8" is a marketing claim, not a UX shift. The queue is ~150 LOC; fsnotify is one stdlib-adjacent dep (`fsnotify/fsnotify`, already widely used in Go ecosystem).

### 6. Session capture via Claude Code Stop hook

New `llmwiki capture-session` reads a session transcript from stdin (Claude Code Stop hooks pipe the session JSON), extracts assistant turns that referenced `LLMWIKI_DIR` or called `llmwiki mcp` tools, files them as a saved answer, and runs the auto-promote gate from §2.

Wire via the user's Claude Code `settings.json`:
```json
{ "hooks": { "Stop": [{"command": "llmwiki capture-session"}] } }
```

**Ship:** the binary command + a 5-line copy-paste recipe in `docs/automation.md`. We don't auto-install hooks into the user's settings.

**Cut:** Cursor and Codex variants. Claude Code is the primary integration today (we already have an MCP server for it); Cursor / Codex hooks land if/when users ask. Over-engineering "every-IDE session capture" for v0.8 is scope-creep.

## Trust property invariants (3 lines)

1. Every page written via auto-promote, watch-mode ingest, or session-capture passes through the existing `wiki.ValidateAndAttachEvidence`. No new write path bypasses it.
2. Auto-promote requires *both* the heuristic gate AND the validator. Two locks; either failure → answer stays in `.llmwiki/answers/` (never silently dropped, never written downgraded).
3. The schema is not auto-edited. `AGENTS.md` is touched only by `init` and the user's editor.

## File touches

| File | Action | What |
|---|---|---|
| `cmd/maintain.go` | Create | New `maintainCmd` + flags + dispatcher |
| `cmd/maintain_test.go` | Create | Cassette + dry-run tests |
| `cmd/watch.go` | Create | New `watchCmd`; fsnotify loop + queue producer |
| `cmd/watch_test.go` | Create | fsnotify event delivery, debounce, queue-write |
| `cmd/capture_session.go` | Create | `captureSessionCmd`; stdin transcript → answer file → auto-promote |
| `cmd/capture_session_test.go` | Create | Transcript parsing + heuristic gate tests |
| `cmd/ask.go` | Modify | After `StreamAnswer`/`AnswerQuestion`, run heuristic gate; if pass, call `wiki.PromoteAnswer` with re-validation; print `→ filed as [[Title]]` line |
| `cmd/ask_test.go` | Modify | Auto-promote pass / fail (each gate condition) / validator-fail / dup-page-skip cases |
| `cmd/ingest.go` | Modify | After ingest, call new `wiki.FastLint`; surface orphan / missing-xref / schema-drift counts |
| `cmd/ingest_test.go` | Modify | Tail-lint output assertions |
| `cmd/init.go` | Modify | Default `[ingest] update_existing = true` in generated config; new `[ask] auto_promote = true`, `[watch]` block |
| `cmd/init_test.go` | Modify | Generated config assertion updates |
| `cmd/root.go` | Modify | Wire `maintain`, `watch`, `capture-session` into root cobra command |
| `internal/wiki/autopromote.go` | Create | `EvaluateAutoPromote(answer, db, cfg) (PromoteVerdict, reason string)` — the four-signal gate |
| `internal/wiki/autopromote_test.go` | Create | Per-signal unit tests |
| `internal/wiki/fast_lint.go` | Create | `FastLint(db, schema) FastLintResult` — orphans, missing xrefs, schema drift |
| `internal/wiki/fast_lint_test.go` | Create | Three-signal coverage |
| `internal/wiki/maintain.go` | Create | `RunMaintenance(opts MaintainOpts) MaintainResult` — composes existing primitives |
| `internal/wiki/maintain_test.go` | Create | Step-skip flag matrix |
| `internal/queue/queue.go` | Create | `Queue.Enqueue / NextPending / MarkSuccess / MarkRetrying / MarkFailed`; SQLite-backed; sits in `wiki.db` |
| `internal/queue/queue_test.go` | Create | Crash-resume, retry-backoff, concurrent-producer cases |
| `internal/db/db.go` | Modify | v6 migration: `CREATE TABLE ingest_queue (...)`, `PRAGMA user_version = 6` |
| `internal/db/queries.go` | Modify | Queue CRUD (or thin wrapper around `internal/queue/`) |
| `internal/wiki/promote.go` | Modify | `PromoteAnswer` gains optional `Source = "auto" \| "manual" \| "session"` arg for log differentiation |
| `internal/mcp/handlers.go` | Modify | Optional: surface auto-promote count in `mcp.ingest` return shape (`auto_promoted: int`) — small win, do it |
| `internal/mcp/server.go` | Modify | `serverVersion` → `"0.8.0-rc.1"` |
| `docs/automation.md` | Create | Cron recipes (launchd, systemd, GitHub Actions); Claude Code hook snippet; `watch` examples |
| `docs/assets/demo.gif` | Create | 30s screen capture: file-drop into watch dir → page appears → ask → auto-promote → contradiction-on-ingest |
| `tools/record-demo.sh` | Create | Demo recording script (asciinema/vhs); referenced by README's TODO comment |
| `README.md` | Modify | New "Always-on" section pointing at `docs/automation.md`; update `--update-existing` default note; flip onboarding line; replace broken hero image link |
| `CHANGELOG.md` | Modify | `## [0.8.0-rc.1] — 2026-05-05` entry |

Estimated diff size: ~1500 LOC of Go (incl. tests), ~300 LOC of docs/scripts. Roughly between v0.5 (small) and v0.7 (medium-large) by precedent.

## Implementation order (5 phases)

### Phase A — Foundation: queue + fast-lint + auto-promote evaluator (no UI yet)
- [ ] `internal/queue/queue.go` + tests
- [ ] DB v6 migration (`ingest_queue` table)
- [ ] `internal/wiki/fast_lint.go` + tests
- [ ] `internal/wiki/autopromote.go` (the four-signal gate) + tests
- [ ] Byte-equality test: existing tests still pass with no behavior changes

### Phase B — Auto-promote in `ask`
- [ ] Wire `EvaluateAutoPromote` into `cmd/ask.go` after stream completes
- [ ] On pass, call existing `PromoteAnswer` with `Source: "auto"`
- [ ] Print `→ filed as [[Title]]` (or `→ saved to .llmwiki/answers/<file>` on gate fail)
- [ ] `[ask] auto_promote`, `auto_promote_score_floor`, `auto_promote_hedging_phrases`, `auto_promote_skip_score` config keys
- [ ] Test matrix: pass / each gate fail / validator fail / dup-page skip

### Phase C — Ingest-tail lint
- [ ] Hook `FastLint` into the tail of `runIngest`
- [ ] Surface orphan / missing-xref / schema-drift counts inline (silent when clean)
- [ ] Default-on `update_existing` in generated config (and in `applyIngestDefaults`)
- [ ] Update tests for the new default

### Phase D — `maintain` umbrella + cron recipes
- [ ] `cmd/maintain.go` with bare + flag dispatch
- [ ] `internal/wiki/maintain.go` composes: refresh-stale (existing `currentHash` check + force-reingest), lint (full version, including LLM contradiction batch), promote-pending (sweep `.llmwiki/answers/` for missed auto-promotes)
- [ ] `docs/automation.md` with launchd / systemd / GitHub Actions recipes
- [ ] `--dry-run` end-to-end test

### Phase E — `watch` + session capture + demo
- [ ] `cmd/watch.go` with fsnotify producer + queue consumer goroutine
- [ ] Debounce, retry/backoff, graceful Ctrl-C
- [ ] `cmd/capture_session.go` reading stdin transcript → answer file → auto-promote
- [ ] Claude Code hook recipe in `docs/automation.md`
- [ ] Record `docs/assets/demo.gif`; add `tools/record-demo.sh`
- [ ] README rewrite + CHANGELOG entry + version bump
- [ ] Manual smoke test against a tiny real wiki + a 60s `llmwiki watch` session

## What we deliberately deferred

- **Graph-based lint** (Louvain communities, Adamic-Adar, isolated-page degree analysis). v1.0 — needs real-wiki data to calibrate thresholds. Borrowing nashsu's recipe wholesale without that data risks shipping noise.
- **URL/feed polling in watch mode.** v0.9.x — local fsnotify is the 80% case; URL polling is a separate goroutine, separate failure mode, separate config knob set. Ship after v0.8 reveals what users actually drop into watch dirs.
- **LLM-judged auto-promote scoring.** Permanent drop. Adds an LLM call per ask, is unreliable, and our four mechanical signals are good enough.
- **Cursor / Codex session-capture variants.** Bolt on if users ask. Claude Code is the primary integration via MCP today.
- **Auto-edit of `AGENTS.md`.** Permanent drop. Schema is the user's.
- **Web UI for any of this.** Permanent drop, consistent with prior subprojects.
- **Auto-source-discovery (web crawler that finds new sources).** Permanent drop. Editorial control is the user's.

## What this borrows from each peer

- **kytmanov/obsidian-llm-wiki-local**: `watch` daemon shape + the `--auto-approve` posture (we go further than they can because we have a validator).
- **nashsu/llm_wiki**: persistent queue with crash recovery + retry. Their graph-lint deferred to v1.0 pending calibration.
- **skyllwt/OmegaWiki**: cron-as-automation. We document recipes instead of building infra.
- **Pratiyush/llm-wiki**: session-end transcript capture as an ingest source.
- **rohitg00 LLM Wiki v2 gist**: the event-hook taxonomy (on-write = auto-promote, on-ingest-tail = fast lint, on-session-end = capture, on-schedule = maintain).
- **Karpathy original**: the gist's "modify 10–15 pages in one pass" default; the "good answers get filed back" default.

## Verification

- All existing tests pass unchanged (no behavior regressions on the unmodified path).
- New tests for each phase per the file table.
- Manual smoke: drop a `.md` file into a watched dir → page appears within 5s; `ask` against a known-answered question → auto-promotes; `maintain --dry-run` lists what it would do; restart `watch` mid-process → queue resumes from the right offset.
- Demo gif recorded against the real binary, not a mockup.

---

**End of plan.** Implementation session can run from this doc + `superpowers:subagent-driven-development` with no further design pass.
