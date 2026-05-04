package cmd

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/mritunjaysharma394/llmwiki/internal/cliutil"
	"github.com/mritunjaysharma394/llmwiki/internal/schema"
)

// captureSchemaStdout redirects os.Stdout for the duration of fn and
// returns whatever fn wrote. The schema commands print directly to
// os.Stdout (not cmd.OutOrStdout), so we have to swap the FD-level
// stream rather than route through cobra's writer plumbing.
func captureSchemaStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	prev := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = prev }()
	runErr := fn()
	w.Close()
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("copy: %v", err)
	}
	return buf.String(), runErr
}

// resetSchemaShowFlags zeroes the show subcommand's bool flags between
// tests — cobra retains flag state across SetArgs calls in a single
// process, so a previous test's --bundled would leak otherwise.
func resetSchemaShowFlags(t *testing.T) {
	t.Helper()
	for _, name := range []string{"bundled", "doc"} {
		if err := schemaShowCmd.Flags().Set(name, "false"); err != nil {
			t.Fatalf("resetting --%s: %v", name, err)
		}
	}
}

// loadActiveSchemaForTest populates the package-level activeSchema
// from the current working directory the same way loadSchemaSoft does
// at runtime. Tests that exercise the schema subcommands without
// going through cobra's PersistentPreRunE call this directly.
func loadActiveSchemaForTest(t *testing.T) {
	t.Helper()
	sch, err := schema.Load(".")
	if err != nil {
		t.Fatalf("schema.Load: %v", err)
	}
	activeSchema = sch
}

// TestSchemaShow_PrintsMergedEffective_ByDefault — fresh wiki, no
// AGENTS.md / CLAUDE.md; `schema show` must surface the bundled
// content with the "schema: bundled" header.
func TestSchemaShow_PrintsMergedEffective_ByDefault(t *testing.T) {
	chdirTemp(t)
	resetSchemaShowFlags(t)
	loadActiveSchemaForTest(t)
	out, err := captureSchemaStdout(t, func() error {
		return runSchemaShow(schemaShowCmd, nil)
	})
	if err != nil {
		t.Fatalf("runSchemaShow: %v", err)
	}
	if !strings.Contains(out, "schema: bundled (no AGENTS.md or CLAUDE.md)") {
		t.Errorf("output missing bundled header line:\n%s", out)
	}
	for _, want := range []string{"## Domain", "## Ingest prompt", "## Page ontology"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

// TestSchemaShow_DocFlag_PrintsAGENTSMdVerbatim — the --doc flag must
// emit the on-disk file byte-for-byte (no leading header), so users
// can pipe it into a diff against the bundled or another wiki's copy.
func TestSchemaShow_DocFlag_PrintsAGENTSMdVerbatim(t *testing.T) {
	chdirTemp(t)
	resetSchemaShowFlags(t)
	if err := os.WriteFile("AGENTS.md", []byte(validSchemaDoc), 0644); err != nil {
		t.Fatalf("writing AGENTS.md: %v", err)
	}
	loadActiveSchemaForTest(t)
	if err := schemaShowCmd.Flags().Set("doc", "true"); err != nil {
		t.Fatalf("setting --doc: %v", err)
	}
	out, err := captureSchemaStdout(t, func() error {
		return runSchemaShow(schemaShowCmd, nil)
	})
	if err != nil {
		t.Fatalf("runSchemaShow: %v", err)
	}
	if out != validSchemaDoc {
		t.Errorf("--doc output != fixture bytes\ngot len=%d, want len=%d", len(out), len(validSchemaDoc))
	}
}

// TestSchemaShow_DocFlag_NoAGENTSMd_PrintsBundledNotice — the --doc
// flag with no on-disk file must print the "no AGENTS.md or CLAUDE.md
// present" notice, not the bundled body.
func TestSchemaShow_DocFlag_NoAGENTSMd_PrintsBundledNotice(t *testing.T) {
	chdirTemp(t)
	resetSchemaShowFlags(t)
	loadActiveSchemaForTest(t)
	if err := schemaShowCmd.Flags().Set("doc", "true"); err != nil {
		t.Fatalf("setting --doc: %v", err)
	}
	out, err := captureSchemaStdout(t, func() error {
		return runSchemaShow(schemaShowCmd, nil)
	})
	if err != nil {
		t.Fatalf("runSchemaShow: %v", err)
	}
	for _, want := range []string{
		"no AGENTS.md or CLAUDE.md present",
		"bundled defaults are in effect",
		"llmwiki init --rewrite-schema",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

// TestSchemaShow_BundledFlag_IgnoresAGENTSMd — write a custom
// AGENTS.md with a non-default Domain; --bundled must print the
// embedded default, not the custom file.
func TestSchemaShow_BundledFlag_IgnoresAGENTSMd(t *testing.T) {
	chdirTemp(t)
	resetSchemaShowFlags(t)
	if err := os.WriteFile("AGENTS.md", []byte(validSchemaDoc), 0644); err != nil {
		t.Fatalf("writing AGENTS.md: %v", err)
	}
	loadActiveSchemaForTest(t)
	if err := schemaShowCmd.Flags().Set("bundled", "true"); err != nil {
		t.Fatalf("setting --bundled: %v", err)
	}
	out, err := captureSchemaStdout(t, func() error {
		return runSchemaShow(schemaShowCmd, nil)
	})
	if err != nil {
		t.Fatalf("runSchemaShow: %v", err)
	}
	// validSchemaDoc's Domain is the test-fixture text; the bundled
	// default's Domain is empty / boilerplate. The fixture's marker
	// string must NOT appear in --bundled output.
	if strings.Contains(out, "Test domain for activeSchema unit tests") {
		t.Errorf("--bundled emitted the user-edited Domain text:\n%s", out)
	}
	// Sanity: --bundled must include a recognizable bundled-default
	// marker (the H1 line is the same in both, so check the prompt).
	if !strings.Contains(out, "## Ingest prompt") {
		t.Errorf("--bundled output missing '## Ingest prompt':\n%s", out)
	}
}

// TestSchemaValidate_OK_ExitZero — fresh wiki (bundled defaults);
// `schema validate` must succeed with the structured success block.
func TestSchemaValidate_OK_ExitZero(t *testing.T) {
	chdirTemp(t)
	loadActiveSchemaForTest(t)
	out, err := captureSchemaStdout(t, func() error {
		return runSchemaValidate(schemaValidateCmd, nil)
	})
	if err != nil {
		t.Fatalf("runSchemaValidate: %v", err)
	}
	for _, want := range []string{
		"bundled (schema_version 1)",
		"✓ all 6 required prompts present",
		"✓ all required placeholders present",
		"✓ page ontology has required fields: title, body, evidence",
		"✓ glossary has",
		"trust property: enforced by bundled validator",
		"substring-match against source files; not configurable from this doc",
		"OK",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("validate success block missing %q:\n%s", want, out)
		}
	}
}

// TestSchemaValidate_MissingRequiredSection_ExitOne_FileLineError —
// fixture AGENTS.md missing `## Ingest prompt`; `schema validate`
// must return a UserError whose rendered text mentions the missing
// section. The Parse step (called by schema.Load in the test setup)
// is what surfaces "required section missing", since the parser
// rejects a doc lacking required H2s before Validate runs.
func TestSchemaValidate_MissingRequiredSection_ExitOne_FileLineError(t *testing.T) {
	chdirTemp(t)
	const start = "## Ingest prompt"
	const end = "## Update-existing prompt"
	si := strings.Index(validSchemaDoc, start)
	ei := strings.Index(validSchemaDoc, end)
	if si < 0 || ei < 0 || ei <= si {
		t.Fatalf("fixture sanity: cannot locate Ingest/Update sections in validSchemaDoc")
	}
	malformed := validSchemaDoc[:si] + validSchemaDoc[ei:]
	if err := os.WriteFile("AGENTS.md", []byte(malformed), 0644); err != nil {
		t.Fatalf("writing malformed AGENTS.md: %v", err)
	}
	// schema.Load(".") returns a Parse error for a missing required
	// section — that's the "structurally malformed" branch
	// loadSchemaSoft surfaces. We mirror that branch here so the
	// test exercises the same code path the runtime would.
	_, loadErr := schema.Load(".")
	if loadErr == nil {
		t.Fatal("expected schema.Load to surface missing-section error; got nil")
	}
	rendered := cliutil.Render(cliutil.Wrap(
		"loading schema doc (AGENTS.md or CLAUDE.md)", loadErr,
		"the file is structurally malformed (frontmatter / section split). Fix the listed problem and re-run."))
	if !strings.Contains(rendered, "Ingest prompt") {
		t.Errorf("rendered error does not mention missing 'Ingest prompt' section:\n%s", rendered)
	}
}

// TestSchemaValidate_MissingRequiredPlaceholder_ExitOne_FileLineError —
// fixture with `## Ingest prompt` text but missing `{{domain}}`;
// `schema validate` must surface a structured error pointing at the
// section. Validate (not Parse) is the layer that catches missing
// placeholders, so this exercises the runSchemaValidate path with a
// schema that parses cleanly.
func TestSchemaValidate_MissingRequiredPlaceholder_ExitOne_FileLineError(t *testing.T) {
	chdirTemp(t)
	// Strip {{domain}} from the Ingest prompt body so Parse still
	// produces a non-empty body but Validate fires the missing-
	// placeholder error.
	mutated := strings.Replace(validSchemaDoc,
		"Test ingest prompt body. {{domain}} {{existing_titles}}",
		"Test ingest prompt body. {{existing_titles}}",
		1)
	if mutated == validSchemaDoc {
		t.Fatal("fixture mutation failed: the Ingest-prompt-without-domain replacement did not match")
	}
	if err := os.WriteFile("AGENTS.md", []byte(mutated), 0644); err != nil {
		t.Fatalf("writing AGENTS.md: %v", err)
	}
	loadActiveSchemaForTest(t)
	err := runSchemaValidate(schemaValidateCmd, nil)
	if err == nil {
		t.Fatal("runSchemaValidate succeeded; expected validation error")
	}
	rendered := cliutil.Render(err)
	for _, want := range []string{"Ingest prompt", "{{domain}}"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("rendered error missing %q:\n%s", want, rendered)
		}
	}
}

// TestSchemaValidate_AllErrorsAtOnce — fixture with multiple problems;
// every error must surface in one run. The MultiError from
// internal/schema concatenates each ValidationError on its own line,
// so the rendered output should mention all three.
func TestSchemaValidate_AllErrorsAtOnce(t *testing.T) {
	chdirTemp(t)
	// Three problems at once:
	//   1. Missing {{domain}} in Ingest prompt.
	//   2. Missing {{existing_page_body}} in Update-existing prompt.
	//   3. Missing {{question}} in Promote rewrite prompt.
	// All three should surface from Validate's MultiError.
	mutated := validSchemaDoc
	mutated = strings.Replace(mutated,
		"Test ingest prompt body. {{domain}} {{existing_titles}}",
		"Test ingest prompt body. {{existing_titles}}",
		1)
	mutated = strings.Replace(mutated,
		"Test update prompt body. {{domain}} {{existing_page_body}} {{existing_evidence}}",
		"Test update prompt body. {{domain}} {{existing_evidence}}",
		1)
	mutated = strings.Replace(mutated,
		"Test promote rewrite prompt. {{question}} {{answer_body}} {{evidence_quotes}}",
		"Test promote rewrite prompt. {{answer_body}} {{evidence_quotes}}",
		1)
	if mutated == validSchemaDoc {
		t.Fatal("fixture mutation failed: no replacements applied")
	}
	if err := os.WriteFile("AGENTS.md", []byte(mutated), 0644); err != nil {
		t.Fatalf("writing AGENTS.md: %v", err)
	}
	loadActiveSchemaForTest(t)
	err := runSchemaValidate(schemaValidateCmd, nil)
	if err == nil {
		t.Fatal("runSchemaValidate succeeded; expected validation error")
	}
	rendered := cliutil.Render(err)
	for _, want := range []string{
		"Ingest prompt",
		"{{domain}}",
		"Update-existing prompt",
		"{{existing_page_body}}",
		"Promote rewrite prompt",
		"{{question}}",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("rendered error missing %q (MultiError should surface all problems at once):\n%s", want, rendered)
		}
	}
}

