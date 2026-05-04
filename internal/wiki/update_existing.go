// Package wiki — update_existing.go
//
// Sub-project 6b — pillar 3 — cross-page page-update pass.
//
// IMPORTANT — TRUST PROPERTY. This is the most validator-hostile change
// in the binary's history. Every code path that writes a page body
// reaches disk only via wiki.ValidateAndAttachEvidence. The validator
// can drop the proposed body — that's the design (Q11 / spec risk #1).
// Pages whose proposed body fails validation STAY AT THEIR PRIOR
// VERSION; we never silently downgrade. The page_update_log audit
// trail records every outcome (updated / body_only / failed /
// skipped) so the user can sqlite3-grep to debug.
//
// B1 lands the candidate-selection scaffold + entrypoint signature
// only. The per-candidate LLM call, validator pass, quote floor,
// content_hash skip, and audit trail land in B2 (Task 4).
package wiki

import (
	"context"
	"io"
	"sort"

	"github.com/mritunjaysharma394/llmwiki/internal/db"
	"github.com/mritunjaysharma394/llmwiki/internal/ingest"
	"github.com/mritunjaysharma394/llmwiki/internal/llm"
)

const (
	defaultUpdateExistingMaxCandidatesPerSource = 20
	defaultUpdateExistingMaxCandidatesTotal     = 50
	defaultUpdateExistingQuoteFloor             = 2
)

// UpdateExistingOptions captures the per-call knobs that map onto the
// `--update-existing-*` flag surface (Phase D will wire those in).
//
// MaxCandidatesPerSource caps the FTS shortlist size per new source
// file; MaxCandidatesTotal caps the union across all new source files
// (forced candidates bypass the per-source cap and the global cap).
//
// QuoteFloor is the lower bound on the number of validated quotes a
// proposed body must carry to be written; the per-candidate body
// effectively uses min(QuoteFloor, len(originalEvidence)) so a
// previously-1-quote page is never held to a higher bar than it
// started at.
//
// DebugUpdates toggles per-candidate verdict lines into Logger.
//
// ForcedCandidateIDs are page IDs that bypass the FTS shortlist and
// the global cap; appended to the candidate list directly. The
// contradiction-on-ingest bridge (Phase F) feeds these in when
// --update-existing is on AND DetectIngestContradictions returned
// non-empty: a contradiction is the strongest possible signal that
// this page is touched by the new source.
type UpdateExistingOptions struct {
	MaxCandidatesPerSource int
	MaxCandidatesTotal     int
	QuoteFloor             int
	DebugUpdates           bool
	Logger                 io.Writer

	ForcedCandidateIDs []int64
}

// UpdateResult is the structured success / failure shape
// UpdateExistingPagesFromSource returns. Updated / BodyOnly / Skipped
// hold page titles in the order outcomes were committed; Failed
// carries the dropped-quotes payload for debugging.
type UpdateResult struct {
	Updated  []string
	BodyOnly []string
	Failed   []UpdateFailure
	Skipped  []string
	// Mirror counts, for the cmd/MCP summary lines.
	PagesUpdated      int
	PagesUpdateFailed int
}

// UpdateFailure is one row in UpdateResult.Failed: the title that
// stayed at its prior version, the short reason code, and the
// dropped-quote payload.
type UpdateFailure struct {
	Title         string
	Reason        string
	DroppedQuotes []DroppedQuote // reused from promote.go
}

// updateCandidate is the internal struct used during shortlist
// construction so we can preserve first-seen order across the FTS-hit
// + evidence-hit union before applying caps.
type updateCandidate struct {
	rec   db.PageRecord
	order int
}

// UpdateExistingPagesFromSource is the pillar 3 entrypoint. Called
// from IngestSource between the contradiction-detection pass and
// RegenerateIndex, gated by IngestOptions.UpdateExisting.
//
// newSourceFiles are the source files for the source just ingested;
// newPageTitles are the page titles written by that ingest in this
// run (excluded from candidate selection — they were just authored
// with the full title set already in mind).
//
// B1 walks candidates with a no-op LLM and returns zero. B2 plugs in
// the per-candidate body.
func UpdateExistingPagesFromSource(
	ctx context.Context,
	cfg IngestSourceConfig,
	database *db.DB,
	client llm.Client,
	sourceID int64,
	newSourceFiles []ingest.SourceFile,
	newPageTitles []string,
	opts UpdateExistingOptions,
) (UpdateResult, error) {
	if opts.MaxCandidatesPerSource == 0 {
		opts.MaxCandidatesPerSource = defaultUpdateExistingMaxCandidatesPerSource
	}
	if opts.MaxCandidatesTotal == 0 {
		opts.MaxCandidatesTotal = defaultUpdateExistingMaxCandidatesTotal
	}
	if opts.QuoteFloor == 0 {
		opts.QuoteFloor = defaultUpdateExistingQuoteFloor
	}
	candidates, err := selectUpdateCandidates(database, newSourceFiles, newPageTitles, opts)
	if err != nil {
		return UpdateResult{}, err
	}
	if len(candidates) == 0 {
		return UpdateResult{}, nil
	}

	// Task 4 (B2) plugs the per-candidate LLM call + validator + audit
	// here. For B1, walk candidates with a no-op LLM and return zero.
	_ = ctx
	_ = cfg
	_ = client
	_ = sourceID
	return UpdateResult{}, nil
}

// selectUpdateCandidates is the test-exposed shortlist builder.
//
// Per new source file, unions db.SearchPages and db.SearchEvidence
// hits (keyed by page ID); excludes pages whose title is in
// newPageTitles; appends opts.ForcedCandidateIDs; caps at
// MaxCandidatesTotal; preserves ForcedCandidateIDs even past the
// cap (forced > FTS-shortlisted).
//
// FTS query text is derived from each new source file's content via
// the same truncation strategy db.SearchPages uses internally
// (ftsQuery folds non-alphanumerics to OR-separated tokens). To keep
// the query bounded for very large files we cap the query text at
// the first ~2000 bytes — the FTS engine OR-tokenizes anyway, so
// further tokens add diminishing recall for considerable cost.
func selectUpdateCandidates(
	database *db.DB,
	newSourceFiles []ingest.SourceFile,
	newPageTitles []string,
	opts UpdateExistingOptions,
) ([]db.PageRecord, error) {
	skip := map[string]struct{}{}
	for _, t := range newPageTitles {
		skip[t] = struct{}{}
	}

	const queryCap = 2000
	seen := map[int64]*updateCandidate{}
	order := 0

	addPage := func(p db.PageRecord) bool {
		if _, dup := seen[p.ID]; dup {
			return false
		}
		if _, blocked := skip[p.Title]; blocked {
			return false
		}
		seen[p.ID] = &updateCandidate{rec: p, order: order}
		order++
		return true
	}

	perCap := opts.MaxCandidatesPerSource
	if perCap <= 0 {
		perCap = defaultUpdateExistingMaxCandidatesPerSource
	}

	for _, f := range newSourceFiles {
		text := f.Content
		if len(text) > queryCap {
			text = text[:queryCap]
		}
		// Per-source candidate budget is a soft cap on the union from
		// pages_fts + evidence_fts hits for this single source file.
		thisSource := 0
		pages, err := database.SearchPages(text, perCap)
		if err == nil {
			for _, p := range pages {
				if thisSource >= perCap {
					break
				}
				if addPage(p) {
					thisSource++
				}
			}
		}
		hits, err := database.SearchEvidence(text, perCap)
		if err == nil {
			for _, h := range hits {
				if thisSource >= perCap {
					break
				}
				if _, dup := seen[h.PageID]; dup {
					continue
				}
				rec, err := database.GetPageByID(h.PageID)
				if err != nil || rec == nil {
					continue
				}
				if addPage(*rec) {
					thisSource++
				}
			}
		}
	}

	// Cap union at MaxCandidatesTotal preserving FTS-walk order.
	totalCap := opts.MaxCandidatesTotal
	if totalCap <= 0 {
		totalCap = defaultUpdateExistingMaxCandidatesTotal
	}
	ordered := make([]*updateCandidate, 0, len(seen))
	for _, c := range seen {
		ordered = append(ordered, c)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].order < ordered[j].order })
	if len(ordered) > totalCap {
		ordered = ordered[:totalCap]
	}

	// Append forced IDs that the per-source/global cap may have dropped.
	// Forced candidates bypass the cap entirely — they're the
	// contradiction-on-ingest bridge (Phase F), the strongest possible
	// signal that the new source touches this page.
	final := make([]db.PageRecord, 0, len(ordered)+len(opts.ForcedCandidateIDs))
	finalSeen := map[int64]struct{}{}
	for _, c := range ordered {
		final = append(final, c.rec)
		finalSeen[c.rec.ID] = struct{}{}
	}
	for _, id := range opts.ForcedCandidateIDs {
		if _, dup := finalSeen[id]; dup {
			continue
		}
		rec, err := database.GetPageByID(id)
		if err != nil || rec == nil {
			continue
		}
		if _, blocked := skip[rec.Title]; blocked {
			continue
		}
		final = append(final, *rec)
		finalSeen[id] = struct{}{}
	}
	return final, nil
}
