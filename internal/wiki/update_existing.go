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
package wiki

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/mritunjaysharma394/llmwiki/internal/db"
	"github.com/mritunjaysharma394/llmwiki/internal/ingest"
	"github.com/mritunjaysharma394/llmwiki/internal/llm"
)

const (
	defaultUpdateExistingMaxCandidatesPerSource = 20
	defaultUpdateExistingMaxCandidatesTotal     = 50
	defaultUpdateExistingQuoteFloor             = 2
)

// updateExistingSystemPrompt is the per-candidate update prompt: the
// LLM either returns a single refined page in the same writePagesTool
// shape IngestSourceFilesToPages uses, or returns {"pages": []} to
// signal "no change".
const updateExistingSystemPrompt = `You update an EXISTING wiki page in light of a NEW SOURCE.
Output a single page with the same title; the body should incorporate
information from NEW SOURCE that refines, qualifies, or extends the
existing page. Every evidence quote must verbatim-substring-match
either the NEW SOURCE files OR the existing page's already-validated
quotes (those are listed under EXISTING EVIDENCE). Do not invent
quotes. If NEW SOURCE does not actually update this page, respond
with {"pages": []} and we will keep the page unchanged.`

// UpdateExistingSystemPromptForTests exposes the v0.6 hard-coded
// update-existing system prompt for internal/schema's byte-equality
// test. Removed in v0.8 once the schema-driven path is the only path.
func UpdateExistingSystemPromptForTests() string { return updateExistingSystemPrompt }

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

// llmOutcome is the per-candidate LLM call's structured result + error.
// We collect one of these per candidate during the goroutine fan-out
// then walk them serially when applying the validator + DB writes.
type llmOutcome struct {
	idx    int
	result map[string]any
	err    error
}

// UpdateExistingPagesFromSource is the pillar 3 entrypoint. Called
// from IngestSource between the contradiction-detection pass and
// RegenerateIndex, gated by IngestOptions.UpdateExisting.
//
// newSourceFiles are the source files for the source just ingested;
// newPageTitles are the page titles written by that ingest in this
// run (excluded from candidate selection — they were just authored
// with the full title set already in mind).
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

	logger := opts.Logger
	if logger == nil {
		logger = io.Discard
	}

	// Pre-compute each candidate's original evidence + on-disk source
	// files BEFORE we fan out to the LLM. This keeps DB reads off the
	// concurrent path (sqlite without WAL mode serialises writers and
	// readers compete with them, so concurrent goroutines that touched
	// the DB raced against each other for the lock).
	type candidateBundle struct {
		cand             db.PageRecord
		originalEvidence []db.Evidence
		existingFiles    []ingest.SourceFile
		unionFiles       []ingest.SourceFile
		user             string
	}
	bundles := make([]candidateBundle, 0, len(candidates))
	for _, cand := range candidates {
		ev, err := database.GetEvidenceForPage(cand.ID)
		if err != nil {
			// Record a failed-outcome row for this candidate and continue.
			writePageUpdateLog(database, cand.ID, sourceID, cand.ContentHash, "", "failed",
				fmt.Sprintf("evidence-read-error: %v", err), 0, 0)
			continue
		}
		existing, _ := loadExistingSourceFiles(database, ev)
		union := append([]ingest.SourceFile{}, newSourceFiles...)
		union = append(union, existing...)
		bundles = append(bundles, candidateBundle{
			cand:             cand,
			originalEvidence: ev,
			existingFiles:    existing,
			unionFiles:       union,
			user:             buildUpdatePromptUser(cand, ev, newSourceFiles),
		})
	}

	llmOutcomes := make([]llmOutcome, len(bundles))

	var wg sync.WaitGroup
	sem := make(chan struct{}, ingestMaxInflight)
	for i, b := range bundles {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, b candidateBundle) {
			defer wg.Done()
			defer func() { <-sem }()
			result, err := client.CompleteStructured(ctx, updateExistingSystemPrompt, b.user, writePagesTool)
			llmOutcomes[i] = llmOutcome{idx: i, result: result, err: err}
		}(i, b)
	}
	wg.Wait()

	// Now walk each candidate serially, applying the validator + quote
	// floor + content_hash skip + DB writes. Serial execution avoids
	// the SQLite write-lock contention we'd otherwise hit when N
	// goroutines all call InsertPageUpdateLog at once.
	var out UpdateResult
	for i, b := range bundles {
		outcome := finishUpdateCandidate(
			cfg, database, sourceID, newSourceFiles, b.cand,
			b.originalEvidence, b.unionFiles, llmOutcomes[i], opts,
		)
		switch outcome.kind {
		case "updated":
			out.Updated = append(out.Updated, b.cand.Title)
			out.PagesUpdated++
		case "body_only":
			out.BodyOnly = append(out.BodyOnly, b.cand.Title)
		case "failed":
			out.Failed = append(out.Failed, UpdateFailure{
				Title:         b.cand.Title,
				Reason:        outcome.reason,
				DroppedQuotes: outcome.dropped,
			})
			out.PagesUpdateFailed++
		case "skipped":
			out.Skipped = append(out.Skipped, b.cand.Title)
		}
		if opts.DebugUpdates {
			if outcome.reason != "" {
				fmt.Fprintf(logger, "  update_existing %q -> %s (%s)\n",
					b.cand.Title, outcome.kind, outcome.reason)
			} else {
				fmt.Fprintf(logger, "  update_existing %q -> %s\n",
					b.cand.Title, outcome.kind)
			}
		}
	}
	return out, nil
}

// candidateOutcome is the per-candidate verdict the goroutine accumulates
// before the parent merges it into UpdateResult under the mutex.
type candidateOutcome struct {
	kind    string // updated | body_only | failed | skipped
	reason  string
	dropped []DroppedQuote
}

// finishUpdateCandidate is the per-candidate body run serially after
// the LLM fan-out: parse the tool result, run the validator, apply
// the quote floor, apply the content_hash skip, and write disk + DB
// on success. Always appends a page_update_log row.
func finishUpdateCandidate(
	cfg IngestSourceConfig,
	database *db.DB,
	sourceID int64,
	newSourceFiles []ingest.SourceFile,
	cand db.PageRecord,
	originalEvidence []db.Evidence,
	unionFiles []ingest.SourceFile,
	out llmOutcome,
	opts UpdateExistingOptions,
) candidateOutcome {
	if out.err != nil {
		writePageUpdateLog(database, cand.ID, sourceID, cand.ContentHash, "", "failed",
			fmt.Sprintf("llm-error: %v", out.err), 0, 0)
		return candidateOutcome{kind: "failed", reason: fmt.Sprintf("llm-error: %v", out.err)}
	}
	result := out.result

	pages, err := ExtractPagesFromToolResult(result)
	if err != nil || len(pages) == 0 {
		// LLM signalled "no change" — record skipped.
		writePageUpdateLog(database, cand.ID, sourceID, cand.ContentHash, "", "skipped",
			"llm-no-change", 0, 0)
		return candidateOutcome{kind: "skipped", reason: "llm-no-change"}
	}

	// Capture the proposed quotes BEFORE the validator mutates them
	// so we can populate DroppedQuotes faithfully on failure.
	proposed := pages[0]
	originalProposed := append([]Evidence(nil), proposed.Evidence...)

	validated, _ := ValidateAndAttachEvidence([]Page{proposed}, unionFiles)

	// --- TRUST GATE ---
	if len(validated) == 0 || len(validated[0].Evidence) == 0 {
		dropped := buildDroppedQuotes(originalProposed, unionFiles)
		writePageUpdateLog(database, cand.ID, sourceID, cand.ContentHash, "", "failed",
			"zero-quotes-matched", 0, 0)
		return candidateOutcome{kind: "failed", reason: "zero-quotes-matched", dropped: dropped}
	}
	updated := validated[0]

	// Quote floor: clamped at len(originalEvidence) so a previously
	// 1-quote page isn't held to a higher bar than it started at.
	floor := opts.QuoteFloor
	if floor <= 0 {
		floor = defaultUpdateExistingQuoteFloor
	}
	if floor > len(originalEvidence) {
		floor = len(originalEvidence)
	}
	if len(updated.Evidence) < floor {
		reason := fmt.Sprintf("below-quote-floor: %d/%d", len(updated.Evidence), floor)
		writePageUpdateLog(database, cand.ID, sourceID, cand.ContentHash, "", "failed", reason, 0, 0)
		return candidateOutcome{kind: "failed", reason: reason}
	}

	// content_hash skip: catches the single-step oscillation case
	// (Q11 / spec risk #3). No disk write of the body; record body_only
	// and continue.
	newHash := HashContent(updated.Body)
	if newHash == cand.ContentHash {
		writePageUpdateLog(database, cand.ID, sourceID, cand.ContentHash, "", "body_only",
			"content_hash-unchanged", 0, 0)
		return candidateOutcome{kind: "body_only", reason: "content_hash-unchanged"}
	}

	// Disk + DB write — the new body lands on disk, then evidence rows
	// are swapped (delete-old, insert-new). Page row's updated_at +
	// content_hash bump via UpsertPage.
	now := time.Now().UTC()
	updated.Title = cand.Title
	updated.SourceIDs = mergeSourceIDs(cand.SourceIDs, sourceID)
	updated.ContentHash = newHash
	updated.UpdatedAt = now
	updated.Tags = []string{"llmwiki", "ingest"}
	updated.Sources = distinctEvidenceSourceFiles(updated.Evidence)
	if updated.Created.IsZero() {
		// Preserve the page's original `created:` if we can fish it out
		// of the on-disk file; otherwise stamp now.
		if priorPage, err := ReadPage(cand.Path); err == nil && !priorPage.Created.IsZero() {
			updated.Created = priorPage.Created
		} else {
			updated.Created = now
		}
	}

	if err := WritePage(updated, cfg.WikiDir); err != nil {
		writePageUpdateLog(database, cand.ID, sourceID, cand.ContentHash, "", "failed",
			fmt.Sprintf("write-page-error: %v", err), 0, 0)
		return candidateOutcome{kind: "failed", reason: fmt.Sprintf("write-page-error: %v", err)}
	}

	rec := db.PageRecord{
		Title:       updated.Title,
		Path:        PagePath(cfg.WikiDir, updated.Title),
		Body:        updated.Body,
		ContentHash: updated.ContentHash,
		SourceIDs:   updated.SourceIDs,
	}
	if err := database.UpsertPage(rec); err != nil {
		writePageUpdateLog(database, cand.ID, sourceID, cand.ContentHash, "", "failed",
			fmt.Sprintf("upsert-page-error: %v", err), 0, 0)
		return candidateOutcome{kind: "failed", reason: fmt.Sprintf("upsert-page-error: %v", err)}
	}

	// Swap evidence: delete-old + insert-new. We record the exact added
	// / removed counts in the audit log.
	if err := database.DeleteEvidenceForPage(cand.ID); err != nil {
		writePageUpdateLog(database, cand.ID, sourceID, cand.ContentHash, "", "failed",
			fmt.Sprintf("delete-evidence-error: %v", err), 0, 0)
		return candidateOutcome{kind: "failed", reason: fmt.Sprintf("delete-evidence-error: %v", err)}
	}

	// Resolve evidence rows: each new evidence quote needs a source_file_id.
	// We look the source_file_id up by (source_id, relative_path) for
	// every distinct new source file; for existing-source quotes we fall
	// back to the previously-stored source_file_id from the original
	// evidence rows when paths line up.
	pathToFileID := buildPathToFileID(database, sourceID, newSourceFiles, originalEvidence)

	// Group evidence by source_id so InsertEvidence (one source per call)
	// writes FK-correct rows. New source files map to the just-ingested
	// sourceID; existing-source quotes map to whichever source_id the
	// original evidence row pointed at (resolved from the source_file).
	evBySource := map[int64][]db.Evidence{}
	for _, e := range updated.Evidence {
		sfid, srcID := resolveEvidenceSource(e.SourceFilePath, sourceID, pathToFileID, originalEvidence)
		row := db.Evidence{
			Quote:     e.Quote,
			LineStart: e.LineStart,
			LineEnd:   e.LineEnd,
		}
		if sfid != 0 {
			id := sfid
			row.SourceFileID = &id
		}
		evBySource[srcID] = append(evBySource[srcID], row)
	}
	for sid, items := range evBySource {
		if err := database.InsertEvidence(cand.ID, sid, items); err != nil {
			writePageUpdateLog(database, cand.ID, sourceID, cand.ContentHash, "", "failed",
				fmt.Sprintf("insert-evidence-error: %v", err), 0, 0)
			return candidateOutcome{kind: "failed", reason: fmt.Sprintf("insert-evidence-error: %v", err)}
		}
	}

	added := len(updated.Evidence)
	removed := len(originalEvidence)
	writePageUpdateLog(database, cand.ID, sourceID, cand.ContentHash, newHash, "updated",
		"", added, removed)

	_ = AppendLog(cfg.WikiDir, LogEntry{
		At:      now,
		Kind:    "update_existing",
		Payload: fmt.Sprintf("%s → %d evidence (was %d)", updated.Title, added, removed),
	})

	return candidateOutcome{kind: "updated"}
}

// buildUpdatePromptUser assembles the prompt body: NEW SOURCE files
// (each under `=== <path> ===`), the EXISTING PAGE marker block, and
// the EXISTING EVIDENCE marker block.
func buildUpdatePromptUser(cand db.PageRecord, originalEvidence []db.Evidence, newSourceFiles []ingest.SourceFile) string {
	var sb stringWriter
	sb.WriteString("NEW SOURCE files:\n\n")
	for i, f := range newSourceFiles {
		if i > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(fmt.Sprintf("=== %s ===\n", f.RelativePath))
		sb.WriteString(f.Content)
		if !endsWithNewline(f.Content) {
			sb.WriteString("\n")
		}
	}
	sb.WriteString("\n=== EXISTING PAGE ===\n")
	sb.WriteString(fmt.Sprintf("title: %s\n\n", cand.Title))
	sb.WriteString(cand.Body)
	if !endsWithNewline(cand.Body) {
		sb.WriteString("\n")
	}
	sb.WriteString("\n=== EXISTING EVIDENCE ===\n")
	for _, e := range originalEvidence {
		sb.WriteString(fmt.Sprintf("- %q\n", e.Quote))
	}
	return sb.String()
}

// loadExistingSourceFiles reads the on-disk content of every distinct
// source_file referenced by the given evidence rows. Failures to read
// any single file are swallowed — the caller still has the new source
// files to validate against, and the trust gate will catch any
// hallucinated quote that can't be substring-matched.
func loadExistingSourceFiles(database *db.DB, evidence []db.Evidence) ([]ingest.SourceFile, error) {
	if len(evidence) == 0 {
		return nil, nil
	}
	sources, err := database.GetAllSources()
	if err != nil {
		return nil, err
	}
	bySourceID := map[int64]db.Source{}
	for _, s := range sources {
		bySourceID[s.ID] = s
	}
	// Resolve source_file rows for every distinct source_file_id.
	type sfKey struct{ rel, uri string }
	seen := map[sfKey]struct{}{}
	out := []ingest.SourceFile{}
	for _, e := range evidence {
		if e.SourceFileID == nil {
			continue
		}
		// Find the source_file row + its parent source URI.
		// We don't have GetSourceFileByID, so iterate through the
		// source_files of the matching source.
		src, ok := bySourceID[e.SourceID]
		if !ok {
			continue
		}
		files, err := database.GetSourceFiles(e.SourceID)
		if err != nil {
			continue
		}
		var matched *db.SourceFile
		for i := range files {
			if files[i].ID == *e.SourceFileID {
				matched = &files[i]
				break
			}
		}
		if matched == nil {
			continue
		}
		key := sfKey{rel: matched.RelativePath, uri: src.URI}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		bytes, err := readSourceFileContent(src.URI, matched.RelativePath)
		if err != nil {
			continue
		}
		out = append(out, ingest.NewSourceFile(matched.RelativePath, bytes))
	}
	// Sort for determinism.
	sort.Slice(out, func(i, j int) bool { return out[i].RelativePath < out[j].RelativePath })
	return out, nil
}

// buildDroppedQuotes formats the per-quote failure payload for the
// returned UpdateFailure: maps each proposed quote to a human-readable
// reason. Mirrors PromoteAnswer's DroppedQuote shape.
func buildDroppedQuotes(proposed []Evidence, unionFiles []ingest.SourceFile) []DroppedQuote {
	byPath := map[string]ingest.SourceFile{}
	for _, f := range unionFiles {
		byPath[f.RelativePath] = f
	}
	var out []DroppedQuote
	for _, e := range proposed {
		if e.Quote == "" {
			continue
		}
		f, ok := byPath[e.SourceFilePath]
		switch {
		case e.SourceFilePath == "":
			out = append(out, DroppedQuote{
				Quote:      e.Quote,
				SourceFile: "",
				Reason:     "no source_file named; not present in any file in (new + existing) union",
			})
		case !ok:
			out = append(out, DroppedQuote{
				Quote:      e.Quote,
				SourceFile: e.SourceFilePath,
				Reason:     "named source_file is not in the (new + existing) union for this candidate",
			})
		case !containsString(f.Content, e.Quote):
			out = append(out, DroppedQuote{
				Quote:      e.Quote,
				SourceFile: e.SourceFilePath,
				Reason:     "quote not a byte-exact substring of the named source_file",
			})
		default:
			// Quote did substring-match — it must have been the trust
			// gate dropping the page (zero-evidence after validation).
			// This branch is rare; left here for defensive completeness.
		}
	}
	return out
}

// buildPathToFileID resolves a source_file_id for each (source_id,
// relative_path) pair we expect to see in evidence. Includes the new
// source files (looked up against the just-ingested sourceID) and
// every distinct path the original evidence already pointed at (so
// we can write FK-correct rows for existing-source quotes too).
func buildPathToFileID(database *db.DB, newSourceID int64,
	newSourceFiles []ingest.SourceFile, originalEvidence []db.Evidence) map[string]struct {
	FileID, SourceID int64
} {
	out := map[string]struct {
		FileID, SourceID int64
	}{}
	// New source files: look up source_file rows under newSourceID.
	if newSourceID != 0 {
		newFiles, err := database.GetSourceFiles(newSourceID)
		if err == nil {
			byPath := map[string]db.SourceFile{}
			for _, sf := range newFiles {
				byPath[sf.RelativePath] = sf
			}
			for _, f := range newSourceFiles {
				if sf, ok := byPath[f.RelativePath]; ok {
					out[f.RelativePath] = struct {
						FileID, SourceID int64
					}{FileID: sf.ID, SourceID: newSourceID}
				}
			}
		}
	}
	// Existing-source paths: walk the original evidence rows and stash
	// their (path -> source_file_id, source_id). Quotes that the LLM
	// re-cites will be attributed back to the same source row.
	if len(originalEvidence) > 0 {
		// Resolve source_file rows for each distinct source_id present.
		seenSources := map[int64]struct{}{}
		for _, e := range originalEvidence {
			seenSources[e.SourceID] = struct{}{}
		}
		for sid := range seenSources {
			files, err := database.GetSourceFiles(sid)
			if err != nil {
				continue
			}
			for _, sf := range files {
				if _, dup := out[sf.RelativePath]; dup {
					continue
				}
				out[sf.RelativePath] = struct {
					FileID, SourceID int64
				}{FileID: sf.ID, SourceID: sid}
			}
		}
	}
	return out
}

// resolveEvidenceSource picks (source_file_id, source_id) for one
// evidence row by looking up its quote's source_file path. Falls back
// to (0, fallbackSourceID) when no path matches — InsertEvidence will
// write source_file_id=NULL for the row, which is consistent with
// how legacy / fallback paths handle quotes that don't carry a path.
func resolveEvidenceSource(path string, fallbackSourceID int64,
	pathToFileID map[string]struct {
		FileID, SourceID int64
	}, _ []db.Evidence,
) (int64, int64) {
	if path == "" {
		return 0, fallbackSourceID
	}
	if hit, ok := pathToFileID[path]; ok {
		return hit.FileID, hit.SourceID
	}
	return 0, fallbackSourceID
}

// mergeSourceIDs unions an existing slice with the new-source ID in
// stable first-seen order.
func mergeSourceIDs(existing []int64, sourceID int64) []int64 {
	seen := map[int64]struct{}{}
	out := make([]int64, 0, len(existing)+1)
	for _, id := range existing {
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	if _, dup := seen[sourceID]; !dup && sourceID != 0 {
		out = append(out, sourceID)
	}
	return out
}

// writePageUpdateLog is the single audit-trail write helper. Every
// per-candidate outcome (updated / body_only / failed / skipped)
// flows through here so the row always lands. Errors are written to
// stderr — we never fail an outcome on log-write error because the
// outcome itself has already happened.
func writePageUpdateLog(database *db.DB, pageID, sourceID int64,
	priorHash, newHash, outcome, reason string, added, removed int) {
	if err := database.InsertPageUpdateLog(db.PageUpdateLogEntry{
		PageID:           pageID,
		SourceID:         sourceID,
		PriorContentHash: priorHash,
		NewContentHash:   newHash,
		Outcome:          outcome,
		Reason:           reason,
		EvidenceAdded:    added,
		EvidenceRemoved:  removed,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "  WARN write page_update_log %d/%s: %v\n", pageID, outcome, err)
	}
}

// stringWriter is a minimal strings.Builder wrapper local to this
// file so the prompt builder doesn't need to import strings just for
// one Builder.
type stringWriter struct {
	buf []byte
}

func (s *stringWriter) WriteString(t string) { s.buf = append(s.buf, t...) }
func (s *stringWriter) String() string       { return string(s.buf) }

func endsWithNewline(s string) bool {
	return len(s) > 0 && s[len(s)-1] == '\n'
}

func containsString(haystack, needle string) bool {
	if needle == "" {
		return true
	}
	// Inlined substring check — avoids a strings import in the hot path.
	hl, nl := len(haystack), len(needle)
	if nl > hl {
		return false
	}
	for i := 0; i <= hl-nl; i++ {
		match := true
		for j := 0; j < nl; j++ {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// selectUpdateCandidates is the test-exposed shortlist builder.
//
// Per new source file, unions db.SearchPages and db.SearchEvidence
// hits (keyed by page ID); excludes pages whose title is in
// newPageTitles; appends opts.ForcedCandidateIDs; caps at
// MaxCandidatesTotal; preserves ForcedCandidateIDs even past the
// cap (forced > FTS-shortlisted).
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
