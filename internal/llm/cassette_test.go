package llm

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"testing"
)

type stubClient struct {
	completeFn   func(system, user string) (string, error)
	structuredFn func(system, user string, ts ToolSchema) (map[string]any, error)
	calls        int
}

func (s *stubClient) Complete(ctx context.Context, system, user string) (string, error) {
	s.calls++
	return s.completeFn(system, user)
}

func (s *stubClient) CompleteStructured(ctx context.Context, system, user string, ts ToolSchema) (map[string]any, error) {
	s.calls++
	return s.structuredFn(system, user, ts)
}

func (s *stubClient) CompleteStream(ctx context.Context, system, user string, w io.Writer) (string, error) {
	s.calls++
	resp, err := s.completeFn(system, user)
	if err != nil {
		return "", err
	}
	w.Write([]byte(resp))
	return resp, nil
}

func TestCassetteRecordThenReplay(t *testing.T) {
	dir := t.TempDir()
	stub := &stubClient{
		completeFn: func(system, user string) (string, error) {
			return "live response: " + user, nil
		},
	}
	rec := NewCassetteClient(stub, dir, "test_record_then_replay", ModeRecord)

	got, err := rec.Complete(context.Background(), "sys", "hello")
	if err != nil {
		t.Fatalf("record Complete: %v", err)
	}
	if got != "live response: hello" {
		t.Errorf("record got %q", got)
	}

	files, _ := filepath.Glob(dir + "/test_record_then_replay__*.json")
	if len(files) != 1 {
		t.Fatalf("expected 1 cassette file, got %d", len(files))
	}

	stub2 := &stubClient{}
	rep := NewCassetteClient(stub2, dir, "test_record_then_replay", ModeReplay)
	got2, err := rep.Complete(context.Background(), "sys", "hello")
	if err != nil {
		t.Fatalf("replay Complete: %v", err)
	}
	if got2 != "live response: hello" {
		t.Errorf("replay got %q", got2)
	}
	if stub2.calls != 0 {
		t.Errorf("replay should not call upstream, got %d calls", stub2.calls)
	}
}

func TestCassetteReplayMismatchFails(t *testing.T) {
	dir := t.TempDir()
	stub := &stubClient{
		completeFn: func(system, user string) (string, error) {
			return "ok", nil
		},
	}
	rec := NewCassetteClient(stub, dir, "test_mismatch", ModeRecord)
	rec.Complete(context.Background(), "sys", "first")
	rep := NewCassetteClient(&stubClient{}, dir, "test_mismatch", ModeReplay)
	_, err := rep.Complete(context.Background(), "sys", "DIFFERENT")
	if err == nil {
		t.Fatal("expected mismatch error")
	}
	if !errors.Is(err, ErrCassetteMismatch) {
		t.Errorf("err = %v, want ErrCassetteMismatch", err)
	}
}

func TestCassetteStructuredRoundTrip(t *testing.T) {
	dir := t.TempDir()
	stub := &stubClient{
		structuredFn: func(system, user string, ts ToolSchema) (map[string]any, error) {
			return map[string]any{"pages": []any{map[string]any{"title": "X"}}}, nil
		},
	}
	ts := ToolSchema{Name: "t"}
	rec := NewCassetteClient(stub, dir, "test_structured", ModeRecord)
	rec.CompleteStructured(context.Background(), "s", "u", ts)

	rep := NewCassetteClient(&stubClient{}, dir, "test_structured", ModeReplay)
	got, err := rep.CompleteStructured(context.Background(), "s", "u", ts)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if _, ok := got["pages"]; !ok {
		t.Errorf("got %+v", got)
	}
}
