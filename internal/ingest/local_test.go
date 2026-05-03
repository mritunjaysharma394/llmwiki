package ingest

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

const sampleDir = "testdata/dirs/sample"

// TestMain materializes the parts of the sample fixture that git refuses to
// track: a nested .git/ directory (git won't allow nesting one repo inside
// another, even with `git add -f`). The walker should treat it like any other
// .git/ directory it encounters in the wild.
func TestMain(m *testing.M) {
	gitDir := filepath.Join(sampleDir, ".git")
	_ = os.MkdirAll(gitDir, 0o755)
	_ = os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644)
	os.Exit(m.Run())
}

func walkPaths(files []SourceFile) []string {
	out := make([]string, len(files))
	for i, f := range files {
		out[i] = f.RelativePath
	}
	sort.Strings(out)
	return out
}

func TestReadLocalFilesSingleFile(t *testing.T) {
	files, err := ReadLocalFiles(filepath.Join(sampleDir, "README.md"), DefaultWalkOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].RelativePath != "README.md" {
		t.Errorf("got %+v, want 1 file at README.md", walkPaths(files))
	}
}

func TestReadLocalFilesDirectoryAppliesSkipRules(t *testing.T) {
	opts := DefaultWalkOptions()
	opts.MaxFileBytes = 256 * 1024
	files, err := ReadLocalFiles(sampleDir, opts)
	if err != nil {
		t.Fatal(err)
	}
	got := walkPaths(files)

	// What we expect:
	// - README.md             kept
	// - src/main.go           kept
	// - nested/deep/file.md   kept
	// skipped: .git/* (deny dir), vendor/* (deny dir), node_modules/* (deny dir),
	//          package-lock.json (deny basename), image.png (deny ext),
	//          ignored.txt + build/output.bin (gitignore), huge.txt (size cap).
	want := []string{
		"README.md",
		"nested/deep/file.md",
		"src/main.go",
	}
	if !equalSorted(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
}

func TestReadLocalFilesGitignoreDisabledKeepsIgnored(t *testing.T) {
	opts := DefaultWalkOptions()
	opts.RespectGitignore = false
	opts.MaxFileBytes = 256 * 1024
	files, err := ReadLocalFiles(sampleDir, opts)
	if err != nil {
		t.Fatal(err)
	}
	got := walkPaths(files)
	found := false
	for _, p := range got {
		if p == "ignored.txt" {
			found = true
		}
	}
	if !found {
		t.Errorf("ignored.txt should appear when RespectGitignore=false; got %v", got)
	}
}

func TestReadLocalFilesGitignoreSkipsSrcDir(t *testing.T) {
	// Write a temporary tree mirroring the sample fixture but with a .gitignore
	// that adds "src/" — confirm src/main.go is excluded.
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "README.md"), "# tmp\n")
	mustWrite(t, filepath.Join(dir, "src", "main.go"), "package main\n")
	mustWrite(t, filepath.Join(dir, ".gitignore"), "src/\n")

	files, err := ReadLocalFiles(dir, DefaultWalkOptions())
	if err != nil {
		t.Fatal(err)
	}
	got := walkPaths(files)
	for _, p := range got {
		if p == "src/main.go" {
			t.Errorf("src/main.go should be skipped via .gitignore src/; got %v", got)
		}
	}
}

func TestReadLocalFilesSizeCapSkips(t *testing.T) {
	opts := DefaultWalkOptions()
	opts.MaxFileBytes = 100 // huge.txt is hundreds of KB
	files, _ := ReadLocalFiles(sampleDir, opts)
	for _, f := range files {
		if f.RelativePath == "huge.txt" {
			t.Errorf("huge.txt should be skipped under tiny MaxFileBytes")
		}
	}
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func equalSorted(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
