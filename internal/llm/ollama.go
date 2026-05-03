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

type OllamaClient struct {
	model  string
	apiURL string
}

func NewOllamaClient(model, apiURL string) *OllamaClient {
	return &OllamaClient{model: model, apiURL: apiURL}
}

type ollamaRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
	System string `json:"system"`
	Format string `json:"format,omitempty"`
	Stream bool   `json:"stream"`
}

type ollamaResponse struct {
	Response string `json:"response"`
	Done     bool   `json:"done"`
}

func (c *OllamaClient) do(ctx context.Context, req ollamaRequest) (string, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiURL+"/api/generate", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	var out ollamaResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return "", fmt.Errorf("parsing ollama response: %w", err)
	}
	return out.Response, nil
}

func (c *OllamaClient) Complete(ctx context.Context, system, user string) (string, error) {
	return c.do(ctx, ollamaRequest{
		Model:  c.model,
		System: system,
		Prompt: user,
		Stream: false,
	})
}

func (c *OllamaClient) CompleteStructured(ctx context.Context, system, user string, ts ToolSchema) (map[string]any, error) {
	prompt := fmt.Sprintf(`%s

You MUST respond with ONLY a JSON object. No explanation, no markdown, no code fences.
The JSON must have a "pages" key whose value is a JSON array.
Each element of the array must be an object with "title" (string) and "body" (string) keys.
Example of the EXACT format required:
{"pages":[{"title":"Example Title","body":"Example body text.","links":[]}]}

Now respond to:
%s`, system, user)

	raw, err := c.do(ctx, ollamaRequest{
		Model:  c.model,
		Prompt: prompt,
		Format: "json",
		Stream: false,
	})
	if err != nil {
		return nil, err
	}
	raw = strings.TrimSpace(raw)
	// Strip markdown code fences if model wrapped it anyway
	if idx := strings.Index(raw, "{"); idx > 0 {
		raw = raw[idx:]
	}
	if idx := strings.LastIndex(raw, "}"); idx >= 0 && idx < len(raw)-1 {
		raw = raw[:idx+1]
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, fmt.Errorf("parsing structured response: %w\nraw: %s", err, raw)
	}
	return result, nil
}
