package cmd

import (
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/mritunjaysharma394/llmwiki/internal/cliutil"
	"github.com/mritunjaysharma394/llmwiki/internal/wiki"
	"github.com/spf13/cobra"
)

var lintCmd = &cobra.Command{
	Use:   "lint",
	Short: "Check for stale pages and contradictions",
	RunE:  runLint,
}

func runLint(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	sources, err := database.GetAllSources()
	if err != nil {
		return cliutil.Wrap("loading sources for staleness check",
			err,
			"the database may be corrupt; back up .llmwiki/wiki.db and re-run 'llmwiki init' + 'llmwiki ingest <source>'")
	}

	fmt.Println("=== Staleness Check ===")
	stale := 0
	for _, s := range sources {
		current, err := currentHash(s.URI)
		if err != nil {
			fmt.Printf("  WARN: cannot check %s: %v\n", s.URI, err)
			continue
		}
		if current != s.ContentHash {
			fmt.Printf("  STALE: %s\n", s.URI)
			stale++
		}
	}
	if stale == 0 {
		fmt.Println("  All sources up to date.")
	}

	fmt.Println("\n=== Contradiction Check ===")
	records, err := database.AllPages()
	if err != nil {
		return cliutil.Wrap("loading pages for contradiction check",
			err,
			"if the wiki is empty, run 'llmwiki ingest <source>' first; otherwise the database may be corrupt")
	}
	if len(records) < 2 {
		fmt.Println("  Not enough pages to check for contradictions.")
		return nil
	}
	var pages []wiki.Page
	for _, r := range records {
		pages = append(pages, wiki.Page{Title: r.Title, Body: r.Body})
	}

	// Check in batches of 10 to avoid huge prompts
	const batchSize = 10
	for i := 0; i < len(pages); i += batchSize {
		end := i + batchSize
		if end > len(pages) {
			end = len(pages)
		}
		batch := pages[i:end]
		spin := startSpinner(fmt.Sprintf("Checking pages %d-%d for contradictions...", i+1, end))
		result, err := wiki.DetectContradictions(ctx, llmClient, batch)
		spin.Stop()
		if err != nil {
			fmt.Printf("  WARN: contradiction check failed: %v\n", err)
			continue
		}
		fmt.Println(result)
	}
	return nil
}

func currentHash(uri string) (string, error) {
	var data []byte
	var err error
	switch {
	case strings.HasPrefix(uri, "http://") || strings.HasPrefix(uri, "https://"):
		resp, err := http.Get(uri)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		data, err = io.ReadAll(resp.Body)
		if err != nil {
			return "", err
		}
	default:
		data, err = os.ReadFile(uri)
		if err != nil {
			return "", err
		}
	}
	return fmt.Sprintf("%x", sha256.Sum256(data)), nil
}
