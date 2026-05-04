package mcp

import (
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	mcpc "github.com/mark3labs/mcp-go/client"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/mritunjaysharma394/llmwiki/internal/db"
	"github.com/mritunjaysharma394/llmwiki/internal/llm"
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
// caller-provided fixtures, and returns a Deps wired with a fakeClient.
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

func TestNewServer_RegistersAllSixTools(t *testing.T) {
	deps, cleanup := newTestDeps(t, nil)
	defer cleanup()

	srv := NewServer(deps)
	tools := srv.ListTools()

	got := make([]string, 0, len(tools))
	for name := range tools {
		got = append(got, name)
	}
	sort.Strings(got)
	want := []string{"ask", "ingest", "lint", "list_pages", "read_page", "write_page"}
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
