package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Print wiki statistics",
	RunE:  runStatus,
}

func runStatus(cmd *cobra.Command, args []string) error {
	stats, err := database.GetStats()
	if err != nil {
		return fmt.Errorf("get stats: %w", err)
	}
	fmt.Printf("pages:              %d\n", stats.TotalPages)
	fmt.Printf("sources:            %d\n", stats.TotalSources)
	fmt.Printf("total source files: %d\n", stats.TotalSourceFiles)
	fmt.Printf("evidence quotes:    %d\n", stats.EvidenceQuotes)
	fmt.Printf("legacy pages:       %d  (run 'llmwiki ingest' on the original sources to upgrade)\n", stats.LegacyPages)
	fmt.Printf("saved answers:      %d\n", stats.SavedAnswers)
	if !stats.LastIngest.IsZero() {
		fmt.Printf("last ingest:        %s\n", stats.LastIngest.Format("2006-01-02 15:04:05 MST"))
	}
	if counts, err := database.CountPageUpdateLogByOutcome(); err != nil {
		fmt.Fprintf(os.Stderr, "  WARN reading page_update_log: %v\n", err)
	} else {
		if updated := counts["updated"]; updated > 0 {
			fmt.Printf("pages updated total: %d\n", updated)
		}
		if failed := counts["failed"]; failed > 0 {
			fmt.Printf("pages update failed: %d\n", failed)
		}
	}
	// Phase H / Task 13: schema: line — surfaces the active doc's
	// filename ("AGENTS.md" / "CLAUDE.md" / "bundled (no AGENTS.md or
	// CLAUDE.md)") + 8-char hash prefix + drift counter. The
	// activeSchema global is set by cmd/root.go's loadConfig (or
	// loadSchemaSoft for `schema` subcommands); DocPath == "" means we
	// fell back to schema.Bundled() because neither AGENTS.md nor
	// CLAUDE.md was present at the wiki root.
	current, prior, err := database.CountPagesByHashState(activeSchema.Hash())
	if err != nil {
		fmt.Fprintf(os.Stderr, "  WARN reading schema_hash counters: %v\n", err)
	} else {
		label := activeSchema.DocPath
		if label == "" {
			label = "bundled (no AGENTS.md or CLAUDE.md)"
		}
		active8 := activeSchema.Hash()[:8]
		if prior > 0 {
			fmt.Printf("schema:             %s (hash %s..., %d pages on prior hash)\n", label, active8, prior)
		} else {
			fmt.Printf("schema:             %s (hash %s..., %d pages on active hash)\n", label, active8, current)
		}
	}
	for _, ls := range stats.LargestSources {
		fmt.Printf("largest source:     %s (%d files)\n", ls.URI, ls.FileCount)
	}
	return nil
}
