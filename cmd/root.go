package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/fatih/color"
	"github.com/mritunjaysharma394/llmwiki/internal/cliutil"
	"github.com/mritunjaysharma394/llmwiki/internal/db"
	"github.com/mritunjaysharma394/llmwiki/internal/llm"
	"github.com/mritunjaysharma394/llmwiki/internal/schema"
	"github.com/mritunjaysharma394/llmwiki/internal/version"
	"github.com/spf13/cobra"
)

type LLMConfig struct {
	Provider  string `toml:"provider"`
	Model     string `toml:"model"`
	OllamaURL string `toml:"ollama_url"`
}

type WikiConfig struct {
	WikiDir string `toml:"wiki_dir"`
	RawDir  string `toml:"raw_dir"`
	DBPath  string `toml:"db_path"`
}

type AskConfig struct {
	AutoSave *bool `toml:"auto_save"`

	// Sub-project 8 Phase B — auto-promote keys. Default ON per plan §2.
	// Pointer pattern matches RespectGitignore / UpdateExisting so an
	// absent key reads as "use default", a present key as the explicit
	// value (TOML zero-value `false` would otherwise be ambiguous with
	// "missing").
	AutoPromote                *bool    `toml:"auto_promote"`
	AutoPromoteScoreFloor      int      `toml:"auto_promote_score_floor"`
	AutoPromoteHedgingPhrases  []string `toml:"auto_promote_hedging_phrases"`
	AutoPromoteSkipScore       float64  `toml:"auto_promote_skip_score"`
}

// AutoPromoteOrDefault returns the configured value, defaulting to true
// when the config left it unset. Plan §2: auto-promote is default-on,
// gated by the four-signal heuristic + the trust validator.
func (c AskConfig) AutoPromoteOrDefault() bool {
	if c.AutoPromote == nil {
		return true
	}
	return *c.AutoPromote
}

// IngestConfig controls ingest defaults that callers can override via flags.
// Defaults are applied silently in loadConfig when fields are zero-valued, so
// pre-sub-project-3 wikis without a [ingest] block keep working.
type IngestConfig struct {
	MaxFileBytes        int64    `toml:"max_file_bytes"`
	ChunkSizeBytes      int      `toml:"chunk_size_bytes"`
	HTTPTimeoutSeconds  int      `toml:"http_timeout_seconds"`
	HTTPMaxBytes        int64    `toml:"http_max_bytes"`
	PDFMinTextPerPage   int      `toml:"pdf_min_text_per_page"`
	ExtraTextExtensions []string `toml:"extra_text_extensions"`
	ExtraSkipGlobs      []string `toml:"extra_skip_globs"`
	// RespectGitignore is a *bool so we can disambiguate "missing from config"
	// (-> default true) from "explicitly set to false". TOML's zero value for
	// bool is false, which is the wrong default for an absent block.
	RespectGitignore *bool `toml:"respect_gitignore"`
	// Sub-project 4 launch surface: feed/sitemap crawl tunables. Pre-v4
	// configs without these keys decode to zero and pick up the defaults
	// silently via applyIngestDefaults.
	FeedRequestsPerSecond float64 `toml:"feed_request_per_second"`
	FeedMaxEntries        int     `toml:"feed_max_entries"`
	SitemapMaxPages       int     `toml:"sitemap_max_pages"`

	// Sub-project 6b — pillar 3 — cross-page page-update pass (v0.6).
	// UpdateExisting is *bool to disambiguate "absent" (-> default
	// false, Q11) from "explicitly false" — same shape RespectGitignore
	// uses. Three tunables for per-source / global candidate caps and
	// the quote floor (Q5, Q7). Default off everywhere; users opt in.
	UpdateExisting                       *bool `toml:"update_existing"`
	UpdateExistingMaxCandidatesPerSource int   `toml:"update_existing_max_candidates_per_source"`
	UpdateExistingMaxCandidatesTotal     int   `toml:"update_existing_max_candidates_total"`
	UpdateExistingQuoteFloor             int   `toml:"update_existing_quote_floor"`
}

// ProvidersConfig groups per-provider knobs that don't belong on the catch-all
// LLMConfig. Pre-v1.1 configs without a [providers] block decode into a
// zero-valued ProvidersConfig; applyProviderDefaults fills the blanks silently
// the same way applyIngestDefaults does for [ingest].
type ProvidersConfig struct {
	OpenAICompat OpenAICompatProviderConfig `toml:"openai_compat"`
	Gemini       GeminiProviderConfig       `toml:"gemini"`
	Anthropic    AnthropicProviderConfig    `toml:"anthropic"`
	Ollama       OllamaProviderConfig       `toml:"ollama"`
}

// OpenAICompatProviderConfig captures the three knobs an operator needs to
// point the OpenAI-compatible client at any of the five supported endpoints
// (OpenRouter, Groq, Together, Cerebras, Mistral La Plateforme).
type OpenAICompatProviderConfig struct {
	BaseURL      string `toml:"base_url"`
	APIKeyEnv    string `toml:"api_key_env"`
	DefaultModel string `toml:"default_model"`
}

type GeminiProviderConfig struct {
	DefaultModel string `toml:"default_model"`
}

type AnthropicProviderConfig struct {
	DefaultModel string `toml:"default_model"`
}

type OllamaProviderConfig struct {
	DefaultModel string `toml:"default_model"`
	URL          string `toml:"url"`
}

type Config struct {
	LLM       LLMConfig       `toml:"llm"`
	Wiki      WikiConfig      `toml:"wiki"`
	Ask       AskConfig       `toml:"ask"`
	Ingest    IngestConfig    `toml:"ingest"`
	Providers ProvidersConfig `toml:"providers"`
}

// RespectGitignoreOrDefault returns the configured value, defaulting to true
// when the config left it unset.
func (c IngestConfig) RespectGitignoreOrDefault() bool {
	if c.RespectGitignore == nil {
		return true
	}
	return *c.RespectGitignore
}

// UpdateExistingOrDefault returns the configured value, defaulting to false
// when the config left it unset. Mirrors RespectGitignoreOrDefault but
// flips the default polarity: the cross-page page-update pass is opt-in
// (Q11), so an absent [ingest] update_existing key reads as off.
func (c IngestConfig) UpdateExistingOrDefault() bool {
	if c.UpdateExisting == nil {
		return false
	}
	return *c.UpdateExisting
}

var (
	cfg       *Config
	llmClient llm.Client
	database  *db.DB
	// activeSchema is the user-editable schema doc loaded by loadConfig
	// from AGENTS.md (canonical) or CLAUDE.md (Claude-Code-native) at
	// the wiki root. When neither file exists, schema.Load returns
	// schema.Bundled() so pre-v0.7 wikis see zero behaviour change.
	// Threaded into every wiki entrypoint (ingest, ask, promote, lint)
	// to drive prompt rendering and ontology shape.
	activeSchema     schema.Schema
	overrideProvider string
	overrideModel    string
)

var rootCmd = &cobra.Command{
	Use:     "llmwiki",
	Short:   "LLM-powered personal wiki",
	Version: version.Format(),
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		switch cmd.Name() {
		case "init", "help", "version":
			return nil
		}
		// LLMWIKI_DIR lets MCP clients (and `llmwiki mcp` users in
		// general) point the server at a wiki living anywhere on disk
		// without spawning the process from inside that directory. We
		// chdir before loadConfig so the relative .llmwiki/config.toml
		// lookup loadConfig does still works. Empty / unset -> no-op.
		if dir := os.Getenv("LLMWIKI_DIR"); dir != "" {
			if err := os.Chdir(dir); err != nil {
				return fmt.Errorf("LLMWIKI_DIR=%q: %w", dir, err)
			}
		}
		// `llmwiki schema show / validate / migrate` is the diagnostic
		// surface for a broken AGENTS.md / CLAUDE.md. If we ran the
		// strict loadConfig path here, a user with a malformed schema
		// could not reach `schema validate` to find out *why* it was
		// malformed. Take the soft path: load activeSchema via
		// schema.Load (no Validate call), so the subcommand can render
		// the structured error itself.
		//
		// `schema migrate` is the one schema subcommand that DOES need
		// a real DB + LLM client (it walks pages and re-runs ingest);
		// for that variant we load the full config but tolerate a
		// schema that fails the strict Validate check by routing
		// through loadSchemaSoft anyway — the migrate path can run
		// against a parse-clean but Validate-failing schema if the user
		// edited AGENTS.md to a state the validator dislikes but the
		// parser still accepts.
		if cmd.Parent() != nil && cmd.Parent().Name() == "schema" {
			if cmd.Name() == "migrate" {
				return loadSchemaSoftWithDB()
			}
			return loadSchemaSoft()
		}
		return loadConfig()
	},
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		out := cliutil.Render(err)
		if out != "" {
			color.New(color.FgRed, color.Bold).Fprint(os.Stderr, "Error:")
			// cliutil.Render already starts with "Error:"; trim our colored prefix
			// and print the rest. Rendered already includes the cause/try lines.
			rest := strings.TrimPrefix(out, "Error:")
			fmt.Fprintln(os.Stderr, rest)
		}
		os.Exit(1)
	}
}

// loadSchemaSoft populates activeSchema for the `schema` subcommands
// without running Validate or opening the database. It exists so a user
// with a malformed AGENTS.md / CLAUDE.md can still reach `llmwiki schema
// validate` to diagnose the problem — the strict loadConfig path would
// have bounced them out before the subcommand body ran. Parse errors
// (which are the structural-level "this isn't a schema doc at all"
// failures, not the Validate-level "missing required placeholder"
// failures) DO bubble up here, since `schema validate` cannot run
// against a Schema that didn't parse.
func loadSchemaSoft() error {
	sch, err := schema.Load(".")
	if err != nil {
		return cliutil.Wrap(
			"loading schema doc (AGENTS.md or CLAUDE.md)",
			err,
			"the file is structurally malformed (frontmatter / section split). Fix the listed problem and re-run.")
	}
	activeSchema = sch
	return nil
}

// loadSchemaSoftWithDB is the `schema migrate` flavour of
// loadSchemaSoft: it parses AGENTS.md / CLAUDE.md without running the
// strict Validate check (so a user with a Validate-failing-but-parse-
// clean schema can still re-run migrate against the prior bytes), AND
// opens the database + selects the LLM client the migrate path needs.
//
// Mirrors loadConfig's database/provider selection block. Kept as a
// sibling helper so the show / validate variants stay zero-DB and the
// dispatch in PersistentPreRunE remains a single switch on cmd.Name().
func loadSchemaSoftWithDB() error {
	if err := loadSchemaSoft(); err != nil {
		return err
	}
	cfg = &Config{}
	if _, err := toml.DecodeFile(".llmwiki/config.toml", cfg); err != nil {
		return fmt.Errorf("config not found — run 'llmwiki init' first: %w", err)
	}
	if overrideProvider != "" {
		cfg.LLM.Provider = overrideProvider
	}
	if overrideModel != "" {
		cfg.LLM.Model = overrideModel
	}
	applyProviderDefaults(cfg)
	if cfg.LLM.OllamaURL == "" {
		cfg.LLM.OllamaURL = cfg.Providers.Ollama.URL
	}
	applyIngestDefaults(&cfg.Ingest)
	applyAskDefaults(&cfg.Ask)
	var err error
	database, err = db.Open(cfg.Wiki.DBPath)
	if err != nil {
		return cliutil.Wrap("opening database",
			err,
			"if the schema is newer than this binary, downgrade is not supported; back up .llmwiki/wiki.db and re-init")
	}
	switch cfg.LLM.Provider {
	case "gemini":
		if os.Getenv("GEMINI_API_KEY") == "" {
			return cliutil.Wrap(
				"GEMINI_API_KEY is not set",
				nil,
				"get a free key at https://aistudio.google.com/apikey, then export GEMINI_API_KEY=...; or use --provider anthropic / openai-compatible / ollama",
			)
		}
		llmClient = llm.NewGeminiClient(cfg.LLM.Model)
	case "anthropic", "":
		if os.Getenv("ANTHROPIC_API_KEY") == "" {
			return cliutil.Wrap(
				"ANTHROPIC_API_KEY is not set",
				nil,
				"export ANTHROPIC_API_KEY=sk-ant-... (get one at https://console.anthropic.com/settings/keys), or use --provider gemini / ollama",
			)
		}
		llmClient = llm.NewAnthropicClient(cfg.LLM.Model)
	case "openai-compatible":
		keyEnv := cfg.Providers.OpenAICompat.APIKeyEnv
		if keyEnv == "" {
			keyEnv = "OPENAI_COMPAT_API_KEY"
		}
		if os.Getenv(keyEnv) == "" {
			return cliutil.Wrap(
				fmt.Sprintf("%s is not set", keyEnv),
				nil,
				"set the env var named in [providers.openai_compat].api_key_env (default OPENAI_COMPAT_API_KEY), or use --provider gemini",
			)
		}
		if cfg.Providers.OpenAICompat.BaseURL == "" {
			return cliutil.Wrap(
				"[providers.openai_compat].base_url is empty",
				nil,
				"set base_url to e.g. https://openrouter.ai/api/v1, https://api.groq.com/openai/v1, https://api.together.xyz/v1, https://api.cerebras.ai/v1, or https://api.mistral.ai/v1",
			)
		}
		llmClient = llm.NewOpenAICompatClient(
			cfg.Providers.OpenAICompat.BaseURL,
			os.Getenv(keyEnv),
			cfg.LLM.Model,
		)
	case "ollama":
		llmClient = llm.NewOllamaClient(cfg.LLM.Model, cfg.LLM.OllamaURL)
	default:
		return fmt.Errorf("unknown provider %q", cfg.LLM.Provider)
	}
	if name := os.Getenv("LLMWIKI_CASSETTE"); name != "" {
		dir := "internal/llm/testdata/cassettes"
		llmClient = llm.NewCassetteClient(llmClient, dir, name, llm.ModeReplay)
	}
	return nil
}

func loadConfig() error {
	cfg = &Config{}
	if _, err := toml.DecodeFile(".llmwiki/config.toml", cfg); err != nil {
		return fmt.Errorf("config not found — run 'llmwiki init' first: %w", err)
	}
	if overrideProvider != "" {
		cfg.LLM.Provider = overrideProvider
	}
	if overrideModel != "" {
		cfg.LLM.Model = overrideModel
	}
	applyProviderDefaults(cfg)
	if cfg.LLM.OllamaURL == "" {
		cfg.LLM.OllamaURL = cfg.Providers.Ollama.URL
	}
	applyIngestDefaults(&cfg.Ingest)
	applyAskDefaults(&cfg.Ask)
	// Load the user-editable schema doc from the wiki root, falling
	// back to schema.Bundled() when neither AGENTS.md nor CLAUDE.md is
	// present (a v0.6 wiki opening under v0.7). Validate immediately —
	// a malformed schema fails at load-time with file:line, not at
	// first ingest, so the user gets the error before the LLM call
	// happens. The chdir into LLMWIKI_DIR has already settled in
	// PersistentPreRunE above, so "." here is the wiki root.
	sch, err := schema.Load(".")
	if err != nil {
		return cliutil.Wrap(
			"loading schema doc (AGENTS.md or CLAUDE.md)",
			err,
			"run `llmwiki schema validate` for the structured error; or `llmwiki init --rewrite-schema` to overwrite with the bundled default")
	}
	if verr := sch.Validate(); verr != nil {
		return cliutil.Wrap(
			"validating schema doc",
			verr,
			"edit the file to fix; run `llmwiki schema validate` to iterate")
	}
	activeSchema = sch
	database, err = db.Open(cfg.Wiki.DBPath)
	if err != nil {
		return cliutil.Wrap("opening database",
			err,
			"if the schema is newer than this binary, downgrade is not supported; back up .llmwiki/wiki.db and re-init")
	}
	switch cfg.LLM.Provider {
	case "gemini":
		if os.Getenv("GEMINI_API_KEY") == "" {
			return cliutil.Wrap(
				"GEMINI_API_KEY is not set",
				nil,
				"get a free key at https://aistudio.google.com/apikey, then export GEMINI_API_KEY=...; or use --provider anthropic / openai-compatible / ollama",
			)
		}
		llmClient = llm.NewGeminiClient(cfg.LLM.Model)
	case "anthropic", "":
		if os.Getenv("ANTHROPIC_API_KEY") == "" {
			return cliutil.Wrap(
				"ANTHROPIC_API_KEY is not set",
				nil,
				"export ANTHROPIC_API_KEY=sk-ant-... (get one at https://console.anthropic.com/settings/keys), or use --provider gemini / ollama",
			)
		}
		llmClient = llm.NewAnthropicClient(cfg.LLM.Model)
	case "openai-compatible":
		keyEnv := cfg.Providers.OpenAICompat.APIKeyEnv
		if keyEnv == "" {
			keyEnv = "OPENAI_COMPAT_API_KEY"
		}
		if os.Getenv(keyEnv) == "" {
			return cliutil.Wrap(
				fmt.Sprintf("%s is not set", keyEnv),
				nil,
				"set the env var named in [providers.openai_compat].api_key_env (default OPENAI_COMPAT_API_KEY), or use --provider gemini",
			)
		}
		if cfg.Providers.OpenAICompat.BaseURL == "" {
			return cliutil.Wrap(
				"[providers.openai_compat].base_url is empty",
				nil,
				"set base_url to e.g. https://openrouter.ai/api/v1, https://api.groq.com/openai/v1, https://api.together.xyz/v1, https://api.cerebras.ai/v1, or https://api.mistral.ai/v1",
			)
		}
		llmClient = llm.NewOpenAICompatClient(
			cfg.Providers.OpenAICompat.BaseURL,
			os.Getenv(keyEnv),
			cfg.LLM.Model,
		)
	case "ollama":
		llmClient = llm.NewOllamaClient(cfg.LLM.Model, cfg.LLM.OllamaURL)
	default:
		return fmt.Errorf("unknown provider %q", cfg.LLM.Provider)
	}
	if name := os.Getenv("LLMWIKI_CASSETTE"); name != "" {
		dir := "internal/llm/testdata/cassettes"
		llmClient = llm.NewCassetteClient(llmClient, dir, name, llm.ModeReplay)
	}
	return nil
}

// applyProviderDefaults fills zero-valued ProvidersConfig fields and resolves
// cfg.LLM.Model from the active provider's default_model when the user left
// model empty after both flag overrides and TOML decoding. Pre-v1.1 configs
// without a [providers] block decode into a zero struct; we silently apply the
// defaults the v1.1 init template would have written, the same pattern
// applyIngestDefaults uses for [ingest].
func applyProviderDefaults(cfg *Config) {
	if cfg.Providers.Gemini.DefaultModel == "" {
		cfg.Providers.Gemini.DefaultModel = "gemini-2.5-flash"
	}
	if cfg.Providers.Anthropic.DefaultModel == "" {
		cfg.Providers.Anthropic.DefaultModel = "claude-haiku-4-5"
	}
	if cfg.Providers.Ollama.DefaultModel == "" {
		cfg.Providers.Ollama.DefaultModel = "llama3.2"
	}
	if cfg.Providers.Ollama.URL == "" {
		cfg.Providers.Ollama.URL = "http://localhost:11434"
	}
	if cfg.Providers.OpenAICompat.APIKeyEnv == "" {
		cfg.Providers.OpenAICompat.APIKeyEnv = "OPENAI_COMPAT_API_KEY"
	}
	// Resolve missing model from the active provider's default. The user's
	// --model flag and the [llm].model field win; default_model only fills the
	// gap. open question 4 in the plan resolves this precedence explicitly.
	if cfg.LLM.Model == "" {
		switch cfg.LLM.Provider {
		case "gemini":
			cfg.LLM.Model = cfg.Providers.Gemini.DefaultModel
		case "anthropic", "":
			cfg.LLM.Model = cfg.Providers.Anthropic.DefaultModel
		case "ollama":
			cfg.LLM.Model = cfg.Providers.Ollama.DefaultModel
		case "openai-compatible":
			cfg.LLM.Model = cfg.Providers.OpenAICompat.DefaultModel
		}
	}
}

// applyIngestDefaults fills zero-valued IngestConfig fields with their default
// values. Pre-v3 configs without a [ingest] block decode into a zero struct; we
// silently apply the same defaults the v3 init template would have written.
func applyIngestDefaults(c *IngestConfig) {
	if c.MaxFileBytes == 0 {
		c.MaxFileBytes = 256 * 1024
	}
	if c.ChunkSizeBytes == 0 {
		c.ChunkSizeBytes = 16 * 1024
	}
	if c.HTTPTimeoutSeconds == 0 {
		c.HTTPTimeoutSeconds = 30
	}
	if c.HTTPMaxBytes == 0 {
		c.HTTPMaxBytes = 5 * 1024 * 1024
	}
	if c.PDFMinTextPerPage == 0 {
		c.PDFMinTextPerPage = 50
	}
	if c.RespectGitignore == nil {
		t := true
		c.RespectGitignore = &t
	}
	if c.FeedRequestsPerSecond == 0 {
		c.FeedRequestsPerSecond = 1.0
	}
	if c.FeedMaxEntries == 0 {
		c.FeedMaxEntries = 50
	}
	if c.SitemapMaxPages == 0 {
		c.SitemapMaxPages = 200
	}
	// Sub-project 6b — pillar 3 — cross-page page-update pass (v0.6).
	// UpdateExisting defaults off (Q11). The three tunables — per-
	// source and global candidate caps (Q5) and the quote floor (Q7) —
	// pick up 20 / 50 / 2 when the config left them at zero.
	if c.UpdateExisting == nil {
		f := false
		c.UpdateExisting = &f
	}
	if c.UpdateExistingMaxCandidatesPerSource == 0 {
		c.UpdateExistingMaxCandidatesPerSource = 20
	}
	if c.UpdateExistingMaxCandidatesTotal == 0 {
		c.UpdateExistingMaxCandidatesTotal = 50
	}
	if c.UpdateExistingQuoteFloor == 0 {
		c.UpdateExistingQuoteFloor = 2
	}
}

// applyAskDefaults fills zero-valued AskConfig fields with their
// sub-project 8 Phase B defaults. Pre-v0.8 configs without an [ask]
// auto_promote_* block decode into zero / nil; we silently apply the
// defaults the v0.8 init template would have written.
//
// Default polarity:
//   - AutoPromote: nil → true (plan §2: auto-promote is default-on, gated
//     by the four-signal heuristic + the trust validator).
//   - AutoPromoteSkipScore: 1e-6 (NOT plan §2's nominal 5.0). Phase A
//     discovered SQLite's bm25() returns very small magnitudes (1e-5 ..
//     1e-6 on small wikis); 5.0 is unreachable in practice, so we hard-
//     code 1e-6 as the default that lets signal 4 actually fire. The
//     [ask] auto_promote_skip_score key remains the user-tunable seam.
//   - AutoPromoteScoreFloor: 0 (reserved; Phase A's gate doesn't read it,
//     but we plumb the key so Phase C/D additions don't churn cfg).
//   - AutoPromoteHedgingPhrases: nil → empty; EvaluateAutoPromote
//     re-defaults to wiki.DefaultAutoPromoteConfig().HedgingPhrases when
//     the slice is empty, so we leave it at len 0 here rather than
//     duplicating the canonical six phrases on the cmd side.
func applyAskDefaults(c *AskConfig) {
	if c.AutoPromote == nil {
		t := true
		c.AutoPromote = &t
	}
	if c.AutoPromoteSkipScore == 0 {
		// SQLite-realistic: bm25() magnitudes on a small wiki land in the
		// 1e-5..1e-6 range, far below the plan's nominal 5.0. See
		// internal/wiki/autopromote.go header for the contract.
		c.AutoPromoteSkipScore = 1e-6
	}
}

func init() {
	// Cobra prints errors and usage by default when RunE returns non-nil; we
	// render UserError ourselves in Execute() via cliutil, so silence cobra to
	// avoid the duplicate one-liner above the polished 3-line block.
	rootCmd.SilenceErrors = true
	rootCmd.SilenceUsage = true
	rootCmd.SetVersionTemplate("{{.Version}}\n")
	rootCmd.PersistentFlags().StringVar(&overrideProvider, "provider", "", "override LLM provider (gemini|anthropic|openai-compatible|ollama)")
	rootCmd.PersistentFlags().StringVar(&overrideModel, "model", "", "override LLM model")
	rootCmd.AddCommand(initCmd)
	rootCmd.AddCommand(ingestCmd)
	rootCmd.AddCommand(askCmd)
	rootCmd.AddCommand(promoteCmd)
	rootCmd.AddCommand(lintCmd)
	rootCmd.AddCommand(mcpCmd)
	rootCmd.AddCommand(statusCmd)
	rootCmd.AddCommand(versionCmd)
}
