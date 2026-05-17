# memhog

A terminal-UI memory & process inspector built for the age of always-on AI dev tools.

`htop` shows you everything. `memhog` shows you what's actually eating your laptop — grouped by what it is (Claude Code, browser, LLM server, editor, container, runtime), with trend arrows, sparklines, and one-key reaping for the usual suspects.

```
⬢ memhog                                                hostname    142 procs · 14:32:08

mem    ████████████  56%   ▁▂▃▄▆▇▆▅   32GB · 14GB free
cpu    ███           18%   ▁▁▂▂▃▂▁▁   1.21 0.98 0.82 · 10c

groups  ⢿ claude:7 5.8GB · ⠶ browser:24 3.2GB · ⠟ editor:3 2.1GB · ⣶ vm:2 1.4GB · +4

    process                              mem      Δ↓     cpu      time         pid
  ▎ ⢿ Claude Code · 2.1.143             1.8GB   ▲42    24.1%   01:24:13     54321
    ⢿ Claude Code · 2.1.143             1.1GB           8.2%   00:42:01     54322
    ⣿ ollama                            980MB    ▼12    0.1%   06:11:55     43210
    ⠶ Google Chrome Helper              512MB           2.4%   02:03:11     61234
    ...
```

## Why

If you run Claude Code, Cursor, an Ollama daemon, a couple browsers, and Docker on the same machine, your memory pressure isn't `top -o MEM` shaped — it's *categorical*. Six `Claude Code · 2.1.143` rows mean nothing in isolation; `claude:7 5.8GB` means a lot.

memhog adds three things `htop` doesn't:

- **Categorical grouping.** Process families (Claude Code, AI · LLM, Browser, Editor, VM, Runtime, …) collapse into a one-line band at the top, with per-group memory totals and counts. Press `1`–`9` to filter to a category.
- **Claude lifecycle awareness.** `C` reaps idle `--bg-spare` / `--bg-pty-host` workers but keeps the daemon root and any pty-host with a live TTY. `X` nukes everything Anthropic-owned (daemon, foreground claude, MCP helpers, claude.app) — except your own ancestor chain, so you can't accidentally kill the shell you're sitting in.
- **A memory guard.** `L` toggles a watchdog that auto-pauses (SIGSTOP, not SIGKILL) the biggest non-LLM, non-system process when available RAM drops below your cap. Cap is adjustable live with `+` / `-`.

Built on [Bubble Tea](https://github.com/charmbracelet/bubbletea) + [gopsutil](https://github.com/shirou/gopsutil). Pure Go, single static binary, no daemon, no config file.

## Install

### Go

```sh
go install github.com/elhenro/memhog@latest
```

### From source

```sh
git clone https://github.com/elhenro/memhog
cd memhog
go build -o ~/.local/bin/memhog .
```

### Binary releases

Download from [Releases](https://github.com/elhenro/memhog/releases) — darwin (arm64/amd64), linux (arm64/amd64), windows (amd64).

## Run

```sh
memhog
```

That's the whole CLI.

## Keys

### Navigation
| key | action |
|---|---|
| `j` / `k` / `↑` / `↓` | move cursor |
| `g` / `G` | top / bottom |
| `ctrl-d` / `ctrl-u` | half-page |
| `ctrl-f` / `ctrl-b` / `pgdn` / `pgup` | page |
| `/` | filter (incremental, case-insensitive) |
| `c` | clear filter |
| `1`–`9` | filter to the Nth group from the band |
| `0` | clear group filter |
| `s` | cycle sort: `mem → cpu → category → name → time → pid` |
| `n` | toggle raw process names (show pre-resolution names — useful when display name guessed wrong) |
| `d` | toggle the detail pane for the selected process |
| `?` | toggle key help |

### Process control
| key | action |
|---|---|
| `space` / `p` | pause selected (SIGSTOP) |
| `r` | resume selected (SIGCONT) |
| `t` | terminate selected (SIGTERM, confirm) |
| `K` | kill selected (SIGKILL, confirm) |
| `C` | claude-cleanup: reap idle `--bg-spare` / `--bg-pty-host` (confirm) |
| `X` | NUKE: kill every claude/anthropic process (except memhog's own ancestor chain) (confirm) |
| `o` | jump cursor to first ollama/omlx process |

### Memory guard
| key | action |
|---|---|
| `L` | toggle guard (auto-pause big non-LLM, non-critical procs when free RAM < cap) |
| `+` / `=` / `-` | adjust cap in 512MB steps |

### Tick rate
| key | action |
|---|---|
| `<` | slower (+500ms) |
| `>` | faster (−500ms, min 500ms) |

### Quit
| key | action |
|---|---|
| `q` / `ctrl-c` | quit |

## What gets categorized as what

- **Claude Code** — `@anthropic-ai/claude-code` npm CLI, `~/.claude/local/` shimmed binary, and the version-string-as-process-name pattern (`2.1.143`) it uses.
- **AI · LLM** — ollama, omlx, llama-server, lm studio, llamafile, vllm, mlx_lm.
- **Browser** — Chrome (+ helpers), Safari, Firefox, Edge, Brave, Vivaldi, Arc, WebKit, Chromium.
- **Editor** — VS Code (+ helpers), Cursor, Windsurf, Zed, Sublime, Neovim.
- **VM · Container** — Docker, OrbStack, Qemu, Podman, VMware, Parallels, Lima.
- **System** — kernel_task, launchd, WindowServer, loginwindow, syslogd, configd, powerd, etc. (also can't be killed by the guard).
- **Runtime** — node, python, ruby, go, java, deno, bun.
- **Chat · Meet** — Slack, Discord, Telegram, WhatsApp, Signal, Zoom, Teams.
- **Other** — everything else.

The classifier is heuristic and lives in `proc.go` — PRs welcome to expand it.

## Safety

- **Claude nuke (`X`) never kills memhog's own ancestor chain** — walks parent PIDs up to 20 hops at startup and excludes them. So the shell / terminal / parent claude session that launched memhog survives.
- **Claude cleanup (`C`) keeps the daemon root** and any pty-host worker with a `/dev/ttys*` open (someone is actively using it). Escalates SIGTERM → SIGKILL after 1s for unresponsive targets.
- **Guard (`L`) refuses to touch** `kernel_task`, `launchd`, `WindowServer`, `loginwindow`, `coreaudio`, `syslogd`, `configd`, `powerd`, ollama, omlx, memhog itself, and anything already stopped.
- **Per-process actions always confirm** before sending signals.

## Platform support

- **macOS** — primary target. Tested on arm64 (M-series) and amd64.
- **Linux** — should work; categorization heuristics tuned for macOS process names (Claude Code Helper, Code Helper (Renderer), etc.) so some categories may land in "Other".
- **Windows** — gopsutil supports it; not tested. PRs welcome.

## Roadmap

- per-process I/O & open-fd stats in detail pane
- save sort / filter / guard preset to `~/.config/memhog/config.toml`
- export current snapshot (CSV / JSON) — for "what was running when the machine got slow"
- treeview mode (parent → child indented)

## License

MIT — see [LICENSE](LICENSE).
