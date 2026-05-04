package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	mcpc "github.com/mark3labs/mcp-go/client"
	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"github.com/mritunjaysharma394/llmwiki/internal/db"
	"github.com/mritunjaysharma394/llmwiki/internal/llm"
	"github.com/mritunjaysharma394/llmwiki/internal/wiki"
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

// TestMCPPromoteAnswerRoundtrip drives the v1.2 promote_answer tool
// in-process via mark3labs/mcp-go's NewInProcessClient through the full
// ingest → ask → promote_answer → read_page loop. The cassette wraps
// the upstream Gemini Flash client (sister to TestMCPWritePageRoundtrip,
// which targets Anthropic) so the test runs in CI without an API key.
//
// Skips when the cassette file is absent — the test ships now,
// recording happens out-of-band via:
//
//	LLMWIKI_RECORD=1 GEMINI_API_KEY=... \
//	  go test ./internal/mcp/ -run TestMCPPromoteAnswerRoundtrip -v
//
// Once the cassette lands, replay verifies that:
//
//  1. mcp.ingest produced at least one page;
//  2. mcp.ask returned an answer + sources payload;
//  3. mcp.promote_answer with a hand-authored saved-answer fixture
//     populated from the ask response succeeds — page lands, response
//     payload carries title / path / evidence_quotes /
//     retro_linked_pages keys;
//  4. mcp.read_page on the new title returns the answer body and
//     evidence array is non-empty;
//  5. a second mcp.promote_answer call against the same answer file
//     returns the structured "title_exists" error with existing_path.
//
// Step 5 is the load-bearing assertion: the title_exists structured
// error must surface to the MCP client (no silent overwrite of the
// canonical page), exactly the way evidence_invalid does for the
// trust validator.
func TestMCPPromoteAnswerRoundtrip(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cassette test in -short mode")
	}
	cassettePath := filepath.Join("..", "llm", "testdata", "cassettes", "TestMCPPromoteAnswerRoundtrip__001.json")
	if _, err := os.Stat(cassettePath); os.IsNotExist(err) && os.Getenv("LLMWIKI_RECORD") == "" {
		t.Skip("cassette not recorded; run with LLMWIKI_RECORD=1 GEMINI_API_KEY=... to record")
	}
	if os.Getenv("LLMWIKI_RECORD") != "" && os.Getenv("GEMINI_API_KEY") == "" {
		t.Fatal("LLMWIKI_RECORD set but GEMINI_API_KEY missing")
	}
	t.Setenv("LLMWIKI_CASSETTE", "TestMCPPromoteAnswerRoundtrip")
	if os.Getenv("GEMINI_API_KEY") == "" {
		t.Setenv("GEMINI_API_KEY", "test-key-for-replay")
	}

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "wiki.db")
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	wikiDir := filepath.Join(dir, "wiki")
	answersDir := filepath.Join(dir, "answers")
	for _, sub := range []string{wikiDir, answersDir} {
		if err := os.MkdirAll(sub, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}

	// Anchor the cassette dir at the repo root the same way
	// TestMCPWritePageRoundtrip does — chdir up two levels so
	// "internal/llm/testdata/cassettes" resolves.
	origCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	repoRoot := filepath.Join(origCwd, "..", "..")
	t.Cleanup(func() { _ = os.Chdir(origCwd) })
	if err := os.Chdir(repoRoot); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	// Build the cassette client. Recording wraps Gemini Flash (the
	// resolved Q2 in the plan calls for Gemini Flash on v1.2 cassettes
	// to keep refresh costs at zero); replay reads from the recorded
	// segments under the canonical cassette dir.
	var base llm.Client
	mode := llm.ModeReplay
	if os.Getenv("LLMWIKI_RECORD") != "" {
		base = llm.NewGeminiClient("gemini-2.0-flash")
		mode = llm.ModeRecord
	}
	client := llm.NewCassetteClient(base, "internal/llm/testdata/cassettes",
		"TestMCPPromoteAnswerRoundtrip", mode)

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

	// Step 1: ingest a synthetic source.
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

	// Step 2: ask via MCP. The handler returns {answer, sources}; the
	// MCP server doesn't write a saved-answer file (that's cmd/ask's
	// job), so we hand-author one populated from the captured response
	// before invoking promote_answer.
	res, askText := callToolRT(t, c, "ask", map[string]any{"question": "what are goroutines?"})
	if res.IsError {
		t.Fatalf("ask returned error: %s", askText)
	}
	var askResp struct {
		Answer  string `json:"answer"`
		Sources []struct {
			PageTitle  string `json:"page_title"`
			Quote      string `json:"quote"`
			SourceFile string `json:"source_file"`
			LineStart  int    `json:"line_start"`
			LineEnd    int    `json:"line_end"`
		} `json:"sources"`
	}
	if err := json.Unmarshal([]byte(askText), &askResp); err != nil {
		t.Fatalf("ask unmarshal: %v\nraw=%s", err, askText)
	}
	if askResp.Answer == "" {
		t.Fatal("ask returned empty answer")
	}
	if len(askResp.Sources) == 0 {
		t.Fatal("ask returned no sources")
	}

	// Step 3: hand-author a saved-answer file from the ask response.
	// FormatSavedAnswer is the same formatter cmd/ask.go uses — we
	// reconstruct an equivalent file under the test's answers dir.
	pages := []wiki.Page{}
	pageEv := map[string][]wiki.Evidence{}
	pageOrder := []string{}
	for _, s := range askResp.Sources {
		if _, seen := pageEv[s.PageTitle]; !seen {
			pageOrder = append(pageOrder, s.PageTitle)
		}
		pageEv[s.PageTitle] = append(pageEv[s.PageTitle], wiki.Evidence{
			Quote:          s.Quote,
			LineStart:      s.LineStart,
			LineEnd:        s.LineEnd,
			SourceFilePath: s.SourceFile,
		})
	}
	for _, title := range pageOrder {
		pages = append(pages, wiki.Page{Title: title, Evidence: pageEv[title]})
	}
	now := time.Now().UTC()
	body := wiki.FormatSavedAnswer(wiki.SavedAnswerInput{
		Question: "what are goroutines?",
		Answer:   askResp.Answer,
		Model:    "gemini-2.0-flash",
		Pages:    pages,
		At:       now,
	})
	answerName := fmt.Sprintf("%s-what-are-goroutines.md", now.Format("2006-01-02-150405"))
	answerPath := filepath.Join(answersDir, answerName)
	if err := os.WriteFile(answerPath, []byte(body), 0644); err != nil {
		t.Fatalf("write answer file: %v", err)
	}

	// Step 4: promote_answer.
	res, promoteText := callToolRT(t, c, "promote_answer", map[string]any{
		"answer_path": answerPath,
		"title":       "Goroutines (MCP Promoted)",
	})
	if res.IsError {
		t.Fatalf("promote_answer returned error: %s", promoteText)
	}
	var promoteResp map[string]any
	if err := json.Unmarshal([]byte(promoteText), &promoteResp); err != nil {
		t.Fatalf("promote_answer unmarshal: %v\nraw=%s", err, promoteText)
	}
	for _, key := range []string{"title", "path", "evidence_quotes", "retro_linked_pages"} {
		if _, ok := promoteResp[key]; !ok {
			t.Errorf("promote_answer response missing key %q; raw=%s", key, promoteText)
		}
	}
	evCount, _ := promoteResp["evidence_quotes"].(float64)
	if evCount <= 0 {
		t.Errorf("promote_answer evidence_quotes = %v, want > 0", promoteResp["evidence_quotes"])
	}

	// Step 5: read_page returns the new page with non-empty evidence.
	_, readText := callToolRT(t, c, "read_page", map[string]any{"title": "Goroutines (MCP Promoted)"})
	var readResp map[string]any
	if err := json.Unmarshal([]byte(readText), &readResp); err != nil {
		t.Fatalf("read_page unmarshal: %v\nraw=%s", err, readText)
	}
	ev, _ := readResp["evidence"].([]any)
	if len(ev) == 0 {
		t.Errorf("read_page evidence empty for promoted page; raw=%s", readText)
	}

	// Step 6: second promote with same title → title_exists.
	res, badText := callToolRT(t, c, "promote_answer", map[string]any{
		"answer_path": answerPath,
		"title":       "Goroutines (MCP Promoted)",
	})
	if !res.IsError {
		t.Fatalf("second promote_answer returned success; want title_exists: %s", badText)
	}
	var badResp map[string]any
	if err := json.Unmarshal([]byte(badText), &badResp); err != nil {
		t.Fatalf("title_exists error not JSON: %v\nraw=%s", err, badText)
	}
	if badResp["code"] != "title_exists" {
		t.Errorf("code = %v, want title_exists; raw=%s", badResp["code"], badText)
	}
	if data, ok := badResp["data"].(map[string]any); ok {
		if _, hasPath := data["existing_path"]; !hasPath {
			t.Errorf("title_exists payload missing existing_path; raw=%s", badText)
		}
	} else {
		// Tolerate flat shape if a future refactor inlines fields, but
		// surface a friendly hint.
		if _, hasPath := badResp["existing_path"]; !hasPath {
			t.Errorf("title_exists payload missing existing_path (no nested data either); raw=%s", badText)
		}
	}

	// Defensive: log.md has at most one **promote** line for our title
	// (the first call). If a regression silently double-writes, this
	// asserts the trust property held.
	if logBytes, err := os.ReadFile(filepath.Join(wikiDir, "log.md")); err == nil {
		count := strings.Count(string(logBytes), "Goroutines (MCP Promoted)")
		if count == 0 {
			t.Errorf("log.md missing promote line for our title:\n%s", logBytes)
		}
		if count > 2 { // 1 promote + retro-link entry tolerance
			t.Errorf("log.md has %d references to our title; rejected promote should not log: %s", count, logBytes)
		}
	}
}

