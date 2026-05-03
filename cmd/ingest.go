package cmd

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/mritunjaysharma394/llmwiki/internal/db"
	"github.com/mritunjaysharma394/llmwiki/internal/ingest"
	"github.com/mritunjaysharma394/llmwiki/internal/wiki"
	"github.com/spf13/cobra"
)

var ingestCmd = &cobra.Command{
	Use:   "ingest <source>",
	Short: "Ingest a source (file/directory, URL, or GitHub repo) into the wiki",
	Args:  cobra.ExactArgs(1),
	RunE:  runIngest,
}

func runIngest(cmd *cobra.Command, args []string) error {
	source := args[0]
	ctx := cmd.Context()

	// Fetch content based on source type
	var content string
	var err error
	switch {
	case ingest.IsGitHubURL(source):
		fmt.Printf("Cloning GitHub repo %s...\n", source)
		content, err = ingest.FetchGitHub(source)
	case strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://"):
		fmt.Printf("Fetching URL %s...\n", source)
		content, err = ingest.FetchURL(source)
	default:
		fmt.Printf("Reading local path %s...\n", source)
		content, err = ingest.ReadLocal(source)
	}
	if err != nil {
		return fmt.Errorf("reading source: %w", err)
	}
	if strings.TrimSpace(content) == "" {
		return fmt.Errorf("no content found in source")
	}

	// Content hash for deduplication
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(content)))

	// Check if source is unchanged
	existing, err := database.GetSource(source)
	if err != nil {
		return fmt.Errorf("checking source: %w", err)
	}
	if existing != nil && existing.ContentHash == hash {
		fmt.Println("Source unchanged, skipping.")
		return nil
	}

	// Store raw content
	rawPath := filepath.Join(cfg.Wiki.RawDir, hash+".txt")
	if err := os.MkdirAll(cfg.Wiki.RawDir, 0755); err != nil {
		return err
	}
	if err := os.WriteFile(rawPath, []byte(content), 0644); err != nil {
		return fmt.Errorf("writing raw: %w", err)
	}

	// Get existing page titles for LLM context
	titles, err := database.AllPageTitles()
	if err != nil {
		return fmt.Errorf("fetching titles: %w", err)
	}

	// Split large content into chunks the model can handle, process in parallel
	// Cap at 3 chunks (18KB) so a 1B model doesn't get overwhelmed
	const chunkSize = 6000
	const maxChunks = 3
	chunks := chunkContent(content, chunkSize)
	if len(chunks) > maxChunks {
		fmt.Printf("  Content is %d bytes; using first %d of %d chunks\n", len(content), maxChunks, len(chunks))
		chunks = chunks[:maxChunks]
	} else if len(chunks) > 1 {
		fmt.Printf("  Content is %d bytes, processing %d chunks in parallel...\n", len(content), len(chunks))
	}

	spin := startSpinner(fmt.Sprintf("Asking LLM to synthesize wiki pages (%d chunks)...", len(chunks)))
	type result struct {
		pages []wiki.Page
		err   error
		idx   int
	}
	results := make([]result, len(chunks))
	var wg sync.WaitGroup
	for i, chunk := range chunks {
		wg.Add(1)
		go func(i int, chunk string) {
			defer wg.Done()
			got, err := wiki.IngestToPages(ctx, llmClient, chunk, titles)
			results[i] = result{pages: got, err: err, idx: i}
		}(i, chunk)
	}
	wg.Wait()
	spin.Stop()

	var pages []wiki.Page
	for _, r := range results {
		if r.err != nil {
			fmt.Printf("  WARN chunk %d failed: %v\n", r.idx+1, r.err)
			continue
		}
		pages = append(pages, r.pages...)
	}
	if len(pages) == 0 {
		fmt.Println("LLM produced no pages.")
		return nil
	}

	// Record source only after LLM succeeds
	sourceID, err := database.UpsertSource(source, hash)
	if err != nil {
		return fmt.Errorf("recording source: %w", err)
	}

	// Write pages to disk and DB
	if err := os.MkdirAll(cfg.Wiki.WikiDir, 0755); err != nil {
		return err
	}
	for i := range pages {
		pages[i].SourceIDs = []int64{sourceID}
		path := wiki.PagePath(cfg.Wiki.WikiDir, pages[i].Title)
		if err := wiki.WritePage(pages[i], cfg.Wiki.WikiDir); err != nil {
			return fmt.Errorf("writing page %q: %w", pages[i].Title, err)
		}
		rec := db.PageRecord{
			Title:       pages[i].Title,
			Path:        path,
			Body:        pages[i].Body,
			ContentHash: pages[i].ContentHash,
			SourceIDs:   pages[i].SourceIDs,
		}
		if err := database.UpsertPage(rec); err != nil {
			return fmt.Errorf("db upsert %q: %w", pages[i].Title, err)
		}
		// Upsert links
		var links []db.Link
		for _, l := range pages[i].Links {
			links = append(links, db.Link{FromPage: pages[i].Title, ToPage: l.To, LinkType: l.Type})
		}
		if len(links) > 0 {
			database.UpsertLinks(pages[i].Title, links)
		}
		fmt.Printf("  ✓ %s\n", pages[i].Title)
	}
	fmt.Printf("Ingested %d page(s) from %s\n", len(pages), source)
	return nil
}

func chunkContent(s string, size int) []string {
	if len(s) <= size {
		return []string{s}
	}
	var chunks []string
	for len(s) > 0 {
		if len(s) <= size {
			chunks = append(chunks, s)
			break
		}
		// Break at last newline within the chunk to avoid splitting mid-sentence
		end := size
		if idx := strings.LastIndex(s[:end], "\n"); idx > size/2 {
			end = idx
		}
		chunks = append(chunks, s[:end])
		s = s[end:]
	}
	return chunks
}
