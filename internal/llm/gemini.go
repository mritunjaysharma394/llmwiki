package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

// GeminiClient speaks Gemini's v1beta REST API directly against
// generativelanguage.googleapis.com. We deliberately avoid pulling
// google.golang.org/api (a large transitive cost the spec explicitly declines)
// and instead hand-roll generateContent / streamGenerateContent. The API key
// is read from GEMINI_API_KEY at request time so a missing-key error surfaces
// as part of a Wrap()-able request failure rather than at construction.
type GeminiClient struct {
	model string
	http  *http.Client
}

// geminiBaseURL is a package-level var (not a const) so tests can redirect it
// at an httptest.NewServer. Mirrors the testability seam Phase A used for the
// OpenAI-compat client by accepting an injectable baseURL constructor arg —
// Gemini's URL is hardcoded for production callers, so we expose the seam via
// a var override instead.
var geminiBaseURL = "https://generativelanguage.googleapis.com/v1beta"

// NewGeminiClient targets gemini-2.0-flash by default in cmd/ but accepts any
// model id the caller pins. The API key is intentionally NOT read here; see
// resolveAPIKey().
func NewGeminiClient(model string) *GeminiClient {
	return &GeminiClient{
		model: model,
		http:  http.DefaultClient,
	}
}

// resolveAPIKey reads GEMINI_API_KEY at the moment of the request rather than
// at construction. Rationale: a missing key should surface via the same error
// path as a 401 — i.e. routable through cliutil.UserError on the request edge
// — rather than crashing the constructor before the caller has a chance to
// install a remediation hint.
func (c *GeminiClient) resolveAPIKey() (string, error) {
	key := os.Getenv("GEMINI_API_KEY")
	if key == "" {
		return "", fmt.Errorf("gemini: GEMINI_API_KEY environment variable not set")
	}
	return key, nil
}

type geminiPart struct {
	Text         string              `json:"text,omitempty"`
	FunctionCall *geminiFunctionCall `json:"functionCall,omitempty"`
}

type geminiFunctionCall struct {
	Name string         `json:"name"`
	Args map[string]any `json:"args"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiCandidate struct {
	Content geminiContent `json:"content"`
}

type geminiResponse struct {
	Candidates []geminiCandidate `json:"candidates"`
}

// post sends one POST to {geminiBaseURL}/models/{model}:{action} (with optional
// extra query params) and returns the response on 2xx. On non-2xx it wraps
// the body so cmd/ Wrap() can surface AI-Studio-specific remediation (e.g.
// the region-restricted 403 hint).
func (c *GeminiClient) post(ctx context.Context, action string, extraQuery string, body []byte) (*http.Response, error) {
	key, err := c.resolveAPIKey()
	if err != nil {
		return nil, err
	}
	url := fmt.Sprintf("%s/models/%s:%s?key=%s", geminiBaseURL, c.model, action, key)
	if extraQuery != "" {
		url += "&" + extraQuery
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		errBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("gemini: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(errBody)))
	}
	return resp, nil
}

// buildContents constructs the request body shared by Complete and
// CompleteStream. Gemini distinguishes the system role via a top-level
// systemInstruction field rather than an in-line role: "system" message.
func buildGeminiBody(system, user string, extras map[string]any) ([]byte, error) {
	body := map[string]any{
		"contents": []map[string]any{
			{
				"role":  "user",
				"parts": []map[string]any{{"text": user}},
			},
		},
	}
	if system != "" {
		body["systemInstruction"] = map[string]any{
			"parts": []map[string]any{{"text": system}},
		}
	}
	for k, v := range extras {
		body[k] = v
	}
	return json.Marshal(body)
}

func (c *GeminiClient) Complete(ctx context.Context, system, user string) (string, error) {
	reqBody, err := buildGeminiBody(system, user, nil)
	if err != nil {
		return "", err
	}
	resp, err := c.post(ctx, "generateContent", "", reqBody)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	var out geminiResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return "", fmt.Errorf("parsing gemini response: %w", err)
	}
	if len(out.Candidates) == 0 {
		return "", fmt.Errorf("gemini: no candidates in response")
	}
	var sb strings.Builder
	for _, p := range out.Candidates[0].Content.Parts {
		sb.WriteString(p.Text)
	}
	return sb.String(), nil
}

func (c *GeminiClient) CompleteStructured(ctx context.Context, system, user string, ts ToolSchema) (map[string]any, error) {
	// Inline tool-schema marshaling — mirroring Phase A's openai_compat.go,
	// which kept the request body literal inline rather than abstracting a
	// shared builder. We'll factor in v1.2 if a third caller appears.
	reqBody, err := buildGeminiBody(system, user, map[string]any{
		"tools": []map[string]any{
			{
				"functionDeclarations": []map[string]any{
					{
						"name":        ts.Name,
						"description": ts.Description,
						"parameters": map[string]any{
							"type":       "OBJECT",
							"properties": ts.Properties,
							"required":   ts.Required,
						},
					},
				},
			},
		},
		"toolConfig": map[string]any{
			"functionCallingConfig": map[string]any{
				"mode":                 "ANY",
				"allowedFunctionNames": []string{ts.Name},
			},
		},
	})
	if err != nil {
		return nil, err
	}
	resp, err := c.post(ctx, "generateContent", "", reqBody)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var out geminiResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("parsing gemini response: %w", err)
	}
	if len(out.Candidates) == 0 {
		return nil, fmt.Errorf("gemini: no candidates in response")
	}
	parts := out.Candidates[0].Content.Parts

	// Preferred path: the model called the tool. args is already a decoded
	// JSON object — Gemini returns it as nested JSON, not a serialized string
	// (this is the structural shape difference from OpenAI's tool_calls).
	for _, p := range parts {
		if p.FunctionCall != nil && p.FunctionCall.Args != nil {
			return p.FunctionCall.Args, nil
		}
	}

	// Fallback path: cheap free-tier responses sometimes drop the tool call
	// and emit JSON in a text part (often wrapped in prose / fences). Mirror
	// openai_compat.go:204-217 — TrimSpace, drop everything before the first
	// '{', drop everything after the last '}', then attempt Unmarshal.
	// Duplicated rather than DRY'd per the plan's per-provider guidance.
	var raw string
	for _, p := range parts {
		if p.Text != "" {
			raw = p.Text
			break
		}
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("gemini: no functionCall and empty text part")
	}
	if idx := strings.Index(raw, "{"); idx > 0 {
		raw = raw[idx:]
	}
	if idx := strings.LastIndex(raw, "}"); idx >= 0 && idx < len(raw)-1 {
		raw = raw[:idx+1]
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, fmt.Errorf("gemini: no functionCall and text not parseable as JSON: %w\nraw: %s", err, raw)
	}
	return result, nil
}

// CompleteStream POSTs to streamGenerateContent?alt=sse and parses the SSE
// frames. Each frame is `data: {json}\n\n` carrying a partial candidate; we
// pull candidates[0].content.parts[0].text from each, write to w, and
// accumulate. Unlike OpenAI's stream there is no `[DONE]` sentinel — the
// server simply closes the connection at the end.
func (c *GeminiClient) CompleteStream(ctx context.Context, system, user string, w io.Writer) (string, error) {
	reqBody, err := buildGeminiBody(system, user, nil)
	if err != nil {
		return "", err
	}
	resp, err := c.post(ctx, "streamGenerateContent", "alt=sse", reqBody)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	// SSE frames can carry chunks larger than the default 64KB scanner buffer;
	// raise the cap so a wide completion doesn't truncate mid-frame.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var full strings.Builder
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" {
			continue
		}
		var chunk geminiResponse
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			// Tolerate vendor-specific keepalive frames by skipping any frame
			// we can't decode rather than aborting the stream. Mirrors Phase
			// A's openai_compat.go SSE handler.
			continue
		}
		if len(chunk.Candidates) == 0 {
			continue
		}
		for _, p := range chunk.Candidates[0].Content.Parts {
			if p.Text == "" {
				continue
			}
			if _, err := io.WriteString(w, p.Text); err != nil {
				return "", err
			}
			full.WriteString(p.Text)
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return full.String(), nil
}

var _ Client = (*GeminiClient)(nil)
