package wiki

import (
	"strings"
	"testing"
	"time"
)

func TestFormatSavedAnswer(t *testing.T) {
	out := FormatSavedAnswer(SavedAnswerInput{
		Question: "what is X?",
		Answer:   "X is Y.",
		Model:    "claude-haiku-4-5",
		Pages: []Page{{
			Title:    "X",
			Evidence: []Evidence{{Quote: "X is short for Y", LineStart: 2, LineEnd: 2}},
		}},
		At: time.Date(2026, 5, 3, 10, 0, 0, 0, time.UTC),
	})
	if !strings.Contains(out, "question: what is X?") {
		t.Errorf("missing question frontmatter:\n%s", out)
	}
	if !strings.Contains(out, "X is Y.") {
		t.Errorf("missing answer body")
	}
	if !strings.Contains(out, `> "X is short for Y"`) {
		t.Errorf("missing source quote")
	}
	if !strings.Contains(out, "(lines 2-2)") {
		t.Errorf("legacy line annotation expected when SourceFilePath empty:\n%s", out)
	}
}

// TestFormatSavedAnswerRendersSourceFile covers the post-sub-project-3
// rendering: when evidence carries a SourceFilePath, the saved answer file
// should annotate the quote with "(path:a-b)" instead of the legacy
// "(lines a-b)" form.
func TestFormatSavedAnswerRendersSourceFile(t *testing.T) {
	out := FormatSavedAnswer(SavedAnswerInput{
		Question: "where is X defined?",
		Answer:   "in internal/db/db.go",
		Model:    "m",
		Pages: []Page{{
			Title: "X",
			Evidence: []Evidence{
				{Quote: "func Open", LineStart: 10, LineEnd: 10, SourceFilePath: "internal/db/db.go"},
				{Quote: "the answer is 42", LineStart: 2, LineEnd: 2, SourceFilePath: "page-3"},
			},
		}},
		At: time.Date(2026, 5, 3, 10, 0, 0, 0, time.UTC),
	})
	if !strings.Contains(out, "(internal/db/db.go:10-10)") {
		t.Errorf("missing file annotation in saved answer:\n%s", out)
	}
	if !strings.Contains(out, "(page-3:2-2)") {
		t.Errorf("missing pdf page annotation:\n%s", out)
	}
	if strings.Contains(out, "(lines 10-10)") {
		t.Errorf("legacy line annotation should not appear when SourceFilePath set:\n%s", out)
	}
}

// TestParseSavedAnswer_RoundTrip builds a SavedAnswerInput with two pages
// (each with one Evidence carrying Quote + line range + SourceFilePath),
// runs FormatSavedAnswer, then ParseSavedAnswer, and asserts every field
// round-trips. This is the deterministic-inverse contract the parser exists
// to satisfy.
func TestParseSavedAnswer_RoundTrip(t *testing.T) {
	at := time.Date(2026, 5, 4, 15, 2, 8, 0, time.UTC)
	in := SavedAnswerInput{
		Question: "how does the validator work?",
		Answer:   "The validator drops unverified quotes before write.",
		Model:    "gemini-2.0-flash",
		Pages: []Page{
			{
				Title: "Validator Internals",
				Evidence: []Evidence{
					{
						Quote:          "every quote must substring-match",
						LineStart:      215,
						LineEnd:        215,
						SourceFilePath: "internal/wiki/ops.go",
					},
				},
			},
			{
				Title: "Trust Property",
				Evidence: []Evidence{
					{
						Quote:          "no page reaches disk without >=1 evidence",
						LineStart:      61,
						LineEnd:        62,
						SourceFilePath: "docs/superpowers/plans/example.md",
					},
				},
			},
		},
		At: at,
	}
	formatted := FormatSavedAnswer(in)
	got, err := ParseSavedAnswer(formatted)
	if err != nil {
		t.Fatalf("ParseSavedAnswer: %v", err)
	}
	if got.Question != in.Question {
		t.Errorf("question: got %q want %q", got.Question, in.Question)
	}
	if strings.TrimSpace(got.Answer) != in.Answer {
		t.Errorf("answer: got %q want %q", got.Answer, in.Answer)
	}
	if got.Model != in.Model {
		t.Errorf("model: got %q want %q", got.Model, in.Model)
	}
	if !got.CreatedAt.Equal(at) {
		t.Errorf("created_at: got %v want %v", got.CreatedAt, at)
	}
	if len(got.Pages) != len(in.Pages) {
		t.Fatalf("pages: got %d want %d", len(got.Pages), len(in.Pages))
	}
	for i, want := range in.Pages {
		gp := got.Pages[i]
		if gp.Title != want.Title {
			t.Errorf("pages[%d].Title: got %q want %q", i, gp.Title, want.Title)
		}
		if len(gp.Evidence) != len(want.Evidence) {
			t.Fatalf("pages[%d].Evidence: got %d want %d", i, len(gp.Evidence), len(want.Evidence))
		}
		for j, we := range want.Evidence {
			ge := gp.Evidence[j]
			if ge.Quote != we.Quote {
				t.Errorf("pages[%d].Evidence[%d].Quote: got %q want %q", i, j, ge.Quote, we.Quote)
			}
			if ge.LineStart != we.LineStart || ge.LineEnd != we.LineEnd {
				t.Errorf("pages[%d].Evidence[%d] line range: got %d-%d want %d-%d", i, j, ge.LineStart, ge.LineEnd, we.LineStart, we.LineEnd)
			}
			if ge.SourceFilePath != we.SourceFilePath {
				t.Errorf("pages[%d].Evidence[%d].SourceFilePath: got %q want %q", i, j, ge.SourceFilePath, we.SourceFilePath)
			}
		}
	}
}

// TestParseSavedAnswer_FromHandAuthoredFixture feeds the parser a literal
// fixture mirroring exactly what cmd/ask.go:saveAnswer writes today, then
// asserts the parsed shape. Guards against the parser drifting from the
// formatter via subtle whitespace conventions ("> "  (...)\n\n").
func TestParseSavedAnswer_FromHandAuthoredFixture(t *testing.T) {
	fixture := "---\n" +
		"question: how does the validator work?\n" +
		"created_at: 2026-05-04T15:02:08Z\n" +
		"model: gemini-2.0-flash\n" +
		"---\n" +
		"\n" +
		"# Answer\n" +
		"\n" +
		"The validator drops unverified quotes before write.\n" +
		"\n" +
		"## Sources\n" +
		"\n" +
		"**[1] Validator Internals**\n" +
		"\n" +
		"> \"every quote must substring-match\"  (internal/wiki/ops.go:215-215)\n" +
		"\n"
	got, err := ParseSavedAnswer(fixture)
	if err != nil {
		t.Fatalf("ParseSavedAnswer: %v", err)
	}
	if got.Question != "how does the validator work?" {
		t.Errorf("question: got %q", got.Question)
	}
	if got.Model != "gemini-2.0-flash" {
		t.Errorf("model: got %q", got.Model)
	}
	want := time.Date(2026, 5, 4, 15, 2, 8, 0, time.UTC)
	if !got.CreatedAt.Equal(want) {
		t.Errorf("created_at: got %v want %v", got.CreatedAt, want)
	}
	if len(got.Pages) != 1 {
		t.Fatalf("pages: got %d want 1", len(got.Pages))
	}
	p := got.Pages[0]
	if p.Title != "Validator Internals" {
		t.Errorf("page title: got %q", p.Title)
	}
	if len(p.Evidence) != 1 {
		t.Fatalf("evidence: got %d want 1", len(p.Evidence))
	}
	e := p.Evidence[0]
	if e.Quote != "every quote must substring-match" {
		t.Errorf("quote: got %q", e.Quote)
	}
	if e.SourceFilePath != "internal/wiki/ops.go" {
		t.Errorf("source_file: got %q", e.SourceFilePath)
	}
	if e.LineStart != 215 || e.LineEnd != 215 {
		t.Errorf("line range: got %d-%d want 215-215", e.LineStart, e.LineEnd)
	}
}

// TestParseSavedAnswer_NonRFC3339TimestampReturnsError asserts the parser
// rejects a non-RFC3339 timestamp by returning an error wrapping
// time.Parse's failure. Garbage in must not silently zero CreatedAt.
func TestParseSavedAnswer_NonRFC3339TimestampReturnsError(t *testing.T) {
	fixture := "---\n" +
		"question: x\n" +
		"created_at: not-a-date\n" +
		"model: m\n" +
		"---\n" +
		"\n" +
		"# Answer\n" +
		"\n" +
		"body\n" +
		"\n" +
		"## Sources\n" +
		"\n"
	_, err := ParseSavedAnswer(fixture)
	if err == nil {
		t.Fatal("expected error for non-RFC3339 timestamp, got nil")
	}
	if !strings.Contains(err.Error(), "created_at") {
		t.Errorf("error should mention created_at: %v", err)
	}
}

// TestParseSavedAnswer_LegacyLineAnnotation covers the pre-sub-project-3
// "(lines a-b)" annotation form: SourceFilePath empty, LineStart/LineEnd
// populated. Backward-compat guard so old answer files still parse.
func TestParseSavedAnswer_LegacyLineAnnotation(t *testing.T) {
	fixture := "---\n" +
		"question: legacy?\n" +
		"created_at: 2026-01-01T00:00:00Z\n" +
		"model: m\n" +
		"---\n" +
		"\n" +
		"# Answer\n" +
		"\n" +
		"body\n" +
		"\n" +
		"## Sources\n" +
		"\n" +
		"**[1] Old Page**\n" +
		"\n" +
		"> \"legacy quote\"  (lines 10-12)\n" +
		"\n"
	got, err := ParseSavedAnswer(fixture)
	if err != nil {
		t.Fatalf("ParseSavedAnswer: %v", err)
	}
	if len(got.Pages) != 1 || len(got.Pages[0].Evidence) != 1 {
		t.Fatalf("expected 1 page with 1 evidence, got pages=%d", len(got.Pages))
	}
	e := got.Pages[0].Evidence[0]
	if e.SourceFilePath != "" {
		t.Errorf("legacy form should leave SourceFilePath empty: got %q", e.SourceFilePath)
	}
	if e.LineStart != 10 || e.LineEnd != 12 {
		t.Errorf("line range: got %d-%d want 10-12", e.LineStart, e.LineEnd)
	}
	if e.Quote != "legacy quote" {
		t.Errorf("quote: got %q", e.Quote)
	}
}

// TestParseSavedAnswer_IgnoresExtraFrontmatterKeys asserts the parser
// tolerates unknown frontmatter keys (forward-compat) and still populates
// the three known fields correctly.
func TestParseSavedAnswer_IgnoresExtraFrontmatterKeys(t *testing.T) {
	fixture := "---\n" +
		"question: q\n" +
		"experimental_key: foo\n" +
		"created_at: 2026-05-04T00:00:00Z\n" +
		"model: m\n" +
		"another_key: bar\n" +
		"---\n" +
		"\n" +
		"# Answer\n" +
		"\n" +
		"body text\n" +
		"\n" +
		"## Sources\n" +
		"\n"
	got, err := ParseSavedAnswer(fixture)
	if err != nil {
		t.Fatalf("ParseSavedAnswer: %v", err)
	}
	if got.Question != "q" {
		t.Errorf("question: got %q", got.Question)
	}
	if got.Model != "m" {
		t.Errorf("model: got %q", got.Model)
	}
	want := time.Date(2026, 5, 4, 0, 0, 0, 0, time.UTC)
	if !got.CreatedAt.Equal(want) {
		t.Errorf("created_at: got %v want %v", got.CreatedAt, want)
	}
}
