// Package wiki — sources.go
//
// CurrentSourceHash computes the SHA-256 of the bytes currently stored
// at a source URI (an HTTP/HTTPS URL or a local filesystem path). Both
// `cmd/lint` and the sub-project 8 Phase D `RunMaintenance` umbrella
// need this primitive to spot stale URL sources whose remote bytes
// drifted from the per-source content_hash captured at last ingest.
//
// The implementation was previously inlined as `currentHash` in
// cmd/lint.go; Phase D extracted it here so the lint and maintain code
// paths share one byte-equal staleness check. The cmd-side wrapper
// stayed in cmd/lint.go (now a one-liner forwarding to this function)
// to keep that file's print-loop readable, but the implementation lives
// here.
//
// Notes:
//   - HTTP errors and ReadAll failures bubble up as the returned error;
//     the caller decides whether to log-and-skip (cmd/lint, RunMaintenance)
//     or abort.
//   - No timeout knob is plumbed; the lint use case is interactive and
//     the maintenance loop runs from cron, both with a network stack
//     that already enforces sensible defaults at the OS layer. Phase E
//     can lift the timeout into a config knob if real-world use surfaces
//     a need.
package wiki

import (
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

// CurrentSourceHash returns the SHA-256 hex digest of the bytes
// currently at uri. A uri starting with "http://" or "https://" is
// fetched via the default http client; everything else is treated as a
// filesystem path and read with os.ReadFile.
func CurrentSourceHash(uri string) (string, error) {
	var data []byte
	var err error
	switch {
	case strings.HasPrefix(uri, "http://") || strings.HasPrefix(uri, "https://"):
		resp, herr := http.Get(uri)
		if herr != nil {
			return "", herr
		}
		defer resp.Body.Close()
		data, err = io.ReadAll(resp.Body)
		if err != nil {
			return "", err
		}
	default:
		data, err = os.ReadFile(uri)
		if err != nil {
			return "", err
		}
	}
	return fmt.Sprintf("%x", sha256.Sum256(data)), nil
}
