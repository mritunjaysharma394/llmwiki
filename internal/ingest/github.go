package ingest

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// FetchGitHubFiles shallow-clones the repo into a tempdir and runs the
// unified directory walker over it. The clone is removed before returning.
func FetchGitHubFiles(repoURL string, opts WalkOptions) ([]SourceFile, error) {
	tmpDir, err := os.MkdirTemp("", "llmwiki-github-*")
	if err != nil {
		return nil, fmt.Errorf("creating temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	cmd := exec.Command("git", "clone", "--depth", "1", "--filter=blob:none", repoURL, tmpDir)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git clone %s: %w", repoURL, err)
	}
	return ReadLocalFiles(tmpDir, opts)
}

func IsGitHubURL(s string) bool {
	return strings.Contains(s, "github.com") && !strings.HasSuffix(s, ".git") ||
		strings.HasSuffix(s, ".git")
}

