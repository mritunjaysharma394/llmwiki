package cmd

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	mcpsrv "github.com/mark3labs/mcp-go/server"
	"github.com/spf13/cobra"

	"github.com/mritunjaysharma394/llmwiki/internal/mcp"
)

// mcpCmd boots the MCP stdio server. v1's intent is exactly what the spec
// promises: read .llmwiki/config.toml from cwd (or LLMWIKI_DIR when set),
// expose ingest / ask / list_pages / read_page / write_page / lint as MCP
// tools, and let the agent drive the wiki. No flags in v1 — every knob the
// server needs lives in config.toml; runtime control is via the MCP tool
// calls themselves.
//
// Stdio is the JSON-RPC channel; logs MUST go to stderr or MCP clients
// will choke trying to parse log lines as JSON-RPC messages.
//
// Lifecycle: server.ServeStdio blocks until stdin closes (EOF) or the
// signal handler it installs trips on SIGINT/SIGTERM. Either path returns
// nil-or-clean from this RunE; fatal startup errors (config missing,
// invalid provider, etc.) propagate up and Execute() exits non-zero so
// MCP clients render the failure as a tool launch error.
var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Run the MCP stdio server",
	Long: `Run the MCP stdio server. Reads .llmwiki/config.toml from the current working directory
(override with LLMWIKI_DIR=<path>).

The server exposes ingest, ask, list_pages, read_page, write_page, and lint as
MCP tools. write_page enforces the same evidence-quote validation as
'llmwiki ingest' — quotes that don't byte-exactly substring-match the named
source_file are rejected with a structured error and never reach disk.

Logs go to stderr (stdout is the JSON-RPC channel). SIGINT/SIGTERM trigger
clean shutdown; stdin EOF also exits cleanly. Exits non-zero on fatal startup
errors.`,
	RunE: runMCP,
}

func runMCP(cmd *cobra.Command, args []string) error {
	// Logs to stderr — stdout is the JSON-RPC channel and any stray bytes
	// confuse MCP clients. The stdlib log default is stderr but we set it
	// explicitly to defend against future code that swaps it.
	log.SetOutput(os.Stderr)

	// Sub-project 7 / Phase I Task 14: pass the active schema (loaded
	// once by cmd/root.go's loadConfig from AGENTS.md / CLAUDE.md or
	// the bundled default) into the MCP server so every handler that
	// runs an LLM prompt — ingest, ask, lint, promote — uses the same
	// schema the rest of the CLI does, and so the new read-only
	// `get_schema` tool can surface it to agents.
	srv := mcp.NewServer(mcp.Deps{
		Cfg: mcp.Config{
			WikiDir: cfg.Wiki.WikiDir,
			RawDir:  cfg.Wiki.RawDir,
			DBPath:  cfg.Wiki.DBPath,
		},
		DB:     database,
		Client: llmClient,
		Schema: activeSchema,
	})

	// server.ServeStdio installs its own SIGINT/SIGTERM handler; we wrap
	// the call so a parent cancel propagates too (cobra's
	// cmd.Context() is cancelled when Execute() returns).
	ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	stdio := mcpsrv.NewStdioServer(srv)
	stdio.SetErrorLogger(log.New(os.Stderr, "[llmwiki mcp] ", log.LstdFlags))
	if err := stdio.Listen(ctx, os.Stdin, os.Stdout); err != nil {
		// EOF on stdin and ctx-cancel both surface as nil from Listen
		// (the upstream lib treats them as clean shutdown). Anything
		// non-nil is a real error worth a non-zero exit.
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("mcp stdio server: %w", err)
	}
	return nil
}
