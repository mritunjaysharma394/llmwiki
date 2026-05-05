# MCP server

`llmwiki mcp` runs as a Model Context Protocol stdio server, letting Claude
Desktop or Claude Code drive the wiki directly. The model calls happen in the
client, so `llmwiki mcp` itself never calls a provider API — your Claude
subscription is the only budget.

## Setup

### Claude Code

```bash
claude mcp add llmwiki --env LLMWIKI_DIR=$PWD -- llmwiki mcp
```

Or hand-edit `~/.config/claude-code/mcp_servers.json` with the same JSON
shape as the Claude Desktop block below.

### Claude Desktop

Add to `~/Library/Application Support/Claude/claude_desktop_config.json`
(macOS) or the platform equivalent:

```json
{
  "mcpServers": {
    "llmwiki": {
      "command": "llmwiki",
      "args": ["mcp"],
      "env": { "LLMWIKI_DIR": "/Users/me/my-wiki" }
    }
  }
}
```

Restart Claude Desktop. The tools appear in the tool picker.

## Tools exposed

| Tool             | Purpose                                                                  |
| ---------------- | ------------------------------------------------------------------------ |
| `list_pages`     | List pages (optional title prefix and limit)                             |
| `read_page`      | Fetch one page's body, frontmatter, evidence, and links                  |
| `lint`           | Staleness + contradiction report across the wiki                         |
| `ask`            | Grounded Q&A with source quotes                                          |
| `write_page`     | Propose a new page; rejected unless every quote is byte-exact            |
| `ingest`         | Pull a new source into the wiki                                          |
| `promote_answer` | Lift a saved answer into a real page with the same trust validation     |
| `get_schema`     | Read-only introspection of the active schema                             |

`ingest` accepts an optional `update_existing: bool` argument (default
matches the wiki's `[ingest] update_existing` config); when enabled the
response gains `pages_updated` and `pages_update_failed` keys.

`get_schema` returns `schema_version`, `domain`, `ontology_fields`,
`prompts.{...}`, `glossary`, `hash`, and `doc_path`. Karpathy-pattern
compliant: an agent can fetch the schema, learn this wiki's domain
framing, and ingest accordingly in one round-trip. There is no
`set_schema` — agents introspect, they do not edit.

## What `write_page` actually guarantees

When Claude proposes a new page, the server runs the proposal through the
same validation pipeline as `llmwiki ingest`:

1. The named `source_file` must already be ingested into this wiki.
2. Every quote in `evidence[]` must be a byte-exact substring of that
   source file on disk.
3. If either check fails, the tool returns a structured error with one of
   `title_exists | evidence_required | source_not_ingested |
   source_not_readable | evidence_invalid | write_failed | bad_request |
   db_error` — the client re-renders the error so you see *why* the write
   was rejected, instead of silently writing bad content.

The MCP server logs to stderr; stdout is reserved for JSON-RPC, so it can
be safely piped from any client.
