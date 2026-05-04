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
	} else {
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
			// Phase C Task 6: activeSchema is loaded by cmd/root.go's
			// loadConfig from AGENTS.md / CLAUDE.md, falling back to
			// schema.Bundled() when neither file exists.
			result, err := wiki.DetectContradictions(ctx, llmClient, batch, activeSchema)
			spin.Stop()
			if err != nil {
				fmt.Printf("  WARN: contradiction check failed: %v\n", err)
				continue
			}
			fmt.Println(result)
		}
	}

	// Phase H / Task 13: schema_drift surface. After the existing
	// contradiction-detection output, surface pages whose schema_hash
	// differs from the active doc's hash. Verbose-on-purpose: name the
	// active hash, name the prior count, recommend both eager
	// (`llmwiki schema migrate`) and lazy (do nothing — the cross-page
	// update pass touches pages naturally) remediation. The wiki does
	// not auto-rebuild — the spec keeps that decision the user's.
	//
	// "prior" is everything-not-at-active grouped together; the count
	// might span multiple historical hashes, so the warning header
	// names only the active hash and lets the bullet list explain.
	_, prior, err := database.CountPagesByHashState(activeSchema.Hash())
	if err != nil {
		fmt.Fprintf(os.Stderr, "  WARN reading schema_hash counters: %v\n", err)
	} else if prior > 0 {
		active8 := activeSchema.Hash()[:8]
		fmt.Println()
		fmt.Printf("!! schema_drift: %d page(s) on a prior schema (active hash: %s...)\n", prior, active8)
		fmt.Println("                 The active schema defines a different ontology or prompt set than")
		fmt.Println("                 the schema those pages were ingested under.")
		fmt.Println()
		fmt.Println("                 To bring all pages up to the new schema:")
		fmt.Println("                   llmwiki schema migrate")
		fmt.Println("                 (runs cross-page page-update on every page; expensive;")
		fmt.Println("                  see `llmwiki schema migrate --help`)")
		fmt.Println()
		fmt.Println("                 To bring pages up lazily as new sources arrive: do nothing.")
		fmt.Println("                 The next `ingest` that touches a given page via the")
		fmt.Println("                 cross-page update pass will bring it to schema.")
		fmt.Println()
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
