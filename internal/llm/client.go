package llm

import (
	"context"
	"io"
)

type ToolSchema struct {
	Name        string
	Description string
	Properties  map[string]any
	Required    []string
}

type Client interface {
	Complete(ctx context.Context, system, user string) (string, error)
	CompleteStructured(ctx context.Context, system, user string, ts ToolSchema) (map[string]any, error)
	CompleteStream(ctx context.Context, system, user string, w io.Writer) (string, error)
}
