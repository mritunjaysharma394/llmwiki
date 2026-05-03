# Sub-project 4 — Launch Surface

**Status:** design approved, awaiting implementation plan
**Date:** 2026-05-03
**Author:** Mritunjay Sharma (with Claude)

## Context

Sub-projects 1 (trust) and 3 (real-world ingestion) shipped. The CLI now ingests PDFs, URLs (Readability-cleaned), and real repos with per-file evidence and incremental re-ingest. Sub-project 2 (web UI) is **deliberately skipped** — `llmwiki` is, and will remain at v1.0, a CLI tool. Anything that assumed a hosted product or browser surface (Twitter/Slack/Gmail OAuth flows, hosted ask UI, history browser) is reconsidered through the CLI lens in this spec.

What's left between today's working binary and a v1.0 release someone can `go install` and recommend:

- The repo has no LICENSE, no CHANGELOG, no version flag (`userAgentVersion = "0.1"` is hardcoded in `internal/ingest/url.go`), no release artifacts, no `go install` story (the module path works, but it's never been verified on a fresh machine), and a README that's an internal scratchpad rather than a launch page.
- The CI workflow runs `go test ./...` on push but never re-records cassettes, so upstream Anthropic API drift will silently rot replay tests until somebody runs `LLMWIKI_RECORD=1` by hand. This was explicitly punted from sub-project 1.
- Sub-project 3 explicitly punted re-chunking on file-boundary changes, plus "additional connectors" (Twitter/X, YouTube, Notion, Slack, Gmail). The connectors list was assembled assuming sub-project 2 would land first; without a UI, most of them don't make sense for a CLI.
- Error UX is uneven — `loadConfig`/`init` have hand-tuned multi-line messages, but most other failure paths surface raw `fmt.Errorf` strings with no remediation hint. A first-time user hitting a stale-API or bad-key error mid-ingest sees Go error-wrapping noise.

This sub-project is about turning a working binary into a shipped tool: licensed, versioned, installable, releasable, and readable. It is **not** an open-ended polish bucket — most "could be better" things stay punted.

## Goals

1. **Versioned, releasable binary.** `llmwiki version` prints a real version. `User-Agent` carries it. `go install` works from a tagged release. GoReleaser publishes Linux/macOS/Windows binaries on tag push.
2. **A README that earns trust in 60 seconds.** Clear what-it-is, install instructions for the three real install paths (`go install`, `brew`-no-tap-but-binary-download, `make install`), an asciinema/screencap of one ingest+ask round trip, the trust property stated up front, and links to the design specs.
3. **A LICENSE and a CHANGELOG.** MIT (or Apache-2.0; see open question) at the repo root. CHANGELOG follows Keep-a-Changelog, populated retroactively for sub-projects 1, 3, 4.
4. **One coherent ingestion expansion: feed-shaped sources.** RSS/Atom feeds + sitemap.xml crawling, layered on top of sub-project 3's URL+HTML pipeline. This covers the "I want to keep my wiki current with a blog/changelog/news source" use case that the deferred Twitter/Slack/Gmail/Notion list was gesturing at, without per-source OAuth.
5. **Re-chunking on file-boundary changes.** Honor the punted item from sub-project 3: when an existing file grows enough to spill into a new chunk, neighbour chunks that share its content get re-processed too, so quotes don't end up orphaned by the chunker's bin-packing decisions.
6. **Nightly cassette refresh in CI.** Scheduled GitHub Actions job runs `LLMWIKI_RECORD=1 go test ./...` against a real `ANTHROPIC_API_KEY` secret on a cron, opens a PR with the cassette diff if anything changed. We see API drift the morning it happens, not the day a contributor's PR fails for unrelated reasons.
7. **Polished error surface.** Every operator-facing error has a one-line cause + one-line remediation. No bare wrapped errors leaking from ingest/ask/lint.
8. **Smoke-tested install path.** A `make smoke` target (and CI step) that does the README quickstart end-to-end against a tiny synthetic source, using a recorded cassette so it runs without an API key.

## Non-goals (deferred / dropped)

- **Twitter/X, Slack, Gmail, Notion connectors.** These were on the sub-project 4 list but were scoped under the assumption a hosted UI would handle OAuth. For a CLI, each is a substantial per-source authentication and rate-limit project for limited payoff (the user can already paste a Notion-export folder, save a Slack archive locally, etc.). Recommendation: **drop**. RSS/Atom + sitemap covers most "ambient web content" use cases with one shared codepath. If a specific connector becomes load-bearing for the user's workflow post-launch, add it then.
- **YouTube transcript ingestion.** Specifically *not* dropped on principle — but it's a one-source niche feature, and the right time to add it is when the user actually has a backlog of YouTube content they want indexed. Out of v1.0 scope.
- **Hosted web UI / `llmwiki serve`.** Sub-project 2 is skipped, period.
- **Auto-update / self-update.** GoReleaser binaries; users can re-run `go install` or download the new tarball. Self-updaters are a security and trust footgun.
- **OCR for scanned PDFs.** Sub-project 3 punted this contingent on Tesseract bindings stabilizing. They haven't. Still skipped.
- **Homebrew tap, Scoop bucket, official Linux packages.** Real binary downloads from GitHub Releases cover the same need at one tenth the maintenance. Reconsider post-launch if there's user demand.
- **Telemetry / phone-home.** Not in v1.0. May never happen. The `User-Agent: llmwiki/<version>` already gives content publishers a way to identify our requests.
- **Sub-project 1's `llmwiki history` browse command.** Was deferred to sub-project 2; without a UI, listing `.llmwiki/answers/` is `ls`. Not a CLI command.
- **Configurable answer retention / pruning.** The user can `rm` files. Not a launch concern.

## What users see

A reader who has never heard of `llmwiki` lands on the README. They install, init, ingest, and ask within five minutes — without an Anthropic key if they have Ollama running, or with a clear key-acquisition path if not.

```bash
# Install
go install github.com/mritunjaysharma394/llmwiki@latest
# OR: download the prebuilt binary
curl -fsSL https://github.com/mritunjaysharma394/llmwiki/releases/latest/download/llmwiki-darwin-arm64.tar.gz \
  | tar -xz -C /usr/local/bin

llmwiki version
# llmwiki 1.0.0 (commit abc1234, built 2026-05-10, go1.26.2)

# First-run
export ANTHROPIC_API_KEY=sk-ant-...
mkdir my-wiki && cd my-wiki
llmwiki init
llmwiki ingest https://github.com/golang/example
llmwiki ask "what does the gotypes example do?"
```

The CLI itself gains:

- `llmwiki version` — prints semver, commit SHA, build date, Go version. Read from `-ldflags` set by GoReleaser; falls back to `(devel)` for `go run` / unreleased builds.
- `llmwiki --version` — same output, root flag for tooling that expects it.
- `llmwiki ingest --feed <url>` — explicit RSS/Atom dispatch; without the flag, `ingest <url>` content-type-sniffs and auto-routes feeds the same way it already routes PDFs. Feed entries become individual `SourceFile`s under one `sources` row; the feed's URL is the `URI`, each entry's permalink is the `RelativePath`.
- `llmwiki ingest --sitemap <url>` — explicit sitemap-crawler dispatch. Same shape: each crawled page is a `SourceFile`. Polite defaults (1 req/sec, 50-page cap, override via flag).
- Errors look like:
  ```
  Error: ingest failed
    cause: HTTP 503 from https://example.com/feed.xml
    try:   re-run with --max-retries=3, or check the feed in a browser
  ```
  not:
  ```
  Error: reading source: fetching url: Get "https://...": 503
  ```

The README, top-to-bottom:

1. One-paragraph what-it-is + the trust property in plain English.
2. A 30-second asciicast (committed as a `.cast` file, embedded via the asciinema GH-pages player or just linked).
3. Install: `go install`, prebuilt binaries, `make install` from source.
4. Quickstart (3 commands).
5. Common workflows: ingest a repo, ingest a folder of PDFs, subscribe to a feed.
6. Configuration table (`.llmwiki/config.toml` keys, defaults, env vars).
7. Trust model: link to sub-project 1 spec, one paragraph summary.
8. Privacy: data flows. Anthropic gets sources. Ollama keeps everything local. `.llmwiki/` is local and ignored by default.
9. Architecture: ASCII diagram, link to specs.
10. Contributing: cassette workflow, recording protocol.
11. License + acknowledgements (Karpathy's wiki post, the dependency authors).

## Architecture overview

The load-bearing pieces are mostly housekeeping; the only architectural addition is the **feed/sitemap dispatcher** in `internal/ingest/` and the **chunk-stability layer** that addresses the punted re-chunking concern. Both fit cleanly into sub-project 3's `[]SourceFile` pipeline without disturbing the per-file evidence invariant.

### Version injection

Single source of truth: `internal/version/version.go` exposes `Version`, `Commit`, `BuildDate` package-level vars. GoReleaser injects them via `-ldflags`. `userAgentVersion` in `internal/ingest/url.go` becomes `version.Version`. The `version` cobra command lives in `cmd/version.go` and prints them in one line. For `go run` / non-release builds, all three default to `(devel)` and `version.Format()` returns `llmwiki (devel)`.

### Feed and sitemap ingestion

`internal/ingest/feed.go` (new). One function: `FetchFeedFiles(url string, opts URLOptions) ([]SourceFile, error)`.

Library: **`github.com/mmcdole/gofeed`** (MIT, ~3k stars, last release 2024). It handles RSS 1.0/2.0, Atom, JSON Feed in one API. We don't need a sitemap library — sitemap.xml is a 30-line `encoding/xml` decode in `internal/ingest/sitemap.go`.

Flow:

1. URL fetcher (sub-project 3) downloads the feed body. Content-type `application/rss+xml`, `application/atom+xml`, `application/xml`+`<rss>`/`<feed>` root, or `application/json`+`feed` shape → dispatch to `FetchFeedFiles`. Sitemap content (`/sitemap.xml`, root `<urlset>` or `<sitemapindex>`) → dispatch to `FetchSitemapFiles`.
2. Parse feed. For each entry, extract: `Link`, `Title`, `PublishedAt`, content (`Content` or `Description`).
3. For each entry whose `Link` looks like an HTML URL, run sub-project 3's HTML pipeline (`go-readability` + `html-to-markdown`) over the entry's inline content if present, else fetch the linked page. Store as `SourceFile{RelativePath: entry.Link, Content: markdown}`.
4. Polite rate-limit: a token bucket at 1 req/sec by default, configurable via `[ingest] feed_request_per_second`.
5. Cap entries per fetch at `[ingest] feed_max_entries` (default 50). Old entries beyond the cap are skipped silently — incremental re-ingest will pick them up next run if they're still in the feed; once they fall off the feed, they're gone, which is correct behaviour for "subscribe to a feed".

Sitemap flow is the same after dispatch: each URL in the sitemap becomes a `SourceFile` via the existing URL pipeline. Sitemap-of-sitemaps recursion is supported one level deep; deeper is a configuration error.

The `Source.URI` is the feed/sitemap URL (so re-ingest hits the same row). Per-file dedup via `content_hash` already exists from sub-project 3 and gives us correct incremental behaviour for free: an unchanged feed with one new post re-fetches one entry's HTML, the rest are dedup'd.

### Re-chunking on file-boundary changes

Sub-project 3 punted this with the rationale "per-file hashing handles content drift; chunk reshuffling is a sub-project 4 concern." Concretely the gap: when a file grows from "fits in chunk N" to "spills out of chunk N", a re-ingest only processes the changed file, but the LLM call for that file may be packed with *different neighbour files* than the original ingest. Existing pages whose evidence cited "fileA + fileB packed together" may have their cross-file synthesis silently invalidated.

Fix is bookkeeping, not redesign:

- `chunks` table (new): `id`, `source_id`, `chunk_hash`, `file_paths` (JSON array of `relative_path` strings), `byte_range_start`, `byte_range_end` per included file, `created_at`. Records the bin-pack decision the chunker made.
- After per-file partition (`partitionByFileHash` in `cmd/ingest.go`), but before chunking: for each `changed` file, look up the prior chunks that contained it. Mark every co-resident file in those chunks as **chunk-dirty** even if their hash didn't change — they need to be re-included in the new chunk pack so the LLM sees the same neighbours-or-better.
- Chunker emits new `chunks` rows after packing. Old rows for affected files are deleted in the same transaction.
- New CLI flag `--no-rechunk` opts out for users who want the cheaper per-file behaviour and accept the synthesis-drift risk.

This is purely additive; it doesn't change the per-file evidence invariant. It just makes incremental re-ingest match what a from-scratch re-ingest would produce, for the "this neighbour grew" edge case.

### Error formatting layer

`internal/cliutil/errors.go` (new). One type:

```go
type UserError struct {
    Cause       string  // one line, no period
    Remediation string  // one line, imperative ("re-run with...", "set ...", "check ...")
    Wrapped     error   // for unwrapping/testing
}
```

Every command's `RunE` wraps non-trivial failures with `cliutil.UserError{...}`. `Execute()` in `cmd/root.go` checks `errors.As(err, &UserError)` and formats:

```
Error: <Cause>
  cause: <wrapped error string, indented>
  try:   <Remediation>
```

For non-`UserError` paths (programmer bugs, panics surfaced as errors), output is the existing `Error: <err>`. The retrofit happens once across `cmd/{ingest,ask,lint,init,status,version}.go` with one-liner remediations per known failure mode (missing API key, network 4xx/5xx, FTS unavailable, schema migration failure, etc.).

### Smoke test as a build artifact

`make smoke` runs:

```bash
go build -o ./llmwiki .
TMP=$(mktemp -d)
( cd $TMP && \
  ../llmwiki init --provider=ollama && \  # no API key needed for replay
  LLMWIKI_CASSETTE=smoke ../llmwiki ingest ./testdata/smoke-source.md && \
  ../llmwiki ask "what is the smoke source about?" --no-save && \
  ../llmwiki status )
rm -rf $TMP
```

The `LLMWIKI_CASSETTE` env var (new, ~10 lines in `cmd/root.go`) wraps the LLM client in a cassette in replay mode using a fixture under `internal/llm/testdata/cassettes/smoke__*.json`. CI runs `make smoke` after `go test ./...`; failure means the README quickstart is broken before a single user touches it.

## Schema changes

One new table for chunk bookkeeping. Migration to `user_version 3`.

```sql
CREATE TABLE chunks (
  id INTEGER PRIMARY KEY,
  source_id INTEGER NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
  chunk_hash TEXT NOT NULL,             -- SHA256 of the chunk text passed to LLM
  file_paths TEXT NOT NULL,             -- JSON array of relative_path strings
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_chunks_source ON chunks(source_id);

PRAGMA user_version = 3;
```

That's it. The "byte range per file in chunk" idea was scoped out — a chunk knowing *which files* it included is enough to mark co-residents chunk-dirty, and we don't need finer granularity until evidence proves it.

Pre-v3 wikis migrate silently. Pre-existing pages have no `chunks` rows backing them; their evidence is still attached at the file level (sub-project 3 invariant). The first re-ingest after upgrade populates the new table.

## CLI surface changes

### New commands

- `llmwiki version` — semver, commit, build date, Go version. `llmwiki --version` is the same output, just registered as a root flag.

### New flags on `ingest`

- `--feed` — force feed-parser dispatch even if content-type sniffing would have routed elsewhere.
- `--sitemap` — force sitemap dispatch.
- `--max-pages N` — cap on feed/sitemap fetch breadth (default 50).
- `--no-rechunk` — opt out of co-resident re-chunking; matches sub-project 3 behaviour.

### Existing flags unchanged

`--max-file-bytes`, `--include`, `--exclude`, `--no-gitignore`, `--force` keep their sub-project 3 semantics.

### Config additions (`[ingest]`)

```toml
[ingest]
# Polite rate limit for feed/sitemap crawls.
feed_request_per_second = 1.0

# Max entries to ingest per feed fetch.
feed_max_entries = 50

# Max URLs to crawl from a sitemap.
sitemap_max_pages = 200
```

Pre-v4 configs missing these get the defaults silently via `applyIngestDefaults` (extending the existing pattern in `cmd/root.go`).

## Release engineering

### GoReleaser (`.goreleaser.yml`)

Targets: `linux/amd64`, `linux/arm64`, `darwin/amd64`, `darwin/arm64`, `windows/amd64`. Archives are `tar.gz` for unix, `zip` for windows. Checksums file alongside. No signing in v1.0 (cosign / sigstore is a sub-project 5 problem if it ever happens).

`-ldflags` injection:

```
-X github.com/mritunjaysharma394/llmwiki/internal/version.Version={{.Version}}
-X github.com/mritunjaysharma394/llmwiki/internal/version.Commit={{.ShortCommit}}
-X github.com/mritunjaysharma394/llmwiki/internal/version.BuildDate={{.Date}}
```

GitHub Release notes are auto-populated from the CHANGELOG entry for the tag.

### CHANGELOG

`CHANGELOG.md` at repo root, Keep-a-Changelog format. Retroactively populated:

- **0.1.0** — sub-project 1 (trust the output): evidence validation, FTS5 over evidence, streaming ask, auto-archive.
- **0.2.0** — sub-project 3 (real-world ingestion): PDFs, URL article extraction, repo walker, per-file dedup.
- **1.0.0** — sub-project 4 (this one): feed/sitemap, re-chunking, version flag, GoReleaser, polished errors, license, README.

The 0.1.0 / 0.2.0 entries are written in past tense; we're not actually tagging old commits, just recording what shipped between them. The 1.0.0 entry is the launch.

### LICENSE

MIT (open question below). One file at repo root, copy of the standard text with copyright line "Copyright (c) 2026 Mritunjay Sharma".

### Nightly cassette refresh (`.github/workflows/cassette-refresh.yml`)

```yaml
on:
  schedule:
    - cron: "17 6 * * *"   # 06:17 UTC daily
  workflow_dispatch:
jobs:
  refresh:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
      - env: { ANTHROPIC_API_KEY: ${{ secrets.ANTHROPIC_API_KEY }} }
        run: LLMWIKI_RECORD=1 go test ./...
      - uses: peter-evans/create-pull-request@v6
        with:
          title: "chore: nightly cassette refresh"
          branch: cassette-refresh-${{ github.run_id }}
          commit-message: "chore: nightly cassette refresh"
```

A clean run produces no diff, no PR. Any drift opens a PR with the cassette delta — a human reviews, merges or rejects, no production impact either way. Total recurring cost: ~5 cents/day of Haiku tokens.

## Migration / backward compat

- `db.Open` runs the v3 migration: `CREATE TABLE chunks`. Idempotent. No `ALTER` on existing tables.
- Existing wikis without `chunks` rows: re-chunking is a no-op until the first ingest writes the first row. By design — there's no historical chunk data to recover.
- Existing config files (pre-v4): missing feed/sitemap keys get defaults from `applyIngestDefaults`. No `init` re-run required.
- `userAgentVersion = "0.1"` becomes `version.Version` — sites that filter on the literal `"llmwiki/0.1"` substring see `"llmwiki/1.0.0"` after the release. Acceptable (we don't know of any such site, and substring filters that strict on dev-tier UAs are anyway rare).
- Pre-existing answers in `.llmwiki/answers/` are untouched. The auto-archive format from sub-project 1 stays.

## Open questions

These need user input before plan-pass:

1. **MIT vs Apache-2.0 vs BSD-3 for LICENSE.** MIT is the path of least resistance and matches the dependency profile (most deps are MIT-licensed). Apache-2.0 adds an explicit patent grant, which matters more for libraries than CLIs. Recommendation: **MIT**. Confirm.
2. **Tag name for the launch.** `v1.0.0`? Or `v0.3.0` / `v1.0.0-rc.1` first, with v1.0.0 reserved for "I've used it for a month and nothing exploded"? Recommendation: **v1.0.0-rc.1 first**, promote to v1.0.0 after a stability window. Confirm.
3. **Module path stability.** `github.com/mritunjaysharma394/llmwiki` is the current path. Once we tag v1.0, we own that path forever for `go install` users. Move it to a personal-domain or org alias before launch, or commit to the username path? Recommendation: **commit to the username path**, it's already in `go.mod` and the GitHub URL.
4. **Chainguard email vs gmail in `git config`.** Local repo identity is `mritunjaysharma394@gmail.com`; the user's spec-context email is `mritunjay.sharma@chainguard.dev`. Out of scope to touch in this spec, but flag for the user: which identity should the launch commit and the LICENSE copyright line use?
5. **Asciinema vs static screenshot in README.** Asciinema is the right format (copyable, scrubbable, lightweight) but adds a new asset workflow. A static `screenshot.png` from `glamour`-rendered output is one commit. Recommendation: **screenshot for v1.0**, asciicast post-launch if there's appetite.
6. **`brew install` story.** A personal Homebrew tap (`brew install mritunjaysharma394/tap/llmwiki`) is two extra files in a separate repo and zero ongoing maintenance via GoReleaser. Worth doing now or post-launch? Recommendation: **post-launch** — non-blocking and we'll know better if it matters.

## Risks

- **Nightly cassette refresh leaks API spend.** Mitigation: the cron job runs only the cassette-driven integration tests (4 cases × ~2K tokens each ≈ pennies/day). If it ever spikes, kill the workflow and re-record manually.
- **Feed/sitemap fetcher accidentally crawl-bombs a site.** Mitigation: 1-req/sec default, hard cap on entries, `User-Agent: llmwiki/<version>` so site owners can identify and block. We're not adding `robots.txt` enforcement (sub-project 3 stance, unchanged).
- **Re-chunking write-amplifies on large repos.** A 200-file repo where one file grows 1 byte across a chunk boundary could mark its 4 co-residents chunk-dirty, causing a 5-file LLM re-call instead of a 1-file one. Acceptable cost for synthesis stability; `--no-rechunk` is the escape hatch.
- **GoReleaser config breaks on tag push, no release artifact.** Mitigation: dry-run via `goreleaser release --snapshot --clean` in CI on every PR. Catch breakage at PR time, not at tag time.
- **`go install` works on the maintainer's machine but not a fresh one.** Mitigation: the smoke-test step in CI runs in a clean GHA runner from a fresh checkout — that *is* the fresh-machine test. Plus the manual verification checklist below includes a `go install` from a clean `$GOPATH`.
- **README promises a feature the binary doesn't quite have.** Mitigation: README writing happens last in the plan, after every other task is checked in and verified. Specifically, no README claim ships unverified.

## Test strategy

### Pure unit tests (no LLM)

- `internal/version` — `Format()` with all-set, all-`(devel)`, partial. Tiny.
- `internal/ingest/feed.go` — fixtures in `internal/ingest/testdata/feeds/`: a 5-entry RSS 2.0, an Atom 1.0, a JSON Feed, a malformed feed (asserts error). Uses `httptest.Server` for the link-resolution fetches.
- `internal/ingest/sitemap.go` — fixtures: flat sitemap, sitemap-of-sitemaps (one level), >`max_pages` cap behaviour, malformed XML.
- `internal/ingest/chunk_test.go` (extended) — re-chunking partition logic: given a prior `chunks` table state and a `partitionByFileHash` result, assert the correct co-resident files get marked chunk-dirty. Pure function, no DB.
- `internal/cliutil/errors_test.go` — `UserError` formatting, `errors.As` round-trip.
- `cmd/version_test.go` — version command output shape.
- `db` migration: v0→v3, v1→v3, v2→v3 all idempotent; `chunks` CRUD.

### Integration / cassette tests

- `TestIngestFeed` (new cassette) — synthetic 3-entry feed served by `httptest.Server`, expects 3 `SourceFile`s, evidence anchored to entry permalinks.
- `TestIngestSitemap` (new cassette) — 5-URL sitemap, expects polite rate-limit observed (test asserts `time.Since(start) >= 4 * time.Second` minus jitter), evidence anchored.
- Existing 6 cassette tests (sub-projects 1+3) continue to pass without re-recording.

### Release smoke

- `make smoke` runs the README quickstart end-to-end against fixture data, in CI on every PR.
- Manual `go install github.com/mritunjaysharma394/llmwiki@<rc-tag>` on a fresh `$GOPATH` once before tagging the final v1.0.0.

### Nightly drift

- The cassette-refresh workflow itself is the test. A green run with no diff is the signal. A PR with non-empty diff is the alert.

## Implementation order

Plan-pass refines. Rough sequence:

1. **`internal/version` package** + version cobra command + `User-Agent` substitution. Pure refactor; nothing else depends on it.
2. **`internal/cliutil/errors.go`** — `UserError` type and `Execute()` wiring. Retrofit existing commands to use it (one PR per command keeps the diff readable, but for plan-pass it's one task).
3. **`chunks` table migration** (db v3) — additive, no behavior change yet.
4. **Re-chunking partition logic** — pure function in `internal/ingest/chunk.go`, plus the chunker writes new `chunks` rows. Wire `cmd/ingest.go` to mark co-residents dirty.
5. **`internal/ingest/feed.go`** + `internal/ingest/sitemap.go` — fetcher, parser, content-type dispatch from `url.go`. New CLI flags + config keys.
6. **`make smoke`** target + smoke cassette + CI step.
7. **GoReleaser config** + dry-run CI step.
8. **CHANGELOG** retroactive entries (0.1.0, 0.2.0) + 1.0.0 stub.
9. **LICENSE** at repo root.
10. **Nightly cassette-refresh workflow.**
11. **README rewrite** — last, verifies everything claimed.
12. **Tag v1.0.0-rc.1**, run manual verification (next section), promote to v1.0.0 after a stability window.

## Verification

```bash
# Version
./llmwiki version
# Expect: "llmwiki 1.0.0-rc.1 (commit abc1234, built 2026-05-NN, go1.26.2)"

./llmwiki --version
# Expect: same output.

curl --user-agent-grep equivalent
./llmwiki ingest https://example.com/   # check server logs for User-Agent: llmwiki/1.0.0-rc.1

# Feed ingestion
./llmwiki ingest --feed https://blog.golang.org/feed.atom
# Expect: per-entry SourceFiles, evidence anchored to entry URLs, polite rate.

# Sitemap ingestion
./llmwiki ingest --sitemap https://example.org/sitemap.xml
# Expect: bounded crawl honoring sitemap_max_pages, evidence anchored.

# Re-chunking
echo "extra content" >> ./internal/ingest/local.go    # grow a file across a chunk boundary
./llmwiki ingest ./internal/
# Expect: not just local.go re-processed; co-resident files in the same chunk
# are also re-LLM'd. Verify via "Packing into N chunks" log line.

./llmwiki ingest --no-rechunk ./internal/
# Expect: only local.go re-processed (sub-project 3 behaviour preserved).

# Polished errors
./llmwiki ingest https://does-not-exist.invalid/feed
# Expect: "Error: ingest failed / cause: ... / try: check the URL is reachable"

unset ANTHROPIC_API_KEY
./llmwiki ingest ./README.md
# Expect: structured 3-line error pointing at console.anthropic.com.

# Smoke test
make smoke
# Expect: green; runs init+ingest+ask+status against synthetic source under /tmp.

# go install
GOBIN=$(mktemp -d) go install github.com/mritunjaysharma394/llmwiki@v1.0.0-rc.1
$GOBIN/llmwiki version
# Expect: matching version, commit, build date.

# Release artifacts
goreleaser release --snapshot --clean
ls dist/
# Expect: linux-amd64/arm64, darwin-amd64/arm64, windows-amd64 archives + checksums.

# Nightly drift
gh workflow run cassette-refresh.yml
# Expect: clean run with no PR opened (or one PR with explainable diff).

# Tests
go test ./...
# Expect: green in replay mode, smoke fixture present, new feed/sitemap tests pass.
```
