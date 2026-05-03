package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/mritunjaysharma394/llmwiki/internal/cliutil"
	"github.com/spf13/cobra"
)

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
`

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize a new wiki in the current directory",
	RunE:  runInit,
}

func init() {
	initCmd.Flags().String("provider", "anthropic", "default LLM provider: anthropic or ollama")
}

func runInit(cmd *cobra.Command, args []string) error {
	provider, _ := cmd.Flags().GetString("provider")

	dir := ".llmwiki"
	for _, sub := range []string{"", "wiki", "raw", "answers"} {
		p := filepath.Join(dir, sub)
		if err := os.MkdirAll(p, 0755); err != nil {
			return fmt.Errorf("mkdir %s: %w", p, err)
		}
	}

	cfgPath := filepath.Join(dir, "config.toml")
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		var content string
		switch provider {
		case "ollama":
			content = defaultConfigOllamaToml
		default:
			content = defaultConfigToml
		}
		if err := os.WriteFile(cfgPath, []byte(content), 0644); err != nil {
			return fmt.Errorf("writing config: %w", err)
		}
	}
	fmt.Printf("Initialized wiki at %s\n", dir)

	if provider == "anthropic" {
		if os.Getenv("ANTHROPIC_API_KEY") == "" {
			return cliutil.Wrap(
				"ANTHROPIC_API_KEY is not set",
				nil,
				"export ANTHROPIC_API_KEY=sk-ant-... (get one at https://console.anthropic.com/settings/keys), or use --provider ollama",
			)
		}
	}
	return nil
}
