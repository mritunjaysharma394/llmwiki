package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	mcpc "github.com/mark3labs/mcp-go/client"
	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"github.com/mritunjaysharma394/llmwiki/internal/db"
	"github.com/mritunjaysharma394/llmwiki/internal/llm"
)

// TestMCPWritePageRoundtrip drives the full ingest -> list_pages ->
// read_page -> write_page (success) -> write_page (rejection) loop
// in-process via mark3labs/mcp-go's NewInProcessClient. The cassette
// wraps the upstream Anthropic client (the same way TestIngestGemini
// wraps Gemini) so the test runs in CI without an API key.
//
// Skips when the cassette file is absent — the test ships now,
// recording happens out-of-band via:
//
//	LLMWIKI_RECORD=1 ANTHROPIC_API_KEY=... \
//	  go test ./internal/mcp/ -run TestMCPWritePageRoundtrip -v
//
// or via the cassette-refresh CI workflow once it's wired for this
// fixture. This is the same skip-on-missing-cassette pattern Phase D
// established for TestIngestGemini / TestIngestOpenAICompat.
//
// Once the cassette lands, replay verifies that:
//
//  1. ingest produced at least one page;
//  2. list_pages surfaces the just-written titles;
//  3. read_page returns evidence-bearing content for one of them;
//  4. write_page with a valid quote succeeds — page on disk, log.md
//     gains an mcp.write_page line;
//  5. write_page with a fabricated quote returns a structured
//     evidence_invalid error and NO page hits disk.
//
// Step 5 is the load-bearing assertion: the validator's rejection of an
// unverified quote MUST surface to the MCP client as a structured error
// (no silent disk write of bad content), exactly the way it does for
// cmd/ingest.
func TestMCPWritePageRoundtrip(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cassette test in -short mode")
	}
	cassettePath := filepath.Join("..", "llm", "testdata", "cassettes", "TestMCPWritePageRoundtrip__001.json")
	if _, err := os.Stat(cassettePath); os.IsNotExist(err) {
		t.Skip("cassette not recorded; run with LLMWIKI_RECORD=1 ANTHROPIC_API_KEY=... to record")
	}
	t.Setenv("LLMWIKI_CASSETTE", "TestMCPWritePageRoundtrip")
	t.Setenv("ANTHROPIC_API_KEY", "test-key-for-replay")

	// 1. Spin up an MCP server in-process backed by a real cassette-
	//    wrapped Anthropic client. The cassette layer wraps the parsed
	//    CompleteStructured map[string]any output of the underlying
	//    client (same as TestIngestGemini), so the wrapper transparently
	//    replays whatever was recorded against the live API.
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "wiki.db")
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	wikiDir := filepath.Join(dir, "wiki")
	if err := os.MkdirAll(wikiDir, 0755); err != nil {
		t.Fatalf("mkdir wiki: %v", err)
	}

	// Anchor the cassette dir for the CassetteClient: it reads from the
	// path the integration setup (loadConfig in the cmd test path)
	// hardcodes — internal/llm/testdata/cassettes relative to cwd. We
	// chdir into the package's own cwd up two levels (the repo root
	// where the canonical cassette dir lives) so the lookup resolves.
	origCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	repoRoot := filepath.Join(origCwd, "..", "..")
	t.Cleanup(func() { _ = os.Chdir(origCwd) })
	if err := os.Chdir(repoRoot); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	base := llm.NewAnthropicClient("claude-haiku-4-5")
	client := llm.NewCassetteClient(base, "internal/llm/testdata/cassettes",
		"TestMCPWritePageRoundtrip", llm.ModeReplay)

	deps := Deps{
		Cfg:    Config{WikiDir: wikiDir, RawDir: filepath.Join(dir, "raw"), DBPath: dbPath},
		DB:     d,
		Client: client,
	}
	srv := NewServer(deps)

	c, err := mcpc.NewInProcessClient(srv)
	if err != nil {
		t.Fatalf("NewInProcessClient: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("client.Start: %v", err)
	}
	initReq := mcpgo.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcpgo.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcpgo.Implementation{Name: "test", Version: "0.0.0"}
	if _, err := c.Initialize(ctx, initReq); err != nil {
		t.Fatalf("client.Initialize: %v", err)
	}

	// 2. Synthetic source. Tiny, repeatable, easy to construct evidence
	//    quotes from. Same shape TestIngestGemini uses.
	srcPath := filepath.Join(dir, "source.md")
	srcBody := "Goroutines are lightweight threads of execution managed by the Go runtime.\nThe `go` keyword starts a goroutine.\nGoroutines communicate via channels.\n"
	if err := os.WriteFile(srcPath, []byte(srcBody), 0644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	res, text := callToolRT(t, c, "ingest", map[string]any{"source": srcPath})
	if res.IsError {
		t.Fatalf("ingest returned error: %s", text)
	}
	var ing map[string]any
	_ = json.Unmarshal([]byte(text), &ing)
	pw, _ := ing["pages_written"].(float64)
	if pw <= 0 {
		t.Fatalf("ingest pages_written = %v, want > 0 (raw=%s)", ing["pages_written"], text)
	}

	// 3. list_pages includes at least one page from the ingest.
	_, listText := callToolRT(t, c, "list_pages", map[string]any{"limit": 50.0})
	var ls struct {
		Pages []map[string]any `json:"pages"`
	}
	if err := json.Unmarshal([]byte(listText), &ls); err != nil {
		t.Fatalf("list_pages unmarshal: %v\nraw=%s", err, listText)
	}
	if len(ls.Pages) == 0 {
		t.Fatalf("list_pages returned no pages; raw=%s", listText)
	}
	firstTitle, _ := ls.Pages[0]["title"].(string)

	// 4. read_page on one of them returns evidence-bearing content.
	_, readText := callToolRT(t, c, "read_page", map[string]any{"title": firstTitle})
	var pg map[string]any
	if err := json.Unmarshal([]byte(readText), &pg); err != nil {
		t.Fatalf("read_page unmarshal: %v\nraw=%s", err, readText)
	}
	ev, _ := pg["evidence"].([]any)
	if len(ev) == 0 {
		t.Errorf("read_page evidence empty for %q", firstTitle)
	}

	// 5. write_page success — quote is a verbatim substring of source.
	res, writeText := callToolRT(t, c, "write_page", map[string]any{
		"title": "Goroutines (manual)",
		"body":  "A short note on goroutines.",
		"evidence": []any{
			map[string]any{"quote": "Goroutines are lightweight", "source_file": srcPath},
		},
	})
	if res.IsError {
		t.Fatalf("write_page success path returned error: %s", writeText)
	}
	if _, err := os.Stat(filepath.Join(wikiDir, "Goroutines (manual).md")); err != nil {
		t.Errorf("expected page on disk: %v", err)
	}
	logBody, _ := os.ReadFile(filepath.Join(wikiDir, "log.md"))
	if !strings.Contains(string(logBody), "mcp.write_page") {
		t.Errorf("log.md missing mcp.write_page line: %s", logBody)
	}

	// 6. write_page rejection — quote is NOT a substring of source.
	res, badText := callToolRT(t, c, "write_page", map[string]any{
		"title": "Bad Page",
		"body":  "Body referencing a fabricated claim.",
		"evidence": []any{
			map[string]any{"quote": "this string does NOT appear", "source_file": srcPath},
		},
	})
	if !res.IsError {
		t.Fatalf("write_page rejection path returned success: %s", badText)
	}
	var bad map[string]any
	if err := json.Unmarshal([]byte(badText), &bad); err != nil {
		t.Fatalf("rejection error not JSON: %v\nraw=%s", err, badText)
	}
	if bad["code"] != "evidence_invalid" {
		t.Errorf("code = %v, want evidence_invalid", bad["code"])
	}
	if _, err := os.Stat(filepath.Join(wikiDir, "Bad Page.md")); err == nil {
		t.Error("Bad Page should NOT have been written to disk")
	}
}

// callToolRT is a duplicate of server_test.go's callTool that takes a
// real client (not the test-helper *mcpc.Client typed argument from
// the in-process flow). Lets the integration test live next to the
// unit tests without exporting helpers.
func callToolRT(t *testing.T, c *mcpc.Client, name string, args map[string]any) (*mcpgo.CallToolResult, string) {
	t.Helper()
	req := mcpgo.CallToolRequest{}
	req.Params.Name = name
	req.Params.Arguments = args
	res, err := c.CallTool(context.Background(), req)
	if err != nil {
		t.Fatalf("CallTool %s: %v", name, err)
	}
	if res == nil {
		t.Fatalf("CallTool %s: nil result", name)
	}
	for _, content := range res.Content {
		if tc, ok := content.(mcpgo.TextContent); ok {
			return res, tc.Text
		}
	}
	return res, ""
}

