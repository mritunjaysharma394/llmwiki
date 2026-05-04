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

// readReqBody decodes the JSON body of an incoming chat-completions request.
func readReqBody(t *testing.T, r *http.Request) map[string]any {
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

// TestOpenAICompatComplete_HappyPath asserts a non-streaming chat completion
// returns the assistant content and that the outbound request carries
// model + messages + Authorization: Bearer <key>.
func TestOpenAICompatComplete_HappyPath(t *testing.T) {
	var gotAuth string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		gotBody = readReqBody(t, r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"hello"}}]}`)
	}))
	defer srv.Close()

	c := NewOpenAICompatClient(srv.URL, "sk-test", "gpt-test")
	got, err := c.Complete(context.Background(), "sys", "user-prompt")
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got != "hello" {
		t.Errorf("got %q, want %q", got, "hello")
	}
	if gotAuth != "Bearer sk-test" {
		t.Errorf("Authorization = %q, want Bearer sk-test", gotAuth)
	}
	if gotBody["model"] != "gpt-test" {
		t.Errorf("model = %v, want gpt-test", gotBody["model"])
	}
	msgs, _ := gotBody["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("messages len = %d, want 2", len(msgs))
	}
	first, _ := msgs[0].(map[string]any)
	if first["role"] != "system" || first["content"] != "sys" {
		t.Errorf("system message = %+v", first)
	}
	second, _ := msgs[1].(map[string]any)
	if second["role"] != "user" || second["content"] != "user-prompt" {
		t.Errorf("user message = %+v", second)
	}
}

// TestOpenAICompatCompleteStructured_HappyPath asserts the tool-call branch:
// the server returns choices[0].message.tool_calls[0].function.arguments and
// the client decodes arguments into the result map.
func TestOpenAICompatCompleteStructured_HappyPath(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody = readReqBody(t, r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"write_pages","arguments":"{\"pages\":[]}"}}]}}]}`)
	}))
	defer srv.Close()

	c := NewOpenAICompatClient(srv.URL, "sk-test", "gpt-test")
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

	// Verify request shape: tools[0].function.name + tool_choice forced to the named function.
	tools, _ := gotBody["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools len = %d, want 1", len(tools))
	}
	tool0, _ := tools[0].(map[string]any)
	if tool0["type"] != "function" {
		t.Errorf("tools[0].type = %v, want function", tool0["type"])
	}
	fn, _ := tool0["function"].(map[string]any)
	if fn["name"] != "write_pages" {
		t.Errorf("tools[0].function.name = %v, want write_pages", fn["name"])
	}
	params, _ := fn["parameters"].(map[string]any)
	if params["type"] != "object" {
		t.Errorf("parameters.type = %v, want object", params["type"])
	}
	if _, ok := params["properties"]; !ok {
		t.Errorf("parameters missing properties: %+v", params)
	}
	choice, _ := gotBody["tool_choice"].(map[string]any)
	if choice["type"] != "function" {
		t.Errorf("tool_choice.type = %v, want function", choice["type"])
	}
	cf, _ := choice["function"].(map[string]any)
	if cf["name"] != "write_pages" {
		t.Errorf("tool_choice.function.name = %v, want write_pages", cf["name"])
	}
}

// TestOpenAICompatCompleteStructured_FallbackJSONExtraction asserts the
// fallback path: when the model fails to call the tool but produces JSON in
// message.content (possibly with prose / code fences), the client strips the
// non-JSON noise and unmarshals.
func TestOpenAICompatCompleteStructured_FallbackJSONExtraction(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// content has leading prose, a markdown fence, and trailing junk.
		body := `{"choices":[{"message":{"role":"assistant","content":"Sure, here you go: ` + "```json\\n" + `{\"pages\":[{\"title\":\"A\",\"body\":\"b\"}]}` + "\\n```" + ` -- done"}}]}`
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()

	c := NewOpenAICompatClient(srv.URL, "sk-test", "gpt-test")
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

// TestOpenAICompat4xx asserts that a non-2xx response surfaces both the HTTP
// status code and the upstream error message body so cliutil.UserError can
// render something a human can act on.
func TestOpenAICompat4xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"invalid api key"}}`)
	}))
	defer srv.Close()

	c := NewOpenAICompatClient(srv.URL, "sk-bad", "gpt-test")
	_, err := c.Complete(context.Background(), "sys", "u")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "401") {
		t.Errorf("error %q missing status 401", msg)
	}
	if !strings.Contains(msg, "invalid api key") {
		t.Errorf("error %q missing upstream message", msg)
	}
}
