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
