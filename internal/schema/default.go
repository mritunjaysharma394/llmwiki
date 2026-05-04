package schema

import (
	_ "embed"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

//go:embed default.md
var DefaultDoc []byte

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

// Load reads <wikiRoot>/AGENTS.md and parses it; falls back to Bundled()
// when the file is absent. Validates structure on success;
// ValidationError bubbles up so cmd/root.go's loadConfig can render
// file:line on failure.
func Load(wikiRoot string) (Schema, error) {
	path := filepath.Join(wikiRoot, "AGENTS.md")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Bundled(), nil
	}
	if err != nil {
		return Schema{}, err
	}
	s, err := Parse(data)
	if err != nil {
		return Schema{}, err
	}
	s.DocPath = "AGENTS.md"
	return s, nil
}
