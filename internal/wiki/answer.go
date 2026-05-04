package wiki

import (
	"fmt"
	"regexp"
	"strconv"
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

// ParsedSavedAnswer is the deserialized form of a .llmwiki/answers/<ts>-<slug>.md
// file as written by cmd/ask.go:saveAnswer via FormatSavedAnswer.
//
// CreatedAt is the RFC3339-parsed `created_at` frontmatter value (the
// formatter's `in.At.UTC().Format(time.RFC3339)`). Pages carry only Title
// and Evidence — the saved-answer file does not embed per-page bodies.
type ParsedSavedAnswer struct {
	Question  string
	Answer    string
	Model     string
	CreatedAt time.Time
	Pages     []Page
}

// evidenceAnnotationPathRE matches the canonical post-sub-project-3 form
// "<path>:<a>-<b>" inside the parenthesized annotation. The path may
// contain any character except a colon (so we can split on the final
// `:<a>-<b>` suffix unambiguously).
var evidenceAnnotationPathRE = regexp.MustCompile(`^(.+):(\d+)-(\d+)$`)

// evidenceAnnotationLegacyRE matches the pre-sub-project-3 legacy form
// "lines <a>-<b>" — kept so old answer files still parse.
var evidenceAnnotationLegacyRE = regexp.MustCompile(`^lines (\d+)-(\d+)$`)

// sourceHeaderRE matches a "**[<n>] <title>**" line that opens a page's
// quote block in the "## Sources" section.
var sourceHeaderRE = regexp.MustCompile(`^\*\*\[\d+\]\s+(.+)\*\*$`)

// ParseSavedAnswer is the deterministic inverse of FormatSavedAnswer. It
// reconstructs the SavedAnswerInput shape (minus the redundant At, which
// surfaces as CreatedAt) from the on-disk content of a saved-answer file.
//
// Tolerances:
//   - Extra/unknown frontmatter keys are ignored (forward-compat).
//   - The legacy "(lines a-b)" annotation form is accepted alongside the
//     canonical "(<path>:a-b)" form. Legacy quotes parse with an empty
//     SourceFilePath.
//   - A non-RFC3339 `created_at` returns an error wrapping time.Parse's
//     failure rather than zero-valuing the timestamp silently.
func ParseSavedAnswer(content string) (ParsedSavedAnswer, error) {
	var out ParsedSavedAnswer
	if !strings.HasPrefix(content, "---\n") {
		return out, fmt.Errorf("ParseSavedAnswer: missing leading frontmatter delimiter")
	}
	rest := content[4:]
	end := strings.Index(rest, "\n---\n")
	if end == -1 {
		return out, fmt.Errorf("ParseSavedAnswer: missing closing frontmatter delimiter")
	}
	frontmatter := rest[:end]
	body := strings.TrimPrefix(rest[end+5:], "\n")

	for _, line := range strings.Split(frontmatter, "\n") {
		switch {
		case strings.HasPrefix(line, "question: "):
			out.Question = strings.TrimSpace(line[len("question: "):])
		case strings.HasPrefix(line, "model: "):
			out.Model = strings.TrimSpace(line[len("model: "):])
		case strings.HasPrefix(line, "created_at: "):
			raw := strings.TrimSpace(line[len("created_at: "):])
			t, err := time.Parse(time.RFC3339, raw)
			if err != nil {
				return out, fmt.Errorf("ParseSavedAnswer: created_at: %w", err)
			}
			out.CreatedAt = t
		}
		// All other keys silently ignored (forward-compat).
	}

	// Body shape: "# Answer\n\n<answer>\n\n## Sources\n\n<sources>".
	const answerAnchor = "# Answer\n\n"
	ai := strings.Index(body, answerAnchor)
	if ai == -1 {
		return out, fmt.Errorf("ParseSavedAnswer: missing '# Answer' section")
	}
	afterAnswer := body[ai+len(answerAnchor):]
	const sourcesAnchor = "\n## Sources\n"
	si := strings.Index(afterAnswer, sourcesAnchor)
	var sourcesBlock string
	if si == -1 {
		out.Answer = strings.TrimRight(afterAnswer, "\n")
	} else {
		out.Answer = strings.TrimRight(afterAnswer[:si], "\n")
		sourcesBlock = afterAnswer[si+len(sourcesAnchor):]
	}

	pages, err := parseSourcesBlock(sourcesBlock)
	if err != nil {
		return out, err
	}
	out.Pages = pages
	return out, nil
}

// parseSourcesBlock walks the body that follows "## Sources\n", grouping
// "> \"<quote>\"  (<annotation>)" lines under their preceding
// "**[<n>] <title>**" header. Blank lines are skipped.
func parseSourcesBlock(block string) ([]Page, error) {
	if strings.TrimSpace(block) == "" {
		return nil, nil
	}
	var pages []Page
	var cur *Page
	for _, line := range strings.Split(block, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if m := sourceHeaderRE.FindStringSubmatch(trimmed); m != nil {
			pages = append(pages, Page{Title: strings.TrimSpace(m[1])})
			cur = &pages[len(pages)-1]
			continue
		}
		if strings.HasPrefix(trimmed, "> ") {
			if cur == nil {
				return nil, fmt.Errorf("ParseSavedAnswer: quote line without preceding source header: %q", trimmed)
			}
			ev, err := parseQuoteLine(trimmed)
			if err != nil {
				return nil, err
			}
			cur.Evidence = append(cur.Evidence, ev)
		}
		// Anything else is not part of the formatter's output; ignore.
	}
	return pages, nil
}

// parseQuoteLine inverts the formatter's `> %q  (%s)` line. The %q-form is
// strconv.Quote, so strconv.Unquote is the exact inverse for the quoted
// segment. The annotation is whatever sits between the final "  (" and
// the trailing ")".
func parseQuoteLine(line string) (Evidence, error) {
	body := strings.TrimPrefix(line, "> ")
	// Annotation lives in the trailing "  (...)". Find the last "  (" and
	// require a ")" at the very end so quotes containing parentheses don't
	// break the split.
	if !strings.HasSuffix(body, ")") {
		return Evidence{}, fmt.Errorf("ParseSavedAnswer: quote line missing trailing annotation: %q", line)
	}
	cut := strings.LastIndex(body, "  (")
	if cut == -1 {
		return Evidence{}, fmt.Errorf("ParseSavedAnswer: quote line missing annotation separator: %q", line)
	}
	quoted := body[:cut]
	annotation := body[cut+len("  (") : len(body)-1]

	quote, err := strconv.Unquote(quoted)
	if err != nil {
		return Evidence{}, fmt.Errorf("ParseSavedAnswer: unquote %q: %w", quoted, err)
	}

	ev := Evidence{Quote: quote}
	if m := evidenceAnnotationLegacyRE.FindStringSubmatch(annotation); m != nil {
		a, _ := strconv.Atoi(m[1])
		b, _ := strconv.Atoi(m[2])
		ev.LineStart, ev.LineEnd = a, b
		return ev, nil
	}
	if m := evidenceAnnotationPathRE.FindStringSubmatch(annotation); m != nil {
		a, _ := strconv.Atoi(m[2])
		b, _ := strconv.Atoi(m[3])
		ev.SourceFilePath = m[1]
		ev.LineStart, ev.LineEnd = a, b
		return ev, nil
	}
	return Evidence{}, fmt.Errorf("ParseSavedAnswer: unrecognized annotation %q", annotation)
}
