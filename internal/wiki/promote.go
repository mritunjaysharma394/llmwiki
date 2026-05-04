// Package wiki — promote.go
//
// PromoteAnswer lifts a saved-answer file (cmd/ask.go's
// .llmwiki/answers/<ts>-<slug>.md output) into a permanent wiki page,
// running the same trust-validator (ValidateAndAttachEvidence) over the
// parsed evidence quotes against current on-disk source bytes before
// writing anything. When the source has changed since ask-time so that
// no quote substring-matches its named source_file, PromoteAnswer
// returns ErrEvidenceInvalid and writes nothing — the same trust
// property cmd/ingest and mcp.write_page enforce.
//
// The disk + DB write path mirrors mcp.write_page (Phase F's MCP handler
// will mirror this code shape back). readSourceFileContent moved here
// from internal/mcp/handlers.go so both consumers share one
// implementation; internal/mcp now imports the wiki version to avoid an
// import cycle (wiki -> mcp -> wiki).
package wiki

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mritunjaysharma394/llmwiki/internal/db"
	"github.com/mritunjaysharma394/llmwiki/internal/ingest"
	"github.com/mritunjaysharma394/llmwiki/internal/llm"
)

// ErrEvidenceInvalid is returned when defensive re-validation of an
// answer's quotes drops every quote (typically because a source file
// changed between ask-time and promote-time). The error carries a
// PromoteResult.DroppedQuotes payload naming each failed quote and the
// reason. Mirrors mcp.write_page's "evidence_invalid" structured-error
// code so Phase F's MCP handler can return the same shape.
var ErrEvidenceInvalid = errors.New("evidence_invalid")

// ErrTitleExists is returned when the promoted page's title would
// collide with a pre-existing wiki page. Mirrors mcp.write_page's
// "title_exists" structured-error code; the existing page's path is
// returned in PromoteResult.Path so callers can render the collision.
var ErrTitleExists = errors.New("title_exists")

// PromoteOptions captures the per-call knobs the cobra command (and
// future MCP handler) translate from their flag/argument surface.
//
// Title overrides the question-derived default ("" → derive from the
// answer's `question:` frontmatter via slugify-then-Title-Case).
// Rewrite triggers one extra LLM call to rewrite the answer body into
// wiki prose; rewrite output that fails defensive re-validation against
// the parsed quotes falls back to the verbatim answer body and emits a
// WARN to stderr — never fails the command.
// NoSave skips appending a `**promote**` line to log.md; debug-only.
type PromoteOptions struct {
	Title   string
	Rewrite bool
	NoSave  bool
}

// PromoteResult is the structured success / failure shape PromoteAnswer
// returns. On ErrEvidenceInvalid, DroppedQuotes is populated and
// EvidenceQuotes is 0; the page never reaches disk. On ErrTitleExists,
// Title and Path point at the colliding existing page.
//
// RetroLinkedTitles is reserved for Phase D's RetroLinkPages
// integration; the standalone PromoteAnswer leaves it empty.
type PromoteResult struct {
	Title             string
	Path              string
	EvidenceQuotes    int
	DroppedQuotes     []DroppedQuote
	RewriteApplied    bool
	RetroLinkedTitles []string
}

// DroppedQuote is one row in PromoteResult.DroppedQuotes. Quote is the
// raw text from the answer file (already strconv.Unquote'd by
// ParseSavedAnswer); SourceFile is the relative path the answer named;
// Reason is a short human-readable explanation matching the codes
// mcp.write_page uses.
type DroppedQuote struct {
	Quote      string
	SourceFile string
	Reason     string
}

// PromoteAnswer parses a saved-answer file, defensively re-validates
// every evidence quote against the current on-disk source bytes via
// ValidateAndAttachEvidence, and on success lands a real wiki page on
// disk + in the DB through the same write path cmd/ingest and
// mcp.write_page use.
//
// Trust property: the function returns ErrEvidenceInvalid (and writes
// nothing to disk or DB) when any quote fails the byte-exact substring
// match against its named source_file. ValidateAndAttachEvidence is the
// single gatekeeper.
func PromoteAnswer(ctx context.Context, cfg IngestSourceConfig, database *db.DB, client llm.Client, answerPath string, opts PromoteOptions) (PromoteResult, error) {
	var out PromoteResult

	// 1. Read + parse the answer file.
	raw, err := os.ReadFile(answerPath)
	if err != nil {
		return out, fmt.Errorf("reading answer: %w", err)
	}
	parsed, err := ParseSavedAnswer(string(raw))
	if err != nil {
		return out, fmt.Errorf("parsing answer: %w", err)
	}
	if len(parsed.Pages) == 0 {
		// Phase A surprise: an answer with an empty Sources block parses
		// successfully but yields no pages. Promotion without evidence
		// violates the trust property, so reject early.
		return out, fmt.Errorf("answer has no source quotes; cannot promote: %w", ErrEvidenceInvalid)
	}

	// Resolve title.
	title := opts.Title
	if title == "" {
		title = titleFromQuestion(parsed.Question)
	}
	if title == "" {
		return out, fmt.Errorf("could not derive title from question %q; pass --title", parsed.Question)
	}
	out.Title = title

	// 2. Title collision: refuse early.
	if existing, err := database.GetPage(title); err != nil {
		return out, fmt.Errorf("checking existing page: %w", err)
	} else if existing != nil {
		out.Path = existing.Path
		return out, ErrTitleExists
	}

	// 3. Collect distinct source_file paths from parsed evidence and
	//    reject legacy quotes (no SourceFilePath) before any disk read.
	parsedEvidence := flattenAnswerEvidence(parsed.Pages)
	if len(parsedEvidence) == 0 {
		return out, fmt.Errorf("answer has no evidence quotes; cannot promote: %w", ErrEvidenceInvalid)
	}
	for _, e := range parsedEvidence {
		if e.SourceFilePath == "" {
			return out, fmt.Errorf("answer contains a legacy (lines a-b) quote with no source_file; the answer pre-dates sub-project 3 — re-run `ask` and promote the new answer: %w", ErrEvidenceInvalid)
		}
	}

	// 4. Resolve every named source_file against the DB. Pattern mirrors
	//    mcp.write_page's byPath lookup so source resolution and error
	//    codes line up.
	sources, err := database.GetAllSources()
	if err != nil {
		return out, fmt.Errorf("loading sources: %w", err)
	}
	type sfRef struct {
		sourceURI string
		file      db.SourceFile
	}
	byPath := map[string]sfRef{}
	for _, s := range sources {
		files, err := database.GetSourceFiles(s.ID)
		if err != nil {
			continue
		}
		for _, f := range files {
			if _, taken := byPath[f.RelativePath]; taken {
				continue
			}
			byPath[f.RelativePath] = sfRef{sourceURI: s.URI, file: f}
		}
	}

	// 5. Read content fresh from disk for each named source_file. This
	//    is the defensive part: the ask was against possibly-older bytes;
	//    we re-validate against what's there now.
	fileSet := map[string]ingest.SourceFile{}
	for _, e := range parsedEvidence {
		if _, dup := fileSet[e.SourceFilePath]; dup {
			continue
		}
		ref, ok := byPath[e.SourceFilePath]
		if !ok {
			out.DroppedQuotes = append(out.DroppedQuotes, DroppedQuote{
				Quote:      e.Quote,
				SourceFile: e.SourceFilePath,
				Reason:     "source_file not in DB; run `llmwiki ingest` against the source first",
			})
			continue
		}
		content, err := readSourceFileContent(ref.sourceURI, ref.file.RelativePath)
		if err != nil {
			out.DroppedQuotes = append(out.DroppedQuotes, DroppedQuote{
				Quote:      e.Quote,
				SourceFile: e.SourceFilePath,
				Reason:     fmt.Sprintf("source not readable from %q: %v", ref.sourceURI, err),
			})
			continue
		}
		fileSet[e.SourceFilePath] = ingest.NewSourceFile(e.SourceFilePath, content)
	}

	// 6. Build candidate Page with the parsed quotes + verbatim answer
	//    body. Run ValidateAndAttachEvidence — the single trust gate.
	body := strings.TrimRight(parsed.Answer, "\n")
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	candidate := Page{
		Title:    title,
		Body:     body,
		Evidence: parsedEvidence,
	}
	ingestFiles := make([]ingest.SourceFile, 0, len(fileSet))
	for _, f := range fileSet {
		ingestFiles = append(ingestFiles, f)
	}
	validated, _ := ValidateAndAttachEvidence([]Page{candidate}, ingestFiles)

	// 7. If validation drops every quote, no page reaches disk; build the
	//    DroppedQuotes payload so the caller can show the user which
	//    quotes failed and why.
	if len(validated) == 0 {
		// Augment any quotes that resolved to a known file but failed the
		// substring check; quotes that bailed earlier already carry their
		// reason from the resolution loop.
		for _, e := range parsedEvidence {
			if alreadyDropped(out.DroppedQuotes, e.Quote, e.SourceFilePath) {
				continue
			}
			f, ok := fileSet[e.SourceFilePath]
			switch {
			case !ok:
				// Already covered above; defensive.
				out.DroppedQuotes = append(out.DroppedQuotes, DroppedQuote{
					Quote:      e.Quote,
					SourceFile: e.SourceFilePath,
					Reason:     "source_file not in resolved set",
				})
			case !strings.Contains(f.Content, e.Quote):
				out.DroppedQuotes = append(out.DroppedQuotes, DroppedQuote{
					Quote:      e.Quote,
					SourceFile: e.SourceFilePath,
					Reason:     "quote not a byte-exact substring of the named source_file (source likely changed since the ask)",
				})
			}
		}
		return out, ErrEvidenceInvalid
	}
	page := validated[0]

	// 8. Optional rewrite. If the rewrite returns a body whose evidence
	//    quotes can't all be substring-validated against the same file
	//    set, fall back to the verbatim body and warn (never fails).
	if opts.Rewrite && client != nil {
		rewritten, rerr := rewritePromoteBody(ctx, client, parsed.Question, body, page.Evidence)
		switch {
		case rerr != nil:
			fmt.Fprintf(os.Stderr, "  WARN rewrite call failed (%v); falling back to verbatim body\n", rerr)
		case !rewriteEvidencesValid(rewritten, page.Evidence, ingestFiles):
			fmt.Fprintln(os.Stderr, "  WARN rewrite produced unverifiable body; falling back to verbatim")
		default:
			page.Body = rewritten
			out.RewriteApplied = true
		}
	}

	// 9. Stamp tags / sources / created and run wikilink rewrite. Same
	//    pattern as mcp.write_page step 5.
	srcIDSet := map[int64]struct{}{}
	var srcIDs []int64
	for _, e := range page.Evidence {
		ref, ok := byPath[e.SourceFilePath]
		if !ok {
			continue
		}
		if _, dup := srcIDSet[ref.file.SourceID]; dup {
			continue
		}
		srcIDSet[ref.file.SourceID] = struct{}{}
		srcIDs = append(srcIDs, ref.file.SourceID)
	}
	now := time.Now().UTC()
	page.SourceIDs = srcIDs
	page.UpdatedAt = now
	if page.Created.IsZero() {
		page.Created = now
	}
	page.ContentHash = HashContent(page.Body)
	page.Tags = []string{"llmwiki", "promote"}
	page.Sources = distinctEvidenceSources(page.Evidence)

	titles, err := database.AllPageTitles()
	if err != nil {
		return out, fmt.Errorf("loading page titles: %w", err)
	}
	titles = append(titles, page.Title)
	page.Body = RewriteBareReferencesAsWikilinks(page.Body, titles)

	// 10. Disk + DB writes (mirrors mcp.write_page step 6).
	if err := os.MkdirAll(cfg.WikiDir, 0755); err != nil {
		return out, fmt.Errorf("mkdir wiki dir: %w", err)
	}
	pagePath := PagePath(cfg.WikiDir, page.Title)
	if err := WritePage(page, cfg.WikiDir); err != nil {
		return out, fmt.Errorf("writing page: %w", err)
	}
	rec := db.PageRecord{
		Title:       page.Title,
		Path:        pagePath,
		Body:        page.Body,
		ContentHash: page.ContentHash,
		SourceIDs:   page.SourceIDs,
	}
	if err := database.UpsertPage(rec); err != nil {
		return out, fmt.Errorf("db upsert: %w", err)
	}
	stored, err := database.GetPage(page.Title)
	if err != nil || stored == nil {
		return out, fmt.Errorf("re-fetching written page: %v", err)
	}

	// Group evidence by source so InsertEvidence (one source per call)
	// resolves FKs correctly.
	evBySource := map[int64][]db.Evidence{}
	for _, e := range page.Evidence {
		ref, ok := byPath[e.SourceFilePath]
		if !ok {
			continue
		}
		sfid := ref.file.ID
		evBySource[ref.file.SourceID] = append(evBySource[ref.file.SourceID], db.Evidence{
			Quote:        e.Quote,
			LineStart:    e.LineStart,
			LineEnd:      e.LineEnd,
			SourceFileID: &sfid,
		})
	}
	evidenceCount := 0
	for sid, items := range evBySource {
		if err := database.InsertEvidence(stored.ID, sid, items); err != nil {
			return out, fmt.Errorf("insert evidence: %w", err)
		}
		evidenceCount += len(items)
	}
	out.EvidenceQuotes = evidenceCount

	if len(page.Links) > 0 {
		var links []db.Link
		for _, l := range page.Links {
			links = append(links, db.Link{FromPage: page.Title, ToPage: l.To, LinkType: l.Type})
		}
		_ = database.UpsertLinks(page.Title, links)
	}

	// Phase D (sub-project 6a): retro-link existing pages to the new
	// title. Body-only, idempotent; evidence rows untouched. Runs BEFORE
	// RegenerateIndex so index.md reflects the bumped updated_at on any
	// rewritten existing pages.
	retroRes, rlErr := RetroLinkPages(database, cfg.WikiDir, []string{page.Title})
	if rlErr != nil {
		fmt.Fprintf(os.Stderr, "  WARN retro-linking existing pages after promote: %v\n", rlErr)
	}
	out.RetroLinkedTitles = retroRes.UpdatedTitles

	// 11. Side files: regenerate index, append log unless NoSave.
	allPageRecs, err := database.AllPages()
	if err == nil {
		allSources, _ := database.GetAllSources()
		if rerr := RegenerateIndex(cfg.WikiDir, allPageRecs, allSources, time.Now().UTC()); rerr != nil {
			fmt.Fprintf(os.Stderr, "  WARN regenerating index.md after promote: %v\n", rerr)
		}
	} else {
		fmt.Fprintf(os.Stderr, "  WARN reading pages for index after promote: %v\n", err)
	}
	if !opts.NoSave {
		_ = AppendLog(cfg.WikiDir, LogEntry{
			At:      time.Now().UTC(),
			Kind:    "promote",
			Payload: fmt.Sprintf("%s → %d evidence", page.Title, evidenceCount),
		})
	}

	out.Path = pagePath
	return out, nil
}

// flattenAnswerEvidence walks every parsed page's Evidence and returns a
// single deduped slice. Quotes are deduped on (Quote, SourceFilePath)
// pairs in case the answer cites the same span from multiple pages.
func flattenAnswerEvidence(pages []Page) []Evidence {
	seen := map[string]struct{}{}
	var out []Evidence
	for _, p := range pages {
		for _, e := range p.Evidence {
			key := e.Quote + "\x00" + e.SourceFilePath
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, e)
		}
	}
	return out
}

func alreadyDropped(list []DroppedQuote, quote, sourceFile string) bool {
	for _, d := range list {
		if d.Quote == quote && d.SourceFile == sourceFile {
			return true
		}
	}
	return false
}

// titleFromQuestion turns "how does the validator work?" into
// "How Does The Validator Work" — slugify (lowercase + dash separators)
// then Title-Case the dash-separated tokens.
func titleFromQuestion(q string) string {
	q = strings.TrimSpace(q)
	if q == "" {
		return ""
	}
	slug := simpleSlugify(q)
	if slug == "" {
		return ""
	}
	parts := strings.Split(slug, "-")
	for i, p := range parts {
		if p == "" {
			continue
		}
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, " ")
}

// simpleSlugify mirrors cmd/ask.go:slugify exactly. Inlined here so
// internal/wiki doesn't have to import cmd. Lowercase ASCII letters and
// digits are kept verbatim; any other rune folds to a single '-'
// separator; trailing dashes are trimmed; result is capped at 60 chars.
func simpleSlugify(s string) string {
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

// promoteRewriteSystemPrompt is the v0.6 inline rewrite system prompt
// hoisted to a const (Phase B Task 4) so the byte-equality test can
// pin it; production reads now flow through schema.Bundled().Render
// in Task 5.
const promoteRewriteSystemPrompt = `You rewrite an LLM-generated answer into a polished wiki page body.

Preserve every verbatim source quote that appears in the input verbatim — they are
the load-bearing evidence the wiki's trust validator will re-check. You may
restructure prose, add headings, and tighten paragraphs; you may NOT alter,
shorten, or paraphrase any quoted span.

Return Markdown only — no preamble, no closing remarks, just the page body.`

// PromoteRewriteSystemPromptForTests exposes the v0.6 rewrite system
// prompt for internal/schema's byte-equality test. Removed in v0.8 once
// the schema-driven path is the only path.
func PromoteRewriteSystemPromptForTests() string { return promoteRewriteSystemPrompt }

// rewritePromoteBody asks the LLM for a wiki-prose rewrite of the answer
// body, gently nudged to keep every evidence quote intact. Returns the
// raw rewritten string; the caller validates and falls back on failure.
func rewritePromoteBody(ctx context.Context, client llm.Client, question, body string, ev []Evidence) (string, error) {
	var sb strings.Builder
	sb.WriteString("Question:\n")
	sb.WriteString(question)
	sb.WriteString("\n\nAnswer body:\n")
	sb.WriteString(body)
	sb.WriteString("\n\nVerbatim quotes that must survive verbatim in your rewrite:\n")
	for _, e := range ev {
		sb.WriteString(fmt.Sprintf("- %q\n", e.Quote))
	}
	return client.Complete(ctx, promoteRewriteSystemPrompt, sb.String())
}

// rewriteEvidencesValid checks that every parsed quote still appears
// verbatim in the rewritten body. The plan's contract: a rewrite is
// only acceptable when the wiki-prose body still contains the
// load-bearing evidence quotes verbatim — otherwise the page body and
// its frontmatter `evidence:` block diverge and a reader can't ground
// claims back to the body. files is unused in this check (kept for
// signature symmetry with ValidateAndAttachEvidence) but we still
// belt-and-braces the source-file substring match in case the
// caller-passed quotes drifted.
func rewriteEvidencesValid(rewritten string, ev []Evidence, files []ingest.SourceFile) bool {
	if len(ev) == 0 || strings.TrimSpace(rewritten) == "" {
		return false
	}
	byPath := make(map[string]*ingest.SourceFile, len(files))
	for i := range files {
		byPath[files[i].RelativePath] = &files[i]
	}
	for _, e := range ev {
		// Rewrite must preserve every quote verbatim in the body.
		if !strings.Contains(rewritten, e.Quote) {
			return false
		}
		// Defensive: quote must still substring-match its source file.
		f, ok := byPath[e.SourceFilePath]
		if !ok {
			return false
		}
		if !strings.Contains(f.Content, e.Quote) {
			return false
		}
	}
	return true
}

// readSourceFileContent reads the bytes of source_file <relPath> living
// under <sourceURI>. For local paths it reads sourceURI as a file (when
// relPath is the basename of sourceURI) or sourceURI/relPath (when
// sourceURI is a directory). HTTP/HTTPS URIs return an error — the v1.1
// trust assumption is that the agent re-ingests before promote/write so
// the bytes live on disk somewhere.
//
// Lifted from internal/mcp/handlers.go so internal/mcp can call it
// without internal/wiki importing internal/mcp (which would create a
// cycle: internal/mcp already imports internal/wiki).
func readSourceFileContent(sourceURI, relPath string) ([]byte, error) {
	if data, err := os.ReadFile(sourceURI); err == nil {
		return data, nil
	}
	candidate := filepath.Join(sourceURI, relPath)
	if data, err := os.ReadFile(candidate); err == nil {
		return data, nil
	}
	if strings.HasPrefix(sourceURI, "http://") || strings.HasPrefix(sourceURI, "https://") {
		return nil, fmt.Errorf("HTTP source — re-ingest before promote/write_page")
	}
	return nil, fmt.Errorf("not found at %q or %q", sourceURI, filepath.Join(sourceURI, relPath))
}

// ReadSourceFileContent is the public shim internal/mcp uses to delegate
// to the wiki package's implementation. Kept as a thin wrapper so
// callers can spell the import without exposing the internal helper's
// signature to the world.
func ReadSourceFileContent(sourceURI, relPath string) ([]byte, error) {
	return readSourceFileContent(sourceURI, relPath)
}
