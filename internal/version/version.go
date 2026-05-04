// Package version exposes build-time identity vars injected by GoReleaser via
// -ldflags. For non-release builds (go run, go test, plain go build) every
// value defaults to "(devel)" and Format() prints "llmwiki (devel)".
package version

import (
	"fmt"
	"runtime"
)

// These are overridden at link time by GoReleaser. See .goreleaser.yml.
var (
	Version   = "(devel)"
	Commit    = "(devel)"
	BuildDate = "(devel)"
)

// Format returns the canonical one-line version string, e.g.
// "llmwiki 0.5.0-rc.1 (commit abc1234, built 2026-05-04, go1.26.2)".
// For development builds it collapses to "llmwiki (devel)".
func Format() string {
	if Version == "(devel)" && Commit == "(devel)" && BuildDate == "(devel)" {
		return "llmwiki (devel)"
	}
	return fmt.Sprintf("llmwiki %s (commit %s, built %s, %s)",
		Version, Commit, BuildDate, runtime.Version())
}
