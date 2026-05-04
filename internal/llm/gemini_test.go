package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// readGeminiReqBody decodes the JSON body of an incoming generateContent
// request. Provider-local helper rather than sharing with openai_compat_test
// per the plan's "duplicate not DRY" guidance — provider tests stay
// self-contained so their request schemas can drift independently. The name
// is provider-prefixed because Go's package-level test scope does collide if
// two files declare the same helper.
func readGeminiReqBody(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	data, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read req body: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode req body: %v\nraw: %s", err, data)
	}
	return got
}

// withGeminiBaseURL temporarily redirects the package-level geminiBaseURL at
// the unexported var so the client points at the httptest server. The test
// restores the original on cleanup.
func withGeminiBaseURL(t *testing.T, url string) {
	t.Helper()
	prev := geminiBaseURL
	geminiBaseURL = url
	t.Cleanup(func() { geminiBaseURL = prev })
}

// TestGeminiComplete_HappyPath asserts a non-streaming generateContent call
// returns the candidate text and that the outbound request carries
// model + contents + systemInstruction + key query parameter.
func TestGeminiComplete_HappyPath(t *testing.T) {
	var gotPath string
	var gotKey string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotKey = r.URL.Query().Get("key")
		gotBody = readGeminiReqBody(t, r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"candidates":[{"content":{"parts":[{"text":"hello"}]}}]}`)
	}))
	defer srv.Close()
	withGeminiBaseURL(t, srv.URL)

	t.Setenv("GEMINI_API_KEY", "k-test")
	c := NewGeminiClient("gemini-test")
	got, err := c.Complete(context.Background(), "sys", "user-prompt")
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got != "hello" {
		t.Errorf("got %q, want %q", got, "hello")
	}
	if gotPath != "/models/gemini-test:generateContent" {
		t.Errorf("path = %q, want /models/gemini-test:generateContent", gotPath)
	}
	if gotKey != "k-test" {
		t.Errorf("key = %q, want k-test", gotKey)
	}
	contents, _ := gotBody["contents"].([]any)
	if len(contents) != 1 {
		t.Fatalf("contents len = %d, want 1", len(contents))
	}
	first, _ := contents[0].(map[string]any)
	if first["role"] != "user" {
		t.Errorf("contents[0].role = %v, want user", first["role"])
	}
	parts, _ := first["parts"].([]any)
	if len(parts) != 1 {
		t.Fatalf("contents[0].parts len = %d, want 1", len(parts))
	}
	p0, _ := parts[0].(map[string]any)
	if p0["text"] != "user-prompt" {
		t.Errorf("contents[0].parts[0].text = %v, want user-prompt", p0["text"])
	}
	sys, _ := gotBody["systemInstruction"].(map[string]any)
	sysParts, _ := sys["parts"].([]any)
	if len(sysParts) != 1 {
		t.Fatalf("systemInstruction.parts len = %d, want 1", len(sysParts))
	}
	sp0, _ := sysParts[0].(map[string]any)
	if sp0["text"] != "sys" {
		t.Errorf("systemInstruction.parts[0].text = %v, want sys", sp0["text"])
	}
}

// TestGeminiCompleteStructured_HappyPath asserts the functionCall branch:
// the server returns candidates[0].content.parts[0].functionCall.args and
// the client returns it as the result map. Also asserts the request body
// carries the expected tools + toolConfig shape.
func TestGeminiCompleteStructured_HappyPath(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody = readGeminiReqBody(t, r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"candidates":[{"content":{"parts":[{"functionCall":{"name":"write_pages","args":{"pages":[]}}}]}}]}`)
	}))
	defer srv.Close()
	withGeminiBaseURL(t, srv.URL)

	t.Setenv("GEMINI_API_KEY", "k-test")
	c := NewGeminiClient("gemini-test")
	ts := ToolSchema{
		Name:        "write_pages",
		Description: "Write pages",
		Properties:  map[string]any{"pages": map[string]any{"type": "array"}},
		Required:    []string{"pages"},
	}
	got, err := c.CompleteStructured(context.Background(), "sys", "u", ts)
	if err != nil {
		t.Fatalf("CompleteStructured: %v", err)
	}
	if _, ok := got["pages"]; !ok {
		t.Errorf("result missing 'pages' key: %+v", got)
	}

	// tools[0].functionDeclarations[0].name == "write_pages"
	tools, _ := gotBody["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools len = %d, want 1", len(tools))
	}
	tool0, _ := tools[0].(map[string]any)
	fns, _ := tool0["functionDeclarations"].([]any)
	if len(fns) != 1 {
		t.Fatalf("functionDeclarations len = %d, want 1", len(fns))
	}
	fn0, _ := fns[0].(map[string]any)
	if fn0["name"] != "write_pages" {
		t.Errorf("functionDeclarations[0].name = %v, want write_pages", fn0["name"])
	}
	params, _ := fn0["parameters"].(map[string]any)
	if params["type"] != "OBJECT" {
		t.Errorf("parameters.type = %v, want OBJECT", params["type"])
	}
	if _, ok := params["properties"]; !ok {
		t.Errorf("parameters missing properties: %+v", params)
	}

	// toolConfig.functionCallingConfig.mode == "ANY", allowedFunctionNames == ["write_pages"]
	tc, _ := gotBody["toolConfig"].(map[string]any)
	fcc, _ := tc["functionCallingConfig"].(map[string]any)
	if fcc["mode"] != "ANY" {
		t.Errorf("functionCallingConfig.mode = %v, want ANY", fcc["mode"])
	}
	allowed, _ := fcc["allowedFunctionNames"].([]any)
	if len(allowed) != 1 || allowed[0] != "write_pages" {
		t.Errorf("allowedFunctionNames = %+v, want [write_pages]", allowed)
	}
}

// TestGeminiCompleteStructured_FallbackJSONExtraction asserts the fallback
// path: when the model emits a text part with JSON instead of a functionCall,
// the client strips prose / fences and unmarshals.
func TestGeminiCompleteStructured_FallbackJSONExtraction(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// text part wraps JSON in prose + a markdown fence + trailing junk.
		body := `{"candidates":[{"content":{"parts":[{"text":"Sure, here you go: ` + "```json\\n" + `{\"pages\":[{\"title\":\"A\",\"body\":\"b\"}]}` + "\\n```" + ` -- done"}]}}]}`
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()
	withGeminiBaseURL(t, srv.URL)

	t.Setenv("GEMINI_API_KEY", "k-test")
	c := NewGeminiClient("gemini-test")
	ts := ToolSchema{Name: "write_pages"}
	got, err := c.CompleteStructured(context.Background(), "sys", "u", ts)
	if err != nil {
		t.Fatalf("CompleteStructured fallback: %v", err)
	}
	pages, ok := got["pages"].([]any)
	if !ok {
		t.Fatalf("result missing 'pages' array: %+v", got)
	}
	if len(pages) != 1 {
		t.Errorf("pages len = %d, want 1", len(pages))
	}
}

// TestGemini4xx asserts that a non-2xx response surfaces both the HTTP status
// and the upstream error body so cmd/ Wrap() can render an AI Studio
// remediation hint for region-restricted 403s.
func TestGemini4xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"error":{"code":403,"message":"API not enabled in region X"}}`)
	}))
	defer srv.Close()
	withGeminiBaseURL(t, srv.URL)

	t.Setenv("GEMINI_API_KEY", "k-bad")
	c := NewGeminiClient("gemini-test")
	_, err := c.Complete(context.Background(), "sys", "u")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "403") {
		t.Errorf("error %q missing status 403", msg)
	}
	if !strings.Contains(msg, "API not enabled in region X") {
		t.Errorf("error %q missing upstream message", msg)
	}
}
