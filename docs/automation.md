# Automation

<!-- TODO(release): record docs/assets/demo.gif via tools/record-demo.sh -->

`llmwiki maintain` runs the three sensible maintenance steps in one
shot — refresh stale URL/file sources, lint the wiki for orphans /
missing cross-refs / schema drift / contradictions, and sweep
`.llmwiki/answers/` for any answers that should be auto-promoted but
weren't (e.g. the `ask` that wrote them ran with `[ask] auto_promote = false`).
Bare invocation runs all three. Pass any of `--lint`,
`--refresh-stale`, `--promote-pending` to run only that subset.
`--dry-run` composes with any subset and writes nothing — useful for a
preview pass before letting cron flip the trigger.

Exit code is non-zero only when an actual error occurred (network
failure, a promote that crashed, a DB error). Cosmetic findings
(orphans, contradictions, schema drift) exit 0, so a cron line that
alerts on non-zero exits won't page you for a wiki that's merely
drifty.

The recipes below set up `llmwiki maintain` to run automatically on
the three platforms most users land on. Two more recipes follow at
the end of this page: a `watch` daemon that ingests files as they
land in a directory, and a Claude Code Stop-hook that captures
sessions back into the wiki.

## launchd (macOS) — daily 3am

Drop the following plist at `~/Library/LaunchAgents/com.llmwiki.maintain.plist`,
edit `WorkingDirectory` to point at your wiki root, then
`launchctl load ~/Library/LaunchAgents/com.llmwiki.maintain.plist`.

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>            <string>com.llmwiki.maintain</string>
  <key>ProgramArguments</key> <array><string>/usr/local/bin/llmwiki</string><string>maintain</string></array>
  <key>WorkingDirectory</key> <string>/Users/YOU/wiki</string>
  <key>StartCalendarInterval</key>
  <dict><key>Hour</key><integer>3</integer><key>Minute</key><integer>0</integer></dict>
  <key>StandardOutPath</key>  <string>/Users/YOU/.llmwiki/maintain.log</string>
  <key>StandardErrorPath</key><string>/Users/YOU/.llmwiki/maintain.log</string>
</dict>
</plist>
```

Tail `~/.llmwiki/maintain.log` to see the morning's run output.

## systemd timer (Linux) — daily

Two files. Service at `~/.config/systemd/user/llmwiki-maintain.service`:

```ini
[Unit]
Description=llmwiki maintain (cron)
[Service]
Type=oneshot
WorkingDirectory=%h/wiki
ExecStart=/usr/local/bin/llmwiki maintain
StandardOutput=append:%h/.llmwiki/maintain.log
StandardError=append:%h/.llmwiki/maintain.log
```

Timer at `~/.config/systemd/user/llmwiki-maintain.timer`:

```ini
[Unit]
Description=daily llmwiki maintain
[Timer]
OnCalendar=daily
Persistent=true
[Install]
WantedBy=timers.target
```

Enable with `systemctl --user daemon-reload &&
systemctl --user enable --now llmwiki-maintain.timer`. Check with
`systemctl --user list-timers | grep llmwiki`.

## GitHub Actions (CI-driven, for shared wikis)

Assumes the wiki lives in a git repo so the workflow can commit any
page changes back. Add `.github/workflows/llmwiki-maintain.yml`:

```yaml
name: llmwiki maintain
on:
  schedule: [{ cron: "0 3 * * *" }]
  workflow_dispatch:
jobs:
  maintain:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: curl -sSL https://github.com/mritunjaysharma394/llmwiki/releases/latest/download/llmwiki-linux-amd64 -o /usr/local/bin/llmwiki && chmod +x /usr/local/bin/llmwiki
      - run: llmwiki maintain
        env: { GEMINI_API_KEY: ${{ secrets.GEMINI_API_KEY }} }
      - run: |
          git config user.email "llmwiki-bot@users.noreply.github.com"
          git config user.name "llmwiki-bot"
          git add -A && git diff --cached --quiet || git commit -m "maintain: $(date -u +%F)"
          git push
```

The workflow assumes the wiki repo holds the `wiki/` directory at its
root and that `GEMINI_API_KEY` (or your provider's env var) is set as
a repo secret. Re-run on demand via the Actions tab → "Run workflow".

## Watching a directory

`llmwiki watch <dir>` is the long-lived companion to `llmwiki ingest`.
fsnotify subscribes to `Create` and `Write` events on the directory,
debounces 2 seconds per file (so editor saves and chunked downloads
coalesce into one ingest), and feeds each path into a SQLite-backed
queue. A consumer goroutine drains the queue via the same pipeline
`llmwiki ingest <source>` uses — pages are written, retro-links update,
contradictions surface, all the usual end-of-ingest output. Drop a
file, get a page within ~30 seconds.

```bash
llmwiki watch ~/wiki/sources/
# watching /Users/you/wiki/sources ... (Ctrl-C to stop)
# [+] paper-2024.pdf → queued
# [✓] paper-2024.pdf → 3 pages, 2 retro-links, 0 contradictions
# [+] meeting-notes.md → queued
# [✓] meeting-notes.md → 1 pages, 0 retro-links, 1 contradictions
```

The queue is crash-resumable. If the process dies mid-ingest, the
next `llmwiki watch` invocation picks up `pending` and `retrying`
rows automatically — no re-enqueue, no double-ingest. Failures retry
3 times with 5s / 30s / 5min exponential backoff before the row is
marked `failed` and the watcher moves on; the failure reason is
logged to stderr and persisted in the queue's `last_error` column.

Persist the directory list in `[watch]` under `.llmwiki/config.toml`
so a bare `llmwiki watch` (no argument) walks the same set every
time:

```toml
[watch]
dirs = ["/Users/you/wiki/sources", "/Users/you/Downloads/papers"]
debounce_seconds = 2
max_attempts = 3
```

A positional `<dir>` argument always wins over the config list (good
for one-off "watch this folder for the next hour" sessions). Pair
this with `llmwiki maintain` on a daily cron schedule above and the
wiki keeps itself current with no further user input.

## Claude Code session capture

`llmwiki capture-session` reads a Claude Code Stop-hook session
payload from stdin, extracts the assistant turns that touched the
wiki (any turn referencing `LLMWIKI_DIR` or invoking `llmwiki ` on
the CLI), and files them as a saved answer. The auto-promote gate
then decides: pages the conversation produced solid, well-cited
prose for get filed automatically; everything else lands in
`.llmwiki/answers/` for manual review via `llmwiki promote`.

Wire it into Claude Code's `Stop` hook by editing
`~/.claude/settings.json`:

```json
{
  "hooks": {
    "Stop": [
      { "command": "llmwiki capture-session" }
    ]
  }
}
```

That's the entire recipe. Claude Code pipes the session JSON into
`llmwiki capture-session` on `Stop`; the command does its filing
work and exits 0 unconditionally — empty payloads, malformed JSON,
or transcripts with no wiki-relevant turns are all silent no-ops, so
the hook never blocks your next prompt. Set `LLMWIKI_DIR` in your
shell environment to point the command at the right wiki when you
work across multiple llmwiki directories.

The capture command relies on the same auto-promote gate as
`llmwiki ask` (four-signal heuristic: cited pages, evidence quotes,
no hedging phrases, no near-duplicate page) plus the byte-exact
substring validator that guards every other write path in the
binary. The same trust property holds: a captured session can
never silently file a page that fails the validator.
