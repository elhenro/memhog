package main

import (
	"sort"
	"strings"
)

// vrow is one rendered list line: either a single process or a collapsed app
// family ("Brave Browser ×12 6.5GB").
type vrow struct {
	group     bool
	child     bool   // member row rendered under an expanded group
	parentKey string // group key, set on child rows
	name      string
	category  string
	count     int
	memMB     float64
	memPct    float32
	cpu       float64
	trend     int8
	deltaMB   float64
	createMs  int64
	pid       int32
	threads   int32
	user      string
	cmdline   string
	exe       string
	rawName   string
	version   string
	stopped   bool
	expanded  bool
	members   []procRow
}

func (v vrow) key() string { return v.name + "\x00" + v.category }

func procVrow(p procRow) vrow {
	return vrow{
		name: p.name, category: p.category, count: 1,
		memMB: p.memMB, memPct: p.memPct, cpu: p.cpu,
		trend: p.trend, deltaMB: p.deltaMB, createMs: p.createMs,
		pid: p.pid, threads: p.threads, user: p.user,
		cmdline: p.cmdline, exe: p.exe, rawName: p.rawName,
		version: p.version, stopped: p.stopped,
		members: []procRow{p},
	}
}

func (m model) lessV(a, b vrow) bool {
	switch m.sortBy {
	case "cpu":
		return a.cpu > b.cpu
	case "delta":
		if a.deltaMB != b.deltaMB {
			return a.deltaMB > b.deltaMB
		}
		return a.memMB > b.memMB
	case "name":
		return strings.ToLower(a.name) < strings.ToLower(b.name)
	case "category":
		if a.category != b.category {
			return catOrder[a.category] < catOrder[b.category]
		}
		return a.memMB > b.memMB
	case "pid":
		return a.pid < b.pid
	case "time":
		return a.createMs < b.createMs
	default:
		return a.memMB > b.memMB
	}
}

func (m model) sortVrows(v []vrow) {
	sort.SliceStable(v, func(i, j int) bool { return m.lessV(v[i], v[j]) })
}

// viewRows builds the displayed rows: app families when grouping is on (the
// default), one row per process otherwise. Expanded groups emit their members
// as indented child rows.
func (m model) viewRows() []vrow {
	src := m.filtered()
	if !m.grouped {
		out := make([]vrow, 0, len(src))
		for _, p := range src {
			out = append(out, procVrow(p))
		}
		return out
	}

	idx := make(map[string]int, len(src))
	groups := make([]vrow, 0, len(src))
	for _, p := range src {
		k := p.family + "\x00" + p.category
		i, ok := idx[k]
		if !ok {
			idx[k] = len(groups)
			groups = append(groups, vrow{
				group:    true,
				name:     p.family,
				category: p.category,
				createMs: p.createMs,
				pid:      p.pid,
			})
			i = idx[k]
		}
		g := &groups[i]
		g.count++
		g.memMB += p.memMB
		g.memPct += p.memPct
		g.cpu += p.cpu
		g.deltaMB += p.deltaMB
		if g.createMs == 0 || (p.createMs > 0 && p.createMs < g.createMs) {
			g.createMs = p.createMs
		}
		if p.pid < g.pid {
			g.pid = p.pid
		}
		g.members = append(g.members, p)
	}
	for i := range groups {
		g := &groups[i]
		switch {
		case g.deltaMB >= 1:
			g.trend = 1
		case g.deltaMB <= -1:
			g.trend = -1
		}
		g.expanded = m.expanded[g.key()]
	}

	m.sortVrows(groups)

	out := make([]vrow, 0, len(groups)+len(src))
	for _, g := range groups {
		if g.count == 1 {
			out = append(out, procVrow(g.members[0]))
			continue
		}
		out = append(out, g)
		if !g.expanded {
			continue
		}
		kids := make([]vrow, 0, len(g.members))
		for _, p := range g.members {
			kids = append(kids, procVrow(p))
		}
		m.sortVrows(kids)
		for _, k := range kids {
			k.child = true
			k.parentKey = g.key()
			out = append(out, k)
		}
	}
	return out
}

// rowTargets: the processes an action on this row applies to.
func rowTargets(v vrow) []procRow { return v.members }

// rowSummary: aggregate used for confirmation dialogs.
func rowSummary(v vrow) procRow {
	if !v.group || v.count == 1 {
		return v.members[0]
	}
	return procRow{
		pid:      v.pid,
		name:     v.name,
		family:   v.name,
		category: v.category,
		memMB:    v.memMB,
		cpu:      v.cpu,
		createMs: v.createMs,
	}
}

func (m model) rowAt(c int) (vrow, bool) {
	rows := m.viewRows()
	if c < 0 || c >= len(rows) {
		return vrow{}, false
	}
	return rows[c], true
}
