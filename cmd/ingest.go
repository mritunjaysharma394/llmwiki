package cmd

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/mritunjaysharma394/llmwiki/internal/db"
	"github.com/mritunjaysharma394/llmwiki/internal/ingest"
	"github.com/mritunjaysharma394/llmwiki/internal/wiki"
	"github.com/spf13/cobra"
)

const (
	ingestChunkSize   = 16 * 1024
	ingestMaxInflight = 5
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

	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(content)))

	existing, err := database.GetSource(source)
	if err != nil {
		return fmt.Errorf("checking source: %w", err)
	}
	if existing != nil && existing.ContentHash == hash {
		fmt.Println("Source unchanged, skipping.")
		return nil
	}

	rawPath := filepath.Join(cfg.Wiki.RawDir, hash+".txt")
	if err := os.MkdirAll(cfg.Wiki.RawDir, 0755); err != nil {
		return err
	}
	if err := os.WriteFile(rawPath, []byte(content), 0644); err != nil {
		return fmt.Errorf("writing raw: %w", err)
	}

	titles, err := database.AllPageTitles()
	if err != nil {
		return fmt.Errorf("fetching titles: %w", err)
	}

	chunks := chunkContent(content, ingestChunkSize)
	if len(chunks) > 1 {
		fmt.Printf("  Content is %d bytes, processing %d chunks (max %d in flight)...\n",
			len(content), len(chunks), ingestMaxInflight)
	}

	type result struct {
		pages []wiki.Page
		err   error
		idx   int
	}
	results := make([]result, len(chunks))

	sem := make(chan struct{}, ingestMaxInflight)
	var wg sync.WaitGroup
	var done int64

	for i, chunk := range chunks {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, chunk string) {
			defer wg.Done()
			defer func() { <-sem }()
			got, err := wiki.IngestToPages(ctx, llmClient, chunk, titles)
			results[i] = result{pages: got, err: err, idx: i}
			n := atomic.AddInt64(&done, 1)
			fmt.Printf("\r  [%d/%d] processed", n, len(chunks))
		}(i, chunk)
	}
	wg.Wait()
	if len(chunks) > 1 {
		fmt.Println()
	}

	var pages []wiki.Page
	for _, r := range results {
		if r.err != nil {
			fmt.Printf("  WARN chunk %d failed: %v\n", r.idx+1, r.err)
			continue
		}
		pages = append(pages, r.pages...)
	}
	if len(pages) == 0 {
		fmt.Println("LLM produced no pages with verifiable evidence.")
		return nil
	}

	sourceID, err := database.UpsertSource(source, hash)
	if err != nil {
		return fmt.Errorf("recording source: %w", err)
	}

	if existing != nil && existing.ContentHash != hash {
		if err := database.DeleteEvidenceForSource(sourceID); err != nil {
			return fmt.Errorf("clearing old evidence: %w", err)
		}
	}

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

		stored, err := database.GetPage(pages[i].Title)
		if err != nil || stored == nil {
			return fmt.Errorf("re-fetch page %q: %v", pages[i].Title, err)
		}
		var dbEv []db.Evidence
		for _, e := range pages[i].Evidence {
			dbEv = append(dbEv, db.Evidence{Quote: e.Quote, LineStart: e.LineStart, LineEnd: e.LineEnd})
		}
		if err := database.InsertEvidence(stored.ID, sourceID, dbEv); err != nil {
			return fmt.Errorf("insert evidence for %q: %w", pages[i].Title, err)
		}

		var links []db.Link
		for _, l := range pages[i].Links {
			links = append(links, db.Link{FromPage: pages[i].Title, ToPage: l.To, LinkType: l.Type})
		}
		if len(links) > 0 {
			database.UpsertLinks(pages[i].Title, links)
		}
		fmt.Printf("  ✓ %s (%d evidence)\n", pages[i].Title, len(pages[i].Evidence))
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
		end := size
		if idx := strings.LastIndex(s[:end], "\n"); idx > size/2 {
			end = idx + 1
		}
		chunks = append(chunks, s[:end])
		s = s[end:]
	}
	return chunks
}

func slugify(s string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range s {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevDash = false
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + 32)
			prevDash = false
		default:
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	out := strings.TrimRight(b.String(), "-")
	if len(out) > 60 {
		out = out[:60]
	}
	return out
}
