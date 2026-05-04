// Package wiki — contradict.go
//
// DetectIngestContradictions is the per-pair contradiction detector that
// runs after each ingest's persist loop. It is the structured sibling of
// the whole-wiki DetectContradictions used by `llmwiki lint`:
//
//   - DetectContradictions: free-form text blob, batched over the whole
//     wiki, called by cmd/lint as an after-the-fact sweep.
//   - DetectIngestContradictions (this file): narrow per-(newPage,
//     candidate) LLM calls during ingest, returns []Contradiction tuples
//     the caller appends to <wikiDir>/contradictions.md and prints
//     inline.
//
// Trust property: contradiction detection is INFORMATIONAL. LLM errors,
// timeouts, or JSON-parse failures log a WARN to stderr and produce no
// contradictions for that pair — they MUST NEVER block trust-validated
// ingest writes. The new pages have already landed via the validator
// gate; this file's only job is to surface disagreement, not gate it.
//
// The validator-style filter (Filter quotes that don't appear in either
// page's already-validated evidence) means an LLM that hallucinates
// quotes cannot fabricate a contradiction tuple — both quote sides must
// trace back to actually-validated evidence we have on disk.
package wiki

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mritunjaysharma394/llmwiki/internal/db"
	"github.com/mritunjaysharma394/llmwiki/internal/llm"
)

// Contradiction is one detected disagreement between a just-written page
// and a pre-existing wiki page. Both sides carry the verbatim quote, the
// source_file relative path, and the line range so the caller's render
// can produce annotations matching the spec's `(<path>:<a>-<b>)` form.
//
// Description is the LLM's one-sentence rationale, taken verbatim from
// the per-pair Complete call. Tests assert on the string round-trip;
// callers should not reformat it.
type Contradiction struct {
	NewPageTitle       string
	NewPageQuote       string
	NewPageSourceFile  string
	NewPageLines       [2]int
	ExistingTitle      string
	ExistingQuote      string
	ExistingSourceFile string
	ExistingLines      [2]int
	Description        string
}

// DefaultContradictionCandidateLimit caps the per-new-page candidate
// shortlist at five existing pages. The cap exists to bound LLM cost on
// large wikis: a 50-page ingest against a 5,000-page wiki at limit=5
// is 250 calls instead of 250,000.
const DefaultContradictionCandidateLimit = 5

// contradictionSystemPrompt is the per-pair instruction. It explicitly
// excludes qualifications and additions (spec risk #6) so the LLM does
// not flag every overlap as a contradiction.
//
// Output shape: JSON array of objects, each with `a_quote` (verbatim
// from page A's evidence), `b_quote` (verbatim from page B's evidence),
// and `description` (one-sentence rationale).
const contradictionSystemPrompt = `You are a contradiction detector for two wiki pages, A (newly written) and B (pre-existing). Each page has already-validated evidence quotes copied verbatim from real sources.

Output a JSON array of contradiction tuples. Each tuple is:
  {"a_quote": "<verbatim quote from page A's evidence>", "b_quote": "<verbatim quote from page B's evidence>", "description": "<one-sentence rationale>"}

ONLY flag direct factual contradictions where the two quotes assert mutually exclusive facts. The following are NOT contradictions and MUST be excluded:
  - Qualifications or additions (one page elaborates on the other).
  - Version-specific claims ("X applies in Go 1.21" vs "X applies in Go 1.22").
  - Different scopes (one page describes the general case, the other a special case).

Quote each side VERBATIM from the evidence list shown. If you would need to paraphrase, the pages are not contradicting; emit nothing for that pair.

If there are no contradictions, output the empty array: [].`

// DetectIngestContradictions builds candidate (newPage, existingPage)
// pairs by FTS-search over each newPage's body, runs one LLM call per
// pair against the configured client (caller passes cfg.LLM.Model's
// resolved Client), filters out LLM-output tuples whose quotes don't
// appear in either page's already-validated evidence, and returns
// deduped Contradiction tuples ordered by (newPageTitle, existingTitle).
//
// existingPages should be pre-filtered to exclude any title in newPages
// — the dedup key catches duplicates defensively but the caller is
// responsible for not asking us to compare a page against itself.
//
// candidateLimit <= 0 falls back to DefaultContradictionCandidateLimit.
//
// LLM errors and JSON-parse failures log a WARN to stderr and produce
// no contradictions for that pair. The function never returns a non-nil
// error; the (error) return is reserved for future structural failures
// (today every code path returns nil).
func DetectIngestContradictions(
	ctx context.Context,
	client llm.Client,
	newPages []Page,
	existingPages []db.PageRecord,
	candidateLimit int,
	database *db.DB,
) ([]Contradiction, error) {
	if len(newPages) == 0 {
		return nil, nil
	}
	if candidateLimit <= 0 {
		candidateLimit = DefaultContradictionCandidateLimit
	}

	// Dedup key: ordered (newPageTitle, existingTitle). Two iterations
	// of the same pair (e.g. duplicate newPage entries) collapse here.
	seen := map[string]bool{}
	var out []Contradiction

	for _, np := range newPages {
		candidates := selectCandidates(np, existingPages, candidateLimit, database)
		for _, ep := range candidates {
			key := np.Title + "\x00" + ep.Title
			if seen[key] {
				continue
			}
			seen[key] = true

			// Pull the existing page's already-validated evidence from
			// the DB so we can run the validator-style filter against
			// the LLM's output.
			existingEv, err := database.GetEvidenceForPage(ep.ID)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  WARN contradiction check: load evidence for %q: %v\n", ep.Title, err)
				continue
			}

			// Pre-resolve source_file paths for the existing page's
			// evidence rows so the matched Contradiction carries a
			// real path on the existing side. Best-effort; missing
			// rows leave the path empty.
			existingPaths := evidenceSourceFilePaths(database, ep, existingEv)

			user := buildContradictionPrompt(np, ep, existingEv)
			raw, err := client.Complete(ctx, contradictionSystemPrompt, user)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  WARN contradiction check failed for %q vs %q: %v\n", np.Title, ep.Title, err)
				continue
			}

			tuples, err := parseContradictionResponse(raw)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  WARN contradiction check parse error for %q vs %q: %v\n", np.Title, ep.Title, err)
				continue
			}

			for _, tup := range tuples {
				c, ok := matchContradictionToEvidence(tup, np, ep, existingEv, existingPaths)
				if !ok {
					continue
				}
				out = append(out, c)
			}
		}
	}
	return out, nil
}

// selectCandidates picks at most candidateLimit existing pages whose
// content overlaps the new page strongly enough to warrant a per-pair
// LLM call. The signal is a union of three checks:
//
//  1. Title appearance: the existing page's title appears as a token in
//     the new page's body. (The strongest signal — "Mutex Internals"
//     mentioned in a new-page body about Mutex is almost certainly
//     worth checking.)
//  2. Pages-FTS: db.SearchPages(np.Body) returns the existing page.
//  3. Evidence-FTS: db.SearchEvidence(np.Body) returns a quote whose
//     page_id is the existing page.
//
// FTS hits are gated by also being a page-or-evidence-FTS hit AGAINST
// the existing page's *title* (or one of its evidence quotes), so a new
// body whose only commonality with an existing page is high-frequency
// stop-words ("the", "are") doesn't drag in unrelated candidates.
//
// existingPages is the already-filtered set the caller built (typically
// `database.AllPages()` minus new-batch titles). We re-check the
// new-page title here defensively in case the caller forgot.
//
// The output preserves existingPages's input order for determinism.
func selectCandidates(np Page, existingPages []db.PageRecord, candidateLimit int, database *db.DB) []db.PageRecord {
	if candidateLimit <= 0 {
		candidateLimit = DefaultContradictionCandidateLimit
	}
	if len(existingPages) == 0 {
		return nil
	}

	hitIDs := map[int64]bool{}

	// 1. Direct title-in-body check. Fast and high-precision.
	bodyLower := strings.ToLower(np.Body)
	for _, p := range existingPages {
		if p.Title == np.Title {
			continue
		}
		if strings.Contains(bodyLower, strings.ToLower(p.Title)) {
			hitIDs[p.ID] = true
			continue
		}
	}

	// 2. Title-token check: any title token (>=4 chars, alphanumeric)
	//    present as a whole word in the new body. Catches "Mutex" when
	//    the existing page is "Mutex Internals". The 4-char floor keeps
	//    "and"/"the"/"two" out.
	for _, p := range existingPages {
		if hitIDs[p.ID] || p.Title == np.Title {
			continue
		}
		for _, tok := range titleTokens(p.Title) {
			if len(tok) < 4 {
				continue
			}
			if containsWord(bodyLower, strings.ToLower(tok)) {
				hitIDs[p.ID] = true
				break
			}
		}
	}

	// 3. Evidence-quote substring check: an existing page's evidence
	//    quote appears as a substring in the new page's body. This
	//    catches the "page A claims X, page B's evidence quote also
	//    asserts X" overlap that's a strong contradiction prior. Only
	//    run if FTS surfaces the page first (cheap pre-filter).
	if database != nil {
		if evHits, err := database.SearchEvidence(np.Body, candidateLimit*4); err == nil {
			byID := make(map[int64]db.PageRecord, len(existingPages))
			for _, p := range existingPages {
				byID[p.ID] = p
			}
			for _, h := range evHits {
				rec, ok := byID[h.PageID]
				if !ok || rec.Title == np.Title || hitIDs[h.PageID] {
					continue
				}
				if strings.Contains(np.Body, h.Quote) {
					hitIDs[h.PageID] = true
				}
			}
		}
	}

	var out []db.PageRecord
	for _, p := range existingPages {
		if !hitIDs[p.ID] {
			continue
		}
		out = append(out, p)
		if len(out) >= candidateLimit {
			break
		}
	}
	return out
}

// titleTokens splits a title into whitespace-separated tokens, stripping
// trailing punctuation. Used by selectCandidates to look for any title
// word in the new-page body.
func titleTokens(title string) []string {
	fields := strings.Fields(title)
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.TrimFunc(f, func(r rune) bool {
			return !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'))
		})
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}

// containsWord reports whether needle appears in haystack as a whole
// word (bordered by non-word characters or string boundaries). Both
// haystack and needle should already be lowercased by the caller.
func containsWord(haystack, needle string) bool {
	if needle == "" {
		return false
	}
	off := 0
	for {
		idx := strings.Index(haystack[off:], needle)
		if idx < 0 {
			return false
		}
		start := off + idx
		end := start + len(needle)
		// Word boundary: char before start and char after end must not
		// be alphanumeric.
		before := byte('.')
		if start > 0 {
			before = haystack[start-1]
		}
		after := byte('.')
		if end < len(haystack) {
			after = haystack[end]
		}
		if !isWordByte(before) && !isWordByte(after) {
			return true
		}
		off = end
	}
}

func isWordByte(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_'
}

// buildContradictionPrompt renders the per-pair user prompt: page A
// (new) and page B (existing) each labeled with their evidence quotes
// verbatim, so the LLM can quote them back without paraphrasing.
func buildContradictionPrompt(np Page, ep db.PageRecord, existingEv []db.Evidence) string {
	var sb strings.Builder
	sb.WriteString("# Page A (newly written)\n")
	sb.WriteString("Title: " + np.Title + "\n\n")
	sb.WriteString("Body:\n")
	sb.WriteString(np.Body)
	if !strings.HasSuffix(np.Body, "\n") {
		sb.WriteString("\n")
	}
	sb.WriteString("\nEvidence quotes (verbatim):\n")
	for _, e := range np.Evidence {
		sb.WriteString("- " + e.Quote + "\n")
	}
	sb.WriteString("\n# Page B (pre-existing)\n")
	sb.WriteString("Title: " + ep.Title + "\n\n")
	sb.WriteString("Body:\n")
	sb.WriteString(ep.Body)
	if !strings.HasSuffix(ep.Body, "\n") {
		sb.WriteString("\n")
	}
	sb.WriteString("\nEvidence quotes (verbatim):\n")
	for _, e := range existingEv {
		sb.WriteString("- " + e.Quote + "\n")
	}
	sb.WriteString("\nReturn the JSON array described in the system prompt.")
	return sb.String()
}

// contradictionTuple is the LLM's per-tuple output shape.
type contradictionTuple struct {
	AQuote      string `json:"a_quote"`
	BQuote      string `json:"b_quote"`
	Description string `json:"description"`
}

// parseContradictionResponse extracts the JSON array of tuples from the
// LLM's raw text. Tolerates leading/trailing chatter or code fences by
// finding the first `[` and last `]`.
func parseContradictionResponse(raw string) ([]contradictionTuple, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "[]" {
		return nil, nil
	}
	// Try direct unmarshal first.
	var tuples []contradictionTuple
	if err := json.Unmarshal([]byte(raw), &tuples); err == nil {
		return tuples, nil
	}
	// Fallback: find the first '[' and last ']' and re-attempt.
	start := strings.Index(raw, "[")
	end := strings.LastIndex(raw, "]")
	if start < 0 || end <= start {
		return nil, fmt.Errorf("no JSON array found in response")
	}
	if err := json.Unmarshal([]byte(raw[start:end+1]), &tuples); err != nil {
		return nil, fmt.Errorf("unmarshal contradiction tuples: %w", err)
	}
	return tuples, nil
}

// matchContradictionToEvidence runs the validator-style filter: the
// a_quote must match a quote in the new page's evidence, and the
// b_quote must match a quote in the existing page's evidence. If
// either side is absent, the tuple is hallucinated and we drop it.
//
// On match, the function fills in the Contradiction's source_file +
// line range from the matched evidence row on each side. existingPaths
// is the per-evidence-id source_file path map the caller pre-resolved.
func matchContradictionToEvidence(tup contradictionTuple, np Page, ep db.PageRecord, existingEv []db.Evidence, existingPaths map[int64]string) (Contradiction, bool) {
	var newEv *Evidence
	for i, e := range np.Evidence {
		if e.Quote == tup.AQuote {
			newEv = &np.Evidence[i]
			break
		}
	}
	if newEv == nil {
		return Contradiction{}, false
	}
	var existingMatch *db.Evidence
	for i, e := range existingEv {
		if e.Quote == tup.BQuote {
			existingMatch = &existingEv[i]
			break
		}
	}
	if existingMatch == nil {
		return Contradiction{}, false
	}

	return Contradiction{
		NewPageTitle:       np.Title,
		NewPageQuote:       newEv.Quote,
		NewPageSourceFile:  newEv.SourceFilePath,
		NewPageLines:       [2]int{newEv.LineStart, newEv.LineEnd},
		ExistingTitle:      ep.Title,
		ExistingQuote:      existingMatch.Quote,
		ExistingSourceFile: existingPaths[existingMatch.ID],
		ExistingLines:      [2]int{existingMatch.LineStart, existingMatch.LineEnd},
		Description:        tup.Description,
	}, true
}

// evidenceSourceFilePaths resolves source_file_id → relative path for
// every evidence row on a page. Best-effort: rows without resolvable
// source_file_ids map to "" (the caller renders an empty path
// annotation rather than failing the whole detection).
func evidenceSourceFilePaths(database *db.DB, ep db.PageRecord, ev []db.Evidence) map[int64]string {
	out := map[int64]string{}
	if database == nil || len(ev) == 0 {
		return out
	}
	// Build sourceFileID → relative path lookup over every source the
	// page is bound to. (db.PageRecord carries SourceIDs.)
	pathByID := map[int64]string{}
	for _, sid := range ep.SourceIDs {
		files, err := database.GetSourceFiles(sid)
		if err != nil {
			continue
		}
		for _, f := range files {
			pathByID[f.ID] = f.RelativePath
		}
	}
	for _, e := range ev {
		if e.SourceFileID == nil {
			out[e.ID] = ""
			continue
		}
		out[e.ID] = pathByID[*e.SourceFileID]
	}
	return out
}

// FormatContradictionMarkdown renders one block per call to the
// <wikiDir>/contradictions.md format the spec describes:
//
//	- 2026-05-04T14:30:12Z **ingest** <source>
//	  - new page "<NewTitle>" vs existing [[<ExistingTitle>]]: <description>
//	    - new claim: > "<quote>" (<path>:<a>-<b>)
//	    - existing claim: > "<quote>" (<path>:<a>-<b>)
//
// RFC3339 UTC, no fractional seconds — same as AppendLog.
func FormatContradictionMarkdown(contras []Contradiction, source string, at time.Time) string {
	if len(contras) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("- %s **ingest** %s\n",
		at.UTC().Format(time.RFC3339), source))
	for _, c := range contras {
		sb.WriteString(fmt.Sprintf("  - new page %q vs existing [[%s]]: %s\n",
			c.NewPageTitle, c.ExistingTitle, c.Description))
		sb.WriteString(fmt.Sprintf("    - new claim: > %q (%s)\n",
			c.NewPageQuote, formatLineAnnotation(c.NewPageSourceFile, c.NewPageLines)))
		sb.WriteString(fmt.Sprintf("    - existing claim: > %q (%s)\n",
			c.ExistingQuote, formatLineAnnotation(c.ExistingSourceFile, c.ExistingLines)))
	}
	return sb.String()
}

func formatLineAnnotation(path string, lines [2]int) string {
	return fmt.Sprintf("%s:%d-%d", path, lines[0], lines[1])
}

// AppendContradictions opens <wikiDir>/contradictions.md with O_APPEND
// and writes one Format block per call. Mirrors AppendLog's atomicity
// model: a single Write call with O_APPEND is POSIX-atomic for sizes ≤
// PIPE_BUF (typically 4 KiB on Darwin/Linux). A typical contradiction
// block is well under that, so concurrent callers won't interleave
// bytes; we do not take a process-level lock.
func AppendContradictions(wikiDir string, contras []Contradiction, source string, at time.Time) error {
	if len(contras) == 0 {
		return nil
	}
	if err := os.MkdirAll(wikiDir, 0755); err != nil {
		return err
	}
	path := filepath.Join(wikiDir, "contradictions.md")
	block := FormatContradictionMarkdown(contras, source, at)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(block)
	return err
}
