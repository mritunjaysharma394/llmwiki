# Configuration

Configuration lives at `.llmwiki/config.toml`, written by `llmwiki init`.
Pre-existing configs missing newer keys silently inherit defaults.

## Providers

| Provider              | Setup                                                                                              | Notes                                                                                       |
| --------------------- | -------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------- |
| `gemini` *(default)*  | `export GEMINI_API_KEY=...` (https://aistudio.google.com/apikey)                                   | Default model `gemini-2.5-flash`                                                            |
| `anthropic`           | `export ANTHROPIC_API_KEY=sk-ant-...`, **or** drive llmwiki via MCP from Claude Desktop / Code     | Default model `claude-haiku-4-5`. Highest quote-fidelity in our cassette tests              |
| `openai-compatible`   | Edit `[providers.openai_compat].base_url` to point at your endpoint's `/v1`                        | Tested against Groq, OpenRouter, Together, Cerebras, Mistral La Plateforme                  |
| `ollama`              | `ollama pull llama3.2`, then `llmwiki init --provider ollama`                                      | Runs against `http://localhost:11434` by default. Source content never leaves your machine  |

Pages from any provider go through the same evidence validator — a quote
that doesn't byte-exact substring-match its named source file is dropped
before disk.

## `[llm]`

| Key          | Default                    | Description                                                                       |
| ------------ | -------------------------- | --------------------------------------------------------------------------------- |
| `provider`   | `"gemini"`                 | LLM provider: `"gemini"`, `"anthropic"`, `"openai-compatible"`, or `"ollama"`     |
| `model`      | provider-dependent         | Model identifier passed to the provider                                           |
| `ollama_url` | `"http://localhost:11434"` | Base URL of the Ollama server                                                     |

## `[providers.openai_compat]`

| Key             | Default                  | Description                                                            |
| --------------- | ------------------------ | ---------------------------------------------------------------------- |
| `base_url`      | `""`                     | OpenAI-compatible endpoint (e.g. `https://openrouter.ai/api/v1`)       |
| `api_key_env`   | `"OPENAI_COMPAT_API_KEY"` | Name of the environment variable holding the API key                  |
| `default_model` | `""`                     | Model passed to `/chat/completions` (override per-call with `--model`) |

## `[wiki]`

| Key        | Default              | Description                                  |
| ---------- | -------------------- | -------------------------------------------- |
| `wiki_dir` | `".llmwiki/wiki"`    | Directory holding generated Markdown pages   |
| `raw_dir`  | `".llmwiki/raw"`     | Cached raw source content                    |
| `db_path`  | `".llmwiki/wiki.db"` | SQLite database path                         |

## `[ask]`

| Key            | Default | Description                                                                       |
| -------------- | ------- | --------------------------------------------------------------------------------- |
| `auto_save`    | `true`  | Archive every answer under `.llmwiki/answers/`                                    |
| `auto_promote` | `true`  | After each ask, run the four-signal heuristic gate; on pass, promote to a page    |

## `[ingest]`

| Key                       | Default   | Description                                                       |
| ------------------------- | --------- | ----------------------------------------------------------------- |
| `update_existing`         | `true`    | Cross-page page-update pass at every ingest (Karpathy-pattern)    |
| `max_file_bytes`          | `262144`  | Per-file size limit (256 KiB)                                     |
| `chunk_size_bytes`        | `16384`   | Target packed-chunk size for LLM calls                            |
| `http_timeout_seconds`    | `30`      | Timeout on URL fetches                                            |
| `http_max_bytes`          | `5242880` | Max URL response body size (5 MiB)                                |
| `pdf_min_text_per_page`   | `50`      | Below this text length a PDF page is treated as scanned, skipped  |
| `extra_text_extensions`   | `[]`      | Additional file extensions the walker treats as text              |
| `extra_skip_globs`        | `[]`      | Additional path globs to skip                                     |
| `respect_gitignore`       | `true`    | Honor `.gitignore` in directory and repo walks                    |
| `feed_request_per_second` | `1.0`     | Polite rate limit for feed/sitemap fetches                        |
| `feed_max_entries`        | `50`      | Max feed entries ingested per fetch                               |
| `sitemap_max_pages`       | `200`     | Max URLs crawled from a sitemap                                   |

## `[watch]`

| Key                | Default | Description                                                       |
| ------------------ | ------- | ----------------------------------------------------------------- |
| `dirs`             | `[]`    | Directories to watch when `llmwiki watch` is invoked bare         |
| `debounce_seconds` | `2`     | Coalesce rapid writes (editors saving in chunks)                  |
| `max_attempts`     | `3`     | Retry budget before marking a queue row `failed`                  |

## Environment variables

| Variable                | Description                                                                       |
| ----------------------- | --------------------------------------------------------------------------------- |
| `GEMINI_API_KEY`        | Required when `provider = "gemini"`                                               |
| `ANTHROPIC_API_KEY`     | Required when `provider = "anthropic"` and you're not driving via MCP             |
| `OPENAI_COMPAT_API_KEY` | Default env var name for `provider = "openai-compatible"`                         |
| `LLMWIKI_DIR`           | Override the wiki directory `llmwiki mcp` operates against (defaults to `$PWD`)   |
| `LLMWIKI_CASSETTE`      | When set, the LLM client replays from a recorded cassette instead of calling the live API |
| `NO_COLOR`              | Disable ANSI colors in CLI output                                                 |

## Privacy

- **Anthropic / Gemini / OpenAI-compatible providers**: source content is
  sent to the configured API at ingest and ask time.
- **Ollama provider**: everything stays on your machine.
- **MCP server**: when driven by Claude Desktop / Claude Code, your Claude
  subscription handles the model calls — `llmwiki mcp` itself does not call
  any LLM API. Source content reaches whichever model the client is
  configured to use.
- **`.llmwiki/`** holds the wiki, the SQLite database, the saved answer
  archive, and `config.toml`. It's local and `.gitignore`d by convention.
- No telemetry, ever.
