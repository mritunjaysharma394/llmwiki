# Automation

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
the three platforms most users land on. Phase E will extend this file
with Claude Code Stop-hook session capture and `llmwiki watch`
examples; this page is the cron surface only.

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
