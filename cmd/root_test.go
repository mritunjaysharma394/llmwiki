package cmd

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/mritunjaysharma394/llmwiki/internal/cliutil"
	"github.com/mritunjaysharma394/llmwiki/internal/schema"
	"github.com/spf13/cobra"
)

func TestExecuteRendersUserError(t *testing.T) {
	// Inject a fake command that always returns a UserError.
	probe := &cobra.Command{
		Use: "probe-fail",
		// Override the inherited persistent prerun so loadConfig isn't invoked
		// in the test environment (no .llmwiki/config.toml).
		PersistentPreRunE: func(*cobra.Command, []string) error { return nil },
		RunE: func(*cobra.Command, []string) error {
			return cliutil.Wrap("ingest failed", errors.New("HTTP 503"), "check URL")
		},
	}
	rootCmd.AddCommand(probe)
	defer rootCmd.RemoveCommand(probe)

	var buf bytes.Buffer
	rootCmd.SetErr(&buf)
	rootCmd.SetOut(&buf)
	rootCmd.SetArgs([]string{"probe-fail"})

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error")
	}
	out := cliutil.Render(err)
	for _, want := range []string{"Error: ingest failed", "cause: HTTP 503", "try:   check URL"} {
		if !strings.Contains(out, want) {
			t.Errorf("Render() missing %q in:\n%s", want, out)
		}
	}
}

// TestApplyIngestDefaultsFillsZeroValues verifies that a zero-valued
// IngestConfig (the shape pre-v3 configs decode into when [ingest] is absent)
// is silently filled with the defaults the v3 init template would have
// written. This is the contract that lets pre-sub-project-3 wikis keep
// running unmodified.
func TestApplyIngestDefaultsFillsZeroValues(t *testing.T) {
	var c IngestConfig
	applyIngestDefaults(&c)
	if c.MaxFileBytes != 256*1024 {
		t.Errorf("MaxFileBytes = %d, want %d", c.MaxFileBytes, 256*1024)
	}
	if c.ChunkSizeBytes != 16*1024 {
		t.Errorf("ChunkSizeBytes = %d, want %d", c.ChunkSizeBytes, 16*1024)
	}
	if c.HTTPTimeoutSeconds != 30 {
		t.Errorf("HTTPTimeoutSeconds = %d, want 30", c.HTTPTimeoutSeconds)
	}
	if c.HTTPMaxBytes != 5*1024*1024 {
		t.Errorf("HTTPMaxBytes = %d", c.HTTPMaxBytes)
	}
	if c.PDFMinTextPerPage != 50 {
		t.Errorf("PDFMinTextPerPage = %d", c.PDFMinTextPerPage)
	}
	if !c.RespectGitignoreOrDefault() {
		t.Error("RespectGitignore default should be true when nil")
	}
}

// TestApplyIngestDefaultsKeepsExplicitValues makes sure non-zero values from
// the config file are preserved — defaults only fill what the user left blank.
func TestApplyIngestDefaultsKeepsExplicitValues(t *testing.T) {
	f := false
	c := IngestConfig{
		MaxFileBytes:       1024,
		ChunkSizeBytes:     2048,
		HTTPTimeoutSeconds: 5,
		HTTPMaxBytes:       1024 * 1024,
		PDFMinTextPerPage:  10,
		RespectGitignore:   &f,
	}
	applyIngestDefaults(&c)
	if c.MaxFileBytes != 1024 {
		t.Errorf("MaxFileBytes overwritten: %d", c.MaxFileBytes)
	}
	if c.ChunkSizeBytes != 2048 {
		t.Errorf("ChunkSizeBytes overwritten: %d", c.ChunkSizeBytes)
	}
	if c.HTTPTimeoutSeconds != 5 {
		t.Errorf("HTTPTimeoutSeconds overwritten: %d", c.HTTPTimeoutSeconds)
	}
	if c.HTTPMaxBytes != 1024*1024 {
		t.Errorf("HTTPMaxBytes overwritten: %d", c.HTTPMaxBytes)
	}
	if c.PDFMinTextPerPage != 10 {
		t.Errorf("PDFMinTextPerPage overwritten: %d", c.PDFMinTextPerPage)
	}
	if c.RespectGitignoreOrDefault() {
		t.Error("explicit RespectGitignore=false was overwritten")
	}
}

// TestLegacyConfigWithoutIngestBlockDecodesAndDefaults simulates a pre-v3
// .llmwiki/config.toml on disk: only [llm], [wiki], [ask] present, no
// [ingest] block. Decoding into Config and running applyIngestDefaults should
// yield a working IngestConfig.
func TestLegacyConfigWithoutIngestBlockDecodesAndDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	body := `[llm]
provider = "ollama"
model = "x"

[wiki]
wiki_dir = ".llmwiki/wiki"
raw_dir = ".llmwiki/raw"
db_path = ".llmwiki/wiki.db"

[ask]
auto_save = true
`
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	var got Config
	if _, err := toml.DecodeFile(path, &got); err != nil {
		t.Fatalf("DecodeFile: %v", err)
	}
	applyIngestDefaults(&got.Ingest)
	if got.Ingest.MaxFileBytes == 0 {
		t.Error("MaxFileBytes default not applied for legacy config")
	}
	if got.Ingest.ChunkSizeBytes == 0 {
		t.Error("ChunkSizeBytes default not applied for legacy config")
	}
	if !got.Ingest.RespectGitignoreOrDefault() {
		t.Error("RespectGitignore default not true for legacy config")
	}
}

// TestExplicitIngestBlockOverridesDefaults confirms a config with [ingest]
// values flowing through TOML decoding takes precedence — no silent default
// clobbering.
func TestExplicitIngestBlockOverridesDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	body := `[llm]
provider = "ollama"
model = "x"

[wiki]
wiki_dir = ".llmwiki/wiki"
raw_dir = ".llmwiki/raw"
db_path = ".llmwiki/wiki.db"

[ingest]
max_file_bytes = 4096
chunk_size_bytes = 8192
http_timeout_seconds = 7
respect_gitignore = false
`
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	var got Config
	if _, err := toml.DecodeFile(path, &got); err != nil {
		t.Fatalf("DecodeFile: %v", err)
	}
	applyIngestDefaults(&got.Ingest)
	if got.Ingest.MaxFileBytes != 4096 {
		t.Errorf("MaxFileBytes = %d, want 4096", got.Ingest.MaxFileBytes)
	}
	if got.Ingest.ChunkSizeBytes != 8192 {
		t.Errorf("ChunkSizeBytes = %d, want 8192", got.Ingest.ChunkSizeBytes)
	}
	if got.Ingest.HTTPTimeoutSeconds != 7 {
		t.Errorf("HTTPTimeoutSeconds = %d, want 7", got.Ingest.HTTPTimeoutSeconds)
	}
	if got.Ingest.RespectGitignoreOrDefault() {
		t.Error("explicit respect_gitignore=false overridden by default")
	}
}

// ptrBool is a tiny helper for the *bool tests below — keeps the fixture
// values terse and avoids local variable noise inside each test.
func ptrBool(b bool) *bool { return &b }

// TestApplyIngestDefaults_UpdateExistingDefaultsFalse verifies that an
// IngestConfig with UpdateExisting nil — the shape pre-v0.6 configs decode
// into when [ingest] update_existing is absent — defaults to false. Q11 in
// the sub-project 6b plan locks this contract: default-off, opt-in only.
func TestApplyIngestDefaults_UpdateExistingDefaultsFalse(t *testing.T) {
	var c IngestConfig
	applyIngestDefaults(&c)
	if c.UpdateExisting == nil {
		t.Fatal("UpdateExisting nil after applyIngestDefaults; expected non-nil pointer to false")
	}
	if *c.UpdateExisting {
		t.Errorf("UpdateExisting default = true, want false (Q11)")
	}
	if c.UpdateExistingOrDefault() {
		t.Error("UpdateExistingOrDefault default should be false")
	}
}

// TestApplyIngestDefaults_UpdateExistingHonoursExplicitTrue makes sure
// applyIngestDefaults doesn't clobber an explicit true.
func TestApplyIngestDefaults_UpdateExistingHonoursExplicitTrue(t *testing.T) {
	c := IngestConfig{UpdateExisting: ptrBool(true)}
	applyIngestDefaults(&c)
	if c.UpdateExisting == nil || !*c.UpdateExisting {
		t.Errorf("explicit UpdateExisting=true overwritten by default")
	}
}

// TestApplyIngestDefaults_UpdateExistingHonoursExplicitFalse confirms that
// an explicitly-false pointer (the "user opted out, even though default is
// also off" case) survives applyIngestDefaults intact. Mirrors the
// RespectGitignore *bool disambiguation pattern.
func TestApplyIngestDefaults_UpdateExistingHonoursExplicitFalse(t *testing.T) {
	c := IngestConfig{UpdateExisting: ptrBool(false)}
	applyIngestDefaults(&c)
	if c.UpdateExisting == nil {
		t.Fatal("UpdateExisting became nil")
	}
	if *c.UpdateExisting {
		t.Error("explicit UpdateExisting=false flipped to true")
	}
}

// TestApplyIngestDefaults_TunablesDefaults verifies the three integer
// tunables default to 20 / 50 / 2 (Q5/Q7) when the [ingest] block leaves
// them at zero.
func TestApplyIngestDefaults_TunablesDefaults(t *testing.T) {
	var c IngestConfig
	applyIngestDefaults(&c)
	if c.UpdateExistingMaxCandidatesPerSource != 20 {
		t.Errorf("UpdateExistingMaxCandidatesPerSource = %d, want 20",
			c.UpdateExistingMaxCandidatesPerSource)
	}
	if c.UpdateExistingMaxCandidatesTotal != 50 {
		t.Errorf("UpdateExistingMaxCandidatesTotal = %d, want 50",
			c.UpdateExistingMaxCandidatesTotal)
	}
	if c.UpdateExistingQuoteFloor != 2 {
		t.Errorf("UpdateExistingQuoteFloor = %d, want 2",
			c.UpdateExistingQuoteFloor)
	}
}

// TestApplyIngestDefaults_TunablesHonourExplicit verifies that non-zero
// values from the config file aren't clobbered.
func TestApplyIngestDefaults_TunablesHonourExplicit(t *testing.T) {
	c := IngestConfig{
		UpdateExistingMaxCandidatesPerSource: 7,
		UpdateExistingMaxCandidatesTotal:     11,
		UpdateExistingQuoteFloor:             5,
	}
	applyIngestDefaults(&c)
	if c.UpdateExistingMaxCandidatesPerSource != 7 {
		t.Errorf("MaxCandidatesPerSource = %d, want 7",
			c.UpdateExistingMaxCandidatesPerSource)
	}
	if c.UpdateExistingMaxCandidatesTotal != 11 {
		t.Errorf("MaxCandidatesTotal = %d, want 11",
			c.UpdateExistingMaxCandidatesTotal)
	}
	if c.UpdateExistingQuoteFloor != 5 {
		t.Errorf("QuoteFloor = %d, want 5", c.UpdateExistingQuoteFloor)
	}
}

// TestLoadConfig_DecodesUpdateExistingTOML verifies that a config.toml
// with [ingest] update_existing = true and one of the tunables decodes
// into the new fields and survives applyIngestDefaults.
func TestLoadConfig_DecodesUpdateExistingTOML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	body := `[llm]
provider = "ollama"
model = "x"

[wiki]
wiki_dir = ".llmwiki/wiki"
raw_dir = ".llmwiki/raw"
db_path = ".llmwiki/wiki.db"

[ingest]
update_existing = true
update_existing_max_candidates_per_source = 7
`
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	var got Config
	if _, err := toml.DecodeFile(path, &got); err != nil {
		t.Fatalf("DecodeFile: %v", err)
	}
	if got.Ingest.UpdateExisting == nil {
		t.Fatal("update_existing did not decode into a non-nil *bool")
	}
	if !*got.Ingest.UpdateExisting {
		t.Error("update_existing = true decoded as false")
	}
	if got.Ingest.UpdateExistingMaxCandidatesPerSource != 7 {
		t.Errorf("update_existing_max_candidates_per_source = %d, want 7",
			got.Ingest.UpdateExistingMaxCandidatesPerSource)
	}
	applyIngestDefaults(&got.Ingest)
	// applyIngestDefaults must not clobber the explicit 7, but must fill
	// the unset tunables with their defaults (50 and 2).
	if got.Ingest.UpdateExistingMaxCandidatesPerSource != 7 {
		t.Errorf("MaxCandidatesPerSource overwritten: %d", got.Ingest.UpdateExistingMaxCandidatesPerSource)
	}
	if got.Ingest.UpdateExistingMaxCandidatesTotal != 50 {
		t.Errorf("MaxCandidatesTotal default not applied: %d", got.Ingest.UpdateExistingMaxCandidatesTotal)
	}
	if got.Ingest.UpdateExistingQuoteFloor != 2 {
		t.Errorf("QuoteFloor default not applied: %d", got.Ingest.UpdateExistingQuoteFloor)
	}
	if !got.Ingest.UpdateExistingOrDefault() {
		t.Error("UpdateExistingOrDefault should be true after explicit decode")
	}
}

// schemaTestConfig is the minimal config.toml body the activeSchema
// loadConfig tests below need. Uses ollama so loadConfig doesn't trip
// on a missing API key env var — schema loading is the unit under test,
// not provider selection.
const schemaTestConfig = `[llm]
provider = "ollama"
model = "x"

[wiki]
wiki_dir = ".llmwiki/wiki"
raw_dir = ".llmwiki/raw"
db_path = ".llmwiki/wiki.db"
`

// validSchemaDoc is a syntactically-valid AGENTS.md / CLAUDE.md body
// used by the activeSchema tests below. It mirrors the bundled default
// in shape but distinguishes itself in the Domain section so the hash
// differs from schema.Bundled().Hash().
const validSchemaDoc = `---
schema_version: 1
generator: llmwiki-test
---

# llmwiki schema (test fixture)

## Domain

Test domain for activeSchema unit tests.

## Page ontology

  - title         (string)         the page's primary key; unique per wiki
  - body          (markdown)       the page's narrative
  - evidence      (list of quotes) verbatim spans from sources; required, >= 1
  - links         (list)           Obsidian wikilinks declared structurally
  - sources       (list of paths)  derived from evidence; emitted by WritePage
  - tags          (list of strings) Obsidian/Dataview-friendly
  - created       (date)           first-ingest date
  - updated_at    (RFC3339 ts)     last-write timestamp
  - content_hash  (sha256)         body hash; recomputed at every write
  - source_ids    (list of int)    DB row IDs backing this page

## Ingest prompt

Test ingest prompt body. {{domain}} {{existing_titles}}

## Update-existing prompt

Test update prompt body. {{domain}} {{existing_page_body}} {{existing_evidence}}

## Ask prompt

Test ask prompt body. {{domain}}

## Contradiction prompt

Test contradiction prompt body.

## Promote rewrite prompt

Test promote rewrite prompt. {{question}} {{answer_body}} {{evidence_quotes}}

## Lint contradictions prompt

Test lint contradictions prompt body.
`

// TestLoadConfig_LoadsAGENTSMdWhenPresent writes a fixture AGENTS.md to
// the wiki root and asserts loadConfig parses it into activeSchema with
// DocPath == "AGENTS.md" and Hash() == sha256(<file bytes>). This is the
// happy path: the user has an AGENTS.md and it drives every wiki entrypoint.
func TestLoadConfig_LoadsAGENTSMdWhenPresent(t *testing.T) {
	chdirTemp(t)
	resetProviderFlags(t)
	writeMinimalConfig(t, schemaTestConfig)
	if err := os.WriteFile("AGENTS.md", []byte(validSchemaDoc), 0644); err != nil {
		t.Fatalf("writing AGENTS.md: %v", err)
	}
	if err := loadConfig(); err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if activeSchema.DocPath != "AGENTS.md" {
		t.Errorf("activeSchema.DocPath = %q, want %q", activeSchema.DocPath, "AGENTS.md")
	}
	wantHash := fmt.Sprintf("%x", sha256.Sum256([]byte(validSchemaDoc)))
	if got := activeSchema.Hash(); got != wantHash {
		t.Errorf("activeSchema.Hash() = %q, want %q (sha256 of fixture bytes)", got, wantHash)
	}
}

// TestLoadConfig_FallsBackToBundledWhenAGENTSMdAbsent confirms a wiki
// with no AGENTS.md (and no CLAUDE.md) gets the bundled default. This
// is the v0.6 -> v0.7 compatibility surface: pre-v0.7 wikis see zero
// behaviour change.
func TestLoadConfig_FallsBackToBundledWhenAGENTSMdAbsent(t *testing.T) {
	chdirTemp(t)
	resetProviderFlags(t)
	writeMinimalConfig(t, schemaTestConfig)
	if err := loadConfig(); err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if activeSchema.DocPath != "" {
		t.Errorf("activeSchema.DocPath = %q, want \"\" (bundled has no on-disk path)", activeSchema.DocPath)
	}
	if got, want := activeSchema.Hash(), schema.Bundled().Hash(); got != want {
		t.Errorf("activeSchema.Hash() = %q, want bundled hash %q", got, want)
	}
}

// TestLoadConfig_LoadsCLAUDEMd_WhenAGENTSMdAbsentButCLAUDEMdPresent
// verifies the dual-filename scan: CLAUDE.md (Claude Code native) is
// read when AGENTS.md (the canonical multi-vendor name) is absent.
// Phase A's SchemaFilenames slice locks the [AGENTS.md, CLAUDE.md] order;
// this test pins the fallback half of that contract from the cmd/ side.
func TestLoadConfig_LoadsCLAUDEMd_WhenAGENTSMdAbsentButCLAUDEMdPresent(t *testing.T) {
	chdirTemp(t)
	resetProviderFlags(t)
	writeMinimalConfig(t, schemaTestConfig)
	if err := os.WriteFile("CLAUDE.md", []byte(validSchemaDoc), 0644); err != nil {
		t.Fatalf("writing CLAUDE.md: %v", err)
	}
	if err := loadConfig(); err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if activeSchema.DocPath != "CLAUDE.md" {
		t.Errorf("activeSchema.DocPath = %q, want %q", activeSchema.DocPath, "CLAUDE.md")
	}
	wantHash := fmt.Sprintf("%x", sha256.Sum256([]byte(validSchemaDoc)))
	if got := activeSchema.Hash(); got != wantHash {
		t.Errorf("activeSchema.Hash() = %q, want %q", got, wantHash)
	}
}

// TestLoadConfig_AGENTSMdValidationFails_ErrorsLoudly writes an AGENTS.md
// missing the `## Ingest prompt` section and asserts loadConfig returns
// an error mentioning the missing section. This is the load-time
// validation contract: a malformed schema fails before the first LLM
// call, not at first ingest.
func TestLoadConfig_AGENTSMdValidationFails_ErrorsLoudly(t *testing.T) {
	chdirTemp(t)
	resetProviderFlags(t)
	writeMinimalConfig(t, schemaTestConfig)
	// Strip the `## Ingest prompt` section (and its body) from the
	// fixture so Parse/Validate must surface a "required section
	// missing" error.
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
	err := loadConfig()
	if err == nil {
		t.Fatal("loadConfig succeeded on malformed AGENTS.md; want error")
	}
	rendered := cliutil.Render(err)
	if !strings.Contains(rendered, "Ingest prompt") {
		t.Errorf("rendered error does not mention missing 'Ingest prompt' section:\n%s", rendered)
	}
}

// TestLoadConfig_AGENTSMdHashStable_AcrossReads calls loadConfig twice
// on the same fixture and asserts activeSchema.Hash() is identical both
// times. This is the stability guard for db.schema_hash queries — if a
// re-read produced a different hash for the same bytes, every status /
// lint drift surface would false-positive.
func TestLoadConfig_AGENTSMdHashStable_AcrossReads(t *testing.T) {
	chdirTemp(t)
	resetProviderFlags(t)
	writeMinimalConfig(t, schemaTestConfig)
	if err := os.WriteFile("AGENTS.md", []byte(validSchemaDoc), 0644); err != nil {
		t.Fatalf("writing AGENTS.md: %v", err)
	}
	if err := loadConfig(); err != nil {
		t.Fatalf("loadConfig (first): %v", err)
	}
	first := activeSchema.Hash()
	if err := loadConfig(); err != nil {
		t.Fatalf("loadConfig (second): %v", err)
	}
	second := activeSchema.Hash()
	if first != second {
		t.Errorf("activeSchema.Hash() unstable across reads:\n  first  = %s\n  second = %s", first, second)
	}
}
