package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// OpenAICompatClient speaks the OpenAI Chat-Completions schema against any
// base URL that re-implements it: Groq, OpenRouter, Together, Cerebras,
// Mistral La Plateforme, and OpenAI itself. Because all five vendors accept
// the same conservatively-typed body, we send one request shape and gate any
// vendor-specific quirks behind small conditional blocks if and only if a
// fixture forces it.
type OpenAICompatClient struct {
	baseURL string
	apiKey  string
	model   string
	http    *http.Client
}

// NewOpenAICompatClient constructs a client targeting baseURL (no trailing
// slash; we append /chat/completions). apiKey is sent as `Authorization:
// Bearer <apiKey>`. model is the provider-specific model id.
func NewOpenAICompatClient(baseURL, apiKey, model string) *OpenAICompatClient {
	return &OpenAICompatClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		model:   model,
		http:    http.DefaultClient,
	}
}

type openAIMessage struct {
	Role       string           `json:"role"`
	Content    string           `json:"content,omitempty"`
	ToolCalls  []openAIToolCall `json:"tool_calls,omitempty"`
}

type openAIToolCall struct {
	ID       string             `json:"id"`
	Type     string             `json:"type"`
	Function openAIToolCallFunc `json:"function"`
}

type openAIToolCallFunc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type openAIChoice struct {
	Index        int           `json:"index"`
	Message      openAIMessage `json:"message"`
	FinishReason string        `json:"finish_reason,omitempty"`
}

type openAIResponse struct {
	Choices []openAIChoice `json:"choices"`
}

type openAIRequest struct {
	Model      string          `json:"model"`
	Messages   []openAIMessage `json:"messages"`
	Stream     bool            `json:"stream,omitempty"`
	Tools      []openAITool    `json:"tools,omitempty"`
	ToolChoice any             `json:"tool_choice,omitempty"`
}

type openAITool struct {
	Type     string         `json:"type"`
	Function openAIFunction `json:"function"`
}

type openAIFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters"`
}

// post sends one POST request and returns the raw response body bytes if 2xx.
// On non-2xx it returns an error containing both the status code and the
// response body so callers (and cliutil.UserError) can render usefully.
func (c *OpenAICompatClient) post(ctx context.Context, body []byte) (*http.Response, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		errBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("openai-compat: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(errBody)))
	}
	return resp, nil
}

func (c *OpenAICompatClient) Complete(ctx context.Context, system, user string) (string, error) {
	reqBody, err := json.Marshal(openAIRequest{
		Model: c.model,
		Messages: []openAIMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
	})
	if err != nil {
		return "", err
	}
	resp, err := c.post(ctx, reqBody)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	var out openAIResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return "", fmt.Errorf("parsing openai-compat response: %w", err)
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("openai-compat: no choices in response")
	}
	return out.Choices[0].Message.Content, nil
}

func (c *OpenAICompatClient) CompleteStructured(ctx context.Context, system, user string, ts ToolSchema) (map[string]any, error) {
	reqBody, err := json.Marshal(openAIRequest{
		Model: c.model,
		Messages: []openAIMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
		Tools: []openAITool{{
			Type: "function",
			Function: openAIFunction{
				Name:        ts.Name,
				Description: ts.Description,
				Parameters: map[string]any{
					"type":       "object",
					"properties": ts.Properties,
					"required":   ts.Required,
				},
			},
		}},
		ToolChoice: map[string]any{
			"type":     "function",
			"function": map[string]any{"name": ts.Name},
		},
	})
	if err != nil {
		return nil, err
	}
	resp, err := c.post(ctx, reqBody)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var out openAIResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("parsing openai-compat response: %w", err)
	}
	if len(out.Choices) == 0 {
		return nil, fmt.Errorf("openai-compat: no choices in response")
	}
	msg := out.Choices[0].Message

	// Preferred path: the model called the tool. Decode arguments string as JSON.
	for _, tc := range msg.ToolCalls {
		if tc.Function.Arguments == "" {
			continue
		}
		var result map[string]any
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &result); err != nil {
			return nil, fmt.Errorf("parsing tool-call arguments: %w\nraw: %s", err, tc.Function.Arguments)
		}
		return result, nil
	}

	// Fallback path: cheap free-tier models sometimes drop the tool call and
	// emit JSON (often wrapped in prose / code fences) in message.content.
	// Mirror OllamaClient.CompleteStructured (ollama.go:104-111): TrimSpace,
	// drop everything before the first '{', drop everything after the last
	// '}', then attempt Unmarshal. Duplicated rather than DRY'd because the
	// Gemini client (Phase B) needs its own near-copy too; we'll factor in
	// v1.2 if a third caller appears.
	raw := strings.TrimSpace(msg.Content)
	if raw == "" {
		return nil, fmt.Errorf("openai-compat: no tool_calls and empty content")
	}
	if idx := strings.Index(raw, "{"); idx > 0 {
		raw = raw[idx:]
	}
	if idx := strings.LastIndex(raw, "}"); idx >= 0 && idx < len(raw)-1 {
		raw = raw[:idx+1]
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, fmt.Errorf("openai-compat: no tool_calls and content not parseable as JSON: %w\nraw: %s", err, raw)
	}
	return result, nil
}

// CompleteStream POSTs with stream:true and parses the SSE event stream. Each
// frame is `data: {json}\n\n`; we extract choices[0].delta.content, write it
// to w, and accumulate. The terminator frame is `data: [DONE]`.
func (c *OpenAICompatClient) CompleteStream(ctx context.Context, system, user string, w io.Writer) (string, error) {
	reqBody, err := json.Marshal(openAIRequest{
		Model: c.model,
		Messages: []openAIMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
		Stream: true,
	})
	if err != nil {
		return "", err
	}
	resp, err := c.post(ctx, reqBody)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	// SSE frames can carry chunks larger than the default 64KB scanner buffer;
	// raise the cap so a wide chat completion doesn't truncate mid-frame.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var full strings.Builder
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			break
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			// Tolerate vendor-specific keepalive frames ("ping" etc.) by skipping
			// any frame we can't decode rather than aborting the stream.
			continue
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		text := chunk.Choices[0].Delta.Content
		if text == "" {
			continue
		}
		if _, err := io.WriteString(w, text); err != nil {
			return "", err
		}
		full.WriteString(text)
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return full.String(), nil
}

var _ Client = (*OpenAICompatClient)(nil)
