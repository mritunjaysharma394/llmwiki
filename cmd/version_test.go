package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/mritunjaysharma394/llmwiki/internal/version"
)

func TestVersionCmdPrintsFormat(t *testing.T) {
	saved := version.Version
	defer func() { version.Version = saved }()
	version.Version = "1.0.0-test"

	buf := &bytes.Buffer{}
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"version"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "1.0.0-test") {
		t.Errorf("version output missing version string: %q", out)
	}
	if !strings.Contains(out, "llmwiki") {
		t.Errorf("version output missing prefix: %q", out)
	}
}
