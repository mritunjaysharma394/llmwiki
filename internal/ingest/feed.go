package ingest

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mmcdole/gofeed"
)

// FeedOptions controls feed crawling rate and breadth.
type FeedOptions struct {
	RequestsPerSecond float64 // 0 = use default 1.0
	MaxEntries        int     // 0 = use default 50
}

// DefaultFeedOptions returns the standard feed crawler configuration: 1 req/s
// polite rate-limit and a 50-entry breadth cap.
func DefaultFeedOptions() FeedOptions {
	return FeedOptions{RequestsPerSecond: 1.0, MaxEntries: 50}
}

// FetchFeedFiles fetches an RSS/Atom/JSON Feed at feedURL and returns one
// SourceFile per entry. Each entry's permalink (Link) becomes the SourceFile's
// RelativePath, and its content is whatever sub-project 3's URL pipeline
// produces for that link (Readability + html-to-markdown for HTML, raw passthrough
// for text/*, page-N for PDFs). Polite rate-limiting is enforced between
// per-entry fetches.
//
// Entries beyond MaxEntries are skipped silently — incremental re-ingest will
// pick them up on later runs as long as they are still in the feed.
func FetchFeedFiles(feedURL string, urlOpts URLOptions, opts FeedOptions) ([]SourceFile, error) {
	if opts.RequestsPerSecond <= 0 {
		opts.RequestsPerSecond = 1.0
	}
	if opts.MaxEntries <= 0 {
		opts.MaxEntries = 50
	}

	parser := gofeed.NewParser()
	parser.UserAgent = urlOpts.UserAgent
	feed, err := parser.ParseURLWithContext(feedURL, context.Background())
	if err != nil {
		return nil, fmt.Errorf("parse feed %s: %w", feedURL, err)
	}

	var out []SourceFile
	gap := time.Duration(float64(time.Second) / opts.RequestsPerSecond)
	for i, item := range feed.Items {
		if i >= opts.MaxEntries {
			break
		}
		link := strings.TrimSpace(item.Link)
		if link == "" {
			continue
		}
		if i > 0 {
			time.Sleep(gap)
		}
		entryFiles, err := FetchURLFiles(link, urlOpts)
		if err != nil {
			// Skip the bad entry; warn via stderr indirectly through caller (we
			// stay silent here because the caller (cmd/ingest) prints a
			// per-entry summary). Returning a partial list is the right
			// behaviour for "subscribe to a feed".
			continue
		}
		for _, f := range entryFiles {
			f.RelativePath = link
			out = append(out, f)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("feed %s yielded zero ingestable entries", feedURL)
	}
	return out, nil
}
