# Sub-project 5 — MCP Server, Obsidian Output, Cheap Providers — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship v1.1 of `llmwiki` with three compounding adoption levers — an MCP stdio server (so users drive the wiki via their Claude Pro subscription instead of API tokens), Obsidian-native disk output (`[[wikilinks]]`, `index.md` hub, append-only `log.md`, Dataview frontmatter), and a provider-abstraction refactor adding Google Gemini and a generic OpenAI-compatible client (Groq / OpenRouter / Together / Cerebras / Mistral La Plateforme) — without weakening sub-project 1's substring-validated evidence invariant.

**Architecture:** Two new `llm.Client` implementations (`internal/llm/openai_compat.go`, `internal/llm/gemini.go`) with the same `Complete` / `CompleteStructured` / `CompleteStream` surface as `AnthropicClient` and `OllamaClient`; a `[providers]` config block plus `applyProviderDefaults` and an expanded provider switch in `cmd/root.go`'s `loadConfig`; new frontmatter keys (`tags`, `sources`, `created`) round-tripped by `ParsePage` / `WritePage`; a new `internal/wiki/obsidian.go` exposing `RewriteBareReferencesAsWikilinks`, `RegenerateIndex`, `AppendLog` called from `cmd/ingest.go`, `cmd/ask.go`, and the new MCP write path; a new `internal/mcp` package wrapping `github.com/mark3labs/mcp-go` with six tool handlers (`ingest`, `ask`, `list_pages`, `read_page`, `write_page`, `lint`) all delegating to existing internal packages so the trust validator continues to be the single gatekeeper for disk writes; a new `cmd/mcp.go` cobra command running `server.ServeStdio`. **No schema changes** (`PRAGMA user_version` stays at 3).

**Tech Stack:** Go 1.26, plus one new direct dep — `github.com/mark3labs/mcp-go v0.50.0` (MIT, stdio + tool API). Gemini and OpenAI-compatible clients use stdlib `net/http` + `encoding/json` (no SDK); we explicitly do not pull `google.golang.org/api` or `github.com/openai/openai-go` to keep the binary small and the dependency surface defensible. No new runtime deps beyond `mark3labs/mcp-go`.

**Spec:** [`docs/superpowers/specs/2026-05-04-mcp-and-cheap-providers-design.md`](../specs/2026-05-04-mcp-and-cheap-providers-design.md)

**Resolved open questions** (the spec lists five; all are resolved here so the implementer is unblocked):

1. **Default Gemini model:** `gemini-2.0-flash`. Free tier, 1M context, structured-output capable on the `functionDeclarations` API. Picked over `gemini-2.5-flash` because Flash 2.0 is the current free-tier default and is the model name AI Studio's new-key onboarding documents.
2. **Directory lock for `llmwiki mcp`:** **skip** in v1. The spec offers "cheap" or "advisory file lock"; cheap is correct for v1 because the only race vector (concurrent `ingest` from a second terminal) requires the user to deliberately run two processes against the same wiki. If a real user reports it, add `flock(.llmwiki/.lock)` in v1.2.
3. **`write_page` title collision:** **refuse-with-error**. Return `{code: "title_exists", existing_path: "<path>"}` and force the agent to call `read_page` + an explicit supersede via the `links` array (`{to: <title>, type: "supersedes"}`). Safer for an agent that hallucinates titles, matches sub-project 1's "fail loud, never silently degrade" posture.
4. **`--provider` + `--model` flag precedence:** **honour both flags as-given**. `loadConfig` already overrides `cfg.LLM.Provider` and `cfg.LLM.Model` independently when each flag is non-empty (root.go:112–117); we keep that semantics and document it. `--provider gemini --model gemini-2.5-pro` selects Gemini-Pro. The `[providers.<name>].default_model` is consulted only when `cfg.LLM.Model` is empty after both flag overrides and the per-provider config block.
5. **Rename `ask` to `query`:** **no rename**. The CLI keeps `llmwiki ask`; the MCP tool is also called `ask`. Namespacing comes from the MCP server name (`llmwiki.ask`), so collisions inside an MCP client's tool list are scoped automatically. Renaming would break every existing user invocation and every existing answer file's archive path for zero gain.

---

## File Structure

| File | Purpose | Action |
|---|---|---|
| `internal/llm/openai_compat.go` | Generic Chat-Completions / function-calling / SSE client | Create |
| `internal/llm/openai_compat_test.go` | `httptest.Server` fixtures: tool-call happy path, JSON-extraction fallback, SSE stream, 4xx | Create |
| `internal/llm/gemini.go` | Gemini v1beta `generateContent` / `streamGenerateContent` HTTP client | Create |
| `internal/llm/gemini_test.go` | `httptest.Server` fixtures: `functionCall` happy path, JSON-extraction fallback, SSE, 4xx | Create |
| `internal/llm/cassette.go` | (unchanged — record/replay continues to wrap whichever `Client` is selected) | — |
| `internal/llm/testdata/cassettes/TestIngestGemini__*.json` | Recorded Gemini cassette | Create |
| `internal/llm/testdata/cassettes/TestIngestOpenAICompat__*.json` | Recorded OpenRouter cassette + synthetic malformed-tool-call entry | Create |
| `internal/llm/testdata/cassettes/TestMCPWritePageRoundtrip__*.json` | Recorded MCP cassette | Create |
| `cmd/root.go` | `[providers]` config struct fields, `applyProviderDefaults`, expanded provider switch, expanded `--provider` flag help | Modify |
| `cmd/init.go` | New `gemini` / `openai-compatible` config templates, walkthrough copy, AI Studio URL hint | Modify |
| `cmd/init_test.go` | Each provider template parses + round-trips through `loadConfig`; non-TTY default is `gemini`; `--provider openai-compatible` writes the `[providers.openai_compat]` block | Create |
| `cmd/mcp.go` | `mcp` cobra command + `server.ServeStdio` + signal handling | Create |
| `cmd/mcp_test.go` | Smoke: server starts, registers six tools, exits cleanly on context cancel | Create |
| `internal/wiki/page.go` | `ParsePage` / `WritePage` round-trip new `tags`, `sources`, `created` keys | Modify |
| `internal/wiki/page_test.go` | Round-trip new keys; pre-v1.1 page (no new keys) parses unchanged | Modify |
| `internal/wiki/obsidian.go` | `RewriteBareReferencesAsWikilinks`, `RegenerateIndex`, `AppendLog`, `LogEntry` | Create |
| `internal/wiki/obsidian_test.go` | Idempotency, code-fence and inline-backtick exclusion, byte-identical regen, RFC3339 log lines | Create |
| `internal/wiki/ops.go` | `writePagesTool` description nudges `[[Page Title]]` syntax (no validator change) | Modify |
| `cmd/ingest.go` | After successful page persistence: rewrite wikilinks before disk write, then `RegenerateIndex` + `AppendLog` once at end | Modify |
| `cmd/ask.go` | After successful answer write: `AppendLog` (no `index.md` regen — `ask` doesn't change the page set) | Modify |
| `internal/mcp/server.go` | `NewServer(cfg, db, llmClient) *server.MCPServer`; tool registry; helpers for structured-error JSON | Create |
| `internal/mcp/handlers.go` | Six handlers: `list_pages`, `read_page`, `lint`, `ask`, `ingest`, `write_page` | Create |
| `internal/mcp/server_test.go` | Each tool registered; `write_page` valid + invalid-evidence + title-collision paths; `read_page` / `list_pages` / `lint` happy paths; uses `mcp-go`'s in-process test client | Create |
| `cmd/ingest_test.go` | Cassette test `TestIngestGemini` + `TestIngestOpenAICompat` (drives `runIngest` with each provider) | Modify |
| `cmd/smoke_test.go` | (unchanged) | — |
| `README.md` | New onboarding section leading with Gemini, MCP-client config snippet, Obsidian section | Modify |
| `CHANGELOG.md` | `## [1.1.0] — 2026-05-04` entry covering all three pillars | Modify |
| `go.mod` / `go.sum` | Add `github.com/mark3labs/mcp-go v0.50.0` | Modify |

**Total:** 17 tasks across 8 phases (A–H). Each task ends with a single commit; the working tree is green at every commit boundary.

---

## Phase summaries

Each phase below is self-contained: it does not depend on later-phase exports, and its last task leaves the tree compiling and `go test ./...` green so a fresh subagent can pick up the next phase from a clean checkout.

- **Phase A — Provider foundations: OpenAI-compat client (Tasks 1–2).** Add `internal/llm/openai_compat.go` with full `Client` surface (Complete / CompleteStructured / CompleteStream) plus `httptest.Server`-driven unit tests. Pure addition; no existing provider is touched, no `cmd/` wiring yet. Exports: `llm.NewOpenAICompatClient(baseURL, apiKey, model)`. Risk: tool-calling shape varies across vendors (Groq, OpenRouter, Together, Cerebras, Mistral) — we send the conservatively-typed request that all five accept and gate per-vendor quirks behind small conditional blocks if and only if a fixture forces it.
- **Phase B — Gemini provider (Task 3).** Add `internal/llm/gemini.go` against the `generativelanguage.googleapis.com/v1beta` HTTP API with `functionDeclarations` + `toolConfig.functionCallingConfig.mode = "ANY"` for forced tool calls, SSE streaming via `streamGenerateContent`, and the same JSON-extraction fallback when the model fails to call the tool. Exports: `llm.NewGeminiClient(model)`. Risk: Google's region restrictions cause 403 on a fresh key in some Cloud regions — surface that case as a `cliutil.UserError` with the AI Studio URL.
- **Phase C — Provider wiring + init walkthrough (Task 4).** Add `[providers]` config struct, expand the `loadConfig` provider switch to handle `gemini` / `openai-compatible` (and keep `anthropic` / `ollama` working), expand `init` templates and the walkthrough copy to recommend Gemini first. Exports: `cmd.ProvidersConfig`, `cmd.applyProviderDefaults`. Risk: pre-v1.1 configs without a `[providers]` block must keep working — `applyProviderDefaults` fills missing values silently, mirroring the `applyIngestDefaults` pattern.
- **Phase D — Cassette tests for new providers (Tasks 5–6).** Record `TestIngestGemini` and `TestIngestOpenAICompat` cassettes; the second includes a synthetic malformed-tool-call entry to force the JSON-extraction fallback path. Exports: none (test fixtures only). Risk: cassette drift — the existing nightly cassette-refresh workflow already covers any cassette under `internal/llm/testdata/cassettes/`, so the new ones inherit drift detection automatically.
- **Phase E — Obsidian frontmatter additions (Task 7).** Round-trip `tags: [llmwiki, ingest]`, `sources: [...]`, `created: YYYY-MM-DD` through `ParsePage` / `WritePage` without breaking pre-v1.1 page files. Exports: extends `wiki.Page` with `Tags []string`, `Sources []string`, `Created time.Time`. Risk: the by-hand YAML parser must accept flat string arrays for `tags` and `sources` — fixture-driven tests cover the format Dataview expects.
- **Phase F — Obsidian-native output: wikilinks + `index.md` + `log.md` (Tasks 8–9).** Add `internal/wiki/obsidian.go` with the three pure helpers, integrate them at the persist-loop tail in `cmd/ingest.go` and the answer-write site in `cmd/ask.go`, and update the `writePagesTool` description to nudge the model toward `[[Page Title]]` syntax. Exports: `wiki.RewriteBareReferencesAsWikilinks`, `wiki.RegenerateIndex`, `wiki.AppendLog`, `wiki.LogEntry`. Risk: idempotency — the rewriter must be a no-op on already-linked bodies and must skip fenced code blocks and inline backticks; tests cover both.
- **Phase G — MCP server (Tasks 10–13).** Add `mark3labs/mcp-go v0.50.0`, create `internal/mcp/{server,handlers}.go` with the six handlers, add `cmd/mcp.go`, then record `TestMCPWritePageRoundtrip`. The handlers delegate to existing internal packages — the trust validator in `wiki.ValidateAndAttachEvidence` remains the single gatekeeper for disk writes. `write_page` is the load-bearing tool and is implemented last in this phase. Exports: `mcp.NewServer`, `cmd/mcp` cobra command. Risk: `mark3labs/mcp-go` is under active development — pin `v0.50.0` and let the nightly cassette-refresh PR-review pass also re-pin if a breaking change lands.
- **Phase H — README + CHANGELOG + tag (Tasks 14–17).** Rewrite the README onboarding section to lead with Gemini, add the MCP-client snippet for Claude Desktop / Claude Code, add the Obsidian "open `.llmwiki/wiki/` as a vault, no plugin needed" section. Add a `[1.1.0]` CHANGELOG entry. Tag `v1.1.0-rc.1` locally (no push). Exports: a tag. Risk: the README must not promise a feature the binary doesn't have — task ordering puts the rewrite last so every claim has already been built and verified by Phases A–G.

---

## Phase A — Provider foundations: OpenAI-compatible client

### Task 1: `internal/llm/openai_compat.go` — Complete + CompleteStructured + httptest fixtures

**Files:**
- Create: `internal/llm/openai_compat.go`
- Create: `internal/llm/openai_compat_test.go`

- [ ] **Step 1: Write failing tests**

Create `internal/llm/openai_compat_test.go` with four cases:

1. `TestOpenAICompatComplete_HappyPath` — `httptest.Server` returns `{"choices":[{"message":{"content":"hello"}}]}`; assert `Complete(ctx, sys, user)` returns `"hello"` and the request body had `model`, `messages`, `Authorization: Bearer <key>`.
2. `TestOpenAICompatCompleteStructured_HappyPath` — server returns a `tool_calls` payload `[{"function":{"name":"write_pages","arguments":"{\"pages\":[]}"}}]`; assert the result map has key `pages`.
3. `TestOpenAICompatCompleteStructured_FallbackJSONExtraction` — server returns no `tool_calls` but `content: "{\"pages\":[]}"`; assert the same result map (proves we strip leading prose, code fences, and trailing junk like the existing Ollama path does).
4. `TestOpenAICompat4xx` — server returns 401 with `{"error":{"message":"invalid api key"}}`; assert the returned `error` contains both the status code and the message body so callers (and `cliutil.UserError`) can render usefully.

- [ ] **Step 2: Run tests and confirm failure**

Run: `go test ./internal/llm/ -run TestOpenAICompat -v`
Expected: FAIL — package does not yet exist.

- [ ] **Step 3: Implement `internal/llm/openai_compat.go`**

```go
package llm

import (
    "bytes"
    "context"
    "encoding/json"
    "fmt"
    "io"
    "net/http"
    "strings"
)

type OpenAICompatClient struct {
    baseURL string
    apiKey  string
    model   string
    http    *http.Client
}

func NewOpenAICompatClient(baseURL, apiKey, model string) *OpenAICompatClient { /* ... */ }

func (c *OpenAICompatClient) Complete(ctx context.Context, system, user string) (string, error) { /* POST /chat/completions, no tools */ }

func (c *OpenAICompatClient) CompleteStructured(ctx context.Context, system, user string, ts ToolSchema) (map[string]any, error) {
    // POST /chat/completions with:
    //   tools: [{type: "function", function: {name: ts.Name, description: ts.Description, parameters: {type: "object", properties: ts.Properties, required: ts.Required}}}]
    //   tool_choice: {type: "function", function: {name: ts.Name}}
    // On response: prefer choices[0].message.tool_calls[0].function.arguments (JSON-decode it).
    // Fallback: choices[0].message.content — strip code fences (mirror OllamaClient.CompleteStructured behaviour, lines 104–111),
    // attempt json.Unmarshal. Return ErrToolCallMissing wrapped error if both fail.
}

func (c *OpenAICompatClient) CompleteStream(ctx context.Context, system, user string, w io.Writer) (string, error) {
    // POST /chat/completions with stream: true. Read SSE: parse "data: {...}\n\n" frames,
    // pull choices[0].delta.content, write to w, accumulate. Stop on "data: [DONE]" frame.
}

var _ Client = (*OpenAICompatClient)(nil)
```

The structured-call fallback path mirrors the JSON-extraction logic already in `internal/llm/ollama.go:104-111` (TrimSpace, find first `{`, find last `}`). Reuse that snippet pattern; do not extract it into a shared helper in this task — it's small enough to duplicate, and Phase B's Gemini client will need its own near-copy too. We can DRY them in a v1.2 follow-up if a third caller appears.

- [ ] **Step 4: Run tests and confirm pass**

Run: `go test ./internal/llm/ -run TestOpenAICompat -v`
Expected: PASS — four subtests green.

- [ ] **Step 5: Commit**

```bash
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
feat(llm): generic OpenAI-compatible client (Chat Completions + tool-calling + SSE)

NewOpenAICompatClient wraps any base_url that speaks OpenAI's /chat/completions
schema (Groq, OpenRouter, Together, Cerebras, Mistral La Plateforme). Implements
Complete, CompleteStructured (with tool_choice forced to the named tool), and
CompleteStream (SSE) — same surface as AnthropicClient and OllamaClient so
NewCassetteClient wraps it transparently. Structured-call path falls back to
JSON-extraction from message.content when the model returns prose instead of a
tool call, mirroring OllamaClient's existing fallback so cheap free-tier models
that drop the tool call still produce parseable output for the validator.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase B — Gemini provider

### Task 2: `internal/llm/gemini.go` — generateContent + streamGenerateContent

**Files:**
- Create: `internal/llm/gemini.go`
- Create: `internal/llm/gemini_test.go`

- [ ] **Step 1: Write failing tests**

Create `internal/llm/gemini_test.go` with the same four-shape matrix as Phase A, against Gemini's request/response JSON:

1. `TestGeminiComplete_HappyPath` — server returns `{"candidates":[{"content":{"parts":[{"text":"hello"}]}}]}`; assert `Complete` returns `"hello"`.
2. `TestGeminiCompleteStructured_HappyPath` — server returns `{"candidates":[{"content":{"parts":[{"functionCall":{"name":"write_pages","args":{"pages":[]}}}]}}]}`; assert the result map has key `pages`. Assert the request body had `tools[0].functionDeclarations[0].name == "write_pages"`, `toolConfig.functionCallingConfig.mode == "ANY"`, `allowedFunctionNames == ["write_pages"]`.
3. `TestGeminiCompleteStructured_FallbackJSONExtraction` — server returns a text part `{"text":"{\"pages\":[]}"}` with no `functionCall`; assert fallback unmarshals.
4. `TestGemini4xx` — server returns 403 (region-restricted simulator) with `{"error":{"code":403,"message":"API not enabled in region X"}}`; assert error includes status and message.

- [ ] **Step 2: Run tests and confirm failure**

Run: `go test ./internal/llm/ -run TestGemini -v`
Expected: FAIL — package does not yet exist.

- [ ] **Step 3: Implement `internal/llm/gemini.go`**

```go
package llm

import (
    "bytes"
    "context"
    "encoding/json"
    "fmt"
    "io"
    "net/http"
    "os"
)

type GeminiClient struct {
    apiKey string
    model  string
    http   *http.Client
}

const geminiBaseURL = "https://generativelanguage.googleapis.com/v1beta"

func NewGeminiClient(model string) *GeminiClient {
    return &GeminiClient{
        apiKey: os.Getenv("GEMINI_API_KEY"),
        model:  model,
        http:   http.DefaultClient,
    }
}

// Complete: POST /models/{model}:generateContent?key=API_KEY
//   body: {"contents":[{"role":"user","parts":[{"text":user}]}],"systemInstruction":{"parts":[{"text":system}]}}
// CompleteStructured: same endpoint with
//   tools: [{functionDeclarations: [{name: ts.Name, description: ts.Description, parameters: {type:"OBJECT", properties: ts.Properties, required: ts.Required}}]}]
//   toolConfig: {functionCallingConfig: {mode:"ANY", allowedFunctionNames:[ts.Name]}}
// On response: prefer candidates[0].content.parts[?].functionCall.args; else extract JSON from candidates[0].content.parts[0].text.
// CompleteStream: POST /models/{model}:streamGenerateContent?alt=sse&key=API_KEY; parse SSE frames.

var _ Client = (*GeminiClient)(nil)
```

For the test server we use `httptest.NewServer` and override the `geminiBaseURL` via an unexported package-level var so tests can point the client at the fake. Mirrors the pattern Phase A used.

- [ ] **Step 4: Run tests and confirm pass**

Run: `go test ./internal/llm/ -run TestGemini -v`
Expected: PASS — four subtests green.

- [ ] **Step 5: Commit**

```bash
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
feat(llm): Gemini v1beta client with functionDeclarations + SSE streaming

NewGeminiClient targets generativelanguage.googleapis.com/v1beta directly
without pulling google.golang.org/api (large transitive cost we explicitly
declined per the spec). Authenticates with GEMINI_API_KEY. CompleteStructured
forces a tool call via toolConfig.functionCallingConfig.mode = "ANY" and
allowedFunctionNames; on a missed call, falls back to JSON-extraction from the
text part identical to the OpenAI-compat client. CompleteStream consumes
streamGenerateContent SSE. Region-restricted 403s surface their message body
verbatim so cmd/ Wrap() can render an AI Studio remediation hint.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase C — Provider wiring + init walkthrough

### Task 3: `[providers]` config block + expanded `loadConfig` switch + init templates

**Files:**
- Modify: `cmd/root.go`
- Modify: `cmd/init.go`
- Create: `cmd/init_test.go` (or modify if it already exists)

- [ ] **Step 1: Write failing tests**

Create `cmd/init_test.go`:

1. `TestInit_DefaultProviderIsGemini` — non-TTY stdin + no `--provider` flag writes `provider = "gemini"` and `model = "gemini-2.0-flash"` to `.llmwiki/config.toml`.
2. `TestInit_OpenAICompatTemplate` — `--provider openai-compatible` writes a `[providers.openai_compat]` block containing `base_url`, `api_key_env`, and a commented-out hint listing the five supported endpoints.
3. `TestInit_AnthropicTemplate` — `--provider anthropic` keeps writing the existing template (regression guard).
4. `TestInit_OllamaTemplate` — `--provider ollama` keeps writing the existing template.
5. `TestLoadConfig_GeminiSelectsGeminiClient` — write a config with `provider = "gemini"`, `GEMINI_API_KEY` set, call `loadConfig`, assert `llmClient` is a `*llm.GeminiClient` (use `reflect.TypeOf(llmClient).String()` to avoid an exported-getter API change).
6. `TestLoadConfig_GeminiMissingKeyReturnsUserError` — same setup with `GEMINI_API_KEY` unset, assert `errors.As(err, &cliutil.UserError{})` and the rendered message contains `https://aistudio.google.com/apikey`.
7. `TestLoadConfig_OpenAICompatHonoursAPIKeyEnvOverride` — config has `api_key_env = "MY_CUSTOM_KEY"`; set that env, assert `*llm.OpenAICompatClient` selected; unset, assert UserError naming `MY_CUSTOM_KEY`.
8. `TestLoadConfig_FlagOverridesHonourBoth` — config `provider="anthropic" model="haiku"`; set `--provider gemini --model gemini-2.5-pro` overrides; assert resulting client is Gemini and `cfg.LLM.Model == "gemini-2.5-pro"` (resolves open question 4).
9. `TestLoadConfig_ApplyProviderDefaultsFillsMissingModel` — config has `provider = "gemini"` but no `model`; after `loadConfig`, `cfg.LLM.Model == "gemini-2.0-flash"` (the `[providers.gemini].default_model`).

- [ ] **Step 2: Run tests and confirm failure**

Run: `go test ./cmd/ -run "TestInit_|TestLoadConfig_" -v`
Expected: FAIL — none of the new providers are wired yet.

- [ ] **Step 3: Implement config types in `cmd/root.go`**

Extend `Config` with:

```go
type ProvidersConfig struct {
    OpenAICompat OpenAICompatProviderConfig `toml:"openai_compat"`
    Gemini       GeminiProviderConfig       `toml:"gemini"`
    Anthropic    AnthropicProviderConfig    `toml:"anthropic"`
    Ollama       OllamaProviderConfig       `toml:"ollama"`
}

type OpenAICompatProviderConfig struct {
    BaseURL    string `toml:"base_url"`
    APIKeyEnv  string `toml:"api_key_env"`
    DefaultModel string `toml:"default_model"`
}
type GeminiProviderConfig    struct{ DefaultModel string `toml:"default_model"` }
type AnthropicProviderConfig struct{ DefaultModel string `toml:"default_model"` }
type OllamaProviderConfig    struct{
    DefaultModel string `toml:"default_model"`
    URL          string `toml:"url"`
}

type Config struct {
    LLM       LLMConfig       `toml:"llm"`
    Wiki      WikiConfig      `toml:"wiki"`
    Ask       AskConfig       `toml:"ask"`
    Ingest    IngestConfig    `toml:"ingest"`
    Providers ProvidersConfig `toml:"providers"`
}
```

Add `applyProviderDefaults(cfg *Config)`:
- Fills `Providers.Gemini.DefaultModel = "gemini-2.0-flash"` if empty.
- Fills `Providers.Anthropic.DefaultModel = "claude-haiku-4-5"` if empty.
- Fills `Providers.Ollama.URL = "http://localhost:11434"` and `DefaultModel = "llama3.2"` if empty.
- Fills `Providers.OpenAICompat.APIKeyEnv = "OPENAI_COMPAT_API_KEY"` if empty.
- After `--provider` / `--model` flag overrides resolve, if `cfg.LLM.Model == ""` look up the active provider's `default_model` and assign it.

Expand the provider switch in `loadConfig`:

```go
switch cfg.LLM.Provider {
case "gemini":
    if os.Getenv("GEMINI_API_KEY") == "" {
        return cliutil.Wrap(
            "GEMINI_API_KEY is not set",
            nil,
            "get a free key at https://aistudio.google.com/apikey, then export GEMINI_API_KEY=...; or use --provider anthropic / openai-compatible / ollama",
        )
    }
    llmClient = llm.NewGeminiClient(cfg.LLM.Model)
case "anthropic", "":
    // existing
case "openai-compatible":
    keyEnv := cfg.Providers.OpenAICompat.APIKeyEnv
    if keyEnv == "" { keyEnv = "OPENAI_COMPAT_API_KEY" }
    if os.Getenv(keyEnv) == "" {
        return cliutil.Wrap(
            fmt.Sprintf("%s is not set", keyEnv),
            nil,
            "set the env var named in [providers.openai_compat].api_key_env (default OPENAI_COMPAT_API_KEY), or use --provider gemini",
        )
    }
    if cfg.Providers.OpenAICompat.BaseURL == "" {
        return cliutil.Wrap(
            "[providers.openai_compat].base_url is empty",
            nil,
            "set base_url to e.g. https://openrouter.ai/api/v1, https://api.groq.com/openai/v1, https://api.together.xyz/v1, https://api.cerebras.ai/v1, or https://api.mistral.ai/v1",
        )
    }
    llmClient = llm.NewOpenAICompatClient(
        cfg.Providers.OpenAICompat.BaseURL,
        os.Getenv(keyEnv),
        cfg.LLM.Model,
    )
case "ollama":
    // existing
default:
    return fmt.Errorf("unknown provider %q", cfg.LLM.Provider)
}
```

The cassette wrapper at the bottom of `loadConfig` continues to wrap whichever client is selected — no change there.

Update the `--provider` flag help string in `init()` to `"override LLM provider (gemini|anthropic|openai-compatible|ollama)"`.

- [ ] **Step 4: Implement `cmd/init.go` template expansion**

Add three new template constants (`defaultConfigGeminiToml`, `defaultConfigOpenAICompatToml`) — the existing two stay unchanged. The Gemini template is the new default when `--provider` is omitted. The walkthrough copy printed before the prompt:

```
Recommended: Gemini (free tier, 1M context, no credit card required)
  Get a key at https://aistudio.google.com/apikey, then:
    export GEMINI_API_KEY=...
  Other options: anthropic | openai-compatible | ollama
```

When stdin is not a TTY (CI), skip the prompt and write the gemini template silently — same idempotency as today's `init`. Use `mattn/go-isatty.IsTerminal(os.Stdin.Fd())` (already an indirect dep).

- [ ] **Step 5: Run tests and confirm pass**

Run: `go test ./cmd/ -run "TestInit_|TestLoadConfig_" -v`
Expected: PASS — nine subtests green.

Run: `go test ./...`
Expected: green.

- [ ] **Step 6: Commit**

```bash
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
feat(cmd): provider wiring for gemini + openai-compatible; init defaults to gemini

[providers] config block stores per-provider details (base_url, api_key_env,
default_model). loadConfig's provider switch now handles "gemini" and
"openai-compatible" alongside "anthropic" and "ollama"; missing keys surface
as cliutil.UserError with provider-specific remediation. applyProviderDefaults
mirrors applyIngestDefaults so pre-v1.1 configs without a [providers] block
keep working — defaults resolve at load time. --provider and --model flags
remain independent overrides; --provider gemini --model gemini-2.5-pro honours
both. init now writes a Gemini template by default (recommended free tier);
--provider anthropic / openai-compatible / ollama keep working and write their
respective templates. Non-TTY stdin skips the walkthrough.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase D — Cassette tests for new providers

### Task 4: `TestIngestGemini` cassette

**Files:**
- Modify: `cmd/ingest_test.go`
- Create: `internal/llm/testdata/cassettes/TestIngestGemini__001.json` (and any subsequent chunks)

- [ ] **Step 1: Write failing test**

Append to `cmd/ingest_test.go`:

```go
func TestIngestGemini(t *testing.T) {
    if testing.Short() { t.Skip("skipping cassette test in -short mode") }
    if _, err := os.Stat("../internal/llm/testdata/cassettes/TestIngestGemini__001.json"); os.IsNotExist(err) {
        t.Skip("cassette not recorded; run with LLMWIKI_RECORD=1 GEMINI_API_KEY=... to record")
    }
    setEnv(t, "LLMWIKI_CASSETTE", "TestIngestGemini")
    setEnv(t, "GEMINI_API_KEY", "test-key-for-replay")
    // Reuse the ingest-test harness pattern from TestIngestSmall: write a tiny
    // synthetic source, configure provider=gemini, run cmd.runIngest, assert
    // pages are written and every page's evidence array substring-matches the
    // source content.
}
```

The skip-when-cassette-missing pattern is the same one already used in `cmd/ingest_integration_test.go` and `cmd/smoke_test.go`.

- [ ] **Step 2: Record the cassette**

Run (one-time):

```bash
export GEMINI_API_KEY=...
LLMWIKI_RECORD=1 go test ./cmd/ -run TestIngestGemini -v
```

The `CassetteClient` already supports `LLMWIKI_RECORD=1` via `modeFromEnv` (`internal/llm/cassette.go:60-62`). Recorded JSON files land under `internal/llm/testdata/cassettes/TestIngestGemini__NNN.json`. Commit the recorded fixtures.

- [ ] **Step 3: Run replay and confirm pass**

Run: `unset GEMINI_API_KEY && go test ./cmd/ -run TestIngestGemini -v`
Expected: PASS — replay path uses `test-key-for-replay` (cassette ignores it).

Also run `go test ./...` to confirm no regression.

- [ ] **Step 4: Commit**

```bash
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
test(cassette): TestIngestGemini — same fixture as TestIngestSmall via gemini

Asserts the validator behaves identically regardless of provider: same source
content yields evidence quotes that substring-match the input. Page count may
differ across providers (Gemini Flash quote-fidelity differs from Haiku), but
quote correctness must not. Fixture recorded with gemini-2.0-flash; replay
uses a sentinel API key that the cassette layer ignores per existing
modeFromEnv() semantics.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 5: `TestIngestOpenAICompat` cassette + synthetic malformed-tool-call entry

**Files:**
- Modify: `cmd/ingest_test.go`
- Create: `internal/llm/testdata/cassettes/TestIngestOpenAICompat__*.json`

- [ ] **Step 1: Write failing test**

Same shape as Task 4, but the cassette includes one synthetic-malformed entry to force the JSON-extraction fallback path (the malformed entry has `tool_calls: null` and `content` containing the JSON wrapped in prose `"Here are the pages: { ... }. Hope this helps!"`). The test asserts the validator still extracts pages from that chunk — proving the fallback path is exercised in CI.

- [ ] **Step 2: Record + edit the cassette**

```bash
export OPENROUTER_API_KEY=...
# Configure config.toml provider = "openai-compatible", base_url = "https://openrouter.ai/api/v1",
# model = "meta-llama-3.1-8b-instruct:free"
LLMWIKI_RECORD=1 go test ./cmd/ -run TestIngestOpenAICompat -v
```

Then hand-edit one of the recorded `__NNN.json` files to drop `tool_calls` and wrap the JSON in prose. Document the edit in the commit message so a future cassette refresh doesn't silently undo it.

- [ ] **Step 3: Run replay and confirm pass**

Run: `unset OPENROUTER_API_KEY && go test ./cmd/ -run TestIngestOpenAICompat -v`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
test(cassette): TestIngestOpenAICompat — OpenRouter free tier + JSON-extraction fallback

Routed through the OpenAI-compat client targeting OpenRouter's free
meta-llama-3.1-8b-instruct:free slot. One recorded chunk has been hand-edited
to strip tool_calls and wrap the JSON in prose, forcing the
CompleteStructured fallback path that strips fences/prose around the JSON
object. This exercises the cheap-provider degraded-path in CI on every run,
not just nightly.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase E — Obsidian frontmatter additions

### Task 6: `tags` / `sources` / `created` round-trip in `ParsePage` and `WritePage`

**Files:**
- Modify: `internal/wiki/page.go`
- Modify: `internal/wiki/page_test.go`

- [ ] **Step 1: Write failing tests**

Append to `internal/wiki/page_test.go`:

1. `TestPage_TagsSourcesCreatedRoundTrip` — build a `Page` with `Tags: []string{"llmwiki", "ingest"}`, `Sources: []string{"internal/db/db.go", "internal/db/queries.go"}`, `Created: time.Date(2026,5,4,0,0,0,0,time.UTC)`; `WritePage` then `ReadPage`; assert all three slices/fields round-trip byte-identical.
2. `TestPage_PreV1_1FilesParseUnchanged` — feed the parser a string from a pre-v1.1 page (without `tags`, `sources`, `created`); assert all three fields are zero-valued and the existing `Title` / `UpdatedAt` / `Evidence` / `Links` round-trip unchanged.
3. `TestPage_TagsArrayFormatIsDataviewCompatible` — assert the written frontmatter contains `tags: [llmwiki, ingest]` (not `tags:\n  - llmwiki\n  - ingest`) because Dataview's bracketed syntax is the canonical one.
4. `TestPage_CreatedIsDateOnly` — assert `created: 2026-05-04` (no time component) per spec.

- [ ] **Step 2: Run tests and confirm failure**

Run: `go test ./internal/wiki/ -run TestPage_ -v`
Expected: FAIL — fields don't exist yet.

- [ ] **Step 3: Implement**

Extend `wiki.Page`:

```go
type Page struct {
    Title       string
    Body        string
    Links       []Link
    SourceIDs   []int64
    ContentHash string
    UpdatedAt   time.Time
    Evidence    []Evidence
    // sub-project 5: Obsidian / Dataview frontmatter
    Tags    []string
    Sources []string
    Created time.Time
}
```

In `WritePage` between `source_ids` and `links`:

```go
if len(p.Tags) > 0 {
    sb.WriteString(fmt.Sprintf("tags: [%s]\n", strings.Join(p.Tags, ", ")))
}
if len(p.Sources) > 0 {
    escaped := make([]string, len(p.Sources))
    for i, s := range p.Sources { escaped[i] = yamlEscapeScalar(s) }
    sb.WriteString(fmt.Sprintf("sources: [%s]\n", strings.Join(escaped, ", ")))
}
if !p.Created.IsZero() {
    sb.WriteString(fmt.Sprintf("created: %s\n", p.Created.UTC().Format("2006-01-02")))
}
sb.WriteString(fmt.Sprintf("updated: %s\n", p.UpdatedAt.UTC().Format("2006-01-02")))
```

The existing `updated_at: <RFC3339>` line stays for round-trip fidelity; the new `updated: <date-only>` is the Dataview-friendly twin.

In `ParsePage`, add three new line prefixes alongside the existing `title:` / `updated_at:` / `content_hash:` / `source_ids:` cases:

```go
case strings.HasPrefix(line, "tags: "):
    p.Tags = parseStringArray(strings.TrimSpace(line[6:]))
case strings.HasPrefix(line, "sources: "):
    p.Sources = parseStringArray(strings.TrimSpace(line[9:]))
case strings.HasPrefix(line, "created: "):
    p.Created, _ = time.Parse("2006-01-02", strings.TrimSpace(line[9:]))
```

Add a `parseStringArray` helper next to `parseIntArray` — strips `[...]`, splits on `,`, trims spaces and matched-pair quotes from each element, drops empties.

- [ ] **Step 4: Run tests and confirm pass**

Run: `go test ./internal/wiki/ -run TestPage_ -v`
Expected: PASS — four subtests green.

Run: `go test ./...`
Expected: green (no callers populate the new fields yet, so the whole tree compiles unchanged).

- [ ] **Step 5: Commit**

```bash
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
feat(wiki): tags, sources, created frontmatter — round-trip in ParsePage/WritePage

Three new optional frontmatter keys spelled the way Obsidian's Dataview plugin
expects: tags as a flat bracketed string array, sources as the distinct list
of evidence source_file relative paths, created as a date-only YAYY-MM-DD
stamp. updated_at stays in RFC3339 for round-trip fidelity; an additional
updated: date-only twin is emitted alongside it. Pre-v1.1 page files (no new
keys) parse without error and round-trip preserves their absence.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase F — Obsidian-native output: wikilinks + index.md + log.md

### Task 7: `internal/wiki/obsidian.go` — pure helpers + tests

**Files:**
- Create: `internal/wiki/obsidian.go`
- Create: `internal/wiki/obsidian_test.go`

- [ ] **Step 1: Write failing tests**

Create `internal/wiki/obsidian_test.go`:

1. `TestRewriteBareReferencesAsWikilinks_KnownTitles` — `body = "See Trust Property for details. The Database Layer also matters."`, known titles `[]string{"Trust Property", "Database Layer", "Ingest Pipeline"}` → `"See [[Trust Property]] for details. The [[Database Layer]] also matters."`. Title not in known list left alone.
2. `TestRewriteBareReferencesAsWikilinks_Idempotent` — second pass with the same known titles is a byte-identical no-op.
3. `TestRewriteBareReferencesAsWikilinks_SkipsCodeFences` — body `"Use Trust Property\n```go\nTrust Property := struct{}\n```\n"` only rewrites the prose occurrence.
4. `TestRewriteBareReferencesAsWikilinks_SkipsInlineBackticks` — `"the `Trust Property` field"` is left alone.
5. `TestRewriteBareReferencesAsWikilinks_CaseSensitive` — title `"Trust Property"` does not match `"trust property"` in the body.
6. `TestRewriteBareReferencesAsWikilinks_WholeWord` — title `"DB"` does not match inside `"DBA"`.
7. `TestRegenerateIndex_EmptyWiki` — empty pages list still writes a valid `index.md` with frontmatter and a `## Pages (0)` header.
8. `TestRegenerateIndex_DeterministicByteIdentical` — calling twice with the same `[]db.PageRecord` produces byte-identical output (sort by title for stability; frontmatter `generated_at` uses a injectable clock helper so tests can pin it).
9. `TestRegenerateIndex_GroupsBySource` — pages with three distinct source URIs produce three sub-headers under `## By source`.
10. `TestAppendLog_RFC3339UTC` — append two entries; assert each line starts with an RFC3339 timestamp in UTC and contains the entry kind + payload.
11. `TestAppendLog_AppendOnly` — second append does not touch the first line.

- [ ] **Step 2: Run tests and confirm failure**

Run: `go test ./internal/wiki/ -run "TestRewriteBareReferencesAsWikilinks|TestRegenerateIndex|TestAppendLog" -v`
Expected: FAIL.

- [ ] **Step 3: Implement `internal/wiki/obsidian.go`**

```go
package wiki

import (
    "fmt"
    "os"
    "path/filepath"
    "regexp"
    "sort"
    "strings"
    "time"

    "github.com/mritunjaysharma394/llmwiki/internal/db"
)

// RewriteBareReferencesAsWikilinks does case-sensitive whole-word substitution
// of every knownTitle into [[Title]] form, skipping fenced code blocks and
// inline-backticked spans. Idempotent: a body that already contains [[Title]]
// is a no-op for that title.
func RewriteBareReferencesAsWikilinks(body string, knownTitles []string) string

type LogEntry struct {
    At      time.Time
    Kind    string // "ingest" | "ask" | "mcp.write_page"
    Payload string // free-form one-liner
}

// RegenerateIndex overwrites wikiDir/index.md with a deterministic listing of
// every page, sorted by title, then grouped by source URI. Frontmatter carries
// title=index, generated_at, generator=llmwiki. Idempotent for a fixed clock.
func RegenerateIndex(wikiDir string, pages []db.PageRecord, sources []db.Source, now time.Time) error

// AppendLog appends one timestamped line to wikiDir/log.md. RFC3339 UTC. Never
// rotated, never truncated by llmwiki. File is created on first call.
func AppendLog(wikiDir string, entry LogEntry) error
```

Wikilink rewriter implementation note — the conservative approach is to:
1. Split the body into in-fence and out-of-fence segments by tracking ``` ``` ``` opens/closes line-by-line.
2. Inside out-of-fence segments, run `regexp.MustCompile(`\b<escaped-title>\b`)` for each known title sorted by descending length (so `"Trust Property Validator"` is tried before `"Trust Property"`).
3. Skip matches whose containing run starts with `[[` and ends with `]]` (an existing wikilink) or is inside a backtick-pair on the same line.

Index format:

```markdown
---
title: index
generated_at: 2026-05-04T14:30:12Z
generator: llmwiki
---

# Wiki index

## Pages (N)

- [[Title A]] — updated 2026-05-04
- [[Title B]] — updated 2026-05-04

## By source

### <source URI>
- [[Title A]]
- [[Title B]]
```

- [ ] **Step 4: Run tests and confirm pass**

Run: `go test ./internal/wiki/ -run "TestRewriteBareReferencesAsWikilinks|TestRegenerateIndex|TestAppendLog" -v`
Expected: PASS — eleven subtests green.

- [ ] **Step 5: Commit**

```bash
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
feat(wiki): obsidian.go — RewriteBareReferencesAsWikilinks, RegenerateIndex, AppendLog

Three pure helpers that turn a directory of page files into a first-class
Obsidian vault without anyone building a UI. Wikilink rewriter is
conservative (case-sensitive, whole-word, skips fenced code blocks and
inline-backticked spans) and idempotent. Index regenerates deterministically
sorted by title and grouped by source URI; output is byte-identical for
identical inputs and an injected clock. Log is append-only RFC3339 UTC, one
line per significant event, never rotated by llmwiki.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 8: Wire wikilink rewrite + RegenerateIndex + AppendLog into ingest and ask

**Files:**
- Modify: `cmd/ingest.go`
- Modify: `cmd/ask.go`
- Modify: `internal/wiki/ops.go` (writePagesTool description nudge only; no validator change)

- [ ] **Step 1: Write failing integration tests**

Extend `cmd/ingest_test.go`:

1. `TestIngest_GeneratesIndexAndLog` — drive `runIngest` against a fixture (the same one `TestIngestSmall` uses); assert `cfg.Wiki.WikiDir/index.md` exists and lists every written page via `[[Title]]`; assert `cfg.Wiki.WikiDir/log.md` exists and ends with an `**ingest**` line.
2. `TestIngest_BodyContainsWikilinksWhenTitlesOverlap` — set up two ingests so the second source mentions a title from the first; assert at least one page body in the second batch contains `[[<title-from-first>]]`.

Extend `cmd/ask_test.go` (or create one) similarly:

3. `TestAsk_AppendsLog` — drive `runAsk` after a successful ingest; assert `log.md` ends with an `**ask**` line.

- [ ] **Step 2: Run tests and confirm failure**

Run: `go test ./cmd/ -run "TestIngest_GeneratesIndexAndLog|TestIngest_BodyContainsWikilinksWhenTitlesOverlap|TestAsk_AppendsLog" -v`
Expected: FAIL.

- [ ] **Step 3: Modify `internal/wiki/ops.go` writePagesTool description**

Add a sentence to the `writePagesTool.Description` (`internal/wiki/ops.go:21`):

> "When a page body references another page that already exists or is being created in this same call, prefer the `[[Page Title]]` wikilink syntax over bare prose."

No validator change. Wikilinks are body-quality, not trust-property.

- [ ] **Step 4: Wire into `cmd/ingest.go`**

Right before the `wiki.WritePage` call (`cmd/ingest.go:436`), pull `existingTitles` (already computed earlier in `runIngest`) plus the new pages' own titles, then:

```go
allTitles := append(existingTitles, allTitlesOf(allPages)...)
for i := range allPages {
    allPages[i].Body = wiki.RewriteBareReferencesAsWikilinks(allPages[i].Body, allTitles)
    allPages[i].Tags = []string{"llmwiki", "ingest"}
    allPages[i].Sources = distinctSourceFiles(allPages[i].Evidence)
    if allPages[i].Created.IsZero() { allPages[i].Created = time.Now().UTC() }
    // ... existing WritePage + UpsertPage + InsertEvidence ...
}
```

After the persist loop completes (after `// for i := range allPages { ... }`):

```go
allPageRecs, _ := database.AllPages()
allSources, _ := database.GetAllSources()
if err := wiki.RegenerateIndex(cfg.Wiki.WikiDir, allPageRecs, allSources, time.Now().UTC()); err != nil {
    fmt.Fprintf(os.Stderr, "WARN regenerating index.md: %v\n", err)
}
_ = wiki.AppendLog(cfg.Wiki.WikiDir, wiki.LogEntry{
    At: time.Now().UTC(), Kind: "ingest",
    Payload: fmt.Sprintf("%s → %d pages, %d evidence quotes, %d dropped", source, len(allPages), totalEvidence, droppedCount),
})
```

`distinctSourceFiles` is a one-liner local helper that walks `Evidence` and returns the unique non-empty `SourceFilePath` values.

- [ ] **Step 5: Wire into `cmd/ask.go`**

After the answer is written/streamed and (optionally) auto-archived:

```go
_ = wiki.AppendLog(cfg.Wiki.WikiDir, wiki.LogEntry{
    At: time.Now().UTC(), Kind: "ask",
    Payload: fmt.Sprintf("%q → %d chars, %d sources", question, len(answer), len(citedSources)),
})
```

No `RegenerateIndex` from `ask` — `ask` does not change the page set.

- [ ] **Step 6: Run tests and confirm pass**

Run: `go test ./...`
Expected: green.

- [ ] **Step 7: Commit**

```bash
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
feat(cmd): wire obsidian wikilinks + index + log into ingest and ask

After validation but before disk write, ingest rewrites bare title references
to [[wikilinks]] and stamps tags/sources/created on every page. After the
persist loop, RegenerateIndex overwrites .llmwiki/wiki/index.md against the
current DB state. AppendLog records one line per ingest run and per ask call.
writePagesTool's description nudges the model toward [[Page Title]] syntax,
but the validator is unchanged — wikilinks are a body-quality concern, not a
trust property. Pages without an existing title match still pass validation.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase G — MCP server

### Task 9: Add `mark3labs/mcp-go v0.50.0` dependency

**Files:**
- Modify: `go.mod`, `go.sum`

- [ ] **Step 1: Run `go get`**

```bash
go get github.com/mark3labs/mcp-go@v0.50.0
go mod tidy
```

- [ ] **Step 2: Verify build**

Run: `go build ./...`
Expected: green (no consumers yet — the dep is added but unused, which is fine for a dep-only commit).

- [ ] **Step 3: Commit**

```bash
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
deps: add github.com/mark3labs/mcp-go v0.50.0 (MIT) for MCP stdio server

Pinned tag, MIT-licensed, smallest defensible MCP library for Go. Considered
alternatives — hand-rolling JSON-RPC over stdio (~600 lines for zero benefit)
and Anthropic's MCP SDK (couples us to Anthropic, defeats the point) —
documented in the spec. Re-pin during the nightly cassette-refresh PR-review
pass if a breaking API change lands.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 10: `internal/mcp/server.go` + read-only handlers (`list_pages`, `read_page`, `lint`, `ask`)

**Files:**
- Create: `internal/mcp/server.go`
- Create: `internal/mcp/handlers.go`
- Create: `internal/mcp/server_test.go`

- [ ] **Step 1: Write failing tests**

Create `internal/mcp/server_test.go`:

1. `TestNewServer_RegistersAllSixTools` — build a server with stub deps, list registered tool names, assert the set is exactly `{ingest, ask, list_pages, read_page, write_page, lint}`.
2. `TestListPages_HappyPath` — seed three `db.PageRecord`s, call `list_pages` with `{limit: 50}`, assert three entries returned with `title`, `path`, `updated_at`, `source_files`.
3. `TestListPages_PrefixFilter` — `{prefix: "Database"}` returns only matching titles.
4. `TestReadPage_HappyPath` — seed one page with two evidence rows, call `read_page` with `{title: "..."}`, assert response has `title`, `body`, `evidence` (length 2), `links`, `source_files`.
5. `TestReadPage_NotFound` — `{title: "missing"}` returns a structured error `{code: "not_found"}`.
6. `TestLint_DelegatesToCmdRunLint` — call `lint` with no inputs, assert response is the contradiction-summary string. Use the same in-process pattern `runLint` is already structured for.
7. `TestAsk_HappyPath` — seed pages + cassette client; call `ask` with `{question: "..."}`, assert response has `answer` string + `sources` array of `{page_title, quote, source_file, line_start, line_end}` tuples.

The MCP test client uses `mcp-go`'s `client.NewInProcessClient(srv)` (or equivalent name in v0.50.0 — confirm exact API on first import).

- [ ] **Step 2: Run tests and confirm failure**

Run: `go test ./internal/mcp/ -v`
Expected: FAIL — package does not exist.

- [ ] **Step 3: Implement `internal/mcp/server.go` + `handlers.go`**

```go
// internal/mcp/server.go
package mcp

import (
    "github.com/mark3labs/mcp-go/mcp"
    "github.com/mark3labs/mcp-go/server"

    "github.com/mritunjaysharma394/llmwiki/internal/db"
    "github.com/mritunjaysharma394/llmwiki/internal/llm"
)

type Deps struct {
    Cfg    Config // a small struct holding wiki dir, raw dir, and a callback to invoke ingest cleanly
    DB     *db.DB
    Client llm.Client
}

func NewServer(d Deps) *server.MCPServer {
    s := server.NewMCPServer("llmwiki", "1.1.0")
    s.AddTool(listPagesTool(), listPagesHandler(d))
    s.AddTool(readPageTool(),  readPageHandler(d))
    s.AddTool(lintTool(),      lintHandler(d))
    s.AddTool(askTool(),       askHandler(d))
    s.AddTool(ingestTool(),    ingestHandler(d))    // implemented in Task 11
    s.AddTool(writePageTool(), writePageHandler(d)) // implemented in Task 11
    return s
}
```

Each handler is a thin adapter:

- `listPagesHandler` → `db.AllPages()` (or `SearchPages(prefix)` when `prefix` is non-empty), shape into the MCP response.
- `readPageHandler` → `db.GetPage(title)` + `db.GetEvidenceForPage(p.ID)` + a join against `source_files` to populate `source_files`.
- `lintHandler` → in v1, run the same logic as `runLint` but capture the printed output via a `bytes.Buffer`. Refactor `runLint` to a `LintResult{Stale, Contradictions}` struct in a small follow-up if the test forces it; otherwise capture stdout.
- `askHandler` → call `wiki.AnswerQuestion(ctx, client, question, contextPages)` (already exists at `internal/wiki/ops.go:277`) and shape the result. No streaming over MCP.

Structured errors use a small helper:

```go
func errorResult(code, msg string, extra map[string]any) *mcp.CallToolResult {
    payload := map[string]any{"code": code, "message": msg}
    for k, v := range extra { payload[k] = v }
    b, _ := json.Marshal(payload)
    return mcp.NewToolResultError(string(b))
}
```

- [ ] **Step 4: Run tests and confirm pass**

Run: `go test ./internal/mcp/ -v`
Expected: PASS — seven subtests green.

- [ ] **Step 5: Commit**

```bash
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
feat(mcp): server skeleton + four read-only tools (list_pages, read_page, lint, ask)

internal/mcp wraps github.com/mark3labs/mcp-go's MCPServer and registers six
tools by name; this commit lands the four read-only ones. Each handler is a
thin adapter delegating to existing internal packages — db.GetPage,
db.GetEvidenceForPage, wiki.AnswerQuestion, the runLint code path. Structured
errors use a tiny helper that JSON-encodes {code, message, ...extra} so MCP
clients can render machine-readable failure modes. write_page and ingest are
the next commit.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 11: `write_page` (load-bearing) + `ingest` handlers

**Files:**
- Modify: `internal/mcp/handlers.go`
- Modify: `internal/mcp/server_test.go`

- [ ] **Step 1: Write failing tests**

Append to `internal/mcp/server_test.go`:

1. `TestWritePage_ValidEvidenceWritesPage` — seed an ingested source with content `"the quick brown fox jumps"`. Call `write_page` with `{title: "Foo", body: "...", evidence: [{quote: "quick brown fox", source_file: "<seeded path>"}]}`. Assert: page exists on disk, page row in DB, evidence rows linked, `index.md` regenerated, `log.md` got an `mcp.write_page` line.
2. `TestWritePage_InvalidEvidenceReturnsStructuredError` — same setup but `evidence: [{quote: "this is not in the source", source_file: "<seeded path>"}]`. Assert: structured error with `code: "evidence_invalid"`, `dropped: [{quote, reason}]`, `hint: "..."`. Assert NO disk write, NO DB row, NO `log.md` line. (`log.md` only records validated, written pages — risk mitigation from spec.)
3. `TestWritePage_TitleCollisionReturnsStructuredError` — write a page successfully, then call `write_page` again with the same title. Assert: structured error `{code: "title_exists", existing_path: "..."}`. Resolves open question 3.
4. `TestWritePage_RequiresAtLeastOneEvidenceEntry` — `evidence: []` → structured error `{code: "evidence_required"}`.
5. `TestWritePage_SourceMustBeIngested` — `source_file` references a path with no `source_files` row → structured error `{code: "source_not_ingested", source_file: "..."}`.
6. `TestIngest_DelegatesToRunIngest` — call `ingest` with `{source: "<temp file>"}`; assert response has `pages_written`, `evidence_quotes`, `dropped_pages` integers; assert the pages reach disk.
7. `TestIngest_ForceFlag` — second call with `force: true` re-ingests despite unchanged hash.

- [ ] **Step 2: Run tests and confirm failure**

Run: `go test ./internal/mcp/ -run "TestWritePage|TestIngest" -v`
Expected: FAIL.

- [ ] **Step 3: Implement `writePageHandler` + `ingestHandler`**

`writePageHandler` flow:
1. Parse `{title, body, evidence: [{quote, source_file}], links}`.
2. If `db.GetPage(title) != nil` → `errorResult("title_exists", ..., {"existing_path": p.Path})`.
3. If `len(evidence) == 0` → `errorResult("evidence_required", ...)`.
4. Resolve every `source_file` against `db.SourceFile.RelativePath`; any unresolved → `errorResult("source_not_ingested", ..., {"source_file": ...})`.
5. Build a synthetic `[]ingest.SourceFile` from the resolved rows (read content from `cfg.Wiki.RawDir/<src>/<rel-path>` or re-read the source file if needed).
6. Call `wiki.ValidateAndAttachEvidence(pages, files)` — same function `IngestSourceFilesToPages` calls today. If any quote drops AND no quote remains valid → `errorResult("evidence_invalid", ..., {"dropped": [...], "hint": "quotes must be byte-exact substrings of an already-ingested source_file"})`.
7. On success: rewrite wikilinks, stamp tags/sources/created, `wiki.WritePage`, `db.UpsertPage`, `db.InsertEvidence`, `wiki.RegenerateIndex`, `wiki.AppendLog{Kind: "mcp.write_page"}`.

`ingestHandler` flow: call into the same code path `cmd.runIngest` exposes, but as a function-callable refactored target. To avoid circular imports (`internal/mcp` cannot import `cmd/`), extract the body of `runIngest` into a new `func IngestSource(ctx, cfg, db, client, source string, opts IngestOptions) (IngestResult, error)` in `internal/wiki/ops.go` (or a new `internal/wiki/ingest_runner.go`). `cmd/ingest.go:runIngest` becomes a thin cobra wrapper around it. This refactor is part of this task's commit.

- [ ] **Step 4: Run tests and confirm pass**

Run: `go test ./...`
Expected: green.

- [ ] **Step 5: Commit**

```bash
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
feat(mcp): write_page + ingest handlers — trust validation enforced over MCP

write_page is the load-bearing tool. Every MCP-driven page write goes through
wiki.ValidateAndAttachEvidence, the same pipeline cmd/ingest uses. Quotes
that don't byte-exactly substring-match the named source_file are rejected
with a structured {code: "evidence_invalid", dropped: [...], hint: ...}
error and NEVER reach disk. Title collisions return code: "title_exists" and
force the agent to call read_page + supersede via links (resolves spec open
question 3). Sources that haven't been ingested return code:
"source_not_ingested" so the agent can call ingest first. Failed write_page
calls go to stderr only; log.md records validated, written pages exclusively
(spec risk mitigation). Refactors runIngest's body into a callable
wiki.IngestSource so the MCP ingest handler can drive it without importing
cmd/.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 12: `cmd/mcp.go` cobra command + `server.ServeStdio`

**Files:**
- Create: `cmd/mcp.go`
- Create: `cmd/mcp_test.go`
- Modify: `cmd/root.go` (`rootCmd.AddCommand(mcpCmd)`)

- [ ] **Step 1: Write failing test**

`cmd/mcp_test.go`:

1. `TestMCPCommand_StartsAndExits` — start the cobra command in a goroutine with a closed-stdin pipe; assert `err == nil` (clean shutdown when stdin EOFs) and the command exited within 1s.
2. `TestMCPCommand_InitFailureExitsNonZero` — set `LLMWIKI_DIR` to a directory with no `.llmwiki/config.toml`; assert the command returns a non-nil error so `Execute()` exits non-zero (per spec — "exits non-zero on fatal startup errors so MCP clients show a useful error").

- [ ] **Step 2: Run test and confirm failure**

Run: `go test ./cmd/ -run TestMCPCommand -v`
Expected: FAIL — command does not exist.

- [ ] **Step 3: Implement `cmd/mcp.go`**

```go
package cmd

import (
    "github.com/mark3labs/mcp-go/server"
    "github.com/mritunjaysharma394/llmwiki/internal/mcp"
    "github.com/spf13/cobra"
)

var mcpCmd = &cobra.Command{
    Use:   "mcp",
    Short: "Run the MCP stdio server",
    Long:  `Run the MCP stdio server. Reads .llmwiki/config.toml from the current working directory.

The server exposes ingest, ask, list_pages, read_page, write_page and lint as
MCP tools. write_page enforces the same evidence-quote validation as
'llmwiki ingest'. Logs go to stderr (stdout is the JSON-RPC channel).`,
    RunE: runMCP,
}

func runMCP(cmd *cobra.Command, args []string) error {
    srv := mcp.NewServer(mcp.Deps{Cfg: toMCPConfig(cfg), DB: database, Client: llmClient})
    return server.ServeStdio(srv)
}
```

Handle `LLMWIKI_DIR` by `os.Chdir`-ing to it before the standard `loadConfig` runs (or by extending the cobra `PersistentPreRunE` switch in `root.go` to honour `LLMWIKI_DIR` for the `mcp` command before falling through to `loadConfig`).

`server.ServeStdio` blocks until stdin closes (EOF) or the context cancels. Wire `signal.Notify(... SIGINT SIGTERM ...)` to a `cancel()` so Ctrl-C is clean.

- [ ] **Step 4: Run test and confirm pass**

Run: `go test ./...`
Expected: green.

- [ ] **Step 5: Commit**

```bash
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
feat(cmd): mcp subcommand — server.ServeStdio with signal-handled shutdown

llmwiki mcp runs an MCP stdio server reading .llmwiki/config.toml from cwd
(or LLMWIKI_DIR when set). No flags in v1. Logs to stderr per the MCP spec
(stdout is the JSON-RPC channel). Exits non-zero on fatal startup errors so
MCP clients render the failure usefully. SIGINT/SIGTERM trigger a clean
context cancel; stdin EOF also exits cleanly.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 13: `TestMCPWritePageRoundtrip` cassette test

**Files:**
- Create: `internal/mcp/integration_test.go`
- Create: `internal/llm/testdata/cassettes/TestMCPWritePageRoundtrip__*.json`

- [ ] **Step 1: Write failing test**

```go
func TestMCPWritePageRoundtrip(t *testing.T) {
    if _, err := os.Stat("../llm/testdata/cassettes/TestMCPWritePageRoundtrip__001.json"); os.IsNotExist(err) {
        t.Skip("cassette not recorded; run with LLMWIKI_RECORD=1 ANTHROPIC_API_KEY=... to record")
    }
    setEnv(t, "LLMWIKI_CASSETTE", "TestMCPWritePageRoundtrip")
    setEnv(t, "ANTHROPIC_API_KEY", "test-key-for-replay")

    // 1. Spin up an MCP server in-process.
    // 2. Use mcp-go's in-process client to call:
    //      ingest(source: <temp synthetic source>)
    //    Assert pages_written > 0.
    // 3. list_pages(limit: 50) — assert the just-written titles are present.
    // 4. read_page(title: <one of them>) — assert evidence array non-empty.
    // 5. write_page(title: "New Page", body: "...", evidence: [{quote: <substring of source>, source_file: <path>}])
    //    Assert success: page on disk, log.md got the mcp.write_page line.
    // 6. write_page(title: "Bad Page", body: "...", evidence: [{quote: "this string does NOT appear", source_file: <path>}])
    //    Assert structured error code: "evidence_invalid", page NOT on disk.
}
```

- [ ] **Step 2: Record the cassette**

```bash
export ANTHROPIC_API_KEY=...
LLMWIKI_RECORD=1 go test ./internal/mcp/ -run TestMCPWritePageRoundtrip -v
```

- [ ] **Step 3: Run replay and confirm pass**

Run: `unset ANTHROPIC_API_KEY && go test ./internal/mcp/ -run TestMCPWritePageRoundtrip -v`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
test(mcp): TestMCPWritePageRoundtrip — full ingest→list→read→write→reject loop

Drives the MCP server in-process via mark3labs/mcp-go's in-process client.
Asserts the validator's rejection of an unverified quote still returns a
structured error to the MCP client (no silent disk write of bad content)
exactly the way it does for cmd/ingest. The cassette wraps the upstream
Anthropic client so the test runs in CI without an API key.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase H — README + CHANGELOG + tag

### Task 14: README rewrite — Gemini-first onboarding + MCP + Obsidian sections

**Files:**
- Modify: `README.md`

- [ ] **Step 1: Write the new README**

Sections to add or rewrite (the existing structure from sub-project 4 stays largely intact; we layer in three new things):

1. **Quickstart now leads with Gemini.** First example block uses `llmwiki init` (defaults to gemini), `export GEMINI_API_KEY=...`, `llmwiki ingest ./README.md`. The Anthropic block becomes the "if you already have a Pro subscription, drive via MCP" section below.
2. **New "Use your Claude subscription via MCP" section** with the JSON config snippet from the spec for Claude Desktop and Claude Code, plus a one-paragraph explainer of `write_page`'s trust validation guarantee.
3. **New "Use Obsidian as the UI" section** — one paragraph: open `.llmwiki/wiki/` as an Obsidian vault, no plugin needed; backlinks, graph view, search, Dataview frontmatter all work out of the box. Include a Dataview query snippet.
4. **Provider matrix table** — Gemini (free, recommended), Anthropic Pro (via MCP, recommended for power users), OpenAI-compatible (Groq / OpenRouter / Together / Cerebras / Mistral La Plateforme; cheap or free tiers), Ollama (offline). One row per provider, columns for cost / model class / setup steps / caveats.
5. **Trust property section** keeps its current text plus one new sentence: "A wiki ingested with Gemini Flash, OpenRouter free-tier, or Ollama may contain fewer pages than the same source ingested with Haiku, but every page that lands in the wiki passes the same evidence check. Switching to a cheaper model produces a sparser wiki, never a more wrong one." (verbatim from the spec).

- [ ] **Step 2: Commit**

```bash
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
docs(readme): v1.1 rewrite — Gemini-first onboarding, MCP section, Obsidian section

Quickstart leads with `llmwiki init` defaulting to Gemini's free tier (no
credit card, 1M context). New "Use your Claude subscription via MCP" section
documents the Claude Desktop / Claude Code config and explains write_page's
trust-validation guarantee. New "Use Obsidian as the UI" section points the
user at .llmwiki/wiki/ as a vault — backlinks, graph view, Dataview queries
work out of the box. Provider matrix table covers Gemini / Anthropic Pro /
OpenAI-compat / Ollama. Trust-property section adds the spec's
"sparser-not-wronger" sentence verbatim.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 15: CHANGELOG `[1.1.0]` entry

**Files:**
- Modify: `CHANGELOG.md`

- [ ] **Step 1: Write the entry**

```markdown
## [1.1.0] — 2026-05-04

### Added
- `llmwiki mcp` — MCP stdio server exposing six tools (`ingest`, `ask`,
  `list_pages`, `read_page`, `write_page`, `lint`). `write_page` runs every
  proposed page through the same evidence-validation pipeline as
  `llmwiki ingest`; quotes that don't substring-match the named source are
  rejected with a structured error.
- Google Gemini provider (`--provider gemini`, default `gemini-2.0-flash`).
  Free tier, 1M context, no credit card. Now the recommended onboarding
  default.
- Generic OpenAI-compatible provider (`--provider openai-compatible`).
  Configurable `base_url` + `api_key_env` + `default_model`. Tested against
  Groq, OpenRouter, Together, Cerebras, and Mistral La Plateforme.
- Obsidian-native disk layout: `[[wikilinks]]` between page bodies, an
  auto-regenerated `.llmwiki/wiki/index.md` hub, an append-only
  `.llmwiki/wiki/log.md` chronicle, and `tags` / `sources` / `created`
  frontmatter keys spelled the way Obsidian's Dataview plugin expects.
- `[providers]` config block with per-provider knobs
  (`base_url`, `api_key_env`, `default_model`, `url`).

### Changed
- `llmwiki init` walkthrough recommends Gemini first (was Anthropic).
  Existing users with `provider = "anthropic"` keep working unchanged.
- `writePagesTool` description nudges the model toward `[[Page Title]]`
  syntax for cross-page references.

### Notes
- No schema migration. `PRAGMA user_version` stays at 3.
- Cheap-provider wikis end up sparser than Haiku wikis but never less
  honest — the validator drops unverified quotes on every provider equally.
```

Move the existing `## [Unreleased]` section's contents (if any) into `[1.1.0]` and leave a fresh empty `[Unreleased]` at the top.

- [ ] **Step 2: Commit**

```bash
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
docs(changelog): [1.1.0] — MCP server, Obsidian output, cheap providers

Three pillars per the spec: MCP stdio server (six tools, write_page enforces
evidence validation), Gemini + OpenAI-compatible providers (Gemini becomes
the default onboarding path), Obsidian-native output (wikilinks, index.md,
log.md, Dataview frontmatter). No schema migration — user_version stays at 3.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 16: Final verification matrix — full re-run of spec's verification block

**Files:** none (verification only)

Run, top to bottom, the verification block from the spec
(`docs/superpowers/specs/2026-05-04-mcp-and-cheap-providers-design.md`, the
`## Verification` section). Each invocation either matches the expected
behaviour described there or the implementer fixes the gap and re-runs.

- [ ] **Step 1: `go test ./...` is green in replay mode** (no API keys exported).
- [ ] **Step 2: `go build ./... && go vet ./...` clean.**
- [ ] **Step 3: Provider walkthrough — Gemini default**
  ```bash
  unset ANTHROPIC_API_KEY GEMINI_API_KEY
  rm -rf /tmp/test-wiki && mkdir /tmp/test-wiki && cd /tmp/test-wiki
  llmwiki init
  # Expect: walkthrough recommends Gemini, prints AI Studio URL,
  # exits 1 with UserError pointing at GEMINI_API_KEY.
  ```
- [ ] **Step 4: Gemini ingest end-to-end**
  ```bash
  export GEMINI_API_KEY=...
  llmwiki ingest README.md
  # Expect: validator drops 0–N pages depending on Gemini Flash quote-fidelity.
  # Every surviving page has evidence in frontmatter.
  ```
- [ ] **Step 5: Obsidian-native output present**
  ```bash
  ls .llmwiki/wiki/             # Expect: index.md, log.md, *.md pages.
  grep -l "\[\[" .llmwiki/wiki/*.md   # Expect: ≥1 page body with [[wikilinks]].
  head -10 .llmwiki/wiki/index.md     # Expect: title=index, generator=llmwiki.
  tail -3 .llmwiki/wiki/log.md         # Expect: RFC3339 ingest+ask lines.
  ```
- [ ] **Step 6: OpenAI-compat ingest**
  ```bash
  llmwiki init --provider openai-compatible --force
  # Edit [providers.openai_compat] base_url / api_key_env per docstring.
  export OPENROUTER_API_KEY=...
  llmwiki ingest README.md
  # Expect: validator drops more pages than Gemini, but every survivor passes.
  ```
- [ ] **Step 7: MCP smoke**
  ```bash
  llmwiki mcp < /dev/null
  # Expect: clean exit when stdin EOFs. Logs on stderr.
  go test ./internal/mcp/... -run TestMCPWritePageRoundtrip
  # Expect: pass.
  ```
- [ ] **Step 8: Status counters unchanged** (`llmwiki status` shows existing fields; v1.1 adds no new schema or counters).

- [ ] **Step 9: No-op verification commit**

Nothing to commit unless a fix was made. If a fix was made, commit it under the relevant phase's task number, not under this verification step.

---

### Task 17: Tag `v1.1.0-rc.1` locally (no push)

**Files:** none (tag only)

- [ ] **Step 1: Tag**

```bash
git -c commit.gpgsign=false tag -a v1.1.0-rc.1 -m "$(cat <<'EOF'
v1.1.0-rc.1 — MCP server, Obsidian output, cheap providers

Sub-project 5 complete. MCP stdio server (six tools, write_page enforces
evidence validation), Gemini + OpenAI-compatible providers (Gemini default
for onboarding), Obsidian-native output (wikilinks, index.md, log.md,
Dataview frontmatter). No schema migration. Promotion to v1.1.0 is a
post-launch follow-up matching the v1.0.0-rc.1 → v1.0.0 pattern from
sub-project 4.
EOF
)"
```

- [ ] **Step 2: Verify**

Run: `git tag -l "v1.1*"`
Expected: prints `v1.1.0-rc.1`.

Do **not** `git push --tags`. Promotion to a real release is a manual step matching sub-project 4's pattern.

---

## Done criteria

- All 17 tasks have a green checkbox.
- `go test ./...` is green in replay mode (no API keys required).
- `go build ./... && go vet ./...` clean.
- A fresh `mkdir wiki && cd wiki && llmwiki init && llmwiki ingest <source>` walks through with `GEMINI_API_KEY` and produces a wiki directory with `index.md`, `log.md`, and per-page Markdown files containing `[[wikilinks]]`.
- `llmwiki mcp` starts cleanly under an MCP client (Claude Desktop or Claude Code), lists six tools, and `write_page` rejects an unverified quote with a structured error visible in the client UI.
- The tag `v1.1.0-rc.1` exists locally.
- The README leads with Gemini, documents MCP, and points the user at Obsidian as the UI.
