package wiki

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Link struct {
	To   string
	Type string
}

type Evidence struct {
	Quote          string
	LineStart      int
	LineEnd        int
	SourceFilePath string
}

type Page struct {
	Title       string
	Body        string
	Links       []Link
	SourceIDs   []int64
	ContentHash string
	UpdatedAt   time.Time
	Evidence    []Evidence
	// sub-project 5: Obsidian / Dataview frontmatter.
	//
	// Tags is emitted on every llmwiki-written page as a flat bracketed
	// string array (Dataview-friendly). Callers populate it (typically with
	// the fixed value {"llmwiki", "ingest"}); ParsePage accepts any string
	// array. When empty/nil, WritePage skips the key entirely so pre-v1.1
	// pages round-trip without spontaneous additions.
	//
	// Sources is the distinct list of source_file relative paths backing
	// this page's evidence. When non-empty, WritePage emits it verbatim;
	// when empty/nil, WritePage derives the distinct set from p.Evidence
	// (so callers can leave Sources unset and still get a Dataview-friendly
	// `sources:` array on disk). On parse, Sources holds whatever the file
	// emitted (may be nil for pre-v1.1 pages).
	//
	// Created is the date-only first-ingest stamp. When zero, WritePage
	// skips emitting `created:` so pre-v1.1 round-trips remain stable.
	Tags    []string
	Sources []string
	Created time.Time
}

func HashContent(content string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(content)))
}

func PagePath(wikiDir, title string) string {
	safe := strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == ':' || r == '*' || r == '?' || r == '"' || r == '<' || r == '>' || r == '|' {
			return '_'
		}
		return r
	}, title)
	return filepath.Join(wikiDir, safe+".md")
}

func WritePage(p Page, wikiDir string) error {
	path := PagePath(wikiDir, p.Title)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	var sb strings.Builder
	sb.WriteString("---\n")
	sb.WriteString(fmt.Sprintf("title: %s\n", p.Title))
	sb.WriteString(fmt.Sprintf("updated_at: %s\n", p.UpdatedAt.UTC().Format(time.RFC3339)))
	sb.WriteString(fmt.Sprintf("content_hash: %s\n", p.ContentHash))
	if len(p.SourceIDs) > 0 {
		ids := make([]string, len(p.SourceIDs))
		for i, id := range p.SourceIDs {
			ids[i] = strconv.FormatInt(id, 10)
		}
		sb.WriteString(fmt.Sprintf("source_ids: [%s]\n", strings.Join(ids, ", ")))
	} else {
		sb.WriteString("source_ids: []\n")
	}
	if len(p.Tags) > 0 {
		escaped := make([]string, len(p.Tags))
		for i, t := range p.Tags {
			escaped[i] = yamlEscapeScalar(t)
		}
		sb.WriteString(fmt.Sprintf("tags: [%s]\n", strings.Join(escaped, ", ")))
	}
	// sources: honor caller-supplied Sources; otherwise derive the distinct
	// set of source_file relative paths from Evidence. Skip the key when
	// neither produces anything (preserves pre-v1.1 round-trip stability).
	srcs := p.Sources
	if len(srcs) == 0 {
		srcs = distinctEvidenceSources(p.Evidence)
	}
	if len(srcs) > 0 {
		escaped := make([]string, len(srcs))
		for i, s := range srcs {
			escaped[i] = yamlEscapeScalar(s)
		}
		sb.WriteString(fmt.Sprintf("sources: [%s]\n", strings.Join(escaped, ", ")))
	}
	if !p.Created.IsZero() {
		sb.WriteString(fmt.Sprintf("created: %s\n", p.Created.UTC().Format("2006-01-02")))
	}
	// Date-only `updated:` twin alongside the RFC3339 `updated_at:` above —
	// Dataview-friendly. Always emitted (UpdatedAt is always populated from
	// real data, so this is "added populated from real data" per the
	// pre-v1.1 round-trip contract rather than a spontaneous empty key).
	if !p.UpdatedAt.IsZero() {
		sb.WriteString(fmt.Sprintf("updated: %s\n", p.UpdatedAt.UTC().Format("2006-01-02")))
	}
	if len(p.Links) > 0 {
		sb.WriteString("links:\n")
		for _, l := range p.Links {
			sb.WriteString(fmt.Sprintf("  - to: %s\n    type: %s\n", l.To, l.Type))
		}
	}
	if len(p.Evidence) > 0 {
		sb.WriteString("evidence:\n")
		for _, e := range p.Evidence {
			esc := strings.ReplaceAll(e.Quote, `\`, `\\`)
			esc = strings.ReplaceAll(esc, `"`, `\"`)
			esc = strings.ReplaceAll(esc, "\n", `\n`)
			esc = strings.ReplaceAll(esc, "\r", `\r`)
			sb.WriteString(fmt.Sprintf("  - quote: \"%s\"\n", esc))
			sb.WriteString(fmt.Sprintf("    line_start: %d\n", e.LineStart))
			sb.WriteString(fmt.Sprintf("    line_end: %d\n", e.LineEnd))
			if e.SourceFilePath != "" {
				sb.WriteString(fmt.Sprintf("    source_file: %s\n", yamlEscapeScalar(e.SourceFilePath)))
			}
		}
	}
	sb.WriteString("---\n\n")
	sb.WriteString(p.Body)
	return os.WriteFile(path, []byte(sb.String()), 0644)
}

func ReadPage(path string) (Page, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Page{}, err
	}
	return ParsePage(string(data))
}

func ParsePage(content string) (Page, error) {
	var p Page
	if !strings.HasPrefix(content, "---\n") {
		p.Body = content
		p.ContentHash = HashContent(content)
		return p, nil
	}
	rest := content[4:]
	end := strings.Index(rest, "\n---\n")
	if end == -1 {
		p.Body = content
		p.ContentHash = HashContent(content)
		return p, nil
	}
	frontmatter := rest[:end]
	p.Body = strings.TrimPrefix(rest[end+5:], "\n")

	var inLinks, inEvidence bool
	var curEv Evidence
	flushEv := func() {
		if curEv.Quote != "" {
			p.Evidence = append(p.Evidence, curEv)
			curEv = Evidence{}
		}
	}
	for _, line := range strings.Split(frontmatter, "\n") {
		switch {
		case strings.HasPrefix(line, "title: "):
			p.Title = strings.TrimSpace(line[7:])
			inLinks, inEvidence = false, false
		case strings.HasPrefix(line, "updated_at: "):
			p.UpdatedAt, _ = time.Parse(time.RFC3339, strings.TrimSpace(line[12:]))
		case strings.HasPrefix(line, "content_hash: "):
			p.ContentHash = strings.TrimSpace(line[14:])
		case strings.HasPrefix(line, "source_ids: "):
			p.SourceIDs = parseIntArray(strings.TrimSpace(line[12:]))
		case strings.HasPrefix(line, "tags: "):
			p.Tags = parseStringArray(strings.TrimSpace(line[6:]))
		case strings.HasPrefix(line, "sources: "):
			p.Sources = parseStringArray(strings.TrimSpace(line[9:]))
		case strings.HasPrefix(line, "created: "):
			p.Created, _ = time.Parse("2006-01-02", strings.TrimSpace(line[9:]))
		case strings.HasPrefix(line, "links:"):
			inLinks, inEvidence = true, false
		case strings.HasPrefix(line, "evidence:"):
			flushEv()
			inLinks, inEvidence = false, true
		case inLinks && strings.HasPrefix(line, "  - to: "):
			p.Links = append(p.Links, Link{To: strings.TrimSpace(line[8:])})
		case inLinks && strings.HasPrefix(line, "    type: ") && len(p.Links) > 0:
			p.Links[len(p.Links)-1].Type = strings.TrimSpace(line[10:])
		case inEvidence && strings.HasPrefix(line, "  - quote: "):
			flushEv()
			curEv.Quote = unescapeQuote(strings.TrimSpace(strings.TrimPrefix(line, "  - quote: ")))
		case inEvidence && strings.HasPrefix(line, "    line_start: "):
			curEv.LineStart, _ = strconv.Atoi(strings.TrimSpace(line[16:]))
		case inEvidence && strings.HasPrefix(line, "    line_end: "):
			curEv.LineEnd, _ = strconv.Atoi(strings.TrimSpace(line[14:]))
		case inEvidence && strings.HasPrefix(line, "    source_file: "):
			raw := strings.TrimSpace(line[len("    source_file: "):])
			if strings.HasPrefix(raw, `"`) && strings.HasSuffix(raw, `"`) && len(raw) >= 2 {
				curEv.SourceFilePath = unescapeQuote(raw)
			} else {
				curEv.SourceFilePath = raw
			}
		}
	}
	flushEv()
	return p, nil
}

// yamlEscapeScalar quotes the string only when it contains characters that
// would confuse the line-oriented YAML parser used by ParsePage.
func yamlEscapeScalar(s string) string {
	if s == "" || strings.ContainsAny(s, ":#[]{},&*!|>'\"%@`\n") {
		esc := strings.ReplaceAll(s, `\`, `\\`)
		esc = strings.ReplaceAll(esc, `"`, `\"`)
		esc = strings.ReplaceAll(esc, "\n", `\n`)
		return `"` + esc + `"`
	}
	return s
}

func unescapeQuote(s string) string {
	s = strings.TrimPrefix(s, `"`)
	s = strings.TrimSuffix(s, `"`)
	s = strings.ReplaceAll(s, `\n`, "\n")
	s = strings.ReplaceAll(s, `\r`, "\r")
	s = strings.ReplaceAll(s, `\"`, `"`)
	s = strings.ReplaceAll(s, `\\`, `\`)
	return s
}

// distinctEvidenceSources returns the distinct, first-occurrence-ordered
// list of non-empty source_file relative paths across the given evidence.
func distinctEvidenceSources(ev []Evidence) []string {
	if len(ev) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(ev))
	var out []string
	for _, e := range ev {
		if e.SourceFilePath == "" {
			continue
		}
		if _, ok := seen[e.SourceFilePath]; ok {
			continue
		}
		seen[e.SourceFilePath] = struct{}{}
		out = append(out, e.SourceFilePath)
	}
	return out
}

// parseStringArray parses a flat bracketed YAML string array (the same
// shape WritePage emits for tags / sources): `[a, b, "c, d"]`. It strips
// the surrounding brackets, splits on commas that aren't inside a quoted
// run, trims whitespace and a single matched-pair of surrounding quotes
// from each element, and drops empty entries. Returns nil for an empty
// or all-empty array so round-trips that started with `nil` stay `nil`.
func parseStringArray(s string) []string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "[")
	s = strings.TrimSuffix(s, "]")
	if strings.TrimSpace(s) == "" {
		return nil
	}
	// Comma-split that respects double-quoted runs so `"a, b"` stays one
	// element. yamlEscapeScalar wraps any value containing a comma in
	// double quotes, so this is sufficient for what WritePage emits.
	var parts []string
	var cur strings.Builder
	inQuote := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"' && (i == 0 || s[i-1] != '\\'):
			inQuote = !inQuote
			cur.WriteByte(c)
		case c == ',' && !inQuote:
			parts = append(parts, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	parts = append(parts, cur.String())

	var out []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if len(p) >= 2 && strings.HasPrefix(p, `"`) && strings.HasSuffix(p, `"`) {
			p = unescapeQuote(p)
		}
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func parseIntArray(s string) []int64 {
	s = strings.Trim(s, "[]")
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	var ids []int64
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		var id int64
		fmt.Sscanf(p, "%d", &id)
		ids = append(ids, id)
	}
	return ids
}
