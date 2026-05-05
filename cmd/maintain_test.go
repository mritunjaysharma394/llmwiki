package cmd

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

// setupMaintainEnv mirrors setupLintEnv (cmd/lint_test.go): chdir to a
// fresh temp dir, write a minimal config, loadConfig, swap the
// llmClient for the lint stub. The maintain command shares the lint
// dispatch shape (DB + LLM + schema) so the same scaffolding works.
func setupMaintainEnv(t *testing.T, completeResp string) string {
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
`)
	for _, d := range []string{".llmwiki/wiki", ".llmwiki/raw", ".llmwiki/answers"} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	if err := loadConfig(); err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	prevClient := llmClient
	llmClient = &stubLintLLM{completeResp: completeResp}
	t.Cleanup(func() { llmClient = prevClient })
	return root
}

// captureMaintainOutput swaps stdout for a pipe and runs runMaintain
// with the given flag map. Returns whatever the command wrote and the
// error it returned (cobra would have rendered this via cliutil).
func captureMaintainOutput(t *testing.T, flags map[string]string) (string, error) {
	t.Helper()
	for name, val := range flags {
		if err := maintainCmd.Flags().Set(name, val); err != nil {
			t.Fatalf("set --%s=%s: %v", name, val, err)
		}
	}
	t.Cleanup(func() {
		// Reset flags so one test's state doesn't leak.
		for _, name := range []string{"lint", "refresh-stale", "promote-pending", "dry-run"} {
			_ = maintainCmd.Flags().Set(name, "false")
		}
	})

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	prev := os.Stdout
	os.Stdout = w
	runErr := runMaintain(maintainCmd, nil)
	w.Close()
	os.Stdout = prev
	var buf bytes.Buffer
	if _, cerr := io.Copy(&buf, r); cerr != nil {
		t.Fatalf("copy: %v", cerr)
	}
	return buf.String(), runErr
}

// TestMaintain_BareInvocationRunsAllThreeSteps — no flags = umbrella
// runs all three step heads. We verify by greppable presence of each
// step's preamble line in the output.
func TestMaintain_BareInvocationRunsAllThreeSteps(t *testing.T) {
	setupMaintainEnv(t, "")
	out, err := captureMaintainOutput(t, nil)
	if err != nil {
		t.Fatalf("runMaintain: %v", err)
	}
	for _, want := range []string{"refresh-stale", "lint", "promote-pending"} {
		if !strings.Contains(out, want) {
			t.Errorf("bare invocation should run %q step; output:\n%s", want, out)
		}
	}
}

// TestMaintain_SubsetFlagRunsOnlyThatStep — pass --lint only and
// confirm refresh-stale and promote-pending step preambles are NOT
// printed.
func TestMaintain_SubsetFlagRunsOnlyThatStep(t *testing.T) {
	setupMaintainEnv(t, "")
	out, err := captureMaintainOutput(t, map[string]string{"lint": "true"})
	if err != nil {
		t.Fatalf("runMaintain --lint: %v", err)
	}
	if !strings.Contains(out, "lint: scanning pages") {
		t.Errorf("--lint should print lint preamble; got:\n%s", out)
	}
	if strings.Contains(out, "refresh-stale: walking sources") {
		t.Errorf("--lint should NOT run refresh-stale; output:\n%s", out)
	}
	if strings.Contains(out, "promote-pending: sweeping") {
		t.Errorf("--lint should NOT run promote-pending; output:\n%s", out)
	}
}

// TestMaintain_DryRunHeader — --dry-run produces the "(dry run)"
// header and the "would refresh" / "would promote" verb forms.
func TestMaintain_DryRunHeader(t *testing.T) {
	setupMaintainEnv(t, "")
	out, err := captureMaintainOutput(t, map[string]string{"dry-run": "true"})
	if err != nil {
		t.Fatalf("runMaintain --dry-run: %v", err)
	}
	if !strings.Contains(out, "(dry run)") {
		t.Errorf("--dry-run should print '(dry run)' header; output:\n%s", out)
	}
	if !strings.Contains(out, "would refresh") {
		t.Errorf("--dry-run should print 'would refresh' verb; output:\n%s", out)
	}
	if !strings.Contains(out, "would promote") {
		t.Errorf("--dry-run should print 'would promote' verb; output:\n%s", out)
	}
}

// TestMaintain_CleanRunReturnsNilError — an empty wiki with no
// sources / no answers / no pages produces zero counts and nil error
// (cosmetic findings exit 0; clean exit 0 too).
func TestMaintain_CleanRunReturnsNilError(t *testing.T) {
	setupMaintainEnv(t, "")
	_, err := captureMaintainOutput(t, nil)
	if err != nil {
		t.Errorf("clean wiki should exit 0; got %v", err)
	}
}
