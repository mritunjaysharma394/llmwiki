package wiki

import (
	"strings"
	"testing"

	"github.com/mritunjaysharma394/llmwiki/internal/ingest"
)

func sourceFiles(source string) []ingest.SourceFile {
	return []ingest.SourceFile{ingest.NewSourceFile("doc", []byte(source))}
}

func TestValidateAndAttachEvidence(t *testing.T) {
	source := `Line one of source.
Line two contains the quick brown fox.
Line three.
Line four mentions kafka consumer group offset.
Line five.`

	pages := []Page{
		{
			Title: "Good page",
			Body:  "About the fox",
			Evidence: []Evidence{
				{Quote: "quick brown fox"},
				{Quote: "this string is NOT in source"},
			},
		},
		{
			Title: "Another good page",
			Body:  "Kafka",
			Evidence: []Evidence{{Quote: "kafka consumer group offset"}},
		},
		{
			Title: "Hallucinated page",
			Body:  "Made up",
			Evidence: []Evidence{{Quote: "absolutely not present anywhere"}},
		},
		{
			Title: "Empty evidence page",
			Body:  "Nope",
		},
	}

	got, dropped := ValidateAndAttachEvidence(pages, sourceFiles(source))

	if len(got) != 2 {
		t.Fatalf("got %d valid pages, want 2 (titles: %v)", len(got), pageTitles(got))
	}
	if got[0].Title != "Good page" {
		t.Errorf("got[0].Title = %q", got[0].Title)
	}
	if len(got[0].Evidence) != 1 {
		t.Errorf("good page kept %d evidence, want 1", len(got[0].Evidence))
	}
	if got[0].Evidence[0].LineStart != 2 || got[0].Evidence[0].LineEnd != 2 {
		t.Errorf("good page line range = %d-%d, want 2-2", got[0].Evidence[0].LineStart, got[0].Evidence[0].LineEnd)
	}
	if got[1].Evidence[0].LineStart != 4 {
		t.Errorf("kafka quote line_start = %d, want 4", got[1].Evidence[0].LineStart)
	}
	if dropped != 2 {
		t.Errorf("dropped = %d, want 2", dropped)
	}
}

func TestValidateAndAttachEvidenceMultilineQuote(t *testing.T) {
	source := "alpha\nbeta\ngamma\ndelta\n"
	pages := []Page{{
		Title:    "T",
		Body:     "b",
		Evidence: []Evidence{{Quote: "beta\ngamma"}},
	}}
	got, _ := ValidateAndAttachEvidence(pages, sourceFiles(source))
	if len(got) != 1 {
		t.Fatal("page dropped")
	}
	if got[0].Evidence[0].LineStart != 2 || got[0].Evidence[0].LineEnd != 3 {
		t.Errorf("multiline lines = %d-%d, want 2-3", got[0].Evidence[0].LineStart, got[0].Evidence[0].LineEnd)
	}
}

func TestValidateAndAttachEvidenceUnicode(t *testing.T) {
	source := "héllo wörld\nsecond line\n"
	pages := []Page{{
		Title:    "T",
		Body:     "b",
		Evidence: []Evidence{{Quote: "héllo wörld"}},
	}}
	got, _ := ValidateAndAttachEvidence(pages, sourceFiles(source))
	if len(got) != 1 {
		t.Fatalf("unicode quote dropped (page count=%d)", len(got))
	}
}

func pageTitles(ps []Page) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Title
	}
	return out
}

func TestExtractPagesFromToolResult(t *testing.T) {
	raw := map[string]any{
		"pages": []any{
			map[string]any{
				"title": "P1",
				"body":  "body 1",
				"links": []any{
					map[string]any{"to": "P2", "type": "supports"},
				},
				"evidence": []any{
					map[string]any{"quote": "first quote"},
					map[string]any{"quote": "second", "explanation": "ignored"},
				},
			},
			map[string]any{
				"title":    "P2",
				"body":     "body 2",
				"evidence": []any{map[string]any{"quote": "another"}},
			},
		},
	}
	pages, err := ExtractPagesFromToolResult(raw)
	if err != nil {
		t.Fatalf("ExtractPagesFromToolResult: %v", err)
	}
	if len(pages) != 2 {
		t.Fatalf("got %d pages, want 2", len(pages))
	}
	if len(pages[0].Evidence) != 2 {
		t.Errorf("page 0 evidence count = %d, want 2", len(pages[0].Evidence))
	}
	if len(pages[0].Links) != 1 || pages[0].Links[0].To != "P2" {
		t.Errorf("page 0 links = %+v", pages[0].Links)
	}
}

func TestValidateAndAttachEvidencePerFile(t *testing.T) {
	files := []ingest.SourceFile{
		ingest.NewSourceFile("a.md", []byte("alpha line\nbeta line\n")),
		ingest.NewSourceFile("b.md", []byte("gamma\ndelta\n")),
	}
	pages := []Page{
		{
			Title: "Found-correctly",
			Body:  "x",
			Evidence: []Evidence{
				{Quote: "alpha line", SourceFilePath: "a.md"},
				{Quote: "delta", SourceFilePath: "b.md"},
			},
		},
		{
			Title: "Wrong-file",
			Body:  "x",
			// quote exists, but the named file doesn't contain it
			Evidence: []Evidence{{Quote: "alpha line", SourceFilePath: "b.md"}},
		},
		{
			Title: "Unknown-file",
			Body:  "x",
			Evidence: []Evidence{{Quote: "alpha line", SourceFilePath: "z.md"}},
		},
	}
	kept, dropped := ValidateAndAttachEvidence(pages, files)
	if len(kept) != 1 || kept[0].Title != "Found-correctly" {
		t.Fatalf("kept = %v", pageTitles(kept))
	}
	if dropped != 2 {
		t.Errorf("dropped = %d, want 2", dropped)
	}
	e0 := kept[0].Evidence[0]
	if e0.SourceFilePath != "a.md" || e0.LineStart != 1 || e0.LineEnd != 1 {
		t.Errorf("ev[0] = %+v", e0)
	}
	e1 := kept[0].Evidence[1]
	if e1.SourceFilePath != "b.md" || e1.LineStart != 2 || e1.LineEnd != 2 {
		t.Errorf("ev[1] = %+v", e1)
	}
}

func TestValidateAndAttachEvidenceFallbackWhenSourceFileMissing(t *testing.T) {
	files := []ingest.SourceFile{
		ingest.NewSourceFile("only.md", []byte("the answer is 42\n")),
	}
	pages := []Page{{
		Title:    "T",
		Body:     "b",
		Evidence: []Evidence{{Quote: "the answer is 42"}}, // no SourceFilePath
	}}
	kept, _ := ValidateAndAttachEvidence(pages, files)
	if len(kept) != 1 {
		t.Fatal("page dropped")
	}
	got := kept[0].Evidence[0]
	if got.SourceFilePath != "only.md" {
		t.Errorf("fallback didn't attribute: %+v", got)
	}
	if got.LineStart != 1 || got.LineEnd != 1 {
		t.Errorf("fallback line range = %d-%d, want 1-1", got.LineStart, got.LineEnd)
	}
}

func TestValidateAndAttachEvidenceQuoteNamedButNotInNamedFile(t *testing.T) {
	files := []ingest.SourceFile{
		ingest.NewSourceFile("a.md", []byte("only this line\n")),
		ingest.NewSourceFile("b.md", []byte("a different thing\n")),
	}
	// LLM names b.md but the quote is actually in a.md — must drop, not silently fix.
	pages := []Page{{
		Title:    "Misattributed",
		Body:     "x",
		Evidence: []Evidence{{Quote: "only this line", SourceFilePath: "b.md"}},
	}}
	kept, dropped := ValidateAndAttachEvidence(pages, files)
	if len(kept) != 0 {
		t.Errorf("expected page dropped, got kept=%v", pageTitles(kept))
	}
	if dropped != 1 {
		t.Errorf("dropped = %d, want 1", dropped)
	}
}

func TestExtractPagesFromToolResultReadsSourceFile(t *testing.T) {
	raw := map[string]any{
		"pages": []any{
			map[string]any{
				"title": "P",
				"body":  "b",
				"evidence": []any{
					map[string]any{"quote": "q1", "source_file": "a.md"},
					map[string]any{"quote": "q2"}, // missing source_file
				},
			},
		},
	}
	pages, err := ExtractPagesFromToolResult(raw)
	if err != nil {
		t.Fatalf("ExtractPagesFromToolResult: %v", err)
	}
	if len(pages) != 1 || len(pages[0].Evidence) != 2 {
		t.Fatalf("unexpected pages: %+v", pages)
	}
	if pages[0].Evidence[0].SourceFilePath != "a.md" {
		t.Errorf("ev[0].SourceFilePath = %q, want %q", pages[0].Evidence[0].SourceFilePath, "a.md")
	}
	if pages[0].Evidence[1].SourceFilePath != "" {
		t.Errorf("ev[1].SourceFilePath = %q, want empty", pages[0].Evidence[1].SourceFilePath)
	}
}

func TestExtractPagesMissingPagesKey(t *testing.T) {
	_, err := ExtractPagesFromToolResult(map[string]any{"foo": "bar"})
	if err == nil {
		t.Fatal("expected error for missing 'pages' key")
	}
}

func TestBuildAnswerPromptIncludesEvidence(t *testing.T) {
	pages := []Page{{
		Title: "Channels",
		Body:  "channels coordinate goroutines",
		Evidence: []Evidence{
			{Quote: "channels block when full", LineStart: 4, LineEnd: 4},
		},
	}}
	prompt := buildAnswerUserPrompt("how do channels work?", pages)
	if !strings.Contains(prompt, "Channels") {
		t.Error("prompt missing page title")
	}
	if !strings.Contains(prompt, "channels block when full") {
		t.Error("prompt missing evidence quote")
	}
	if !strings.Contains(prompt, "(lines 4-4)") {
		t.Error("prompt missing line range")
	}
	if !strings.Contains(prompt, "Question: how do channels work?") {
		t.Error("prompt missing question")
	}
}
