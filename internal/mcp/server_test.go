package mcp

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	mcpc "github.com/mark3labs/mcp-go/client"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/mritunjaysharma394/llmwiki/internal/db"
	"github.com/mritunjaysharma394/llmwiki/internal/llm"
	"github.com/mritunjaysharma394/llmwiki/internal/schema"
	"github.com/mritunjaysharma394/llmwiki/internal/wiki"
)

// fakeClient is a minimal llm.Client used by handler tests. Each method
// returns a canned value or echoes the inputs so the handler shape can be
// asserted without disk-cassette plumbing.
type fakeClient struct {
	completeText string
	structured   map[string]any
}

func (f *fakeClient) Complete(ctx context.Context, system, user string) (string, error) {
	if f.completeText == "" {
		return "[stub answer] " + user, nil
	}
	return f.completeText, nil
}

func (f *fakeClient) CompleteStructured(ctx context.Context, system, user string, ts llm.ToolSchema) (map[string]any, error) {
	return f.structured, nil
}

func (f *fakeClient) CompleteStream(ctx context.Context, system, user string, w io.Writer) (string, error) {
	out, err := f.Complete(ctx, system, user)
	if err != nil {
		return "", err
	}
	_, _ = w.Write([]byte(out))
	return out, nil
}

// newTestDeps opens a fresh on-disk sqlite DB under t.TempDir(), seeds the
// caller-provided fixtures, and returns a Deps wired with a fakeClient
// and the bundled schema (mirroring the production path's
// schema.Load-falls-back-to-Bundled behaviour when no AGENTS.md is on disk).
func newTestDeps(t *testing.T, client llm.Client) (Deps, func()) {
	t.Helper()
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "wiki.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if client == nil {
		client = &fakeClient{}
	}
	deps := Deps{
		Cfg: Config{
			WikiDir: filepath.Join(dir, "wiki"),
			RawDir:  filepath.Join(dir, "raw"),
			DBPath:  filepath.Join(dir, "wiki.db"),
		},
		DB:     d,
		Client: client,
		Schema: schema.Bundled(),
	}
	return deps, func() { _ = d.Close() }
}

// connect creates an in-process MCP client for the given server, starts it,
// initializes the protocol handshake, and returns it. The returned cleanup
// closes the client.
func connect(t *testing.T, srv *mcpsrv.MCPServer) (*mcpc.Client, func()) {
	t.Helper()
	c, err := mcpc.NewInProcessClient(srv)
	if err != nil {
		t.Fatalf("new in-process client: %v", err)
	}
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("client start: %v", err)
	}
	initReq := mcpgo.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcpgo.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcpgo.Implementation{Name: "test", Version: "0.0.0"}
	if _, err := c.Initialize(context.Background(), initReq); err != nil {
		t.Fatalf("client initialize: %v", err)
	}
	return c, func() { _ = c.Close() }
}

// callTool is a tiny helper that returns the parsed first-text-content of a
// CallTool response and the raw result for IsError checks.
func callTool(t *testing.T, c *mcpc.Client, name string, args map[string]any) (*mcpgo.CallToolResult, string) {
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

func TestNewServer_RegistersAllEightTools(t *testing.T) {
	deps, cleanup := newTestDeps(t, nil)
	defer cleanup()

	srv := NewServer(deps)
	tools := srv.ListTools()

	got := make([]string, 0, len(tools))
	for name := range tools {
		got = append(got, name)
	}
	sort.Strings(got)
	// Sub-project 7 / Phase I Task 14 added get_schema to the seven
	// existing tools, taking the count to eight.
	want := []string{"ask", "get_schema", "ingest", "lint", "list_pages", "promote_answer", "read_page", "write_page"}
	if !equalSlices(got, want) {
		t.Errorf("tool names = %v, want %v", got, want)
	}
}

func TestListPages_HappyPath(t *testing.T) {
	deps, cleanup := newTestDeps(t, nil)
	defer cleanup()

	srcID, err := deps.DB.UpsertSource("seed:s1", "h1")
	if err != nil {
		t.Fatalf("UpsertSource: %v", err)
	}
	sfID, err := deps.DB.UpsertSourceFile(db.SourceFile{
		SourceID: srcID, RelativePath: "doc.md", ContentHash: "h", ByteSize: 1, LineCount: 1,
	})
	if err != nil {
		t.Fatalf("UpsertSourceFile: %v", err)
	}
	for _, title := range []string{"Database Internals", "Database Indexing", "Networking Basics"} {
		if err := deps.DB.UpsertPage(db.PageRecord{
			Title: title, Path: title + ".md", Body: "body", ContentHash: "x",
			SourceIDs: []int64{srcID},
		}); err != nil {
			t.Fatalf("UpsertPage(%s): %v", title, err)
		}
		p, _ := deps.DB.GetPage(title)
		_ = deps.DB.InsertEvidence(p.ID, srcID, []db.Evidence{
			{Quote: "q for " + title, LineStart: 1, LineEnd: 1, SourceFileID: &sfID},
		})
	}

	srv := NewServer(deps)
	c, done := connect(t, srv)
	defer done()
	_, text := callTool(t, c, "list_pages", map[string]any{"limit": 50.0})

	var got struct {
		Pages []map[string]any `json:"pages"`
	}
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatalf("unmarshal list_pages response: %v\nraw: %s", err, text)
	}
	if len(got.Pages) != 3 {
		t.Fatalf("got %d pages, want 3 (raw: %s)", len(got.Pages), text)
	}
	for _, p := range got.Pages {
		for _, key := range []string{"title", "path", "updated_at", "source_files"} {
			if _, ok := p[key]; !ok {
				t.Errorf("page %v missing key %q", p["title"], key)
			}
		}
	}
}

func TestListPages_PrefixFilter(t *testing.T) {
	deps, cleanup := newTestDeps(t, nil)
	defer cleanup()

	for _, title := range []string{"Database Internals", "Database Indexing", "Networking Basics"} {
		if err := deps.DB.UpsertPage(db.PageRecord{
			Title: title, Path: title + ".md", Body: "body",
		}); err != nil {
			t.Fatalf("UpsertPage(%s): %v", title, err)
		}
	}

	srv := NewServer(deps)
	c, done := connect(t, srv)
	defer done()
	_, text := callTool(t, c, "list_pages", map[string]any{"prefix": "Database"})

	var got struct {
		Pages []map[string]any `json:"pages"`
	}
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Pages) != 2 {
		t.Fatalf("got %d pages, want 2 (raw: %s)", len(got.Pages), text)
	}
	for _, p := range got.Pages {
		title, _ := p["title"].(string)
		if !strings.HasPrefix(title, "Database") {
			t.Errorf("page title %q does not have prefix Database", title)
		}
	}
}

func TestReadPage_HappyPath(t *testing.T) {
	deps, cleanup := newTestDeps(t, nil)
	defer cleanup()

	srcID, _ := deps.DB.UpsertSource("seed:r1", "h")
	sfID, _ := deps.DB.UpsertSourceFile(db.SourceFile{
		SourceID: srcID, RelativePath: "guide.md", ContentHash: "h", ByteSize: 4, LineCount: 4,
	})
	if err := deps.DB.UpsertPage(db.PageRecord{
		Title: "Channels", Path: "Channels.md", Body: "Channel basics body.",
		ContentHash: "abc", SourceIDs: []int64{srcID},
	}); err != nil {
		t.Fatalf("UpsertPage: %v", err)
	}
	p, _ := deps.DB.GetPage("Channels")
	if err := deps.DB.InsertEvidence(p.ID, srcID, []db.Evidence{
		{Quote: "channels block when full", LineStart: 4, LineEnd: 4, SourceFileID: &sfID},
		{Quote: "buffered channels exist", LineStart: 6, LineEnd: 6, SourceFileID: &sfID},
	}); err != nil {
		t.Fatalf("InsertEvidence: %v", err)
	}
	_ = deps.DB.UpsertLinks("Channels", []db.Link{{FromPage: "Channels", ToPage: "Goroutines", LinkType: "related"}})

	srv := NewServer(deps)
	c, done := connect(t, srv)
	defer done()
	_, text := callTool(t, c, "read_page", map[string]any{"title": "Channels"})

	var got map[string]any
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, text)
	}
	if got["title"] != "Channels" {
		t.Errorf("title = %v, want Channels", got["title"])
	}
	if got["body"] == nil || got["body"].(string) == "" {
		t.Errorf("body empty; raw: %s", text)
	}
	ev, _ := got["evidence"].([]any)
	if len(ev) != 2 {
		t.Errorf("evidence length = %d, want 2", len(ev))
	}
	if _, ok := got["links"]; !ok {
		t.Errorf("missing links key")
	}
	if _, ok := got["source_files"]; !ok {
		t.Errorf("missing source_files key")
	}
}

func TestReadPage_NotFound(t *testing.T) {
	deps, cleanup := newTestDeps(t, nil)
	defer cleanup()

	srv := NewServer(deps)
	c, done := connect(t, srv)
	defer done()
	res, text := callTool(t, c, "read_page", map[string]any{"title": "missing"})

	if !res.IsError {
		t.Fatalf("expected IsError=true; raw: %s", text)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatalf("error payload not JSON: %v\nraw: %s", err, text)
	}
	if got["code"] != "not_found" {
		t.Errorf("code = %v, want not_found", got["code"])
	}
}

func TestLint_DelegatesToCmdRunLint(t *testing.T) {
	// Two pages, no real sources — runLintInternal returns "no contradictions"
	// (or a stubbed model output) and skips staleness checks because the source
	// list is empty. The handler shape is what matters: a single text content
	// string that mentions the staleness and contradiction headings.
	fc := &fakeClient{completeText: "No contradictions found."}
	deps, cleanup := newTestDeps(t, fc)
	defer cleanup()

	for _, title := range []string{"P1", "P2"} {
		if err := deps.DB.UpsertPage(db.PageRecord{
			Title: title, Path: title + ".md", Body: "body of " + title,
		}); err != nil {
			t.Fatalf("UpsertPage: %v", err)
		}
	}

	srv := NewServer(deps)
	c, done := connect(t, srv)
	defer done()
	res, text := callTool(t, c, "lint", map[string]any{})

	if res.IsError {
		t.Fatalf("lint returned IsError=true, text=%s", text)
	}
	if !strings.Contains(text, "Staleness") {
		t.Errorf("lint output missing 'Staleness' heading; raw: %s", text)
	}
	if !strings.Contains(text, "Contradiction") {
		t.Errorf("lint output missing 'Contradiction' heading; raw: %s", text)
	}
	if !strings.Contains(text, "No contradictions") {
		t.Errorf("lint output missing model verdict; raw: %s", text)
	}
}

func TestAsk_HappyPath(t *testing.T) {
	fc := &fakeClient{completeText: "Channels block when full. [Channels]"}
	deps, cleanup := newTestDeps(t, fc)
	defer cleanup()

	srcID, _ := deps.DB.UpsertSource("seed:a1", "h")
	sfID, _ := deps.DB.UpsertSourceFile(db.SourceFile{
		SourceID: srcID, RelativePath: "guide.md", ContentHash: "h", ByteSize: 4, LineCount: 4,
	})
	if err := deps.DB.UpsertPage(db.PageRecord{
		Title: "Channels", Path: "Channels.md", Body: "Channel body discusses blocking.",
		ContentHash: "abc", SourceIDs: []int64{srcID},
	}); err != nil {
		t.Fatalf("UpsertPage: %v", err)
	}
	p, _ := deps.DB.GetPage("Channels")
	_ = deps.DB.InsertEvidence(p.ID, srcID, []db.Evidence{
		{Quote: "channels block when full", LineStart: 4, LineEnd: 4, SourceFileID: &sfID},
	})

	srv := NewServer(deps)
	c, done := connect(t, srv)
	defer done()
	_, text := callTool(t, c, "ask", map[string]any{"question": "what about channels?"})

	var got map[string]any
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatalf("unmarshal ask response: %v\nraw: %s", err, text)
	}
	answer, _ := got["answer"].(string)
	if !strings.Contains(answer, "Channels block when full") {
		t.Errorf("answer = %q, want it to contain stub text", answer)
	}
	sources, _ := got["sources"].([]any)
	if len(sources) == 0 {
		t.Fatalf("expected sources array to be non-empty; raw: %s", text)
	}
	first, _ := sources[0].(map[string]any)
	for _, key := range []string{"page_title", "quote", "source_file", "line_start", "line_end"} {
		if _, ok := first[key]; !ok {
			t.Errorf("source[0] missing key %q (raw: %v)", key, first)
		}
	}
}

func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ----- write_page / ingest tests (Phase G2) ---------------------------------

// seedIngestedSource writes <content> to a temp file under deps.Cfg.WikiDir's
// parent and inserts the matching sources / source_files rows so the
// write_page handler can resolve the source_file path. Returns the
// absolute on-disk path the test should pass as `source_file`.
func seedIngestedSource(t *testing.T, deps Deps, content string) string {
	t.Helper()
	// Use a sibling tempdir to deps.Cfg.WikiDir so paths are predictable.
	dir := filepath.Dir(deps.Cfg.WikiDir)
	srcPath := filepath.Join(dir, "seed-source.md")
	if err := os.WriteFile(srcPath, []byte(content), 0644); err != nil {
		t.Fatalf("write seed source: %v", err)
	}
	srcID, err := deps.DB.UpsertSource(srcPath, "h-seed")
	if err != nil {
		t.Fatalf("UpsertSource: %v", err)
	}
	if _, err := deps.DB.UpsertSourceFile(db.SourceFile{
		SourceID:     srcID,
		RelativePath: srcPath,
		ContentHash:  "h-seed",
		ByteSize:     int64(len(content)),
		LineCount:    1,
	}); err != nil {
		t.Fatalf("UpsertSourceFile: %v", err)
	}
	return srcPath
}

func TestWritePage_ValidEvidenceWritesPage(t *testing.T) {
	deps, cleanup := newTestDeps(t, nil)
	defer cleanup()
	if err := os.MkdirAll(deps.Cfg.WikiDir, 0755); err != nil {
		t.Fatalf("mkdir wiki: %v", err)
	}

	content := "the quick brown fox jumps over the lazy dog\n"
	srcPath := seedIngestedSource(t, deps, content)

	srv := NewServer(deps)
	c, done := connect(t, srv)
	defer done()

	res, text := callTool(t, c, "write_page", map[string]any{
		"title": "Foo",
		"body":  "Foo is a study of foxes.",
		"evidence": []any{
			map[string]any{"quote": "quick brown fox", "source_file": srcPath},
		},
	})
	if res.IsError {
		t.Fatalf("expected success; got IsError=true, raw=%s", text)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, text)
	}
	if got["title"] != "Foo" {
		t.Errorf("title = %v, want Foo", got["title"])
	}

	// Disk: page file exists.
	pagePath := filepath.Join(deps.Cfg.WikiDir, "Foo.md")
	if _, err := os.Stat(pagePath); err != nil {
		t.Errorf("page not on disk at %s: %v", pagePath, err)
	}
	// DB: page row exists.
	page, err := deps.DB.GetPage("Foo")
	if err != nil || page == nil {
		t.Fatalf("page row missing: err=%v page=%v", err, page)
	}
	// Evidence linked.
	ev, err := deps.DB.GetEvidenceForPage(page.ID)
	if err != nil {
		t.Fatalf("GetEvidenceForPage: %v", err)
	}
	if len(ev) != 1 || ev[0].Quote != "quick brown fox" {
		t.Errorf("evidence rows = %v, want one quote 'quick brown fox'", ev)
	}
	// index.md regenerated.
	if _, err := os.Stat(filepath.Join(deps.Cfg.WikiDir, "index.md")); err != nil {
		t.Errorf("index.md not regenerated: %v", err)
	}
	// log.md got an mcp.write_page entry.
	logBody, err := os.ReadFile(filepath.Join(deps.Cfg.WikiDir, "log.md"))
	if err != nil {
		t.Fatalf("read log.md: %v", err)
	}
	if !strings.Contains(string(logBody), "mcp.write_page") {
		t.Errorf("log.md missing mcp.write_page line: %s", logBody)
	}
}

func TestWritePage_InvalidEvidenceReturnsStructuredError(t *testing.T) {
	deps, cleanup := newTestDeps(t, nil)
	defer cleanup()
	if err := os.MkdirAll(deps.Cfg.WikiDir, 0755); err != nil {
		t.Fatalf("mkdir wiki: %v", err)
	}

	content := "the quick brown fox jumps over the lazy dog\n"
	srcPath := seedIngestedSource(t, deps, content)

	srv := NewServer(deps)
	c, done := connect(t, srv)
	defer done()

	res, text := callTool(t, c, "write_page", map[string]any{
		"title": "Bad",
		"body":  "Body that quotes something not in source.",
		"evidence": []any{
			map[string]any{"quote": "this is not in the source", "source_file": srcPath},
		},
	})
	if !res.IsError {
		t.Fatalf("expected IsError=true, raw=%s", text)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatalf("error payload not JSON: %v\nraw: %s", err, text)
	}
	if got["code"] != "evidence_invalid" {
		t.Errorf("code = %v, want evidence_invalid", got["code"])
	}
	if _, ok := got["dropped"]; !ok {
		t.Errorf("missing dropped field; raw=%s", text)
	}
	if _, ok := got["hint"]; !ok {
		t.Errorf("missing hint field; raw=%s", text)
	}
	dropped, _ := got["dropped"].([]any)
	if len(dropped) != 1 {
		t.Errorf("dropped len = %d, want 1", len(dropped))
	} else {
		dm, _ := dropped[0].(map[string]any)
		if dm["quote"] != "this is not in the source" {
			t.Errorf("dropped[0].quote = %v", dm["quote"])
		}
		if dm["reason"] == "" {
			t.Errorf("dropped[0].reason missing")
		}
	}

	// No disk write.
	if _, err := os.Stat(filepath.Join(deps.Cfg.WikiDir, "Bad.md")); err == nil {
		t.Error("page Bad.md should NOT exist after evidence_invalid")
	}
	// No DB row.
	if p, _ := deps.DB.GetPage("Bad"); p != nil {
		t.Error("DB row for Bad should NOT exist")
	}
	// No log.md line for Bad — log.md may not exist at all, or may
	// exist from an unrelated run, but it must not contain a
	// mcp.write_page entry referencing this title.
	if data, err := os.ReadFile(filepath.Join(deps.Cfg.WikiDir, "log.md")); err == nil {
		if strings.Contains(string(data), "Bad") {
			t.Errorf("log.md should not record failed write_page; got: %s", data)
		}
	}
}

func TestWritePage_TitleCollisionReturnsStructuredError(t *testing.T) {
	deps, cleanup := newTestDeps(t, nil)
	defer cleanup()
	if err := os.MkdirAll(deps.Cfg.WikiDir, 0755); err != nil {
		t.Fatalf("mkdir wiki: %v", err)
	}

	content := "alpha beta gamma delta\n"
	srcPath := seedIngestedSource(t, deps, content)

	srv := NewServer(deps)
	c, done := connect(t, srv)
	defer done()

	// First call: should succeed.
	res, text := callTool(t, c, "write_page", map[string]any{
		"title": "Greek",
		"body":  "Greek letters body.",
		"evidence": []any{
			map[string]any{"quote": "alpha beta", "source_file": srcPath},
		},
	})
	if res.IsError {
		t.Fatalf("first call should succeed; got error: %s", text)
	}

	// Second call same title: title_exists.
	res, text = callTool(t, c, "write_page", map[string]any{
		"title": "Greek",
		"body":  "Different body but same title.",
		"evidence": []any{
			map[string]any{"quote": "gamma delta", "source_file": srcPath},
		},
	})
	if !res.IsError {
		t.Fatalf("expected title_exists error, raw=%s", text)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatalf("unmarshal error: %v\nraw=%s", err, text)
	}
	if got["code"] != "title_exists" {
		t.Errorf("code = %v, want title_exists", got["code"])
	}
	if got["existing_path"] == nil || got["existing_path"] == "" {
		t.Errorf("existing_path missing or empty")
	}
}

func TestWritePage_RequiresAtLeastOneEvidenceEntry(t *testing.T) {
	deps, cleanup := newTestDeps(t, nil)
	defer cleanup()
	if err := os.MkdirAll(deps.Cfg.WikiDir, 0755); err != nil {
		t.Fatalf("mkdir wiki: %v", err)
	}

	srv := NewServer(deps)
	c, done := connect(t, srv)
	defer done()

	res, text := callTool(t, c, "write_page", map[string]any{
		"title":    "Empty",
		"body":     "Body.",
		"evidence": []any{},
	})
	if !res.IsError {
		t.Fatalf("expected IsError=true; raw=%s", text)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatalf("error payload not JSON: %v\nraw=%s", err, text)
	}
	if got["code"] != "evidence_required" {
		t.Errorf("code = %v, want evidence_required", got["code"])
	}
}

func TestWritePage_SourceMustBeIngested(t *testing.T) {
	deps, cleanup := newTestDeps(t, nil)
	defer cleanup()
	if err := os.MkdirAll(deps.Cfg.WikiDir, 0755); err != nil {
		t.Fatalf("mkdir wiki: %v", err)
	}

	srv := NewServer(deps)
	c, done := connect(t, srv)
	defer done()

	res, text := callTool(t, c, "write_page", map[string]any{
		"title": "Floating",
		"body":  "Body referencing an unknown source.",
		"evidence": []any{
			map[string]any{"quote": "anything", "source_file": "/no/such/path/never-ingested.md"},
		},
	})
	if !res.IsError {
		t.Fatalf("expected IsError=true; raw=%s", text)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatalf("error payload not JSON: %v\nraw=%s", err, text)
	}
	if got["code"] != "source_not_ingested" {
		t.Errorf("code = %v, want source_not_ingested", got["code"])
	}
	if got["source_file"] != "/no/such/path/never-ingested.md" {
		t.Errorf("source_file = %v", got["source_file"])
	}
}

// fakeIngestClient returns a canned CompleteStructured response shaped like
// the write_pages tool result so wiki.IngestSourceFilesToPages produces a
// validated page without any real LLM call.
type fakeIngestClient struct {
	fakeClient
}

func (f *fakeIngestClient) CompleteStructured(ctx context.Context, system, user string, ts llm.ToolSchema) (map[string]any, error) {
	// Pull the source file path out of the user prompt's "=== <path> ===" header
	// so the validator's source_file attribution matches. Best-effort: the
	// full prompt has a `=== <path> ===` line right before the body.
	path := "source"
	for _, line := range strings.Split(user, "\n") {
		if strings.HasPrefix(line, "=== ") && strings.HasSuffix(line, " ===") {
			path = strings.TrimSuffix(strings.TrimPrefix(line, "=== "), " ===")
			break
		}
	}
	return map[string]any{
		"pages": []any{
			map[string]any{
				"title": "FakePage",
				"body":  "Body about goroutines.",
				"evidence": []any{
					map[string]any{"quote": "Goroutines are lightweight", "source_file": path},
				},
			},
		},
	}, nil
}

func TestIngest_DelegatesToRunIngest(t *testing.T) {
	deps, cleanup := newTestDeps(t, &fakeIngestClient{})
	defer cleanup()
	if err := os.MkdirAll(deps.Cfg.WikiDir, 0755); err != nil {
		t.Fatalf("mkdir wiki: %v", err)
	}

	tempDir := filepath.Dir(deps.Cfg.WikiDir)
	srcPath := filepath.Join(tempDir, "ingest-src.md")
	if err := os.WriteFile(srcPath, []byte("Goroutines are lightweight threads.\n"), 0644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	srv := NewServer(deps)
	c, done := connect(t, srv)
	defer done()

	res, text := callTool(t, c, "ingest", map[string]any{"source": srcPath})
	if res.IsError {
		t.Fatalf("ingest returned error: %s", text)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatalf("unmarshal: %v\nraw=%s", err, text)
	}
	for _, key := range []string{"pages_written", "evidence_quotes", "dropped_pages"} {
		if _, ok := got[key]; !ok {
			t.Errorf("response missing %q (raw=%s)", key, text)
		}
	}
	pw, _ := got["pages_written"].(float64)
	if pw <= 0 {
		t.Errorf("pages_written = %v, want > 0", got["pages_written"])
	}

	// Pages reach disk.
	page, err := deps.DB.GetPage("FakePage")
	if err != nil || page == nil {
		t.Fatalf("FakePage missing from DB: err=%v page=%v", err, page)
	}
	if _, err := os.Stat(filepath.Join(deps.Cfg.WikiDir, "FakePage.md")); err != nil {
		t.Errorf("FakePage.md not on disk: %v", err)
	}
}

func TestIngest_ForceFlag(t *testing.T) {
	deps, cleanup := newTestDeps(t, &fakeIngestClient{})
	defer cleanup()
	if err := os.MkdirAll(deps.Cfg.WikiDir, 0755); err != nil {
		t.Fatalf("mkdir wiki: %v", err)
	}

	tempDir := filepath.Dir(deps.Cfg.WikiDir)
	srcPath := filepath.Join(tempDir, "ingest-src.md")
	if err := os.WriteFile(srcPath, []byte("Goroutines are lightweight threads.\n"), 0644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	srv := NewServer(deps)
	c, done := connect(t, srv)
	defer done()

	// First ingest.
	if _, text := callTool(t, c, "ingest", map[string]any{"source": srcPath}); text == "" {
		t.Fatal("first ingest produced empty result")
	}

	// Second ingest without force: should be skipped (unchanged hash).
	_, text2 := callTool(t, c, "ingest", map[string]any{"source": srcPath})
	var got2 map[string]any
	_ = json.Unmarshal([]byte(text2), &got2)
	if got2["skipped"] != true {
		t.Errorf("expected skipped=true on second call without force; got %v (raw=%s)", got2["skipped"], text2)
	}

	// Third ingest with force: should NOT be skipped.
	_, text3 := callTool(t, c, "ingest", map[string]any{"source": srcPath, "force": true})
	var got3 map[string]any
	_ = json.Unmarshal([]byte(text3), &got3)
	if got3["skipped"] == true {
		t.Errorf("force=true should re-ingest; got skipped=true (raw=%s)", text3)
	}
}

// TestWritePage_RetroLinksExistingPages pre-seeds two pages mentioning
// "FooBar" in bare prose, then mcp.write_page lands a new page titled
// "FooBar". The response payload should include `retro_linked_pages: 2`
// and both pre-existing pages on disk should now contain `[[FooBar]]`.
// Phase D wiring of RetroLinkPages into mcp.write_page.
func TestWritePage_RetroLinksExistingPages(t *testing.T) {
	deps, cleanup := newTestDeps(t, nil)
	defer cleanup()
	if err := os.MkdirAll(deps.Cfg.WikiDir, 0755); err != nil {
		t.Fatalf("mkdir wiki: %v", err)
	}

	content := "FooBar is the canonical identifier in our examples.\n"
	srcPath := seedIngestedSource(t, deps, content)

	// Pre-seed two existing pages mentioning FooBar in bare prose. The
	// page-write path here goes through the DB directly so we can pin
	// the body bytes; the write_page handler's retro-linker will then
	// re-emit them via RetroLinkPages once the new "FooBar" page lands.
	for _, seed := range []struct{ title, body string }{
		{"Alpha Component", "Alpha Component depends on FooBar in two places.\n"},
		{"Beta Component", "Beta Component refactored away from FooBar last quarter.\n"},
	} {
		path := filepath.Join(deps.Cfg.WikiDir, seed.title+".md")
		// Match what wiki.WritePage would write so RetroLinkPages can
		// ReadPage round-trip without surprises.
		fm := "---\ntitle: " + seed.title + "\n---\n\n"
		if err := os.WriteFile(path, []byte(fm+seed.body), 0644); err != nil {
			t.Fatalf("seed write %s: %v", seed.title, err)
		}
		if err := deps.DB.UpsertPage(db.PageRecord{
			Title: seed.title, Path: path, Body: seed.body, ContentHash: "h-" + seed.title,
		}); err != nil {
			t.Fatalf("seed UpsertPage %s: %v", seed.title, err)
		}
	}

	srv := NewServer(deps)
	c, done := connect(t, srv)
	defer done()

	res, text := callTool(t, c, "write_page", map[string]any{
		"title": "FooBar",
		"body":  "FooBar is canonical.",
		"evidence": []any{
			map[string]any{"quote": "FooBar is the canonical identifier", "source_file": srcPath},
		},
	})
	if res.IsError {
		t.Fatalf("expected success; got IsError=true, raw=%s", text)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, text)
	}
	rl, ok := got["retro_linked_pages"]
	if !ok {
		t.Fatalf("response missing retro_linked_pages key; raw=%s", text)
	}
	rlf, _ := rl.(float64)
	if int(rlf) != 2 {
		t.Errorf("retro_linked_pages = %v, want 2 (raw=%s)", rl, text)
	}

	// Disk: both seeded pages now contain [[FooBar]].
	for _, want := range []string{"Alpha Component", "Beta Component"} {
		body, err := os.ReadFile(filepath.Join(deps.Cfg.WikiDir, want+".md"))
		if err != nil {
			t.Fatalf("read %s: %v", want, err)
		}
		if !strings.Contains(string(body), "[[FooBar]]") {
			t.Errorf("page %s missing [[FooBar]]:\n%s", want, body)
		}
	}
}

// TestIngest_ReturnShapeIncludesRetroLinkedPages drives mcp.ingest via
// the fakeIngestClient and asserts the response payload includes a
// `retro_linked_pages` integer key. Phase D extension of the ingest
// handler return shape.
func TestIngest_ReturnShapeIncludesRetroLinkedPages(t *testing.T) {
	deps, cleanup := newTestDeps(t, &fakeIngestClient{})
	defer cleanup()
	if err := os.MkdirAll(deps.Cfg.WikiDir, 0755); err != nil {
		t.Fatalf("mkdir wiki: %v", err)
	}

	tempDir := filepath.Dir(deps.Cfg.WikiDir)
	srcPath := filepath.Join(tempDir, "ingest-src.md")
	if err := os.WriteFile(srcPath, []byte("Goroutines are lightweight threads.\n"), 0644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	srv := NewServer(deps)
	c, done := connect(t, srv)
	defer done()

	res, text := callTool(t, c, "ingest", map[string]any{"source": srcPath})
	if res.IsError {
		t.Fatalf("ingest returned error: %s", text)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatalf("unmarshal: %v\nraw=%s", err, text)
	}
	if _, ok := got["retro_linked_pages"]; !ok {
		t.Errorf("response missing retro_linked_pages key (raw=%s)", text)
	}
}

// TestIngest_ReturnShapeIncludesContradictionsFlagged drives mcp.ingest
// via the fakeIngestClient and asserts the response payload includes a
// `contradictions_flagged` integer key. Phase E extension of the
// ingest handler return shape — informational counter populated by
// wiki.DetectIngestContradictions during IngestSource.
func TestIngest_ReturnShapeIncludesContradictionsFlagged(t *testing.T) {
	deps, cleanup := newTestDeps(t, &fakeIngestClient{})
	defer cleanup()
	if err := os.MkdirAll(deps.Cfg.WikiDir, 0755); err != nil {
		t.Fatalf("mkdir wiki: %v", err)
	}

	tempDir := filepath.Dir(deps.Cfg.WikiDir)
	srcPath := filepath.Join(tempDir, "ingest-src.md")
	if err := os.WriteFile(srcPath, []byte("Goroutines are lightweight threads.\n"), 0644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	srv := NewServer(deps)
	c, done := connect(t, srv)
	defer done()

	res, text := callTool(t, c, "ingest", map[string]any{"source": srcPath})
	if res.IsError {
		t.Fatalf("ingest returned error: %s", text)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatalf("unmarshal: %v\nraw=%s", err, text)
	}
	if _, ok := got["contradictions_flagged"]; !ok {
		t.Errorf("response missing contradictions_flagged key (raw=%s)", text)
	}
}

// ----- promote_answer tests (Phase F / sub-project 6a) -------------------

// seedPromoteFixture wires a deps-backed source/source_file row matching
// the on-disk source bytes and writes a saved-answer file (using the same
// FormatSavedAnswer the CLI's saveAnswer uses) to a temp answers dir.
// Returns (answerPath, srcPath) so tests can mutate or pre-seed before
// calling mcp.promote_answer.
func seedPromoteFixture(t *testing.T, deps Deps, sourceContent, question, answerBody, evidenceQuote string) (string, string) {
	t.Helper()
	root := filepath.Dir(deps.Cfg.WikiDir)
	answersDir := filepath.Join(root, "answers")
	if err := os.MkdirAll(answersDir, 0755); err != nil {
		t.Fatalf("mkdir answers: %v", err)
	}
	srcPath := filepath.Join(root, "promote-src.md")
	if err := os.WriteFile(srcPath, []byte(sourceContent), 0644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	srcID, err := deps.DB.UpsertSource(srcPath, "h-promote")
	if err != nil {
		t.Fatalf("UpsertSource: %v", err)
	}
	if _, err := deps.DB.UpsertSourceFile(db.SourceFile{
		SourceID:     srcID,
		RelativePath: filepath.Base(srcPath),
		ContentHash:  "h-promote",
		ByteSize:     int64(len(sourceContent)),
		LineCount:    1,
	}); err != nil {
		t.Fatalf("UpsertSourceFile: %v", err)
	}
	in := wiki.SavedAnswerInput{
		Question: question,
		Answer:   answerBody,
		Model:    "test-model",
		Pages: []wiki.Page{{
			Title: "X",
			Evidence: []wiki.Evidence{{
				Quote:          evidenceQuote,
				LineStart:      2,
				LineEnd:        2,
				SourceFilePath: filepath.Base(srcPath),
			}},
		}},
		At: time.Date(2026, 5, 4, 15, 2, 8, 0, time.UTC),
	}
	body := wiki.FormatSavedAnswer(in)
	answerPath := filepath.Join(answersDir, "2026-05-04-150208-test.md")
	if err := os.WriteFile(answerPath, []byte(body), 0644); err != nil {
		t.Fatalf("write answer: %v", err)
	}
	return answerPath, srcPath
}

// TestPromoteAnswer_ToolRegistered asserts the tool appears in
// srv.ListTools(). The serverVersion check tracks the current
// release line — sub-project 7 / Phase I bumps to 0.7.0-rc.1.
func TestPromoteAnswer_ToolRegistered(t *testing.T) {
	deps, cleanup := newTestDeps(t, nil)
	defer cleanup()

	srv := NewServer(deps)
	tools := srv.ListTools()
	if _, ok := tools["promote_answer"]; !ok {
		got := make([]string, 0, len(tools))
		for n := range tools {
			got = append(got, n)
		}
		sort.Strings(got)
		t.Fatalf("promote_answer not registered; tools=%v", got)
	}
	if serverVersion != "0.7.0-rc.1" {
		t.Errorf("serverVersion = %q, want %q", serverVersion, "0.7.0-rc.1")
	}
}

// TestPromoteAnswer_HappyPath wires a real on-disk source + answer file,
// then calls mcp.promote_answer with the absolute answer_path. The
// response should expose title/path/evidence_quotes/rewrite_applied/
// retro_linked_pages keys, and the page should land on disk.
func TestPromoteAnswer_HappyPath(t *testing.T) {
	deps, cleanup := newTestDeps(t, nil)
	defer cleanup()
	if err := os.MkdirAll(deps.Cfg.WikiDir, 0755); err != nil {
		t.Fatalf("mkdir wiki: %v", err)
	}

	source := "Line one of source.\nthe validator drops unverified quotes\nLine three.\n"
	answerPath, _ := seedPromoteFixture(t, deps,
		source,
		"how does the validator work?",
		"The validator drops unverified quotes before write.",
		"the validator drops unverified quotes",
	)

	srv := NewServer(deps)
	c, done := connect(t, srv)
	defer done()

	res, text := callTool(t, c, "promote_answer", map[string]any{
		"answer_path": answerPath,
		"title":       "Validator Internals",
	})
	if res.IsError {
		t.Fatalf("expected success; got IsError=true, raw=%s", text)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatalf("unmarshal: %v\nraw=%s", err, text)
	}
	if got["title"] != "Validator Internals" {
		t.Errorf("title = %v, want Validator Internals", got["title"])
	}
	if got["path"] == nil || got["path"].(string) == "" {
		t.Errorf("path empty; raw=%s", text)
	}
	evq, _ := got["evidence_quotes"].(float64)
	if int(evq) != 1 {
		t.Errorf("evidence_quotes = %v, want 1", got["evidence_quotes"])
	}
	if got["rewrite_applied"] != false {
		t.Errorf("rewrite_applied = %v, want false", got["rewrite_applied"])
	}
	if _, ok := got["retro_linked_pages"]; !ok {
		t.Errorf("response missing retro_linked_pages key; raw=%s", text)
	}

	// Page on disk + DB.
	pagePath := filepath.Join(deps.Cfg.WikiDir, "Validator Internals.md")
	if _, err := os.Stat(pagePath); err != nil {
		t.Errorf("page not on disk at %s: %v", pagePath, err)
	}
	if p, _ := deps.DB.GetPage("Validator Internals"); p == nil {
		t.Errorf("DB row missing for Validator Internals")
	}
}

// TestPromoteAnswer_StaleEvidence mutates the source file between
// answer-write and promote so the quote no longer substring-matches; the
// handler should return a structured evidence_invalid error with a
// dropped-quotes payload.
func TestPromoteAnswer_StaleEvidence(t *testing.T) {
	deps, cleanup := newTestDeps(t, nil)
	defer cleanup()
	if err := os.MkdirAll(deps.Cfg.WikiDir, 0755); err != nil {
		t.Fatalf("mkdir wiki: %v", err)
	}

	source := "Line one.\nthe validator drops unverified quotes\nLine three.\n"
	answerPath, srcPath := seedPromoteFixture(t, deps,
		source,
		"stale check?",
		"verbatim",
		"the validator drops unverified quotes",
	)

	// Mutate the source so the quote no longer substring-matches.
	if err := os.WriteFile(srcPath, []byte("totally different content\n"), 0644); err != nil {
		t.Fatalf("rewrite source: %v", err)
	}

	srv := NewServer(deps)
	c, done := connect(t, srv)
	defer done()

	res, text := callTool(t, c, "promote_answer", map[string]any{
		"answer_path": answerPath,
		"title":       "Stale Page",
	})
	if !res.IsError {
		t.Fatalf("expected IsError=true; raw=%s", text)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatalf("error payload not JSON: %v\nraw=%s", err, text)
	}
	if got["code"] != "evidence_invalid" {
		t.Errorf("code = %v, want evidence_invalid", got["code"])
	}
	if _, ok := got["dropped"]; !ok {
		t.Errorf("missing dropped field; raw=%s", text)
	}
	dropped, _ := got["dropped"].([]any)
	if len(dropped) == 0 {
		t.Errorf("dropped empty; want at least one entry")
	}

	// No disk write.
	if _, err := os.Stat(filepath.Join(deps.Cfg.WikiDir, "Stale Page.md")); err == nil {
		t.Error("page should NOT be on disk after evidence_invalid")
	}
}

// TestPromoteAnswer_TitleCollision pre-seeds an existing page with the
// target title; the handler should return a structured title_exists.
// ----- Phase E (sub-project 6b) — mcp.ingest update_existing wiring -----

// TestServerVersionIs070 pins the serverVersion constant to the v0.7
// release line. Sub-project 7 / Phase I Task 14 bumps from
// "0.6.0-rc.1" to "0.7.0-rc.1"; this guard rail catches accidental
// rollback of the version string.
func TestServerVersionIs070(t *testing.T) {
	if serverVersion != "0.7.0-rc.1" {
		t.Errorf("serverVersion = %q, want %q", serverVersion, "0.7.0-rc.1")
	}
}

// TestIngest_AcceptsUpdateExistingArg drives mcp.ingest with
// update_existing: true and asserts the flag propagates through the
// ingestSourceFn seam into wiki.IngestOptions.
func TestIngest_AcceptsUpdateExistingArg(t *testing.T) {
	deps, cleanup := newTestDeps(t, &fakeIngestClient{})
	defer cleanup()
	if err := os.MkdirAll(deps.Cfg.WikiDir, 0755); err != nil {
		t.Fatalf("mkdir wiki: %v", err)
	}

	tempDir := filepath.Dir(deps.Cfg.WikiDir)
	srcPath := filepath.Join(tempDir, "ingest-src.md")
	if err := os.WriteFile(srcPath, []byte("Goroutines are lightweight threads.\n"), 0644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	prev := ingestSourceFn
	defer func() { ingestSourceFn = prev }()
	var captured wiki.IngestOptions
	ingestSourceFn = func(ctx context.Context, cfg wiki.IngestSourceConfig, database *db.DB, client llm.Client, source string, opts wiki.IngestOptions) (wiki.IngestRunResult, error) {
		captured = opts
		return wiki.IngestRunResult{Source: source}, nil
	}

	srv := NewServer(deps)
	c, done := connect(t, srv)
	defer done()

	res, text := callTool(t, c, "ingest", map[string]any{
		"source":          srcPath,
		"update_existing": true,
	})
	if res.IsError {
		t.Fatalf("ingest returned error: %s", text)
	}
	if !captured.UpdateExisting {
		t.Errorf("captured IngestOptions.UpdateExisting = false, want true")
	}
}

// TestIngest_DefaultsUpdateExistingOff drives mcp.ingest without an
// update_existing argument and asserts the flag defaults to false.
func TestIngest_DefaultsUpdateExistingOff(t *testing.T) {
	deps, cleanup := newTestDeps(t, &fakeIngestClient{})
	defer cleanup()
	if err := os.MkdirAll(deps.Cfg.WikiDir, 0755); err != nil {
		t.Fatalf("mkdir wiki: %v", err)
	}

	tempDir := filepath.Dir(deps.Cfg.WikiDir)
	srcPath := filepath.Join(tempDir, "ingest-src.md")
	if err := os.WriteFile(srcPath, []byte("Goroutines are lightweight threads.\n"), 0644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	prev := ingestSourceFn
	defer func() { ingestSourceFn = prev }()
	var captured wiki.IngestOptions
	ingestSourceFn = func(ctx context.Context, cfg wiki.IngestSourceConfig, database *db.DB, client llm.Client, source string, opts wiki.IngestOptions) (wiki.IngestRunResult, error) {
		captured = opts
		return wiki.IngestRunResult{Source: source}, nil
	}

	srv := NewServer(deps)
	c, done := connect(t, srv)
	defer done()

	res, text := callTool(t, c, "ingest", map[string]any{"source": srcPath})
	if res.IsError {
		t.Fatalf("ingest returned error: %s", text)
	}
	if captured.UpdateExisting {
		t.Errorf("captured IngestOptions.UpdateExisting = true, want false (default)")
	}
}

// TestIngest_ReturnShapeIncludesPagesUpdated drives mcp.ingest through
// the ingestSourceFn seam with a synthetic IngestRunResult containing
// PagesUpdated=2 and PagesUpdateFailed=1; the response payload must
// surface both keys.
func TestIngest_ReturnShapeIncludesPagesUpdated(t *testing.T) {
	deps, cleanup := newTestDeps(t, &fakeIngestClient{})
	defer cleanup()
	if err := os.MkdirAll(deps.Cfg.WikiDir, 0755); err != nil {
		t.Fatalf("mkdir wiki: %v", err)
	}

	tempDir := filepath.Dir(deps.Cfg.WikiDir)
	srcPath := filepath.Join(tempDir, "ingest-src.md")
	if err := os.WriteFile(srcPath, []byte("Goroutines are lightweight threads.\n"), 0644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	prev := ingestSourceFn
	defer func() { ingestSourceFn = prev }()
	ingestSourceFn = func(ctx context.Context, cfg wiki.IngestSourceConfig, database *db.DB, client llm.Client, source string, opts wiki.IngestOptions) (wiki.IngestRunResult, error) {
		return wiki.IngestRunResult{
			Source:            source,
			PagesUpdated:      2,
			PagesUpdateFailed: 1,
		}, nil
	}

	srv := NewServer(deps)
	c, done := connect(t, srv)
	defer done()

	res, text := callTool(t, c, "ingest", map[string]any{
		"source":          srcPath,
		"update_existing": true,
	})
	if res.IsError {
		t.Fatalf("ingest returned error: %s", text)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatalf("unmarshal: %v\nraw=%s", err, text)
	}
	pu, ok := got["pages_updated"]
	if !ok {
		t.Fatalf("response missing pages_updated key (raw=%s)", text)
	}
	if puf, _ := pu.(float64); int(puf) != 2 {
		t.Errorf("pages_updated = %v, want 2", got["pages_updated"])
	}
	puf, ok := got["pages_update_failed"]
	if !ok {
		t.Fatalf("response missing pages_update_failed key (raw=%s)", text)
	}
	if puff, _ := puf.(float64); int(puff) != 1 {
		t.Errorf("pages_update_failed = %v, want 1", got["pages_update_failed"])
	}
}

// TestIngest_ReturnShapePreservesV05Keys is a backwards-compat guard:
// every v0.5 key (source, pages_written, evidence_quotes, dropped_pages,
// skipped, retro_linked_pages, contradictions_flagged) must remain
// present in the v0.6 response payload, alongside the new
// pages_updated / pages_update_failed keys.
func TestIngest_ReturnShapePreservesV05Keys(t *testing.T) {
	deps, cleanup := newTestDeps(t, &fakeIngestClient{})
	defer cleanup()
	if err := os.MkdirAll(deps.Cfg.WikiDir, 0755); err != nil {
		t.Fatalf("mkdir wiki: %v", err)
	}

	tempDir := filepath.Dir(deps.Cfg.WikiDir)
	srcPath := filepath.Join(tempDir, "ingest-src.md")
	if err := os.WriteFile(srcPath, []byte("Goroutines are lightweight threads.\n"), 0644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	srv := NewServer(deps)
	c, done := connect(t, srv)
	defer done()

	res, text := callTool(t, c, "ingest", map[string]any{"source": srcPath})
	if res.IsError {
		t.Fatalf("ingest returned error: %s", text)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatalf("unmarshal: %v\nraw=%s", err, text)
	}
	for _, key := range []string{
		"source", "pages_written", "evidence_quotes", "dropped_pages",
		"skipped", "retro_linked_pages", "contradictions_flagged",
		"pages_updated", "pages_update_failed",
	} {
		if _, ok := got[key]; !ok {
			t.Errorf("response missing %q key (raw=%s)", key, text)
		}
	}
}

// TestIngestTool_DescriptionMentionsUpdateExisting asserts the ingest
// tool's input schema lists update_existing as a (boolean, optional)
// argument with a description.
func TestIngestTool_DescriptionMentionsUpdateExisting(t *testing.T) {
	deps, cleanup := newTestDeps(t, nil)
	defer cleanup()

	srv := NewServer(deps)
	tools := srv.ListTools()
	st, ok := tools["ingest"]
	if !ok {
		t.Fatalf("ingest tool not registered")
	}
	props := st.Tool.InputSchema.Properties
	prop, ok := props["update_existing"].(map[string]any)
	if !ok {
		t.Fatalf("update_existing property missing from ingest tool schema; properties=%v", props)
	}
	if t2, _ := prop["type"].(string); t2 != "boolean" {
		t.Errorf("update_existing type = %v, want boolean", prop["type"])
	}
	if desc, _ := prop["description"].(string); desc == "" {
		t.Errorf("update_existing description missing")
	}
	for _, r := range st.Tool.InputSchema.Required {
		if r == "update_existing" {
			t.Errorf("update_existing should not be required")
		}
	}
}

func TestPromoteAnswer_TitleCollision(t *testing.T) {
	deps, cleanup := newTestDeps(t, nil)
	defer cleanup()
	if err := os.MkdirAll(deps.Cfg.WikiDir, 0755); err != nil {
		t.Fatalf("mkdir wiki: %v", err)
	}

	source := "alpha\nthe validator drops unverified quotes\ngamma\n"
	answerPath, _ := seedPromoteFixture(t, deps,
		source,
		"title collision check?",
		"verbatim",
		"the validator drops unverified quotes",
	)

	// Pre-seed colliding page.
	preExistingPath := filepath.Join(deps.Cfg.WikiDir, "Validator Internals.md")
	if err := os.WriteFile(preExistingPath, []byte("---\ntitle: Validator Internals\n---\nseed body\n"), 0644); err != nil {
		t.Fatalf("seed page: %v", err)
	}
	if err := deps.DB.UpsertPage(db.PageRecord{
		Title: "Validator Internals", Path: preExistingPath, Body: "seed body", ContentHash: "h-seed",
	}); err != nil {
		t.Fatalf("seed UpsertPage: %v", err)
	}

	srv := NewServer(deps)
	c, done := connect(t, srv)
	defer done()

	res, text := callTool(t, c, "promote_answer", map[string]any{
		"answer_path": answerPath,
		"title":       "Validator Internals",
	})
	if !res.IsError {
		t.Fatalf("expected IsError=true; raw=%s", text)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatalf("error payload not JSON: %v\nraw=%s", err, text)
	}
	if got["code"] != "title_exists" {
		t.Errorf("code = %v, want title_exists", got["code"])
	}
	if got["existing_path"] != preExistingPath {
		t.Errorf("existing_path = %v, want %s", got["existing_path"], preExistingPath)
	}
}

// ----- get_schema tests (Phase I / sub-project 7) ----------------------

// validSchemaFixture is a syntactically-valid AGENTS.md / CLAUDE.md body
// the get_schema tests parse with schema.Parse to spin up Deps.Schema
// pointing at a custom schema. The Domain section is uniquely set so
// tests can assert the *active* schema (not the bundled default) is
// what flows through the handlers.
const validSchemaFixture = `---
schema_version: 1
generator: llmwiki-test
---

# llmwiki schema (mcp test fixture)

## Domain

Custom test domain for mcp.get_schema unit tests.

## Page ontology

  - title         (string)         the page's primary key; unique per wiki
  - body          (markdown)       the page's narrative
  - citations     (list of quotes) verbatim spans from sources; required, >= 1
  - links         (list)           Obsidian wikilinks declared structurally
  - sources       (list of paths)  derived from evidence; emitted by WritePage
  - tags          (list of strings) Obsidian/Dataview-friendly
  - created       (date)           first-ingest date
  - updated_at    (RFC3339 ts)     last-write timestamp
  - content_hash  (sha256)         body hash; recomputed at every write
  - source_ids    (list of int)    DB row IDs backing this page

## Ingest prompt

CUSTOM ingest prompt for tests. {{domain}} {{existing_titles}}

## Update-existing prompt

CUSTOM update prompt for tests. {{domain}} {{existing_page_body}} {{existing_evidence}}

## Ask prompt

CUSTOM ask prompt for tests. {{domain}}

## Contradiction prompt

CUSTOM contradiction prompt for tests.

## Promote rewrite prompt

CUSTOM promote rewrite. {{question}} {{answer_body}} {{evidence_quotes}}

## Lint contradictions prompt

CUSTOM lint contradictions prompt.

## Glossary

  - widget: a small thing
  - gizmo: a slightly larger small thing
`

// TestGetSchema_BundledByDefault drives the MCP server in-process with
// Deps.Schema = schema.Bundled() (the default newTestDeps wires) and
// asserts the get_schema payload matches the bundled state: doc_path
// is empty (no on-disk AGENTS.md / CLAUDE.md), hash equals
// schema.Bundled().Hash(), schema_version is 1, and ontology_fields
// equals the canonical bundled list.
func TestGetSchema_BundledByDefault(t *testing.T) {
	deps, cleanup := newTestDeps(t, nil)
	defer cleanup()

	srv := NewServer(deps)
	c, done := connect(t, srv)
	defer done()

	res, text := callTool(t, c, "get_schema", map[string]any{})
	if res.IsError {
		t.Fatalf("get_schema returned IsError=true: %s", text)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatalf("unmarshal: %v\nraw=%s", err, text)
	}

	if dp, _ := got["doc_path"].(string); dp != "" {
		t.Errorf("doc_path = %q, want \"\" (bundled has no on-disk path)", dp)
	}
	if h, _ := got["hash"].(string); h != schema.Bundled().Hash() {
		t.Errorf("hash = %q, want %q", h, schema.Bundled().Hash())
	}
	if sv, _ := got["schema_version"].(float64); int(sv) != 1 {
		t.Errorf("schema_version = %v, want 1", got["schema_version"])
	}
	rawFields, ok := got["ontology_fields"].([]any)
	if !ok {
		t.Fatalf("ontology_fields not []any (raw=%s)", text)
	}
	gotFields := make([]string, len(rawFields))
	for i, f := range rawFields {
		gotFields[i], _ = f.(string)
	}
	wantFields := []string{
		"title", "body", "evidence", "links", "sources",
		"tags", "created", "updated_at", "content_hash", "source_ids",
	}
	if !equalSlices(gotFields, wantFields) {
		t.Errorf("ontology_fields = %v, want %v", gotFields, wantFields)
	}
}

// TestGetSchema_ReturnsActivePromptsAndOntology spins up Deps.Schema
// from a parsed custom fixture (with `evidence` renamed to `citations`
// and uniquely-tagged prompts) and asserts the get_schema response
// surfaces the user's text — the raw template, not the rendered
// output. The DocPath round-trip ("AGENTS.md") is also asserted since
// that is the agent-facing signal for "this wiki has a hand-edited
// schema doc".
func TestGetSchema_ReturnsActivePromptsAndOntology(t *testing.T) {
	deps, cleanup := newTestDeps(t, nil)
	defer cleanup()

	custom, err := schema.Parse([]byte(validSchemaFixture))
	if err != nil {
		t.Fatalf("schema.Parse fixture: %v", err)
	}
	custom.DocPath = "AGENTS.md"
	deps.Schema = custom

	srv := NewServer(deps)
	c, done := connect(t, srv)
	defer done()

	res, text := callTool(t, c, "get_schema", map[string]any{})
	if res.IsError {
		t.Fatalf("get_schema returned IsError=true: %s", text)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatalf("unmarshal: %v\nraw=%s", err, text)
	}

	if dp, _ := got["doc_path"].(string); dp != "AGENTS.md" {
		t.Errorf("doc_path = %q, want %q", dp, "AGENTS.md")
	}
	if h, _ := got["hash"].(string); h != custom.Hash() {
		t.Errorf("hash = %q, want custom %q", h, custom.Hash())
	}

	prompts, ok := got["prompts"].(map[string]any)
	if !ok {
		t.Fatalf("prompts not map[string]any (raw=%s)", text)
	}
	// Verify the raw template is what flows through — not the rendered
	// output. Each prompt body in the fixture starts with "CUSTOM ".
	wantContains := map[string]string{
		"ingest":              "CUSTOM ingest prompt for tests.",
		"update_existing":     "CUSTOM update prompt for tests.",
		"ask":                 "CUSTOM ask prompt for tests.",
		"contradiction":       "CUSTOM contradiction prompt for tests.",
		"promote_rewrite":     "CUSTOM promote rewrite.",
		"lint_contradictions": "CUSTOM lint contradictions prompt.",
	}
	for name, want := range wantContains {
		body, _ := prompts[name].(string)
		if !strings.Contains(body, want) {
			t.Errorf("prompts[%q] = %q, want it to contain %q", name, body, want)
		}
	}
	// Raw templates must contain the {{placeholder}} tokens — the
	// server renders them at LLM-call time, not at get_schema time.
	if !strings.Contains(prompts["ingest"].(string), "{{domain}}") {
		t.Errorf("ingest prompt must keep {{domain}} placeholder unrendered (raw=%s)", prompts["ingest"])
	}
	if !strings.Contains(prompts["ingest"].(string), "{{existing_titles}}") {
		t.Errorf("ingest prompt must keep {{existing_titles}} placeholder unrendered (raw=%s)", prompts["ingest"])
	}

	// ontology_fields uses DeclaredName, so the renamed `citations`
	// shows up in the agent-facing surface.
	rawFields, ok := got["ontology_fields"].([]any)
	if !ok {
		t.Fatalf("ontology_fields not []any (raw=%s)", text)
	}
	hasCitations := false
	for _, f := range rawFields {
		if s, _ := f.(string); s == "citations" {
			hasCitations = true
		}
	}
	if !hasCitations {
		t.Errorf("ontology_fields = %v, want it to contain renamed declared name \"citations\"", rawFields)
	}

	// Glossary round-trip: the fixture lists two terms.
	glossary, ok := got["glossary"].([]any)
	if !ok {
		t.Fatalf("glossary not []any (raw=%s)", text)
	}
	if len(glossary) != 2 {
		t.Errorf("glossary length = %d, want 2 (raw=%s)", len(glossary), text)
	}

	if dom, _ := got["domain"].(string); !strings.Contains(dom, "Custom test domain") {
		t.Errorf("domain = %q, want it to mention 'Custom test domain'", dom)
	}
}

// TestGetSchema_ReadOnly_NoSetSchemaTool walks the registered tool list
// and asserts no tool name carries any schema-mutating shape. Q15 — the
// schema is the user's; agents introspect, they do not edit. An agent
// that can rewrite the system prompts an agent runs against is a
// confused-deputy surface.
func TestGetSchema_ReadOnly_NoSetSchemaTool(t *testing.T) {
	deps, cleanup := newTestDeps(t, nil)
	defer cleanup()

	srv := NewServer(deps)
	tools := srv.ListTools()
	for name := range tools {
		lower := strings.ToLower(name)
		for _, banned := range []string{"set_schema", "write_schema", "update_schema", "edit_schema", "modify_schema", "patch_schema"} {
			if strings.Contains(lower, banned) {
				t.Errorf("tool %q contains banned schema-mutating substring %q (Q15: read-only is the contract)", name, banned)
			}
		}
	}
}

// TestGetSchema_ResponseShape pins the exact JSON keys of the
// get_schema response. Sub-project 7 / Phase I Task 14 fixes the
// payload shape; downstream agents will key off it. This test catches
// accidental key renames or omissions.
func TestGetSchema_ResponseShape(t *testing.T) {
	deps, cleanup := newTestDeps(t, nil)
	defer cleanup()

	srv := NewServer(deps)
	c, done := connect(t, srv)
	defer done()

	res, text := callTool(t, c, "get_schema", map[string]any{})
	if res.IsError {
		t.Fatalf("get_schema returned IsError=true: %s", text)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatalf("unmarshal: %v\nraw=%s", err, text)
	}

	wantTopKeys := []string{
		"schema_version", "domain", "ontology_fields",
		"prompts", "glossary", "hash", "doc_path",
	}
	for _, k := range wantTopKeys {
		if _, ok := got[k]; !ok {
			t.Errorf("response missing top-level key %q (raw=%s)", k, text)
		}
	}
	gotTop := make([]string, 0, len(got))
	for k := range got {
		gotTop = append(gotTop, k)
	}
	sort.Strings(gotTop)
	sortedWant := append([]string(nil), wantTopKeys...)
	sort.Strings(sortedWant)
	if !equalSlices(gotTop, sortedWant) {
		t.Errorf("response keys = %v, want exactly %v", gotTop, sortedWant)
	}

	prompts, ok := got["prompts"].(map[string]any)
	if !ok {
		t.Fatalf("prompts not map (raw=%s)", text)
	}
	wantPromptKeys := []string{
		"ingest", "update_existing", "ask", "contradiction",
		"promote_rewrite", "lint_contradictions",
	}
	for _, k := range wantPromptKeys {
		if _, ok := prompts[k]; !ok {
			t.Errorf("prompts missing key %q (raw=%s)", k, text)
		}
	}
	gotPrompt := make([]string, 0, len(prompts))
	for k := range prompts {
		gotPrompt = append(gotPrompt, k)
	}
	sort.Strings(gotPrompt)
	sortedWantPrompts := append([]string(nil), wantPromptKeys...)
	sort.Strings(sortedWantPrompts)
	if !equalSlices(gotPrompt, sortedWantPrompts) {
		t.Errorf("prompts keys = %v, want exactly %v", gotPrompt, sortedWantPrompts)
	}
}

// promptCapturingClient records every (system, user) prompt pair sent
// through Complete / CompleteStream so TestMCPHandlersThreadSchema_NotJustBundled
// can assert the active schema's text — not the bundled default —
// flows through the prompt path.
type promptCapturingClient struct {
	systemPrompts []string
	userPrompts   []string
}

func (p *promptCapturingClient) Complete(ctx context.Context, system, user string) (string, error) {
	p.systemPrompts = append(p.systemPrompts, system)
	p.userPrompts = append(p.userPrompts, user)
	return "stub answer", nil
}

func (p *promptCapturingClient) CompleteStructured(ctx context.Context, system, user string, ts llm.ToolSchema) (map[string]any, error) {
	p.systemPrompts = append(p.systemPrompts, system)
	p.userPrompts = append(p.userPrompts, user)
	return map[string]any{}, nil
}

func (p *promptCapturingClient) CompleteStream(ctx context.Context, system, user string, w io.Writer) (string, error) {
	p.systemPrompts = append(p.systemPrompts, system)
	p.userPrompts = append(p.userPrompts, user)
	if _, err := w.Write([]byte("stub answer")); err != nil {
		return "", err
	}
	return "stub answer", nil
}

// TestMCPHandlersThreadSchema_NotJustBundled drives askHandler with
// Deps.Schema set to a custom parsed schema and asserts the system
// prompt sent to the LLM contains the custom prompt body — not the
// bundled default. This is the load-bearing test for Phase I: it
// proves the handlers actually thread d.Schema through to the wiki
// entrypoints rather than silently falling back to schema.Bundled().
func TestMCPHandlersThreadSchema_NotJustBundled(t *testing.T) {
	pc := &promptCapturingClient{}
	deps, cleanup := newTestDeps(t, pc)
	defer cleanup()

	custom, err := schema.Parse([]byte(validSchemaFixture))
	if err != nil {
		t.Fatalf("schema.Parse fixture: %v", err)
	}
	deps.Schema = custom

	// Seed a page so askHandler has something to bundle into the prompt.
	srcID, _ := deps.DB.UpsertSource("seed:custom-schema", "h")
	sfID, _ := deps.DB.UpsertSourceFile(db.SourceFile{
		SourceID: srcID, RelativePath: "guide.md", ContentHash: "h", ByteSize: 4, LineCount: 4,
	})
	if err := deps.DB.UpsertPage(db.PageRecord{
		Title: "Channels", Path: "Channels.md", Body: "Channel body discusses blocking.",
		ContentHash: "abc", SourceIDs: []int64{srcID},
	}); err != nil {
		t.Fatalf("UpsertPage: %v", err)
	}
	p, _ := deps.DB.GetPage("Channels")
	_ = deps.DB.InsertEvidence(p.ID, srcID, []db.Evidence{
		{Quote: "channels block when full", LineStart: 4, LineEnd: 4, SourceFileID: &sfID},
	})

	srv := NewServer(deps)
	c, done := connect(t, srv)
	defer done()

	if _, _ = callTool(t, c, "ask", map[string]any{"question": "what about channels?"}); len(pc.systemPrompts) == 0 {
		t.Fatalf("no LLM prompt captured by stub client")
	}
	// The custom ask prompt's distinctive marker is "CUSTOM ask
	// prompt for tests." — if the handler had silently used
	// schema.Bundled() we would see the bundled "You answer using the
	// provided wiki pages and source quotes." instead.
	combined := strings.Join(pc.systemPrompts, "\n----\n") + strings.Join(pc.userPrompts, "\n----\n")
	if !strings.Contains(combined, "CUSTOM ask prompt for tests.") {
		t.Errorf("captured prompts do not contain the custom schema's ask body; raw system prompts:\n%s",
			strings.Join(pc.systemPrompts, "\n---\n"))
	}
	// Negative assertion: the bundled default's distinctive opener
	// must NOT appear when a custom schema is active.
	if strings.Contains(combined, "You answer using the provided wiki pages and source quotes.") {
		t.Errorf("bundled default ask prompt leaked into prompt path despite custom schema being active")
	}
}
