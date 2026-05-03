package wiki

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/mritunjaysharma394/llmwiki/internal/llm"
)

type IngestResult struct {
	Pages []Page
}

var writePagesTool = llm.ToolSchema{
	Name:        "write_pages",
	Description: "Write wiki pages synthesized from the ingested source content. Every page MUST include verbatim evidence quotes from the source.",
	Properties: map[string]any{
		"pages": map[string]any{
			"type":        "array",
			"description": "Wiki pages. Aim for 1-4 pages per call. Better to return one solid page than five thin ones.",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"title": map[string]any{"type": "string", "description": "Concise page title"},
					"body":  map[string]any{"type": "string", "description": "Markdown body, well-structured"},
					"links": map[string]any{
						"type": "array",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"to":   map[string]any{"type": "string"},
								"type": map[string]any{"type": "string", "enum": []string{"supports", "contradicts", "supersedes", "related"}},
							},
							"required": []string{"to", "type"},
						},
					},
					"evidence": map[string]any{
						"type":        "array",
						"description": "Verbatim quotes copied character-for-character from SOURCE. At least one required per page.",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"quote":       map[string]any{"type": "string", "description": "Verbatim substring of SOURCE"},
								"explanation": map[string]any{"type": "string", "description": "Optional: why this quote supports the page"},
							},
							"required": []string{"quote"},
						},
					},
				},
				"required": []string{"title", "body", "evidence"},
			},
		},
	},
	Required: []string{"pages"},
}

const ingestSystemPrompt = `You write wiki pages strictly grounded in the SOURCE provided.

RULES:
1. Every page MUST include "evidence" — verbatim spans copied character-for-character from SOURCE that justify the page's claims.
2. Do NOT include general knowledge that is not in SOURCE.
3. If SOURCE doesn't contain enough material for a high-quality page on a topic, do NOT create that page.
4. Better to return one solid page than five thin ones. Aim for 1-4 pages per call.
5. Page bodies should synthesize and organize, but every claim must be defensible from the evidence quotes you provide.
6. When linking pages, only reference existing pages or pages you are creating in this same call.`

func IngestToPages(ctx context.Context, client llm.Client, sourceContent string, existingTitles []string) ([]Page, error) {
	var sb strings.Builder
	sb.WriteString("Existing wiki pages (titles only):\n")
	if len(existingTitles) == 0 {
		sb.WriteString("(none yet)\n")
	} else {
		for _, t := range existingTitles {
			sb.WriteString("- " + t + "\n")
		}
	}
	sb.WriteString("\n---\nSOURCE to ingest:\n\n")
	sb.WriteString(sourceContent)

	result, err := client.CompleteStructured(ctx, ingestSystemPrompt, sb.String(), writePagesTool)
	if err != nil {
		return nil, fmt.Errorf("llm structured call: %w", err)
	}

	pages, err := ExtractPagesFromToolResult(result)
	if err != nil {
		return nil, err
	}
	pages, _ = ValidateAndAttachEvidence(pages, sourceContent)
	now := time.Now().UTC()
	for i := range pages {
		pages[i].UpdatedAt = now
		pages[i].ContentHash = HashContent(pages[i].Body)
	}
	return pages, nil
}

// ExtractPagesFromToolResult parses the LLM tool-call result into Page structs.
// Does not validate evidence — call ValidateAndAttachEvidence next.
func ExtractPagesFromToolResult(result map[string]any) ([]Page, error) {
	pagesRaw, ok := result["pages"]
	if !ok {
		return nil, fmt.Errorf("no 'pages' in llm response (keys: %v)", keys(result))
	}
	pagesSlice, ok := toSlice(pagesRaw)
	if !ok {
		return nil, fmt.Errorf("'pages' has unexpected type %T", pagesRaw)
	}
	var pages []Page
	for _, raw := range pagesSlice {
		pm, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		var p Page
		if t, ok := pm["title"].(string); ok {
			p.Title = t
		}
		if b, ok := pm["body"].(string); ok {
			p.Body = b
		}
		if linksRaw, ok := pm["links"].([]any); ok {
			for _, lr := range linksRaw {
				if lm, ok := lr.(map[string]any); ok {
					to, _ := lm["to"].(string)
					typ, _ := lm["type"].(string)
					if to != "" {
						p.Links = append(p.Links, Link{To: to, Type: typ})
					}
				}
			}
		}
		if evRaw, ok := pm["evidence"].([]any); ok {
			for _, er := range evRaw {
				if em, ok := er.(map[string]any); ok {
					q, _ := em["quote"].(string)
					if q != "" {
						p.Evidence = append(p.Evidence, Evidence{Quote: q})
					}
				}
			}
		}
		if p.Title != "" && p.Body != "" {
			pages = append(pages, p)
		}
	}
	return pages, nil
}

// ValidateAndAttachEvidence drops evidence quotes that are not verbatim
// substrings of source, drops pages that have zero valid evidence after that,
// and computes line_start/line_end for surviving quotes (1-indexed).
//
// Returns (kept pages, count of pages dropped).
func ValidateAndAttachEvidence(pages []Page, source string) ([]Page, int) {
	var kept []Page
	dropped := 0
	for _, p := range pages {
		var valid []Evidence
		for _, e := range p.Evidence {
			if e.Quote == "" {
				continue
			}
			idx := strings.Index(source, e.Quote)
			if idx < 0 {
				fmt.Fprintf(os.Stderr, "  WARN dropped quote in page %q — not present in source\n", p.Title)
				continue
			}
			start, end := lineRange(source, idx, len(e.Quote))
			e.LineStart = start
			e.LineEnd = end
			valid = append(valid, e)
		}
		if len(valid) == 0 {
			fmt.Fprintf(os.Stderr, "  WARN dropped page %q — no verifiable evidence\n", p.Title)
			dropped++
			continue
		}
		p.Evidence = valid
		kept = append(kept, p)
	}
	return kept, dropped
}

// lineRange returns 1-indexed (start, end) line numbers for a substring at
// byte offset idx of length n in source. Both start and end are inclusive.
func lineRange(source string, idx, n int) (int, int) {
	start := 1 + strings.Count(source[:idx], "\n")
	end := start + strings.Count(source[idx:idx+n], "\n")
	return start, end
}

const answerSystemPrompt = `You answer using the provided wiki pages and source quotes.
Cite pages inline using [Page Title] notation.
When using a verbatim quote from a source, render it as a markdown blockquote and label the line range, e.g.:
> "channels block when full" (lines 4-4)

If pages and quotes are insufficient, say so plainly. Do not fabricate.`

func AnswerQuestion(ctx context.Context, client llm.Client, question string, contextPages []Page) (string, error) {
	return client.Complete(ctx, answerSystemPrompt, buildAnswerUserPrompt(question, contextPages))
}

func StreamAnswer(ctx context.Context, client llm.Client, question string, contextPages []Page, w io.Writer) (string, error) {
	return client.CompleteStream(ctx, answerSystemPrompt, buildAnswerUserPrompt(question, contextPages), w)
}

func buildAnswerUserPrompt(question string, pages []Page) string {
	var sb strings.Builder
	sb.WriteString("## Wiki pages\n\n")
	for _, p := range pages {
		sb.WriteString(fmt.Sprintf("### %s\n\n%s\n", p.Title, p.Body))
		if len(p.Evidence) > 0 {
			sb.WriteString("\n**Source quotes for this page:**\n")
			for _, e := range p.Evidence {
				sb.WriteString(fmt.Sprintf("> %q  (lines %d-%d)\n", e.Quote, e.LineStart, e.LineEnd))
			}
		} else {
			sb.WriteString("\n*(no source quotes attached — legacy page)*\n")
		}
		sb.WriteString("\n---\n\n")
	}
	sb.WriteString(fmt.Sprintf("Question: %s", question))
	return sb.String()
}

func keys(m map[string]any) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}

// toSlice handles []any directly, or map[string]any with numeric/sequential keys.
func toSlice(v any) ([]any, bool) {
	if s, ok := v.([]any); ok {
		return s, true
	}
	if m, ok := v.(map[string]any); ok {
		result := make([]any, len(m))
		for i := range result {
			if val, ok := m[fmt.Sprintf("%d", i)]; ok {
				result[i] = val
			}
		}
		return result, len(result) > 0
	}
	return nil, false
}

func DetectContradictions(ctx context.Context, client llm.Client, pages []Page) (string, error) {
	if len(pages) < 2 {
		return "", nil
	}
	system := `You are a wiki consistency checker. Identify factual contradictions between wiki pages.
List each contradiction as: "Page A vs Page B: <description>". If no contradictions, say "No contradictions found."`

	var sb strings.Builder
	for _, p := range pages {
		sb.WriteString(fmt.Sprintf("## %s\n\n%s\n\n---\n\n", p.Title, p.Body))
	}

	return client.Complete(ctx, system, sb.String())
}
