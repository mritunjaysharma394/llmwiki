package wiki

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mritunjaysharma394/llmwiki/internal/db"
)

// autoPromoteFixture wires a temp wiki.db. Tests seed pages directly
// via UpsertPage; the answer side is constructed with ParsedSavedAnswer
// literals so each test pins the exact body, citation pattern, and
// evidence-quote count under inspection.
type autoPromoteFixture struct {
	DB *db.DB
}

func setupAutoPromoteFixture(t *testing.T) *autoPromoteFixture {
	t.Helper()
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "wiki.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return &autoPromoteFixture{DB: d}
}

func (f *autoPromoteFixture) seed(t *testing.T, title, body string) {
	t.Helper()
	if err := f.DB.UpsertPage(db.PageRecord{
		Title: title, Path: "wiki/" + title + ".md",
		Body: body, ContentHash: HashContent(body),
	}); err != nil {
		t.Fatalf("UpsertPage %s: %v", title, err)
	}
}

// makeAnswer builds a ParsedSavedAnswer with the requested word count
// (approximate — pads with the word "lorem"), N evidence quotes, and
// a body that cites each provided existing-page title via [Title].
func makeAnswer(question string, citations []string, evidenceQuotes, words int) ParsedSavedAnswer {
	var sb strings.Builder
	for _, c := range citations {
		sb.WriteString("See [")
		sb.WriteString(c)
		sb.WriteString("] for details. ")
	}
	// Pad to the requested word count.
	have := len(strings.Fields(sb.String()))
	for i := have; i < words; i++ {
		sb.WriteString("lorem ")
	}
	pages := []Page{}
	if evidenceQuotes > 0 {
		ev := make([]Evidence, evidenceQuotes)
		for i := range ev {
			ev[i] = Evidence{Quote: "quote", SourceFilePath: "src.md", LineStart: 1, LineEnd: 1}
		}
		pages = append(pages, Page{Title: citations[0], Evidence: ev})
	}
	return ParsedSavedAnswer{
		Question:  question,
		Answer:    sb.String(),
		CreatedAt: time.Now().UTC(),
		Pages:     pages,
	}
}

// TestEvaluateAutoPromote_Pass — all four signals clean.
func TestEvaluateAutoPromote_Pass(t *testing.T) {
	f := setupAutoPromoteFixture(t)
	f.seed(t, "Foo", "body")
	f.seed(t, "Bar", "body")
	// Use a question that won't match any page title to dodge
	// signal-4 near-duplicate skip.
	a := makeAnswer("how does the X widget reticulate splines",
		[]string{"Foo", "Bar"}, 3, 200)

	v, reason := EvaluateAutoPromote(a, f.DB, DefaultAutoPromoteConfig())
	if !v.AutoPromote {
		t.Fatalf("AutoPromote=false, reason=%q, verdict=%+v", reason, v)
	}
	if v.Citations != 2 {
		t.Errorf("Citations=%d, want 2", v.Citations)
	}
	if v.EvidenceQuotes != 3 {
		t.Errorf("EvidenceQuotes=%d, want 3", v.EvidenceQuotes)
	}
}

// TestEvaluateAutoPromote_FailsOnTooFewCitations — only one citation.
func TestEvaluateAutoPromote_FailsOnTooFewCitations(t *testing.T) {
	f := setupAutoPromoteFixture(t)
	f.seed(t, "Foo", "body")
	a := makeAnswer("about X widget reticulating splines",
		[]string{"Foo"}, 3, 200)

	v, reason := EvaluateAutoPromote(a, f.DB, DefaultAutoPromoteConfig())
	if v.AutoPromote {
		t.Fatalf("expected fail, got pass; verdict=%+v", v)
	}
	if !strings.Contains(reason, "too few citations") {
		t.Errorf("reason=%q, want 'too few citations'", reason)
	}
}

// TestEvaluateAutoPromote_FailsOnTooFewEvidenceQuotes — citations OK,
// but only 2 evidence quotes (need ≥ 3).
func TestEvaluateAutoPromote_FailsOnTooFewEvidenceQuotes(t *testing.T) {
	f := setupAutoPromoteFixture(t)
	f.seed(t, "Foo", "body")
	f.seed(t, "Bar", "body")
	a := makeAnswer("about X widget", []string{"Foo", "Bar"}, 2, 200)

	v, reason := EvaluateAutoPromote(a, f.DB, DefaultAutoPromoteConfig())
	if v.AutoPromote {
		t.Fatalf("expected fail, got pass; verdict=%+v", v)
	}
	if !strings.Contains(reason, "too few evidence quotes") {
		t.Errorf("reason=%q, want 'too few evidence quotes'", reason)
	}
}

// TestEvaluateAutoPromote_FailsOnTooShort — < 100 words.
func TestEvaluateAutoPromote_FailsOnTooShort(t *testing.T) {
	f := setupAutoPromoteFixture(t)
	f.seed(t, "Foo", "body")
	f.seed(t, "Bar", "body")
	a := makeAnswer("about X widget", []string{"Foo", "Bar"}, 3, 50)

	v, reason := EvaluateAutoPromote(a, f.DB, DefaultAutoPromoteConfig())
	if v.AutoPromote {
		t.Fatalf("expected fail, got pass; verdict=%+v", v)
	}
	if !strings.Contains(reason, "too short") {
		t.Errorf("reason=%q, want 'too short'", reason)
	}
}

// TestEvaluateAutoPromote_FailsOnTooLong — > 3000 words.
func TestEvaluateAutoPromote_FailsOnTooLong(t *testing.T) {
	f := setupAutoPromoteFixture(t)
	f.seed(t, "Foo", "body")
	f.seed(t, "Bar", "body")
	a := makeAnswer("about X widget", []string{"Foo", "Bar"}, 3, 3500)

	v, reason := EvaluateAutoPromote(a, f.DB, DefaultAutoPromoteConfig())
	if v.AutoPromote {
		t.Fatalf("expected fail, got pass; verdict=%+v", v)
	}
	if !strings.Contains(reason, "too long") {
		t.Errorf("reason=%q, want 'too long'", reason)
	}
}

// TestEvaluateAutoPromote_FailsOnHedgingPhrase — body contains a
// default hedging phrase.
func TestEvaluateAutoPromote_FailsOnHedgingPhrase(t *testing.T) {
	f := setupAutoPromoteFixture(t)
	f.seed(t, "Foo", "body")
	f.seed(t, "Bar", "body")
	a := makeAnswer("about X widget", []string{"Foo", "Bar"}, 3, 200)
	// Splice a hedging phrase mid-body (case-insensitive match).
	a.Answer = strings.Replace(a.Answer, "lorem lorem lorem",
		"I'm not sure about this part", 1)

	v, reason := EvaluateAutoPromote(a, f.DB, DefaultAutoPromoteConfig())
	if v.AutoPromote {
		t.Fatalf("expected fail, got pass; verdict=%+v", v)
	}
	if !strings.Contains(reason, "hedging phrase") {
		t.Errorf("reason=%q, want 'hedging phrase'", reason)
	}
	if v.HedgingPhraseHit != "i'm not sure" {
		t.Errorf("HedgingPhraseHit=%q, want 'i'm not sure'", v.HedgingPhraseHit)
	}
}

// TestEvaluateAutoPromote_HedgingPhraseOverride — cfg.HedgingPhrases
// replaces the default list.
func TestEvaluateAutoPromote_HedgingPhraseOverride(t *testing.T) {
	f := setupAutoPromoteFixture(t)
	f.seed(t, "Foo", "body")
	f.seed(t, "Bar", "body")
	a := makeAnswer("about X widget", []string{"Foo", "Bar"}, 3, 200)
	a.Answer += " probably this is fine"

	cfg := AutoPromoteConfig{
		HedgingPhrases: []string{"probably"},
		SkipScore:      5.0,
	}
	v, reason := EvaluateAutoPromote(a, f.DB, cfg)
	if v.AutoPromote {
		t.Fatalf("expected fail with custom phrase, got pass; reason=%q", reason)
	}
	if v.HedgingPhraseHit != "probably" {
		t.Errorf("HedgingPhraseHit=%q, want 'probably'", v.HedgingPhraseHit)
	}
}

// TestEvaluateAutoPromote_NearDuplicateSkip — a page whose title
// matches the question dominates BM25; the gate must trigger Skipped.
//
// Contract surprise: SQLite's bm25() returns very small magnitudes
// (typically 1e-5 .. 1e-6 on small wikis), not the 5.0+ range the
// plan §2 default presumes. The test pins SkipScore to a
// SQLite-realistic threshold so the gate can actually fire; Phase
// B's [ask] auto_promote_skip_score config is the production seam.
func TestEvaluateAutoPromote_NearDuplicateSkip(t *testing.T) {
	f := setupAutoPromoteFixture(t)
	// Seed a page whose title and body match the question almost
	// verbatim → high BM25 score (very negative raw, large |raw|).
	f.seed(t, "Goroutines", strings.Repeat("goroutines scheduler runtime ", 30))
	f.seed(t, "Channels", "channels block when full")

	a := makeAnswer("how do goroutines scheduler runtime work",
		[]string{"Goroutines", "Channels"}, 3, 200)

	cfg := DefaultAutoPromoteConfig()
	cfg.SkipScore = 1e-6 // SQLite-realistic threshold for "strong match"
	v, reason := EvaluateAutoPromote(a, f.DB, cfg)
	if v.AutoPromote {
		t.Fatalf("expected skipped, got pass; verdict=%+v reason=%q", v, reason)
	}
	if !v.Skipped {
		t.Errorf("Skipped=false, want true (near-duplicate)")
	}
	if v.NearDupTitle != "Goroutines" {
		t.Errorf("NearDupTitle=%q, want 'Goroutines'", v.NearDupTitle)
	}
	if v.NearDupScore < 1e-6 {
		t.Errorf("NearDupScore=%v, want ≥ 1e-6", v.NearDupScore)
	}
}

// TestEvaluateAutoPromote_NearDuplicateBelowSkipScore — a weak FTS
// match that scores below the threshold must NOT skip.
func TestEvaluateAutoPromote_NearDuplicateBelowSkipScore(t *testing.T) {
	f := setupAutoPromoteFixture(t)
	// Seed pages with nothing matching the question.
	f.seed(t, "Foo", "totally unrelated content")
	f.seed(t, "Bar", "also unrelated")

	a := makeAnswer("how does X widget reticulate splines",
		[]string{"Foo", "Bar"}, 3, 200)

	v, reason := EvaluateAutoPromote(a, f.DB, DefaultAutoPromoteConfig())
	if !v.AutoPromote {
		t.Fatalf("expected pass, got fail; reason=%q verdict=%+v", reason, v)
	}
	if v.Skipped {
		t.Errorf("Skipped=true on weak match")
	}
}

// TestEvaluateAutoPromote_SkipScoreOverride — cfg.SkipScore overrides
// the default 5.0; setting it very high should let the same fixture
// that fails TestNearDuplicateSkip pass.
func TestEvaluateAutoPromote_SkipScoreOverride(t *testing.T) {
	f := setupAutoPromoteFixture(t)
	f.seed(t, "Goroutines", strings.Repeat("goroutines scheduler runtime ", 30))
	f.seed(t, "Channels", "channels block when full")
	a := makeAnswer("how do goroutines scheduler runtime work",
		[]string{"Goroutines", "Channels"}, 3, 200)

	cfg := DefaultAutoPromoteConfig()
	cfg.SkipScore = 1000.0 // unreachable threshold
	v, reason := EvaluateAutoPromote(a, f.DB, cfg)
	if !v.AutoPromote {
		t.Errorf("expected pass with high SkipScore, got fail: %s", reason)
	}
}

// TestEvaluateAutoPromote_DistinctCitations — duplicate `[Foo]`
// citations count once, not twice.
func TestEvaluateAutoPromote_DistinctCitations(t *testing.T) {
	f := setupAutoPromoteFixture(t)
	f.seed(t, "Foo", "body")
	a := ParsedSavedAnswer{
		Question: "what",
		Answer:   "Cite [Foo] and [Foo] and [Foo]. " + strings.Repeat("lorem ", 200),
		Pages:    []Page{{Title: "Foo", Evidence: []Evidence{{Quote: "q"}, {Quote: "q"}, {Quote: "q"}}}},
	}
	v, reason := EvaluateAutoPromote(a, f.DB, DefaultAutoPromoteConfig())
	if v.AutoPromote {
		t.Errorf("3 dup citations should count as 1; got AutoPromote=true reason=%q", reason)
	}
	if v.Citations != 1 {
		t.Errorf("Citations=%d, want 1 (distinct dedup)", v.Citations)
	}
}

// TestEvaluateAutoPromote_WikilinkBracketsSkipped — `[[Foo]]` is the
// wikilink form, NOT a citation; the gate counts only single-bracket
// `[Foo]` references.
func TestEvaluateAutoPromote_WikilinkBracketsSkipped(t *testing.T) {
	f := setupAutoPromoteFixture(t)
	f.seed(t, "Foo", "body")
	f.seed(t, "Bar", "body")
	body := "Read [[Foo]] and [[Bar]]. " + strings.Repeat("lorem ", 200)
	a := ParsedSavedAnswer{
		Question: "what",
		Answer:   body,
		Pages: []Page{{Title: "Foo", Evidence: []Evidence{
			{Quote: "q"}, {Quote: "q"}, {Quote: "q"},
		}}},
	}
	v, _ := EvaluateAutoPromote(a, f.DB, DefaultAutoPromoteConfig())
	if v.Citations != 0 {
		t.Errorf("Citations=%d, want 0 ([[X]] should be skipped)", v.Citations)
	}
	if v.AutoPromote {
		t.Error("AutoPromote=true, want false (no [Foo] single-bracket citations)")
	}
}

// TestEvaluateAutoPromote_NonExistingCitationsExcluded — `[Bogus]`
// where no Bogus page exists must not count.
func TestEvaluateAutoPromote_NonExistingCitationsExcluded(t *testing.T) {
	f := setupAutoPromoteFixture(t)
	f.seed(t, "Foo", "body")
	a := ParsedSavedAnswer{
		Question: "what",
		Answer:   "Cite [Foo] and [Bogus] and [AlsoBogus]. " + strings.Repeat("lorem ", 200),
		Pages: []Page{{Title: "Foo", Evidence: []Evidence{
			{Quote: "q"}, {Quote: "q"}, {Quote: "q"},
		}}},
	}
	v, _ := EvaluateAutoPromote(a, f.DB, DefaultAutoPromoteConfig())
	if v.Citations != 1 {
		t.Errorf("Citations=%d, want 1 (only Foo exists)", v.Citations)
	}
}

// TestEvaluateAutoPromote_EmptyDB — no pages at all → no citations
// possible → fails the citation gate, not the FTS scan.
func TestEvaluateAutoPromote_EmptyDB(t *testing.T) {
	f := setupAutoPromoteFixture(t)
	a := makeAnswer("anything", []string{"Phantom", "Other"}, 3, 200)
	v, reason := EvaluateAutoPromote(a, f.DB, DefaultAutoPromoteConfig())
	if v.AutoPromote {
		t.Error("expected fail on empty wiki")
	}
	if !strings.Contains(reason, "too few citations") {
		t.Errorf("reason=%q, want citation failure", reason)
	}
}

// TestDefaultAutoPromoteConfig — the bundled defaults match plan §2
// verbatim. Acts as a regression rail for the canonical six phrases.
func TestDefaultAutoPromoteConfig(t *testing.T) {
	got := DefaultAutoPromoteConfig()
	want := []string{
		"i can't tell from the wiki",
		"the sources don't cover",
		"i'm not sure",
		"insufficient information",
		"the wiki doesn't say",
		"unclear from",
	}
	if len(got.HedgingPhrases) != len(want) {
		t.Fatalf("HedgingPhrases len=%d, want %d", len(got.HedgingPhrases), len(want))
	}
	for i, p := range want {
		if got.HedgingPhrases[i] != p {
			t.Errorf("HedgingPhrases[%d]=%q, want %q", i, got.HedgingPhrases[i], p)
		}
	}
	if got.SkipScore != 5.0 {
		t.Errorf("SkipScore=%v, want 5.0", got.SkipScore)
	}
}
