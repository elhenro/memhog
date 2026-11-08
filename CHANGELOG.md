# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Fixed
- Rows rendered exactly the terminal width, so the terminal auto-wrapped and ate the last
  character of every line (5-digit pids showed as 4 digits, frames scrolled by a line).
  All width math now reserves the final column.
- `1`–`9` group filter matched process *names* by substring (`sys` also matched
  `syslogd`); it now filters on the exact category and toggles off on a second press.
- Cursor went stale after narrowing a filter (and jumped to a different process when the
  list re-sorted each tick); selection is now clamped to the row set and preserved by pid.
- Sort indicators always showed `↓`; ascending sorts (`name`, `pid`, `time`) now show `↑`.
- `C` claude-cleanup shelled out to `lsof` once per pty-host on the UI goroutine
  (up to 800ms each); it is now a single batched `lsof` call.
- A single failed refresh blanked the whole UI; the last good snapshot is kept and the
  error is reported in the status line.
- Filter backspace cut UTF-8 by byte; pasted multi-character text was ignored.
- The memory guard could SIGSTOP memhog's own terminal/shell and picked targets in the
  current sort order instead of by memory; it now skips the ancestor chain and picks the
  largest eligible process.
- `t` / `K` / `space` could signal memhog itself, its ancestor chain, or critical
  daemons; those targets are now refused.
- List chrome was under-reserved by a line (overflow at short terminal heights).

### Fixed (second pass)
- **Windows release target did not compile** (`syscall.Kill`/`SIGSTOP` are unix-only), so
  `goreleaser`'s windows/amd64 build would fail. Signals moved behind
  `signals_unix.go` / `signals_windows.go`; pause/resume and the memory guard are disabled
  on Windows and report that instead of failing silently.
- **claude-cleanup could reap live sessions**: `lsof` exits non-zero when any requested pid
  has already died, and the old code discarded all output on error — every pty-host then
  looked idle. It now parses partial `-F pn` output, and when detection is impossible
  (no `lsof`, timeout) it keeps pty-hosts rather than guessing.
- The SIGKILL escalation after 1s could hit an unrelated process if a pid was reused in the
  grace window; it now verifies the process start time first.
- `X` nuke treated editors/pagers that merely carried a `~/.claude/...` path in their
  arguments as Anthropic-owned; such tools are now excluded.
- The per-pid cache was evicted for processes whose memory read fails (protected system
  procs), re-resolving exe/cmdline and losing their trend baseline every tick.
- Names containing wide glyphs (CJK, emoji) were padded by rune count and could wrap the
  row; truncation and padding now use terminal cell width.
- Group detail listed members in list order under a "largest" label; it is sorted by size.
- `e` on an expanded child now collapses its group and returns the cursor to the header;
  `c`/`0` clamp the cursor; sorting is stable so equal-memory rows stop reshuffling.

### Performance
- A refresh spent ~3.5s in `gopsutil` `Status()` (one syscall per process), longer than the
  1.5s tick — so refreshes overlapped and memhog burned 100–290% CPU watching the machine.
  Stopped-process detection is now a single batched `ps -axo pid=,stat=` call: a tick takes
  ~50ms instead of ~2s.
- Per-process `Δ` trend threshold lowered to 1MB so movement is actually visible.

### Added
- Sort by memory growth (`s` cycles mem→cpu→Δ→category→name→time→pid) to surface leaks;
  `Δ↓`/`time↑` column indicators now follow the active sort.
- Swap usage appended to the memory line when non-zero.
- Group rows are bold in their category colour, so families read differently from single
  processes.
- App-family grouping in the list (on by default): `Brave Browser Helper (Renderer)` +
  `Brave Browser` + GPU/plugin helpers collapse into one `Brave Browser ×32 6.3GB` row.
  `e`/`enter` expands a group in place, `u` toggles grouping off, `t`/`K`/`p`/`r` act on
  every member of a collapsed group (with the same self/ancestor/critical refusals).
- Group rows in the detail pane summarise count, total memory/CPU, oldest member, and the
  four largest members.
- `R` resumes every paused process.
- `esc` cancels an in-progress filter instead of just closing it.
- Footer shows the primary keys; empty filter results explain how to clear them.
- Empty state line, guarded narrow-terminal footer, `fmtSize`-consistent guard cap units.
- Regression tests for filtering, cursor restore, sort direction, and signal refusals.

### Changed
- Dependencies updated (`x/ansi`, `x/cellbuf`, `x/text`, `x/sys`, `go-runewidth`, `m1cpu`, …).

## [0.1.0] - 2026-05-17

First public release.

### Added
- Bubble Tea TUI with mem + cpu bars, sparklines, and load average.
- Process list with categorical grouping (Claude Code, AI · LLM, Browser, Editor, VM · Container, System, Runtime, Chat · Meet, Other).
- Group band with one-line per-category memory totals; filter to a group with `1`–`9`.
- Trend arrows (`▲`/`▼` ± delta MB) per process tick.
- Live filter (`/`), cycling sort (`s`), raw-name toggle (`n`).
- Detail pane (`d`) with pid/ppid, cpu, threads, uptime, exe, cmdline.
- Per-process signal actions: pause (`space`/`p`), resume (`r`), term (`t`), kill (`K`).
- Claude lifecycle helpers:
  - `C` claude-cleanup: reap idle `--bg-spare` / `--bg-pty-host` workers, keep daemon root and TTY-attached pty-hosts.
  - `X` claude-nuke: SIGTERM (escalating to SIGKILL after 1s) every anthropic-owned process except memhog's own ancestor chain.
- Memory guard (`L`): auto-SIGSTOP biggest non-LLM, non-critical process when free RAM < cap; cap adjustable live with `+`/`-`.
- Adjustable tick rate (`<`/`>`).
- Process cache to keep tick cost down (one-shot resolution of exe/cmdline/createTime, per-tick refresh of mem/cpu/status only).

[Unreleased]: https://github.com/elhenro/memhog/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/elhenro/memhog/releases/tag/v0.1.0
