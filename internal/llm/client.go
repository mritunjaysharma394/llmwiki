package llm

import "context"

type ToolSchema struct {
	Name        string
	Description string
	Properties  map[string]any
	Required    []string
}

type Client interface {
	Complete(ctx context.Context, system, user string) (string, error)
	CompleteStructured(ctx context.Context, system, user string, ts ToolSchema) (map[string]any, error)
}
