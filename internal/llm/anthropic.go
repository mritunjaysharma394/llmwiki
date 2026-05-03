package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/packages/param"
)

type AnthropicClient struct {
	client *anthropic.Client
	model  string
}

func NewAnthropicClient(model string) *AnthropicClient {
	c := anthropic.NewClient()
	return &AnthropicClient{client: &c, model: model}
}

func (c *AnthropicClient) Complete(ctx context.Context, system, user string) (string, error) {
	msg, err := c.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(c.model),
		MaxTokens: 4096,
		System: []anthropic.TextBlockParam{
			{Text: system},
		},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(user)),
		},
	})
	if err != nil {
		return "", err
	}
	for _, block := range msg.Content {
		if t, ok := block.AsAny().(anthropic.TextBlock); ok {
			return t.Text, nil
		}
	}
	return "", fmt.Errorf("no text in response")
}

func (c *AnthropicClient) CompleteStructured(ctx context.Context, system, user string, ts ToolSchema) (map[string]any, error) {
	msg, err := c.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(c.model),
		MaxTokens: 4096,
		System: []anthropic.TextBlockParam{
			{
				Text:         system,
				CacheControl: anthropic.NewCacheControlEphemeralParam(),
			},
		},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(user)),
		},
		Tools: []anthropic.ToolUnionParam{
			{OfTool: &anthropic.ToolParam{
				Name:        ts.Name,
				Description: param.NewOpt(ts.Description),
				InputSchema: anthropic.ToolInputSchemaParam{
					Properties: ts.Properties,
					Required:   ts.Required,
				},
			}},
		},
		ToolChoice: anthropic.ToolChoiceParamOfTool(ts.Name),
	})
	if err != nil {
		return nil, err
	}
	for _, block := range msg.Content {
		if tu, ok := block.AsAny().(anthropic.ToolUseBlock); ok {
			var result map[string]any
			if err := json.Unmarshal(tu.Input, &result); err != nil {
				return nil, fmt.Errorf("parsing tool input: %w", err)
			}
			return result, nil
		}
	}
	return nil, fmt.Errorf("no tool use in response")
}

func (c *AnthropicClient) CompleteStream(ctx context.Context, system, user string, w io.Writer) (string, error) {
	stream := c.client.Messages.NewStreaming(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(c.model),
		MaxTokens: 4096,
		System: []anthropic.TextBlockParam{
			{Text: system},
		},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(user)),
		},
	})
	defer stream.Close()
	var full strings.Builder
	for stream.Next() {
		event := stream.Current()
		if event.Type == "content_block_delta" && event.Delta.Type == "text_delta" {
			text := event.Delta.Text
			if text == "" {
				continue
			}
			if _, err := io.WriteString(w, text); err != nil {
				return "", err
			}
			full.WriteString(text)
		}
	}
	if err := stream.Err(); err != nil {
		return "", err
	}
	return full.String(), nil
}

var _ Client = (*AnthropicClient)(nil)
