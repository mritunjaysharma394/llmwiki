// Package schema implements the user-editable wiki schema doc — the
// third Karpathy layer (after raw sources and the wiki itself). The
// schema is a Markdown document the user reads and edits at AGENTS.md
// in the wiki root; it controls what the LLM is *asked* and how the
// page is *shaped*. It does NOT control what counts as valid evidence —
// wiki.ValidateAndAttachEvidence is bundled and runs after every LLM
// call regardless of what the schema-rendered prompt told the LLM.
//
// The trust property holds at the schema boundary: a malicious or
// compromised schema can degrade page quality (more drops, fewer
// pages land), but it cannot ground a false claim, because the
// substring-match validator gates every page that reaches disk.
package schema

import (
	"crypto/sha256"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
)

// SchemaFormatVersion is the schema doc format version this binary
// understands. A doc declaring a higher version is rejected with a
// "this binary supports version 1" error.
const SchemaFormatVersion = 1

// Schema is the parsed shape of an AGENTS.md schema doc. It carries
// the prompts the LLM is asked, the ontology the page is shaped to,
// and the optional glossary of domain terms. Hash() over the on-disk
// raw bytes drives the db.schema_hash drift surface.
type Schema struct {
	Version   int
	Generator string
	Domain    string
	Ontology  Ontology
	Prompts   Prompts
	Glossary  []GlossaryTerm

	// raw is the on-disk bytes (or the embedded default for Bundled()).
	// Hash() returns sha256(raw); a re-saved file with whitespace
	// differences yields a new hash, matching user intuition (write =
	// new hash = drift surface).
	raw []byte

	// DocPath is "AGENTS.md" when Load read the file, "" for Bundled().
	// Surfaced in `schema show` and `mcp.get_schema`.
	DocPath string
}

// Prompts carries the six load-bearing system prompts. Each is the
// raw text from the corresponding H2 section, ready to be passed to
// Render with per-call vars.
type Prompts struct {
	Ingest             string // {{domain}}, {{existing_titles}}, optional {{glossary}}
	UpdateExisting     string // {{domain}}, {{existing_page_body}}, {{existing_evidence}}, optional {{glossary}}
	Ask                string // {{domain}}, optional {{glossary}}
	Contradiction      string // (no required placeholders)
	PromoteRewrite     string // {{question}}, {{answer_body}}, {{evidence_quotes}}
	LintContradictions string // (no required placeholders)
}

// Ontology is the shape of the page on disk. Field order matches the
// canonical bundled order; rename is a name-string mapping over this
// list (the canonical struct field carrying evidence is fixed).
type Ontology struct {
	Fields []OntologyField
}

// OntologyField is one row of the Page ontology bullet list. CanonicalName
// maps positionally to the bundled canonical list; DeclaredName is what
// the user wrote (equal to CanonicalName for an unrenamed schema).
type OntologyField struct {
	// CanonicalName is the bundled struct field name (e.g. "evidence",
	// "title"). Maps via position to the bundled canonical list:
	// [title, body, evidence, links, sources, tags, created,
	//  updated_at, content_hash, source_ids]. The mapping is
	// position-stable across renames.
	CanonicalName string
	// DeclaredName is what the user wrote (e.g. "citations" if they
	// renamed `evidence`). Equal to CanonicalName for an unrenamed
	// schema. Read on WritePage; consulted on ParsePage.
	DeclaredName string
	Type         string
	Description  string
}

// GlossaryTerm is one row of the optional Glossary section. The
// glossary is surfaced to prompts that opt into the optional
// {{glossary}} placeholder.
type GlossaryTerm struct {
	Term       string
	Definition string
}

// ValidationError is the structured failure shape returned by Parse
// and Validate. Section names the H2 the problem lives under; Line
// is 1-indexed into the raw doc (0 when not applicable); Problem is
// a one-line human-readable summary.
type ValidationError struct {
	Section string
	Line    int
	Problem string
}

func (e ValidationError) Error() string {
	if e.Line > 0 {
		return fmt.Sprintf("schema validation: %s (line %d): %s", e.Section, e.Line, e.Problem)
	}
	if e.Section != "" {
		return fmt.Sprintf("schema validation: %s: %s", e.Section, e.Problem)
	}
	return fmt.Sprintf("schema validation: %s", e.Problem)
}

// MultiError collects every ValidationError surfaced by Parse / Validate
// so cmd/schema.go's `schema validate` can render every problem at once
// rather than the user fixing one error, re-running, fixing the next.
type MultiError struct {
	Errors []ValidationError
}

func (m MultiError) Error() string {
	var sb strings.Builder
	for i, e := range m.Errors {
		if i > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(e.Error())
	}
	return sb.String()
}

// Hash returns the sha256 of the on-disk doc bytes. Used by
// db.schema_hash to gate the lint/status drift surface and by
// `schema migrate` to skip already-migrated pages. Bundled() sets
// raw = DefaultDoc, so Bundled().Hash() is a real hex hash too —
// no sentinel, db.schema_hash treats bundled and edited uniformly.
func (s Schema) Hash() string {
	return fmt.Sprintf("%x", sha256.Sum256(s.raw))
}

// Raw returns the on-disk bytes; used by `schema show --doc`.
func (s Schema) Raw() []byte { return s.raw }

// placeholderRE matches a `{{name}}` token where name is one or more
// word characters (Q3: not text/template; the schema is a doc the
// user reads, not a Go-template-coupled artefact).
var placeholderRE = regexp.MustCompile(`\{\{(\w+)\}\}`)

// warnedPlaceholders tracks which (prompt, name) pairs we have already
// warned about, so Render emits one WARN per unknown placeholder per
// process. Keyed on a sha256 of the prompt body + name to keep memory
// bounded on a long-running process.
var warnedPlaceholders sync.Map

// Render replaces every {{name}} in prompt with vars[name]. Unknown
// names are left intact (forward-compat: a future binary may
// interpolate them). Emits one WARN per unknown name per process
// keyed on the prompt body so two distinct prompts each get one warn
// for the same unknown name.
func (s Schema) Render(prompt string, vars map[string]string) string {
	return placeholderRE.ReplaceAllStringFunc(prompt, func(m string) string {
		name := m[2 : len(m)-2]
		if v, ok := vars[name]; ok {
			return v
		}
		warnUnknownPlaceholderOnce(prompt, name)
		return m
	})
}

// warnUnknownPlaceholderOnce emits one WARN per (prompt, name) pair
// per process. The key is sha256(prompt) + ":" + name so a long-lived
// process does not retain the full prompt body in the warn map.
func warnUnknownPlaceholderOnce(prompt, name string) {
	key := fmt.Sprintf("%x:%s", sha256.Sum256([]byte(prompt)), name)
	if _, loaded := warnedPlaceholders.LoadOrStore(key, struct{}{}); loaded {
		return
	}
	fmt.Fprintf(os.Stderr, "WARN: schema: unknown placeholder {{%s}} in prompt; leaving intact\n", name)
}

// requiredPromptPlaceholders maps prompt names (as they appear on the
// Schema.Prompts field) to the set of placeholders Validate enforces.
// The list mirrors the table in the plan's Task 3 Step 3.
var requiredPromptPlaceholders = map[string][]string{
	"Ingest prompt":           {"domain", "existing_titles"},
	"Update-existing prompt":  {"domain", "existing_page_body", "existing_evidence"},
	"Ask prompt":              {"domain"},
	"Contradiction prompt":    nil,
	"Promote rewrite prompt":  {"question", "answer_body", "evidence_quotes"},
	"Lint contradictions prompt": nil,
}

// requiredOntologyFields are the canonical names that must appear in
// the parsed ontology. The user may rename them (declared name differs
// from canonical), but they MUST be present at their canonical
// position in the field list — which Parse guarantees via positional
// canonical mapping.
var requiredOntologyFields = []string{"title", "body", "evidence"}

// Validate enforces the required-prompt + required-placeholder +
// required-ontology-field contracts. Returns MultiError surfacing all
// problems at once so cmd/schema.go's `schema validate` can render
// every file:line column in one shot rather than the user fixing one
// error, re-running, fixing the next. Returns nil on success.
//
// TRUST PROPERTY UNCHANGED. Validate is purely structural. It does NOT
// verify the prompt is *good* — a user can write a schema that
// validates but produces awful pages. The validator (bundled,
// unreachable from this package) is the gate that protects the
// trust property.
func (s Schema) Validate() error {
	var errs []ValidationError

	type promptCheck struct {
		section string
		body    string
	}
	checks := []promptCheck{
		{"Ingest prompt", s.Prompts.Ingest},
		{"Update-existing prompt", s.Prompts.UpdateExisting},
		{"Ask prompt", s.Prompts.Ask},
		{"Contradiction prompt", s.Prompts.Contradiction},
		{"Promote rewrite prompt", s.Prompts.PromoteRewrite},
		{"Lint contradictions prompt", s.Prompts.LintContradictions},
	}
	for _, c := range checks {
		if strings.TrimSpace(c.body) == "" {
			errs = append(errs, ValidationError{
				Section: c.section,
				Problem: "prompt body is empty",
			})
			continue
		}
		for _, ph := range requiredPromptPlaceholders[c.section] {
			token := "{{" + ph + "}}"
			if !strings.Contains(c.body, token) {
				errs = append(errs, ValidationError{
					Section: c.section,
					Problem: fmt.Sprintf("missing required placeholder %s", token),
				})
			}
		}
	}

	declaredCanonical := make(map[string]bool, len(s.Ontology.Fields))
	for _, f := range s.Ontology.Fields {
		declaredCanonical[f.CanonicalName] = true
	}
	for _, f := range requiredOntologyFields {
		if !declaredCanonical[f] {
			errs = append(errs, ValidationError{
				Section: "Page ontology",
				Problem: fmt.Sprintf("missing required field: %s", f),
			})
		}
	}

	if len(errs) == 0 {
		return nil
	}
	return MultiError{Errors: errs}
}
