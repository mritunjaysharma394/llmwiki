# llmwiki

`llmwiki` is a CLI that ingests sources (files, URLs, repos, PDFs) and
synthesizes them into a hyperlinked wiki, with answers grounded in verbatim
source quotes.

## Quickstart

```bash
go build -o ./llmwiki .
./llmwiki init
export ANTHROPIC_API_KEY=sk-ant-...
./llmwiki ingest <path-or-url-or-github-repo>
./llmwiki ask "what does this codebase do?"
```

To use a local model instead of the Anthropic API:

```bash
./llmwiki init --provider ollama
```

Run `./llmwiki status` to see the wiki state.

## Features

- **Per-file evidence** — every quote in an answer is anchored to a specific
  file (or PDF page), and rendered as `(path/to/file.go:lines)`.
- **PDF support** — text PDFs are extracted page-by-page; scanned/OCR-only
  pages are detected and skipped with a warning.
- **GitHub repos and local directories** — shallow-cloned or walked, with a
  built-in deny list (`.git`, `node_modules`, `vendor`, lockfiles, binaries),
  optional `.gitignore` honoring, and a configurable per-file size cap.
- **URL ingestion** — content-type-sniffed; HTML pages are passed through
  Readability + html-to-markdown, PDF URLs go through the PDF path.
- **FTS5 search** — page bodies and evidence quotes are indexed in SQLite for
  fast retrieval at ask time.
- **Auto-archived answers** — every `ask` writes a Markdown transcript under
  `.llmwiki/answers/` and a row in the database.

## Design docs

- Specs: [`docs/superpowers/specs/`](docs/superpowers/specs/)
- Plans: [`docs/superpowers/plans/`](docs/superpowers/plans/)

The wiki itself, the SQLite database, and saved answers all live under
`.llmwiki/` in the working directory.
