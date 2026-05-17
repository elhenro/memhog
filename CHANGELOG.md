# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
