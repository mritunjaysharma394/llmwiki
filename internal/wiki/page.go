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
	Quote     string
	LineStart int
	LineEnd   int
}

type Page struct {
	Title       string
	Body        string
	Links       []Link
	SourceIDs   []int64
	ContentHash string
	UpdatedAt   time.Time
	Evidence    []Evidence
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
		}
	}
	flushEv()
	return p, nil
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
