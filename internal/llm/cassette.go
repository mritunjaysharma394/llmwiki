package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
)

type Mode int

const (
	ModeReplay Mode = iota
	ModeRecord
	ModeLive
)

var ErrCassetteMismatch = errors.New("cassette: request did not match recorded fixture")

type cassetteEntry struct {
	System         string `json:"system"`
	User           string `json:"user"`
	ToolSchemaName string `json:"tool_schema_name,omitempty"`
	Method         string `json:"method"`
	Response       any    `json:"response,omitempty"`
	ResponseText   string `json:"response_text,omitempty"`
}

type CassetteClient struct {
	upstream Client
	dir      string
	name     string
	mode     Mode
	idx      int64
}

func NewCassetteClient(upstream Client, dir, name string, mode Mode) *CassetteClient {
	if envMode, set := modeFromEnv(); set {
		mode = envMode
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		panic(fmt.Sprintf("cassette dir: %v", err))
	}
	return &CassetteClient{upstream: upstream, dir: dir, name: name, mode: mode}
}

func modeFromEnv() (Mode, bool) {
	switch os.Getenv("LLMWIKI_CASSETTE_MODE") {
	case "record":
		return ModeRecord, true
	case "live":
		return ModeLive, true
	case "replay":
		return ModeReplay, true
	}
	if os.Getenv("LLMWIKI_RECORD") != "" {
		return ModeRecord, true
	}
	if os.Getenv("LLMWIKI_LIVE") != "" {
		return ModeLive, true
	}
	return ModeReplay, false
}

func (c *CassetteClient) nextPath() string {
	i := atomic.AddInt64(&c.idx, 1)
	return filepath.Join(c.dir, fmt.Sprintf("%s__%03d.json", c.name, i))
}

func (c *CassetteClient) Complete(ctx context.Context, system, user string) (string, error) {
	path := c.nextPath()
	switch c.mode {
	case ModeLive:
		return c.upstream.Complete(ctx, system, user)
	case ModeRecord:
		resp, err := c.upstream.Complete(ctx, system, user)
		if err != nil {
			return "", err
		}
		entry := cassetteEntry{System: system, User: user, Method: "Complete", ResponseText: resp}
		if err := writeEntry(path, entry); err != nil {
			return "", err
		}
		return resp, nil
	default:
		entry, err := readEntry(path)
		if err != nil {
			return "", err
		}
		if entry.Method != "Complete" || entry.System != system || entry.User != user {
			return "", fmt.Errorf("%w: %s\n  system match: %v\n  user match: %v",
				ErrCassetteMismatch, path, entry.System == system, entry.User == user)
		}
		return entry.ResponseText, nil
	}
}

func (c *CassetteClient) CompleteStructured(ctx context.Context, system, user string, ts ToolSchema) (map[string]any, error) {
	path := c.nextPath()
	switch c.mode {
	case ModeLive:
		return c.upstream.CompleteStructured(ctx, system, user, ts)
	case ModeRecord:
		resp, err := c.upstream.CompleteStructured(ctx, system, user, ts)
		if err != nil {
			return nil, err
		}
		entry := cassetteEntry{System: system, User: user, ToolSchemaName: ts.Name, Method: "CompleteStructured", Response: resp}
		if err := writeEntry(path, entry); err != nil {
			return nil, err
		}
		return resp, nil
	default:
		entry, err := readEntry(path)
		if err != nil {
			return nil, err
		}
		if entry.Method != "CompleteStructured" || entry.System != system || entry.User != user || entry.ToolSchemaName != ts.Name {
			return nil, fmt.Errorf("%w: %s", ErrCassetteMismatch, path)
		}
		m, ok := entry.Response.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("cassette response not a map: %T", entry.Response)
		}
		return m, nil
	}
}

func (c *CassetteClient) CompleteStream(ctx context.Context, system, user string, w io.Writer) (string, error) {
	resp, err := c.Complete(ctx, system, user)
	if err != nil {
		return "", err
	}
	_, _ = w.Write([]byte(resp))
	return resp, nil
}

func writeEntry(path string, entry cassetteEntry) error {
	data, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func readEntry(path string) (cassetteEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return cassetteEntry{}, fmt.Errorf("read cassette %s: %w", path, err)
	}
	var entry cassetteEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return cassetteEntry{}, fmt.Errorf("parse cassette %s: %w", path, err)
	}
	return entry, nil
}

var _ Client = (*CassetteClient)(nil)
