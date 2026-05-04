package schema

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

//go:embed default.md
var DefaultDoc []byte

// SchemaFilenames is the ordered list of filenames Load scans at the
// wiki root. The first file present wins; AGENTS.md is canonical
// (multi-vendor) and beats CLAUDE.md (Claude Code native) when both
// exist with byte-identical content. Phase G's `init` and Phase E's
// `schema show --doc` reuse this slice as the single source of truth
// for the candidate list.
var SchemaFilenames = []string{"AGENTS.md", "CLAUDE.md"}

var (
	bundledOnce sync.Once
	bundled     Schema
	bundledErr  error
)

// Bundled returns the parsed embedded default schema. Cached after the
// first call. Used by the cmd/wiki entrypoints when AGENTS.md is absent
// (a v0.6 wiki opening under v0.7).
//
// The embedded default carries its own raw bytes, so Bundled().Hash()
// is a real hex hash equal to sha256(DefaultDoc). db.schema_hash rows
// for pages ingested under bundled defaults all carry that one hex
// value uniformly — no sentinel-vs-real-hash branching at every read
// site, and a user who later writes an AGENTS.md byte-identical to
// the bundled default sees zero drift.
func Bundled() Schema {
	bundledOnce.Do(func() {
		bundled, bundledErr = Parse(DefaultDoc)
	})
	if bundledErr != nil {
		// The embedded default is checked into the binary; a parse
		// error here is a programmer bug, not a runtime condition.
		// Panic at first use is the right shape — a CI build that
		// breaks the embedded doc fails the whole package's tests.
		panic("internal/schema: bundled default fails to parse: " + bundledErr.Error())
	}
	return bundled
}

// Load scans <wikiRoot> for the schema doc using SchemaFilenames in
// order. The first file present is parsed and returned with DocPath
// set to that filename. When both AGENTS.md and CLAUDE.md are present
// with byte-identical content, AGENTS.md wins silently (the common
// case is a symlink or copy). When both are present with different
// content, Load refuses to guess and returns a typed ValidationError
// naming both filenames so the user picks one before re-running.
//
// When no candidate file exists, Load falls back to Bundled() exactly
// as v0.7 Phase A.1 did. Parse errors on the chosen file bubble up
// unchanged so cmd/root.go's loadConfig can render file:line.
func Load(wikiRoot string) (Schema, error) {
	type found struct {
		name string
		data []byte
	}
	var hits []found
	for _, name := range SchemaFilenames {
		path := filepath.Join(wikiRoot, name)
		data, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return Schema{}, err
		}
		hits = append(hits, found{name: name, data: data})
	}

	if len(hits) == 0 {
		return Bundled(), nil
	}

	chosen := hits[0]
	if len(hits) > 1 {
		// Multiple schema-doc files present at the wiki root. If their
		// contents are byte-identical, prefer the canonical filename
		// (AGENTS.md, by virtue of SchemaFilenames ordering) silently.
		// Otherwise refuse — surfacing a typed error is friendlier
		// than silently picking one and confusing the user about
		// which doc actually drove the next ingest.
		allEqual := true
		for i := 1; i < len(hits); i++ {
			if !bytes.Equal(hits[0].data, hits[i].data) {
				allEqual = false
				break
			}
		}
		if !allEqual {
			names := make([]string, 0, len(hits))
			for _, h := range hits {
				names = append(names, h.name)
			}
			return Schema{}, ValidationError{
				Section: "(load)",
				Problem: fmt.Sprintf(
					"found %s and %s with different contents at %s; remove one or make them identical (symlink one to the other) before continuing",
					names[0], names[1], wikiRoot,
				),
			}
		}
	}

	s, err := Parse(chosen.data)
	if err != nil {
		return Schema{}, err
	}
	s.DocPath = chosen.name
	return s, nil
}
