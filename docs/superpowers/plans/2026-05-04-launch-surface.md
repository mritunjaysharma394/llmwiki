# Launch Surface — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn the working `llmwiki` binary into a shipped tool: versioned, licensed, releasable from a tag, installable via `go install`, with feed/sitemap ingestion, co-resident re-chunking, polished errors, a smoke-test, nightly cassette drift detection, and a README that earns trust in 60 seconds.

**Architecture:** A new `internal/version` package as the single source of truth for `Version`, `Commit`, `BuildDate`; a `cliutil.UserError` wrapper rendered by `cmd/root.go`'s `Execute()` for cause+remediation messages; a `chunks` table (db v3) plus a co-resident dirtying pass in `cmd/ingest.go` so neighbour files re-pack together; `internal/ingest/feed.go` (gofeed) and `internal/ingest/sitemap.go` (encoding/xml) layered on top of sub-project 3's URL+`[]SourceFile` pipeline with content-type dispatch from `url.go`; a GoReleaser config injecting version vars via `-ldflags`; a nightly cassette-refresh GitHub workflow; a `make smoke` end-to-end target backed by a recorded cassette; an Apache-2.0 LICENSE; a Keep-a-Changelog `CHANGELOG.md`; a rewritten README pointing at a static screenshot.

**Tech Stack:** Go 1.26, plus one new direct dep — `github.com/mmcdole/gofeed` (MIT, RSS/Atom/JSON Feed). Sitemap parsing uses stdlib `encoding/xml`. Release tooling: GoReleaser v2 (no Go dep — invoked from CI). No new runtime deps beyond gofeed.

**Spec:** [`docs/superpowers/specs/2026-05-03-launch-surface-design.md`](../specs/2026-05-03-launch-surface-design.md)

**Resolved open questions:** Apache-2.0 (with `Copyright 2026 Mritunjay Sharma`). First tag is `v1.0.0-rc.1`; real `v1.0.0` is post-launch follow-up. Module path stays `github.com/mritunjaysharma394/llmwiki`. Personal git identity (`mritunjaysharma394@gmail.com`) is already configured locally; this plan does not touch git config and uses only that identity in any committed file. README demo is a static GIF/screenshot committed under `docs/assets/`; the asset itself is a manual user step (placeholder TODO in the plan). Homebrew tap is out of scope.

---

## File Structure

| File | Purpose | Action |
|---|---|---|
| `internal/version/version.go` | `Version`, `Commit`, `BuildDate` vars; `Format()` helper | Create |
| `internal/version/version_test.go` | `Format()` tests for set/(devel)/partial | Create |
| `cmd/version.go` | `llmwiki version` cobra command | Create |
| `cmd/version_test.go` | Output shape test | Create |
| `cmd/root.go` | Register `version` cmd; `--version` root flag; render `UserError` via `Execute()` | Modify |
| `internal/ingest/url.go` | Replace `userAgentVersion = "0.1"` constant with `version.Version` | Modify |
| `internal/cliutil/errors.go` | `UserError` type + formatting | Create |
| `internal/cliutil/errors_test.go` | Wrapping/unwrapping/format tests | Create |
| `cmd/ingest.go` | Wrap network/parse failures in `UserError`; thread `--no-rechunk`; co-resident dirtying | Modify |
| `cmd/ask.go` | Wrap missing-key/missing-FTS errors in `UserError` | Modify |
| `cmd/init.go` | Wrap key-missing error in `UserError` | Modify |
| `cmd/lint.go` | Wrap typical failures in `UserError` | Modify |
| `internal/db/db.go` | v3 migration: `chunks` table | Modify |
| `internal/db/db_test.go` | v3 migration tests, `chunks` CRUD, v2→v3 idempotency | Modify |
| `internal/db/queries.go` | `Chunk` struct, `InsertChunks`, `GetChunksForFile`, `DeleteChunksForSource` | Modify |
| `internal/ingest/chunk.go` | `MarkCoResidentDirty` pure function | Modify |
| `internal/ingest/chunk_test.go` | Co-resident dirtying tests | Modify |
| `internal/ingest/feed.go` | `FetchFeedFiles` via gofeed + URL pipeline reuse | Create |
| `internal/ingest/feed_test.go` | RSS 2.0 / Atom / JSON Feed / malformed fixtures | Create |
| `internal/ingest/sitemap.go` | `FetchSitemapFiles` via `encoding/xml` | Create |
| `internal/ingest/sitemap_test.go` | Flat / sitemap-of-sitemaps / malformed / cap | Create |
| `internal/ingest/testdata/feeds/` | RSS, Atom, JSON, malformed fixture files | Create |
| `internal/ingest/testdata/sitemaps/` | Flat, nested, malformed fixture files | Create |
| `internal/ingest/url.go` | Content-type sniffing dispatches to feed/sitemap | Modify |
| `cmd/ingest.go` | `--feed`, `--sitemap`, `--max-pages`, `--no-rechunk` flags | Modify |
| `cmd/root.go` | `[ingest] feed_request_per_second`, `feed_max_entries`, `sitemap_max_pages` defaults | Modify |
| `cmd/init.go` | New keys in default config templates | Modify |
| `Makefile` | `smoke` target | Modify |
| `cmd/smoke_test.go` | Cassette-backed end-to-end smoke unit-test (driving `make smoke`'s code path) | Create |
| `internal/llm/testdata/cassettes/smoke__*.json` | Smoke cassette | Create |
| `internal/ingest/testdata/smoke-source.md` | Tiny synthetic source for smoke | Create |
| `.goreleaser.yml` | Build matrix + `-ldflags` injection | Create |
| `.github/workflows/test.yml` | Add `goreleaser release --snapshot --clean` dry-run + `make smoke` | Modify |
| `.github/workflows/cassette-refresh.yml` | Nightly `LLMWIKI_RECORD=1` cron + auto-PR | Create |
| `LICENSE` | Apache-2.0 full text + `Copyright 2026 Mritunjay Sharma` | Create |
| `CHANGELOG.md` | Keep-a-Changelog with retroactive 0.1.0, 0.2.0 + 1.0.0-rc.1 entry | Create |
| `README.md` | Full rewrite per spec (install, quickstart, workflows, trust, privacy, arch) | Modify |
| `docs/assets/.gitkeep` | Placeholder for the (user-supplied) demo GIF | Create |
| `go.mod` / `go.sum` | Add `github.com/mmcdole/gofeed` | Modify |

**Total:** 23 tasks across 7 phases (A–G). Each task ends with a single commit; the working tree is green at every commit boundary.

---

## Phase summaries

Each phase below is self-contained: it does not depend on later-phase exports, and its last task leaves the tree compiling and `go test ./...` green so a fresh subagent can pick up the next phase from a clean checkout.

- **Phase A — Versioning core (Tasks 1–3).** Create `internal/version` package, the `version` cobra command, and the `--version` root flag. Replace the hardcoded `userAgentVersion` constant. Exports: `version.Version`, `version.Commit`, `version.BuildDate`, `version.Format()`. Risk: SDK wiring of the cobra `--version` flag must not collide with the `version` subcommand (cobra has a built-in `--version` mechanism — we use `rootCmd.Version` to opt in cleanly).
- **Phase B — Polished error layer (Tasks 4–6).** Add `internal/cliutil/errors.go` with `UserError`, render it from `Execute()`, and retrofit each command (`ingest`, `ask`, `init`, `lint`) to wrap one or two known failure modes. Exports: `cliutil.UserError`, `cliutil.Wrap()`. Risk: bare-`fmt.Errorf` callsites are everywhere; the retrofit deliberately covers only the high-traffic ones called out in the spec to keep churn bounded.
- **Phase C — Chunk bookkeeping + re-chunking (Tasks 7–9).** Schema v3 migration (`chunks` table), `Chunk` queries, the pure-function `MarkCoResidentDirty` partition pass, and the `--no-rechunk` flag wired through `cmd/ingest.go`. Exports: `db.Chunk`, `db.InsertChunks`, `db.GetChunksForFile`, `ingest.MarkCoResidentDirty`. Risk: write-amplification on neighbour-heavy chunks — `--no-rechunk` is the documented escape hatch.
- **Phase D — Feed and sitemap ingestion (Tasks 10–13).** Add gofeed dependency, fixtures, `FetchFeedFiles`, `FetchSitemapFiles`, content-type dispatch from `url.go`, and the new `--feed` / `--sitemap` / `--max-pages` flags + `[ingest]` config keys. Exports: `ingest.FetchFeedFiles`, `ingest.FetchSitemapFiles`. Risk: the URL pipeline currently returns one `SourceFile` per fetch — feeds return many. Per-entry fetches must reuse `FetchURLFiles` so the Readability path stays consistent; do not duplicate the HTML→Markdown logic.
- **Phase E — Smoke test + release engineering (Tasks 14–17).** `LLMWIKI_CASSETTE` env var, `make smoke` target, `cmd/smoke_test.go` Go-level smoke covering the same path, `.goreleaser.yml`, `LICENSE` (Apache-2.0), `CHANGELOG.md`. Exports: `make smoke` target; release artifact set on `goreleaser release --snapshot`. Risk: GoReleaser config drift breaks the tag-push release; we add a `--snapshot --clean` dry-run to CI to catch that at PR time.
- **Phase F — Error retrofit, README, demo asset, nightly drift CI (Tasks 18–21).** Apply `UserError` to the remaining commands' high-traffic failure modes (Phase B did the framework; this phase does the rollout), full README rewrite, placeholder demo asset, nightly cassette-refresh workflow. Exports: none (operator-facing only). Risk: README must not promise a feature the binary doesn't have — task ordering puts the rewrite last so every claim has already been built and tested.
- **Phase G — Final verification + rc tag (Tasks 22–23).** Whole-repo green check, manual `go install` from a clean `$GOPATH`, tag `v1.0.0-rc.1` (no push). Exports: a tag. Risk: the `go install` from a clean machine is the real test of module-path stability — flagged in the spec as a release-blocker.

---

## Phase A — Versioning core

### Task 1: `internal/version` package + `Format()` helper

**Files:**
- Create: `internal/version/version.go`
- Create: `internal/version/version_test.go`

- [ ] **Step 1: Write failing format tests**

Create `internal/version/version_test.go`:

```go
package version

import (
	"strings"
	"testing"
)

func TestFormatAllSet(t *testing.T) {
	saved := Version
	savedC := Commit
	savedD := BuildDate
	defer func() { Version, Commit, BuildDate = saved, savedC, savedD }()
	Version, Commit, BuildDate = "1.0.0-rc.1", "abc1234", "2026-05-04"
	got := Format()
	for _, want := range []string{"llmwiki", "1.0.0-rc.1", "abc1234", "2026-05-04"} {
		if !strings.Contains(got, want) {
			t.Errorf("Format() = %q, missing %q", got, want)
		}
	}
}

func TestFormatAllDevel(t *testing.T) {
	saved := Version
	savedC := Commit
	savedD := BuildDate
	defer func() { Version, Commit, BuildDate = saved, savedC, savedD }()
	Version, Commit, BuildDate = "(devel)", "(devel)", "(devel)"
	got := Format()
	if !strings.Contains(got, "(devel)") {
		t.Errorf("Format() = %q, want substring (devel)", got)
	}
}

func TestFormatPartial(t *testing.T) {
	saved := Version
	savedC := Commit
	savedD := BuildDate
	defer func() { Version, Commit, BuildDate = saved, savedC, savedD }()
	Version = "1.0.0"
	Commit = "(devel)"
	BuildDate = "(devel)"
	got := Format()
	if !strings.Contains(got, "1.0.0") {
		t.Errorf("Format() = %q, want substring 1.0.0", got)
	}
}
```

- [ ] **Step 2: Run test, confirm failure**

Run: `go test ./internal/version/ -v`
Expected: FAIL — package missing.

- [ ] **Step 3: Implement**

Create `internal/version/version.go`:

```go
// Package version exposes build-time identity vars injected by GoReleaser via
// -ldflags. For non-release builds (go run, go test, plain go build) every
// value defaults to "(devel)" and Format() prints "llmwiki (devel)".
package version

import (
	"fmt"
	"runtime"
)

// These are overridden at link time by GoReleaser. See .goreleaser.yml.
var (
	Version   = "(devel)"
	Commit    = "(devel)"
	BuildDate = "(devel)"
)

// Format returns the canonical one-line version string, e.g.
// "llmwiki 1.0.0-rc.1 (commit abc1234, built 2026-05-04, go1.26.2)".
// For development builds it collapses to "llmwiki (devel)".
func Format() string {
	if Version == "(devel)" && Commit == "(devel)" && BuildDate == "(devel)" {
		return "llmwiki (devel)"
	}
	return fmt.Sprintf("llmwiki %s (commit %s, built %s, %s)",
		Version, Commit, BuildDate, runtime.Version())
}
```

- [ ] **Step 4: Run test, confirm pass**

Run: `go test ./internal/version/ -v`
Expected: PASS — three subtests green.

- [ ] **Step 5: Commit**

```bash
git -c commit.gpgsign=false commit -m "feat(version): internal/version package with Format() and (devel) defaults"
```

---

### Task 2: `version` subcommand + `--version` root flag

**Files:**
- Create: `cmd/version.go`
- Create: `cmd/version_test.go`
- Modify: `cmd/root.go`

- [ ] **Step 1: Write failing output-shape test**

Create `cmd/version_test.go`:

```go
package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/mritunjaysharma394/llmwiki/internal/version"
)

func TestVersionCmdPrintsFormat(t *testing.T) {
	saved := version.Version
	defer func() { version.Version = saved }()
	version.Version = "1.0.0-test"

	buf := &bytes.Buffer{}
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"version"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "1.0.0-test") {
		t.Errorf("version output missing version string: %q", out)
	}
	if !strings.Contains(out, "llmwiki") {
		t.Errorf("version output missing prefix: %q", out)
	}
}
```

- [ ] **Step 2: Run test, confirm failure**

Run: `go test ./cmd/ -run TestVersionCmd -v`
Expected: FAIL — `version` subcommand not registered.

- [ ] **Step 3: Implement subcommand**

Create `cmd/version.go`:

```go
package cmd

import (
	"github.com/mritunjaysharma394/llmwiki/internal/version"
	"github.com/spf13/cobra"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version, commit, build date, Go version",
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.Println(version.Format())
		return nil
	},
}
```

In `cmd/root.go`:

1. Import `"github.com/mritunjaysharma394/llmwiki/internal/version"`.
2. Set `rootCmd.Version = version.Format()` so `--version` works as a root flag (cobra's built-in `--version` is keyed off this field).
3. Register the subcommand: `rootCmd.AddCommand(versionCmd)` in the existing `init()`.
4. The `PersistentPreRunE` already short-circuits for `init` and `help`; add `"version"` to the same skip list so `llmwiki version` works on a fresh checkout without a config file.

```go
PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
	switch cmd.Name() {
	case "init", "help", "version":
		return nil
	}
	return loadConfig()
},
```

- [ ] **Step 4: Run tests, confirm pass**

Run: `go test ./cmd/ -run TestVersionCmd -v`
Expected: PASS.

Manual check (optional): `go run . version` and `go run . --version` both print the same `llmwiki (devel)` line.

- [ ] **Step 5: Build + full test suite**

Run: `go build ./... && go test ./...`
Expected: clean build, all green.

- [ ] **Step 6: Commit**

```bash
git -c commit.gpgsign=false commit -m "feat(cmd): version subcommand and --version root flag"
```

---

### Task 3: User-Agent uses `version.Version`

**Files:**
- Modify: `internal/ingest/url.go`
- Modify: `internal/ingest/url_test.go`

The hardcoded `userAgentVersion = "0.1"` constant in `internal/ingest/url.go` must point at `version.Version` so the User-Agent header tracks releases automatically.

- [ ] **Step 1: Write failing UA test**

Append to `internal/ingest/url_test.go`:

```go
import "github.com/mritunjaysharma394/llmwiki/internal/version"

func TestFetchURLUsesVersionedUserAgent(t *testing.T) {
	saved := version.Version
	defer func() { version.Version = saved }()
	version.Version = "9.9.9-test"

	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	if _, err := FetchURLFiles(srv.URL, DefaultURLOptions()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "llmwiki/9.9.9-test") {
		t.Errorf("User-Agent = %q, want substring llmwiki/9.9.9-test", got)
	}
}
```

(The existing `httptest`/`http`/`strings` imports already cover the new test; only the `version` import is new.)

- [ ] **Step 2: Run test, confirm failure**

Run: `go test ./internal/ingest/ -run TestFetchURLUsesVersionedUserAgent -v`
Expected: FAIL — UA still says `llmwiki/0.1`.

- [ ] **Step 3: Replace the constant**

In `internal/ingest/url.go`:

1. Delete the line `const userAgentVersion = "0.1"`.
2. Import `"github.com/mritunjaysharma394/llmwiki/internal/version"`.
3. Replace both literals `"llmwiki/" + userAgentVersion` (in `DefaultURLOptions` and the `if opts.UserAgent == ""` branch of `FetchURLFiles`) with `"llmwiki/" + version.Version`.

- [ ] **Step 4: Run tests, confirm pass**

Run: `go test ./internal/ingest/ -v`
Expected: PASS — new UA test green; existing URL tests still green (they never asserted on the UA literal).

- [ ] **Step 5: Build + full test suite**

Run: `go build ./... && go test ./...`
Expected: clean build, all green.

- [ ] **Step 6: Commit**

```bash
git -c commit.gpgsign=false commit -m "feat(ingest): User-Agent reads from version.Version, drop hardcoded 0.1"
```

**End of Phase A.** Tree is green. The CLI prints version info via subcommand and root flag, and the User-Agent on every HTTP fetch carries the same string. No production behaviour besides version-string surface has changed.

---

## Phase B — Polished error layer

### Task 4: `internal/cliutil/errors.go` — `UserError` type

**Files:**
- Create: `internal/cliutil/errors.go`
- Create: `internal/cliutil/errors_test.go`

- [ ] **Step 1: Write failing tests**

Create `internal/cliutil/errors_test.go`:

```go
package cliutil

import (
	"errors"
	"strings"
	"testing"
)

func TestUserErrorFormat(t *testing.T) {
	ue := &UserError{
		Cause:       "ingest failed",
		Wrapped:     errors.New("HTTP 503 from https://example.com/feed.xml"),
		Remediation: "re-run with --max-retries=3, or check the feed in a browser",
	}
	got := ue.Error()
	for _, want := range []string{"ingest failed", "HTTP 503", "re-run with --max-retries=3"} {
		if !strings.Contains(got, want) {
			t.Errorf("Error() = %q, missing %q", got, want)
		}
	}
}

func TestUserErrorUnwrap(t *testing.T) {
	inner := errors.New("inner")
	ue := &UserError{Cause: "x", Wrapped: inner, Remediation: "y"}
	if !errors.Is(ue, inner) {
		t.Errorf("errors.Is failed for wrapped error")
	}
}

func TestUserErrorAs(t *testing.T) {
	ue := &UserError{Cause: "x", Remediation: "y"}
	wrapped := errors.New("outer: " + ue.Error())
	_ = wrapped // placeholder
	var got *UserError
	if !errors.As(ue, &got) {
		t.Errorf("errors.As failed on direct UserError")
	}
	if got.Cause != "x" {
		t.Errorf("As round-trip lost Cause: %+v", got)
	}
}

func TestRenderFormatsMultiline(t *testing.T) {
	ue := &UserError{
		Cause:       "ingest failed",
		Wrapped:     errors.New("HTTP 503"),
		Remediation: "check the URL",
	}
	got := Render(ue)
	for _, want := range []string{"Error: ingest failed", "cause: HTTP 503", "try:   check the URL"} {
		if !strings.Contains(got, want) {
			t.Errorf("Render() = %q, missing %q", got, want)
		}
	}
}

func TestRenderPlainError(t *testing.T) {
	plain := errors.New("boom")
	got := Render(plain)
	if !strings.Contains(got, "Error: boom") {
		t.Errorf("Render(plain) = %q, want Error: boom", got)
	}
}
```

- [ ] **Step 2: Run test, confirm failure**

Run: `go test ./internal/cliutil/ -v`
Expected: FAIL — package missing.

- [ ] **Step 3: Implement**

Create `internal/cliutil/errors.go`:

```go
// Package cliutil holds CLI-only helpers that don't belong in domain packages.
// UserError is the structured error type rendered by cmd/root.go's Execute().
package cliutil

import "fmt"

// UserError is an operator-facing error: a one-line cause, an optional wrapped
// underlying error (to print indented under "cause:"), and a one-line
// remediation hint. The Render function formats the three lines; Error()
// flattens them so non-Render code paths still produce a useful string.
type UserError struct {
	Cause       string // one line, no trailing period
	Wrapped     error  // optional underlying error
	Remediation string // imperative, e.g. "re-run with --max-retries=3"
}

func (e *UserError) Error() string {
	if e.Wrapped != nil {
		return fmt.Sprintf("%s: %v (try: %s)", e.Cause, e.Wrapped, e.Remediation)
	}
	return fmt.Sprintf("%s (try: %s)", e.Cause, e.Remediation)
}

func (e *UserError) Unwrap() error { return e.Wrapped }

// Wrap is a small constructor used by command-package callers.
func Wrap(cause string, wrapped error, remediation string) *UserError {
	return &UserError{Cause: cause, Wrapped: wrapped, Remediation: remediation}
}

// Render formats any error for display by Execute(). UserError gets a 3-line
// block; everything else gets the one-line "Error: <err>" treatment.
func Render(err error) string {
	if err == nil {
		return ""
	}
	var ue *UserError
	if asUserError(err, &ue) {
		if ue.Wrapped != nil {
			return fmt.Sprintf("Error: %s\n  cause: %v\n  try:   %s", ue.Cause, ue.Wrapped, ue.Remediation)
		}
		return fmt.Sprintf("Error: %s\n  try:   %s", ue.Cause, ue.Remediation)
	}
	return fmt.Sprintf("Error: %v", err)
}

// asUserError centralizes the errors.As call so callers don't need to import
// the standard "errors" package alongside cliutil.
func asUserError(err error, target **UserError) bool {
	for cur := err; cur != nil; {
		if ue, ok := cur.(*UserError); ok {
			*target = ue
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := cur.(unwrapper)
		if !ok {
			return false
		}
		cur = u.Unwrap()
	}
	return false
}
```

- [ ] **Step 4: Run tests, confirm pass**

Run: `go test ./internal/cliutil/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git -c commit.gpgsign=false commit -m "feat(cliutil): UserError type with cause+remediation rendering"
```

---

### Task 5: Wire `cliutil.Render` into `cmd/root.go`'s `Execute()`

**Files:**
- Modify: `cmd/root.go`
- Modify: `cmd/root_test.go`

- [ ] **Step 1: Write failing rendering test**

Append to `cmd/root_test.go`:

```go
import (
	"bytes"
	"errors"

	"github.com/mritunjaysharma394/llmwiki/internal/cliutil"
)

func TestExecuteRendersUserError(t *testing.T) {
	// Inject a fake command that always returns a UserError.
	probe := &cobra.Command{
		Use: "probe-fail",
		RunE: func(*cobra.Command, []string) error {
			return cliutil.Wrap("ingest failed", errors.New("HTTP 503"), "check URL")
		},
	}
	rootCmd.AddCommand(probe)
	defer rootCmd.RemoveCommand(probe)

	var buf bytes.Buffer
	rootCmd.SetErr(&buf)
	rootCmd.SetOut(&buf)
	rootCmd.SetArgs([]string{"probe-fail"})

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error")
	}
	out := cliutil.Render(err)
	for _, want := range []string{"Error: ingest failed", "cause: HTTP 503", "try:   check URL"} {
		if !strings.Contains(out, want) {
			t.Errorf("Render() missing %q in:\n%s", want, out)
		}
	}
}
```

(Add `"bytes"`, `"errors"`, `"strings"`, and the `cliutil` import as needed; `cobra` is already imported.)

- [ ] **Step 2: Run test, confirm pass**

The test exercises the rendering helper directly; `Execute()` itself returns the error unchanged. The existing `Execute()` formats with `color`, but we want the structured cause/try/remediation block in front of it.

Run: `go test ./cmd/ -run TestExecuteRendersUserError -v`
Expected: PASS — `Render()` formats the structured output regardless of how `Execute()` writes it. (If the test fails because the existing `color`-based path swallows the `cause:` line, fix it by replacing the `Execute` body in Step 3 below.)

- [ ] **Step 3: Replace `Execute()` to delegate to `cliutil.Render`**

In `cmd/root.go`, replace the body of `Execute()`:

```go
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		out := cliutil.Render(err)
		if out != "" {
			color.New(color.FgRed, color.Bold).Fprint(os.Stderr, "Error:")
			// cliutil.Render already starts with "Error:"; trim our colored prefix
			// and print the rest. Rendered already includes the cause/try lines.
			rest := strings.TrimPrefix(out, "Error:")
			fmt.Fprintln(os.Stderr, rest)
		}
		os.Exit(1)
	}
}
```

Add `"strings"` and `"github.com/mritunjaysharma394/llmwiki/internal/cliutil"` imports.

- [ ] **Step 4: Build + test**

Run: `go build ./... && go test ./...`
Expected: clean build, all green.

- [ ] **Step 5: Commit**

```bash
git -c commit.gpgsign=false commit -m "feat(cmd): Execute() renders UserError via cliutil with cause/try lines"
```

---

### Task 6: Retrofit `cmd/init.go` and `cmd/ask.go` with `UserError`

**Files:**
- Modify: `cmd/init.go`
- Modify: `cmd/ask.go`

The full retrofit happens in Phase F; this task covers the two highest-traffic failure modes called out in the spec — the missing-API-key and the FTS-unavailable paths — to prove the pattern end-to-end before broader rollout.

- [ ] **Step 1: Wrap `init`'s key-missing error**

In `cmd/init.go`, replace the existing `fmt.Errorf("ANTHROPIC_API_KEY ...")` block with:

```go
if provider == "anthropic" {
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		return cliutil.Wrap(
			"ANTHROPIC_API_KEY is not set",
			nil,
			"export ANTHROPIC_API_KEY=sk-ant-... (get one at https://console.anthropic.com/settings/keys), or use --provider ollama",
		)
	}
}
```

Add the `cliutil` import.

- [ ] **Step 2: Wrap `loadConfig`'s key-missing error**

In `cmd/root.go`, the same key-missing block in `loadConfig()` exists. Replace it with a `cliutil.Wrap` call so non-`init` commands also produce structured output. Use the same Cause/Remediation strings.

- [ ] **Step 3: Wrap `ask`'s "no pages, no key" branch**

In `cmd/ask.go`, the FTS-unavailable fallback path currently prints `"(page FTS unavailable; scanning all pages)"` and silently degrades. Leave that alone — it's a graceful degradation, not an error. Instead, find the `runAsk` early-exit when the wiki is empty and the user has run `ask` before any `ingest`. Wrap that as:

```go
if len(allPages) == 0 {
	return cliutil.Wrap(
		"no pages in wiki",
		nil,
		"run 'llmwiki ingest <source>' to add content first",
	)
}
```

(Substitute the actual variable name used by `runAsk`'s scan path.)

- [ ] **Step 4: Manual smoke check**

```bash
go build -o /tmp/llmwiki .
unset ANTHROPIC_API_KEY
/tmp/llmwiki init
# Expect:
# Error: ANTHROPIC_API_KEY is not set
#   try:   export ANTHROPIC_API_KEY=sk-ant-... ...
```

- [ ] **Step 5: Build + test**

Run: `go build ./... && go test ./...`
Expected: clean build, all green.

- [ ] **Step 6: Commit**

```bash
git -c commit.gpgsign=false commit -m "feat(cmd): wrap key-missing and empty-wiki errors as UserError"
```

**End of Phase B.** Tree is green. Two failure modes (missing key, empty wiki) print 3-line structured errors. The remaining retrofit lands in Phase F after the new failure modes from feed/sitemap/re-chunking exist to be wrapped.

---

## Phase C — Chunk bookkeeping + re-chunking

### Task 7: Schema v3 — `chunks` table

**Files:**
- Modify: `internal/db/db.go`
- Modify: `internal/db/db_test.go`

- [ ] **Step 1: Write failing v3 migration tests**

Append to `internal/db/db_test.go`:

```go
func TestOpenAtFreshV3(t *testing.T) {
	d := mustOpen(t)
	var name string
	if err := d.sql.QueryRow(`SELECT name FROM sqlite_master WHERE name = 'chunks'`).Scan(&name); err != nil {
		t.Errorf("chunks table missing: %v", err)
	}
	var version int
	d.sql.QueryRow(`PRAGMA user_version`).Scan(&version)
	if version != 3 {
		t.Errorf("user_version = %d, want 3", version)
	}
}

func TestOpenUpgradesV2ToV3(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wiki.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	d.sql.Exec(`DROP TABLE chunks`)
	d.sql.Exec(`PRAGMA user_version = 2`)
	d.Close()

	d2, err := Open(path)
	if err != nil {
		t.Fatalf("re-open: %v", err)
	}
	defer d2.Close()
	var v int
	d2.sql.QueryRow(`PRAGMA user_version`).Scan(&v)
	if v != 3 {
		t.Errorf("user_version after upgrade = %d, want 3", v)
	}
}
```

Update existing tests that asserted `user_version = 2` to assert `= 3` (`TestOpenAtFreshV2`, `TestOpenUpgradesV1ToV2`, `TestOpenUpgradesLegacyV0ToV2`).

- [ ] **Step 2: Run tests, confirm failure**

Run: `go test ./internal/db/ -v`
Expected: FAIL — `chunks` missing, `user_version` is 2.

- [ ] **Step 3: Add v3 migration block in `db.go`**

In `internal/db/db.go`'s `migrate()`, after the v2 block and before the `PRAGMA foreign_keys = ON` exec, insert:

```go
if version < 3 {
	v3 := []string{
		`CREATE TABLE IF NOT EXISTS chunks (
			id INTEGER PRIMARY KEY,
			source_id INTEGER NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
			chunk_hash TEXT NOT NULL,
			file_paths TEXT NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_chunks_source ON chunks(source_id)`,
		`PRAGMA user_version = 3`,
	}
	for _, stmt := range v3 {
		if _, err := d.sql.Exec(stmt); err != nil {
			return fmt.Errorf("v3 migration %q: %w", stmt[:min(50, len(stmt))], err)
		}
	}
}
```

- [ ] **Step 4: Run tests, confirm pass**

Run: `go test ./internal/db/ -v`
Expected: PASS — fresh-v3 + v2→v3 + idempotent re-open all green.

- [ ] **Step 5: Commit**

```bash
git -c commit.gpgsign=false commit -m "feat(db): schema v3 — chunks table for co-resident bookkeeping"
```

---

### Task 8: `Chunk` queries + `MarkCoResidentDirty` partition pass

**Files:**
- Modify: `internal/db/queries.go`
- Modify: `internal/db/db_test.go`
- Modify: `internal/ingest/chunk.go`
- Modify: `internal/ingest/chunk_test.go`

- [ ] **Step 1: Write failing CRUD + partition tests**

Append to `internal/db/db_test.go`:

```go
func TestChunkCRUD(t *testing.T) {
	d := mustOpen(t)
	srcID, _ := d.UpsertSource("u", "h")
	c1 := Chunk{SourceID: srcID, ChunkHash: "h1", FilePaths: []string{"a.go", "b.go"}}
	c2 := Chunk{SourceID: srcID, ChunkHash: "h2", FilePaths: []string{"c.go"}}
	if err := d.InsertChunks([]Chunk{c1, c2}); err != nil {
		t.Fatal(err)
	}

	got, err := d.GetChunksForFile(srcID, "a.go")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ChunkHash != "h1" {
		t.Errorf("GetChunksForFile a.go = %+v", got)
	}

	if err := d.DeleteChunksForSource(srcID); err != nil {
		t.Fatal(err)
	}
	got, _ = d.GetChunksForFile(srcID, "a.go")
	if len(got) != 0 {
		t.Errorf("post-delete = %+v", got)
	}
}
```

Append to `internal/ingest/chunk_test.go`:

```go
func TestMarkCoResidentDirtyPicksNeighbours(t *testing.T) {
	priorChunks := map[string][]string{
		// chunkA contained a.go + b.go + c.go
		"chunkA": {"a.go", "b.go", "c.go"},
		// chunkB contained d.go alone
		"chunkB": {"d.go"},
	}
	changed := []string{"a.go"}
	dirty := MarkCoResidentDirty(changed, priorChunks)

	want := map[string]bool{"a.go": true, "b.go": true, "c.go": true}
	if len(dirty) != 3 {
		t.Errorf("got %d dirty, want 3: %v", len(dirty), dirty)
	}
	for _, p := range dirty {
		if !want[p] {
			t.Errorf("unexpected dirty file: %q", p)
		}
	}
}

func TestMarkCoResidentDirtyMultipleChangedDeduped(t *testing.T) {
	priorChunks := map[string][]string{
		"chunkA": {"a.go", "b.go"},
		"chunkB": {"b.go", "c.go"},
	}
	dirty := MarkCoResidentDirty([]string{"a.go", "c.go"}, priorChunks)
	// a.go pulls in b.go via chunkA; c.go pulls in b.go via chunkB. b.go appears
	// once.
	if len(dirty) != 3 {
		t.Errorf("got %d dirty, want 3 (a, b, c): %v", len(dirty), dirty)
	}
}

func TestMarkCoResidentDirtyEmptyPrior(t *testing.T) {
	dirty := MarkCoResidentDirty([]string{"a.go"}, nil)
	if len(dirty) != 1 || dirty[0] != "a.go" {
		t.Errorf("got %v, want [a.go]", dirty)
	}
}
```

- [ ] **Step 2: Run tests, confirm failure**

Run: `go test ./internal/db/ ./internal/ingest/ -v`
Expected: FAIL — `Chunk`, `InsertChunks`, `GetChunksForFile`, `DeleteChunksForSource`, `MarkCoResidentDirty` undefined.

- [ ] **Step 3: Implement DB queries**

In `internal/db/queries.go`:

```go
type Chunk struct {
	ID         int64
	SourceID   int64
	ChunkHash  string
	FilePaths  []string
	CreatedAt  time.Time
}

func (d *DB) InsertChunks(chunks []Chunk) error {
	if len(chunks) == 0 {
		return nil
	}
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT INTO chunks (source_id, chunk_hash, file_paths) VALUES (?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, c := range chunks {
		paths, _ := json.Marshal(c.FilePaths)
		if _, err := stmt.Exec(c.SourceID, c.ChunkHash, string(paths)); err != nil {
			return fmt.Errorf("insert chunk: %w", err)
		}
	}
	return tx.Commit()
}

// GetChunksForFile returns chunks that included relativePath in their pack.
// Uses LIKE on the JSON-encoded array; for the v1 row volumes (low thousands
// at most) this is fast enough and avoids needing JSON1 builds of sqlite.
func (d *DB) GetChunksForFile(sourceID int64, relativePath string) ([]Chunk, error) {
	pat := "%" + jsonEscape(relativePath) + "%"
	rows, err := d.sql.Query(
		`SELECT id, source_id, chunk_hash, file_paths, created_at
		 FROM chunks WHERE source_id = ? AND file_paths LIKE ?`,
		sourceID, pat,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Chunk
	for rows.Next() {
		var c Chunk
		var paths string
		var ts string
		if err := rows.Scan(&c.ID, &c.SourceID, &c.ChunkHash, &paths, &ts); err != nil {
			return nil, err
		}
		json.Unmarshal([]byte(paths), &c.FilePaths)
		c.CreatedAt, _ = time.Parse(time.RFC3339, ts)
		// Filter out false positives from the LIKE: ensure the path is in the
		// JSON array exactly.
		exact := false
		for _, p := range c.FilePaths {
			if p == relativePath {
				exact = true
				break
			}
		}
		if exact {
			out = append(out, c)
		}
	}
	return out, rows.Err()
}

func (d *DB) DeleteChunksForSource(sourceID int64) error {
	_, err := d.sql.Exec(`DELETE FROM chunks WHERE source_id = ?`, sourceID)
	return err
}

// jsonEscape is the minimal escape needed for LIKE patterns over JSON-encoded
// strings: the path itself can contain characters that LIKE treats specially
// (% and _). We escape them with a backslash and use LIKE ... ESCAPE '\\'.
// In practice file paths don't contain % or _ in the underscore-as-glob sense,
// but be defensive.
func jsonEscape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}
```

(The `GetChunksForFile` query above does not actually use `ESCAPE '\'`; the in-Go exact-match filter after scan is the real correctness gate. The LIKE is just an index-friendly pre-filter.)

- [ ] **Step 4: Implement `MarkCoResidentDirty`**

In `internal/ingest/chunk.go`:

```go
// MarkCoResidentDirty returns the union of `changed` plus every file that
// shared a prior chunk with any element of `changed`. Used by re-ingest to
// keep cross-file synthesis stable when a single neighbour grows enough to
// shift bin-packing decisions.
//
// priorChunks maps a chunk identifier (caller-defined; typically the chunk
// hash) to the list of relative paths that chunk packed. When priorChunks is
// nil or empty, MarkCoResidentDirty returns `changed` unchanged — fresh
// ingests have no prior chunks to consult.
func MarkCoResidentDirty(changed []string, priorChunks map[string][]string) []string {
	dirty := map[string]bool{}
	for _, p := range changed {
		dirty[p] = true
	}
	for _, p := range changed {
		for _, paths := range priorChunks {
			has := false
			for _, q := range paths {
				if q == p {
					has = true
					break
				}
			}
			if !has {
				continue
			}
			for _, q := range paths {
				dirty[q] = true
			}
		}
	}
	out := make([]string, 0, len(dirty))
	for k := range dirty {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
```

Add `"sort"` to the imports of `internal/ingest/chunk.go`.

- [ ] **Step 5: Run tests, confirm pass**

Run: `go test ./internal/db/ ./internal/ingest/ -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git -c commit.gpgsign=false commit -m "feat(db,ingest): chunks CRUD and MarkCoResidentDirty pure partition"
```

---

### Task 9: Wire re-chunking into `cmd/ingest.go` + `--no-rechunk` flag

**Files:**
- Modify: `cmd/ingest.go`
- Modify: `cmd/ingest_test.go`

- [ ] **Step 1: Register the flag**

In `cmd/ingest.go`'s `init()`:

```go
ingestCmd.Flags().Bool("no-rechunk", false, "skip co-resident re-chunking; only re-process files whose own content changed")
```

- [ ] **Step 2: Write failing flag-plumbing test**

Append to `cmd/ingest_test.go`:

```go
func TestIngestFlagNoRechunkRegistered(t *testing.T) {
	if ingestCmd.Flags().Lookup("no-rechunk") == nil {
		t.Fatal("--no-rechunk flag not registered")
	}
}
```

- [ ] **Step 3: Run test, confirm pass**

Run: `go test ./cmd/ -run TestIngestFlagNoRechunkRegistered -v`
Expected: PASS once Step 1 is in place.

- [ ] **Step 4: Wire dirtying into `runIngest`**

In `cmd/ingest.go`'s `runIngest`, after `partitionByFileHash` returns `parts` and before the chunker runs, insert:

```go
if v, _ := cmd.Flags().GetBool("no-rechunk"); !v && len(parts.changed) > 0 {
	// Build prior-chunks map for this source.
	priorChunks := map[string][]string{}
	for _, ch := range parts.changed {
		chunks, err := database.GetChunksForFile(sourceID, ch.RelativePath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  WARN reading prior chunks for %s: %v\n", ch.RelativePath, err)
			continue
		}
		for _, c := range chunks {
			priorChunks[c.ChunkHash] = c.FilePaths
		}
	}
	changedPaths := make([]string, len(parts.changed))
	for i, f := range parts.changed {
		changedPaths[i] = f.RelativePath
	}
	dirtyPaths := ingest.MarkCoResidentDirty(changedPaths, priorChunks)

	// Promote each dirty co-resident from `unchanged` into `changed`.
	dirtySet := map[string]bool{}
	for _, p := range dirtyPaths {
		dirtySet[p] = true
	}
	stillUnchanged := parts.unchanged[:0]
	for _, f := range parts.unchanged {
		if dirtySet[f.RelativePath] {
			parts.changed = append(parts.changed, f)
		} else {
			stillUnchanged = append(stillUnchanged, f)
		}
	}
	parts.unchanged = stillUnchanged
}
```

After chunking, write the new `chunks` rows. After the existing `database.UpsertSourceFile` loop, but before the page-write loop, insert:

```go
// Replace prior chunk bookkeeping for this source with the fresh pack.
if err := database.DeleteChunksForSource(sourceID); err != nil {
	fmt.Fprintf(os.Stderr, "  WARN clearing chunks for source %d: %v\n", sourceID, err)
}
var chunkRows []db.Chunk
for _, ch := range chunks {
	hash := sha256.Sum256([]byte(ch.Text))
	paths := make([]string, len(ch.Files))
	for i, f := range ch.Files {
		paths[i] = f.RelativePath
	}
	chunkRows = append(chunkRows, db.Chunk{
		SourceID:  sourceID,
		ChunkHash: fmt.Sprintf("%x", hash),
		FilePaths: paths,
	})
}
if err := database.InsertChunks(chunkRows); err != nil {
	fmt.Fprintf(os.Stderr, "  WARN persisting chunks for source %d: %v\n", sourceID, err)
}
```

(`crypto/sha256` is already imported by `cmd/ingest.go`.)

- [ ] **Step 5: Build + test**

Run: `go build ./... && go test ./...`
Expected: clean build, all green. Existing cassette tests still replay because re-ingest of a fresh wiki has no prior `chunks` rows; the dirtying pass is a no-op.

- [ ] **Step 6: Commit**

```bash
git -c commit.gpgsign=false commit -m "feat(ingest): co-resident re-chunking with --no-rechunk escape hatch"
```

**End of Phase C.** Tree is green. Re-ingest with a single-byte change to a neighbour now re-LLMs the original chunkmates as well, fixing the punted invariant from sub-project 3.

---

## Phase D — Feed and sitemap ingestion

### Task 10: Add `gofeed` dep + RSS/Atom/JSON Feed fixtures

**Files:**
- Modify: `go.mod` / `go.sum`
- Create: `internal/ingest/testdata/feeds/sample.rss.xml`
- Create: `internal/ingest/testdata/feeds/sample.atom.xml`
- Create: `internal/ingest/testdata/feeds/sample.json`
- Create: `internal/ingest/testdata/feeds/malformed.xml`

- [ ] **Step 1: Add the dependency**

```bash
go get github.com/mmcdole/gofeed
```

- [ ] **Step 2: Build the fixtures**

Each fixture should contain three entries with distinct titles, plain HTML content (`<p>...</p>`), and `<link>` URLs that point at `https://example.test/post-1`, `/post-2`, `/post-3`. The `httptest.Server` in the next task substitutes its own base URL for the test runs.

`sample.rss.xml`: minimal RSS 2.0 with `<channel>` and three `<item>`s.
`sample.atom.xml`: minimal Atom 1.0 with three `<entry>`s.
`sample.json`: JSON Feed v1.1 with three `items`.
`malformed.xml`: a file whose root element is `<random>foo</random>` — gofeed must reject.

Commit small files; total < 4 KB.

- [ ] **Step 3: Verify build green**

Run: `go build ./...`
Expected: clean build.

- [ ] **Step 4: Commit**

```bash
git -c commit.gpgsign=false commit -m "deps,test: gofeed v1.x and feed fixtures (rss/atom/json/malformed)"
```

---

### Task 11: `internal/ingest/feed.go` — `FetchFeedFiles`

**Files:**
- Create: `internal/ingest/feed.go`
- Create: `internal/ingest/feed_test.go`

- [ ] **Step 1: Write failing tests**

Create `internal/ingest/feed_test.go`:

```go
package ingest

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// serveFile returns an httptest.Server that serves a single file at "/" with
// the given content-type, plus three "post-N" routes returning HTML bodies.
func serveFeedAndPosts(t *testing.T, fixturePath, contentType string) *httptest.Server {
	t.Helper()
	body, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.Write(body)
	})
	for i := 1; i <= 3; i++ {
		i := i
		mux.HandleFunc(fmt.Sprintf("/post-%d", i), func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprintf(w, "<html><body><article><h1>Post %d</h1><p>Body of post %d.</p></article></body></html>", i, i)
		})
	}
	return httptest.NewServer(mux)
}

func TestFetchFeedFilesRSS(t *testing.T) {
	srv := serveFeedAndPosts(t, "testdata/feeds/sample.rss.xml", "application/rss+xml")
	defer srv.Close()
	files, err := FetchFeedFiles(srv.URL+"/", DefaultURLOptions(), DefaultFeedOptions())
	if err != nil {
		t.Fatalf("FetchFeedFiles: %v", err)
	}
	if len(files) != 3 {
		t.Fatalf("got %d entries, want 3", len(files))
	}
	for _, f := range files {
		if !strings.HasPrefix(f.RelativePath, srv.URL+"/post-") {
			t.Errorf("entry path %q not anchored to entry URL", f.RelativePath)
		}
	}
}

func TestFetchFeedFilesAtom(t *testing.T) {
	srv := serveFeedAndPosts(t, "testdata/feeds/sample.atom.xml", "application/atom+xml")
	defer srv.Close()
	files, err := FetchFeedFiles(srv.URL+"/", DefaultURLOptions(), DefaultFeedOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 3 {
		t.Errorf("atom: got %d, want 3", len(files))
	}
}

func TestFetchFeedFilesJSON(t *testing.T) {
	srv := serveFeedAndPosts(t, "testdata/feeds/sample.json", "application/json")
	defer srv.Close()
	files, err := FetchFeedFiles(srv.URL+"/", DefaultURLOptions(), DefaultFeedOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 3 {
		t.Errorf("jsonfeed: got %d, want 3", len(files))
	}
}

func TestFetchFeedFilesCapAtMaxEntries(t *testing.T) {
	srv := serveFeedAndPosts(t, "testdata/feeds/sample.rss.xml", "application/rss+xml")
	defer srv.Close()
	opts := DefaultFeedOptions()
	opts.MaxEntries = 2
	files, err := FetchFeedFiles(srv.URL+"/", DefaultURLOptions(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Errorf("MaxEntries=2 honored? got %d files", len(files))
	}
}

func TestFetchFeedFilesMalformed(t *testing.T) {
	srv := serveFeedAndPosts(t, "testdata/feeds/malformed.xml", "application/rss+xml")
	defer srv.Close()
	if _, err := FetchFeedFiles(srv.URL+"/", DefaultURLOptions(), DefaultFeedOptions()); err == nil {
		t.Error("expected error for malformed feed")
	}
}
```

(`fmt` import added implicitly.)

- [ ] **Step 2: Run tests, confirm failure**

Run: `go test ./internal/ingest/ -run FetchFeedFiles -v`
Expected: FAIL — `FetchFeedFiles`, `FeedOptions`, `DefaultFeedOptions` undefined.

- [ ] **Step 3: Implement**

Create `internal/ingest/feed.go`:

```go
package ingest

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mmcdole/gofeed"
)

// FeedOptions controls feed crawling rate and breadth.
type FeedOptions struct {
	RequestsPerSecond float64 // 0 = use default 1.0
	MaxEntries        int     // 0 = use default 50
}

func DefaultFeedOptions() FeedOptions {
	return FeedOptions{RequestsPerSecond: 1.0, MaxEntries: 50}
}

// FetchFeedFiles fetches an RSS/Atom/JSON Feed at feedURL and returns one
// SourceFile per entry. Each entry's permalink (Link) becomes the SourceFile's
// RelativePath, and its content is whatever sub-project 3's URL pipeline
// produces for that link (Readability + html-to-markdown for HTML, raw passthrough
// for text/*, page-N for PDFs). Polite rate-limiting is enforced between
// per-entry fetches.
//
// Entries beyond MaxEntries are skipped silently — incremental re-ingest will
// pick them up on later runs as long as they are still in the feed.
func FetchFeedFiles(feedURL string, urlOpts URLOptions, opts FeedOptions) ([]SourceFile, error) {
	if opts.RequestsPerSecond <= 0 {
		opts.RequestsPerSecond = 1.0
	}
	if opts.MaxEntries <= 0 {
		opts.MaxEntries = 50
	}

	parser := gofeed.NewParser()
	parser.UserAgent = urlOpts.UserAgent
	feed, err := parser.ParseURLWithContext(feedURL, context.Background())
	if err != nil {
		return nil, fmt.Errorf("parse feed %s: %w", feedURL, err)
	}

	var out []SourceFile
	gap := time.Duration(float64(time.Second) / opts.RequestsPerSecond)
	for i, item := range feed.Items {
		if i >= opts.MaxEntries {
			break
		}
		link := strings.TrimSpace(item.Link)
		if link == "" {
			continue
		}
		if i > 0 {
			time.Sleep(gap)
		}
		entryFiles, err := FetchURLFiles(link, urlOpts)
		if err != nil {
			// Skip the bad entry; warn via stderr indirectly through caller (we
			// stay silent here because the caller (cmd/ingest) prints a
			// per-entry summary). Returning a partial list is the right
			// behaviour for "subscribe to a feed".
			continue
		}
		for _, f := range entryFiles {
			f.RelativePath = link
			out = append(out, f)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("feed %s yielded zero ingestable entries", feedURL)
	}
	return out, nil
}
```

- [ ] **Step 4: Run tests, confirm pass**

Run: `go test ./internal/ingest/ -v`
Expected: PASS — RSS, Atom, JSON, cap, malformed all green.

- [ ] **Step 5: Commit**

```bash
git -c commit.gpgsign=false commit -m "feat(ingest): FetchFeedFiles via gofeed with polite rate-limit and entry cap"
```

---

### Task 12: `internal/ingest/sitemap.go` — `FetchSitemapFiles`

**Files:**
- Create: `internal/ingest/sitemap.go`
- Create: `internal/ingest/sitemap_test.go`
- Create: `internal/ingest/testdata/sitemaps/flat.xml`
- Create: `internal/ingest/testdata/sitemaps/index.xml`
- Create: `internal/ingest/testdata/sitemaps/nested.xml`
- Create: `internal/ingest/testdata/sitemaps/malformed.xml`

- [ ] **Step 1: Build fixtures**

`flat.xml` — `<urlset>` with 5 `<url><loc>...</loc></url>` entries pointing at `https://example.test/p-1` ... `/p-5`.
`index.xml` — `<sitemapindex>` referencing `nested.xml` once.
`nested.xml` — `<urlset>` with 3 entries.
`malformed.xml` — invalid XML.

The test substitutes `srv.URL` for `https://example.test` at runtime.

- [ ] **Step 2: Write failing tests**

Create `internal/ingest/sitemap_test.go`:

```go
package ingest

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func serveSitemapAndPages(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	for _, name := range []string{"flat.xml", "index.xml", "nested.xml", "malformed.xml"} {
		path := name
		body, err := os.ReadFile("testdata/sitemaps/" + path)
		if err != nil {
			t.Fatal(err)
		}
		mux.HandleFunc("/"+path, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/xml")
			w.Write(body)
		})
	}
	for _, prefix := range []string{"/p-", "/q-"} {
		prefix := prefix
		for i := 1; i <= 5; i++ {
			i := i
			mux.HandleFunc(fmt.Sprintf("%s%d", prefix, i), func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				fmt.Fprintf(w, "<html><body><article><p>%s%d body</p></article></body></html>", prefix, i)
			})
		}
	}
	return httptest.NewServer(mux)
}

func TestFetchSitemapFilesFlat(t *testing.T) {
	srv := serveSitemapAndPages(t)
	defer srv.Close()
	// Replace the example.test base URL in the fixture by serving a rewritten
	// version. For simplicity, the fixtures already use relative paths /p-N
	// resolved against the server URL — adjust fixtures accordingly.
	files, err := FetchSitemapFiles(srv.URL+"/flat.xml", DefaultURLOptions(), DefaultSitemapOptions())
	if err != nil {
		t.Fatalf("FetchSitemapFiles: %v", err)
	}
	if len(files) != 5 {
		t.Errorf("got %d, want 5", len(files))
	}
}

func TestFetchSitemapFilesNested(t *testing.T) {
	srv := serveSitemapAndPages(t)
	defer srv.Close()
	files, err := FetchSitemapFiles(srv.URL+"/index.xml", DefaultURLOptions(), DefaultSitemapOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 3 {
		t.Errorf("nested got %d, want 3", len(files))
	}
}

func TestFetchSitemapFilesMaxPagesCap(t *testing.T) {
	srv := serveSitemapAndPages(t)
	defer srv.Close()
	opts := DefaultSitemapOptions()
	opts.MaxPages = 2
	files, _ := FetchSitemapFiles(srv.URL+"/flat.xml", DefaultURLOptions(), opts)
	if len(files) > 2 {
		t.Errorf("cap not honored: got %d", len(files))
	}
}

func TestFetchSitemapFilesMalformed(t *testing.T) {
	srv := serveSitemapAndPages(t)
	defer srv.Close()
	if _, err := FetchSitemapFiles(srv.URL+"/malformed.xml", DefaultURLOptions(), DefaultSitemapOptions()); err == nil {
		t.Error("expected error for malformed sitemap")
	}
}
```

Adjust fixtures so `<loc>` URLs are absolute against the test server. Easiest: have the test rewrite fixtures via `strings.ReplaceAll(body, "https://example.test", srv.URL)` before serving — adjust the handler to do this.

- [ ] **Step 3: Run tests, confirm failure**

Run: `go test ./internal/ingest/ -run FetchSitemapFiles -v`
Expected: FAIL — `FetchSitemapFiles`, `SitemapOptions`, `DefaultSitemapOptions` undefined.

- [ ] **Step 4: Implement**

Create `internal/ingest/sitemap.go`:

```go
package ingest

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type SitemapOptions struct {
	MaxPages          int     // 0 → use default 200
	RequestsPerSecond float64 // 0 → use default 1.0
}

func DefaultSitemapOptions() SitemapOptions {
	return SitemapOptions{MaxPages: 200, RequestsPerSecond: 1.0}
}

type sitemapURL struct {
	Loc string `xml:"loc"`
}

type urlSet struct {
	XMLName xml.Name     `xml:"urlset"`
	URLs    []sitemapURL `xml:"url"`
}

type sitemapIndex struct {
	XMLName  xml.Name     `xml:"sitemapindex"`
	Sitemaps []sitemapURL `xml:"sitemap"`
}

// FetchSitemapFiles crawls a sitemap.xml or sitemap-index. One level of
// sitemap-of-sitemaps recursion is supported; deeper is rejected. Each leaf
// URL becomes a SourceFile via FetchURLFiles, with the URL as RelativePath.
func FetchSitemapFiles(sitemapURL string, urlOpts URLOptions, opts SitemapOptions) ([]SourceFile, error) {
	if opts.MaxPages <= 0 {
		opts.MaxPages = 200
	}
	if opts.RequestsPerSecond <= 0 {
		opts.RequestsPerSecond = 1.0
	}
	urls, err := flattenSitemap(sitemapURL, urlOpts, 0)
	if err != nil {
		return nil, err
	}
	if len(urls) > opts.MaxPages {
		urls = urls[:opts.MaxPages]
	}
	gap := time.Duration(float64(time.Second) / opts.RequestsPerSecond)
	var out []SourceFile
	for i, u := range urls {
		if i > 0 {
			time.Sleep(gap)
		}
		entryFiles, err := FetchURLFiles(u, urlOpts)
		if err != nil {
			continue
		}
		for _, f := range entryFiles {
			f.RelativePath = u
			out = append(out, f)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("sitemap %s yielded zero ingestable URLs", sitemapURL)
	}
	return out, nil
}

func flattenSitemap(u string, urlOpts URLOptions, depth int) ([]string, error) {
	if depth > 1 {
		return nil, fmt.Errorf("sitemap recursion depth > 1: %s", u)
	}
	body, err := fetchRaw(u, urlOpts)
	if err != nil {
		return nil, err
	}
	// Try urlset.
	var us urlSet
	if err := xml.Unmarshal(body, &us); err == nil && len(us.URLs) > 0 {
		out := make([]string, 0, len(us.URLs))
		for _, e := range us.URLs {
			loc := strings.TrimSpace(e.Loc)
			if loc != "" {
				out = append(out, loc)
			}
		}
		return out, nil
	}
	// Try sitemapindex.
	var si sitemapIndex
	if err := xml.Unmarshal(body, &si); err == nil && len(si.Sitemaps) > 0 {
		var out []string
		for _, e := range si.Sitemaps {
			child, err := flattenSitemap(strings.TrimSpace(e.Loc), urlOpts, depth+1)
			if err != nil {
				return nil, err
			}
			out = append(out, child...)
		}
		return out, nil
	}
	return nil, fmt.Errorf("sitemap %s: not a valid <urlset> or <sitemapindex>", u)
}

func fetchRaw(u string, opts URLOptions) ([]byte, error) {
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: opts.Timeout}
	}
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", opts.UserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching sitemap %s: %w", u, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d fetching %s", resp.StatusCode, u)
	}
	return io.ReadAll(io.LimitReader(resp.Body, opts.MaxBodyBytes))
}
```

- [ ] **Step 5: Run tests, confirm pass**

Run: `go test ./internal/ingest/ -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git -c commit.gpgsign=false commit -m "feat(ingest): FetchSitemapFiles with one-level index recursion and page cap"
```

---

### Task 13: Content-type dispatch + CLI flags + config keys

**Files:**
- Modify: `internal/ingest/url.go`
- Modify: `cmd/ingest.go`
- Modify: `cmd/root.go`
- Modify: `cmd/init.go`

- [ ] **Step 1: Sniff feed/sitemap content from `FetchURLFiles`**

In `internal/ingest/url.go`, extend the content-type switch in `FetchURLFiles`:

```go
switch {
case ct == "application/pdf" || extLower == ".pdf":
	return fetchPDFViaTempFile(body)
case isFeedContentType(ct, body):
	// Re-dispatch through FetchFeedFiles, which re-fetches the feed body
	// (cheap; gofeed parses URL directly). Acceptable double-fetch for v1.
	return FetchFeedFiles(rawURL, opts, DefaultFeedOptions())
case isSitemapContentType(ct, body, parsed):
	return FetchSitemapFiles(rawURL, opts, DefaultSitemapOptions())
case ct == "text/html", ct == "application/xhtml+xml":
	return fetchHTMLAsMarkdown(body, rawURL)
case strings.HasPrefix(ct, "text/"):
	return []SourceFile{NewSourceFile("body.txt", body)}, nil
default:
	return nil, fmt.Errorf("unsupported content-type %q for URL ingestion", ct)
}
```

Add the helpers in the same file:

```go
func isFeedContentType(ct string, body []byte) bool {
	switch ct {
	case "application/rss+xml", "application/atom+xml":
		return true
	}
	if ct == "application/json" {
		// JSON Feed — sniff the "feed_url" or "version" key.
		return bytes.Contains(body, []byte(`"version"`)) && bytes.Contains(body, []byte(`jsonfeed.org`))
	}
	if ct == "application/xml" || ct == "text/xml" {
		head := body
		if len(head) > 512 {
			head = head[:512]
		}
		return bytes.Contains(head, []byte("<rss")) || bytes.Contains(head, []byte("<feed"))
	}
	return false
}

func isSitemapContentType(ct string, body []byte, parsed *url.URL) bool {
	if parsed != nil && strings.HasSuffix(strings.ToLower(parsed.Path), "sitemap.xml") {
		return true
	}
	if ct == "application/xml" || ct == "text/xml" {
		head := body
		if len(head) > 512 {
			head = head[:512]
		}
		return bytes.Contains(head, []byte("<urlset")) || bytes.Contains(head, []byte("<sitemapindex"))
	}
	return false
}
```

- [ ] **Step 2: Add the new flags + config keys**

In `cmd/ingest.go`'s `init()`:

```go
ingestCmd.Flags().Bool("feed", false, "force feed-parser dispatch")
ingestCmd.Flags().Bool("sitemap", false, "force sitemap dispatch")
ingestCmd.Flags().Int("max-pages", 0, "cap on feed entries / sitemap pages (0 uses [ingest] defaults)")
```

In `runIngest`, before the existing scheme switch, check `--feed`/`--sitemap`:

```go
if v, _ := cmd.Flags().GetBool("feed"); v {
	feedOpts := DefaultFeedOptionsFromConfig(cfg)
	if mp, _ := cmd.Flags().GetInt("max-pages"); mp > 0 {
		feedOpts.MaxEntries = mp
	}
	sourceFiles, err = ingest.FetchFeedFiles(source, urlOpts, feedOpts)
} else if v, _ := cmd.Flags().GetBool("sitemap"); v {
	smOpts := DefaultSitemapOptionsFromConfig(cfg)
	if mp, _ := cmd.Flags().GetInt("max-pages"); mp > 0 {
		smOpts.MaxPages = mp
	}
	sourceFiles, err = ingest.FetchSitemapFiles(source, urlOpts, smOpts)
} else {
	// existing dispatch (github / http / local)
	switch { ... }
}
```

In `cmd/root.go`, extend `IngestConfig` and `applyIngestDefaults`:

```go
type IngestConfig struct {
	// ... existing fields ...
	FeedRequestsPerSecond float64 `toml:"feed_request_per_second"`
	FeedMaxEntries        int     `toml:"feed_max_entries"`
	SitemapMaxPages       int     `toml:"sitemap_max_pages"`
}

// in applyIngestDefaults:
if c.FeedRequestsPerSecond == 0 {
	c.FeedRequestsPerSecond = 1.0
}
if c.FeedMaxEntries == 0 {
	c.FeedMaxEntries = 50
}
if c.SitemapMaxPages == 0 {
	c.SitemapMaxPages = 200
}
```

Add small helpers used by `cmd/ingest.go`:

```go
// in cmd/ingest.go
func DefaultFeedOptionsFromConfig(c *Config) ingest.FeedOptions {
	if c == nil {
		return ingest.DefaultFeedOptions()
	}
	return ingest.FeedOptions{
		RequestsPerSecond: c.Ingest.FeedRequestsPerSecond,
		MaxEntries:        c.Ingest.FeedMaxEntries,
	}
}

func DefaultSitemapOptionsFromConfig(c *Config) ingest.SitemapOptions {
	if c == nil {
		return ingest.DefaultSitemapOptions()
	}
	return ingest.SitemapOptions{
		MaxPages:          c.Ingest.SitemapMaxPages,
		RequestsPerSecond: c.Ingest.FeedRequestsPerSecond,
	}
}
```

In `cmd/init.go`, extend both `defaultConfigToml` and `defaultConfigOllamaToml`'s `[ingest]` block with:

```toml
feed_request_per_second = 1.0
feed_max_entries = 50
sitemap_max_pages = 200
```

- [ ] **Step 3: Build + test**

Run: `go build ./... && go test ./...`
Expected: clean build, all green. Existing URL tests still pass — the new dispatch only activates on feed/sitemap content-types.

- [ ] **Step 4: Commit**

```bash
git -c commit.gpgsign=false commit -m "feat(ingest): content-type dispatch to feed/sitemap; --feed --sitemap --max-pages flags + config"
```

**End of Phase D.** Tree is green. `llmwiki ingest <feed-url>` and `<sitemap-url>` now work, content-type-sniffed by default and forced via flags. Per-entry rate-limiting and breadth caps are in place.

---

## Phase E — Smoke test + release engineering

### Task 14: `LLMWIKI_CASSETTE` env wiring + `make smoke` + smoke cassette

**Files:**
- Modify: `cmd/root.go`
- Modify: `Makefile`
- Create: `cmd/smoke_test.go`
- Create: `internal/ingest/testdata/smoke-source.md`
- Create: `internal/llm/testdata/cassettes/smoke__001.json` (etc — recorded)

- [ ] **Step 1: Build the smoke fixture**

Create `internal/ingest/testdata/smoke-source.md` with ~300 bytes of Markdown content describing a synthetic topic, e.g. "The Smokehouse: a fixture used by `llmwiki`'s end-to-end smoke test. Mentions the trust property, cassettes, and incremental re-ingest." The smoke test asks "what is the smoke source about?" and expects the answer to mention at least one of those terms.

- [ ] **Step 2: Wire `LLMWIKI_CASSETTE`**

In `cmd/root.go`'s `loadConfig()`, after the existing client construction, add:

```go
if name := os.Getenv("LLMWIKI_CASSETTE"); name != "" {
	dir := "internal/llm/testdata/cassettes"
	llmClient = llm.NewCassetteClient(llmClient, dir, name, llm.ModeReplay)
}
```

The cassette client already supports `LLMWIKI_RECORD=1` to flip into record mode; the env var is the simplest knob for the smoke target.

- [ ] **Step 3: Add `make smoke` target**

In `Makefile`:

```make
.PHONY: smoke
smoke: build
	@TMP=$$(mktemp -d) && \
	  cd $$TMP && \
	  $(CURDIR)/$(BINARY) init --provider=ollama && \
	  cp $(CURDIR)/internal/ingest/testdata/smoke-source.md . && \
	  LLMWIKI_CASSETTE=smoke $(CURDIR)/$(BINARY) ingest smoke-source.md && \
	  LLMWIKI_CASSETTE=smoke $(CURDIR)/$(BINARY) ask "what is the smoke source about?" --no-save && \
	  $(CURDIR)/$(BINARY) status && \
	  rm -rf $$TMP
```

Note `--provider=ollama` so the smoke run does not require an `ANTHROPIC_API_KEY` even though we will be replaying from a cassette recorded against Anthropic.

- [ ] **Step 4: Add Go-level smoke unit-test**

Create `cmd/smoke_test.go` covering the same path so `go test ./...` exercises it without spawning the binary:

```go
package cmd

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/mritunjaysharma394/llmwiki/internal/ingest"
	"github.com/mritunjaysharma394/llmwiki/internal/wiki"
)

func TestSmokeIngestThenAsk(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	files, err := ingest.ReadLocalFiles("../internal/ingest/testdata/smoke-source.md", ingest.DefaultWalkOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("got %d files, want 1", len(files))
	}
	chunks := ingest.ChunkSourceFiles(files, 16*1024)
	client := integrationClient(t, "smoke")
	pages, err := wiki.IngestSourceFilesToPages(context.Background(), client, chunks[0].Files, nil)
	if err != nil {
		t.Fatalf("IngestSourceFilesToPages: %v", err)
	}
	if len(pages) == 0 {
		t.Fatal("smoke ingest produced no pages")
	}
	answer, err := wiki.AnswerQuestion(context.Background(), client, "what is the smoke source about?", pages)
	if err != nil {
		t.Fatalf("AnswerQuestion: %v", err)
	}
	if !strings.Contains(strings.ToLower(answer), "smoke") {
		t.Errorf("answer doesn't mention 'smoke': %q", answer)
	}
	_ = os.Stderr // silence import if unused
}
```

- [ ] **Step 5: Record the cassette**

```bash
LLMWIKI_RECORD=1 go test ./cmd/ -run TestSmokeIngestThenAsk -v
```

Expected: PASS, with `internal/llm/testdata/cassettes/smoke__*.json` created.

If `ANTHROPIC_API_KEY` is unavailable at planning time, leave the cassette write as a flagged TODO step in the implementer's checklist; the Go test will skip until the cassette is present, and the Make target will fail loudly.

- [ ] **Step 6: Verify replay (no API key)**

```bash
unset ANTHROPIC_API_KEY
go test ./cmd/ -run TestSmokeIngestThenAsk -v
make smoke
```

Expected: both green.

- [ ] **Step 7: Commit**

```bash
git -c commit.gpgsign=false commit -m "feat(smoke): make smoke target, LLMWIKI_CASSETTE env, smoke cassette + fixture"
```

---

### Task 15: GoReleaser config + CI dry-run

**Files:**
- Create: `.goreleaser.yml`
- Modify: `.github/workflows/test.yml`

- [ ] **Step 1: Write the GoReleaser config**

Create `.goreleaser.yml`:

```yaml
version: 2

project_name: llmwiki

before:
  hooks:
    - go mod tidy

builds:
  - id: llmwiki
    main: ./
    binary: llmwiki
    env:
      - CGO_ENABLED=0
    goos:
      - linux
      - darwin
      - windows
    goarch:
      - amd64
      - arm64
    ignore:
      - goos: windows
        goarch: arm64
    ldflags:
      - -s -w
      - -X github.com/mritunjaysharma394/llmwiki/internal/version.Version={{.Version}}
      - -X github.com/mritunjaysharma394/llmwiki/internal/version.Commit={{.ShortCommit}}
      - -X github.com/mritunjaysharma394/llmwiki/internal/version.BuildDate={{.Date}}

archives:
  - id: default
    name_template: "{{ .ProjectName }}-{{ .Os }}-{{ .Arch }}"
    formats: ["tar.gz"]
    format_overrides:
      - goos: windows
        formats: ["zip"]
    files:
      - LICENSE
      - README.md
      - CHANGELOG.md

checksum:
  name_template: "checksums.txt"

snapshot:
  version_template: "{{ incpatch .Version }}-snapshot-{{ .ShortCommit }}"

changelog:
  use: github
  filters:
    exclude:
      - "^docs:"
      - "^test:"
      - "^chore:"

release:
  draft: false
  prerelease: auto
```

- [ ] **Step 2: Add CI dry-run**

In `.github/workflows/test.yml`, after the `Test` step, add:

```yaml
      - name: GoReleaser dry-run
        uses: goreleaser/goreleaser-action@v6
        with:
          version: latest
          args: release --snapshot --clean --skip=publish
      - name: Smoke
        run: make smoke
```

- [ ] **Step 3: Verify locally**

```bash
which goreleaser || brew install goreleaser
goreleaser release --snapshot --clean
ls dist/
```

Expected: `dist/` contains five archives (linux-amd64, linux-arm64, darwin-amd64, darwin-arm64, windows-amd64) + checksums.txt.

- [ ] **Step 4: Commit**

```bash
git -c commit.gpgsign=false commit -m "build(release): GoReleaser config (5-arch matrix, ldflags injection) + CI dry-run"
```

---

### Task 16: `LICENSE` (Apache-2.0) + `CHANGELOG.md`

**Files:**
- Create: `LICENSE`
- Create: `CHANGELOG.md`

- [ ] **Step 1: Write the LICENSE file**

Create `LICENSE` containing the full Apache License, Version 2.0 text (verbatim from https://www.apache.org/licenses/LICENSE-2.0.txt) followed by:

```
Copyright 2026 Mritunjay Sharma

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
```

The full Apache-2.0 boilerplate goes above; the standard "APPENDIX" with the boilerplate notice block is included as Apache requires.

- [ ] **Step 2: Write the CHANGELOG**

Create `CHANGELOG.md`:

```markdown
# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [1.0.0-rc.1] — 2026-05-04

### Added

- `internal/version` package with `Version`, `Commit`, `BuildDate` injected via
  `-ldflags` at release time. `llmwiki version` and `llmwiki --version` print
  semver, commit SHA, build date, Go version. `User-Agent` on outgoing HTTP
  fetches reads from the same source.
- `internal/cliutil/errors.go` with `UserError` rendered by `Execute()` as a
  three-line `Error: / cause: / try:` block. Retrofitted across `init`,
  `ingest`, `ask`, `lint` for the high-traffic failure modes.
- Feed and sitemap ingestion: `internal/ingest/feed.go` (gofeed) for
  RSS/Atom/JSON Feed, `internal/ingest/sitemap.go` (encoding/xml) for
  sitemap.xml and one-level sitemap-of-sitemaps. Polite 1-req/sec default,
  configurable caps on entries (`feed_max_entries=50`) and pages
  (`sitemap_max_pages=200`). Content-type dispatch from `url.go`; explicit
  `--feed` / `--sitemap` flags; `--max-pages` override.
- Co-resident re-chunking via a new `chunks` table (db v3). When a file's
  content changes, every neighbour that was packed in the same prior chunk is
  re-included in the new pack so cross-file synthesis stays stable on
  incremental re-ingest. `--no-rechunk` opts out for callers who accept the
  drift risk.
- `make smoke` target running the README quickstart end-to-end against a
  recorded cassette (`smoke__*.json`) — no API key needed.
- GoReleaser configuration producing `linux/amd64`, `linux/arm64`,
  `darwin/amd64`, `darwin/arm64`, `windows/amd64` archives + checksums on
  tag push. Dry-run runs in CI on every PR.
- Nightly cassette-refresh GitHub workflow (cron `17 6 * * *`) running
  `LLMWIKI_RECORD=1 go test ./...` against the project's `ANTHROPIC_API_KEY`
  secret; opens a PR if cassettes diff.
- Apache-2.0 `LICENSE`. README rewrite covering install, quickstart, common
  workflows, configuration table, trust model, privacy, architecture,
  contributing.

### Changed

- `userAgentVersion` constant in `internal/ingest/url.go` is replaced by
  `version.Version` from `internal/version`. Sites filtering on the literal
  `"llmwiki/0.1"` substring will see `"llmwiki/1.0.0-rc.1"` after this release.

## [0.2.0] — 2026-05-03 (sub-project 3)

### Added

- PDF ingest with per-page extraction and a scanned-page heuristic.
- HTTP/HTTPS URL ingest with content-type sniffing, Readability article
  extraction, html-to-markdown pipeline.
- Real GitHub-repo and local-directory walking with a built-in deny list
  (`.git`, `node_modules`, `vendor`, lockfiles, binaries), `.gitignore`
  honoring, and a configurable per-file size cap.
- Per-file evidence: every `Evidence` row is anchored to a specific
  `SourceFile` (file path or PDF page). `ask` renders sources as
  `(path/to/file.go:lines)`.
- Per-file content hashing → incremental re-ingest only re-processes files
  whose own content changed.

## [0.1.0] — 2026-05-03 (sub-project 1)

### Added

- Evidence requirement in the LLM tool schema; server-side validation that
  every quote is a verbatim substring of the source.
- `evidence` and `saved_answers` SQLite tables with FTS5 indexes.
- Streaming `ask` with TTY-aware glamour rendering.
- Auto-archive of every answer to `.llmwiki/answers/` and a row in the
  database.
- Cassette-based LLM client for record/replay testing.

[Unreleased]: https://github.com/mritunjaysharma394/llmwiki/compare/v1.0.0-rc.1...HEAD
[1.0.0-rc.1]: https://github.com/mritunjaysharma394/llmwiki/releases/tag/v1.0.0-rc.1
[0.2.0]: https://github.com/mritunjaysharma394/llmwiki/releases/tag/v0.2.0
[0.1.0]: https://github.com/mritunjaysharma394/llmwiki/releases/tag/v0.1.0
```

The 0.1.0 / 0.2.0 entries are retroactive — the project was not in fact tagged at those points. Recording them here is the launch's audit trail; we are not back-tagging old commits.

- [ ] **Step 3: Build green (no Go change here, sanity check)**

Run: `go build ./... && go test ./...`
Expected: green.

- [ ] **Step 4: Commit**

```bash
git -c commit.gpgsign=false commit -m "docs(license,changelog): Apache-2.0 LICENSE + retroactive CHANGELOG up to 1.0.0-rc.1"
```

---

### Task 17: Smoke + GoReleaser dry-run pass in CI on a fresh clone

**Files:** none (verification only)

- [ ] **Step 1: Push the branch and observe the workflow**

```bash
git push origin <branch>
gh run watch
```

Expected: `test`, `vet`, `go test ./...`, `goreleaser release --snapshot --clean --skip=publish`, and `make smoke` all green.

- [ ] **Step 2: Confirm `dist/` artifacts in the snapshot**

```bash
gh run download <run-id> -n snapshot
ls
```

Expected: five archives + checksums.

- [ ] **Step 3: No commit needed**

If anything failed, fix forward in a new task; do not re-roll commits in Phase E.

**End of Phase E.** Tree is green. The release path is provable end-to-end via snapshot, the smoke cassette runs without an API key, the LICENSE and CHANGELOG ship.

---

## Phase F — Error retrofit, README, demo asset, nightly drift

### Task 18: Retrofit `cmd/ingest.go` and `cmd/lint.go` with `UserError`

**Files:**
- Modify: `cmd/ingest.go`
- Modify: `cmd/lint.go`

The Phase B retrofit covered key-missing and empty-wiki paths. This task wraps the remaining high-traffic failure modes called out in the spec: HTTP errors during `ingest`, schema-migration errors at `loadConfig` time, and lint failures.

- [ ] **Step 1: Wrap the network error in `runIngest`**

In `cmd/ingest.go`'s `runIngest`, after the dispatch switch:

```go
if err != nil {
	if strings.Contains(err.Error(), "HTTP ") {
		return cliutil.Wrap("ingest failed",
			err,
			"check the URL is reachable in a browser; for transient 5xx errors retry the command")
	}
	if strings.Contains(err.Error(), "no extractable text") {
		return cliutil.Wrap("PDF appears to be scanned",
			err,
			"this PDF has no text layer; OCR is not supported in v1.0")
	}
	return fmt.Errorf("reading source: %w", err)
}
```

- [ ] **Step 2: Wrap migration errors in `loadConfig`**

In `cmd/root.go`:

```go
database, err = db.Open(cfg.Wiki.DBPath)
if err != nil {
	return cliutil.Wrap("opening database",
		err,
		"if the schema is newer than this binary, downgrade is not supported; back up .llmwiki/wiki.db and re-init")
}
```

- [ ] **Step 3: Wrap lint failures**

In `cmd/lint.go`, wrap the orphan-page detection and broken-link branches with `UserError` whose remediation hints at `--fix` (if implemented) or the manual remediation.

- [ ] **Step 4: Manual smoke**

```bash
unset ANTHROPIC_API_KEY
./llmwiki ingest https://does-not-exist.invalid
# Expect:
# Error: ingest failed
#   cause: ... no such host ...
#   try:   check the URL is reachable in a browser ...
```

- [ ] **Step 5: Build + test**

Run: `go build ./... && go test ./...`
Expected: clean build, all green.

- [ ] **Step 6: Commit**

```bash
git -c commit.gpgsign=false commit -m "feat(cmd): UserError retrofit on ingest network/PDF errors and db migration"
```

---

### Task 19: README rewrite

**Files:**
- Modify: `README.md`
- Create: `docs/assets/.gitkeep`

The rewrite happens late on purpose — every claim in the README must already be true and tested in the codebase before this task lands.

- [ ] **Step 1: Create the `docs/assets/` directory**

```bash
mkdir -p docs/assets && touch docs/assets/.gitkeep
```

The directory holds the demo GIF/screenshot. Task 20 marks the asset itself as a manual user step.

- [ ] **Step 2: Replace `README.md`**

Sections, top-to-bottom, matching the spec's "What users see" outline:

1. **Title + one-paragraph what-it-is.** "`llmwiki` ingests sources (files, URLs, repos, PDFs, RSS/Atom feeds, sitemaps) and synthesizes them into a Markdown wiki, with answers grounded in verbatim source quotes. Trust comes from validation: every page that ships includes evidence quotes that are byte-exact substrings of the original source — hallucinated pages are dropped before they hit disk."
2. **Demo asset.** A static image referencing `docs/assets/demo.png`. The image itself is added by the user post-plan; for the README commit we just embed the link with a `<!-- TODO: regenerate via tools/record-demo.sh -->` comment so the missing asset is visible to anyone who clones.
3. **Install.** Three real install paths:
   - `go install github.com/mritunjaysharma394/llmwiki@latest`
   - Pre-built binary downloads from GitHub Releases (per-OS curl one-liner, the same shape the spec shows).
   - `make install` from source.
4. **Quickstart.** The three-command flow from the spec.
5. **Common workflows.** Subsections for "ingest a repo", "ingest a folder of PDFs", "subscribe to a feed", "crawl a small site via sitemap".
6. **Configuration.** Markdown table listing every key in `[llm]`, `[wiki]`, `[ask]`, `[ingest]` with default and short description, plus `ANTHROPIC_API_KEY` and `LLMWIKI_CASSETTE` env vars.
7. **Trust model.** One paragraph + link to `docs/superpowers/specs/2026-05-03-trust-the-output-design.md`.
8. **Privacy.** "Anthropic gets sources during ingest; Ollama keeps everything local. `.llmwiki/` is local and `.gitignore`d by default. No telemetry, ever."
9. **Architecture.** ASCII diagram of `ingest → chunk → LLM → validate → wiki + db / ask → retrieve → LLM → render`. Link to `docs/superpowers/specs/`.
10. **Contributing.** Cassette workflow: `LLMWIKI_RECORD=1 go test ./...` re-records; bare `go test ./...` replays.
11. **License + acknowledgements.** Apache-2.0; thanks to Karpathy's wiki post and the dependency authors.

- [ ] **Step 3: Verify every command in the README runs**

For each `bash` block in the README, run it locally end-to-end. Anything that doesn't work is a spec/plan bug; fix the binary before merging the README.

- [ ] **Step 4: Build + test**

Run: `go build ./... && go test ./...`
Expected: clean build, all green (no Go change but sanity).

- [ ] **Step 5: Commit**

```bash
git -c commit.gpgsign=false commit -m "docs(readme): full launch rewrite — install, quickstart, workflows, trust, privacy"
```

---

### Task 20: Placeholder demo asset (TODO marker)

**Files:** none committed by this task

The actual GIF/screenshot is a manual step the user performs. This task documents the expectation in a TODO and verifies the placeholder reference in the README does not break rendering.

- [ ] **Step 1: Acknowledge the TODO**

The README references `docs/assets/demo.png`. Until the user records and commits that file:

- The README renders with a broken-image marker (acceptable for v1.0-rc.1).
- `docs/assets/.gitkeep` keeps the directory in git so the path resolves once the asset lands.

- [ ] **Step 2: Verify no plan/spec drift**

Read the README's image reference, confirm it matches `docs/assets/demo.png`. No commit.

- [ ] **Step 3: No commit**

This task exists only to call out the manual user step. The implementer should not invent a placeholder PNG or commit empty bytes — leave the file truly missing so the user is reminded to record one.

---

### Task 21: Nightly cassette-refresh workflow

**Files:**
- Create: `.github/workflows/cassette-refresh.yml`

- [ ] **Step 1: Write the workflow**

Create `.github/workflows/cassette-refresh.yml`:

```yaml
name: cassette-refresh

on:
  schedule:
    - cron: "17 6 * * *"
  workflow_dispatch:

permissions:
  contents: write
  pull-requests: write

jobs:
  refresh:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: "1.26"
      - name: Re-record cassettes
        env:
          ANTHROPIC_API_KEY: ${{ secrets.ANTHROPIC_API_KEY }}
          LLMWIKI_RECORD: "1"
        run: go test ./...
      - name: Open PR if cassettes drifted
        uses: peter-evans/create-pull-request@v6
        with:
          title: "chore: nightly cassette refresh"
          branch: cassette-refresh-${{ github.run_id }}
          commit-message: "chore: nightly cassette refresh"
          body: |
            Automated nightly cassette refresh. If this PR has a non-empty diff,
            upstream Anthropic API output drifted overnight. Review the diff
            against the spec's expected behavior; merge if intentional, close
            otherwise.
          add-paths: |
            internal/llm/testdata/cassettes/**
```

- [ ] **Step 2: Verify the workflow parses**

```bash
gh workflow view cassette-refresh
# (or push and observe; do not run the cron-only path manually here)
```

- [ ] **Step 3: Commit**

```bash
git -c commit.gpgsign=false commit -m "ci: nightly cassette-refresh workflow with auto-PR on drift"
```

---

## Phase G — Final verification + rc tag

### Task 22: Whole-repo verification matrix

**Files:** none (verification only)

Mirrors the spec's "Verification" block.

- [ ] **Step 1: Version output**

```bash
go build -o ./llmwiki .
./llmwiki version
./llmwiki --version
# Expect: same single line, "llmwiki (devel) ..." or "llmwiki 1.0.0-rc.1 ..." after tagging.
```

- [ ] **Step 2: User-Agent**

```bash
go run . ingest https://example.com/ 2>&1 | head -20
# Or: serve a local httptest probe; verify the UA header reads "llmwiki/<version>".
```

- [ ] **Step 3: Feed ingestion**

```bash
./llmwiki ingest --feed https://blog.golang.org/feed.atom
# Expect: per-entry SourceFiles, evidence anchored to entry URLs, ~1 req/sec.
```

- [ ] **Step 4: Sitemap ingestion**

```bash
./llmwiki ingest --sitemap https://example.org/sitemap.xml
# Expect: bounded crawl honoring sitemap_max_pages, evidence anchored.
```

- [ ] **Step 5: Re-chunking**

```bash
echo "// extra content" >> ./internal/ingest/local.go
./llmwiki ingest ./internal/
# Expect: not just local.go; co-resident files in the same chunk are also
# re-LLM'd. Confirm via the "Packing into N chunks" log line and ✓ summary.

./llmwiki ingest --no-rechunk ./internal/
# Expect: only local.go re-processed (sub-project 3 behaviour).
```

- [ ] **Step 6: Polished errors**

```bash
./llmwiki ingest https://does-not-exist.invalid/feed
# Expect: 3-line block.

unset ANTHROPIC_API_KEY
./llmwiki ingest ./README.md
# Expect: structured error pointing at console.anthropic.com.
```

- [ ] **Step 7: Smoke**

```bash
make smoke
# Expect: green.
```

- [ ] **Step 8: `go install` from a fresh `$GOPATH`**

```bash
GOBIN=$(mktemp -d) GOPATH=$(mktemp -d) go install github.com/mritunjaysharma394/llmwiki@<commit-or-rc-tag>
$GOBIN/llmwiki version
# Expect: matching version line. This is the real fresh-machine test.
```

- [ ] **Step 9: GoReleaser snapshot**

```bash
goreleaser release --snapshot --clean
ls dist/
# Expect: linux-amd64/arm64, darwin-amd64/arm64, windows-amd64 archives + checksums.
```

- [ ] **Step 10: Whole test suite**

```bash
go test ./...
# Expect: green in replay mode.
```

- [ ] **Step 11: Nightly drift dry-run**

```bash
gh workflow run cassette-refresh.yml
# Expect: clean run, no PR (or one PR with explainable diff).
```

- [ ] **Step 12: No commit needed**

If any check failed, fix forward in a new task; do not re-roll Phase G.

---

### Task 23: Tag `v1.0.0-rc.1`

**Files:** none — tag only

- [ ] **Step 1: Confirm the working tree is clean**

```bash
git status
# Expect: "nothing to commit, working tree clean"
```

- [ ] **Step 2: Tag**

```bash
git tag -a v1.0.0-rc.1 -m "v1.0.0-rc.1: launch candidate (sub-project 4)"
```

- [ ] **Step 3: Do not push**

The tag stays local until the user is ready. Real `v1.0.0` is post-stability-window follow-up, out of scope here.

- [ ] **Step 4: No commit**

The tag is the artifact. The CHANGELOG entry already captures the release notes.

---

## Done criteria

- `llmwiki version` prints `llmwiki <semver> (commit <sha>, built <date>, go<version>)` from `internal/version` vars injected by GoReleaser.
- `User-Agent: llmwiki/<version>` on every outgoing HTTP fetch.
- Feed ingest (RSS/Atom/JSON Feed) and sitemap crawl (one-level sitemap-of-sitemaps) work end-to-end with polite rate-limiting and breadth caps; content-type sniffing routes them automatically.
- Re-chunking on file-boundary changes promotes co-resident files into the next pack. `--no-rechunk` opts out.
- Every operator-facing failure mode listed in the spec prints a 3-line `Error: / cause: / try:` block via `cliutil.UserError`.
- `make smoke` runs the README quickstart end-to-end against a recorded cassette without an API key.
- `goreleaser release --snapshot --clean` produces five archive variants + checksums; the same path is exercised in CI on every PR.
- Apache-2.0 `LICENSE` and Keep-a-Changelog `CHANGELOG.md` are at the repo root.
- README is the launch page, and every command it shows works.
- Nightly cassette-refresh workflow is scheduled; `LLMWIKI_RECORD=1 go test ./...` re-records on demand.
- `v1.0.0-rc.1` tag exists locally. `go install github.com/mritunjaysharma394/llmwiki@v1.0.0-rc.1` works on a fresh `$GOPATH`.

When all 23 tasks above are complete and Phase G's verification block is green, sub-project 4 is done. Real `v1.0.0` waits on a stability window of real-world use; that promotion is post-launch follow-up, out of scope for this plan.
