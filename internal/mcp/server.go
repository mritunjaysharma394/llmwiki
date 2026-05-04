// Package mcp wraps github.com/mark3labs/mcp-go's MCPServer and registers
// llmwiki's six tools by name. Handlers are thin adapters over internal/db,
// internal/wiki, and the configured llm.Client; structured errors flow back
// to clients as JSON-encoded {code, message, ...} payloads.
package mcp

import (
	mcpgo "github.com/mark3labs/mcp-go/mcp"
	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/mritunjaysharma394/llmwiki/internal/db"
	"github.com/mritunjaysharma394/llmwiki/internal/llm"
)

// Config carries the slim subset of cmd.Config the MCP handlers need. The
// cobra command in Phase G2 maps cmd.Config -> mcp.Config explicitly so the
// internal/mcp package never imports cmd (avoids the obvious import cycle
// and keeps the handlers testable without spinning up cobra).
type Config struct {
	WikiDir string
	RawDir  string
	DBPath  string
}

// Deps is the wiring that NewServer needs to register every tool. Tests
// construct Deps directly with an in-memory-friendly llm.Client; production
// code (Phase G2's cmd/mcp.go) builds it from cmd.Config and the same
// llmClient the rest of the CLI uses.
type Deps struct {
	Cfg    Config
	DB     *db.DB
	Client llm.Client
}

const (
	serverName    = "llmwiki"
	serverVersion = "1.1.0"
)

// NewServer registers all six tools — four read-only (list_pages, read_page,
// lint, ask) implemented in this commit and two write-side (write_page,
// ingest) wired as "not yet implemented" stubs that Phase G2 replaces.
func NewServer(d Deps) *mcpsrv.MCPServer {
	s := mcpsrv.NewMCPServer(serverName, serverVersion)
	s.AddTool(listPagesTool(), listPagesHandler(d))
	s.AddTool(readPageTool(), readPageHandler(d))
	s.AddTool(lintTool(), lintHandler(d))
	s.AddTool(askTool(), askHandler(d))
	s.AddTool(writePageTool(), writePageHandler(d))
	s.AddTool(ingestTool(), ingestHandler(d))
	return s
}

// listPagesTool / readPageTool / lintTool / askTool / writePageTool /
// ingestTool define the public schemas surfaced over MCP. Names use snake_case
// to match the convention most MCP clients (Claude Desktop, Cursor, etc.)
// already display, and to keep the same shape this project exposes everywhere
// else (e.g. cmd/ingest.go, internal/wiki/ops.go's write_pages tool schema).
func listPagesTool() mcpgo.Tool {
	return mcpgo.NewTool(
		"list_pages",
		mcpgo.WithDescription("List wiki pages, optionally filtered by a title prefix."),
		mcpgo.WithString("prefix", mcpgo.Description("Optional title prefix filter; empty means all pages.")),
		mcpgo.WithNumber("limit", mcpgo.Description("Maximum number of pages to return. Defaults to 50.")),
	)
}

func readPageTool() mcpgo.Tool {
	return mcpgo.NewTool(
		"read_page",
		mcpgo.WithDescription("Return a single page by exact title, including body, links, evidence, and source files."),
		mcpgo.WithString("title", mcpgo.Description("Exact page title."), mcpgo.Required()),
	)
}

func lintTool() mcpgo.Tool {
	return mcpgo.NewTool(
		"lint",
		mcpgo.WithDescription("Run staleness and contradiction checks across the wiki. Returns a human-readable text report."),
	)
}

func askTool() mcpgo.Tool {
	return mcpgo.NewTool(
		"ask",
		mcpgo.WithDescription("Answer a question grounded in the wiki, returning the answer plus structured source citations."),
		mcpgo.WithString("question", mcpgo.Description("The question to answer."), mcpgo.Required()),
	)
}

func writePageTool() mcpgo.Tool {
	return mcpgo.NewTool(
		"write_page",
		mcpgo.WithDescription("Create or update a wiki page (Phase G2)."),
		mcpgo.WithString("title", mcpgo.Required()),
		mcpgo.WithString("body", mcpgo.Required()),
	)
}

func ingestTool() mcpgo.Tool {
	return mcpgo.NewTool(
		"ingest",
		mcpgo.WithDescription("Ingest a source URI into the wiki (Phase G2)."),
		mcpgo.WithString("source", mcpgo.Required()),
	)
}
