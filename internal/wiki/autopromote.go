// Package wiki — autopromote.go
//
// EvaluateAutoPromote is the four-signal heuristic gate sub-project
// 8 Phase A defines per plan §Six design calls #2. Phase B wires it
// into cmd/ask after the answer is saved; Phase A defines the types
// and the gate logic.
//
// Four signals — ALL must pass for AutoPromote:
//
//  1. Citations + evidence quotes. The answer body cites ≥ 2 distinct
//     existing wiki pages via [Page Title] (single-bracket) notation,
//     AND the saved-answer file has ≥ 3 evidence quotes. The
//     validator runs separately downstream — this gate counts only.
//
//  2. Length. The answer body is 100–3000 words inclusive. Below 100
//     a real page can't carry useful claims; above 3000 the answer
//     is more digest than page and should be split before promotion.
//
//  3. No hedging phrases. The default list (overridable via cfg) is
//     plan §2: "i can't tell from the wiki", "the sources don't cover",
//     "i'm not sure", "insufficient information",
//     "the wiki doesn't say", "unclear from".
//     Case-insensitive substring match.
//
//  4. No near-duplicate page exists. BM25 over page titles + first
//     500 chars of body via SQLite FTS5; if the top match's |bm25|
//     score >= cfg.SkipScore (default 5.0), skip auto-promote.
//
// Trust property: this is a TASTE gate. The validator is the trust
// gate; auto-promote runs the heuristic gate THEN re-validates via
// PromoteAnswer's existing path. Heuristic-fail or validator-fail
// both leave the answer in .llmwiki/answers/ for manual review per
// plan §2 ("Two locks; either failure → answer stays").
//
// SQLite BM25 contract surprise: SQLite's bm25() aux function
// returns NEGATIVE numbers (lower is a better match), and the
// magnitude scale is far smaller than Lucene-style BM25 — typical
// strong matches on a small wiki land around |bm25| ≈ 1e-5 to 1e-6,
// not the 5.0–20.0 range the plan §2 default presumes. We keep
// cfg.SkipScore=5.0 as the documented default so the field name
// matches the plan, but in practice on a SQLite-only stack a user
// who wants the near-duplicate skip to fire MUST tune SkipScore down
// (Phase B's [ask] auto_promote_skip_score config knob is the seam).
// Tests below pin a SQLite-realistic threshold so the signal can
// actually be exercised.
package wiki

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/mritunjaysharma394/llmwiki/internal/db"
)

// PromoteVerdict is the result of EvaluateAutoPromote. AutoPromote
// is true only when ALL four signals pass; on any failure the caller
// (cmd/ask in Phase B) keeps the answer in .llmwiki/answers/ and
// surfaces `reason` to the user.
//
// Skipped is true when signal 4 — the near-duplicate check —
// fired. The caller may differentiate "skipped because a page
// already answers this" from "rejected because of content quality"
// in the user-facing line.
type PromoteVerdict struct {
	AutoPromote      bool
	Skipped          bool // true ⇒ near-duplicate check fired
	Citations        int  // distinct existing-page citations found
	EvidenceQuotes   int  // count of parsed-answer evidence quotes
	WordCount        int
	HedgingPhraseHit string  // empty unless hedging caught
	NearDupTitle     string  // top BM25 hit title (only set when Skipped or for diagnostics)
	NearDupScore     float64 // |bm25| of the top hit (always set when DB returned a row)
}

// AutoPromoteConfig is the Phase A typed knobs the four-signal gate
// reads. Phase B will wire these into cmd.Config / [ask] toml; Phase
// A leaves them as a local struct so cmd.Config doesn't churn yet.
//
// Defaults match plan §2:
//   - HedgingPhrases: the six phrases from plan §Six design calls #2
//   - SkipScore:      5.0 (BM25 |raw|; cmp against the top-hit's
//     absolute value — see autopromote.go header for SQLite
//     sign convention)
//   - ScoreFloor:     reserved; unused in Phase A's mechanical gate.
//     Phase B/C may use it once the four signals expand. Hold the
//     field so Phase B can add `[ask] auto_promote_score_floor`
//     without re-shuffling cfg.
type AutoPromoteConfig struct {
	HedgingPhrases []string
	SkipScore      float64
	ScoreFloor     int
}

// DefaultAutoPromoteConfig returns the plan-§2 defaults. cmd-level
// callers pass this directly when no [ask] overrides are set.
func DefaultAutoPromoteConfig() AutoPromoteConfig {
	return AutoPromoteConfig{
		HedgingPhrases: []string{
			"i can't tell from the wiki",
			"the sources don't cover",
			"i'm not sure",
			"insufficient information",
			"the wiki doesn't say",
			"unclear from",
		},
		SkipScore:  5.0,
		ScoreFloor: 0,
	}
}

// MinCitations / MinEvidenceQuotes / MinWords / MaxWords pin the
// length and citation thresholds from plan §2. Held as exported
// consts so tests pin against the same numbers; Phase B may promote
// these into cfg knobs if user feedback demands tuning.
const (
	MinCitations      = 2
	MinEvidenceQuotes = 3
	MinWords          = 100
	MaxWords          = 3000
)

// citationRefRE matches `[Title]` single-bracket citation notation.
// Distinct from the wikilink form `[[Title]]` (FindBareReferences
// already covers wikilinks). Title must be ≥ 2 chars and may not
// contain "[" or "]" — that disambiguates from "[[..." prefixes
// (the post-test below tolerates "[[X]]" by skipping any match whose
// trailing char is a "]" past the close, and any whose preceding char
// is "[").
var citationRefRE = regexp.MustCompile(`\[([^\[\]]{2,})\]`)

// EvaluateAutoPromote runs the four-signal heuristic gate. Returns
// the verdict + a one-line reason suitable for the user-facing
// `→ saved to .llmwiki/answers/<file>` print path. On any signal
// failure AutoPromote=false and reason names the failed signal.
//
// `answer` is the parsed answer file (cmd/ask saves via
// FormatSavedAnswer; cmd/ask in Phase B re-parses via
// ParseSavedAnswer before calling this).
//
// `database` is the live wiki.db; near-duplicate scoring is one
// pages_fts query, no full-table scans.
func EvaluateAutoPromote(answer ParsedSavedAnswer, database *db.DB, cfg AutoPromoteConfig) (PromoteVerdict, string) {
	var v PromoteVerdict

	// Apply defaults for any zero-valued cfg fields. Cleanest seam
	// for Phase B's `[ask]` toml: a partial override (e.g. only
	// SkipScore) leaves the rest at plan defaults.
	if len(cfg.HedgingPhrases) == 0 {
		cfg.HedgingPhrases = DefaultAutoPromoteConfig().HedgingPhrases
	}
	if cfg.SkipScore == 0 {
		cfg.SkipScore = DefaultAutoPromoteConfig().SkipScore
	}

	// Signal 1: citations + evidence quotes.
	v.EvidenceQuotes = countAnswerEvidenceQuotes(answer)
	citations, err := countDistinctExistingCitations(answer.Answer, database)
	if err != nil {
		return v, fmt.Sprintf("citation lookup failed: %v", err)
	}
	v.Citations = citations
	if v.Citations < MinCitations {
		return v, fmt.Sprintf("too few citations: %d (need ≥ %d)", v.Citations, MinCitations)
	}
	if v.EvidenceQuotes < MinEvidenceQuotes {
		return v, fmt.Sprintf("too few evidence quotes: %d (need ≥ %d)", v.EvidenceQuotes, MinEvidenceQuotes)
	}

	// Signal 2: length.
	v.WordCount = countWords(answer.Answer)
	if v.WordCount < MinWords {
		return v, fmt.Sprintf("answer too short: %d words (need ≥ %d)", v.WordCount, MinWords)
	}
	if v.WordCount > MaxWords {
		return v, fmt.Sprintf("answer too long: %d words (cap %d — split before promote)", v.WordCount, MaxWords)
	}

	// Signal 3: hedging phrases. Case-insensitive substring scan.
	if hit := findHedgingPhrase(answer.Answer, cfg.HedgingPhrases); hit != "" {
		v.HedgingPhraseHit = hit
		return v, fmt.Sprintf("hedging phrase detected: %q", hit)
	}

	// Signal 4: near-duplicate page check via FTS5 BM25.
	score, title, err := nearestPageBM25(answer.Question, database)
	if err != nil {
		// FTS errors are non-fatal — degrade to "no near-duplicate
		// detected" rather than fail the gate. A malformed FTS
		// index degrading gracefully matches the retro-linker's
		// stance (warn-and-skip).
		// Surface the error in the verdict's reason but still let
		// the answer through if all OTHER signals passed: this
		// matches plan §2's "two locks" — the validator is the
		// trust gate, the heuristic gate is taste, and we don't
		// want a sick FTS index to block every auto-promote.
		v.AutoPromote = true
		return v, fmt.Sprintf("auto-promote OK (near-duplicate scan error: %v)", err)
	}
	v.NearDupScore = score
	v.NearDupTitle = title
	if score >= cfg.SkipScore {
		v.Skipped = true
		return v, fmt.Sprintf("near-duplicate page exists: %q (BM25 %.2f ≥ %.2f)", title, score, cfg.SkipScore)
	}

	v.AutoPromote = true
	return v, "auto-promote OK"
}

// countAnswerEvidenceQuotes sums the parsed answer's per-page
// Evidence slices into one count.
func countAnswerEvidenceQuotes(a ParsedSavedAnswer) int {
	var n int
	for _, p := range a.Pages {
		n += len(p.Evidence)
	}
	return n
}

// countDistinctExistingCitations parses every `[Title]` (NOT [[Title]])
// from the answer body, intersects with current wiki page titles, and
// returns the size of the distinct set.
//
// `[[X]]` wikilinks are intentionally NOT counted here — plan §2
// distinguishes the two notations because the auto-promote gate
// wants to count "the LLM cited an existing page in prose," which
// canonical chat output uses single brackets for. A wiki-internal
// rewriter (RewriteBareReferencesAsWikilinks) already converts bare
// references to [[X]]; the gate runs BEFORE that pass on the saved
// answer body, so single brackets are the right shape.
func countDistinctExistingCitations(answerBody string, database *db.DB) (int, error) {
	titles, err := database.AllPageTitles()
	if err != nil {
		return 0, err
	}
	if len(titles) == 0 {
		return 0, nil
	}
	exist := make(map[string]bool, len(titles))
	for _, t := range titles {
		exist[t] = true
	}

	matches := citationRefRE.FindAllStringSubmatchIndex(answerBody, -1)
	seen := map[string]bool{}
	for _, m := range matches {
		// m = [outerStart, outerEnd, innerStart, innerEnd]
		outerStart, outerEnd := m[0], m[1]
		innerStart, innerEnd := m[2], m[3]
		// Skip [[ wrapped wikilinks: the regex's `[^\[\]]` already
		// rules out the inner from containing brackets, but the
		// outer "[" might be the second of "[[". And the closing
		// "]" might be the first of "]]". Test both edges.
		if outerStart > 0 && answerBody[outerStart-1] == '[' {
			continue
		}
		if outerEnd < len(answerBody) && answerBody[outerEnd] == ']' {
			continue
		}
		title := answerBody[innerStart:innerEnd]
		if exist[title] {
			seen[title] = true
		}
	}
	return len(seen), nil
}

// countWords splits the answer body on whitespace; an exact O(n)
// counter rather than the Unicode-segmenter version because the
// thresholds (100 / 3000) are tolerant of off-by-a-few.
func countWords(s string) int {
	return len(strings.Fields(s))
}

// findHedgingPhrase returns the first hedging phrase that appears
// (case-insensitive substring) in the body, or "" if none. The
// phrases list is iterated in caller order so the test can pin
// which phrase wins on a multi-hit body.
func findHedgingPhrase(body string, phrases []string) string {
	low := strings.ToLower(body)
	for _, p := range phrases {
		if p == "" {
			continue
		}
		if strings.Contains(low, strings.ToLower(p)) {
			return p
		}
	}
	return ""
}

// nearestPageBM25 runs the question through pages_fts and returns the
// |bm25| of the top hit plus that hit's title. Returns (0, "", nil)
// when no row matches (no FTS hit ⇒ no near-duplicate ⇒ pass signal
// 4). Errors only on malformed query or DB failure; the caller
// degrades gracefully when one is returned.
//
// SQLite's bm25() returns NEGATIVE numbers (lower = better match).
// The plan §2 threshold is documented as a positive 5.0; we
// uniformly compare the absolute value.
func nearestPageBM25(question string, database *db.DB) (float64, string, error) {
	q := buildFTSQueryForBM25(question)
	if q == "" {
		return 0, "", nil
	}
	row := database.SQL().QueryRow(`
		SELECT p.title, bm25(pages_fts)
		FROM pages_fts
		JOIN pages p ON p.id = pages_fts.rowid
		WHERE pages_fts MATCH ?
		ORDER BY bm25(pages_fts)
		LIMIT 1`, q)
	var title string
	var raw float64
	if err := row.Scan(&title, &raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, "", nil
		}
		return 0, "", err
	}
	if raw < 0 {
		raw = -raw
	}
	return raw, title, nil
}

// buildFTSQueryForBM25 sanitises the question into an FTS5 MATCH
// expression. Same shape as db.ftsQuery (lowercase alnum tokens
// joined with OR), redefined here so we don't reach into an
// unexported helper.
func buildFTSQueryForBM25(q string) string {
	var words []string
	for _, w := range strings.Fields(q) {
		clean := strings.Map(func(r rune) rune {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
				return r
			}
			return -1
		}, w)
		if len(clean) > 1 {
			words = append(words, clean)
		}
	}
	if len(words) == 0 {
		return ""
	}
	return strings.Join(words, " OR ")
}
