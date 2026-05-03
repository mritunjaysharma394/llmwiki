package cmd

import (
	"fmt"
	"os"

	"github.com/mritunjaysharma394/llmwiki/internal/db"
	"github.com/spf13/cobra"
)

const defaultConfig = `[llm]
provider = "anthropic"
model    = "claude-opus-4-7"
ollama_url = "http://localhost:11434"

[wiki]
wiki_dir = ".llmwiki/wiki"
raw_dir  = ".llmwiki/raw"
db_path  = ".llmwiki/wiki.db"
`

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize a new llmwiki in the current directory",
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		return nil // override parent — no config needed for init
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		dirs := []string{".llmwiki", ".llmwiki/wiki", ".llmwiki/raw"}
		for _, d := range dirs {
			if err := os.MkdirAll(d, 0755); err != nil {
				return fmt.Errorf("creating %s: %w", d, err)
			}
		}
		configPath := ".llmwiki/config.toml"
		if _, err := os.Stat(configPath); err == nil {
			fmt.Println("llmwiki already initialized.")
			return nil
		}
		if err := os.WriteFile(configPath, []byte(defaultConfig), 0644); err != nil {
			return fmt.Errorf("writing config: %w", err)
		}
		d, err := db.Open(".llmwiki/wiki.db")
		if err != nil {
			return fmt.Errorf("initializing database: %w", err)
		}
		d.Close()
		fmt.Println("Initialized llmwiki in .llmwiki/")
		fmt.Println("Edit .llmwiki/config.toml to configure your LLM provider and model.")
		return nil
	},
}
