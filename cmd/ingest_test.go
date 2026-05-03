package cmd

import (
	"sort"
	"testing"

	"github.com/mritunjaysharma394/llmwiki/internal/db"
	"github.com/mritunjaysharma394/llmwiki/internal/ingest"
)

func TestSlugifyForArchive(t *testing.T) {
	tests := []struct{ in, want string }{
		{"What dependencies?", "what-dependencies"},
		{"Hello,  World!", "hello-world"},
		{"a/b\\c", "a-b-c"},
		{"   ", ""},
	}
	for _, tc := range tests {
		if got := slugify(tc.in); got != tc.want {
			t.Errorf("slugify(%q) = %q want %q", tc.in, got, tc.want)
		}
	}
}

func TestPartitionByFileHash(t *testing.T) {
	incoming := []ingest.SourceFile{
		ingest.NewSourceFile("unchanged.go", []byte("u\n")),
		ingest.NewSourceFile("changed.go", []byte("new\n")),
		ingest.NewSourceFile("new.go", []byte("n\n")),
	}
	existing := map[string]db.SourceFile{
		"unchanged.go": {RelativePath: "unchanged.go", ContentHash: incoming[0].ContentHash},
		"changed.go":   {RelativePath: "changed.go", ContentHash: "old"},
		"gone.go":      {RelativePath: "gone.go", ContentHash: "irrelevant"},
	}
	p := partitionByFileHash(incoming, existing)
	if len(p.unchanged) != 1 || p.unchanged[0].RelativePath != "unchanged.go" {
		t.Errorf("unchanged = %v", p.unchanged)
	}
	if len(p.changed) != 1 || p.changed[0].RelativePath != "changed.go" {
		t.Errorf("changed = %v", p.changed)
	}
	if len(p.newFiles) != 1 || p.newFiles[0].RelativePath != "new.go" {
		t.Errorf("new = %v", p.newFiles)
	}
	if len(p.gone) != 1 || p.gone[0].RelativePath != "gone.go" {
		t.Errorf("gone = %v", p.gone)
	}
}

func TestPartitionByFileHashEmptyExisting(t *testing.T) {
	incoming := []ingest.SourceFile{
		ingest.NewSourceFile("a.md", []byte("a")),
		ingest.NewSourceFile("b.md", []byte("b")),
	}
	p := partitionByFileHash(incoming, map[string]db.SourceFile{})
	if len(p.newFiles) != 2 {
		t.Errorf("expected 2 new files, got %d", len(p.newFiles))
	}
	if len(p.unchanged) != 0 || len(p.changed) != 0 || len(p.gone) != 0 {
		t.Errorf("expected only newFiles populated, got %+v", p)
	}
}

func TestComputeWholeHashOrderIndependent(t *testing.T) {
	a := []ingest.SourceFile{
		ingest.NewSourceFile("a.md", []byte("x")),
		ingest.NewSourceFile("b.md", []byte("y")),
	}
	b := []ingest.SourceFile{a[1], a[0]}
	if computeWholeHash(a) != computeWholeHash(b) {
		t.Error("computeWholeHash should be order-independent")
	}
}

func TestComputeWholeHashChangesWithContent(t *testing.T) {
	a := []ingest.SourceFile{
		ingest.NewSourceFile("a.md", []byte("x")),
		ingest.NewSourceFile("b.md", []byte("y")),
	}
	c := []ingest.SourceFile{
		ingest.NewSourceFile("a.md", []byte("x")),
		ingest.NewSourceFile("b.md", []byte("Y")),
	}
	if computeWholeHash(a) == computeWholeHash(c) {
		t.Error("computeWholeHash should change when any file content changes")
	}
}

func TestBuildIngestOptionsAppliesFlags(t *testing.T) {
	// Use a fresh cobra.Command so flag state from ingestCmd's package init
	// doesn't bleed across tests (cobra Flags() are per-command and parse-once
	// safe, but isolating mirrors how runIngest uses them).
	cmd := ingestCmd
	if err := cmd.ParseFlags([]string{
		"--max-file-bytes", "1024",
		"--exclude", "*.foo,*.bar",
		"--no-gitignore",
		"--include", ".md,.go",
		"--force",
	}); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}
	t.Cleanup(func() {
		// Reset flags so other tests see defaults.
		cmd.Flags().Set("max-file-bytes", "0")
		cmd.Flags().Set("exclude", "")
		cmd.Flags().Set("include", "")
		cmd.Flags().Set("no-gitignore", "false")
		cmd.Flags().Set("force", "false")
	})

	walk, _ := buildIngestOptions(cmd, nil)
	if walk.MaxFileBytes != 1024 {
		t.Errorf("MaxFileBytes = %d", walk.MaxFileBytes)
	}
	if walk.RespectGitignore {
		t.Error("--no-gitignore should disable RespectGitignore")
	}
	if len(walk.ExtraSkipGlobs) != 2 || walk.ExtraSkipGlobs[0] != "*.foo" || walk.ExtraSkipGlobs[1] != "*.bar" {
		t.Errorf("ExtraSkipGlobs = %v", walk.ExtraSkipGlobs)
	}
	if len(walk.IncludeOnly) != 2 || walk.IncludeOnly[0] != ".md" || walk.IncludeOnly[1] != ".go" {
		t.Errorf("IncludeOnly = %v", walk.IncludeOnly)
	}
	if !forceFlag(cmd) {
		t.Error("--force not propagated")
	}
}

// TestBuildIngestOptionsAppliesConfigDefaults verifies that values from the
// [ingest] config block flow into WalkOptions / URLOptions when no flags
// override them.
func TestBuildIngestOptionsAppliesConfigDefaults(t *testing.T) {
	cmd := ingestCmd
	t.Cleanup(func() {
		cmd.Flags().Set("max-file-bytes", "0")
		cmd.Flags().Set("exclude", "")
		cmd.Flags().Set("include", "")
		cmd.Flags().Set("no-gitignore", "false")
		cmd.Flags().Set("force", "false")
	})
	// reset to clean state in case other tests left flags set
	cmd.Flags().Set("max-file-bytes", "0")
	cmd.Flags().Set("exclude", "")
	cmd.Flags().Set("include", "")
	cmd.Flags().Set("no-gitignore", "false")

	f := false
	c := &Config{
		Ingest: IngestConfig{
			MaxFileBytes:       2048,
			HTTPTimeoutSeconds: 7,
			HTTPMaxBytes:       1024 * 1024,
			ExtraSkipGlobs:     []string{"*.log"},
			RespectGitignore:   &f,
		},
	}
	walk, urlOpts := buildIngestOptions(cmd, c)
	if walk.MaxFileBytes != 2048 {
		t.Errorf("MaxFileBytes from config = %d, want 2048", walk.MaxFileBytes)
	}
	if walk.RespectGitignore {
		t.Error("RespectGitignore from config (false) not applied")
	}
	if len(walk.ExtraSkipGlobs) != 1 || walk.ExtraSkipGlobs[0] != "*.log" {
		t.Errorf("ExtraSkipGlobs from config = %v", walk.ExtraSkipGlobs)
	}
	if urlOpts.Timeout.Seconds() != 7 {
		t.Errorf("urlOpts.Timeout = %v, want 7s", urlOpts.Timeout)
	}
	if urlOpts.MaxBodyBytes != 1024*1024 {
		t.Errorf("urlOpts.MaxBodyBytes = %d", urlOpts.MaxBodyBytes)
	}
}

// TestBuildIngestOptionsFlagsOverrideConfig confirms that explicit CLI flags
// win over [ingest] config values, regardless of which the user set first.
func TestBuildIngestOptionsFlagsOverrideConfig(t *testing.T) {
	cmd := ingestCmd
	if err := cmd.ParseFlags([]string{"--max-file-bytes", "9999"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cmd.Flags().Set("max-file-bytes", "0")
	})
	c := &Config{Ingest: IngestConfig{MaxFileBytes: 1}}
	walk, _ := buildIngestOptions(cmd, c)
	if walk.MaxFileBytes != 9999 {
		t.Errorf("flag override = %d, want 9999", walk.MaxFileBytes)
	}
}

func TestSplitCSV(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", []string{}},
		{"a", []string{"a"}},
		{"a,b,c", []string{"a", "b", "c"}},
		{"  a , b ,, c ", []string{"a", "b", "c"}},
	}
	for _, tc := range cases {
		got := splitCSV(tc.in)
		if len(got) != len(tc.want) {
			t.Errorf("splitCSV(%q) = %v want %v", tc.in, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("splitCSV(%q)[%d] = %q want %q", tc.in, i, got[i], tc.want[i])
			}
		}
	}
}

// sanity assertion: paths in `gone` are returned in arbitrary order; tests
// shouldn't depend on map iteration order.
func TestPartitionGoneSortable(t *testing.T) {
	existing := map[string]db.SourceFile{
		"z": {RelativePath: "z", ContentHash: "z"},
		"a": {RelativePath: "a", ContentHash: "a"},
	}
	p := partitionByFileHash(nil, existing)
	paths := make([]string, len(p.gone))
	for i, g := range p.gone {
		paths[i] = g.RelativePath
	}
	sort.Strings(paths)
	if len(paths) != 2 || paths[0] != "a" || paths[1] != "z" {
		t.Errorf("got %v", paths)
	}
}
