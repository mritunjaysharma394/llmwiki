package cmd

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show wiki statistics",
	RunE:  runStatus,
}

func runStatus(cmd *cobra.Command, args []string) error {
	stats, err := database.GetStats()
	if err != nil {
		return fmt.Errorf("getting stats: %w", err)
	}

	fmt.Printf("Pages:   %d\n", stats.TotalPages)
	fmt.Printf("Sources: %d\n", stats.TotalSources)
	if !stats.LastIngest.IsZero() {
		fmt.Printf("Last ingest: %s\n", stats.LastIngest.Format(time.RFC1123))
	} else {
		fmt.Printf("Last ingest: never\n")
	}
	return nil
}
