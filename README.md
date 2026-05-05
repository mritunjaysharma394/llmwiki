# llmwiki

`llmwiki` ingests sources (files, URLs, repos, PDFs, RSS/Atom feeds, sitemaps)
and synthesises them into a Markdown wiki — with answers grounded in
verbatim source quotes. Trust comes from validation: every page that ships
includes evidence quotes that are byte-exact substrings of the original
source. Hallucinated pages are dropped before they hit disk.

![llmwiki demo](docs/assets/demo.gif)
<!-- TODO(release): asset missing — record via tools/record-demo.sh (requires vhs or asciinema). -->

## Quickstart

The fastest path uses your existing Claude subscription via MCP — no API
key, no `export`, two commands:

```bash
go install github.com/mritunjaysharma394/llmwiki@latest

mkdir my-wiki && cd my-wiki
llmwiki init --mcp-only
claude mcp add llmwiki --env LLMWIKI_DIR=$PWD -- llmwiki mcp
```

Now ask Claude Code to `ingest https://github.com/golang/example` or to
summarise a PDF you drop on it. The model calls happen inside Claude;
`llmwiki mcp` itself never calls a provider API.

Don't have a Claude subscription? Run llmwiki against any provider directly:

```bash
llmwiki init --provider anthropic     # set ANTHROPIC_API_KEY first
llmwiki init --provider gemini        # set GEMINI_API_KEY first
llmwiki init --provider ollama        # local-only, no key
```

Then:

```bash
llmwiki ingest https://github.com/golang/example
llmwiki ask "what does the gotypes example do?"
llmwiki status
```

## Install

```bash
# From source (requires Go 1.26+):
go install github.com/mritunjaysharma394/llmwiki@latest

# Pre-built binary (replace OS/ARCH as needed):
curl -fsSL https://github.com/mritunjaysharma394/llmwiki/releases/latest/download/llmwiki-darwin-arm64.tar.gz \
  | tar -xz -C /usr/local/bin

# From a checkout:
git clone https://github.com/mritunjaysharma394/llmwiki.git
cd llmwiki && make install   # installs to $HOME/.local/bin
```

Verify with `llmwiki version`.

## What you get

**A persistent wiki, not a query-time RAG cache.** Every ingest can modify
10–15 existing pages alongside writing new ones (Karpathy's framing). Every
ask can file a good answer back as a permanent page. Contradictions surface
inline; orphaned pages and schema drift surface in lint.

**A trust property bundled in the binary.** Every page on disk has at least
one evidence quote that is a byte-exact substring of its source. The
validator runs after every LLM call on every code path — `ingest`,
`promote`, `mcp.write_page`, every provider. Switching to a cheaper model
produces a sparser wiki, never a more wrong one.

**Obsidian-native output.** `.llmwiki/wiki/` is a folder of Markdown files
with YAML frontmatter and `[[wikilinks]]`. Open it in Obsidian and you get
backlinks, the graph view, search, and Dataview queries with no plugin.

## Always-on

Three commands compose the always-on surface:

- `llmwiki watch <dir>` — fsnotify daemon. Drop a file in the watched
  directory; debounce 2s; ingest via a SQLite-backed crash-resumable queue.
- `llmwiki maintain` — umbrella for cron / launchd / GitHub Actions. Runs
  `--lint`, `--refresh-stale`, `--promote-pending`. `--dry-run` composes.
- `llmwiki capture-session` — Claude Code Stop-hook companion. Pipes session
  JSON in; wiki-touching turns are filed as saved answers; the auto-promote
  gate decides whether they become permanent pages.

Plus auto-promote inside `llmwiki ask`: a four-signal heuristic gate (cited
pages, evidence quotes, length, no hedging, no near-duplicate) files
qualifying answers as permanent pages. Default ON; opt out with
`[ask] auto_promote = false`.

Full launchd / systemd / GitHub Actions recipes, watch examples, and the
Stop-hook recipe live in **[`docs/automation.md`](docs/automation.md)**.

## Learn more

- [`docs/mcp.md`](docs/mcp.md) — MCP server setup, tool surface, write_page contract
- [`docs/living-wiki.md`](docs/living-wiki.md) — promote, auto-promote, contradictions, retro-link, cross-page updates
- [`docs/schema.md`](docs/schema.md) — customise the wiki via `AGENTS.md` / `CLAUDE.md`
- [`docs/ingestion.md`](docs/ingestion.md) — every source type and flag
- [`docs/configuration.md`](docs/configuration.md) — full config reference, env vars, providers, privacy
- [`docs/automation.md`](docs/automation.md) — cron, launchd, systemd, GitHub Actions, Claude Code hooks
- [`docs/architecture.md`](docs/architecture.md) — pipeline diagram, trust property, design specs
- [`CONTRIBUTING.md`](CONTRIBUTING.md) — dev setup, cassettes, smoke test

## License

Apache-2.0. See [`LICENSE`](LICENSE) and [`CHANGELOG.md`](CHANGELOG.md).

## Acknowledgements

Inspired by Andrej Karpathy's
[note on building a personal wiki with an LLM](https://gist.github.com/karpathy/442a6bf555914893e9891c11519de94f).
Thanks to the authors of the dependencies that make this possible —
[`charmbracelet/glamour`](https://github.com/charmbracelet/glamour),
[`mark3labs/mcp-go`](https://github.com/mark3labs/mcp-go),
[`mmcdole/gofeed`](https://github.com/mmcdole/gofeed),
[`go-shiori/go-readability`](https://github.com/go-shiori/go-readability),
[`JohannesKaufmann/html-to-markdown`](https://github.com/JohannesKaufmann/html-to-markdown),
the [`spf13/cobra`](https://github.com/spf13/cobra) family, and
[`mattn/go-sqlite3`](https://github.com/mattn/go-sqlite3).
