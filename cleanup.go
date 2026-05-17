package main

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/shirou/gopsutil/v3/process"
)

// claude-cleanup: reap idle/leaked `claude` bg-spare + bg-pty-host workers.
// mirrors ~/.local/bin/claude-cleanup keep-rules:
//   - keep `claude daemon run` (warm pool root)
//   - keep ancestry of memhog itself (parent shell + parent claude session)
//   - keep pty-hosts with a live tty attached (someone is using it)

func isClaudeBgWorker(cmd string) bool {
	if !strings.Contains(cmd, "--bg-") {
		return false
	}
	return strings.Contains(cmd, "--bg-spare") || strings.Contains(cmd, "--bg-pty-host")
}

func isClaudePtyHost(cmd string) bool {
	return strings.Contains(cmd, "--bg-pty-host")
}

func isClaudeDaemonRoot(cmd string) bool {
	return strings.Contains(cmd, "claude daemon run")
}

// buildAncestors walks parent pids starting at pid up to 20 hops.
// used at startup so we never reap our own shell/session chain.
func buildAncestors(pid int32) map[int32]bool {
	out := map[int32]bool{pid: true}
	p, err := process.NewProcess(pid)
	if err != nil {
		return out
	}
	for depth := 0; depth < 20; depth++ {
		par, err := p.Parent()
		if err != nil || par == nil || par.Pid <= 1 {
			break
		}
		out[par.Pid] = true
		p = par
	}
	return out
}

// hasAttachedTTY: shells out to lsof to detect /dev/ttys* fds.
// matches bash script; macOS gopsutil lacks OpenFiles.
func hasAttachedTTY(pid int32) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	defer cancel()
	out, err := exec.CommandContext(ctx, "lsof", "-p", strconv.Itoa(int(pid))).Output()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "/dev/ttys") {
			return true
		}
	}
	return false
}

type cleanupPlan struct {
	targets    []procRow
	keptDaemon int
	keptTTY    int
}

// isAnthropicOwned: broad match for nuke mode. covers claude code (daemon +
// bg workers + version-pinned bins), claude desktop app, claude-cli, MCP
// helpers spawned under ~/.claude, and anything self-identifying as anthropic.
// kept narrow enough to avoid matching e.g. user code that happens to mention
// claude — we anchor on binary paths and well-known names.
func isAnthropicOwned(p procRow) bool {
	name := strings.ToLower(p.name)
	raw := strings.ToLower(p.rawName)
	cmd := strings.ToLower(p.cmdline)
	exe := strings.ToLower(p.exe)
	if name == "claude" || raw == "claude" {
		return true
	}
	if strings.HasPrefix(name, "claude-") || strings.HasPrefix(raw, "claude-") {
		return true
	}
	if strings.Contains(name, "anthropic") || strings.Contains(raw, "anthropic") {
		return true
	}
	for _, needle := range []string{
		"/.local/share/claude/",
		"/.local/bin/claude",
		"/.claude/",
		"claude daemon run",
		"claude agents",
		"anthropic",
		"/claude.app/",
	} {
		if strings.Contains(cmd, needle) || strings.Contains(exe, needle) {
			return true
		}
	}
	return false
}

type nukePlan struct {
	targets   []procRow
	keptSelf  int // memhog pid + ancestor chain (shell, terminal, parent claude)
	totalMB   float64
}

// planClaudeNuke: every anthropic-owned proc except ourselves + ancestors.
// preserving ancestors avoids killing the shell/session that launched memhog.
func planClaudeNuke(procs []procRow, selfPid int32, ancestors map[int32]bool) nukePlan {
	pl := nukePlan{}
	for _, p := range procs {
		if !isAnthropicOwned(p) {
			continue
		}
		if p.pid == selfPid || ancestors[p.pid] {
			pl.keptSelf++
			continue
		}
		pl.targets = append(pl.targets, p)
		pl.totalMB += p.memMB
	}
	return pl
}

func planClaudeCleanup(procs []procRow, ancestors map[int32]bool) cleanupPlan {
	pl := cleanupPlan{}
	for _, p := range procs {
		if isClaudeDaemonRoot(p.cmdline) {
			pl.keptDaemon++
			continue
		}
		if !isClaudeBgWorker(p.cmdline) {
			continue
		}
		if ancestors[p.pid] {
			continue
		}
		if isClaudePtyHost(p.cmdline) && hasAttachedTTY(p.pid) {
			pl.keptTTY++
			continue
		}
		pl.targets = append(pl.targets, p)
	}
	return pl
}

// cleanupTermAll: SIGTERM each target. KILL escalation runs as a delayed cmd.
func cleanupTermAll(targets []procRow) {
	for _, t := range targets {
		_ = syscall.Kill(int(t.pid), syscall.SIGTERM)
	}
}

type cleanupEscalateMsg struct{ pids []int32 }

func cleanupEscalateCmd(targets []procRow) tea.Cmd {
	pids := make([]int32, 0, len(targets))
	for _, t := range targets {
		pids = append(pids, t.pid)
	}
	return tea.Tick(1*time.Second, func(time.Time) tea.Msg {
		return cleanupEscalateMsg{pids: pids}
	})
}
