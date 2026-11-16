package main

import (
	"fmt"
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// workerPollEvery is how often the sidekick reads the workers' panes.
const workerPollEvery = 3 * time.Second

var sBlocked = lipgloss.NewStyle().Foreground(colClosed).Bold(true)

// paneWatch is what the sidekick has seen of a worker's pane.
type paneWatch struct {
	pane      paneState
	read      bool      // the pane has been read at least once
	screen    string    // the last capture, to tell when it changes
	changedAt time.Time // when the screen last changed: the worker's last activity
	// When the prompt it's waiting on showed up, and whether the bell rang
	// for it. A prompt has to last two reads before it rings, so one the
	// user answers right away doesn't.
	promptSince time.Time
	rang        bool
}

// workersMsg is one read of every worker's pane.
type workersMsg struct {
	screens map[string]string // by block, for the panes that could be read
	live    map[string]bool   // the session's Rex tabs, nil if Rex couldn't list them
}

func (m *model) reloadWorkers() {
	mod := m.st.WorkersModTime() // before the read, like reload
	ws, err := m.st.Workers()
	if err != nil {
		m.setError("reading workers: " + err.Error())
		return
	}
	m.workers, m.workerMod = ws, mod
}

// pollWorkers reads every worker's pane when it's time.
func (m *model) pollWorkers() tea.Cmd {
	if len(m.workers) == 0 || m.workersBusy || time.Since(m.workersAt) < workerPollEvery {
		return nil
	}
	m.workersBusy, m.workersAt = true, time.Now()
	session, ws := m.st.Session, slices.Clone(m.workers)
	return func() tea.Msg {
		msg := workersMsg{screens: map[string]string{}}
		msg.live, _ = liveWindows(session)
		for _, w := range ws {
			if screen, err := capturePaneText(session, w.Block); err == nil {
				msg.screens[w.Block] = screen
			}
		}
		return msg
	}
}

// gotWorkers takes in a read of the workers' panes. It drops workers whose
// tab has closed, and rings the bell when one has been waiting on a prompt
// since the last read.
func (m *model) gotWorkers(msg workersMsg) tea.Cmd {
	m.workersBusy = false
	if msg.live != nil {
		if n, err := m.st.RemoveWorkers(func(w Worker) bool { return !msg.live[w.Window] }); err != nil {
			m.setError("dropping closed workers: " + err.Error())
		} else if n > 0 {
			m.reloadWorkers()
		}
	}
	if m.panes == nil {
		m.panes = map[string]*paneWatch{}
	}
	now := time.Now()
	var ring []string
	for _, w := range m.workers {
		screen, ok := msg.screens[w.Block]
		if !ok {
			continue
		}
		p := m.panes[w.Block]
		if p == nil {
			p = &paneWatch{changedAt: now}
			m.panes[w.Block] = p
		}
		if p.read && screen != p.screen {
			p.changedAt = now
		}
		prev := p.pane.Prompt
		p.read, p.screen, p.pane = true, screen, readPane(screen)
		switch {
		case p.pane.Prompt == "":
			p.promptSince, p.rang = time.Time{}, false
		case prev != "" && prev != p.pane.Prompt:
			// A different prompt took its place.
			p.promptSince, p.rang = now, false
		case p.promptSince.IsZero():
			p.promptSince = now
		case !p.rang:
			p.rang = true
			ring = append(ring, w.Name)
		}
	}
	for b := range m.panes {
		if !slices.ContainsFunc(m.workers, func(w Worker) bool { return w.Block == b }) {
			delete(m.panes, b)
		}
	}
	if len(ring) == 0 {
		return nil
	}
	m.note(strings.Join(ring, ", ") + " waiting on a prompt in its tab. See the Workers tab")
	return bell
}

// workerViews lists the Workers tab: workers waiting on a prompt first, then
// those waiting on the user in the inbox, then the rest, with finished ones
// last. Each group keeps the order the workers were added in.
func (m *model) workerViews() []workerView {
	var out []workerView
	for _, w := range m.workers {
		p := m.panes[w.Block]
		if p == nil {
			p = &paneWatch{}
		}
		v := viewWorker(w, p.pane, p.read, m.questions, m.entries, m.todos)
		for _, e := range m.closed {
			if e.NotifyBlock == w.Block && !slices.ContainsFunc(v.Linked, func(l Entry) bool { return l.URL == e.URL }) {
				v.Linked = append(v.Linked, e)
			}
		}
		out = append(out, v)
	}
	rank := func(v workerView) int {
		switch v.State {
		case stPrompt:
			return 0
		case stQuestion:
			return 1
		case stDone:
			return 3
		}
		return 2
	}
	slices.SortStableFunc(out, func(a, b workerView) int { return rank(a) - rank(b) })
	return out
}

// blockedWorkers counts the workers waiting on a prompt.
func (m *model) blockedWorkers() int {
	n := 0
	for _, w := range m.workers {
		if p := m.panes[w.Block]; p != nil && p.pane.Prompt != "" {
			n++
		}
	}
	return n
}

func (m *model) selectedWorker() (workerView, bool) {
	vs, i := m.workerViews(), m.cursors[tabWorkers]
	if i >= 0 && i < len(vs) {
		return vs[i], true
	}
	return workerView{}, false
}

// focusWorker brings the worker's pane to the front, so the user can answer
// its prompt or talk to it.
func (m *model) focusWorker(v workerView) tea.Cmd {
	session := m.st.Session
	return func() tea.Msg {
		return cmdErrMsg{what: "focusing " + v.Name, err: focusPane(session, v.Block)}
	}
}

// removeWorker takes a worker off the tab. Its tab stays open.
func (m *model) removeWorker(v workerView) {
	if _, err := m.st.RemoveWorkers(func(w Worker) bool { return w.Name == v.Name }); err != nil {
		m.setError(err.Error())
	}
	m.reloadWorkers()
}

// workerBlocks draws the Workers tab, one block per worker, in a box per
// group: Waiting on a prompt, Workers, and Done.
func (m *model) workerBlocks(cur, w int) []string {
	vs := m.workerViews()
	inner := max(10, w-4)
	group := func(v workerView) string {
		switch v.State {
		case stPrompt:
			return "Waiting on a prompt"
		case stDone:
			return "Done"
		}
		return "Workers"
	}
	blocks := make([]string, len(vs))
	for i, v := range vs {
		first := i == 0 || group(vs[i-1]) != group(v)
		last := i+1 == len(vs) || group(vs[i+1]) != group(v)
		block := m.renderWorker(v, i == cur, inner)
		if last {
			block = strings.TrimSuffix(block, "\n")
		}
		block = boxSides(block, w)
		if first {
			n := 0
			for _, x := range vs {
				if group(x) == group(v) {
					n++
				}
			}
			title := fmt.Sprintf("%s (%d)", group(v), n)
			if v.State == stPrompt {
				title = sBlocked.Render(title)
			}
			top := boxTop(title, w) + "\n"
			if i > 0 {
				top = "\n" + top
			}
			block = top + block
		}
		if last {
			block += boxBottom(w) + "\n"
		}
		blocks[i] = block
	}
	return blocks
}

// renderWorker draws one worker, wrapped to w columns, followed by a blank
// line: its name and state, what it's waiting on, its task, and its PRs,
// plan and last activity.
func (m *model) renderWorker(v workerView, selected bool, w int) string {
	bar, nameStyle := "  ", lipgloss.NewStyle()
	if selected {
		bar, nameStyle = sSel.Render("▌ "), nameStyle.Bold(true)
	}
	const indent = "    "
	inner := max(10, w-len(indent))

	mark, style := "●", sAdd
	switch v.State {
	case stPrompt:
		mark, style = "!", sBlocked
	case stQuestion:
		mark, style = "?", sQuestion
	case stPR:
		mark, style = "⎇", sAdd
	case stIdle, stDone:
		mark, style = "○", sDim
	}
	head := []string{nameStyle.Render(v.Name), style.Render(v.State)}
	var lines []string
	for i, l := range wrapItems(head, sDim.Render(" · "), inner) {
		lead := indent
		if i == 0 {
			lead = bar + style.Render(mark) + " "
		}
		lines = append(lines, lead+l)
	}
	if v.Prompt != "" {
		for _, l := range wrapWords(v.Prompt, inner) {
			lines = append(lines, indent+sBlocked.Render(l))
		}
	}
	if v.Task != "" {
		for _, l := range wrapWords(v.Task, inner) {
			lines = append(lines, indent+l)
		}
	}

	var meta []string
	for _, e := range v.Linked {
		label := fmt.Sprintf("%s #%d", e.Repo[strings.LastIndex(e.Repo, "/")+1:], e.Number)
		style := sDim.Underline(true)
		if m.isClosed(e.URL) {
			label += " " + strings.ToLower(m.statuses[e.URL].State)
		} else if slices.ContainsFunc(m.entries, func(x Entry) bool { return x.URL == e.URL }) {
			style = sAdd.Underline(true)
		}
		meta = append(meta, link(e.URL, style.Render(label)))
	}
	if v.Steps > 0 {
		meta = append(meta, sDim.Render(fmt.Sprintf("plan %d/%d", v.StepsDone, v.Steps)))
	}
	if v.Todo != "" {
		meta = append(meta, sDim.Render("todo "+v.Todo))
	}
	if p := m.panes[v.Block]; p != nil && p.read {
		switch {
		case v.State == stPrompt && !p.promptSince.IsZero():
			waited := strings.TrimSuffix(ago(p.promptSince), " ago")
			if waited == "just now" {
				waited = "<1m"
			}
			meta = append(meta, sBlocked.Render("waiting "+waited))
		case v.State == stWorking:
			meta = append(meta, sDim.Render("active "+ago(p.changedAt)))
		default:
			meta = append(meta, sDim.Render("last active "+ago(p.changedAt)))
		}
	}
	for _, l := range wrapItems(meta, sDim.Render(" · "), inner) {
		lines = append(lines, indent+l)
	}
	return strings.Join(lines, "\n") + "\n\n"
}
