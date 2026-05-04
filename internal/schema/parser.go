package schema

import (
	"bufio"
	"bytes"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// canonicalOntologyFields is the fixed bundled order. Rename is a
// name-string mapping over this list; reorder reorders Schema.Ontology.Fields
// without changing the canonical mapping. Truly new structured fields with
// their own validation are a v0.8+ question (Q9).
var canonicalOntologyFields = []string{
	"title", "body", "evidence", "links", "sources",
	"tags", "created", "updated_at", "content_hash", "source_ids",
}

// requiredSections are the eight H2 sections every schema doc must
// declare. Glossary is optional.
var requiredSections = []string{
	"Domain",
	"Page ontology",
	"Ingest prompt",
	"Update-existing prompt",
	"Ask prompt",
	"Contradiction prompt",
	"Promote rewrite prompt",
	"Lint contradictions prompt",
}

// ontologyBulletRE matches an ontology bullet of the form
//
//	- name (type) description
//
// The leading bullet may be indented; the type parenthesis-group is
// required; the description is the trailing free text.
var ontologyBulletRE = regexp.MustCompile(`^\s*-\s+(\S+)\s*\(([^)]+)\)\s*(.*)$`)

// glossaryBulletRE matches a glossary bullet of the form
//
//	- term: definition
var glossaryBulletRE = regexp.MustCompile(`^\s*-\s+([^:]+):\s*(.+)$`)

// Parse splits the doc into frontmatter + sections, validates required
// sections are present, populates Schema fields. Returns ValidationError
// (typed) on structural failures so callers can render file:line.
func Parse(raw []byte) (Schema, error) {
	s := Schema{raw: raw}

	// 1. Read frontmatter: leading "---\n…\n---\n".
	rest, fmFields, fmErr := parseFrontmatter(raw)
	if fmErr != nil {
		return Schema{}, fmErr
	}

	if v, ok := fmFields["schema_version"]; ok {
		n, err := strconv.Atoi(strings.TrimSpace(v.value))
		if err != nil {
			return Schema{}, ValidationError{
				Section: "frontmatter",
				Line:    v.line,
				Problem: fmt.Sprintf("schema_version is not an integer: %q", v.value),
			}
		}
		if n != SchemaFormatVersion {
			return Schema{}, ValidationError{
				Section: "frontmatter",
				Line:    v.line,
				Problem: fmt.Sprintf("unknown schema_version: %d (this binary supports version %d)", n, SchemaFormatVersion),
			}
		}
		s.Version = n
	} else {
		return Schema{}, ValidationError{
			Section: "frontmatter",
			Problem: "schema_version missing from frontmatter",
		}
	}
	if g, ok := fmFields["generator"]; ok {
		s.Generator = strings.TrimSpace(g.value)
	}

	// 2. Split body on H2 headers. Headers are lines beginning with
	//    "## " followed by the section name.
	sections, dupErr := splitH2Sections(rest)
	if dupErr != nil {
		return Schema{}, dupErr
	}

	// 3. Enforce required sections present.
	var errs []ValidationError
	for _, name := range requiredSections {
		if _, ok := sections[name]; !ok {
			errs = append(errs, ValidationError{
				Section: name,
				Problem: "required section missing",
			})
		}
	}
	if len(errs) > 0 {
		return Schema{}, MultiError{Errors: errs}
	}

	// 4. Populate per-section fields.
	s.Domain = strings.TrimSpace(sections["Domain"].body)
	s.Prompts.Ingest = strings.TrimSpace(sections["Ingest prompt"].body)
	s.Prompts.UpdateExisting = strings.TrimSpace(sections["Update-existing prompt"].body)
	s.Prompts.Ask = strings.TrimSpace(sections["Ask prompt"].body)
	s.Prompts.Contradiction = strings.TrimSpace(sections["Contradiction prompt"].body)
	s.Prompts.PromoteRewrite = strings.TrimSpace(sections["Promote rewrite prompt"].body)
	s.Prompts.LintContradictions = strings.TrimSpace(sections["Lint contradictions prompt"].body)

	if onto, ok := sections["Page ontology"]; ok {
		fields, err := parseOntology(onto.body)
		if err != nil {
			return Schema{}, err
		}
		s.Ontology.Fields = fields
	}

	if gloss, ok := sections["Glossary"]; ok {
		s.Glossary = parseGlossary(gloss.body)
	}

	return s, nil
}

// fmField captures a key/value pair plus the 1-indexed line it lived
// on so we can surface accurate file:line on malformed values.
type fmField struct {
	value string
	line  int
}

// parseFrontmatter peels off a leading "---\n…\n---\n" frontmatter
// block. Returns the post-frontmatter remainder, the parsed key/value
// table, and a typed error if the frontmatter is malformed or absent.
func parseFrontmatter(raw []byte) ([]byte, map[string]fmField, error) {
	// We require the first line to be exactly "---".
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	lineNum := 0

	if !scanner.Scan() {
		return nil, nil, ValidationError{Problem: "schema doc must begin with frontmatter (---)"}
	}
	lineNum++
	first := scanner.Text()
	if strings.TrimSpace(first) != "---" {
		return nil, nil, ValidationError{
			Line:    lineNum,
			Problem: "schema doc must begin with frontmatter (---)",
		}
	}

	fields := make(map[string]fmField)
	closed := false
	bodyStart := -1
	// scanner can't tell us byte offsets; track them ourselves.
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		if strings.TrimSpace(line) == "---" {
			closed = true
			break
		}
		// key: value
		if strings.TrimSpace(line) == "" {
			continue
		}
		idx := strings.Index(line, ":")
		if idx < 0 {
			return nil, nil, ValidationError{
				Section: "frontmatter",
				Line:    lineNum,
				Problem: fmt.Sprintf("malformed frontmatter line: %q (expected 'key: value')", line),
			}
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])
		fields[key] = fmField{value: val, line: lineNum}
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, fmt.Errorf("schema: scanner: %w", err)
	}
	if !closed {
		return nil, nil, ValidationError{
			Section: "frontmatter",
			Problem: "unterminated frontmatter (missing closing ---)",
		}
	}

	// Compute the byte offset of the line after the closing "---".
	// We need to count newlines in raw up to lineNum.
	bodyStart = byteOffsetAfterLine(raw, lineNum)
	if bodyStart < 0 || bodyStart > len(raw) {
		bodyStart = len(raw)
	}
	return raw[bodyStart:], fields, nil
}

// byteOffsetAfterLine returns the byte offset in raw immediately after
// the n-th line break (1-indexed). If n exceeds the number of lines,
// returns len(raw).
func byteOffsetAfterLine(raw []byte, n int) int {
	count := 0
	for i, b := range raw {
		if b == '\n' {
			count++
			if count == n {
				return i + 1
			}
		}
	}
	return len(raw)
}

// section is one parsed H2 block: the body text under the heading and
// the 1-indexed line of the heading itself.
type section struct {
	body string
	line int
}

// splitH2Sections walks the post-frontmatter body line-by-line,
// gathering the text under each "## Name" heading. Returns a map
// keyed by section name. Errors on duplicate sections.
func splitH2Sections(body []byte) (map[string]section, error) {
	out := make(map[string]section)
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	var currentName string
	var currentBody strings.Builder
	currentLine := 0
	lineNum := 0

	flush := func() error {
		if currentName == "" {
			return nil
		}
		if _, exists := out[currentName]; exists {
			return ValidationError{
				Section: currentName,
				Line:    currentLine,
				Problem: "duplicate section",
			}
		}
		out[currentName] = section{body: currentBody.String(), line: currentLine}
		return nil
	}

	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		if strings.HasPrefix(line, "## ") {
			if err := flush(); err != nil {
				return nil, err
			}
			currentName = strings.TrimSpace(strings.TrimPrefix(line, "## "))
			currentBody.Reset()
			currentLine = lineNum
			continue
		}
		// H1 lines and pre-first-H2 prose are dropped on the floor.
		if currentName != "" {
			currentBody.WriteString(line)
			currentBody.WriteByte('\n')
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("schema: scanner: %w", err)
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return out, nil
}

// parseOntology walks the Page ontology body and pulls out each
// bullet of the form "  - name (type) description". Position in the
// list is the canonical mapping (position 0 = title, 1 = body, etc.).
func parseOntology(body string) ([]OntologyField, error) {
	var fields []OntologyField
	scanner := bufio.NewScanner(strings.NewReader(body))
	for scanner.Scan() {
		line := scanner.Text()
		m := ontologyBulletRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		declared := strings.TrimSpace(m[1])
		typ := strings.TrimSpace(m[2])
		desc := strings.TrimSpace(m[3])
		canonical := declared
		// Position-stable canonical mapping: the i-th bullet maps to
		// canonicalOntologyFields[i] when in range. Out-of-range
		// bullets keep their declared name as canonical (extra fields
		// are pass-through, see Q9).
		if i := len(fields); i < len(canonicalOntologyFields) {
			canonical = canonicalOntologyFields[i]
		}
		fields = append(fields, OntologyField{
			CanonicalName: canonical,
			DeclaredName:  declared,
			Type:          typ,
			Description:   desc,
		})
	}
	return fields, nil
}

// parseGlossary walks the Glossary body and pulls out each bullet of
// the form "  - term: definition". Empty / unmatched lines are dropped.
func parseGlossary(body string) []GlossaryTerm {
	var out []GlossaryTerm
	scanner := bufio.NewScanner(strings.NewReader(body))
	for scanner.Scan() {
		line := scanner.Text()
		m := glossaryBulletRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		term := strings.TrimSpace(m[1])
		def := strings.TrimSpace(m[2])
		out = append(out, GlossaryTerm{Term: term, Definition: def})
	}
	return out
}
