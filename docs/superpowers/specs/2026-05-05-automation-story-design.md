# Sub-project 8 — Automation Story (v0.8 → v0.9 → v1.0 north-star)

**Status:** design — awaiting user feedback before plan-pass
**Date:** 2026-05-05
**Author:** Mritunjay Sharma (with Claude)

## Context

`llmwiki v0.7.0-rc.1` shipped on 2026-05-04 and closed the third Karpathy layer: a user-owned `AGENTS.md` / `CLAUDE.md` schema doc lifted the bundled prompts and the page ontology out of the binary. Add that to v0.6's cross-page page-update pass, v0.5's MCP surface, and v0.3–v0.4's deterministic substring-match validator, and `llmwiki` is now a coherent reference implementation of the gist's three-layer architecture (raw sources, the wiki, the schema), with a trust property — every evidence quote on disk substring-matches its named source file, byte-for-byte — that no peer in the ecosystem ships.

There is one large piece of Karpathy's pattern that we have not yet instantiated, and it is the piece every adjacent project either hand-waves past or gets noticeably wrong: **automation**. Karpathy's gist (https://gist.github.com/karpathy/442a6bf555914893e9891c11519de94f) opens by arguing that "the tedious part of maintaining a knowledge base is not the reading or the thinking — it's the bookkeeping." The LLM owns the wiki entirely; the user directs (points at sources, asks questions); the LLM does the integration. Three operations recur: **ingest** (read source → discuss takeaways → integrate into 10–15 pages in one pass), **query** (run against the wiki first, raw retrieval as fallback, file good answers back as new pages), **lint** (periodic health check: contradictions, stale claims, orphan pages, missing cross-references, data gaps). The first two are reactive ("the user invokes them"); the third is intrinsically background ("runs without prompting"). The overall posture the gist describes is a wiki that **maintains itself** in the gaps between the user's direct invocations.

`llmwiki` today is a CLI-invoked accumulator. `ingest`, `promote`, `lint`, `schema migrate` all run when (and only when) the user types them. The cross-page page-update pass that v0.6 added — the closest thing to the gist's "modify 10–15 relevant pages in one pass" behaviour — is gated behind `--update-existing`, opt-in by default. `lint` covers staleness, contradictions, and (after v0.7) schema-drift, but not the gist's other three lint dimensions: **orphan pages**, **missing cross-references**, and **data gaps**. URL and feed sources go stale silently. `promote` is fully manual: the user has to `ls .llmwiki/answers/` and pick. There is no file watcher, no daemon, no scheduled sweep, no Claude-Code session hook. The "good answers get filed back as pages" feedback loop the gist describes is something the user has to remember to close by hand.

This is the automation gap, and it is what sub-project 8 fills. The work spans three releases — v0.8 ("always-on maintenance"), v0.9 ("the watcher"), v1.0 ("graph lint") — because the architectural surface is too large to ship at once and because the risk profile escalates monotonically: v0.8 is composition-of-existing-primitives only, v0.9 introduces the binary's first long-running process and a persistent queue, v1.0 lights up new analytical lint dimensions whose signal is genuinely novel. Every phase preserves the trust property — every page written via any automation path goes through `wiki.ValidateAndAttachEvidence` — and every phase preserves the "schema is the user's, not the agent's" invariant — no automation feature edits `AGENTS.md` / `CLAUDE.md`.

This north-star spec sets the shape of all three releases. The v0.8 spec (`2026-05-05-automation-v0.8-design.md`) and v0.8 plan (`2026-05-05-automation-v0.8.md`) drill into the first cycle. v0.9 and v1.0 ship later cycles; this spec scopes them at depth sufficient to argue that v0.8's design decisions (the umbrella `maintain` subcommand, the auto-promote heuristic shape, the cron-recipe-not-daemon stance) carry forward without needing to be revisited.

## Why automation is the right next axis

Three signals make sub-project 8 the right next move, and each one has gotten louder since v0.7 shipped.

1. **The peer ecosystem has converged on automation as the next frontier.** The five projects we benchmarked through v0.5–v0.7 (Lucas, Pratiyush, nashsu, OmegaWiki, kytmanov) have all shipped some form of automation in the last six weeks. nashsu's persistent ingest queue with crash recovery + 3× retry is the most sophisticated; kytmanov's `olw watch` daemon + `auto_approve = true` is the most aggressive; OmegaWiki's `/daily-arxiv` cron skill is the cheapest first cut; Pratiyush's coding-session-transcript ingest is the most novel automation source. Lucas explicitly rejects the daemon path and says automation should come from the agent runtime, not the wiki binary. **Five different answers to the same question.** None of them has the trust property; every one of them has shipped before us. Every additional week we wait, an adjacent project runs another ingest cycle on a wiki their automation kept current and ours did not. The positioning move is "automated like kytmanov, safer than kytmanov" — and it requires shipping.

2. **Every recent v0.7 user-feedback cycle has surfaced the same friction.** "I forgot to run `llmwiki ingest` for two weeks; my reading-list feed is now full of stale URLs." "I asked a great question, the answer was great, then I closed my laptop and never promoted it." "I changed three sources and now I have to remember which pages to re-touch with `--update-existing`." All three resolve to the same root: the binary does not act unless the user types. **The user is the bottleneck**, and automation is the only way through it.

3. **The infrastructure we shipped through v0.5–v0.7 was specifically the precondition for safe automation.** v0.5's MCP server is the agent-side handle; v0.6's cross-page update pass is the "edit ten pages on one ingest" body; v0.6's `page_update_log` is the audit trail every automated action will write to; v0.7's user-editable schema means "the agent acts within bounds the user defined." Every previous sub-project paid for sub-project 8's pre-conditions. Shipping it now lets us claim the architectural arc retroactively.

There is also a self-pacing consideration: the v0.8 surface is small (one umbrella subcommand, one default flip, one ingest-tail lint hook, one auto-promote heuristic, three cron recipes) and re-uses every primitive that already exists. The v0.9 surface (file watcher, persistent queue, session-end hook) is the riskiest piece of work in the binary's history; doing v0.8 first means the auto-promote shape, the umbrella subcommand, and the "what does the maintenance summary line look like" UX questions are already settled when we start the watcher. The v1.0 surface (graph-based lint dimensions) lights up only after the watcher has accumulated enough state to make the graph signals meaningful. The order is forced.

## Peer ecosystem readout (cited explicitly because the spec leans on them)

We surveyed five active LLM-wiki implementations during the v0.7 cycle. The automation positions and what we steal from each:

- **lucasastorian/llmwiki** — Claude/MCP-driven; explicit philosophy that "automation should come from the agent runtime, not a daemon." File watcher exists for a raw folder but every page write goes through a Claude conversation. **Lesson: this is the "minimal automation" extreme; we should articulate why we're rejecting it.** We are not Claude-only; we ship a CLI that runs without an agent attached; cron is a load-bearing target for us in a way it isn't for them.

- **Pratiyush/llm-wiki** — built around capturing coding-session transcripts (Claude Code, Cursor, Codex, Copilot) as the ingest source. The wiki absorbs work as you work. **Lesson: session-end hooks are a real automation pattern, not a contrived one.** v0.9's `llmwiki capture-session` subcommand wired via Claude Code's `Stop` hook (https://docs.anthropic.com/en/docs/agents-and-tools/claude-code/hooks) is directly Pratiyush-shaped, but our version validates every quote against actual source-file bytes before promoting.

- **skyllwt/OmegaWiki** — Claude Code Skills + GitHub Actions cron. Ships `/daily-arxiv` for daily auto-fetch. No daemon — cron does periodic work. **Lesson: cron-as-automation is the cheapest first step; document a recipe before building infrastructure.** v0.8's plist + systemd unit + GH Actions workflow yaml are this lesson taken seriously: ship the recipe in `docs/automation/`, not the daemon.

- **nashsu/llm_wiki** — Tauri desktop app; two-step ingest chain (analysis → generation); **persistent ingest queue, disk-backed, with crash recovery and 3× retry**; SHA256 incremental cache; **async review queue** for items the LLM flags. Graph-based lint: **Louvain community detection** (low-cohesion clusters < 0.15), isolated pages (degree ≤ 1), Adamic-Adar surprising-connection scoring. Stance: "Human curates, LLM maintains" — explicit anti-auto-promote. **Lesson: the persistent queue is the safety rail that makes automation reliable; the graph-based lint signals are the published recipe for the gist's missing lint dimensions.** v0.9 steals the queue shape (SQLite table, exponential-backoff retry); v1.0 steals the Louvain + Adamic-Adar recipe verbatim. nashsu's anti-auto-promote stance is the strongest argument for our v0.8 Q2 ("auto-promote default — on or off?") landing on **off** by default; we ship the heuristic but require explicit opt-in.

- **kytmanov/obsidian-llm-wiki-local** — `olw watch` daemon (drop file in raw/ → ingest+compile auto-fires) + `olw run --auto-approve` and `auto_approve = true` in `wiki.toml` for fully unattended pipeline + `olw maintain --fix` for broken-alias repair. **Lesson: the most aggressively automated CLI in the ecosystem; their `--auto-approve` is risky because they have no validator.** Ours can be safer-than-theirs because the validator gates every write. The `llmwiki maintain` subcommand name is borrowed from kytmanov directly (their `olw maintain --fix` is the closest semantic neighbour); we use the same word so users moving between the two CLIs find what they expect.

- **rohitg00 LLM Wiki v2 gist** (https://gist.github.com/rohitg00/2067ab416f7bbe447c1977edaaa681e2) — purely architectural; the **automation roadmap Karpathy's original left implicit**. Lists event-driven hooks: on-ingestion (auto-extract, update graph, refresh index), on-session-start (load relevant context), on-session-end (compress observations, file insights if quality > threshold = auto-promote), on-schedule (periodic lint, consolidation, retention decay), on-write (contradiction check, supersession). **Lesson: cite this directly as the architectural blueprint for our phasing.** v0.8 implements on-ingestion + on-write (already partially shipped through v0.6) + on-schedule (cron recipes); v0.9 implements on-session-end; v1.0 extends on-schedule with the graph-lint dimensions. on-session-start is deliberately deferred — the existing `mcp.get_schema` + `mcp.list_pages` already cover the agent-side shape rohitg00 describes for it, and a per-Claude-Code-session loader is out of scope.

## Recommended phasing

Three releases, in strict dependency order. Each is a single shippable cycle.

### v0.8 — "Always-on maintenance" (no daemon, no new long-running process)

The first cycle is composition-of-existing-primitives only. We add no new long-running processes, no new external integrations, no new lint dimensions; we wire the primitives we already have into a posture that automatically runs them in the gaps between the user's direct invocations. Five user-visible additions:

1. **Flip `[ingest] update_existing` default to `true`.** The CHANGELOG entry for v0.6 explicitly flagged this as a v0.7 candidate ("Consider flipping the default in v0.7 once we have real-world numbers from opt-in users"). v0.7 deferred (the schema lift was sized to fit a single cycle). v0.8 is when the flip happens. The cost picture (~$0.30/ingest on Anthropic Haiku, free on Gemini Flash) and the validator-interaction picture (pages whose proposed body fails validation stay at their previous version, never silent downgrade) are both well-understood now. The flip turns "the wiki updates itself when reality updates" from opt-in to default — which is the behaviour Karpathy's gist describes. Users who want the v0.7 posture set `update_existing = false` once.

2. **Ingest-tail lint pass.** After every successful `ingest`, silently run a "fast" lint pass — schema-drift check + retro-link sweep (already in v0.6) — and surface any actionable items inline at the bottom of the ingest output. The full `llmwiki lint` (which does network HEAD requests for staleness + per-batch contradiction LLM calls) stays as the explicit user-invoked "audit my whole wiki" command; the ingest-tail variant skips the network and skips the contradiction batch (those run on the new pages only, already, via v0.6's contradiction-on-ingest pass). This wires the gist's "periodic health check" into every operation, where "every operation" replaces "periodic" with "every time the user does anything." Cost: zero LLM calls beyond what already runs.

3. **Auto-promote heuristic.** After every `ask`, evaluate a quality score over the answer. The score is a documented, config-tunable formula over: (a) `len(citations) >= floor`, (b) the LLM did not emit hedging phrases ("I'm not sure", "the wiki doesn't say", "insufficient information"), (c) FTS distance from question to nearest existing page-title is above a threshold (no existing page already answers this), (d) answer length is in a sensible range (not a one-liner, not a 5K-token essay). When the score crosses a threshold, auto-call `wiki.PromoteAnswer` with a derived title. The validator gates the write; bad pages are dropped. The score is logged to `log.md` regardless of outcome so users can tune the formula against their own wiki. **Default: off** (per peer-ecosystem stance from nashsu). Opt-in via `[ask] auto_promote = true` in `config.toml`, or `--auto-promote` per-call. Once on, opt-out per-call via `--no-auto-promote`. Two locks gate every auto-promote: the validator must keep at least one quote AND the heuristic score must clear the floor. Either lock failing means the answer stays a saved answer, not a permanent page.

4. **Cron / scheduler recipes shipped as docs and `make` targets.** A `launchd` plist for macOS, a systemd unit for Linux, a GitHub Actions workflow YAML — each calling `llmwiki maintain` (the new umbrella subcommand below). These ship in `docs/automation/` as Markdown recipes the user copies into their own setup. We are not shipping the daemon; we are shipping the recipe. This matches OmegaWiki's playbook deliberately: it's the cheapest possible automation surface, and it works on every Unix-like machine without us writing a watcher or maintaining cross-platform service installation logic.

5. **`llmwiki maintain` umbrella subcommand.** A new top-level command that composes the existing primitives into one idempotent invocation. Bare `llmwiki maintain` runs everything sensible (refresh stale URLs, run lint, promote pending answers above the heuristic floor); flag-extensions (`--refresh-stale`, `--lint`, `--promote-pending`, `--update-existing`) let cron / scripts target a subset. Useful both as the cron target and as the "I'll just press the button" command. The shape is borrowed from kytmanov's `olw maintain` directly.

The v0.8 spec (`2026-05-05-automation-v0.8-design.md`) drills into each of these in detail. Trust property invariant for v0.8: every write path that any of these features touches goes through `ValidateAndAttachEvidence`. The flip in (1) does not loosen validation; the auto-promote in (3) requires both the validator AND the heuristic floor; the cron recipe in (4) calls a binary that itself passes through the same gates.

### v0.9 — "The watcher" (long-running process; persistent queue)

The second cycle introduces the binary's first long-running process. Three user-visible additions, all gated behind the new `[watch]` config block.

1. **`llmwiki watch <dir>` — file watcher + URL-source poller.** `fsnotify` over a sources directory + per-source TTL polling for HTTP / feed / sitemap sources. Drops events into a persistent queue (see (2) below). On a file event, the watcher does not run `ingest` directly; it enqueues the source URI and the queue runner picks it up. Decoupling the event-detection path from the LLM-call path is what makes the watcher debuggable: a flood of file events does not mean a flood of in-flight LLM calls.

2. **Persistent ingest queue (new SQLite table `ingest_queue`).** Columns: `id, source_uri, enqueued_at, attempts, last_error, status`. Crash-resumable. Retry with exponential backoff up to `[watch] retry_max` (default 3). The pattern is borrowed from nashsu directly — their queue is the closest peer, and the design considerations (SQLite for crash safety, exponential backoff for transient HTTP failures, status enum for `pending` / `running` / `failed` / `done`) are the same. **Question (Q9 below): does the queue live in `wiki.db` or a separate `queue.db`?** Recommended answer: same DB, sibling table to `page_update_log`, because the audit-trail story is consistent and a corrupted queue does not imply a corrupted wiki (we restore from the log).

3. **Claude Code session-end hook.** A new `llmwiki capture-session` subcommand reads a Claude Code session transcript from stdin (the `Stop` hook's input format), files it as a saved answer (using the existing `wiki.FormatSavedAnswer` shape), and runs the v0.8 auto-promote heuristic. Wired via Claude Code's `Stop` hook config in `.claude/settings.json`. The user opts in by editing their settings file; we ship the binary command + a documented hooks snippet under `docs/automation/`. Pattern stolen from Pratiyush + rohitg00 v2.

v0.9 also adds a `[watch]` config block (poll interval, retry cap, sources allow-list) and a `--watch-debug` flag on `maintain` that dumps the queue state. Trust property invariant: every queue runner write path goes through `wiki.IngestSource`, which goes through `ValidateAndAttachEvidence`. The watcher cannot circumvent the validator — it only feeds inputs into a path that already validates.

### v1.0 — "Graph lint" (covers gist's missing lint dimensions)

The third cycle lights up the lint dimensions Karpathy's gist names but `llmwiki` does not yet implement: orphan pages, missing cross-references, data gaps. Plus two graph-analytical signals taken directly from nashsu's published recipe.

1. **Orphan-page detection.** Pages with zero inbound `[[wikilinks]]`. v0.5's retro-linker is preventive at write-time (every new page gets back-linked from existing pages that mention it); orphan-detection is curative (pages that pre-date the retro-linker, or that nothing mentions in bare prose, surface here).

2. **Missing-cross-reference detection.** Page X mentions title Y in bare prose without a `[[Y]]` link. The retro-linker only fires on new-page write; lint extends the same scan to every existing-pair. Cheap (string-match only); idempotent; surfaces as a list with line numbers. `lint --fix` (4 below) auto-resolves a subset.

3. **Data-gap detection.** Pages with stub-shaped bodies (length < floor, evidence count < floor, or whose source files have shrunk between the time the page was written and now). Surfaces as "this page may be under-evidenced relative to its sources."

4. **Louvain communities** (per nashsu's recipe). Surface low-cohesion clusters (cohesion < 0.15) as "your wiki has 3 disconnected sub-graphs; consider whether they belong together." Cohesion threshold tunable.

5. **Adamic-Adar surprising connections.** Flag page pairs with unexpectedly high shared-neighbour scores (top decile by Adamic-Adar). These are pages that should probably be linked but aren't — the analytical inverse of orphan-detection. Cheap to compute over the link graph; the whole-wiki run is O(N²) but N is small (target wiki size: ≤ 500 pages).

6. **`lint --fix`.** Where the LLM can auto-resolve a finding, do so. Specifically: clearly add a missing `[[link]]` because the body literally names the page (no LLM call needed; pure string-replace, idempotent), mark older claim as "see also [[newer]]" when contradiction is a clean version supersession (one LLM call per pair, validator-gated). Falls back to surfacing for human resolution otherwise.

Trust property invariant: `lint --fix` writes through `wiki.WritePageWithSchema` + `wiki.ValidateAndAttachEvidence` like every other write path. Pages whose `--fix` proposal fails validation stay at their previous version. The graph-analytical signals (Louvain, Adamic-Adar) are read-only — they surface findings but do not write pages.

## Why this scoping (rejected alternatives)

We considered three other shapings and rejected each:

- **"Ship everything in one v1.0 cycle."** Rejected. The architectural surface is too large and the risk profile is non-uniform: a daemon failure mode in v0.9 cannot land in the same release as a "default flip" in v0.8 because the rollback semantics are different. v0.8's flip is a config change; v0.9's daemon corruption is a wedge. We ship them separately so a v0.9 hotfix never has to roll back v0.8 behaviour.

- **"Ship the watcher first; cron recipes second."** Rejected. The watcher is the riskiest piece in the binary's history; doing it before we have v0.8's umbrella subcommand means we ship a watcher that calls primitives directly, then re-do the wiring when the umbrella lands. The umbrella subcommand is the watcher's call target; ship it first.

- **"Ship graph-lint without the watcher."** Possible but cargo-cult. Graph-lint signals are most useful when the wiki is fresh; a stale wiki has stale graph signals. Shipping the watcher first means the wiki is fresh by the time graph-lint lands. The order is dependency-driven.

So: three cycles, in strict order, each implementable from a fresh subagent-driven-development session. v0.8 is the only one this round of plan-pass covers; v0.9 and v1.0 will get their own design + plan passes when v0.8 ships and stabilises.

## Hard invariants every phase must preserve

These are the load-bearing properties. Any spec, any plan, any implementation that violates one of these is wrong, regardless of how shiny the feature is.

1. **The trust property is non-negotiable.** Every page written via any automation path goes through `wiki.ValidateAndAttachEvidence`. Pages that fail validation are dropped or kept-at-previous-version (mirroring `--update-existing`'s semantics from v0.6). No automation feature loosens the validator. This is the differentiator the v0.5–v0.7 cycle paid for; we do not give it back to ship faster.

2. **The schema is the user's, not the agent's.** No automation feature edits `AGENTS.md` / `CLAUDE.md`. The schema migration command stays user-initiated (existing `schema migrate --yes` opt-in from v0.7). An auto-fix that wanted to "update the schema to match what the wiki actually looks like" is a permanent drop.

3. **No schema-as-code, no executable hooks IN the schema.** Carried forward from v0.7's spec (line 68). Hooks live Claude-Code-side or cron-side, never inside `AGENTS.md`. We do not ship a `## Hooks` section the user fills with shell commands; that is the kind of magic that breaks trust.

4. **Always-resumable, always-debuggable.** Every automated action writes a row to an append-only log: `page_update_log` already exists for v0.6's update pass, `ingest_queue.attempts` for v0.9's queue runner, `auto_promote_log` (new in v0.8) for v0.8's auto-promote outcomes. `--debug-*` flags surface per-candidate verdicts. No silent state.

5. **Auto-approve is gated on the validator AND a separate quality floor.** "The validator dropped 0 quotes" is necessary but not sufficient for auto-promote; auto-promote also requires the heuristic-score floor. **Two locks, not one.** This is what makes our `--auto-approve` story safer than kytmanov's.

6. **No web UI for any automation surface.** Permanent drop, consistent with sub-projects 2, 6, 7. Automation is headless and scriptable; if you want a UI, render the queue in Obsidian via Dataview over `wiki.db` (which is a documented surface).

## Non-goals (deferred / dropped)

- **Web UI for any automation surface.** Permanent drop.
- **Auto-editing the schema.** Permanent drop.
- **Auto-source-discovery (a web crawler that finds new sources for the user).** Editorial control is the user's. Permanent drop.
- **Multi-user / team collaboration.** Out of scope for v0.8–v1.0; v1.x+ question.
- **Real-time streaming of changes between watcher and MCP clients.** v1 of our MCP server is tools-only (per sub-project 5's non-goals); v0.9's watcher does not send `progress` notifications. **Deferred to whenever we add MCP `progress`.**
- **Auto-quarantine of failed-validation drafts.** When the validator drops a proposed page, it stays dropped; we do not write it to a `quarantine/` directory for human review. Mitigation: the existing `--debug-updates` flag prints the LLM's proposed body to stderr; users pipe to a file if they want to inspect. **Permanent drop** — quarantine creates a parallel "wiki" the user has to maintain, defeating the point.
- **Cross-wiki automation** (a shared scheduler that ingests from one wiki into another). Out of scope. Each wiki is its own dir; cron recipes are per-wiki. **Deferred indefinitely.**
- **LLM-judged auto-promote** (rather than the heuristic in v0.8). One extra LLM call per ask to ask "is this a good answer?" was considered and rejected on cost: at one Gemini Flash call per ask it adds N calls to every interactive session. The heuristic is good enough for 90% of cases; the 10% miss surfaces as "this answer wasn't auto-promoted, run `llmwiki promote` manually" which the user can do. **Permanent drop in v0.8; revisit if user feedback surfaces a real miss rate.**
- **Image / multimodal automation** (auto-OCR scanned PDFs, auto-describe images on ingest). Still nashsu's niche; we do not have the multimodal infrastructure. **Deferred to v1.x.**

## Trust-property reaffirmation

Every automation feature this spec describes preserves the trust property. We say so explicitly because each phase introduces a new write-path or a new write-path-trigger, and the ecosystem's other automation stories visibly do not.

- **v0.8 default flip (`update_existing = true`).** Already validated: the v0.6 cross-page update path runs `wiki.ValidateAndAttachEvidence` over every proposed body before any disk write. Pages whose proposed body fails validation stay at their prior version (`page_update_log` records the failure with `outcome = 'failed'`). The flip changes the default, not the gate.

- **v0.8 ingest-tail lint.** Read-only. Surfaces findings; does not write pages.

- **v0.8 auto-promote heuristic.** Two locks. Lock 1 is the validator (`wiki.PromoteAnswer` already calls `wiki.ValidateAndAttachEvidence` defensively against fresh source-file bytes; v0.5's promote path is the gatekeeper). Lock 2 is the heuristic-score floor. Both must clear; either failing means the answer stays a saved answer. The auto-promote outcome (whether the heuristic cleared, whether the validator kept any quotes, whether the page reached disk) is logged to `auto_promote_log` regardless.

- **v0.8 cron recipes.** Shell out to `llmwiki maintain`, which calls only paths that already validate.

- **v0.8 `llmwiki maintain` umbrella.** Composes existing primitives. Each primitive validates as it does today.

- **v0.9 watcher.** The watcher is event-detection + queue-enqueue only. The queue runner calls `wiki.IngestSource`, which validates. The watcher cannot bypass the validator; it does not have a write path of its own.

- **v0.9 session-end hook.** `llmwiki capture-session` writes a saved answer (validated already by ask-time) and optionally calls auto-promote (validated by promote's defensive re-validation). Both paths are pre-existing gates.

- **v1.0 `lint --fix`.** Writes pages through `wiki.WritePageWithSchema` + `wiki.ValidateAndAttachEvidence`. Pages whose `--fix` proposal fails validation stay at their previous version.

The sentence we will keep saying in commit messages and in the README: **"the validator runs after every LLM call, regardless of which automation feature triggered the call."**

## Open questions

Numbered for user feedback. First-cut answers are the spec's working defaults; the user picks on review and the v0.8 plan inherits the resolutions. Questions specific to v0.9 or v1.0 are marked, and may be deferred to those cycles' own design passes.

### Q1. Should v0.8 ship `llmwiki maintain` as one umbrella subcommand or as flag-extensions on existing commands?

The tradeoff is discoverability vs surface-area sprawl. v0.7's `schema` group (with `show` / `validate` / `migrate` subcommands under one parent) is the closest precedent and the user reaction to it was positive ("schema is its own thing, group it"). Maintenance is similarly its own thing.

  - (a) **Umbrella subcommand `llmwiki maintain`** with `--refresh-stale`, `--lint`, `--promote-pending`, `--update-existing` as composable flags; bare invocation runs all sensible defaults.
  - (b) Flag-extensions on existing commands: `llmwiki lint --refresh-stale`, `llmwiki ingest --auto-promote`, `llmwiki ask --auto-promote`. No new top-level command.
  - (c) Both: the umbrella command exists AND the per-command flags exist.

  **Recommended: (a).** The umbrella is the cron / launchd / systemd target; documenting one command is clearer than documenting four. Per-command flags clutter the surface; users iterating interactively don't need them, and cron config is easier to read with one binary call.

### Q2. Auto-promote default — on or off?

  - (a) **On by default.** Matches gist intent ("good answers get filed back as pages"). The validator + heuristic-score combination de-risks "on."
  - (b) **Off by default.** Matches nashsu's "human curates, LLM maintains" stance. Users who want it set `auto_promote = true` once in config; per-call `--auto-promote` always works.
  - (c) **On for new wikis, off for upgrade-from-v0.7 wikis.** Idempotent: `init` writes `auto_promote = true`; existing configs keep their (absent → off) state.

  **Recommended: (b).** Auto-writing pages on every `ask` is surprising for a new user; the heuristic mis-fire is "an extra page lands in your wiki" which is editorially loud even if the trust property holds. Document the flag prominently in the README's automation section. Revisit in v0.9 once we have data.

### Q3. Auto-promote quality-score formula — what signals?

The heuristic is config-tunable; we ship a formula and reasonable defaults but expose every weight. Suggested signals (pick all four; the v0.8 plan implements them):

  - (a) `len(answer.evidence) >= floor` (default `floor = 2`). Below this the answer is too thin to be a page.
  - (b) `answer_text` does not contain hedging phrases. Default phrase list: `["i'm not sure", "the wiki doesn't say", "insufficient information", "i don't have enough", "based on the available pages it appears", "i'm not certain"]`. Case-insensitive substring match.
  - (c) FTS distance from `question` to nearest existing page-title is above threshold (default `threshold = 0.3` over `pages_fts.bm25` normalised). Below this an existing page already answers; promoting creates a duplicate.
  - (d) `len(answer_text) in [200, 5000]` chars. Outside this range the answer is too thin or too sprawly to be a page.

  Each signal yields a 0-or-1 vote; threshold is `votes >= 3` (default; configurable via `[ask] auto_promote_min_votes`). Ship all four; the formula and weights are in `[ask]` config so users can tune per wiki.

### Q4. Ingest-tail lint — full lint or fast lint?

Today `lint` does staleness (network HEAD calls, slow; one round-trip per source) + contradictions (one LLM call per page-batch of 10) + schema-drift (instant; one DB query). Running the full lint on every ingest tail multiplies ingest cost.

  - (a) **Fast-lint only**: skip staleness re-fetch (slow + redundant on the source we just ingested), skip whole-wiki contradiction batch (the new pages already had contradiction-on-ingest run during the same `ingest`), keep schema-drift check + a "this ingest's new pages list" sanity scan. Total added cost: zero LLM calls, ~100ms.
  - (b) **Full lint** including staleness + contradiction batch.

  **Recommended: (a).** The user can always run `llmwiki lint` for the full sweep. Ingest-tail is the "did this ingest leave the wiki in a sound state" check, not "is the whole wiki sound."

### Q5. Cron recipe location — `docs/automation/` or inline in README?

  - (a) `docs/automation/{macos-launchd.md,linux-systemd.md,github-actions.md}` — three files, each self-contained, README links to them.
  - (b) Inline in README under "Automation" section. One screenful of YAML and plist XML.
  - (c) Both: inline minimal copy-paste in README, full annotated recipes in `docs/automation/`.

  **Recommended: (a).** The README is already 700+ lines; inlining three platform-specific recipes pushes it past readability. v0.4's MCP recipes used the same shape (linked-from-README, lived under `docs/`) and that was fine.

### Q6. `llmwiki maintain` flag set — `--all` vs individual flags?

  - (a) Bare `maintain` runs everything sensible; `--refresh-stale`, `--lint`, `--promote-pending`, `--update-existing` as composable flags; `--no-X` flags to opt out of any default.
  - (b) `--all` is the default + opt-in flag; per-flag opt-in via `--lint`, `--refresh-stale`, etc.; passing `--lint --no-refresh-stale` is allowed.
  - (c) Single `--phase=<name>` argument; `maintain --phase=lint`, `maintain --phase=stale`.

  **Recommended: (a).** Matches `init`'s flag-combination semantics from v0.5 (where `--provider=X` is a flag, not `--phase=provider`). Bare invocation is the documented "press the button" command; composability is for cron config.

### Q7. Should v0.8 add `--auto-approve` to `ingest` itself, or just to `promote`?

kytmanov's `--auto-approve` runs the entire pipeline unattended including ingest. We already auto-write validated pages on ingest; the question is whether to auto-write the *failed-validation* drafts to a quarantine directory.

  - (a) **No auto-approve on ingest.** Pages that fail validation stay dropped (current behaviour). `--auto-approve` only applies to promote.
  - (b) Auto-approve on ingest writes failed-validation drafts to `.llmwiki/quarantine/` for later human review.

  **Recommended: (a).** Quarantine creates a parallel "wiki" surface the user has to maintain (review queue, retry policy, expiry). The simplicity-loss is not worth the marginal recall improvement. Per-non-goal in §Non-goals above.

### Q8. Should the auto-promote heuristic be evaluated per-call (one heuristic eval per `ask`) or batched (every N asks, maintain runs the heuristic over all pending answers)?

  - (a) **Per-call.** Every `ask` evaluates the heuristic on the answer it just produced. If the score clears, auto-promote inline. Latency-positive: the user sees "(auto-promoted to page X)" inline.
  - (b) **Batched.** `ask` only saves the answer + score. `llmwiki maintain --promote-pending` (or the cron run) walks pending saved answers and promotes those above the floor.

  **Recommended: both.** Per-call is the v0.8 default behaviour; batched is the cron behaviour for "asks that landed when the user wasn't watching." `maintain --promote-pending` re-evaluates every saved answer's score against the current wiki (FTS distance changes as the wiki grows) and promotes the now-qualifying ones. Two paths, same heuristic, no contradiction.

### Q9 (v0.9). Where does the queue live in v0.9?

  - (a) **SQLite table `ingest_queue` inside `wiki.db`**. Consistent with `page_update_log`. One DB to back up; one migration to v6.
  - (b) Separate `queue.db`. Decouples queue from main DB schema; survives `wiki.db` corruption.

  **Recommended: (a).** Spec defers final answer to the v0.9 design pass. First cut is "same DB" because the audit-trail consistency and backup story are simpler; if v0.9's design uncovers a real corruption-isolation reason to split, we revisit.

### Q10 (v0.9). Session-end hook scope — every Claude Code session that touched the wiki, or only when the user opts in?

The Claude Code hooks doc is explicit that session hooks are user-configured (the user edits `.claude/settings.json`). We ship the binary command + a documented snippet.

  - (a) **User opts in by editing `.claude/settings.json`.** We do not auto-install the hook. Default behaviour: nothing fires.
  - (b) `llmwiki init` writes a `.claude/settings.json` snippet alongside `config.toml`. Auto-installed for new wikis.

  **Recommended: (a).** Auto-installing into another tool's config is the kind of magic that breaks trust. Document the snippet under `docs/automation/claude-code-hooks.md`; the user pastes it.

### Q11 (v0.9). Should the watcher handle URL/feed re-fetch, or only fsnotify-watch local sources?

  - (a) **fsnotify only in v0.9; URL polling in v0.9.x.** fsnotify is well-understood, low-risk; URL polling needs goroutines + per-source TTL state.
  - (b) Both in v0.9.

  **Recommended: (a) — split.** v0.9 ships local fsnotify; v0.9.x adds URL polling once the queue is proven. The cron recipe handles URL refresh in v0.8; v0.9 doesn't have to.

### Q12 (v1.0). Graph-lint thresholds — defensible defaults or calibration needed?

  - (a) **Use nashsu's defaults verbatim**: cohesion floor 0.15, degree-1 = orphan, Adamic-Adar top-decile = surprising.
  - (b) Calibrate against real wikis (run on the v0.7 cassette wikis + the user's own wiki) before publishing.

  **Recommended: (a) for v1.0 ship; (b) for v1.0.x tuning.** Ship the defaults nashsu published; surface them as `[lint.graph]` config keys; iterate on real-world feedback.

### Q13. Should `maintain` log to `log.md` (existing user-visible audit) or only to a new `auto_promote_log` SQLite table?

  - (a) **Both.** `log.md` gets a `**maintain**` line per run with summary counts; `auto_promote_log` gets one row per evaluated answer.
  - (b) Only the SQLite table; the user queries via `sqlite3` if they want details.

  **Recommended: (a).** `log.md` is the user-facing audit trail (Obsidian-readable); the SQLite table is the structured audit. Both serve different consumers; the volume is small (one summary line per cron run).

### Q14. Should the auto-promote heuristic block on the validator first or evaluate the heuristic first?

The two locks both have to clear, but order matters for cost and for log readability.

  - (a) **Heuristic first; if it fails, skip the validator.** Saves the validator's substring-match work (cheap, but non-zero) on answers that wouldn't promote anyway. Log entry: `heuristic_failed: votes=2/3`.
  - (b) Validator first; if it fails, skip the heuristic.
  - (c) Always evaluate both; log both verdicts.

  **Recommended: (c).** Logging both verdicts means a user tuning the heuristic against a corpus of past asks can see "would this answer have promoted under heuristic-vote-floor=2?" without re-running. The cost is one extra string scan per answer; trivial.

### Q15. Default frequency for the cron recipes we publish?

  - (a) **Hourly** for `--refresh-stale`, **daily** for `--lint --promote-pending --update-existing`.
  - (b) Daily for everything.
  - (c) Configurable per platform; document the recipe but don't pick a frequency.

  **Recommended: (a) with one note.** Hourly stale-refresh is OK (HEAD requests are cheap); daily lint+promote is the right cadence for "the LLM gets a chance to file pending answers once a day." The recipes ship with these defaults; users edit if they have different cadence needs.

### Q16. Should the v0.8 plan ship `make automation-recipes` to print / copy the recipes, or just rely on the user finding `docs/automation/`?

  - (a) **Just the docs**, link prominently from README's "Automation" section.
  - (b) Add `llmwiki automation print --platform=launchd|systemd|github-actions` as a built-in command that prints the recipe to stdout.

  **Recommended: (a).** A `print` subcommand is ceremony around three text files. README link is enough.

### Q17. Where do auto-promote outcomes get logged for the user?

  - (a) Inline at end of `ask` output: `(auto-promoted to page "Validator Internals")` or `(not promoted: heuristic_score 1/3)`.
  - (b) Only in `log.md` and `auto_promote_log` SQLite table; `ask` output unchanged.
  - (c) Inline AND in the audit trails.

  **Recommended: (c).** Users tuning the heuristic want inline feedback; users running cron want the audit trail. Both are cheap.

### Q18. Should `maintain --promote-pending` re-evaluate every saved answer's heuristic score every run, or only re-evaluate ones whose score crossed the floor on prior eval?

  - (a) **Re-evaluate every run.** FTS distance changes as the wiki grows; an answer that scored 2/3 on day 1 might score 3/3 on day 7 because no competing page exists.
  - (b) Re-evaluate only those who scored above (some lower) floor previously, marking the rest as "permanently below" until manually re-checked.

  **Recommended: (a).** The cost is one heuristic eval per saved answer per cron run; the saved-answer count is small (target wiki: ≤ 100 pending answers). Re-evaluation lets the heuristic adapt to wiki growth without surprising the user.

### Q19. v0.8 minimum schema migration — is one needed?

  - (a) **No new migration.** Auto-promote outcomes log to a new table `auto_promote_log` via a v6 additive migration (or to `page_update_log` if the shape fits). Re-using `page_update_log` is awkward (it's keyed on `page_id`; failed auto-promotes have no page yet). New table is cleaner.
  - (b) New table `auto_promote_log` (id, saved_answer_id, evaluated_at, votes, voted_signals JSON, validator_outcome, promoted_page_id NULL, reason). One additive migration to v6.

  **Recommended: (b).** New table; v6 additive migration. Pattern matches v0.6's `page_update_log` exactly.

### Q20. Should the cron recipes use `LLMWIKI_DIR` env var or `cd /path/to/wiki && llmwiki maintain`?

  - (a) **`LLMWIKI_DIR=/path/to/wiki llmwiki maintain`.** Already supported (v0.5 added the env var for MCP). One-line invocation.
  - (b) `cd /path/to/wiki && llmwiki maintain`. Shell-traditional; `LLMWIKI_DIR` is a curiosity.

  **Recommended: (a).** Matches the v0.5 MCP recipe pattern and the env-var is already in production.

## Implementation order (high-level)

Phase boundaries refine in each cycle's own plan-pass. High-level order:

1. **v0.8** — composition-of-existing-primitives (5 features, no new long-running process). Plan: `2026-05-05-automation-v0.8.md`.
2. **v0.9** — file watcher + persistent queue + Claude Code session hook. Plan: TBD (its own design pass when v0.8 ships).
3. **v1.0** — graph lint dimensions (orphans, missing cross-refs, data gaps, Louvain, Adamic-Adar) + `lint --fix`. Plan: TBD.

Each cycle is a single shippable release. Each preserves every invariant in §Hard invariants. Each writes through the bundled validator. Each respects the user's editorial control over the schema.

## Verification (at the north-star level)

The end-state we are designing toward:

```bash
# A wiki that maintains itself in the gaps between user invocations.

# v0.8: cron does daily maintenance.
$ crontab -l | grep llmwiki
0 3 * * *  LLMWIKI_DIR=/path/to/wiki llmwiki maintain >> /var/log/llmwiki.log 2>&1

# v0.9: watcher + session-end hook keep the wiki current as the user works.
$ launchctl list | grep llmwiki
com.user.llmwiki.watch  -  -

# v1.0: lint surfaces graph-level findings nobody else surfaces.
$ llmwiki lint
=== Staleness Check ===
  All sources up to date.

=== Contradiction Check ===
  no contradictions found.

=== Graph Check (v1.0) ===
  3 orphan page(s):
    - Some Old Page (no inbound links; consider linking from [[Hub]])
    - ...
  2 missing cross-reference(s):
    - "Trust Validator" mentioned in [[Architecture]] line 47 but not linked
    - ...
  1 low-cohesion cluster:
    - {Page A, Page B, Page C} (cohesion 0.11; consider whether they belong together)

  to auto-fix the safe ones: llmwiki lint --fix
```

The user types nothing for stretches of weeks. The wiki's pages stay current with their sources. The validator's contract — every quote substring-matches its source — holds across every automated write. **That is the gist's vision instantiated**, with our trust property bolted on as the invariant nobody else has.
