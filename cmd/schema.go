package cmd

import (
	"fmt"
	"os"

	"github.com/mritunjaysharma394/llmwiki/internal/cliutil"
	"github.com/mritunjaysharma394/llmwiki/internal/schema"
	"github.com/spf13/cobra"
)

// schemaCmd is the parent for `schema show` and `schema validate`. The
// subcommands deliberately bypass loadConfig's strict validation path
// (see cmd/root.go's PersistentPreRunE) so a user with a malformed
// AGENTS.md / CLAUDE.md can still reach `schema validate` to diagnose
// the problem — the strict path would have bounced them out before the
// subcommand body ran.
var schemaCmd = &cobra.Command{
	Use:   "schema",
	Short: "Inspect, validate, or migrate the wiki's schema",
	Long: `The wiki's schema lives at AGENTS.md (canonical) or CLAUDE.md
(Claude-Code-native fallback) in the wiki root. It defines the page
ontology, the prompts that drive ingest / ask / contradiction
detection / cross-page updates / promote rewrite / lint, and an
optional glossary. The bundled default is byte-identical to v0.6
behaviour; a user-edited schema doc overrides it.

Trust property: the schema controls what the LLM is *asked*, not
what counts as valid evidence. The bundled substring-match validator
runs after every LLM call regardless of what the schema-rendered
prompt told the LLM.`,
}

var schemaShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Print the effective schema (merged: bundled + user)",
	RunE:  runSchemaShow,
}

var schemaValidateCmd = &cobra.Command{
	Use:   "validate",
	Short: "Validate AGENTS.md / CLAUDE.md against the bundled schema-format contract",
	RunE:  runSchemaValidate,
}

func init() {
	schemaShowCmd.Flags().Bool("bundled", false, "ignore AGENTS.md / CLAUDE.md and print the bundled default")
	schemaShowCmd.Flags().Bool("doc", false, "print AGENTS.md / CLAUDE.md verbatim (or notice if absent)")
	schemaShowCmd.Flags().Bool("hash", false, "print only the active schema's sha256 hex hash (scriptable; useful for comparing across wikis sharing a schema)")
	schemaCmd.AddCommand(schemaShowCmd)
	schemaCmd.AddCommand(schemaValidateCmd)
	rootCmd.AddCommand(schemaCmd)
}

// schemaPathLabel returns the doc path for display in `schema validate`'s
// success block, or the literal "bundled" when no on-disk doc backs
// activeSchema.
func schemaPathLabel(s schema.Schema) string {
	if s.DocPath == "" {
		return "bundled"
	}
	return s.DocPath
}

func runSchemaShow(cmd *cobra.Command, args []string) error {
	hash, _ := cmd.Flags().GetBool("hash")
	if hash {
		// Scriptable one-liner: just the hex hash + newline. Mitigates
		// spec Risk #3 — a team co-edits a single schema doc and copies
		// it across N wikis; this lets them script the comparison.
		fmt.Println(activeSchema.Hash())
		return nil
	}
	bundled, _ := cmd.Flags().GetBool("bundled")
	doc, _ := cmd.Flags().GetBool("doc")
	switch {
	case bundled:
		os.Stdout.Write(schema.DefaultDoc)
	case doc:
		if activeSchema.DocPath == "" {
			fmt.Println("no AGENTS.md or CLAUDE.md present; bundled defaults are in effect (run `llmwiki init --rewrite-schema` to write one)")
			return nil
		}
		os.Stdout.Write(activeSchema.Raw())
	default:
		// merged-effective: print activeSchema's content, with a leading
		// header naming hash + doc path or "bundled".
		if activeSchema.DocPath == "" {
			fmt.Println("schema: bundled (no AGENTS.md or CLAUDE.md)")
		} else {
			fmt.Printf("schema: %s (hash %s)\n", activeSchema.DocPath, activeSchema.Hash())
		}
		fmt.Println()
		os.Stdout.Write(activeSchema.Raw())
	}
	return nil
}

func runSchemaValidate(cmd *cobra.Command, args []string) error {
	if err := activeSchema.Validate(); err != nil {
		return cliutil.Wrap(
			"validating schema",
			err,
			"edit AGENTS.md / CLAUDE.md to fix the listed problems, then re-run `llmwiki schema validate`")
	}
	fmt.Printf("%s (schema_version %d)\n", schemaPathLabel(activeSchema), activeSchema.Version)
	fmt.Println("  ✓ all 6 required prompts present")
	fmt.Println("  ✓ all required placeholders present in each prompt")
	fmt.Println("  ✓ page ontology has required fields: title, body, evidence")
	fmt.Printf("  ✓ glossary has %d terms (optional)\n", len(activeSchema.Glossary))
	fmt.Println()
	fmt.Println("  trust property: enforced by bundled validator")
	fmt.Println("  (substring-match against source files; not configurable from this doc)")
	fmt.Println("OK")
	return nil
}
