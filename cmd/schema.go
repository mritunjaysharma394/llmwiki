package cmd

import (
	"bufio"
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	"github.com/mritunjaysharma394/llmwiki/internal/cliutil"
	"github.com/mritunjaysharma394/llmwiki/internal/schema"
	"github.com/mritunjaysharma394/llmwiki/internal/wiki"
	"github.com/spf13/cobra"
)

// schemaCmd is the parent for `schema show`, `schema validate`, and
// `schema migrate`. The show + validate subcommands deliberately bypass
// loadConfig's strict validation path (see cmd/root.go's
// PersistentPreRunE) so a user with a malformed AGENTS.md / CLAUDE.md
// can still reach `schema validate` to diagnose the problem — the
// strict path would have bounced them out before the subcommand body
// ran. `schema migrate` runs through loadSchemaSoftWithDB which still
// skips the strict Validate check but does open the DB + pick an LLM
// client, since migrate has actual work to do beyond inspection.
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

var schemaMigrateCmd = &cobra.Command{
	Use:   "migrate",
	Short: "Re-ingest pages on a prior schema hash under the active schema",
	Long: `Walks every page whose schema_hash differs from the active schema's
hash, re-reads its source files, re-runs IngestSourceFilesToPages
under the active schema, and runs ValidateAndAttachEvidence as
usual. Pages whose proposed body fails validation STAY AT THEIR
PRIOR VERSION — the trust property holds.

Resumable: succeeded pages get stamped with the active hash, so
re-running after a Ctrl-C skips the already-migrated pages
naturally (Q14).

Cost: roughly one LLM call per migrated page. On Gemini Flash
(free tier), comfortable for any wiki size; on Anthropic Haiku
~$0.005/page; on Ollama, expect more update_failed because small
models often miss the structured-output schema. --dry-run runs
the LLM calls but skips disk + DB writes so users can preview
the cost picture before committing.`,
	RunE: runSchemaMigrate,
}

func init() {
	schemaShowCmd.Flags().Bool("bundled", false, "ignore AGENTS.md / CLAUDE.md and print the bundled default")
	schemaShowCmd.Flags().Bool("doc", false, "print AGENTS.md / CLAUDE.md verbatim (or notice if absent)")
	schemaShowCmd.Flags().Bool("hash", false, "print only the active schema's sha256 hex hash (scriptable; useful for comparing across wikis sharing a schema)")
	schemaMigrateCmd.Flags().Bool("yes", false, "skip the confirmation prompt")
	schemaMigrateCmd.Flags().Bool("dry-run", false, "run LLM calls but do not write to disk or DB")
	schemaCmd.AddCommand(schemaShowCmd)
	schemaCmd.AddCommand(schemaValidateCmd)
	schemaCmd.AddCommand(schemaMigrateCmd)
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

// confirmStdin reads one line from os.Stdin and returns true iff the
// trimmed lowercase response begins with "y". Empty / EOF / anything
// else returns false (the safe default for a destructive prompt).
func confirmStdin(prompt string) bool {
	fmt.Print(prompt)
	r := bufio.NewReader(os.Stdin)
	line, err := r.ReadString('\n')
	if err != nil && line == "" {
		return false
	}
	line = strings.TrimSpace(strings.ToLower(line))
	return strings.HasPrefix(line, "y")
}

func runSchemaMigrate(cmd *cobra.Command, args []string) error {
	yes, _ := cmd.Flags().GetBool("yes")
	dryRun, _ := cmd.Flags().GetBool("dry-run")

	activeHash := activeSchema.Hash()
	drifted, err := database.ListPagesNotAtHash(activeHash, math.MaxInt32)
	if err != nil {
		return cliutil.Wrap("listing pages on prior schema", err,
			"the database may be corrupt; back up .llmwiki/wiki.db and re-init")
	}
	if len(drifted) == 0 {
		fmt.Println("no pages on prior schema; nothing to do")
		return nil
	}

	shortHash := activeHash
	if len(shortHash) > 8 {
		shortHash = shortHash[:8] + "..."
	}
	fmt.Printf("Re-ingesting %d page(s) under active schema (hash %s).\n", len(drifted), shortHash)
	fmt.Println("This walks every page's source_files and re-runs IngestSourceFilesToPages")
	fmt.Println("under the active schema, then runs ValidateAndAttachEvidence as usual.")
	fmt.Println("Pages whose proposed body fails validation STAY AT THEIR PRIOR VERSION.")
	if dryRun {
		fmt.Println("DRY RUN: LLM calls will fire, but no disk or DB writes will happen.")
	}
	if !yes {
		if !confirmStdin("Continue? [y/N] ") {
			fmt.Println("aborted")
			return nil
		}
	}

	var migrated, unchanged, skipped int
	type failedRow struct{ title, reason string }
	var failed []failedRow
	for i, p := range drifted {
		fmt.Printf("[%d/%d] %s\n", i+1, len(drifted), p.Title)
		outcome, err := wiki.MigratePage(cmd.Context(), database, llmClient, cfg.Wiki.WikiDir, p, activeSchema, dryRun)
		if err != nil {
			// MigratePage is engineered not to return an error today;
			// surface anything unexpected as a per-page failed outcome
			// so we keep walking the rest of the list.
			failed = append(failed, failedRow{title: p.Title, reason: err.Error()})
			continue
		}
		switch outcome.Kind {
		case "migrated":
			migrated++
		case "unchanged":
			unchanged++
		case "skipped":
			skipped++
			reason := outcome.Reason
			if reason == "" {
				reason = "no source files"
			}
			fmt.Printf("    [skipped: %s]\n", reason)
		case "failed":
			reason := outcome.Reason
			if reason == "" {
				reason = "unknown"
			}
			failed = append(failed, failedRow{title: p.Title, reason: reason})
		}
	}

	fmt.Println()
	fmt.Printf("%d page(s) brought to active schema.\n", migrated)
	if unchanged > 0 {
		fmt.Printf("%d page(s) unchanged (proposed body identical to prior).\n", unchanged)
	}
	if skipped > 0 {
		fmt.Printf("%d page(s) skipped (no backing source files).\n", skipped)
	}
	if len(failed) > 0 {
		fmt.Printf("%d page(s) update FAILED — kept at prior version:\n", len(failed))
		for _, f := range failed {
			fmt.Printf("  - %s: %s\n", f.title, f.reason)
		}
	}

	if !dryRun {
		_ = wiki.AppendLog(cfg.Wiki.WikiDir, wiki.LogEntry{
			At:   time.Now().UTC(),
			Kind: "schema_migrate",
			Payload: fmt.Sprintf("hash → %s; %d migrated, %d unchanged, %d skipped, %d failed",
				shortHash, migrated, unchanged, skipped, len(failed)),
		})
	}
	return nil
}
