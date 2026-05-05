// Package mcp wraps github.com/mark3labs/mcp-go's MCPServer and registers
// llmwiki's seven tools by name. Handlers are thin adapters over internal/db,
// internal/wiki, and the configured llm.Client; structured errors flow back
// to clients as JSON-encoded {code, message, ...} payloads.
package mcp

import (
	mcpgo "github.com/mark3labs/mcp-go/mcp"
	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/mritunjaysharma394/llmwiki/internal/db"
	"github.com/mritunjaysharma394/llmwiki/internal/llm"
	"github.com/mritunjaysharma394/llmwiki/internal/schema"
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
//
// Schema is the active schema doc (sub-project 7 / Phase I Task 14): a
// snapshot loaded once at server start from cmd/root.go's activeSchema,
// which itself is the result of schema.Load(wikiRoot) (returning the
// bundled default when no AGENTS.md / CLAUDE.md is present). The handlers
// thread it into every wiki entrypoint that takes a schema argument
// (ingest, ask, lint, promote) and surface it via the read-only
// `get_schema` tool. There is no per-call override and no
// `set_schema` / `write_schema` tool — the schema is the user's; agents
// introspect, they do not edit (Q15).
type Deps struct {
	Cfg    Config
	DB     *db.DB
	Client llm.Client
	Schema schema.Schema
}

const (
	serverName    = "llmwiki"
	serverVersion = "0.8.0-rc.1" // bumped from "0.7.0-rc.1" for sub-project 8 (automation: watch, maintain, auto-promote, capture-session)
)

// NewServer registers all eight tools — five read-only (list_pages,
// read_page, lint, ask, get_schema) and three write-side (write_page,
// ingest, promote_answer). promote_answer was added in sub-project 6a;
// get_schema was added in sub-project 7 / Phase I Task 14 and surfaces
// the active schema as a structured payload (read-only — no
// `set_schema` or `write_schema` counterpart, Q15).
func NewServer(d Deps) *mcpsrv.MCPServer {
	s := mcpsrv.NewMCPServer(serverName, serverVersion)
	s.AddTool(listPagesTool(), listPagesHandler(d))
	s.AddTool(readPageTool(), readPageHandler(d))
	s.AddTool(lintTool(), lintHandler(d))
	s.AddTool(askTool(), askHandler(d))
	s.AddTool(writePageTool(), writePageHandler(d))
	s.AddTool(ingestTool(), ingestHandler(d))
	s.AddTool(promoteAnswerTool(), promoteAnswerHandler(d))
	s.AddTool(getSchemaTool(), getSchemaHandler(d))
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

// writePageTool's schema mirrors what the LLM ingest pipeline accepts:
// every page MUST include at least one evidence quote, each evidence
// entry names the source_file the quote was copied from, and quotes
// that don't byte-exactly substring-match the named source_file get
// rejected by ValidateAndAttachEvidence. mcp-go v0.50.0's WithArray +
// Items takes a JSON-Schema-shaped map[string]any for nested objects;
// we match the same shape used by the v3 write_pages tool schema in
// internal/wiki/ops.go.
func writePageTool() mcpgo.Tool {
	return mcpgo.NewTool(
		"write_page",
		mcpgo.WithDescription(
			"Create a new wiki page. Every evidence quote MUST be a verbatim substring "+
				"of the named source_file's content; the same validator that gates 'llmwiki "+
				"ingest' rejects unverified quotes here. Title collisions return code: "+
				"\"title_exists\"; pre-ingest your sources first if a quote's source_file "+
				"isn't yet known to the DB."),
		mcpgo.WithString("title", mcpgo.Description("New page title (must not collide with an existing page)."), mcpgo.Required()),
		mcpgo.WithString("body", mcpgo.Description("Markdown body of the page."), mcpgo.Required()),
		mcpgo.WithArray("evidence",
			mcpgo.Description("At least one quote required. Each quote must byte-exactly substring-match the named source_file's content."),
			mcpgo.Required(),
			mcpgo.Items(map[string]any{
				"type": "object",
				"properties": map[string]any{
					"quote":       map[string]any{"type": "string", "description": "Verbatim substring of the named source_file's content."},
					"source_file": map[string]any{"type": "string", "description": "Path of an already-ingested source_file (relative_path)."},
				},
				"required": []string{"quote", "source_file"},
			})),
		mcpgo.WithArray("links",
			mcpgo.Description("Optional outbound links to other pages."),
			mcpgo.Items(map[string]any{
				"type": "object",
				"properties": map[string]any{
					"to":   map[string]any{"type": "string"},
					"type": map[string]any{"type": "string", "enum": []string{"supports", "contradicts", "supersedes", "related"}},
				},
				"required": []string{"to", "type"},
			})),
	)
}

// ingestTool exposes the ingest pipeline to MCP clients. The handler
// drives wiki.IngestSource, the same callable cmd/ingest's runIngest
// wraps. force re-ingests despite an unchanged whole-source hash; feed
// / sitemap force-dispatch the relevant fetcher.
func ingestTool() mcpgo.Tool {
	return mcpgo.NewTool(
		"ingest",
		mcpgo.WithDescription("Ingest a source (file path, URL, or GitHub repo) into the wiki. Returns pages_written, evidence_quotes, dropped_pages."),
		mcpgo.WithString("source", mcpgo.Description("Source URI: a local path, http(s):// URL, or github.com URL."), mcpgo.Required()),
		mcpgo.WithBoolean("force", mcpgo.Description("Re-ingest even if the source's content hash is unchanged.")),
		mcpgo.WithBoolean("feed", mcpgo.Description("Force feed-parser dispatch (RSS / Atom / JSON Feed).")),
		mcpgo.WithBoolean("sitemap", mcpgo.Description("Force sitemap dispatch.")),
		mcpgo.WithNumber("max_pages", mcpgo.Description("Cap on feed entries / sitemap pages.")),
		// Sub-project 6b (v0.6): cross-page page-update pass.
		mcpgo.WithBoolean("update_existing",
			mcpgo.Description("after writing new pages, propose updates to existing pages whose claims this source touches; off by default. Pages whose proposed body fails byte-exact substring-match validation against the union of (new + existing) source files stay at their previous version. Returns pages_updated and pages_update_failed counters in the response.")),
	)
}

// promoteAnswerTool lifts a saved answer (.llmwiki/answers/<ts>-<slug>.md)
// into a real wiki page. Defensive re-validation runs every parsed
// evidence quote through ValidateAndAttachEvidence — the same byte-exact
// substring-match validator that gates write_page — against the current
// on-disk source bytes. Quotes whose source files have changed since the
// ask are rejected with code: "evidence_invalid"; title collisions
// return code: "title_exists". The trust property holds at the MCP
// boundary: a stale answer never reaches disk.
//
// Inputs differ from cmd/promote.go in one way: MCP accepts only an
// absolute answer_path (the agent doesn't share the CLI's answers-dir
// convention). title / rewrite / no_save mirror PromoteOptions exactly.
func promoteAnswerTool() mcpgo.Tool {
	return mcpgo.NewTool(
		"promote_answer",
		mcpgo.WithDescription(
			"Promote a saved answer file into a real wiki page. Defensive "+
				"re-validation runs every evidence quote through the same byte-exact "+
				"substring-match validator that gates write_page; quotes whose source "+
				"files have changed since the ask are rejected with code: "+
				"\"evidence_invalid\". Title collisions return code: \"title_exists\"."),
		mcpgo.WithString("answer_path",
			mcpgo.Description("Absolute path to the saved-answer file."),
			mcpgo.Required()),
		mcpgo.WithString("title",
			mcpgo.Description("Override page title; defaults to Title-Cased question.")),
		mcpgo.WithBoolean("rewrite",
			mcpgo.Description("LLM-rewrite the answer body into wiki prose; default false. The rewrite must preserve every evidence quote verbatim or it falls back to the verbatim body.")),
		mcpgo.WithBoolean("no_save",
			mcpgo.Description("Skip appending a **promote** line to log.md; default false. Debug-only.")),
	)
}

// getSchemaTool exposes the active wiki schema (the merged
// bundled+user content) over MCP. Read-only by design (Q15): there is
// no `set_schema` / `write_schema` counterpart. An agent that can
// rewrite the system prompts an agent runs against is a confused-deputy
// surface; agents read the schema to adapt behaviour, they do not
// edit it. The schema is the user's; agents introspect; that's it.
//
// The Karpathy-pattern alignment is the reason this tool exists at
// all: an agent asked "ingest this URL" can call get_schema first,
// learn the wiki's domain ("meeting notes" vs "research papers"), and
// tailor its ingestion behaviour accordingly — all in one round-trip,
// no out-of-band signalling required.
//
// TRUST PROPERTY UNCHANGED. get_schema is a pure read; the trust gate
// (ValidateAndAttachEvidence) is unaffected by what an agent learns
// from this tool.
func getSchemaTool() mcpgo.Tool {
	return mcpgo.NewTool(
		"get_schema",
		mcpgo.WithDescription(
			"Return the active wiki schema (the merged bundled+user content). "+
				"Read-only; no per-call overrides. Agents that introspect the schema "+
				"before acting (Karpathy-pattern compliant) can adapt their behaviour "+
				"to 'this wiki is about meeting notes' without out-of-band signalling. "+
				"Returns schema_version, domain, ontology_fields, prompts (raw "+
				"templates — the server renders them at LLM-call time), glossary, "+
				"hash, and doc_path (\"AGENTS.md\" / \"CLAUDE.md\" / \"\" for bundled)."),
	)
}
