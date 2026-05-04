package cmd

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/mritunjaysharma394/llmwiki/internal/cliutil"
	"github.com/mritunjaysharma394/llmwiki/internal/schema"
)

// chdirTemp moves the working directory to a fresh temp dir for the duration
// of one test and restores it after. cmd/init.go and loadConfig() both use
// relative paths (".llmwiki/..."), so we need the cwd anchored somewhere
// writable and isolated.
func chdirTemp(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
	return dir
}

// resetProviderFlags clears the persistent --provider/--model flag globals so
// one test's state can't leak into another's loadConfig() call.
func resetProviderFlags(t *testing.T) {
	t.Helper()
	prevP, prevM := overrideProvider, overrideModel
	overrideProvider, overrideModel = "", ""
	t.Cleanup(func() { overrideProvider, overrideModel = prevP, prevM })
}

// runInitWithProvider invokes runInit() the way cobra would: it sets the
// initCmd's --provider flag, then calls runInit. We reset the flag on cleanup.
func runInitWithProvider(t *testing.T, provider string) error {
	t.Helper()
	prev, _ := initCmd.Flags().GetString("provider")
	if err := initCmd.Flags().Set("provider", provider); err != nil {
		t.Fatalf("setting --provider: %v", err)
	}
	t.Cleanup(func() { _ = initCmd.Flags().Set("provider", prev) })
	return runInit(initCmd, nil)
}

// runInitWithFlags invokes runInit() with arbitrary flag overrides. Flags
// not in the map keep their default value. Each overridden flag is reset
// to its prior value on cleanup so one test's state can't leak.
func runInitWithFlags(t *testing.T, flags map[string]string) error {
	t.Helper()
	for name, val := range flags {
		prev, _ := initCmd.Flags().GetString(name)
		// Bool flags also round-trip through GetString in cobra; if that
		// returns an empty string we still set the desired textual value.
		if f := initCmd.Flags().Lookup(name); f != nil {
			prev = f.Value.String()
		}
		if err := initCmd.Flags().Set(name, val); err != nil {
			t.Fatalf("setting --%s=%q: %v", name, val, err)
		}
		prevCopy := prev
		nameCopy := name
		t.Cleanup(func() { _ = initCmd.Flags().Set(nameCopy, prevCopy) })
	}
	return runInit(initCmd, nil)
}

// captureInitStdout swaps os.Stdout for a pipe for the duration of fn and
// returns whatever fn wrote. cmd/init.go prints with fmt.Println /
// fmt.Printf directly to os.Stdout, so we have to swap the FD-level
// stream rather than route through cobra plumbing.
func captureInitStdout(t *testing.T, fn func() error) (string, error) {
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

// tolerateUserError treats a *cliutil.UserError as a non-fatal outcome of
// runInit — the schema-doc write happens before the provider-key check, so
// tests that don't set GEMINI_API_KEY still need to assert on disk state
// even when init returns the missing-key UserError.
func tolerateUserError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	var ue *cliutil.UserError
	if !errors.As(err, &ue) {
		t.Fatalf("runInit: %v", err)
	}
}

// TestInit_DefaultProviderIsGemini asserts that running `llmwiki init` in a
// non-TTY environment with no --provider flag writes the Gemini template.
// The non-TTY path is what CI hits, and is also the path most users encounter
// the first time when piping output through tee/script.
func TestInit_DefaultProviderIsGemini(t *testing.T) {
	chdirTemp(t)
	if err := runInitWithProvider(t, ""); err != nil {
		// init may emit a UserError when GEMINI_API_KEY is unset; that's not
		// a failure of *config writing*. Tolerate it; assert on disk only.
		var ue *cliutil.UserError
		if !errors.As(err, &ue) {
			t.Fatalf("runInit: %v", err)
		}
	}
	body, err := os.ReadFile(filepath.Join(".llmwiki", "config.toml"))
	if err != nil {
		t.Fatalf("reading config: %v", err)
	}
	want := []string{`provider = "gemini"`, `model = "gemini-2.5-flash"`}
	for _, w := range want {
		if !strings.Contains(string(body), w) {
			t.Errorf("config missing %q in:\n%s", w, body)
		}
	}
}

// TestInit_OpenAICompatTemplate asserts the openai-compatible template carries
// a [providers.openai_compat] block with the bits an operator needs to point
// the client at any of the five supported endpoints.
func TestInit_OpenAICompatTemplate(t *testing.T) {
	chdirTemp(t)
	if err := runInitWithProvider(t, "openai-compatible"); err != nil {
		var ue *cliutil.UserError
		if !errors.As(err, &ue) {
			t.Fatalf("runInit: %v", err)
		}
	}
	body, err := os.ReadFile(filepath.Join(".llmwiki", "config.toml"))
	if err != nil {
		t.Fatalf("reading config: %v", err)
	}
	want := []string{
		`provider = "openai-compatible"`,
		`[providers.openai_compat]`,
		`base_url`,
		`api_key_env`,
		"openrouter.ai",
		"groq.com",
		"together.xyz",
		"cerebras.ai",
		"mistral.ai",
	}
	for _, w := range want {
		if !strings.Contains(string(body), w) {
			t.Errorf("config missing %q in:\n%s", w, body)
		}
	}
}

// TestInit_AnthropicTemplate is a regression guard: --provider anthropic must
// keep producing the existing template shape.
func TestInit_AnthropicTemplate(t *testing.T) {
	chdirTemp(t)
	if err := runInitWithProvider(t, "anthropic"); err != nil {
		var ue *cliutil.UserError
		if !errors.As(err, &ue) {
			t.Fatalf("runInit: %v", err)
		}
	}
	body, err := os.ReadFile(filepath.Join(".llmwiki", "config.toml"))
	if err != nil {
		t.Fatalf("reading config: %v", err)
	}
	for _, w := range []string{`provider = "anthropic"`, `model = "claude-haiku-4-5"`} {
		if !strings.Contains(string(body), w) {
			t.Errorf("config missing %q in:\n%s", w, body)
		}
	}
}

// TestInit_OllamaTemplate is the second regression guard.
func TestInit_OllamaTemplate(t *testing.T) {
	chdirTemp(t)
	if err := runInitWithProvider(t, "ollama"); err != nil {
		t.Fatalf("runInit: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(".llmwiki", "config.toml"))
	if err != nil {
		t.Fatalf("reading config: %v", err)
	}
	for _, w := range []string{`provider = "ollama"`, `model = "llama3.2"`, `ollama_url = "http://localhost:11434"`} {
		if !strings.Contains(string(body), w) {
			t.Errorf("config missing %q in:\n%s", w, body)
		}
	}
}

// writeMinimalConfig drops a config.toml at .llmwiki/config.toml and creates
// the wiki/raw/answers subdirs that loadConfig + db.Open expect.
func writeMinimalConfig(t *testing.T, body string) {
	t.Helper()
	if err := os.MkdirAll(".llmwiki", 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(".llmwiki", "config.toml"), []byte(body), 0644); err != nil {
		t.Fatalf("writing config: %v", err)
	}
}

func TestLoadConfig_GeminiSelectsGeminiClient(t *testing.T) {
	chdirTemp(t)
	resetProviderFlags(t)
	t.Setenv("GEMINI_API_KEY", "test-key")
	writeMinimalConfig(t, `[llm]
provider = "gemini"
model = "gemini-2.0-flash"

[wiki]
wiki_dir = ".llmwiki/wiki"
raw_dir = ".llmwiki/raw"
db_path = ".llmwiki/wiki.db"
`)
	if err := loadConfig(); err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	got := reflect.TypeOf(llmClient).String()
	if got != "*llm.GeminiClient" {
		t.Errorf("llmClient type = %s, want *llm.GeminiClient", got)
	}
}

func TestLoadConfig_GeminiMissingKeyReturnsUserError(t *testing.T) {
	chdirTemp(t)
	resetProviderFlags(t)
	// Explicitly clear in case the developer's shell exports it.
	t.Setenv("GEMINI_API_KEY", "")
	writeMinimalConfig(t, `[llm]
provider = "gemini"
model = "gemini-2.0-flash"

[wiki]
wiki_dir = ".llmwiki/wiki"
raw_dir = ".llmwiki/raw"
db_path = ".llmwiki/wiki.db"
`)
	err := loadConfig()
	if err == nil {
		t.Fatal("expected UserError for missing GEMINI_API_KEY")
	}
	var ue *cliutil.UserError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *cliutil.UserError, got %T: %v", err, err)
	}
	rendered := cliutil.Render(err)
	if !strings.Contains(rendered, "https://aistudio.google.com/apikey") {
		t.Errorf("rendered error missing AI Studio URL:\n%s", rendered)
	}
}

func TestLoadConfig_OpenAICompatHonoursAPIKeyEnvOverride(t *testing.T) {
	chdirTemp(t)
	resetProviderFlags(t)
	t.Setenv("MY_CUSTOM_KEY", "abc")
	writeMinimalConfig(t, `[llm]
provider = "openai-compatible"
model = "meta-llama-3.1-8b-instruct:free"

[wiki]
wiki_dir = ".llmwiki/wiki"
raw_dir = ".llmwiki/raw"
db_path = ".llmwiki/wiki.db"

[providers.openai_compat]
base_url = "https://openrouter.ai/api/v1"
api_key_env = "MY_CUSTOM_KEY"
`)
	if err := loadConfig(); err != nil {
		t.Fatalf("loadConfig with custom env set: %v", err)
	}
	got := reflect.TypeOf(llmClient).String()
	if got != "*llm.OpenAICompatClient" {
		t.Errorf("llmClient type = %s, want *llm.OpenAICompatClient", got)
	}

	// Now unset the custom env var; expect a UserError naming MY_CUSTOM_KEY.
	t.Setenv("MY_CUSTOM_KEY", "")
	err := loadConfig()
	if err == nil {
		t.Fatal("expected UserError when MY_CUSTOM_KEY is unset")
	}
	var ue *cliutil.UserError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *cliutil.UserError, got %T: %v", err, err)
	}
	if !strings.Contains(cliutil.Render(err), "MY_CUSTOM_KEY") {
		t.Errorf("rendered error should name MY_CUSTOM_KEY:\n%s", cliutil.Render(err))
	}
}

func TestLoadConfig_FlagOverridesHonourBoth(t *testing.T) {
	chdirTemp(t)
	resetProviderFlags(t)
	t.Setenv("GEMINI_API_KEY", "test-key")
	writeMinimalConfig(t, `[llm]
provider = "anthropic"
model = "claude-haiku-4-5"

[wiki]
wiki_dir = ".llmwiki/wiki"
raw_dir = ".llmwiki/raw"
db_path = ".llmwiki/wiki.db"
`)
	overrideProvider = "gemini"
	overrideModel = "gemini-2.5-pro"
	if err := loadConfig(); err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if got := reflect.TypeOf(llmClient).String(); got != "*llm.GeminiClient" {
		t.Errorf("llmClient type = %s, want *llm.GeminiClient", got)
	}
	if cfg.LLM.Model != "gemini-2.5-pro" {
		t.Errorf("cfg.LLM.Model = %q, want gemini-2.5-pro", cfg.LLM.Model)
	}
}

func TestLoadConfig_ApplyProviderDefaultsFillsMissingModel(t *testing.T) {
	chdirTemp(t)
	resetProviderFlags(t)
	t.Setenv("GEMINI_API_KEY", "test-key")
	writeMinimalConfig(t, `[llm]
provider = "gemini"

[wiki]
wiki_dir = ".llmwiki/wiki"
raw_dir = ".llmwiki/raw"
db_path = ".llmwiki/wiki.db"
`)
	if err := loadConfig(); err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.LLM.Model != "gemini-2.5-flash" {
		t.Errorf("cfg.LLM.Model = %q, want default gemini-2.5-flash", cfg.LLM.Model)
	}
}

// TestApplyProviderDefaultsFillsZeroValues mirrors the IngestConfig defaults
// test pattern: a zero-valued ProvidersConfig should pick up the same defaults
// the v1.1 init template would have written. This is the contract that lets
// pre-v1.1 wikis without a [providers] block keep running unmodified.
func TestApplyProviderDefaultsFillsZeroValues(t *testing.T) {
	c := &Config{}
	applyProviderDefaults(c)
	if c.Providers.Gemini.DefaultModel != "gemini-2.5-flash" {
		t.Errorf("Gemini default = %q", c.Providers.Gemini.DefaultModel)
	}
	if c.Providers.Anthropic.DefaultModel != "claude-haiku-4-5" {
		t.Errorf("Anthropic default = %q", c.Providers.Anthropic.DefaultModel)
	}
	if c.Providers.Ollama.DefaultModel != "llama3.2" {
		t.Errorf("Ollama default model = %q", c.Providers.Ollama.DefaultModel)
	}
	if c.Providers.Ollama.URL != "http://localhost:11434" {
		t.Errorf("Ollama URL default = %q", c.Providers.Ollama.URL)
	}
	if c.Providers.OpenAICompat.APIKeyEnv != "OPENAI_COMPAT_API_KEY" {
		t.Errorf("OpenAICompat APIKeyEnv default = %q", c.Providers.OpenAICompat.APIKeyEnv)
	}
}

// resetInitSchemaFlags zeroes the init subcommand's schema-related flags
// between tests — cobra retains flag state across Set calls in a single
// process, so a previous test's --rewrite-schema or --schema-file value
// would leak into the next runInit otherwise.
func resetInitSchemaFlags(t *testing.T) {
	t.Helper()
	if err := initCmd.Flags().Set("rewrite-schema", "false"); err != nil {
		t.Fatalf("resetting --rewrite-schema: %v", err)
	}
	if err := initCmd.Flags().Set("schema-file", "AGENTS.md"); err != nil {
		t.Fatalf("resetting --schema-file: %v", err)
	}
}

// TestInit_WritesAGENTSMdAtWikiRoot — fresh tmp dir; runInit; assert
// AGENTS.md at the wiki root has the bundled DefaultDoc bytes verbatim.
// This is the trust property: a fresh v0.7 init reproduces v0.6 behaviour
// because the bundled doc is the schema users would write themselves.
func TestInit_WritesAGENTSMdAtWikiRoot(t *testing.T) {
	dir := chdirTemp(t)
	resetInitSchemaFlags(t)
	tolerateUserError(t, runInitWithProvider(t, ""))
	got, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if err != nil {
		t.Fatalf("reading AGENTS.md: %v", err)
	}
	if !bytes.Equal(got, schema.DefaultDoc) {
		t.Errorf("AGENTS.md bytes != schema.DefaultDoc (len got=%d want=%d)", len(got), len(schema.DefaultDoc))
	}
}

// TestInit_LeavesExistingAGENTSMdAlone — when AGENTS.md already exists,
// init must not touch it. The user may have edited the bundled default
// (or written something entirely custom); a re-run for provider-key
// fixes shouldn't clobber that work.
func TestInit_LeavesExistingAGENTSMdAlone(t *testing.T) {
	dir := chdirTemp(t)
	resetInitSchemaFlags(t)
	custom := []byte("# Custom schema\n\nThis is not the bundled default.\n")
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), custom, 0644); err != nil {
		t.Fatalf("writing custom AGENTS.md: %v", err)
	}
	tolerateUserError(t, runInitWithProvider(t, ""))
	got, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if err != nil {
		t.Fatalf("reading AGENTS.md: %v", err)
	}
	if !bytes.Equal(got, custom) {
		t.Errorf("AGENTS.md was modified — pre=%q post=%q", custom, got)
	}
}

// TestInit_LeavesExistingCLAUDEMdAlone — idempotency respects the full
// discovery list. If CLAUDE.md is the only schema doc on disk, init
// must NOT create AGENTS.md (which would yield two competing schema
// docs and trigger Phase A's "different bytes" error on next load).
func TestInit_LeavesExistingCLAUDEMdAlone(t *testing.T) {
	dir := chdirTemp(t)
	resetInitSchemaFlags(t)
	custom := []byte("# Custom CLAUDE.md schema\n")
	if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), custom, 0644); err != nil {
		t.Fatalf("writing custom CLAUDE.md: %v", err)
	}
	tolerateUserError(t, runInitWithProvider(t, ""))
	got, err := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	if err != nil {
		t.Fatalf("reading CLAUDE.md: %v", err)
	}
	if !bytes.Equal(got, custom) {
		t.Errorf("CLAUDE.md was modified — pre=%q post=%q", custom, got)
	}
	if _, err := os.Stat(filepath.Join(dir, "AGENTS.md")); err == nil {
		t.Errorf("AGENTS.md was created even though CLAUDE.md already existed")
	} else if !os.IsNotExist(err) {
		t.Errorf("unexpected stat error on AGENTS.md: %v", err)
	}
}

// TestInit_RewriteSchemaFlag_OverwritesAGENTSMd — --rewrite-schema is the
// explicit opt-in for "give me the bundled v0.7 default back." A custom
// AGENTS.md must be replaced with schema.DefaultDoc when the flag is set.
func TestInit_RewriteSchemaFlag_OverwritesAGENTSMd(t *testing.T) {
	dir := chdirTemp(t)
	resetInitSchemaFlags(t)
	custom := []byte("# Stale custom schema\n")
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), custom, 0644); err != nil {
		t.Fatalf("writing custom AGENTS.md: %v", err)
	}
	tolerateUserError(t, runInitWithFlags(t, map[string]string{
		"rewrite-schema": "true",
	}))
	got, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if err != nil {
		t.Fatalf("reading AGENTS.md: %v", err)
	}
	if !bytes.Equal(got, schema.DefaultDoc) {
		t.Errorf("--rewrite-schema did not restore bundled default (got len=%d, want %d)", len(got), len(schema.DefaultDoc))
	}
}

// TestInit_RewriteSchema_WithSchemaFileCLAUDE_WritesCLAUDEMd — a
// Claude-Code-only operator can opt out of the multi-vendor AGENTS.md
// name and write CLAUDE.md instead. Fresh tmp dir, both flags set:
// CLAUDE.md gets the bundled bytes, AGENTS.md is NOT created.
func TestInit_RewriteSchema_WithSchemaFileCLAUDE_WritesCLAUDEMd(t *testing.T) {
	dir := chdirTemp(t)
	resetInitSchemaFlags(t)
	tolerateUserError(t, runInitWithFlags(t, map[string]string{
		"rewrite-schema": "true",
		"schema-file":    "CLAUDE.md",
	}))
	got, err := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	if err != nil {
		t.Fatalf("reading CLAUDE.md: %v", err)
	}
	if !bytes.Equal(got, schema.DefaultDoc) {
		t.Errorf("CLAUDE.md bytes != schema.DefaultDoc (len got=%d want=%d)", len(got), len(schema.DefaultDoc))
	}
	if _, err := os.Stat(filepath.Join(dir, "AGENTS.md")); err == nil {
		t.Errorf("AGENTS.md was created even though --schema-file=CLAUDE.md")
	} else if !os.IsNotExist(err) {
		t.Errorf("unexpected stat error on AGENTS.md: %v", err)
	}
}

// TestInit_OutputMentionsSchemaFile_OnFirstWrite — first init in a fresh
// dir must announce the schema-doc write so the user can find the file
// they're invited to edit. The two-line format mirrors the existing
// "Initialized wiki at .llmwiki" line.
func TestInit_OutputMentionsSchemaFile_OnFirstWrite(t *testing.T) {
	chdirTemp(t)
	resetInitSchemaFlags(t)
	out, err := captureInitStdout(t, func() error {
		return runInitWithProvider(t, "")
	})
	tolerateUserError(t, err)
	if !strings.Contains(out, "Wrote default schema at AGENTS.md") {
		t.Errorf("stdout missing schema-write line:\n%s", out)
	}
	if !strings.Contains(out, "(defines page shape and prompts; edit to fit your domain)") {
		t.Errorf("stdout missing schema-write helper line:\n%s", out)
	}
}

// TestInit_OutputDoesNotMentionSchemaFile_OnIdempotentRun — a second init
// in the same dir (AGENTS.md already on disk) must NOT emit the
// schema-write line. Saying "Wrote default schema" when we didn't write
// it would be a lie and would also confuse users who run init twice.
func TestInit_OutputDoesNotMentionSchemaFile_OnIdempotentRun(t *testing.T) {
	chdirTemp(t)
	resetInitSchemaFlags(t)
	// First init seeds AGENTS.md.
	tolerateUserError(t, runInitWithProvider(t, ""))
	// Second init should be silent on the schema-write front.
	out, err := captureInitStdout(t, func() error {
		return runInitWithProvider(t, "")
	})
	tolerateUserError(t, err)
	if strings.Contains(out, "Wrote default schema") {
		t.Errorf("second init emitted schema-write line on idempotent re-run:\n%s", out)
	}
}

// TestInit_SchemaFileFlag_RejectsUnknownValue — only AGENTS.md and
// CLAUDE.md are valid; anything else is a user error with a message
// listing the allowed names. We assert against schema.SchemaFilenames
// so this test stays in lockstep with the discovery list.
func TestInit_SchemaFileFlag_RejectsUnknownValue(t *testing.T) {
	chdirTemp(t)
	resetInitSchemaFlags(t)
	err := runInitWithFlags(t, map[string]string{
		"schema-file": "README.md",
	})
	if err == nil {
		t.Fatal("expected error for --schema-file=README.md")
	}
	var ue *cliutil.UserError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *cliutil.UserError, got %T: %v", err, err)
	}
	rendered := cliutil.Render(err)
	for _, name := range schema.SchemaFilenames {
		if !strings.Contains(rendered, name) {
			t.Errorf("rendered error missing %q in:\n%s", name, rendered)
		}
	}
}
