package ingest

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func FetchGitHub(repoURL string) (string, error) {
	tmpDir, err := os.MkdirTemp("", "llmwiki-github-*")
	if err != nil {
		return "", fmt.Errorf("creating temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	cmd := exec.Command("git", "clone", "--depth", "1", "--filter=blob:none", repoURL, tmpDir)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git clone %s: %w", repoURL, err)
	}

	// Prefer docs directories; fall back to whole repo
	docDirs := []string{
		filepath.Join(tmpDir, "docs"),
		filepath.Join(tmpDir, "doc"),
		filepath.Join(tmpDir, "documentation"),
		filepath.Join(tmpDir, "README.md"),
	}

	var content strings.Builder
	for _, d := range docDirs {
		if info, err := os.Stat(d); err == nil {
			if info.IsDir() {
				text, err := ReadLocal(d)
				if err == nil && text != "" {
					content.WriteString(text)
				}
			} else {
				text, err := readFile(d)
				if err == nil && text != "" {
					content.WriteString(fmt.Sprintf("=== %s ===\n%s\n\n", "README.md", text))
				}
			}
		}
	}
	if content.Len() == 0 {
		return ReadLocal(tmpDir)
	}
	return content.String(), nil
}

func IsGitHubURL(s string) bool {
	return strings.Contains(s, "github.com") && !strings.HasSuffix(s, ".git") ||
		strings.HasSuffix(s, ".git")
}
