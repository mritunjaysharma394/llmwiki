package cmd

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

// TestMCPCommand_StartsAndExits boots `llmwiki mcp` with a closed stdin
// pipe and asserts the cobra command shuts down cleanly within 1s. The
// upstream stdio server treats stdin EOF as the "client disconnected"
// signal — same path Claude Desktop / Cursor use when they kill the
// child process — so this test verifies the happy-path lifecycle without
// touching a real client.
func TestMCPCommand_StartsAndExits(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LLMWIKI_DIR", dir)
	t.Setenv("ANTHROPIC_API_KEY", "test-key-not-used")
	writeMCPConfig(t, dir)

	// Redirect stdin to a closed pipe so Listen's first ReadMessage
	// returns EOF immediately. Same idea for stdout — drain it into a
	// pipe whose read end we close. We restore the real os.Stdin/Stdout
	// in a Cleanup so adjacent tests are unaffected.
	origStdin, origStdout := os.Stdin, os.Stdout
	rIn, wIn, _ := os.Pipe()
	rOut, wOut, _ := os.Pipe()
	os.Stdin = rIn
	os.Stdout = wOut
	t.Cleanup(func() {
		os.Stdin = origStdin
		os.Stdout = origStdout
		_ = rIn.Close()
		_ = wIn.Close()
		_ = rOut.Close()
		_ = wOut.Close()
	})
	// Closing the write end of stdin makes any read on rIn return EOF.
	_ = wIn.Close()
	// Drain stdout so writes don't block.
	go func() { _, _ = io.Copy(io.Discard, rOut) }()

	// Mirror what rootCmd's PersistentPreRunE does: chdir into
	// LLMWIKI_DIR, then loadConfig. We don't go through Execute()
	// directly because that path also parses os.Args. Restore the
	// original cwd in cleanup so adjacent tests that use relative
	// paths (smoke_test.go reads ../internal/...) keep working.
	origCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(origCwd) })
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	// Run loadConfig + the mcp cobra command in a goroutine.
	errCh := make(chan error, 1)
	go func() {
		if err := loadConfig(); err != nil {
			errCh <- err
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		c := &cobra.Command{}
		c.SetContext(ctx)
		errCh <- runMCP(c, nil)
	}()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("runMCP returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runMCP did not exit within 2s of stdin EOF")
	}
}

// TestMCPCommand_InitFailureExitsNonZero asserts loadConfig propagates
// errors so Execute() can render them and exit non-zero. With LLMWIKI_DIR
// pointing at an empty directory there's no .llmwiki/config.toml; the
// config loader returns an error and the cobra layer never reaches
// runMCP. MCP clients show the failure as a tool launch error.
func TestMCPCommand_InitFailureExitsNonZero(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LLMWIKI_DIR", dir)

	origCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(origCwd) })

	// The MCP cobra command's PersistentPreRunE calls loadConfig which
	// expects .llmwiki/config.toml under cwd. Empty dir → error.
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	if err := loadConfig(); err == nil {
		t.Fatal("expected loadConfig to fail with no config in cwd")
	}
}

// writeMCPConfig drops a minimal config.toml under <dir>/.llmwiki/ that
// the mcp command's loadConfig path can read. Mirrors the cmd-package's
// writeMinimalConfig helper but without the chdir + flag-snapshot
// machinery (the test runs in a stable working dir via LLMWIKI_DIR).
func writeMCPConfig(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".llmwiki"), 0755); err != nil {
		t.Fatalf("mkdir .llmwiki: %v", err)
	}
	body := `[llm]
provider = "anthropic"
model = "claude-haiku-4-5"

[wiki]
wiki_dir = ".llmwiki/wiki"
raw_dir = ".llmwiki/raw"
db_path = ".llmwiki/wiki.db"
`
	if err := os.WriteFile(filepath.Join(dir, ".llmwiki", "config.toml"), []byte(body), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
}
