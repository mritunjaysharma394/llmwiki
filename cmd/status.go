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
	for _, ls := range stats.LargestSources {
		fmt.Printf("largest source:     %s (%d files)\n", ls.URI, ls.FileCount)
	}
	return nil
}
