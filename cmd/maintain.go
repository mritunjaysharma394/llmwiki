// Package cmd — maintain.go
//
// `llmwiki maintain` is the sub-project 8 Phase D umbrella that
// composes refresh-stale, lint, and promote-pending into one
// cron-friendly entrypoint per plan §"Six design calls #3".
//
// Surface:
//
//	llmwiki maintain                      # bare = --refresh-stale --lint --promote-pending
//	llmwiki maintain --lint               # only lint
//	llmwiki maintain --refresh-stale      # only refresh stale URL/file sources
//	llmwiki maintain --promote-pending    # only sweep .llmwiki/answers/ for missed auto-promotes
//	llmwiki maintain --dry-run            # composable; print what would happen, write nothing
//
// Bare invocation runs all three steps. Any explicit step flag flips
// the dispatch to "subset override" — the user gets only what they
// passed. `--dry-run` is composable with any subset.
//
// Exit-code policy (plan §3): exit non-zero ONLY when an actual error
// occurred (network failure during refresh-stale, promote that crashed,
// DB error). Cosmetic findings — orphans, contradictions, schema drift
// — exit 0. The wrapped wiki.RunMaintenance returns its per-source /
// per-answer recoverable failures via res.Errors; we sum those (plus
// the structural-error return) into the exit decision.
package cmd

import (
	"errors"
	"fmt"
	"os"

	"github.com/mritunjaysharma394/llmwiki/internal/cliutil"
	"github.com/mritunjaysharma394/llmwiki/internal/wiki"
	"github.com/spf13/cobra"
)

var maintainCmd = &cobra.Command{
	Use:   "maintain",
	Short: "Run maintenance: refresh stale sources, lint, promote pending answers",
	Long: `Run maintenance steps over the wiki:

  - refresh-stale:   re-fetch sources whose bytes have changed since ingest
  - lint:            orphan / missing-xref / schema-drift / contradiction scan
  - promote-pending: sweep .llmwiki/answers/ for missed auto-promotes

Bare invocation runs all three. Pass any of --lint / --refresh-stale /
--promote-pending to run only that subset. --dry-run composes with all
of the above; with it set, no DB row is written, no wiki page is
written, no log entry is appended.

Exit code is non-zero only when an actual error occurred (network
failure during refresh-stale, a promote that crashed, a DB error).
Cosmetic findings — orphans, contradictions, schema drift — exit 0.`,
	RunE: runMaintain,
}

func init() {
	maintainCmd.Flags().Bool("lint", false, "run lint pass (orphans, contradictions, schema drift)")
	maintainCmd.Flags().Bool("refresh-stale", false, "re-fetch sources whose bytes drifted since ingest")
	maintainCmd.Flags().Bool("promote-pending", false, "sweep .llmwiki/answers/ for missed auto-promotes")
	maintainCmd.Flags().Bool("dry-run", false, "show what would happen; write nothing")
}

// runMaintain translates flags → wiki.MaintainOpts and renders the
// MaintainResult into the user-facing summary. Exit-code policy lives
// here (cobra's RunE-returns-error → non-zero exit).
func runMaintain(cmd *cobra.Command, args []string) error {
	lintFlag, _ := cmd.Flags().GetBool("lint")
	refreshFlag, _ := cmd.Flags().GetBool("refresh-stale")
	promoteFlag, _ := cmd.Flags().GetBool("promote-pending")
	dryRun, _ := cmd.Flags().GetBool("dry-run")

	// Bare invocation = all three. Any explicit step flag = subset.
	anyExplicit := lintFlag || refreshFlag || promoteFlag
	if !anyExplicit {
		lintFlag, refreshFlag, promoteFlag = true, true, true
	}

	opts := wiki.MaintainOpts{
		Lint:           lintFlag,
		RefreshStale:   refreshFlag,
		PromotePending: promoteFlag,
		DryRun:         dryRun,
		IngestCfg:      toWikiIngestConfig(cfg),
		AutoPromoteCfg: wiki.AutoPromoteConfig{
			HedgingPhrases: cfg.Ask.AutoPromoteHedgingPhrases,
			SkipScore:      cfg.Ask.AutoPromoteSkipScore,
			ScoreFloor:     cfg.Ask.AutoPromoteScoreFloor,
		},
		Schema: activeSchema,
		Logger: os.Stdout,
	}

	if dryRun {
		fmt.Println("=== maintain (dry run) ===")
	} else {
		fmt.Println("=== maintain ===")
	}
	if refreshFlag {
		fmt.Println("→ refresh-stale: walking sources...")
	}
	if lintFlag {
		fmt.Println("→ lint: scanning pages...")
	}
	if promoteFlag {
		fmt.Println("→ promote-pending: sweeping .llmwiki/answers/...")
	}

	res, err := wiki.RunMaintenance(cmd.Context(), toWikiIngestConfig(cfg), database, llmClient, activeSchema, opts)
	if err != nil {
		return cliutil.Wrap("maintain failed",
			err,
			"a structural failure (DB, filesystem) aborted the run; see error message for the cause")
	}

	renderMaintainSummary(res, dryRun, lintFlag, refreshFlag, promoteFlag)

	// Exit-code policy: non-zero only on actual errors per plan §3.
	if len(res.Errors) > 0 {
		return cliutil.Wrap(
			fmt.Sprintf("maintain finished with %d recoverable error(s)", len(res.Errors)),
			errors.New(joinErrorLines(res.Errors)),
			"see the per-line errors above; cron jobs may want to alert on this exit code",
		)
	}
	return nil
}

// renderMaintainSummary writes the human-readable per-step summary to
// stdout. Output mirrors the existing cmd/lint phrasing for the lint
// step so users who already grep `llmwiki lint` output keep their
// habits. Numeric columns are picked to be greppable from cron logs.
func renderMaintainSummary(res wiki.MaintainResult, dryRun, lint, refresh, promote bool) {
	fmt.Println()
	if refresh {
		verb := "refreshed"
		if dryRun {
			verb = "would refresh"
		}
		fmt.Printf("refresh-stale: %d/%d sources stale; %s %d; %d errors\n",
			res.StaleRefetched, res.StaleSourcesChecked, verb, res.StaleRefetched, res.RefetchErrors)
	}
	if lint {
		fmt.Printf("lint: orphans=%d missing_xrefs=%d schema_drift=%d contradictions=%d\n",
			res.FastLint.OrphanCount, res.FastLint.MissingXRefCount, res.SchemaDriftPages, res.ContradictionsFound)
		if len(res.FastLint.TopOrphanTitles) > 0 {
			fmt.Printf("  orphan examples: %v\n", res.FastLint.TopOrphanTitles)
		}
	}
	if promote {
		verb := "promoted"
		if dryRun {
			verb = "would promote"
		}
		fmt.Printf("promote-pending: %d total; %s %d; gate_failed=%d validator_failed=%d\n",
			res.PromotePendingTotal, verb,
			res.PromotePendingPromoted,
			res.PromotePendingGateFailed,
			res.PromotePendingValidatorFailed)
	}
}

// joinErrorLines turns a []string of error reasons into a single
// newline-separated block, suitable for the cliutil.Wrap cause.
func joinErrorLines(errs []string) string {
	if len(errs) == 0 {
		return ""
	}
	out := errs[0]
	for _, e := range errs[1:] {
		out += "\n" + e
	}
	return out
}
