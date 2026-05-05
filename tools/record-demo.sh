#!/usr/bin/env bash
# tools/record-demo.sh — re-record docs/assets/demo.gif.
#
# Drives the v0.8 demo:
#   1. fresh wiki via `llmwiki init`
#   2. drop a tiny markdown file into a sources/ subdir
#   3. start `llmwiki watch sources/` in the background; show the page
#      land in the wiki within seconds
#   4. `llmwiki ask "..."` against the just-ingested content; the
#      auto-promote gate files the answer as a permanent page (one-line
#      `→ filed as [[Title]]` output)
#   5. ingest a second source whose claim conflicts with page 1 — the
#      contradiction-on-ingest pipeline flags it inline
#
# We use VHS (https://github.com/charmbracelet/vhs) because it produces
# pixel-stable GIFs from a declarative .tape file — asciinema's GIF
# export is also fine but has more rendering knobs to tune. If neither
# tool is installed, the script prints install instructions and exits 1.
#
# The recording lives at docs/assets/demo.gif. README.md hero image
# points at that path.
#
# Usage:
#   ./tools/record-demo.sh           # record into docs/assets/demo.gif
#   ./tools/record-demo.sh --dry-run # walk the script without recording

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TAPE_PATH="${REPO_ROOT}/tools/demo.tape"
OUTPUT_PATH="${REPO_ROOT}/docs/assets/demo.gif"

DRY_RUN=0
if [[ "${1-}" == "--dry-run" ]]; then
    DRY_RUN=1
fi

# Probe for a recording tool. We prefer vhs; asciinema is a fallback.
if command -v vhs >/dev/null 2>&1; then
    RECORDER=vhs
elif command -v asciinema >/dev/null 2>&1; then
    RECORDER=asciinema
else
    cat <<'EOF' >&2
record-demo.sh: neither vhs nor asciinema is installed.

Install vhs (recommended for GIFs):
    brew install vhs                       # macOS
    go install github.com/charmbracelet/vhs@latest

Or asciinema (record to .cast, then asciinema-agg → .gif):
    brew install asciinema
    cargo install --git https://github.com/asciinema/agg
EOF
    exit 1
fi

# Build the binary the demo will exercise. We always use a fresh build
# so the GIF reflects the just-tagged version, not whatever stale
# llmwiki happens to be on $PATH.
BIN="$(mktemp -t llmwiki-demo-bin)"
trap 'rm -f "$BIN"' EXIT
echo "Building llmwiki → ${BIN}"
( cd "$REPO_ROOT" && go build -o "$BIN" ./... )

# Write the .tape script (vhs only). The demo runs in a fresh tempdir
# so the recording captures `init` from a blank slate every time.
DEMO_DIR="$(mktemp -d -t llmwiki-demo-dir)"
trap 'rm -rf "$DEMO_DIR" "$BIN"' EXIT

cat > "$TAPE_PATH" <<EOF
# vhs tape for docs/assets/demo.gif — see tools/record-demo.sh
Output ${OUTPUT_PATH}
Set FontSize 14
Set Width 1100
Set Height 700
Set Theme "Catppuccin Mocha"
Set TypingSpeed 25ms

Type "cd ${DEMO_DIR}"  Enter
Sleep 500ms
Type "${BIN} init --provider gemini"  Enter
Sleep 1s

Type "mkdir sources"  Enter
Type "echo '# Goroutines\nGoroutines are lightweight threads.' > sources/intro.md"  Enter
Sleep 500ms

Type "${BIN} watch sources/ &"  Enter
Sleep 500ms

# Drop a second source while the watcher is running:
Type "echo '# Channels\nChannels send values between goroutines.' > sources/channels.md"  Enter
Sleep 5s

Type "${BIN} ask 'how do goroutines and channels relate?'"  Enter
Sleep 4s

Type "kill %1"  Enter
Sleep 500ms
EOF

if [[ "$DRY_RUN" == "1" ]]; then
    echo "Dry run — wrote tape to ${TAPE_PATH}; not invoking ${RECORDER}."
    exit 0
fi

case "$RECORDER" in
    vhs)
        echo "Recording via vhs → ${OUTPUT_PATH}"
        vhs "$TAPE_PATH"
        ;;
    asciinema)
        # asciinema's GIF path is two-step: .cast then agg.
        CAST="$(mktemp -t llmwiki-demo.cast)"
        echo "Recording via asciinema → ${CAST}"
        asciinema rec -c "bash ${TAPE_PATH}" "$CAST"
        echo "Converting cast to GIF (requires asciinema-agg / agg)"
        agg "$CAST" "$OUTPUT_PATH"
        rm -f "$CAST"
        ;;
esac

echo "Recorded ${OUTPUT_PATH}"
