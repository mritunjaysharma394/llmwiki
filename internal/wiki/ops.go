package wiki

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mritunjaysharma394/llmwiki/internal/llm"
)

type IngestResult struct {
	Pages []Page
}

var writePagesTool = llm.ToolSchema{
	Name:        "write_pages",
	Description: "Write wiki pages synthesized from the ingested source content.",
	Properties: map[string]any{
		"pages": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"title": map[string]any{"type": "string", "description": "Page title, concise and specific"},
					"body":  map[string]any{"type": "string", "description": "Markdown body content, well-structured"},
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
				},
				"required": []string{"title", "body"},
			},
		},
	},
	Required: []string{"pages"},
}

func IngestToPages(ctx context.Context, client llm.Client, sourceContent string, existingTitles []string) ([]Page, error) {
	system := `You are a knowledge management assistant. You extract key concepts from source content and organize them as wiki pages.
Each page should focus on one concept. Use Markdown formatting for the body.
Link pages to each other using typed links. Only reference existing pages or pages you are creating now.`

	var sb strings.Builder
	sb.WriteString("Existing wiki pages (titles only):\n")
	if len(existingTitles) == 0 {
		sb.WriteString("(none yet)\n")
	} else {
		for _, t := range existingTitles {
			sb.WriteString("- " + t + "\n")
		}
	}
	sb.WriteString("\n---\nSource content to ingest:\n\n")
	sb.WriteString(sourceContent)

	result, err := client.CompleteStructured(ctx, system, sb.String(), writePagesTool)
	if err != nil {
		return nil, fmt.Errorf("llm structured call: %w", err)
	}

	pagesRaw, ok := result["pages"]
	if !ok {
		return nil, fmt.Errorf("no 'pages' in llm response (keys: %v)", keys(result))
	}
	pagesSlice, ok := toSlice(pagesRaw)
	if !ok {
		return nil, fmt.Errorf("'pages' has unexpected type %T", pagesRaw)
	}

	now := time.Now().UTC()
	var pages []Page
	for _, raw := range pagesSlice {
		pm, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		p := Page{
			UpdatedAt: now,
		}
		if t, ok := pm["title"].(string); ok {
			p.Title = t
		}
		if b, ok := pm["body"].(string); ok {
			p.Body = b
		}
		p.ContentHash = HashContent(p.Body)
		if linksRaw, ok := pm["links"].([]any); ok {
			for _, lr := range linksRaw {
				if lm, ok := lr.(map[string]any); ok {
					l := Link{}
					if to, ok := lm["to"].(string); ok {
						l.To = to
					}
					if lt, ok := lm["type"].(string); ok {
						l.Type = lt
					}
					if l.To != "" {
						p.Links = append(p.Links, l)
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

func AnswerQuestion(ctx context.Context, client llm.Client, question string, contextPages []Page) (string, error) {
	system := `You are a knowledgeable assistant answering questions from a personal wiki.
Use the provided wiki pages as your primary source. Cite pages by title using [Page Title] notation.
If the wiki pages don't contain enough information, say so clearly.`

	var sb strings.Builder
	sb.WriteString("Wiki pages:\n\n")
	for _, p := range contextPages {
		sb.WriteString(fmt.Sprintf("## %s\n\n%s\n\n---\n\n", p.Title, p.Body))
	}
	sb.WriteString(fmt.Sprintf("Question: %s", question))

	return client.Complete(ctx, system, sb.String())
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
