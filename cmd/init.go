package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mattn/go-isatty"
	"github.com/mritunjaysharma394/llmwiki/internal/cliutil"
	"github.com/mritunjaysharma394/llmwiki/internal/schema"
	"github.com/spf13/cobra"
)

// defaultConfigGeminiToml is the default template (sub-project 5): Gemini's
// free tier with no credit card required is the lowest-friction first run.
const defaultConfigGeminiToml = `[llm]
provider = "gemini"
model = "gemini-2.5-flash"
ollama_url = "http://localhost:11434"

[wiki]
wiki_dir = ".llmwiki/wiki"
raw_dir  = ".llmwiki/raw"
db_path  = ".llmwiki/wiki.db"

[ask]
auto_save = true

[ingest]
max_file_bytes = 262144
chunk_size_bytes = 16384
http_timeout_seconds = 30
http_max_bytes = 5242880
pdf_min_text_per_page = 50
extra_text_extensions = []
extra_skip_globs = []
respect_gitignore = true
# v0.8: cross-page page-update pass is on by default (Karpathy's "modify
# 10-15 relevant pages in one pass" shape). Validator still drops bad
# proposals; set to false to disable per-ingest cross-page updates.
update_existing = true
feed_request_per_second = 1.0
feed_max_entries = 50
sitemap_max_pages = 200

[watch]
# v0.8: 'llmwiki watch <dir>' runs as a long-lived daemon, fsnotifying
# the listed directories and ingesting new/changed files via a SQLite-
# backed crash-resumable queue. Empty 'dirs' means "no auto-watch on
# bare invocation"; pass <dir> as an arg or fill this list to opt in.
# Debounce coalesces rapid writes (editors saving in chunks); max_attempts
# caps retries with 5s/30s/5min exponential backoff before status='failed'.
dirs = []
debounce_seconds = 2
max_attempts = 3

[providers.gemini]
default_model = "gemini-2.5-flash"

[providers.anthropic]
default_model = "claude-haiku-4-5"

[providers.ollama]
default_model = "llama3.2"
url = "http://localhost:11434"

[providers.openai_compat]
# base_url options:
#   https://openrouter.ai/api/v1
#   https://api.groq.com/openai/v1
#   https://api.together.xyz/v1
#   https://api.cerebras.ai/v1
#   https://api.mistral.ai/v1
base_url = ""
api_key_env = "OPENAI_COMPAT_API_KEY"
default_model = ""
`

const defaultConfigToml = `[llm]
provider = "anthropic"
model = "claude-haiku-4-5"
ollama_url = "http://localhost:11434"

[wiki]
wiki_dir = ".llmwiki/wiki"
raw_dir  = ".llmwiki/raw"
db_path  = ".llmwiki/wiki.db"

[ask]
auto_save = true

[ingest]
max_file_bytes = 262144
chunk_size_bytes = 16384
http_timeout_seconds = 30
http_max_bytes = 5242880
pdf_min_text_per_page = 50
extra_text_extensions = []
extra_skip_globs = []
respect_gitignore = true
# v0.8: cross-page page-update pass is on by default (Karpathy's "modify
# 10-15 relevant pages in one pass" shape). Validator still drops bad
# proposals; set to false to disable per-ingest cross-page updates.
update_existing = true
feed_request_per_second = 1.0
feed_max_entries = 50
sitemap_max_pages = 200

[watch]
# v0.8: 'llmwiki watch <dir>' runs as a long-lived daemon, fsnotifying
# the listed directories and ingesting new/changed files via a SQLite-
# backed crash-resumable queue. Empty 'dirs' means "no auto-watch on
# bare invocation"; pass <dir> as an arg or fill this list to opt in.
# Debounce coalesces rapid writes (editors saving in chunks); max_attempts
# caps retries with 5s/30s/5min exponential backoff before status='failed'.
dirs = []
debounce_seconds = 2
max_attempts = 3
`

const defaultConfigOllamaToml = `[llm]
provider = "ollama"
model = "llama3.2"
ollama_url = "http://localhost:11434"

[wiki]
wiki_dir = ".llmwiki/wiki"
raw_dir  = ".llmwiki/raw"
db_path  = ".llmwiki/wiki.db"

[ask]
auto_save = true

[ingest]
max_file_bytes = 262144
chunk_size_bytes = 16384
http_timeout_seconds = 30
http_max_bytes = 5242880
pdf_min_text_per_page = 50
extra_text_extensions = []
extra_skip_globs = []
respect_gitignore = true
# v0.8: cross-page page-update pass is on by default (Karpathy's "modify
# 10-15 relevant pages in one pass" shape). Validator still drops bad
# proposals; set to false to disable per-ingest cross-page updates.
update_existing = true
feed_request_per_second = 1.0
feed_max_entries = 50
sitemap_max_pages = 200

[watch]
# v0.8: 'llmwiki watch <dir>' runs as a long-lived daemon, fsnotifying
# the listed directories and ingesting new/changed files via a SQLite-
# backed crash-resumable queue. Empty 'dirs' means "no auto-watch on
# bare invocation"; pass <dir> as an arg or fill this list to opt in.
# Debounce coalesces rapid writes (editors saving in chunks); max_attempts
# caps retries with 5s/30s/5min exponential backoff before status='failed'.
dirs = []
debounce_seconds = 2
max_attempts = 3
`

// defaultConfigOpenAICompatToml seeds the openai-compatible provider with a
// commented hint listing the five supported endpoints; the operator picks one
// and exports the matching API key in the env var named by api_key_env.
const defaultConfigOpenAICompatToml = `[llm]
provider = "openai-compatible"
model = ""
ollama_url = "http://localhost:11434"

[wiki]
wiki_dir = ".llmwiki/wiki"
raw_dir  = ".llmwiki/raw"
db_path  = ".llmwiki/wiki.db"

[ask]
auto_save = true

[ingest]
max_file_bytes = 262144
chunk_size_bytes = 16384
http_timeout_seconds = 30
http_max_bytes = 5242880
pdf_min_text_per_page = 50
extra_text_extensions = []
extra_skip_globs = []
respect_gitignore = true
# v0.8: cross-page page-update pass is on by default (Karpathy's "modify
# 10-15 relevant pages in one pass" shape). Validator still drops bad
# proposals; set to false to disable per-ingest cross-page updates.
update_existing = true
feed_request_per_second = 1.0
feed_max_entries = 50
sitemap_max_pages = 200

[watch]
# v0.8: 'llmwiki watch <dir>' runs as a long-lived daemon, fsnotifying
# the listed directories and ingesting new/changed files via a SQLite-
# backed crash-resumable queue. Empty 'dirs' means "no auto-watch on
# bare invocation"; pass <dir> as an arg or fill this list to opt in.
# Debounce coalesces rapid writes (editors saving in chunks); max_attempts
# caps retries with 5s/30s/5min exponential backoff before status='failed'.
dirs = []
debounce_seconds = 2
max_attempts = 3

[providers.openai_compat]
# base_url options:
#   https://openrouter.ai/api/v1   (model = "meta-llama-3.1-8b-instruct:free")
#   https://api.groq.com/openai/v1 (model = "llama-3.3-70b-versatile")
#   https://api.together.xyz/v1
#   https://api.cerebras.ai/v1
#   https://api.mistral.ai/v1
base_url = ""
api_key_env = "OPENAI_COMPAT_API_KEY"
default_model = ""
`

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize a new wiki in the current directory",
	RunE:  runInit,
}

func init() {
	initCmd.Flags().String("provider", "", "default LLM provider: gemini|anthropic|openai-compatible|ollama (empty = gemini)")
	initCmd.Flags().Bool("rewrite-schema", false, "Overwrite an existing schema doc with the bundled default. By default `init` leaves an existing AGENTS.md/CLAUDE.md alone.")
	initCmd.Flags().String("schema-file", "AGENTS.md", "Filename to write the bundled schema to: AGENTS.md (default, multi-vendor) or CLAUDE.md (Claude-Code-only).")
}

// templateForProvider picks the config template body for a given --provider
// value. Empty/unknown falls through to the Gemini default — that's the new
// recommended free-tier first run.
func templateForProvider(provider string) string {
	switch provider {
	case "anthropic":
		return defaultConfigToml
	case "ollama":
		return defaultConfigOllamaToml
	case "openai-compatible":
		return defaultConfigOpenAICompatToml
	default:
		return defaultConfigGeminiToml
	}
}

func runInit(cmd *cobra.Command, args []string) error {
	provider, _ := cmd.Flags().GetString("provider")

	// On a TTY with no explicit --provider, surface the recommendation copy.
	// Non-TTY (CI, piped invocation) silently writes the gemini template —
	// that's the same idempotency we had pre-v1.1 for the anthropic default.
	if provider == "" && isatty.IsTerminal(os.Stdin.Fd()) {
		fmt.Println("Recommended: Gemini (free tier, 1M context, no credit card required)")
		fmt.Println("  Get a key at https://aistudio.google.com/apikey, then:")
		fmt.Println("    export GEMINI_API_KEY=...")
		fmt.Println("  Other options: anthropic | openai-compatible | ollama")
	}

	dir := ".llmwiki"
	for _, sub := range []string{"", "wiki", "raw", "answers"} {
		p := filepath.Join(dir, sub)
		if err := os.MkdirAll(p, 0755); err != nil {
			return fmt.Errorf("mkdir %s: %w", p, err)
		}
	}

	cfgPath := filepath.Join(dir, "config.toml")
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		content := templateForProvider(provider)
		if err := os.WriteFile(cfgPath, []byte(content), 0644); err != nil {
			return fmt.Errorf("writing config: %w", err)
		}
	}
	fmt.Printf("Initialized wiki at %s\n", dir)

	// Write the schema doc at the wiki root (NOT inside .llmwiki/, per Q1 —
	// AGENTS.md is the multi-vendor convention, discoverable on `ls`). The
	// idempotency contract: an existing schema doc (under any name in the
	// discovery list — AGENTS.md or CLAUDE.md) is left alone so a re-run for
	// provider-key fixes doesn't clobber user edits. --rewrite-schema is the
	// explicit opt-in for "give me the bundled v0.7 default back."
	rewriteSchema, _ := cmd.Flags().GetBool("rewrite-schema")
	schemaFile, _ := cmd.Flags().GetString("schema-file")
	if schemaFile == "" {
		schemaFile = "AGENTS.md"
	}
	allowed := false
	for _, name := range schema.SchemaFilenames {
		if schemaFile == name {
			allowed = true
			break
		}
	}
	if !allowed {
		return cliutil.Wrap(
			fmt.Sprintf("--schema-file=%q is not a recognised schema filename", schemaFile),
			nil,
			fmt.Sprintf("use one of: %s", strings.Join(schema.SchemaFilenames, ", ")),
		)
	}
	schemaExists := false
	for _, name := range schema.SchemaFilenames {
		if _, err := os.Stat(name); err == nil {
			schemaExists = true
			break
		}
	}
	if !schemaExists || rewriteSchema {
		if err := os.WriteFile(schemaFile, schema.DefaultDoc, 0644); err != nil {
			return fmt.Errorf("writing %s: %w", schemaFile, err)
		}
		fmt.Printf("Wrote default schema at %s\n", schemaFile)
		fmt.Println("  (defines page shape and prompts; edit to fit your domain)")
	}

	// Surface a missing-key UserError matching the chosen provider so the very
	// first run produces a copy-pasteable next step. The empty/default case is
	// gemini per the v1.1 recommendation.
	switch provider {
	case "anthropic":
		if os.Getenv("ANTHROPIC_API_KEY") == "" {
			return cliutil.Wrap(
				"ANTHROPIC_API_KEY is not set",
				nil,
				"export ANTHROPIC_API_KEY=sk-ant-... (get one at https://console.anthropic.com/settings/keys), or use --provider gemini / ollama",
			)
		}
	case "openai-compatible":
		if os.Getenv("OPENAI_COMPAT_API_KEY") == "" {
			return cliutil.Wrap(
				"OPENAI_COMPAT_API_KEY is not set",
				nil,
				"set the env var named in [providers.openai_compat].api_key_env (default OPENAI_COMPAT_API_KEY) and edit base_url in .llmwiki/config.toml",
			)
		}
	case "ollama":
		// nothing to verify pre-flight; the daemon check happens at first call
	default: // "" or "gemini"
		if os.Getenv("GEMINI_API_KEY") == "" {
			return cliutil.Wrap(
				"GEMINI_API_KEY is not set",
				nil,
				"get a free key at https://aistudio.google.com/apikey, then export GEMINI_API_KEY=...; or use --provider anthropic / openai-compatible / ollama",
			)
		}
	}
	return nil
}
