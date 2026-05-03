package cmd

import (
	"github.com/mritunjaysharma394/llmwiki/internal/version"
	"github.com/spf13/cobra"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version, commit, build date, Go version",
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.Println(version.Format())
		return nil
	},
}
