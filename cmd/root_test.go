package cmd

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/mritunjaysharma394/llmwiki/internal/cliutil"
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
