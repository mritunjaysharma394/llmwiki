package wiki

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mritunjaysharma394/llmwiki/internal/schema"
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
	// Sub-project 7 / Phase J Task 16: extra frontmatter declared in
	// the schema's `## Page ontology` (or simply present on disk) but
	// not in the canonical struct set. Round-tripped on Read/Write
	// under the schema-aware path. The validator does NOT check these
	// values — they are pass-through, intended as an extension point
	// for v0.8+'s "truly new structured fields with their own
	// validation."
	ExtraFrontmatter map[string]string
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

// WritePage is the legacy backwards-compat shim. Pre-v0.7 callers see
// no behaviour change: it delegates to WritePageWithSchema with
// schema.Bundled() so on-disk emission is byte-identical to v0.6.
//
// New code that already carries the active schema in scope should call
// WritePageWithSchema directly so user renames + reorders propagate to
// disk. The v0.7 in-package callers (ingest_runner, update_existing,
// promote, migrate) are wired through their `sch schema.Schema`
// entrypoints and call WritePageWithSchema directly.
func WritePage(p Page, wikiDir string) error {
	return WritePageWithSchema(p, wikiDir, schema.Bundled())
}

// WritePageWithSchema is the schema-aware page writer. It walks
// sch.Ontology.Fields IN DECLARED ORDER and emits each canonical-known
// field's lines using the user's declared name. The body lives below
// the closing `---` regardless of where it sits in the ontology slice
// (no frontmatter emission for `body`).
//
// TRUST PROPERTY. The canonical struct field carrying evidence quotes
// (`Page.Evidence`) is fixed. The rename here is a name-string mapping
// over disk emission only — wiki.ValidateAndAttachEvidence reads
// p.Evidence regardless of what the user named it on disk. A schema
// that renames evidence → citations still has every quote substring-
// matched against the bytes of its named source file.
//
// Declared-but-not-canonical fields (e.g. a user-declared `priority`
// row in the ontology) are NOT fabricated on write. They round-trip
// through Page.ExtraFrontmatter, which ParsePageWithSchema populates
// when those keys are present on disk, and which WritePageWithSchema
// emits in alphabetical order after the canonical fields when set.
func WritePageWithSchema(p Page, wikiDir string, sch schema.Schema) error {
	path := PagePath(wikiDir, p.Title)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}

	// Build a name resolver: canonical -> declared. A canonical name
	// not in the schema's ontology falls through to itself, so callers
	// passing a partially-populated schema (e.g. a Schema with only the
	// required three fields) still emit the un-declared canonical
	// fields under their canonical names.
	declaredFor := make(map[string]string, len(sch.Ontology.Fields))
	for _, f := range sch.Ontology.Fields {
		declaredFor[f.CanonicalName] = f.DeclaredName
	}
	name := func(canonical string) string {
		if d, ok := declaredFor[canonical]; ok && d != "" {
			return d
		}
		return canonical
	}

	var sb strings.Builder
	sb.WriteString("---\n")

	// Track which canonical fields we've emitted so a duplicate ontology
	// entry doesn't cause double emission.
	emitted := make(map[string]bool, len(sch.Ontology.Fields))

	emit := func(canonical string) {
		if emitted[canonical] {
			return
		}
		emitted[canonical] = true
		switch canonical {
		case "title":
			sb.WriteString(fmt.Sprintf("%s: %s\n", name("title"), p.Title))
		case "updated_at":
			sb.WriteString(fmt.Sprintf("%s: %s\n", name("updated_at"), p.UpdatedAt.UTC().Format(time.RFC3339)))
		case "content_hash":
			sb.WriteString(fmt.Sprintf("%s: %s\n", name("content_hash"), p.ContentHash))
		case "source_ids":
			if len(p.SourceIDs) > 0 {
				ids := make([]string, len(p.SourceIDs))
				for i, id := range p.SourceIDs {
					ids[i] = strconv.FormatInt(id, 10)
				}
				sb.WriteString(fmt.Sprintf("%s: [%s]\n", name("source_ids"), strings.Join(ids, ", ")))
			} else {
				sb.WriteString(fmt.Sprintf("%s: []\n", name("source_ids")))
			}
		case "tags":
			if len(p.Tags) > 0 {
				escaped := make([]string, len(p.Tags))
				for i, t := range p.Tags {
					escaped[i] = yamlEscapeScalar(t)
				}
				sb.WriteString(fmt.Sprintf("%s: [%s]\n", name("tags"), strings.Join(escaped, ", ")))
			}
		case "sources":
			// honor caller-supplied Sources; otherwise derive the distinct
			// set of source_file relative paths from Evidence. Skip the key
			// when neither produces anything.
			srcs := p.Sources
			if len(srcs) == 0 {
				srcs = distinctEvidenceSources(p.Evidence)
			}
			if len(srcs) > 0 {
				escaped := make([]string, len(srcs))
				for i, s := range srcs {
					escaped[i] = yamlEscapeScalar(s)
				}
				sb.WriteString(fmt.Sprintf("%s: [%s]\n", name("sources"), strings.Join(escaped, ", ")))
			}
		case "created":
			if !p.Created.IsZero() {
				sb.WriteString(fmt.Sprintf("%s: %s\n", name("created"), p.Created.UTC().Format("2006-01-02")))
			}
		case "links":
			if len(p.Links) > 0 {
				sb.WriteString(fmt.Sprintf("%s:\n", name("links")))
				for _, l := range p.Links {
					sb.WriteString(fmt.Sprintf("  - to: %s\n    type: %s\n", l.To, l.Type))
				}
			}
		case "evidence":
			if len(p.Evidence) > 0 {
				sb.WriteString(fmt.Sprintf("%s:\n", name("evidence")))
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
		case "body":
			// Body lives below the closing ---; no frontmatter emission.
		default:
			// Non-canonical declared field (e.g. user-added `priority`).
			// Round-trip via Page.ExtraFrontmatter — handled below as a
			// post-canonical pass.
		}
	}

	// Emit `updated:` (the date-only twin of updated_at) alongside the
	// `created:` slot — that's where v0.6's emitter put it (after
	// `created:` and before `links:`). The twin has no canonical
	// position of its own.
	emitUpdatedTwin := func() {
		if !p.UpdatedAt.IsZero() {
			sb.WriteString(fmt.Sprintf("updated: %s\n", p.UpdatedAt.UTC().Format("2006-01-02")))
		}
	}

	// Canonical fields we know how to emit. The schema declares the
	// per-field order; if the user reordered, the user wins.
	canonicalKnown := map[string]bool{
		"title": true, "updated_at": true, "content_hash": true,
		"source_ids": true, "tags": true, "sources": true,
		"created": true, "links": true, "evidence": true, "body": true,
	}

	if len(sch.Ontology.Fields) == 0 {
		// Defensive: a malformed / zero-value schema with no ontology.
		// Fall back to the canonical bundled emission order so we never
		// emit an empty frontmatter block.
		fallback := []string{"title", "updated_at", "content_hash", "source_ids",
			"tags", "sources", "created", "links", "evidence"}
		for _, c := range fallback {
			emit(c)
			if c == "created" {
				emitUpdatedTwin()
			}
		}
	} else {
		for _, f := range sch.Ontology.Fields {
			if canonicalKnown[f.CanonicalName] {
				emit(f.CanonicalName)
				if f.CanonicalName == "created" {
					emitUpdatedTwin()
				}
			}
		}
	}

	// Page.ExtraFrontmatter round-trip (Task 16). Emit alphabetically
	// after the canonical fields, before the closing `---`. We do NOT
	// fabricate values for keys that aren't already in the map — values
	// only land here when ParsePageWithSchema previously read them off
	// disk. Alphabetical order is for stability (an unstable map-walk
	// order would produce churn in `git diff` for unchanged pages).
	if len(p.ExtraFrontmatter) > 0 {
		keys := make([]string, 0, len(p.ExtraFrontmatter))
		for k := range p.ExtraFrontmatter {
			keys = append(keys, k)
		}
		sortStrings(keys)
		for _, k := range keys {
			sb.WriteString(fmt.Sprintf("%s: %s\n", k, p.ExtraFrontmatter[k]))
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

// ReadPageWithSchema is the schema-aware companion to ReadPage; it
// delegates to ParsePageWithSchema so production callers that have
// the active schema in scope can read pages written under renamed
// ontologies without losing data into ExtraFrontmatter.
func ReadPageWithSchema(path string, sch schema.Schema) (Page, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Page{}, err
	}
	return ParsePageWithSchema(string(data), sch)
}

// ParsePage is the legacy backwards-compat shim. Pre-v0.7 callers see
// no behaviour change: it delegates to ParsePageWithSchema with
// schema.Bundled() so canonical-named pages still parse identically.
func ParsePage(content string) (Page, error) {
	return ParsePageWithSchema(content, schema.Bundled())
}

// ParsePageWithSchema parses page content using the schema's declared
// field names; falls back to canonical names for keys the schema didn't
// rename (so pre-v0.7 pages on disk still parse under a renamed
// schema). Keys not in the canonical struct set land in
// Page.ExtraFrontmatter for round-tripping.
//
// Pathological case: when both the renamed key (declared) and its
// canonical fallback are present in the same file (botched
// migration), the declared name wins — the canonical-name block is
// dropped on the floor for that canonical field. The Phase H
// schema_drift / page_update_log surfaces will surface this condition
// so the user can reconcile.
//
// TRUST PROPERTY UNCHANGED. The validator reads p.Evidence
// unconditionally, regardless of whether the user named it
// "evidence", "citations", or "quotes" on disk. The substring-match
// check is bundled and unreachable from the schema.
func ParsePageWithSchema(content string, sch schema.Schema) (Page, error) {
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

	// Build the bidirectional resolver. declared → canonical AND
	// canonical → canonical. The second binding is the pre-v0.7
	// fallback: a page on disk written under bundled canonical names
	// still parses correctly when the active schema renamed those
	// fields. When both keys are present in the same file, the
	// declared-name resolver wins (its mapping is added second so its
	// presence in the file is preferred — but more importantly, we
	// track which canonical fields we've already seen via the declared
	// key and skip canonical-name matches for those).
	canonicalFor := make(map[string]string, 2*len(sch.Ontology.Fields))
	declaredFor := make(map[string]string, len(sch.Ontology.Fields))
	for _, f := range sch.Ontology.Fields {
		declaredFor[f.CanonicalName] = f.DeclaredName
		canonicalFor[f.DeclaredName] = f.CanonicalName
		canonicalFor[f.CanonicalName] = f.CanonicalName
	}

	// declaredKeysPresent[canonical] = true once we've hit the user's
	// declared key for that canonical field. Used to suppress the
	// canonical-name fallback when both are present (declared wins).
	declaredKeysPresent := map[string]bool{}
	for _, f := range sch.Ontology.Fields {
		if f.DeclaredName != f.CanonicalName {
			// Pre-scan: if the declared key is present in the file,
			// mark the canonical as "declared wins" so a later
			// canonical-named line doesn't overwrite the declared
			// value.
			declaredPrefix := f.DeclaredName + ":"
			for _, line := range strings.Split(frontmatter, "\n") {
				if strings.HasPrefix(line, declaredPrefix) {
					declaredKeysPresent[f.CanonicalName] = true
					break
				}
			}
		}
	}

	// resolveKey returns the canonical name for a frontmatter key, or
	// "" if the key isn't in our canonical-or-declared set. The
	// declared-wins suppression: a canonical-name line is skipped when
	// the declared name for that canonical also appears in the file.
	// When the key isn't in the schema map at all, fall back to the
	// bundled canonical name (so a page on disk with `evidence:` still
	// parses under a schema that didn't declare that canonical at all).
	resolveKey := func(key string) string {
		c, ok := canonicalFor[key]
		if !ok {
			// Fallback to the bundled canonical set so pre-v0.7 pages
			// parse even if the active schema is partial / synthetic.
			if canonicalSchemaFields[key] {
				return key
			}
			return ""
		}
		// If this is a canonical-name match AND the declared name
		// (different from canonical) also appears in the file, skip
		// this line — declared wins.
		decl := declaredFor[c]
		if key == c && decl != "" && decl != c && declaredKeysPresent[c] {
			return ""
		}
		return c
	}

	var inLinks, inEvidence bool
	// curBlockCanonical is the canonical name of the multi-line block
	// (links / evidence / extra-frontmatter) we are currently inside.
	// It controls how subsequent indented lines are interpreted.
	var curEv Evidence
	flushEv := func() {
		if curEv.Quote != "" {
			p.Evidence = append(p.Evidence, curEv)
			curEv = Evidence{}
		}
	}
	for _, line := range strings.Split(frontmatter, "\n") {
		// Identify scalar or block-start lines: `<key>:` or `<key>: <value>`.
		// Indented continuation lines are handled separately below.
		if !strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "    ") {
			// Top-level frontmatter key.
			colonIdx := strings.Index(line, ":")
			if colonIdx < 0 {
				continue
			}
			key := line[:colonIdx]
			value := strings.TrimSpace(line[colonIdx+1:])
			// Special-case the date-only `updated:` twin. It's a
			// write-only emission alongside `updated_at:` — the source
			// of truth is `updated_at`. We silently drop it on parse
			// so it doesn't end up in ExtraFrontmatter and round-trip
			// as a duplicate.
			if key == "updated" {
				inLinks, inEvidence = false, false
				continue
			}
			canonical := resolveKey(key)
			// A non-canonical key (canonical == "" OR canonical maps
			// to itself but isn't in canonicalSchemaFields, e.g. a
			// user-declared `priority`) is an ExtraFrontmatter scalar.
			if canonical == "" || !canonicalSchemaFields[canonical] {
				inLinks, inEvidence = false, false
				if canonical == "" {
					// Suppressed by declared-wins (declared key is
					// also present somewhere in the file). Drop on
					// the floor.
					decl := declaredFor[canonicalFor[key]]
					if key == canonicalFor[key] && decl != "" && decl != key {
						continue
					}
				}
				if value != "" {
					if p.ExtraFrontmatter == nil {
						p.ExtraFrontmatter = map[string]string{}
					}
					p.ExtraFrontmatter[key] = value
				}
				continue
			}
			// We have a canonical match. Reset block flags and dispatch.
			inLinks, inEvidence = false, false
			switch canonical {
			case "title":
				p.Title = value
			case "updated_at":
				p.UpdatedAt, _ = time.Parse(time.RFC3339, value)
			case "content_hash":
				p.ContentHash = value
			case "source_ids":
				p.SourceIDs = parseIntArray(value)
			case "tags":
				p.Tags = parseStringArray(value)
			case "sources":
				p.Sources = parseStringArray(value)
			case "created":
				p.Created, _ = time.Parse("2006-01-02", value)
			case "links":
				// Reset Links so a duplicate canonical block doesn't
				// append to an earlier one (declared-wins suppression
				// already filters the canonical block when both
				// declared+canonical appear).
				p.Links = nil
				inLinks = true
			case "evidence":
				flushEv()
				p.Evidence = nil
				inEvidence = true
			}
			continue
		}
		// Indented continuation line.
		switch {
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

// canonicalSchemaFields is the fixed canonical name set known to the
// page parser/writer. Mirrors canonicalOntologyFields in
// internal/schema/parser.go. Used by ParsePageWithSchema to decide
// whether an unresolved frontmatter key is "extra" (lands in
// ExtraFrontmatter) or "canonical-but-suppressed-by-declared-wins"
// (silently dropped).
var canonicalSchemaFields = map[string]bool{
	"title": true, "body": true, "evidence": true, "links": true,
	"sources": true, "tags": true, "created": true, "updated_at": true,
	"content_hash": true, "source_ids": true,
}

// sortStrings is a small in-place insertion sort for string slices —
// avoids pulling in `sort` for the single use site here (the
// alphabetical ExtraFrontmatter emission in WritePageWithSchema).
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
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
