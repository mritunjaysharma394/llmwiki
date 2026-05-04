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
