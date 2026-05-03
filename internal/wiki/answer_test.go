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
}
