# Contributing

```bash
go build ./...
go test ./...
```

Tests run in cassette **replay** mode by default — no API key required, no
network. To re-record a cassette against the live API:

```bash
LLMWIKI_RECORD=1 ANTHROPIC_API_KEY=sk-ant-... go test ./... -run TestName -v
```

The nightly `cassette-refresh` workflow under `.github/workflows/`
re-records everything against `secrets.ANTHROPIC_API_KEY` /
`secrets.GEMINI_API_KEY` / `secrets.OPENROUTER_API_KEY` and opens a PR if
the cassettes drifted.

`make smoke` runs the README quickstart end-to-end against a tiny synthetic
source, using a recorded cassette so it works without an API key. The
smoke cassette must first be recorded with:

```bash
LLMWIKI_RECORD=1 ANTHROPIC_API_KEY=... go test ./cmd/ -run TestSmokeIngestThenAsk -v
```

## Project layout

- `cmd/` — Cobra subcommands (ingest, ask, watch, maintain, promote, schema, mcp, ...)
- `internal/wiki/` — page lifecycle: write, validate, retro-link, contradict, promote
- `internal/ingest/` — source pipelines: local, URL, GitHub, PDF, feed, sitemap, chunker
- `internal/llm/` — provider clients: Anthropic, Gemini, OpenAI-compat, Ollama
- `internal/db/` — SQLite schema and migrations
- `internal/mcp/` — Model Context Protocol stdio server
- `internal/queue/` — SQLite-backed work queue used by `watch`
- `internal/schema/` — bundled `AGENTS.md` default + ontology types
- `docs/` — user-facing docs
- `docs/superpowers/specs/` & `docs/superpowers/plans/` — design notes

## Specs and plans

Design specs and per-phase plans for in-flight work live under
`docs/superpowers/`. New sub-projects start with a spec, then a plan, then
phased implementation commits.
