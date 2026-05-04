// Package wiki — fast_lint.go
//
// FastLint is the sub-second post-ingest lint pass plan §4 calls for.
// Three signals, all mechanical, no LLM calls:
//
//   - Orphans: pages with zero inbound `[[wikilinks]]`. The "links"
//     table only carries explicit frontmatter `links:` entries, so
//     orphan detection scans the bodies of every other page for
//     `[[<title>]]` substrings — a body-level inbound-link count.
//
//   - Missing cross-refs: pages whose body mentions another existing
//     page title in bare prose (no wikilink wrapping). Reuses the
//     FindBareReferences primitive lifted from the retro-linker so the
//     fence / frontmatter / backtick / longer-first semantics are
//     byte-identical.
//
//   - Schema drift: count of pages whose schema_hash != active. Pure
//     SQL (db.CountPagesByHashState); v0.7's existing surface,
//     re-exposed here so the ingest tail can render it inline without
//     pulling in cmd/status's heavier output.
//
// Sub-project 8 Phase A defines FastLint and its result shape; Phase C
// wires it into the tail of cmd/ingest. The function is silent when
// clean (zero counts) — the cmd-level surface decides whether to print
// anything based on the returned counts.
package wiki

import (
	"fmt"
	"sort"
	"strings"

	"github.com/mritunjaysharma394/llmwiki/internal/db"
	"github.com/mritunjaysharma394/llmwiki/internal/schema"
)

// FastLintResult is the shape returned to cmd-level callers. Each
// signal carries a count and (for orphan / missing-xref) up to three
// example titles so the inline output can name the offenders without
// flooding the terminal.
//
// Determinism: TopOrphanTitles and MissingXRefs are sorted (orphans:
// ascending by title; missing-xrefs: ascending by Page) so two runs
// over the same DB produce byte-identical output.
type FastLintResult struct {
	// OrphanCount is the total number of pages with zero inbound
	// [[wikilink]] mentions in any other page's body.
	OrphanCount int
	// TopOrphanTitles is the first 3 orphan titles, sorted ascending.
	TopOrphanTitles []string

	// MissingXRefCount is the total number of pages whose body
	// references at least one other existing title in bare prose.
	MissingXRefCount int
	// MissingXRefs lists per-offender details. At most 3 entries by
	// FastLint contract; each entry's MissingTitles is at most 3 long.
	MissingXRefs []MissingCrossRef

	// SchemaDriftCount is the count of pages whose schema_hash !=
	// active. Pulled from db.CountPagesByHashState. The active hash
	// itself is not surfaced — the lint tail only needs the count;
	// `schema show` / `schema migrate` carry the hash for
	// debug/repair workflows.
	SchemaDriftCount int
}

// MissingCrossRef is one row of FastLintResult.MissingXRefs: the
// offending page's title and the (capped) list of bare-prose titles
// it should have wikilinked.
type MissingCrossRef struct {
	Page          string
	MissingTitles []string
}

// FastLintTopN bounds the number of titles surfaced per signal. Plan
// §4: "first 3 titles" for orphans and offending pages. Held as a
// const so tests pin against the same number.
const FastLintTopN = 3

// FastLint runs the three sub-second checks and returns a populated
// FastLintResult. Read-only over the DB; never writes a row, never
// mutates a page on disk. Caller-side surfaces (cmd/ingest's tail in
// Phase C, cmd/maintain --lint in Phase D) decide whether to render.
//
// Performance posture: O(P²) substring scans for orphan detection in
// the worst case (every page mentions every other page). At the v0.8
// expected scale (low thousands of pages) this is sub-second; if a
// future user hits 50k pages we can re-route through pages_fts
// without changing the API.
func FastLint(database *db.DB, sch schema.Schema) (FastLintResult, error) {
	var res FastLintResult

	pages, err := database.AllPages()
	if err != nil {
		return res, fmt.Errorf("loading pages: %w", err)
	}

	titles := make([]string, 0, len(pages))
	bodyByTitle := make(map[string]string, len(pages))
	for _, p := range pages {
		if p.Title == "" {
			continue
		}
		titles = append(titles, p.Title)
		bodyByTitle[p.Title] = p.Body
	}

	// Orphan scan: for each title T, count occurrences of "[[T]]" in
	// every OTHER page's body. Plan defines orphan as "zero inbound
	// wikilinks" — pages whose own body wikilinks themselves don't
	// count (the retro-linker writes self-references at index time
	// for nobody, and a page mentioning its own title is a no-op).
	orphans := make([]string, 0)
	for _, t := range titles {
		needle := "[[" + t + "]]"
		var inbound int
		for _, other := range titles {
			if other == t {
				continue
			}
			if strings.Contains(bodyByTitle[other], needle) {
				inbound++
				if inbound > 0 {
					break // one is enough; we only need "≥1"
				}
			}
		}
		if inbound == 0 {
			orphans = append(orphans, t)
		}
	}
	sort.Strings(orphans)
	res.OrphanCount = len(orphans)
	if len(orphans) > FastLintTopN {
		res.TopOrphanTitles = append([]string(nil), orphans[:FastLintTopN]...)
	} else {
		res.TopOrphanTitles = append([]string(nil), orphans...)
	}

	// Missing cross-ref scan: for each page, find bare-prose mentions
	// of any OTHER existing title. Reuses FindBareReferences so
	// fence/frontmatter/backtick/longer-first semantics match the
	// rewriter exactly.
	type pageHits struct {
		title string
		hits  []string
	}
	var offenders []pageHits
	for _, p := range pages {
		// Build the candidate set: all titles other than this page's.
		others := make([]string, 0, len(titles)-1)
		for _, t := range titles {
			if t == p.Title {
				continue
			}
			others = append(others, t)
		}
		hits := FindBareReferences(p.Body, others)
		if len(hits) == 0 {
			continue
		}
		offenders = append(offenders, pageHits{title: p.Title, hits: hits})
	}
	// Determinism: sort offenders by page title.
	sort.Slice(offenders, func(i, j int) bool { return offenders[i].title < offenders[j].title })
	res.MissingXRefCount = len(offenders)
	cap := FastLintTopN
	if len(offenders) < cap {
		cap = len(offenders)
	}
	for _, o := range offenders[:cap] {
		titlesCap := o.hits
		if len(titlesCap) > FastLintTopN {
			titlesCap = titlesCap[:FastLintTopN]
		}
		res.MissingXRefs = append(res.MissingXRefs, MissingCrossRef{
			Page:          o.title,
			MissingTitles: append([]string(nil), titlesCap...),
		})
	}

	// Schema drift counter — v0.7 surface; reused as-is.
	_, prior, err := database.CountPagesByHashState(sch.Hash())
	if err != nil {
		return res, fmt.Errorf("schema drift count: %w", err)
	}
	res.SchemaDriftCount = prior

	return res, nil
}
