// Package wiki — retrolink.go
//
// RetroLinkPages is the body-only, idempotent backfill that runs after a
// new page lands on disk. For every existing page whose body mentions one
// of the new titles in bare prose, the body is rewritten to wrap the
// mention in [[wikilink]] form. Evidence rows, source_ids, and links are
// untouched — the trust validator never needs to run in this path because
// no claim is being made, only a link is being drawn.
//
// Idempotent: a second call with the same newTitles is a no-op for every
// already-linked page (the underlying RewriteBareReferencesAsWikilinks is
// idempotent, so a body that already contains [[Title]] returns
// byte-identical and we skip the disk write).
//
// Trust property: unchanged. The rewriter touches body only; evidence
// rows are read for nothing in this path.
//
// Phase D wires this into ingest, promote, and mcp.write_page after each
// of those write paths has finished its own writes (so the just-written
// page is excluded from the candidate set via newTitles membership).
package wiki

import (
	"fmt"
	"os"
	"time"

	"github.com/mritunjaysharma394/llmwiki/internal/db"
)

// retroLinkFTSThreshold gates whether RetroLinkPages pre-filters the
// candidate set via db.SearchPages (FTS5). Below this many existing pages
// the unfiltered O(N×M) substring scan via RewriteBareReferencesAsWikilinks
// is fast enough that the FTS narrowing isn't worth the round-trips. At or
// above the threshold we narrow first per new title and union the hits, so
// very large wikis stay sub-linear in the wiki-wide retro-link cost.
//
// Tests lower this to a small value to exercise both the narrowed and the
// full-scan path. Production callers do not change it.
var retroLinkFTSThreshold = 500

// RetroLinkResult is the small return shape Phase D consumes. UpdatedTitles
// lists every existing page whose body changed and was persisted (disk +
// DB) on this call. Order is "first-seen during the candidate walk", which
// is db.AllPages's underlying order at small N and the FTS-union order at
// large N — neither is guaranteed stable across schema changes, so callers
// that need a deterministic listing should sort.
type RetroLinkResult struct {
	UpdatedTitles []string
}

// RetroLinkPages walks the wiki's existing pages (minus the just-written
// titles in newTitles) and rewrites bodies to add [[wikilink]] mentions of
// any newTitle that appears in bare prose. Pages whose body changes get
// their content_hash + updated_at recomputed and persisted via the same
// WritePage + db.UpsertPage chain ingest uses. Evidence rows are never
// touched.
//
// Pages whose title is in newTitles are skipped — those were just written
// by the caller's own write step (ingest, promote, or mcp.write_page) over
// the full title set, so they're already wikilink-correct.
//
// At or above retroLinkFTSThreshold pages, candidates are pre-filtered via
// db.SearchPages on each new title; below that, we walk all pages. FTS
// errors are non-fatal — they fall through to the full-scan candidate set
// for that title, so a missing or sick FTS index degrades gracefully
// instead of failing the call.
//
// Sub-project 7 / Phase D Task 8: activeSchemaHash is stamped onto every
// rewritten page. The retro-linker writes a body-only update under the
// active schema (the new wikilinks anchor to titles authored under the
// same schema), so the page is "current under the active schema" by the
// time we leave this call. An empty activeSchemaHash leaves schema_hash
// untouched (callers that don't carry a schema yet — e.g. legacy paths
// in tests — pass "").
func RetroLinkPages(database *db.DB, wikiDir string, newTitles []string, activeSchemaHash string) (RetroLinkResult, error) {
	var res RetroLinkResult
	if len(newTitles) == 0 {
		return res, nil
	}

	newSet := make(map[string]bool, len(newTitles))
	for _, t := range newTitles {
		newSet[t] = true
	}

	all, err := database.AllPages()
	if err != nil {
		return res, fmt.Errorf("loading pages: %w", err)
	}

	var candidates []db.PageRecord
	if len(all) >= retroLinkFTSThreshold {
		// Narrow via FTS5 on each new title; union the candidate IDs so a
		// page mentioning multiple new titles is only rewritten once.
		seen := map[int64]bool{}
		for _, t := range newTitles {
			hits, err := database.SearchPages(t, len(all))
			if err != nil {
				// FTS error is non-fatal — skip narrowing for this title.
				// At worst we under-link this run; the next call (with the
				// same newTitle and a healthy index) catches it.
				fmt.Fprintf(os.Stderr, "  WARN retro-link FTS pre-filter failed for %q: %v\n", t, err)
				continue
			}
			for _, h := range hits {
				if seen[h.ID] || newSet[h.Title] {
					continue
				}
				seen[h.ID] = true
				candidates = append(candidates, h)
			}
		}
	} else {
		for _, p := range all {
			if newSet[p.Title] {
				continue
			}
			candidates = append(candidates, p)
		}
	}

	now := time.Now().UTC()
	for _, p := range candidates {
		original := p.Body
		rewritten := RewriteBareReferencesAsWikilinks(p.Body, newTitles)
		if rewritten == original {
			continue
		}
		// db.PageRecord doesn't carry evidence/links/tags — re-read the
		// full Page from disk so WritePage round-trips frontmatter
		// faithfully. Evidence rows themselves stay in the DB; we only
		// reuse the parsed-from-disk evidence list to re-emit the
		// frontmatter `evidence:` block byte-equivalently.
		full, err := ReadPage(p.Path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  WARN reading %s for retro-link: %v\n", p.Path, err)
			continue
		}
		full.Body = rewritten
		full.ContentHash = HashContent(rewritten)
		full.UpdatedAt = now
		if err := WritePage(full, wikiDir); err != nil {
			fmt.Fprintf(os.Stderr, "  WARN writing %s after retro-link: %v\n", p.Path, err)
			continue
		}
		rec := db.PageRecord{
			Title:       full.Title,
			Path:        p.Path,
			Body:        rewritten,
			ContentHash: full.ContentHash,
			SourceIDs:   p.SourceIDs,
		}
		if err := database.UpsertPage(rec); err != nil {
			fmt.Fprintf(os.Stderr, "  WARN db upsert for retro-linked %s: %v\n", full.Title, err)
			continue
		}
		// Sub-project 7 / Phase D Task 8: stamp active schema hash on
		// the retro-linked page. The retro-linker doesn't run
		// ValidateAndAttachEvidence — no claim is made, only a link
		// is drawn — but the body now reflects an under-active-schema
		// state, so the hash is correct. Stamp failures are
		// non-fatal; an empty activeSchemaHash skips the stamp.
		if activeSchemaHash != "" {
			if err := database.UpdateSchemaHash(p.ID, activeSchemaHash); err != nil {
				fmt.Fprintf(os.Stderr, "  WARN stamping schema_hash for retro-linked %q: %v\n", full.Title, err)
			}
		}
		res.UpdatedTitles = append(res.UpdatedTitles, full.Title)
	}
	return res, nil
}
