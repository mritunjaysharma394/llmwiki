package cmd

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setupCaptureSessionEnv mirrors setupWatchEnv: minimal config, loadConfig.
func setupCaptureSessionEnv(t *testing.T) string {
	t.Helper()
	root := chdirTemp(t)
	resetProviderFlags(t)
	t.Setenv("GEMINI_API_KEY", "test-key-not-used")
	writeMinimalConfig(t, `[llm]
provider = "gemini"
model = "gemini-2.0-flash"

[wiki]
wiki_dir = ".llmwiki/wiki"
raw_dir = ".llmwiki/raw"
db_path = ".llmwiki/wiki.db"

[ask]
auto_promote = false
`)
	for _, d := range []string{".llmwiki/wiki", ".llmwiki/raw", ".llmwiki/answers"} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	if err := loadConfig(); err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	return root
}

// runCaptureSessionWith pipes the given stdin payload through
// runCaptureSession, captures stdout, and returns it. Errors from
// runCaptureSession propagate (it always returns nil — the contract
// is that capture-session never fails the user's Stop hook).
func runCaptureSessionWith(t *testing.T, stdin string) (string, error) {
	t.Helper()
	prevStdin := captureSessionStdin
	captureSessionStdin = strings.NewReader(stdin)
	t.Cleanup(func() { captureSessionStdin = prevStdin })

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	prevStdout := os.Stdout
	os.Stdout = w
	runErr := runCaptureSession(captureSessionCmd, nil)
	w.Close()
	os.Stdout = prevStdout
	var buf bytes.Buffer
	if _, cerr := io.Copy(&buf, r); cerr != nil {
		t.Fatalf("copy: %v", cerr)
	}
	return buf.String(), runErr
}

// TestCaptureSession_EmptyStdinSilent — empty stdin is a no-op, no
// answers file is created, exit 0.
func TestCaptureSession_EmptyStdinSilent(t *testing.T) {
	root := setupCaptureSessionEnv(t)
	out, err := runCaptureSessionWith(t, "")
	if err != nil {
		t.Fatalf("runCaptureSession: %v", err)
	}
	if out != "" {
		t.Errorf("empty stdin should produce no stdout, got: %q", out)
	}
	answersDir := filepath.Join(root, ".llmwiki", "answers")
	entries, _ := os.ReadDir(answersDir)
	if len(entries) != 0 {
		t.Errorf("empty stdin should not create answer files, got %d", len(entries))
	}
}

// TestCaptureSession_MalformedJSONExitsZero — malformed JSON does
// not cause a non-zero exit; runCaptureSession returns nil and writes
// a stderr WARN (we don't assert the WARN here — just the no-error,
// no-files behaviour).
func TestCaptureSession_MalformedJSONExitsZero(t *testing.T) {
	root := setupCaptureSessionEnv(t)
	_, err := runCaptureSessionWith(t, "not valid json {")
	if err != nil {
		t.Fatalf("malformed JSON should not surface an error, got: %v", err)
	}
	answersDir := filepath.Join(root, ".llmwiki", "answers")
	entries, _ := os.ReadDir(answersDir)
	if len(entries) != 0 {
		t.Errorf("malformed JSON should not create answer files, got %d", len(entries))
	}
}

// TestCaptureSession_NoWikiReferencesNoFile — a transcript whose
// assistant turns never mention LLMWIKI_DIR or `llmwiki ` produces no
// answer file (silent no-op).
func TestCaptureSession_NoWikiReferencesNoFile(t *testing.T) {
	root := setupCaptureSessionEnv(t)
	payload := captureSessionPayload{
		Transcript: []captureSessionMessage{
			{Role: "user", Content: jsonString("hello")},
			{Role: "assistant", Content: jsonString("hi there, I'm a helpful assistant")},
		},
	}
	raw, _ := json.Marshal(payload)
	out, err := runCaptureSessionWith(t, string(raw))
	if err != nil {
		t.Fatalf("runCaptureSession: %v", err)
	}
	if out != "" {
		t.Errorf("no-wiki transcript should be silent, got: %q", out)
	}
	answersDir := filepath.Join(root, ".llmwiki", "answers")
	entries, _ := os.ReadDir(answersDir)
	if len(entries) != 0 {
		t.Errorf("no wiki refs should not create answer files, got %d entries", len(entries))
	}
}

// TestCaptureSession_WikiRelevantTurnSavesFile — when an assistant
// turn references `llmwiki `, the relevant turns get saved to
// .llmwiki/answers/<ts>-session-<slug>.md and the gate's verdict
// renders. With auto_promote = false in the test config the file
// is left in place; we assert the file exists and contains the
// expected synthetic Q+A.
func TestCaptureSession_WikiRelevantTurnSavesFile(t *testing.T) {
	root := setupCaptureSessionEnv(t)
	payload := captureSessionPayload{
		Transcript: []captureSessionMessage{
			{Role: "user", Content: jsonString("how do I add a source?")},
			{Role: "assistant", Content: jsonString("Run `llmwiki ingest <source>` to add a source to the wiki.")},
		},
	}
	raw, _ := json.Marshal(payload)
	out, err := runCaptureSessionWith(t, string(raw))
	if err != nil {
		t.Fatalf("runCaptureSession: %v", err)
	}
	// Auto-promote off → expect the "→ saved to ..." line.
	if !strings.Contains(out, "→ saved to") {
		t.Errorf("expected `→ saved to` line, got:\n%s", out)
	}
	answersDir := filepath.Join(root, ".llmwiki", "answers")
	entries, err := os.ReadDir(answersDir)
	if err != nil {
		t.Fatalf("read answers dir: %v", err)
	}
	var sessionFile string
	for _, e := range entries {
		if strings.Contains(e.Name(), "-session-") {
			sessionFile = filepath.Join(answersDir, e.Name())
			break
		}
	}
	if sessionFile == "" {
		t.Fatalf("expected one *-session-*.md file in %s, got %d entries", answersDir, len(entries))
	}
	body, err := os.ReadFile(sessionFile)
	if err != nil {
		t.Fatalf("read session file: %v", err)
	}
	if !strings.Contains(string(body), "llmwiki ingest") {
		t.Errorf("session file should contain the assistant turn body, got:\n%s", body)
	}
	if !strings.Contains(string(body), "how do I add a source?") {
		t.Errorf("session file should contain the synthetic question, got:\n%s", body)
	}
}

// TestCaptureSession_TranscriptPathPointer — the payload may point
// at an on-disk transcript JSON via transcript_path; capture-session
// should follow the pointer and behave the same way.
func TestCaptureSession_TranscriptPathPointer(t *testing.T) {
	root := setupCaptureSessionEnv(t)
	tdir := t.TempDir()
	transcript := []captureSessionMessage{
		{Role: "user", Content: jsonString("question via transcript_path")},
		{Role: "assistant", Content: jsonString("Use llmwiki ask to query the wiki.")},
	}
	tdata, _ := json.Marshal(transcript)
	tpath := filepath.Join(tdir, "transcript.json")
	if err := os.WriteFile(tpath, tdata, 0644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	pointer, _ := json.Marshal(map[string]any{"transcript_path": tpath})

	if _, err := runCaptureSessionWith(t, string(pointer)); err != nil {
		t.Fatalf("runCaptureSession: %v", err)
	}
	entries, _ := os.ReadDir(filepath.Join(root, ".llmwiki", "answers"))
	found := false
	for _, e := range entries {
		if strings.Contains(e.Name(), "-session-") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("transcript_path pointer should produce a session file, got %d entries", len(entries))
	}
}

// jsonString helper: marshal a string into json.RawMessage shape.
func jsonString(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return b
}
