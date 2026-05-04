package schema

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixtureAllSections is a minimally complete schema doc with all eight
// required sections plus an optional Glossary. Used by several tests as
// a baseline; each test mutates a copy to exercise a specific failure.
const fixtureAllSections = `---
schema_version: 1
generator: llmwiki
---

# Test schema

## Domain

A test wiki.

## Page ontology

  - title (string) the page's primary key
  - body (markdown) the page's narrative
  - evidence (list of quotes) required

## Ingest prompt

Ingest under {{domain}}. Existing titles: {{existing_titles}}.

## Update-existing prompt

Update under {{domain}} given {{existing_page_body}} and {{existing_evidence}}.

## Ask prompt

Answer under {{domain}}.

## Contradiction prompt

Detect contradictions.

## Promote rewrite prompt

Rewrite for {{question}} given {{answer_body}} and {{evidence_quotes}}.

## Lint contradictions prompt

Lint for contradictions.

## Glossary

  - foo: a foo thing
  - bar: a bar thing
`

func TestParse_FrontmatterRoundTrip(t *testing.T) {
	doc := `---
schema_version: 1
generator: llmwiki
---

# title

## Domain
A test wiki.

## Page ontology
  - title (string) primary key
  - body (markdown) narrative
  - evidence (list of quotes) required

## Ingest prompt
Ingest under {{domain}} with {{existing_titles}}.

## Update-existing prompt
Update under {{domain}} {{existing_page_body}} {{existing_evidence}}.

## Ask prompt
Answer under {{domain}}.

## Contradiction prompt
Detect contradictions.

## Promote rewrite prompt
Rewrite {{question}} {{answer_body}} {{evidence_quotes}}.

## Lint contradictions prompt
Lint contradictions.
`
	s, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("Parse: unexpected error: %v", err)
	}
	if s.Version != 1 {
		t.Errorf("Version = %d, want 1", s.Version)
	}
	if s.Generator != "llmwiki" {
		t.Errorf("Generator = %q, want %q", s.Generator, "llmwiki")
	}
}

func TestParse_ExtractsAllRequiredSections(t *testing.T) {
	s, err := Parse([]byte(fixtureAllSections))
	if err != nil {
		t.Fatalf("Parse: unexpected error: %v", err)
	}
	if !strings.Contains(s.Domain, "test wiki") {
		t.Errorf("Domain = %q, want substring %q", s.Domain, "test wiki")
	}
	if s.Prompts.Ingest == "" {
		t.Error("Prompts.Ingest empty")
	}
	if s.Prompts.UpdateExisting == "" {
		t.Error("Prompts.UpdateExisting empty")
	}
	if s.Prompts.Ask == "" {
		t.Error("Prompts.Ask empty")
	}
	if s.Prompts.Contradiction == "" {
		t.Error("Prompts.Contradiction empty")
	}
	if s.Prompts.PromoteRewrite == "" {
		t.Error("Prompts.PromoteRewrite empty")
	}
	if s.Prompts.LintContradictions == "" {
		t.Error("Prompts.LintContradictions empty")
	}
	if len(s.Ontology.Fields) == 0 {
		t.Error("Ontology.Fields empty")
	}
}

func TestParse_GlossaryOptional_AbsentParsesEmpty(t *testing.T) {
	// Strip the Glossary section from the fixture.
	doc := strings.Split(fixtureAllSections, "## Glossary")[0]
	s, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("Parse: unexpected error: %v", err)
	}
	if len(s.Glossary) != 0 {
		t.Errorf("len(Glossary) = %d, want 0", len(s.Glossary))
	}
}

func TestParse_GlossaryPresent_ParsesBulletList(t *testing.T) {
	s, err := Parse([]byte(fixtureAllSections))
	if err != nil {
		t.Fatalf("Parse: unexpected error: %v", err)
	}
	if len(s.Glossary) != 2 {
		t.Fatalf("len(Glossary) = %d, want 2", len(s.Glossary))
	}
	if s.Glossary[0].Term != "foo" || s.Glossary[0].Definition != "a foo thing" {
		t.Errorf("Glossary[0] = %+v", s.Glossary[0])
	}
	if s.Glossary[1].Term != "bar" || s.Glossary[1].Definition != "a bar thing" {
		t.Errorf("Glossary[1] = %+v", s.Glossary[1])
	}
}

func TestParse_OntologyParsesBulletList(t *testing.T) {
	s, err := Parse([]byte(fixtureAllSections))
	if err != nil {
		t.Fatalf("Parse: unexpected error: %v", err)
	}
	if len(s.Ontology.Fields) != 3 {
		t.Fatalf("len(Ontology.Fields) = %d, want 3", len(s.Ontology.Fields))
	}
	want := []struct {
		canonical, declared, typ string
	}{
		{"title", "title", "string"},
		{"body", "body", "markdown"},
		{"evidence", "evidence", "list of quotes"},
	}
	for i, w := range want {
		got := s.Ontology.Fields[i]
		if got.CanonicalName != w.canonical {
			t.Errorf("Fields[%d].CanonicalName = %q, want %q", i, got.CanonicalName, w.canonical)
		}
		if got.DeclaredName != w.declared {
			t.Errorf("Fields[%d].DeclaredName = %q, want %q", i, got.DeclaredName, w.declared)
		}
		if got.Type != w.typ {
			t.Errorf("Fields[%d].Type = %q, want %q", i, got.Type, w.typ)
		}
		if got.Description == "" {
			t.Errorf("Fields[%d].Description empty", i)
		}
	}
}

func TestParse_MissingRequiredSection_Errors_WithLineNumber(t *testing.T) {
	// Strip "## Ingest prompt" and the lines under it to the next section.
	parts := strings.SplitN(fixtureAllSections, "## Ingest prompt", 2)
	tail := parts[1]
	idx := strings.Index(tail, "## Update-existing")
	doc := parts[0] + tail[idx:]
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("Parse: expected error, got nil")
	}
	var ve ValidationError
	if !errors.As(err, &ve) {
		// also accept MultiError with at least one matching ValidationError
		var me MultiError
		if errors.As(err, &me) {
			found := false
			for _, e := range me.Errors {
				if e.Section == "Ingest prompt" && strings.Contains(e.Problem, "required section missing") {
					found = true
					ve = e
					break
				}
			}
			if !found {
				t.Fatalf("MultiError did not contain Ingest prompt missing error: %v", me)
			}
		} else {
			t.Fatalf("error not ValidationError or MultiError: %T %v", err, err)
		}
	}
	if ve.Section != "Ingest prompt" {
		t.Errorf("Section = %q, want %q", ve.Section, "Ingest prompt")
	}
	if !strings.Contains(ve.Problem, "required section missing") {
		t.Errorf("Problem = %q, want substring %q", ve.Problem, "required section missing")
	}
}

func TestParse_UnknownSchemaVersion_Errors(t *testing.T) {
	doc := strings.Replace(fixtureAllSections, "schema_version: 1", "schema_version: 2", 1)
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("Parse: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "unknown schema_version") {
		t.Errorf("error = %v, want substring 'unknown schema_version'", err)
	}
	if !strings.Contains(err.Error(), "version 1") {
		t.Errorf("error = %v, want substring 'version 1'", err)
	}
}

func TestParse_MalformedFrontmatter_Errors(t *testing.T) {
	doc := `---
schema_version not_an_int
---

## Domain
x.
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("Parse: expected error, got nil")
	}
	var ve ValidationError
	if !errors.As(err, &ve) {
		var me MultiError
		if errors.As(err, &me) && len(me.Errors) > 0 {
			ve = me.Errors[0]
		} else {
			t.Fatalf("error not ValidationError: %T %v", err, err)
		}
	}
	if ve.Line <= 0 {
		t.Errorf("Line = %d, want > 0", ve.Line)
	}
}

func TestParse_NoFrontmatter_Errors(t *testing.T) {
	doc := `# title

## Domain
x.
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("Parse: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "frontmatter") {
		t.Errorf("error = %v, want substring 'frontmatter'", err)
	}
}

func TestParse_DuplicateSection_Errors(t *testing.T) {
	doc := strings.Replace(
		fixtureAllSections,
		"## Ask prompt\n\nAnswer under {{domain}}.",
		"## Ask prompt\n\nAnswer under {{domain}}.\n\n## Domain\n\nDuplicate.",
		1,
	)
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatal("Parse: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "duplicate section") {
		t.Errorf("error = %v, want substring 'duplicate section'", err)
	}
}

// ---------- Task 2: Bundled() + Load() ----------

func TestBundled_Parses_NoError(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Bundled() panicked: %v", r)
		}
	}()
	s := Bundled()
	if s.Version != 1 {
		t.Errorf("Bundled().Version = %d, want 1", s.Version)
	}
	if s.Domain == "" {
		t.Error("Bundled().Domain empty")
	}
	if len(s.Ontology.Fields) == 0 {
		t.Error("Bundled().Ontology.Fields empty")
	}
}

func TestBundled_HashIsRealHex(t *testing.T) {
	s := Bundled()
	h := s.Hash()
	want := fmt.Sprintf("%x", sha256.Sum256(DefaultDoc))
	if h != want {
		t.Errorf("Bundled().Hash() = %q, want %q", h, want)
	}
	if h == "bundled" {
		t.Error("Bundled().Hash() must be a real sha256 hex, not the legacy 'bundled' sentinel")
	}
	if len(h) != 64 {
		t.Errorf("Bundled().Hash() = %q (len %d), want 64-hex", h, len(h))
	}
}

func TestBundled_AllRequiredPromptsNonEmpty(t *testing.T) {
	s := Bundled()
	cases := []struct {
		name string
		got  string
	}{
		{"Ingest", s.Prompts.Ingest},
		{"UpdateExisting", s.Prompts.UpdateExisting},
		{"Ask", s.Prompts.Ask},
		{"Contradiction", s.Prompts.Contradiction},
		{"PromoteRewrite", s.Prompts.PromoteRewrite},
		{"LintContradictions", s.Prompts.LintContradictions},
	}
	for _, c := range cases {
		if strings.TrimSpace(c.got) == "" {
			t.Errorf("Bundled().Prompts.%s empty", c.name)
		}
	}
}

func TestLoad_FallsBackToBundledWhenAGENTSMdAbsent(t *testing.T) {
	tmp := t.TempDir()
	s, err := Load(tmp)
	if err != nil {
		t.Fatalf("Load: unexpected error: %v", err)
	}
	if s.DocPath != "" {
		t.Errorf("DocPath = %q, want empty (fell back to Bundled)", s.DocPath)
	}
	if s.Hash() != Bundled().Hash() {
		t.Errorf("Load(empty dir).Hash() = %q, want bundled hash %q", s.Hash(), Bundled().Hash())
	}
}

func TestLoad_ParsesAGENTSMdWhenPresent(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "AGENTS.md")
	if err := os.WriteFile(path, []byte(fixtureAllSections), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	s, err := Load(tmp)
	if err != nil {
		t.Fatalf("Load: unexpected error: %v", err)
	}
	if s.DocPath != "AGENTS.md" {
		t.Errorf("DocPath = %q, want %q", s.DocPath, "AGENTS.md")
	}
	want := fmt.Sprintf("%x", sha256.Sum256([]byte(fixtureAllSections)))
	if s.Hash() != want {
		t.Errorf("Hash() = %q, want %q (sha256 of fixture bytes)", s.Hash(), want)
	}
}
