package wiki

import (
	"fmt"
	"strings"
	"time"
)

type SavedAnswerInput struct {
	Question string
	Answer   string
	Model    string
	Pages    []Page
	At       time.Time
}

func FormatSavedAnswer(in SavedAnswerInput) string {
	var sb strings.Builder
	sb.WriteString("---\n")
	sb.WriteString(fmt.Sprintf("question: %s\n", strings.ReplaceAll(in.Question, "\n", " ")))
	sb.WriteString(fmt.Sprintf("created_at: %s\n", in.At.UTC().Format(time.RFC3339)))
	sb.WriteString(fmt.Sprintf("model: %s\n", in.Model))
	sb.WriteString("---\n\n")
	sb.WriteString("# Answer\n\n")
	sb.WriteString(in.Answer)
	sb.WriteString("\n\n## Sources\n\n")
	for i, p := range in.Pages {
		sb.WriteString(fmt.Sprintf("**[%d] %s**\n\n", i+1, p.Title))
		for _, e := range p.Evidence {
			sb.WriteString(fmt.Sprintf("> %q  (%s)\n\n", e.Quote, evidenceAnnotation(e)))
		}
	}
	return sb.String()
}
