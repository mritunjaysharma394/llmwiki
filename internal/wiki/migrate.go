// Package wiki — migrate.go
//
// Sub-project 7 — Phase F — `llmwiki schema migrate`.
//
// MigratePage is the per-page worker `cmd/schema migrate` walks every
// drifted-schema-hash page through. It re-reads the page's backing
// source files from disk, re-runs IngestSourceFilesToPages under the
// active schema, and runs ValidateAndAttachEvidence as usual. Pages
// whose proposed body fails validation STAY AT THEIR PRIOR VERSION —
// the trust property holds across the migrate boundary.
//
// TRUST PROPERTY. The validator is bundled and unreachable from the
// schema; it runs after every LLM call regardless of which prompt the
// schema rendered. A user-edited AGENTS.md cannot ground a false claim
// on a migrated page.
//
// The schema-hash stamp (db.UpdateSchemaHash) only fires on the
// `migrated` and `unchanged` outcomes. The `failed` outcome leaves the
// prior schema_hash in place so a re-run of `schema migrate` retries
// the page automatically (resumability via per-page hash check, Q14).
// `skipped` (page with no source_files) likewise leaves the prior hash
// untouched — there's nothing to re-ingest, so we don't claim the page
// was reconciled.
package wiki

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/mritunjaysharma394/llmwiki/internal/db"
	"github.com/mritunjaysharma394/llmwiki/internal/ingest"
	"github.com/mritunjaysharma394/llmwiki/internal/llm"
	"github.com/mritunjaysharma394/llmwiki/internal/schema"
)

// MigratePageOutcome captures the per-page verdict of a migrate run.
// Kind is one of: "migrated" | "unchanged" | "failed" | "skipped".
//
//   - migrated: validator-passed proposed body differed from prior;
//     disk + DB updated; schema_hash stamped.
//   - unchanged: validator-passed proposed body byte-identical to
//     prior; nothing rewritten on disk; schema_hash stamped so the
//     resumability skip is honoured next run.
//   - failed: validator dropped the proposed body (zero quotes
//     substring-matched). Prior version preserved on disk + DB;
//     schema_hash NOT stamped so a re-run retries.
//   - skipped: page had no source_files (typically a hand-written or
//     promoted-answer page). Nothing to re-ingest; schema_hash NOT
//     stamped — a future writer of the page will stamp it then.
type MigratePageOutcome struct {
	Kind   string
	Reason string
}

// MigratePage runs the per-page migrate pass for a single drifted page.
// Returns the outcome and, when DryRun is true, performs the LLM call
// and validator pass but skips every disk + DB write.
//
// activeSchema is the schema the page is being migrated TO (its hash
// is what gets stamped on success). wikiDir is the on-disk wiki root
// where WritePage will write the body.
func MigratePage(
	ctx context.Context,
	database *db.DB,
	client llm.Client,
	wikiDir string,
	page db.PageRecord,
	activeSchema schema.Schema,
	dryRun bool,
) (MigratePageOutcome, error) {
	// 1. A page with no backing sources can't be re-ingested. Skip
	//    cleanly — typically a hand-written or promoted-answer page.
	if len(page.SourceIDs) == 0 {
		return MigratePageOutcome{Kind: "skipped", Reason: "no source files"}, nil
	}

	// 2. Read every source's source_files from disk into ingest.SourceFile.
	//    Failures here surface as a "skipped" outcome so a missing source
	//    on disk doesn't cascade into a `failed` (which would block
	//    resumability and falsely accuse the LLM).
	files, err := LoadSourceFilesForPage(database, page)
	if err != nil {
		return MigratePageOutcome{Kind: "skipped", Reason: fmt.Sprintf("loading source files: %v", err)}, nil
	}
	if len(files) == 0 {
		return MigratePageOutcome{Kind: "skipped", Reason: "no source files readable on disk"}, nil
	}

	// 3. Re-ingest under the active schema. existing_titles is the page's
	//    own title — telling the LLM "this page already exists" so it
	//    doesn't propose a renamed twin.
	existingTitles := []string{page.Title}
	pages, err := IngestSourceFilesToPages(ctx, client, files, existingTitles, activeSchema)
	if err != nil {
		return MigratePageOutcome{Kind: "failed", Reason: fmt.Sprintf("llm call: %v", err)}, nil
	}

	// 4. Pick the proposed page that matches our title. The LLM may have
	//    emitted multiple pages from the same source set; we only care
	//    about the one we're migrating.
	var proposed *Page
	for i := range pages {
		if pages[i].Title == page.Title {
			proposed = &pages[i]
			break
		}
	}
	// 5. TRUST GATE. IngestSourceFilesToPages already ran
	//    ValidateAndAttachEvidence; a page that survived has at least
	//    one validated quote. If our title is missing (LLM dropped it
	//    entirely, or the validator dropped every quote), keep the
	//    prior version.
	if proposed == nil || len(proposed.Evidence) == 0 {
		return MigratePageOutcome{Kind: "failed", Reason: "validator dropped proposed body (zero quotes matched)"}, nil
	}

	newBody := proposed.Body
	newHash := HashContent(newBody)

	// 6. Body byte-identical to prior — just stamp the active hash and
	//    move on. No disk write, no evidence churn. Still counts as
	//    "successfully migrated to active schema" for resumability.
	if newHash == page.ContentHash {
		if !dryRun {
			if err := database.UpdateSchemaHash(page.ID, activeSchema.Hash()); err != nil {
				return MigratePageOutcome{Kind: "unchanged", Reason: fmt.Sprintf("stamp hash: %v", err)}, nil
			}
		}
		return MigratePageOutcome{Kind: "unchanged"}, nil
	}

	// 7. Body changed and validator passed. In dry-run, count and
	//    return without touching disk or DB.
	if dryRun {
		return MigratePageOutcome{Kind: "migrated"}, nil
	}

	// 8. Build the full Page write payload preserving sticky metadata
	//    (Created, SourceIDs) the existing on-disk page already had.
	now := time.Now().UTC()
	proposed.Title = page.Title
	proposed.SourceIDs = page.SourceIDs
	proposed.ContentHash = newHash
	proposed.UpdatedAt = now
	proposed.Tags = []string{"llmwiki", "ingest"}
	proposed.Sources = distinctEvidenceSourceFiles(proposed.Evidence)
	if proposed.Created.IsZero() {
		if priorPage, err := ReadPage(page.Path); err == nil && !priorPage.Created.IsZero() {
			proposed.Created = priorPage.Created
		} else {
			proposed.Created = now
		}
	}

	if err := WritePage(*proposed, wikiDir); err != nil {
		return MigratePageOutcome{Kind: "failed", Reason: fmt.Sprintf("WritePage: %v", err)}, nil
	}
	rec := db.PageRecord{
		Title:       proposed.Title,
		Path:        PagePath(wikiDir, proposed.Title),
		Body:        proposed.Body,
		ContentHash: proposed.ContentHash,
		SourceIDs:   proposed.SourceIDs,
	}
	if err := database.UpsertPage(rec); err != nil {
		return MigratePageOutcome{Kind: "failed", Reason: fmt.Sprintf("UpsertPage: %v", err)}, nil
	}

	// 9. Swap evidence — delete-old + insert-new, same shape as
	//    update_existing.go's finishUpdateCandidate. We resolve each
	//    new evidence row's source_file_id by walking the page's
	//    SourceIDs and matching relative paths.
	if err := database.DeleteEvidenceForPage(page.ID); err != nil {
		return MigratePageOutcome{Kind: "failed", Reason: fmt.Sprintf("DeleteEvidenceForPage: %v", err)}, nil
	}
	pathToFileID := buildPathToFileIDForMigrate(database, page.SourceIDs)
	evBySource := map[int64][]db.Evidence{}
	for _, e := range proposed.Evidence {
		row := db.Evidence{
			Quote:     e.Quote,
			LineStart: e.LineStart,
			LineEnd:   e.LineEnd,
		}
		var srcID int64
		if hit, ok := pathToFileID[e.SourceFilePath]; ok {
			fid := hit.FileID
			row.SourceFileID = &fid
			srcID = hit.SourceID
		} else if len(page.SourceIDs) > 0 {
			// Fallback: attribute to the page's first known source so the
			// FK still resolves. The validator already proved the quote
			// substring-matches some file in the loaded set; the source_id
			// is just bookkeeping.
			srcID = page.SourceIDs[0]
		}
		if srcID == 0 {
			continue
		}
		evBySource[srcID] = append(evBySource[srcID], row)
	}
	for sid, items := range evBySource {
		if err := database.InsertEvidence(page.ID, sid, items); err != nil {
			return MigratePageOutcome{Kind: "failed", Reason: fmt.Sprintf("InsertEvidence: %v", err)}, nil
		}
	}

	// 10. Stamp the active hash. The stamp lands ONLY on this happy
	//     path — failed/skipped branches above all return without
	//     reaching here, so the resumability invariant holds.
	if err := database.UpdateSchemaHash(page.ID, activeSchema.Hash()); err != nil {
		return MigratePageOutcome{Kind: "migrated", Reason: fmt.Sprintf("stamp hash: %v", err)}, nil
	}
	return MigratePageOutcome{Kind: "migrated"}, nil
}

// LoadSourceFilesForPage reads every source_file row backing a page's
// source_ids into []ingest.SourceFile, ready to feed back into
// IngestSourceFilesToPages. Files that fail to read on disk are
// dropped silently — the validator will catch any quote that can't
// be substring-matched against the surviving set.
//
// Exported so cmd/schema migrate can compose a per-page re-ingest
// without depending on internal/wiki's package-private helpers.
func LoadSourceFilesForPage(database *db.DB, page db.PageRecord) ([]ingest.SourceFile, error) {
	if len(page.SourceIDs) == 0 {
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
	type pathKey struct{ rel, uri string }
	seen := map[pathKey]struct{}{}
	var out []ingest.SourceFile
	for _, sid := range page.SourceIDs {
		src, ok := bySourceID[sid]
		if !ok {
			continue
		}
		files, err := database.GetSourceFiles(sid)
		if err != nil {
			continue
		}
		for _, sf := range files {
			key := pathKey{rel: sf.RelativePath, uri: src.URI}
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			bytes, err := readSourceFileContent(src.URI, sf.RelativePath)
			if err != nil {
				continue
			}
			out = append(out, ingest.NewSourceFile(sf.RelativePath, bytes))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RelativePath < out[j].RelativePath })
	return out, nil
}

// buildPathToFileIDForMigrate is the migrate-flavoured cousin of
// update_existing.go's buildPathToFileID: it resolves
// (relative_path -> {source_file_id, source_id}) for every source
// the migrating page already knew about. We use it to write
// FK-correct evidence rows on the migrated path.
func buildPathToFileIDForMigrate(database *db.DB, sourceIDs []int64) map[string]struct {
	FileID, SourceID int64
} {
	out := map[string]struct {
		FileID, SourceID int64
	}{}
	for _, sid := range sourceIDs {
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
	return out
}
