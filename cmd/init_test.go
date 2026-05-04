package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/mritunjaysharma394/llmwiki/internal/cliutil"
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
