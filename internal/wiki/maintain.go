// Package wiki — maintain.go
//
// RunMaintenance is the sub-project 8 Phase D umbrella that composes
// the existing primitives into one cron-friendly entrypoint:
//
//   - refresh-stale: walk every source row, compare its on-record
//     content_hash to wiki.CurrentSourceHash(uri); for each row whose
//     remote/local bytes drifted, force-reingest the source via
//     IngestSource with IngestOptions.Force = true. Network errors are
//     logged-and-skipped (per source) — one unreachable URL must not
//     abort the whole maintenance pass.
//
//   - lint: full lint surface — orphan / missing-xref / schema-drift
//     via wiki.FastLint, plus the per-batch contradiction LLM call
//     mirroring cmd/lint's batchSize=10 loop. The staleness signal is
//     consumed from the refresh-stale step (when both run in the same
//     RunMaintenance call) so we don't redo the network walk twice.
//
//   - promote-pending: sweep <answersDir>/*.md, parse each saved
//     answer, run EvaluateAutoPromote (the four-signal Phase A gate),
//     and on pass call PromoteAnswer with Source="auto". Files that
//     fail the gate stay in place (existing trust property: never
//     silently drop). Files that pass the gate but fail the validator
//     also stay; we surface a `→ promote_failed: <file> (validator)`
//     line per plan §Six design calls #2.
//
// Phase E will share this contract — the watch loop and capture-session
// hook will call RunMaintenance directly with subset opts (e.g. only
// PromotePending=true). The struct shape is the public contract.
//
// Trust property: nothing here bypasses ValidateAndAttachEvidence. The
// refresh-stale path goes through IngestSource (which already gates
// every page write); the promote-pending path goes through PromoteAnswer
// (which re-validates evidence quotes against current on-disk source
// bytes). DryRun=true skips every mutation: no IngestSource call, no
// PromoteAnswer call, no log.md append. The result struct is populated
// with what *would* have happened so the caller can render a preview.
package wiki

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mritunjaysharma394/llmwiki/internal/db"
	"github.com/mritunjaysharma394/llmwiki/internal/llm"
	"github.com/mritunjaysharma394/llmwiki/internal/schema"
)

// MaintainOpts is the per-call knob set RunMaintenance reads. The
// bool fields are step toggles; opts with all three step bools = false
// is a no-op (returns a zero-valued result and nil error). The cobra
// wrapper in cmd/maintain.go translates --lint / --refresh-stale /
// --promote-pending / --dry-run into this struct.
//
// Pass-through fields:
//   - AnswersDir is the directory promote-pending sweeps for *.md.
//     Empty string falls back to filepath.Join(filepath.Dir(IngestCfg.WikiDir), "answers")
//     — the same convention cmd/ask uses. Phase E (watch) may pass an
//     explicit override.
//   - IngestCfg is forwarded to IngestSource for the force-reingest
//     leg of refresh-stale and to PromoteAnswer for promote-pending.
//   - AutoPromoteCfg is forwarded to EvaluateAutoPromote; the cmd
//     wrapper builds it from cfg.Ask.
//   - Schema is the active schema; threaded into IngestOptions.Schema,
//     PromoteOptions.Schema, FastLint, and DetectContradictions.
type MaintainOpts struct {
	RefreshStale   bool
	Lint           bool
	PromotePending bool
	DryRun         bool

	AnswersDir     string
	IngestCfg      IngestSourceConfig
	AutoPromoteCfg AutoPromoteConfig
	Schema         schema.Schema

	// Logger receives human-readable progress lines. nil → io.Discard.
	// The cobra wrapper passes os.Stdout; tests pass a captured buffer.
	Logger io.Writer
}

// MaintainResult carries the structured outcome of one RunMaintenance
// invocation. The cobra wrapper renders these into the user-facing
// summary; Phase E's watcher / session-capture surface the same struct
// without re-rendering. Errors lists per-source / per-answer failures
// that were logged-and-skipped (not aborted on); cmd/maintain decides
// whether the whole command exits non-zero based on len(Errors) plus
// the structural-error return.
type MaintainResult struct {
	// refresh-stale
	StaleSourcesChecked int
	StaleRefetched      int
	RefetchErrors       int

	// lint
	ContradictionsFound int
	SchemaDriftPages    int
	FastLint            FastLintResult

	// promote-pending
	PromotePendingTotal           int
	PromotePendingPromoted        int
	PromotePendingValidatorFailed int
	PromotePendingGateFailed      int

	// Errors collects per-step recoverable failures (one network
	// error, one promote that crashed). Non-fatal — RunMaintenance
	// continues and reports them via this slice. The cobra wrapper
	// uses len(Errors) > 0 as the "actual error" exit-code signal,
	// distinct from cosmetic findings.
	Errors []string
}

// runMaintenanceIngestFn is the package-level seam tests swap to
// avoid driving a full IngestSource call (which would need a real
// LLM client + cassette). Production calls IngestSource.
var runMaintenanceIngestFn = IngestSource

// runMaintenancePromoteFn is the package-level seam tests swap to
// avoid driving a full PromoteAnswer call. Production calls
// PromoteAnswer.
var runMaintenancePromoteFn = PromoteAnswer

// RunMaintenance composes refresh-stale + lint + promote-pending. See
// the package-level comment for the contract; this function is the
// implementation glue between the existing primitives.
//
// The function never returns a non-nil error for individual per-source
// or per-answer failures — those land in MaintainResult.Errors so the
// caller can decide exit-code policy. The non-nil error return is
// reserved for structural failures (DB unreachable, AnswersDir is a
// file rather than a directory, etc.).
func RunMaintenance(ctx context.Context, cfg IngestSourceConfig, database *db.DB, client llm.Client, sch schema.Schema, opts MaintainOpts) (MaintainResult, error) {
	var res MaintainResult
	logger := opts.Logger
	if logger == nil {
		logger = io.Discard
	}
	logf := func(format string, args ...any) { fmt.Fprintf(logger, format, args...) }

	// Resolve answers dir. Phase A's cmd/ask convention is
	// <wiki-parent>/answers/. Fall back when AnswersDir is empty.
	answersDir := opts.AnswersDir
	if answersDir == "" && cfg.WikiDir != "" {
		answersDir = filepath.Join(filepath.Dir(cfg.WikiDir), "answers")
	}

	// === refresh-stale =================================================
	// Tracks which source URIs were re-fetched so the lint step can
	// avoid redoing the same network walk in the same call.
	refetched := map[string]bool{}
	if opts.RefreshStale {
		sources, err := database.GetAllSources()
		if err != nil {
			return res, fmt.Errorf("loading sources: %w", err)
		}
		res.StaleSourcesChecked = len(sources)
		// Determinism: sort by URI so log output is byte-stable across
		// runs over the same DB.
		sort.SliceStable(sources, func(i, j int) bool { return sources[i].URI < sources[j].URI })
		for _, s := range sources {
			cur, err := CurrentSourceHash(s.URI)
			if err != nil {
				logf("  WARN cannot check %s: %v\n", s.URI, err)
				res.RefetchErrors++
				res.Errors = append(res.Errors, fmt.Sprintf("refresh-stale %s: %v", s.URI, err))
				continue
			}
			if cur == s.ContentHash {
				continue
			}
			logf("  STALE: %s\n", s.URI)
			if opts.DryRun {
				// Count it as if it would have refreshed; the cmd wrapper
				// renders "would refresh: N sources".
				res.StaleRefetched++
				refetched[s.URI] = true
				continue
			}
			ingestOpts := IngestOptions{
				Force:                                true,
				Schema:                               sch,
				UpdateExisting:                       true,
				UpdateExistingMaxCandidatesPerSource: 0,
				UpdateExistingMaxCandidatesTotal:     0,
				UpdateExistingQuoteFloor:             0,
				Logger:                               io.Discard,
			}
			if _, err := runMaintenanceIngestFn(ctx, cfg, database, client, s.URI, ingestOpts); err != nil {
				logf("  WARN re-ingest %s failed: %v\n", s.URI, err)
				res.RefetchErrors++
				res.Errors = append(res.Errors, fmt.Sprintf("refresh-stale %s: %v", s.URI, err))
				continue
			}
			logf("  ✓ refreshed %s\n", s.URI)
			res.StaleRefetched++
			refetched[s.URI] = true
		}
	}

	// === lint ===========================================================
	if opts.Lint {
		// 1. FastLint — orphans, missing-xrefs, schema-drift count. The
		//    schema-drift count it produces is the canonical one; we
		//    surface it both at the top level (SchemaDriftPages) and in
		//    res.FastLint for callers that want the first-three-titles
		//    payload.
		fl, err := FastLint(database, sch)
		if err != nil {
			logf("  WARN fast lint: %v\n", err)
			res.Errors = append(res.Errors, fmt.Sprintf("lint fast: %v", err))
		} else {
			res.FastLint = fl
			res.SchemaDriftPages = fl.SchemaDriftCount
		}

		// 2. Contradictions — same batch-of-10 loop as cmd/lint. Each
		//    batch is one LLM call; we count "**Contradiction**" occurrences
		//    plus the simpler "Possible contradiction" / "vs" hint as a
		//    fallback so the response shape from older providers still
		//    contributes a numeric.
		records, err := database.AllPages()
		if err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("lint pages: %v", err))
		} else if len(records) >= 2 {
			var pages []Page
			for _, r := range records {
				pages = append(pages, Page{Title: r.Title, Body: r.Body})
			}
			const batchSize = 10
			for i := 0; i < len(pages); i += batchSize {
				end := i + batchSize
				if end > len(pages) {
					end = len(pages)
				}
				batch := pages[i:end]
				out, derr := DetectContradictions(ctx, client, batch, sch)
				if derr != nil {
					logf("  WARN contradiction batch %d-%d: %v\n", i+1, end, derr)
					res.Errors = append(res.Errors, fmt.Sprintf("lint contradictions: %v", derr))
					continue
				}
				res.ContradictionsFound += countContradictions(out)
			}
		}
	}

	// === promote-pending ==============================================
	if opts.PromotePending {
		if answersDir == "" {
			// No wiki dir → no answers dir → silently no-op. cmd-side
			// loadConfig guarantees WikiDir is non-empty for production
			// invocations; this branch only fires on bare-metal tests
			// that pass an empty IngestCfg.
		} else {
			entries, err := os.ReadDir(answersDir)
			if err != nil {
				if !errors.Is(err, os.ErrNotExist) {
					res.Errors = append(res.Errors, fmt.Sprintf("promote-pending readdir %s: %v", answersDir, err))
				}
			} else {
				// Sort for determinism (cmd output is greppable).
				files := make([]string, 0, len(entries))
				for _, e := range entries {
					if e.IsDir() {
						continue
					}
					if !strings.HasSuffix(e.Name(), ".md") {
						continue
					}
					files = append(files, e.Name())
				}
				sort.Strings(files)
				for _, name := range files {
					full := filepath.Join(answersDir, name)
					raw, err := os.ReadFile(full)
					if err != nil {
						res.Errors = append(res.Errors, fmt.Sprintf("promote-pending read %s: %v", full, err))
						continue
					}
					parsed, err := ParseSavedAnswer(string(raw))
					if err != nil {
						// Malformed answer file — skip and keep going.
						logf("  skip %s (parse: %v)\n", name, err)
						continue
					}
					res.PromotePendingTotal++
					verdict, reason := EvaluateAutoPromote(parsed, database, opts.AutoPromoteCfg)
					if !verdict.AutoPromote {
						logf("  → skip %s (%s)\n", name, reason)
						res.PromotePendingGateFailed++
						continue
					}
					if opts.DryRun {
						// Would promote; don't actually call PromoteAnswer.
						logf("  → would promote %s\n", name)
						res.PromotePendingPromoted++
						continue
					}
					_, perr := runMaintenancePromoteFn(ctx, cfg, database, client, full, PromoteOptions{
						Schema: sch,
						Source: "auto",
					})
					switch {
					case perr == nil:
						logf("  → filed %s\n", name)
						res.PromotePendingPromoted++
					case errors.Is(perr, ErrEvidenceInvalid):
						// Plan §6 design call 2: validator-fail → file
						// stays in place; surface a `promote_failed`
						// line so the user can find it in the next
						// morning's status output.
						logf("  → promote_failed: %s (validator)\n", name)
						res.PromotePendingValidatorFailed++
					case errors.Is(perr, ErrTitleExists):
						// Title collision is a soft fail — the answer
						// already has a permanent home under a different
						// path. Treat as gate-failed (the heuristic gate
						// would have caught this if the FTS skip-score
						// were tuned), file stays in place.
						logf("  → skip %s (title exists)\n", name)
						res.PromotePendingGateFailed++
					default:
						res.Errors = append(res.Errors, fmt.Sprintf("promote-pending %s: %v", name, perr))
						logf("  WARN promote %s: %v\n", name, perr)
					}
				}
			}
		}
	}

	return res, nil
}

// countContradictions tallies likely contradiction mentions in a
// DetectContradictions response string. Plan §Phase D Task 2 wording:
// "count of '**Contradiction**' lines or similar". DetectContradictions's
// system prompt asks the LLM to phrase findings as "Page A vs Page B:
// <description>" or "No contradictions found." — we count occurrences
// of " vs " on non-empty lines that are NOT the "no contradictions"
// negation, with the explicit "**Contradiction**" Markdown bold
// fallback for providers that prefer that shape.
//
// This is best-effort; the raw text is also surfaced to the user via
// the lint print path. The numeric is for status / scripting only.
func countContradictions(s string) int {
	if s == "" {
		return 0
	}
	low := strings.ToLower(s)
	if strings.Contains(low, "no contradictions found") || strings.Contains(low, "no contradictions detected") {
		return 0
	}
	// Prefer the explicit "**Contradiction**" markdown bold form when
	// the LLM uses it; that's the strongest signal.
	if n := strings.Count(s, "**Contradiction**"); n > 0 {
		return n
	}
	// Fallback: count "Page A vs Page B" structure. One per non-empty
	// line containing " vs " (case-insensitive).
	var n int
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if strings.Contains(strings.ToLower(line), " vs ") {
			n++
		}
	}
	return n
}
