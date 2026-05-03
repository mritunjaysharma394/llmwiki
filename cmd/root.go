package cmd

import (
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
	"github.com/mritunjaysharma394/llmwiki/internal/db"
	"github.com/mritunjaysharma394/llmwiki/internal/llm"
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

type Config struct {
	LLM  LLMConfig  `toml:"llm"`
	Wiki WikiConfig `toml:"wiki"`
}

var (
	cfg       *Config
	llmClient llm.Client
	database  *db.DB
)

var rootCmd = &cobra.Command{
	Use:   "llmwiki",
	Short: "LLM-powered personal wiki",
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		return loadConfig()
	},
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func loadConfig() error {
	cfg = &Config{}
	if _, err := toml.DecodeFile(".llmwiki/config.toml", cfg); err != nil {
		return fmt.Errorf("config not found — run 'llmwiki init' first: %w", err)
	}
	if cfg.LLM.OllamaURL == "" {
		cfg.LLM.OllamaURL = "http://localhost:11434"
	}
	var err error
	database, err = db.Open(cfg.Wiki.DBPath)
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}
	switch cfg.LLM.Provider {
	case "anthropic", "":
		llmClient = llm.NewAnthropicClient(cfg.LLM.Model)
	case "ollama":
		llmClient = llm.NewOllamaClient(cfg.LLM.Model, cfg.LLM.OllamaURL)
	default:
		return fmt.Errorf("unknown provider %q", cfg.LLM.Provider)
	}
	return nil
}

func init() {
	rootCmd.AddCommand(initCmd)
	rootCmd.AddCommand(ingestCmd)
	rootCmd.AddCommand(askCmd)
	rootCmd.AddCommand(lintCmd)
	rootCmd.AddCommand(statusCmd)
}
