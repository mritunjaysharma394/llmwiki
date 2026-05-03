package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/BurntSushi/toml"
)

// TestApplyIngestDefaultsFillsZeroValues verifies that a zero-valued
// IngestConfig (the shape pre-v3 configs decode into when [ingest] is absent)
// is silently filled with the defaults the v3 init template would have
// written. This is the contract that lets pre-sub-project-3 wikis keep
// running unmodified.
func TestApplyIngestDefaultsFillsZeroValues(t *testing.T) {
	var c IngestConfig
	applyIngestDefaults(&c)
	if c.MaxFileBytes != 256*1024 {
		t.Errorf("MaxFileBytes = %d, want %d", c.MaxFileBytes, 256*1024)
	}
	if c.ChunkSizeBytes != 16*1024 {
		t.Errorf("ChunkSizeBytes = %d, want %d", c.ChunkSizeBytes, 16*1024)
	}
	if c.HTTPTimeoutSeconds != 30 {
		t.Errorf("HTTPTimeoutSeconds = %d, want 30", c.HTTPTimeoutSeconds)
	}
	if c.HTTPMaxBytes != 5*1024*1024 {
		t.Errorf("HTTPMaxBytes = %d", c.HTTPMaxBytes)
	}
	if c.PDFMinTextPerPage != 50 {
		t.Errorf("PDFMinTextPerPage = %d", c.PDFMinTextPerPage)
	}
	if !c.RespectGitignoreOrDefault() {
		t.Error("RespectGitignore default should be true when nil")
	}
}

// TestApplyIngestDefaultsKeepsExplicitValues makes sure non-zero values from
// the config file are preserved — defaults only fill what the user left blank.
func TestApplyIngestDefaultsKeepsExplicitValues(t *testing.T) {
	f := false
	c := IngestConfig{
		MaxFileBytes:       1024,
		ChunkSizeBytes:     2048,
		HTTPTimeoutSeconds: 5,
		HTTPMaxBytes:       1024 * 1024,
		PDFMinTextPerPage:  10,
		RespectGitignore:   &f,
	}
	applyIngestDefaults(&c)
	if c.MaxFileBytes != 1024 {
		t.Errorf("MaxFileBytes overwritten: %d", c.MaxFileBytes)
	}
	if c.ChunkSizeBytes != 2048 {
		t.Errorf("ChunkSizeBytes overwritten: %d", c.ChunkSizeBytes)
	}
	if c.HTTPTimeoutSeconds != 5 {
		t.Errorf("HTTPTimeoutSeconds overwritten: %d", c.HTTPTimeoutSeconds)
	}
	if c.HTTPMaxBytes != 1024*1024 {
		t.Errorf("HTTPMaxBytes overwritten: %d", c.HTTPMaxBytes)
	}
	if c.PDFMinTextPerPage != 10 {
		t.Errorf("PDFMinTextPerPage overwritten: %d", c.PDFMinTextPerPage)
	}
	if c.RespectGitignoreOrDefault() {
		t.Error("explicit RespectGitignore=false was overwritten")
	}
}

// TestLegacyConfigWithoutIngestBlockDecodesAndDefaults simulates a pre-v3
// .llmwiki/config.toml on disk: only [llm], [wiki], [ask] present, no
// [ingest] block. Decoding into Config and running applyIngestDefaults should
// yield a working IngestConfig.
func TestLegacyConfigWithoutIngestBlockDecodesAndDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	body := `[llm]
provider = "ollama"
model = "x"

[wiki]
wiki_dir = ".llmwiki/wiki"
raw_dir = ".llmwiki/raw"
db_path = ".llmwiki/wiki.db"

[ask]
auto_save = true
`
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	var got Config
	if _, err := toml.DecodeFile(path, &got); err != nil {
		t.Fatalf("DecodeFile: %v", err)
	}
	applyIngestDefaults(&got.Ingest)
	if got.Ingest.MaxFileBytes == 0 {
		t.Error("MaxFileBytes default not applied for legacy config")
	}
	if got.Ingest.ChunkSizeBytes == 0 {
		t.Error("ChunkSizeBytes default not applied for legacy config")
	}
	if !got.Ingest.RespectGitignoreOrDefault() {
		t.Error("RespectGitignore default not true for legacy config")
	}
}

// TestExplicitIngestBlockOverridesDefaults confirms a config with [ingest]
// values flowing through TOML decoding takes precedence — no silent default
// clobbering.
func TestExplicitIngestBlockOverridesDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	body := `[llm]
provider = "ollama"
model = "x"

[wiki]
wiki_dir = ".llmwiki/wiki"
raw_dir = ".llmwiki/raw"
db_path = ".llmwiki/wiki.db"

[ingest]
max_file_bytes = 4096
chunk_size_bytes = 8192
http_timeout_seconds = 7
respect_gitignore = false
`
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	var got Config
	if _, err := toml.DecodeFile(path, &got); err != nil {
		t.Fatalf("DecodeFile: %v", err)
	}
	applyIngestDefaults(&got.Ingest)
	if got.Ingest.MaxFileBytes != 4096 {
		t.Errorf("MaxFileBytes = %d, want 4096", got.Ingest.MaxFileBytes)
	}
	if got.Ingest.ChunkSizeBytes != 8192 {
		t.Errorf("ChunkSizeBytes = %d, want 8192", got.Ingest.ChunkSizeBytes)
	}
	if got.Ingest.HTTPTimeoutSeconds != 7 {
		t.Errorf("HTTPTimeoutSeconds = %d, want 7", got.Ingest.HTTPTimeoutSeconds)
	}
	if got.Ingest.RespectGitignoreOrDefault() {
		t.Error("explicit respect_gitignore=false overridden by default")
	}
}
