// Package mcp — server_integration_test.go
//
// Integration tests for sub-project 7 / Phase K Task 19: in-process
// drives of mcp.get_schema across the file-on-disk Load boundary.
// These tests run without an LLM, without cassettes, and without API
// keys — they spin up the in-process MCP server, drop a real
// AGENTS.md at a wiki-root tempdir, route schema.Load through
// Deps.Schema, and assert the get_schema response payload reflects
// the on-disk content (not the bundled default).
//
// Phase I (Task 14) already covered the structural shape via
//
//   - TestGetSchema_BundledByDefault            (bundled hash + ontology)
//   - TestGetSchema_ReturnsActivePromptsAndOntology (custom schema via Parse)
//   - TestGetSchema_ReadOnly_NoSetSchemaTool     (Q15 — read-only is contract)
//   - TestGetSchema_ResponseShape                (response key pinning)
//   - TestMCPHandlersThreadSchema_NotJustBundled (handlers thread Schema)
//
// What's covered HERE that Phase I did not: the actual on-disk
// AGENTS.md → schema.Load → MCP get_schema integration. Phase I's
// custom-schema test built a Schema by calling schema.Parse on a
// fixture string and assigning it to deps.Schema directly. The
// production loadConfig path always goes through schema.Load(wikiRoot)
// to resolve AGENTS.md / CLAUDE.md, so this test pins the file-on-
// disk → DocPath round-trip an agent will actually see in the wild.

package mcp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mritunjaysharma394/llmwiki/internal/schema"
)

// onDiskCustomSchema is a syntactically-valid AGENTS.md body the
// file-on-disk integration test drops at the wiki-root tempdir
// before it spins up the MCP server. The Domain section carries a
// distinctive marker so the get_schema response can be asserted to
// surface the user's content, not the bundled default. Every
// required H2 section is present so schema.Parse accepts it.
const onDiskCustomSchema = `---
schema_version: 1
generator: llmwiki-mcp-integration-test
---

# llmwiki schema (on-disk integration fixture)

## Domain

DISTINCTIVE-DOMAIN-MARKER: TestMCPGetSchema_WithCustomAGENTSMdOnDisk
runs the full file-on-disk → schema.Load → mcp.get_schema chain.

## Page ontology

  - title         (string)         the page's primary key; unique per wiki
  - updated_at    (RFC3339 ts)     last-write timestamp
  - content_hash  (sha256)         body hash; recomputed at every write
  - source_ids    (list of int)    DB row IDs backing this page
  - tags          (list of strings) Obsidian/Dataview-friendly
  - sources       (list of paths)  derived from evidence; emitted by WritePage
  - created       (date)           first-ingest date
  - links         (list)           Obsidian wikilinks declared structurally
  - evidence      (list of quotes) verbatim spans from sources; required, >= 1
  - body          (markdown)       the page's narrative

## Ingest prompt

DISTINCTIVE-INGEST-MARKER for the on-disk integration test.
{{domain}}{{existing_titles}}

## Update-existing prompt

DISTINCTIVE-UPDATE-MARKER. {{domain}}{{existing_page_body}}{{existing_evidence}}

## Ask prompt

DISTINCTIVE-ASK-MARKER. {{domain}}

## Contradiction prompt

DISTINCTIVE-CONTRADICTION-MARKER.

## Promote rewrite prompt

DISTINCTIVE-PROMOTE-MARKER. {{question}}{{answer_body}}{{evidence_quotes}}

## Lint contradictions prompt

DISTINCTIVE-LINT-MARKER.

## Glossary

  - on-disk: a schema doc loaded from AGENTS.md at the wiki root
`

// TestMCPGetSchema_WithCustomAGENTSMdOnDisk drives the full
// file-on-disk → schema.Load → mcp.get_schema chain in-process. It
// is the integration-level companion to Phase I's
// TestGetSchema_ReturnsActivePromptsAndOntology (which assigns a
// schema.Parse result directly to deps.Schema, bypassing Load's
// candidate-file scan + DocPath assignment).
//
// Asserts:
//
//   - get_schema's `domain` field surfaces the on-disk AGENTS.md
//     content (the DISTINCTIVE-DOMAIN-MARKER), not the bundled
//     domain (which is empty per Phase B's byte-equality work);
//   - get_schema's `hash` equals sha256(AGENTS.md bytes);
//   - get_schema's `doc_path` equals "AGENTS.md" (the filename Load
//     stamps when it picks the file from the wiki-root candidate list);
//   - every prompt body is the raw template (DISTINCTIVE-*-MARKER
//     present, {{placeholder}} tokens unrendered) — that's the
//     agent-introspection contract Q15 fixes.
func TestMCPGetSchema_WithCustomAGENTSMdOnDisk(t *testing.T) {
	deps, cleanup := newTestDeps(t, nil)
	defer cleanup()

	// Drop AGENTS.md into a wiki-root tempdir, then load it via the
	// production schema.Load entrypoint. This is the path
	// cmd/root.go's loadConfig walks at process start.
	wikiRoot := t.TempDir()
	agentsPath := filepath.Join(wikiRoot, "AGENTS.md")
	if err := os.WriteFile(agentsPath, []byte(onDiskCustomSchema), 0644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}
	loaded, err := schema.Load(wikiRoot)
	if err != nil {
		t.Fatalf("schema.Load: %v", err)
	}
	if loaded.DocPath != "AGENTS.md" {
		t.Fatalf("schema.Load DocPath = %q, want %q", loaded.DocPath, "AGENTS.md")
	}
	deps.Schema = loaded

	srv := NewServer(deps)
	c, done := connect(t, srv)
	defer done()

	res, text := callTool(t, c, "get_schema", map[string]any{})
	if res.IsError {
		t.Fatalf("get_schema returned IsError=true: %s", text)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatalf("unmarshal: %v\nraw=%s", err, text)
	}

	// doc_path round-trips through Load's filename stamping.
	if dp, _ := got["doc_path"].(string); dp != "AGENTS.md" {
		t.Errorf("doc_path = %q, want %q", dp, "AGENTS.md")
	}

	// hash equals sha256(AGENTS.md) — load reads the same bytes the
	// hash is computed over, so hash(loaded) == sha256(file).
	wantHashBytes := sha256.Sum256([]byte(onDiskCustomSchema))
	wantHash := hex.EncodeToString(wantHashBytes[:])
	if h, _ := got["hash"].(string); h != wantHash {
		t.Errorf("hash = %q, want %q (sha256 of on-disk AGENTS.md)", h, wantHash)
	}
	// And the Schema.Hash() method agrees — pin both.
	if h, _ := got["hash"].(string); h != loaded.Hash() {
		t.Errorf("hash = %q, want %q (loaded.Hash())", h, loaded.Hash())
	}

	// hash differs from Bundled() — proves we surfaced user content.
	if h, _ := got["hash"].(string); h == schema.Bundled().Hash() {
		t.Errorf("hash = %q matches bundled %q; user AGENTS.md should diverge",
			h, schema.Bundled().Hash())
	}

	// domain surfaces the user's marker, not the bundled default
	// (which Phase B established as the empty string for byte-equality).
	dom, _ := got["domain"].(string)
	if !strings.Contains(dom, "DISTINCTIVE-DOMAIN-MARKER") {
		t.Errorf("domain = %q, want it to contain DISTINCTIVE-DOMAIN-MARKER", dom)
	}

	// Every prompt body is the raw template the user wrote — markers
	// present, {{placeholder}} tokens unrendered. The server renders
	// at LLM-call time, not at get_schema time.
	prompts, ok := got["prompts"].(map[string]any)
	if !ok {
		t.Fatalf("prompts not map[string]any (raw=%s)", text)
	}
	wantMarker := map[string]string{
		"ingest":              "DISTINCTIVE-INGEST-MARKER",
		"update_existing":     "DISTINCTIVE-UPDATE-MARKER",
		"ask":                 "DISTINCTIVE-ASK-MARKER",
		"contradiction":       "DISTINCTIVE-CONTRADICTION-MARKER",
		"promote_rewrite":     "DISTINCTIVE-PROMOTE-MARKER",
		"lint_contradictions": "DISTINCTIVE-LINT-MARKER",
	}
	for name, marker := range wantMarker {
		body, _ := prompts[name].(string)
		if !strings.Contains(body, marker) {
			t.Errorf("prompts[%q] missing marker %q (got %q)", name, marker, body)
		}
	}
	// {{domain}} survives in ingest, update_existing, ask — those are
	// the prompts whose bodies our fixture wrote it into.
	for _, name := range []string{"ingest", "update_existing", "ask"} {
		body, _ := prompts[name].(string)
		if !strings.Contains(body, "{{domain}}") {
			t.Errorf("prompts[%q] should keep {{domain}} placeholder unrendered (raw=%s)", name, body)
		}
	}

	// schema_version is 1 (the only supported version).
	if sv, _ := got["schema_version"].(float64); int(sv) != 1 {
		t.Errorf("schema_version = %v, want 1", got["schema_version"])
	}

	// glossary round-trips the single user-defined term.
	glossary, _ := got["glossary"].([]any)
	if len(glossary) != 1 {
		t.Errorf("glossary length = %d, want 1 (raw=%s)", len(glossary), text)
	}
}
