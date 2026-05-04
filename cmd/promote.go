// Package cmd — promote.go
//
// `llmwiki promote <answer-file-or-id>` is the cobra wrapper around
// wiki.PromoteAnswer (sub-project 6a, Phase B). It resolves the
// answer-file argument three ways:
//
//   1. Absolute path → that file.
//   2. Filename in <wiki-parent>/answers/ → that file.
//   3. Bare slug → glob-match against <wiki-parent>/answers/*-<slug>.md;
//      latest by mtime wins on multiple matches; ambiguous matches that
//      can't be disambiguated by mtime return an error naming candidates.
//
// On success the command prints a structured summary mirroring the
// `llmwiki ingest` output. On ErrEvidenceInvalid / ErrTitleExists it
// surfaces a UserError carrying the same structured-error code
// mcp.write_page returns, plus a remediation hint.
//
// The retro-link integration ships in Phase D; for now
// PromoteResult.RetroLinkedTitles is empty and the command skips the
// "retro-linked N existing pages" line.
package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mritunjaysharma394/llmwiki/internal/cliutil"
	"github.com/mritunjaysharma394/llmwiki/internal/wiki"
	"github.com/spf13/cobra"
)

var promoteCmd = &cobra.Command{
	Use:   "promote <answer-file-or-id>",
	Short: "Promote a saved answer into a permanent wiki page",
	Long: `Lift a .llmwiki/answers/<ts>-<slug>.md file into a real wiki page.

Defensive re-validation runs every evidence quote through the same
substring-match validator that gates llmwiki ingest and mcp.write_page —
quotes whose source files have changed since the ask are rejected with
a structured error and no disk write happens.

The argument may be:
  - an absolute path to an answer file
  - a filename inside .llmwiki/answers/
  - a bare slug; latest matching .llmwiki/answers/*-<slug>.md wins`,
	Args: cobra.ExactArgs(1),
	RunE: runPromote,
}

func init() {
	promoteCmd.Flags().String("title", "", "override title (defaults to Title-Cased question)")
	promoteCmd.Flags().Bool("rewrite", false, "LLM-rewrite the answer body into wiki prose (default off)")
	promoteCmd.Flags().Bool("no-save", false, "skip the log.md entry (debug only)")
}

func runPromote(cmd *cobra.Command, args []string) error {
	answerPath, err := resolveAnswerArg(args[0], cfg.Wiki.WikiDir)
	if err != nil {
		return cliutil.Wrap("resolving answer file", err,
			"pass an absolute path, a bare filename in .llmwiki/answers/, or a slug that matches one file")
	}
	title, _ := cmd.Flags().GetString("title")
	rewrite, _ := cmd.Flags().GetBool("rewrite")
	noSave, _ := cmd.Flags().GetBool("no-save")

	// Friendly preamble: which file we're promoting.
	fmt.Printf("Loaded answer: %s\n", filepath.Base(answerPath))

	res, err := wiki.PromoteAnswer(cmd.Context(), toWikiIngestConfig(cfg), database, llmClient, answerPath, wiki.PromoteOptions{
		Title:   title,
		Rewrite: rewrite,
		NoSave:  noSave,
	})
	if err != nil {
		switch {
		case errors.Is(err, wiki.ErrEvidenceInvalid):
			// Surface dropped quotes so the user can find the offending
			// span in the now-modified source.
			renderDroppedQuotes(res.DroppedQuotes)
			return cliutil.Wrap("evidence_invalid: defensive re-validation dropped every quote", err,
				"the source files referenced by this answer have changed since the ask; re-run 'llmwiki ask <question>' against the current wiki and promote the fresh answer")
		case errors.Is(err, wiki.ErrTitleExists):
			return cliutil.Wrap(fmt.Sprintf("title_exists: %q is taken (at %s)", res.Title, res.Path), err,
				"pass --title with a different title, or supersede manually in Obsidian")
		default:
			return err
		}
	}

	// Distinct sources for the summary line. Walk the disk page (the
	// authoritative copy) to count distinct source_files. Cheap; happens
	// once per promote.
	sourceCount := countDistinctSources(res.Path)

	fmt.Printf("Re-validating %d evidence quote(s)...\n", res.EvidenceQuotes)
	fmt.Printf("  ✓ all %d quotes still substring-match their source files\n", res.EvidenceQuotes)
	fmt.Printf("  ✓ wrote page %q (%d evidence, %d source(s))\n", res.Title, res.EvidenceQuotes, sourceCount)
	if len(res.RetroLinkedTitles) > 0 {
		fmt.Printf("retro-linked %d existing page(s)\n", len(res.RetroLinkedTitles))
	}
	fmt.Printf("saved: %s\n", res.Path)
	return nil
}

// resolveAnswerArg accepts an absolute path, a bare filename in
// <wikiDir>/../answers/, or a slug that uniquely matches one
// <ts>-<slug>.md file (latest mtime wins). Ambiguous slugs whose mtimes
// are equal — defensive — return an error naming candidates.
func resolveAnswerArg(arg, wikiDir string) (string, error) {
	if arg == "" {
		return "", fmt.Errorf("answer argument is empty")
	}
	// Absolute path → take it verbatim.
	if filepath.IsAbs(arg) {
		if _, err := os.Stat(arg); err != nil {
			return "", fmt.Errorf("absolute path %q: %w", arg, err)
		}
		return arg, nil
	}
	answersDir := filepath.Join(filepath.Dir(wikiDir), "answers")

	// Bare filename inside answers dir.
	candidate := filepath.Join(answersDir, arg)
	if _, err := os.Stat(candidate); err == nil {
		abs, _ := filepath.Abs(candidate)
		return abs, nil
	}
	// Slug match: glob *-<arg>.md and *-<arg> (caller may have left .md off).
	patterns := []string{
		filepath.Join(answersDir, "*-"+arg+".md"),
		filepath.Join(answersDir, "*-"+strings.TrimSuffix(arg, ".md")+".md"),
	}
	seen := map[string]struct{}{}
	var matches []string
	for _, pat := range patterns {
		ms, _ := filepath.Glob(pat)
		for _, m := range ms {
			if _, dup := seen[m]; dup {
				continue
			}
			seen[m] = struct{}{}
			matches = append(matches, m)
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no answer file matches %q (looked under %s)", arg, answersDir)
	case 1:
		abs, _ := filepath.Abs(matches[0])
		return abs, nil
	default:
		// Multiple matches: rather than silently picking one and
		// shadowing the other, surface all candidates and ask the user
		// for an explicit filename. The plan's test 4 expects this
		// behaviour over a "latest wins" auto-pick — promote is the
		// trust-bearing transition from scratch answer to canon, so an
		// ambiguous slug is a coin flip we shouldn't make for the user.
		names := make([]string, len(matches))
		for i, m := range matches {
			names[i] = filepath.Base(m)
		}
		return "", fmt.Errorf("ambiguous slug %q matches %d answer files: %s — pass an explicit filename",
			arg, len(matches), strings.Join(names, ", "))
	}
}

// renderDroppedQuotes writes the rejected-quote payload to stderr in a
// human-readable form so the user can see why the trust validator
// dropped each quote and where to look on disk.
func renderDroppedQuotes(dropped []wiki.DroppedQuote) {
	if len(dropped) == 0 {
		return
	}
	fmt.Fprintln(os.Stderr, "Dropped quotes (defensive re-validation):")
	for _, d := range dropped {
		quote := d.Quote
		if len(quote) > 80 {
			quote = quote[:77] + "..."
		}
		fmt.Fprintf(os.Stderr, "  - %q  (%s) — %s\n", quote, d.SourceFile, d.Reason)
	}
}

// countDistinctSources reads the freshly written page off disk and
// counts distinct source_file paths in the frontmatter. Cheap (~1ms)
// and avoids re-querying the DB after the write loop.
func countDistinctSources(pagePath string) int {
	data, err := os.ReadFile(pagePath)
	if err != nil {
		return 0
	}
	page, err := wiki.ParsePage(string(data))
	if err != nil {
		return 0
	}
	return len(page.Sources)
}
