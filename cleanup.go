package main

import (
	"context"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
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

// ttyPids reports which of pids have a /dev/ttys* fd open, using a single
// lsof invocation. macOS gopsutil lacks OpenFiles, and one call per pid
// would freeze the UI for up to 800ms per worker.
// lookPath is a test seam for the lsof availability check.
var lookPath = exec.LookPath

// ttyPids reports pids with a /dev/ttys* fd open. ok=false means detection was
// impossible (no lsof, timeout): callers must then treat every result as live
// rather than reaping sessions they cannot see.
func ttyPids(pids []int32) (map[int32]bool, bool) {
	out := make(map[int32]bool, len(pids))
	if len(pids) == 0 {
		return out, true
	}
	if _, err := lookPath("lsof"); err != nil {
		return nil, false
	}
	parts := make([]string, len(pids))
	for i, pid := range pids {
		parts[i] = strconv.Itoa(int(pid))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	// lsof exits 1 when any requested pid has exited, but still prints the
	// live ones: dropping that output would mark every pty-host idle and let
	// cleanup reap sessions somebody is using. -F gives field output, safe for
	// command names containing spaces.
	raw, err := exec.CommandContext(ctx, "lsof", "-F", "pn", "-p", strings.Join(parts, ",")).Output()
	if ctx.Err() != nil {
		return nil, false
	}
	if err != nil && len(raw) == 0 {
		return nil, false
	}
	var cur int32
	for _, line := range strings.Split(string(raw), "\n") {
		if len(line) < 2 {
			continue
		}
		switch line[0] {
		case 'p':
			if pid, err := strconv.Atoi(line[1:]); err == nil {
				cur = int32(pid)
			}
		case 'n':
			if cur != 0 && strings.HasPrefix(line[1:], "/dev/ttys") {
				out[cur] = true
			}
		}
	}
	return out, true
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
	// an editor/pager/shell that merely has a claude path in its arguments is
	// not anthropic-owned — nuking someone's `vim ~/.claude/settings.json`
	// would be a nasty surprise.
	if isPathToolExe(exe) {
		return false
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

// isPathToolExe: tools that commonly carry arbitrary file paths as arguments.
func isPathToolExe(exe string) bool {
	switch strings.ToLower(filepath.Base(exe)) {
	case "vim", "nvim", "vi", "nano", "emacs", "pico", "ed",
		"less", "more", "most", "bat", "cat", "tail", "head",
		"grep", "egrep", "fgrep", "rg", "ag", "fd", "find", "ls",
		"open", "code", "cursor", "zed", "subl", "atom":
		return true
	}
	return false
}

type nukePlan struct {
	targets  []procRow
	keptSelf int // memhog pid + ancestor chain (shell, terminal, parent claude)
	totalMB  float64
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
	var ptyHosts []int32
	for _, p := range procs {
		if isClaudePtyHost(p.cmdline) && !ancestors[p.pid] {
			ptyHosts = append(ptyHosts, p.pid)
		}
	}
	live, ttyKnown := ttyPids(ptyHosts)
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
		if isClaudePtyHost(p.cmdline) && (!ttyKnown || live[p.pid]) {
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
		_ = sendSignal(t.pid, actTerm)
	}
}

type cleanupEscalateMsg struct{ targets []procRow }

func cleanupEscalateCmd(targets []procRow) tea.Cmd {
	saved := append([]procRow(nil), targets...)
	return tea.Tick(1*time.Second, func(time.Time) tea.Msg {
		return cleanupEscalateMsg{targets: saved}
	})
}

// stillSameProcess guards the SIGKILL escalation against pid reuse during the
// 1s grace period: a pid that now belongs to a different process is skipped.
func stillSameProcess(t procRow) bool {
	p, err := process.NewProcess(t.pid)
	if err != nil {
		return false
	}
	ct, err := p.CreateTime()
	if err != nil {
		return false
	}
	diff := ct - t.createMs
	if diff < 0 {
		diff = -diff
	}
	return diff <= 1000
}
