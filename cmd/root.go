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
}

type Config struct {
	LLM    LLMConfig    `toml:"llm"`
	Wiki   WikiConfig   `toml:"wiki"`
	Ask    AskConfig    `toml:"ask"`
	Ingest IngestConfig `toml:"ingest"`
}

// RespectGitignoreOrDefault returns the configured value, defaulting to true
// when the config left it unset.
func (c IngestConfig) RespectGitignoreOrDefault() bool {
	if c.RespectGitignore == nil {
		return true
	}
	return *c.RespectGitignore
}

var (
	cfg              *Config
	llmClient        llm.Client
	database         *db.DB
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
	if cfg.LLM.OllamaURL == "" {
		cfg.LLM.OllamaURL = "http://localhost:11434"
	}
	applyIngestDefaults(&cfg.Ingest)
	var err error
	database, err = db.Open(cfg.Wiki.DBPath)
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}
	switch cfg.LLM.Provider {
	case "anthropic", "":
		if os.Getenv("ANTHROPIC_API_KEY") == "" {
			return cliutil.Wrap(
				"ANTHROPIC_API_KEY is not set",
				nil,
				"export ANTHROPIC_API_KEY=sk-ant-... (get one at https://console.anthropic.com/settings/keys), or use --provider ollama",
			)
		}
		llmClient = llm.NewAnthropicClient(cfg.LLM.Model)
	case "ollama":
		llmClient = llm.NewOllamaClient(cfg.LLM.Model, cfg.LLM.OllamaURL)
	default:
		return fmt.Errorf("unknown provider %q", cfg.LLM.Provider)
	}
	return nil
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
}

func init() {
	rootCmd.SetVersionTemplate("{{.Version}}\n")
	rootCmd.PersistentFlags().StringVar(&overrideProvider, "provider", "", "override LLM provider (anthropic|ollama)")
	rootCmd.PersistentFlags().StringVar(&overrideModel, "model", "", "override LLM model")
	rootCmd.AddCommand(initCmd)
	rootCmd.AddCommand(ingestCmd)
	rootCmd.AddCommand(askCmd)
	rootCmd.AddCommand(lintCmd)
	rootCmd.AddCommand(statusCmd)
	rootCmd.AddCommand(versionCmd)
}
