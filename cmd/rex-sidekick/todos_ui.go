package main

import (
	"cmp"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// todoItem is one row of the TODO tab: a list's heading or one of its tasks.
// A sub-group of one of the user's lists has a heading of its own, with Sub
// set to its path.
type todoItem struct {
	todoNode
	group   string // the list's heading
	heading bool   // the row is the list's heading, not a task
	// The row is the "New task" button at the end of one of the user's
	// lists, or of one of its sub-groups.
	addRow bool
	// The row is the "New group" button at the top of the user's section.
	newGroupRow bool
	// The row is the "New sub-group" button at the end of one of the user's
	// lists, or of one of its sub-groups.
	newSubRow bool
	level     int // how many sub-groups deep the row sits
	section   int // todoMine, todoAgents, or todoFinished
	// For a heading: the unfinished tasks showing now in its list or
	// sub-group, the sub-groups inside it included, and whether the user
	// collapsed it.
	open      int
	collapsed bool
}

// task reports whether the row is a task, not a heading or a button.
func (t todoItem) task() bool { return !t.heading && !t.addRow && !t.newGroupRow && !t.newSubRow }

// maxLevel is the deepest the tab indents a sub-group, so a task's text
// keeps room in a narrow pane.
const maxLevel = 6

func indent(level int) string { return strings.Repeat("  ", min(level, maxLevel)) }

// laterList is the list in the user's section that holds their snoozed tasks
// and agents' reminders for them.
const laterList = "Later"

// The TODO tab's sections, top to bottom.
const (
	todoMine = iota
	todoAgents
	todoFinished
)

// rowKey names a row so the cursor can find it again after a reload.
func (t todoItem) rowKey() string {
	sub := ""
	if t.Sub != "" {
		sub = "\x1f" + t.Sub
	}
	switch {
	case t.heading:
		return fmt.Sprintf("list:%v:%s", t.Mine, t.group) + sub
	case t.addRow:
		return "add:" + t.group + sub
	case t.newSubRow:
		return "new-sub:" + t.group + sub
	case t.newGroupRow:
		return "new-group"
	}
	return t.ID
}

// todoInput is the text box for a done note, a new reminder, when to show a
// task, a message to the task's agent, or a name for one of the user's lists.
type todoInput struct {
	adding bool // a new reminder for the user's list
	// The list a new reminder goes on, "" for My list, or the list of the
	// sub-group the box names.
	group string
	// The sub-group a new reminder goes in, the one a new sub-group goes
	// inside, or the one being renamed. Empty means the list itself.
	sub        string
	newSub     bool        // a name for a new sub-group
	renameSub  bool        // a new name for the sub-group at sub
	newGroup   bool        // a name for a new list of the user's
	renameFrom string      // the user's list being renamed
	backendFor string      // the user's list getting a git repo, or leaving one
	scheduling bool        // when to show the task, rather than a done note
	asking     bool        // a message for the task's agent, leaving the task open
	handTo     *Dispatcher // the user's own task, going to the dispatcher
	id         string      // the task being marked done, scheduled, or asked about
	ta         textarea.Model
}

func newTextInput(placeholder string) textarea.Model {
	ta := textarea.New()
	ta.Placeholder = placeholder
	ta.ShowLineNumbers = false
	ta.Prompt = ""
	ta.CharLimit = 0
	ta.KeyMap.InsertNewline.SetEnabled(false) // Enter sends
	ta.FocusedStyle.CursorLine = lipgloss.NewStyle()
	ta.FocusedStyle.Placeholder = sDim
	ta.BlurredStyle.Placeholder = sDim
	ta.Cursor.SetMode(cursor.CursorStatic)
	return ta
}

// reloadTodos reads the TODO list and keeps the cursor on the same task. It
// reports whether a task scheduled for later came due since the last reload.
func (m *model) reloadTodos() bool {
	// Stat before reading, like reload. Clearing expired tasks below changes
	// the mtime again, so the next tick reloads once more.
	mod := m.st.TodosModTime()
	ts, subs, err := m.st.TodosAndSubGroups()
	if err != nil {
		m.setError("reading TODOs: " + err.Error())
		return false
	}
	now := time.Now()
	expired, expiresNext := expiredTodos(ts, now)
	if len(expired) > 0 {
		if _, err := m.st.RemoveTodos(func(t Todo) bool { return expired[t.ID] }); err != nil {
			m.setError("clearing finished lists: " + err.Error())
		} else if ts, subs, err = m.st.TodosAndSubGroups(); err != nil {
			m.setError("reading TODOs: " + err.Error())
			return false
		}
	}
	m.todoMod = mod
	if m.todoBacks, err = m.st.TodoBackends(); err != nil {
		m.setError("reading your lists: " + err.Error())
	}
	var sel string
	if i := m.cursors[tabTodo]; i < len(m.todoItems) {
		sel = m.todoItems[i].rowKey()
	}
	// Only a task whose time came counts, not one the user just moved.
	wasLater := map[string]time.Time{}
	for _, t := range m.todos {
		if t.later(m.todoReload) {
			wasLater[t.ID] = t.ShowAt
		}
	}
	surfaced := false
	for _, t := range ts {
		if at, ok := wasLater[t.ID]; ok && at.Equal(t.ShowAt) && !t.later(now) && !t.Done {
			surfaced = true
		}
	}
	m.todos, m.todoSubs, m.todoReload = ts, subs, now
	names, groups, mine := todoGroups(ts)
	m.todoItems, m.todoLater, m.todoOpen, m.todoWake = nil, map[string]int{}, 0, time.Time{}
	// myGroup is which of the user's own lists it is, for those. A list the
	// user made shows its sub-groups after its own tasks, each with a heading
	// and rows of its own, and ends with a button for another.
	add := func(group string, mine bool, myGroup string, section int, list []Todo) {
		withSubs := mine && myGroup != ""
		var subs []string
		if withSubs {
			subs = m.todoSubs[myGroup]
		}
		var heads []int // the headings showing over the sub-group being added
		var emit func(sub string, hidden bool)
		emit = func(sub string, hidden bool) {
			level := subDepth(sub)
			h := todoItem{todoNode: todoNode{Todo: Todo{Mine: mine, Group: myGroup, Sub: sub}}, group: group, heading: true, section: section, level: level}
			var own []Todo
			for _, t := range list {
				if t.Sub == sub || !withSubs && sub == "" {
					own = append(own, t)
				}
			}
			// A list of nothing but scheduled tasks, like reminders, starts
			// folded, and opening it shows those tasks with when they're due.
			nodes := todoTree(own, now)
			scheduledOnly := slices.ContainsFunc(nodes, func(n todoNode) bool { return n.Later }) &&
				!slices.ContainsFunc(nodes, func(n todoNode) bool { return !n.Done && !n.Later })
			collapsed, set := m.todoCollapsed[h.rowKey()]
			if !set {
				collapsed = scheduledOnly && !mine
			}
			h.collapsed = collapsed
			if !hidden {
				m.todoItems = append(m.todoItems, h)
				heads = append(heads, len(m.todoItems)-1)
			}
			shown := !hidden && !h.collapsed
			for _, n := range nodes {
				if n.later(now) && (m.todoWake.IsZero() || n.ShowAt.Before(m.todoWake)) {
					m.todoWake = n.ShowAt
				}
				switch {
				case n.Later:
					m.todoLater[group]++
				case !n.Done:
					m.todoOpen++
					// A folded heading counts the sub-groups hidden inside it.
					for _, hi := range heads {
						m.todoItems[hi].open++
					}
				}
				if (!n.Later || scheduledOnly) && shown {
					m.todoItems = append(m.todoItems, todoItem{todoNode: n, group: group, section: section, level: level})
				}
			}
			if mine && shown {
				m.todoItems = append(m.todoItems, todoItem{todoNode: todoNode{Todo: Todo{Mine: true, Group: myGroup, Sub: sub}}, group: group, section: section, level: level, addRow: true})
			}
			for _, kid := range subChildren(subs, sub) {
				emit(kid, !shown)
			}
			if withSubs && shown {
				m.todoItems = append(m.todoItems, todoItem{todoNode: todoNode{Todo: Todo{Mine: true, Group: myGroup, Sub: sub}}, group: group, section: section, level: level, newSubRow: true})
			}
			if !hidden {
				heads = heads[:len(heads)-1]
			}
		}
		emit("", false)
	}
	// The user's own lists come first and always show, so there's somewhere
	// to add to: My list, then the ones they made, under a button to make
	// another. Agents' lists meant for the user join them. The rest of the
	// agents' lists follow, with the finished ones at the bottom.
	// The user's snoozed tasks wait in Later, alongside agents' reminders for
	// the user, and go back to the user's list when their time comes.
	// A task waiting on a snoozed one goes with it.
	snoozed := map[string]bool{}
	for changed := true; changed; {
		changed = false
		for _, t := range mine {
			if !snoozed[t.ID] && (t.later(now) || (t.After != "" && snoozed[t.After])) {
				snoozed[t.ID], changed = true, true
			}
		}
	}
	var active, later []Todo
	for _, t := range mine {
		if snoozed[t.ID] {
			later = append(later, t)
		} else {
			active = append(active, t)
		}
	}
	mineNames, mineGroups := myGroups(active)
	made, err := m.st.MyGroups()
	if err != nil {
		m.setError("reading your lists: " + err.Error())
	}
	// A list whose name went missing still shows its tasks.
	for _, name := range mineNames {
		if !slices.Contains(made, name) {
			made = append(made, name)
		}
	}
	// My list stays gone once removed, unless a task turns up on it.
	gone, err := m.st.MyListRemoved()
	if err != nil {
		m.setError("reading your lists: " + err.Error())
	}
	m.todoItems = append(m.todoItems, todoItem{newGroupRow: true, section: todoMine})
	if !gone || len(mineGroups[""]) > 0 {
		add(m.myList, true, "", todoMine, mineGroups[""])
	}
	for _, name := range made {
		add(name, true, name, todoMine, mineGroups[name])
	}
	if l := groups[laterList]; slices.ContainsFunc(l, func(t Todo) bool { return t.ForUser }) {
		later = append(later, l...)
	}
	if len(later) > 0 {
		add(laterList, false, "", todoMine, later)
	}
	var agents, finished []string
	for _, name := range names {
		if name == laterList && slices.ContainsFunc(groups[name], func(t Todo) bool { return t.ForUser }) {
			continue
		}
		switch _, done := finishedAt(groups[name]); {
		case done:
			finished = append(finished, name)
		case slices.ContainsFunc(groups[name], func(t Todo) bool { return t.ForUser }):
			add(name, false, "", todoMine, groups[name])
		default:
			agents = append(agents, name)
		}
	}
	for _, name := range agents {
		add(name, false, "", todoAgents, groups[name])
	}
	for _, name := range finished {
		add(name, false, "", todoFinished, groups[name])
	}
	if !expiresNext.IsZero() && (m.todoWake.IsZero() || expiresNext.Before(m.todoWake)) {
		m.todoWake = expiresNext
	}
	if i := slices.IndexFunc(m.todoItems, func(t todoItem) bool { return t.rowKey() == sel }); i >= 0 {
		m.cursors[tabTodo] = i
	} else {
		// Land on a list's first task rather than its heading.
		i := m.cursors[tabTodo]
		for i+1 < len(m.todoItems) && (m.todoItems[i].heading || m.todoItems[i].newGroupRow || m.todoItems[i].newSubRow) && !m.todoItems[i+1].addRow {
			i++
		}
		if t := m.todoItems[min(i, len(m.todoItems)-1)]; i < len(m.todoItems) && !t.heading && !t.newGroupRow && !t.newSubRow {
			m.cursors[tabTodo] = i
		}
	}
	if in := m.todoInput; in != nil && in.id != "" && !slices.ContainsFunc(m.todoItems, func(t todoItem) bool { return t.ID == in.id }) {
		m.todoInput = nil
	}
	if mv := m.todoMove; mv != nil && !slices.ContainsFunc(m.todoItems, func(t todoItem) bool { return !t.heading && t.ID == mv.id }) {
		m.todoMove = nil
	}
	if _, ok := m.detailTodo(); m.todoDetail != nil && !ok {
		m.todoDetail = nil
		m.note("That task was removed")
	}
	return surfaced
}

// scheduleTodo asks when to show the selected task. It also pushes back a
// task that's already showing.
func (m *model) scheduleTodo() {
	t, ok := m.selectedTodo()
	switch {
	case !ok:
	case t.Done:
		m.note("That task is already done")
	default:
		ta := newTextInput("Show at: 1h, tomorrow, fri 9am, 2026-11-10 14:00 (empty shows it now)")
		ta.Focus()
		m.todoInput = &todoInput{scheduling: true, id: t.ID, ta: ta}
	}
}

// selectedTodo returns the task under the cursor. A list's heading isn't one.
func (m *model) selectedTodo() (todoItem, bool) {
	if i := m.cursor(); m.tab == tabTodo && i < len(m.todoItems) && m.todoItems[i].task() {
		return m.todoItems[i], true
	}
	return todoItem{}, false
}

// selectedNewSubRow returns the "New sub-group" button under the cursor.
func (m *model) selectedNewSubRow() (todoItem, bool) {
	if i := m.cursor(); m.tab == tabTodo && i < len(m.todoItems) && m.todoItems[i].newSubRow {
		return m.todoItems[i], true
	}
	return todoItem{}, false
}

// selectedAddRow returns the "New task" button under the cursor.
func (m *model) selectedAddRow() (todoItem, bool) {
	if i := m.cursor(); m.tab == tabTodo && i < len(m.todoItems) && m.todoItems[i].addRow {
		return m.todoItems[i], true
	}
	return todoItem{}, false
}

// onNewGroupRow reports whether the cursor is on the "New group" button.
func (m *model) onNewGroupRow() bool {
	i := m.cursor()
	return m.tab == tabTodo && i < len(m.todoItems) && m.todoItems[i].newGroupRow
}

// selectedHeading returns the list heading under the cursor.
func (m *model) selectedHeading() (todoItem, bool) {
	if i := m.cursor(); m.tab == tabTodo && i < len(m.todoItems) && m.todoItems[i].heading {
		return m.todoItems[i], true
	}
	return todoItem{}, false
}

// inList reports whether task x belongs to the list or sub-group that
// heading h heads. A list holds the tasks of its sub-groups too.
func inList(h todoItem, x Todo) bool {
	if h.Mine || x.Mine {
		return h.Mine && x.Mine && h.Group == x.Group && inSub(x.Sub, h.Sub)
	}
	name := x.List
	if name == "" {
		name = "an agent"
	}
	return name == h.group
}

// removeList drops every task in the list under the cursor, including ones
// hidden until later. Every list waits for a second x, and confirm is the
// list that got the first one. A list of the user's goes too, My list
// included.
func (m *model) removeList(h todoItem, confirm string) {
	name, total := h.group, 0
	switch {
	case h.Sub != "":
		name = subName(h.Sub)
	case h.Mine && h.Group == "":
		name = "My list"
	}
	for _, x := range m.todos {
		if inList(h, x) {
			total++
		}
	}
	var inside int
	if h.Mine && h.Group != "" {
		inside = len(preorderSubs(m.todoSubs[h.Group], h.Sub))
	}
	switch {
	case total == 0 && !h.Mine:
		m.note("That list is already empty")
		return
	case confirm != h.rowKey():
		m.confirmList = h.rowKey()
		var has []string
		if inside > 0 {
			has = append(has, fmt.Sprintf("its %d %s", inside, plural(inside, "sub-group", "sub-groups")))
		}
		if total > 0 {
			has = append(has, fmt.Sprintf("its %d %s", total, plural(total, "task", "tasks")))
		}
		ask := fmt.Sprintf("Remove %s? It has no tasks.", name)
		if len(has) > 0 {
			ask = fmt.Sprintf("Remove %s and %s?", name, strings.Join(has, " and "))
		}
		if repo := m.todoBacks[h.Group].Git; h.Mine && repo != "" {
			dir, _ := filepath.Rel(repo, subDir(listDir(repo, h.Group), h.Sub))
			ask += fmt.Sprintf(" This deletes %s/ from %s.", dir, repo)
		}
		m.note(ask + " Press x again to confirm.")
		return
	}
	if h.Sub != "" {
		n, err := m.st.RemoveSubGroup(h.Group, h.Sub)
		if err != nil {
			m.setError(err.Error())
		}
		m.reloadTodos()
		m.note(fmt.Sprintf("Removed %s (%d %s)", name, n, plural(n, "task", "tasks")))
		return
	}
	n, err := m.st.RemoveTodos(func(x Todo) bool { return inList(h, x) })
	switch {
	case err != nil || !h.Mine:
	case h.Group == "":
		err = m.st.RemoveMyList()
	default:
		err = m.st.RemoveMyGroup(h.Group)
	}
	if err != nil {
		m.setError(err.Error())
	}
	m.reloadTodos()
	m.note(fmt.Sprintf("Removed %s (%d %s)", name, n, plural(n, "task", "tasks")))
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// toggleTodo checks off the selected task. The user's own tasks flip right
// away. An agent's task first asks for a note to send the agent.
func (m *model) toggleTodo() tea.Cmd {
	t, ok := m.selectedTodo()
	switch {
	case !ok:
	case t.Mine:
		err := m.st.UpdateTodo(t.ID, func(x *Todo, _ []Todo) error {
			x.Done, x.DoneAt, x.DoneBy = !x.Done, time.Now(), "user"
			x.Started = false
			return nil
		})
		if err != nil {
			m.setError(err.Error())
		}
		m.reloadTodos()
	case t.Done:
		m.note("Ask " + fromNameOf(t.From) + " to reopen its task")
	default:
		ta := newTextInput("Note for " + fromNameOf(t.From) + " (optional)")
		ta.Focus()
		m.todoInput = &todoInput{id: t.ID, ta: ta}
	}
	return nil
}

// toggleStarted marks the user's own task in progress, or not. Agents mark
// theirs with `todo start`.
func (m *model) toggleStarted() {
	t, ok := m.selectedTodo()
	switch {
	case !ok:
	case !t.Mine:
		m.note("Agents mark their own tasks in progress")
	case t.Done:
		m.note("That task is already done")
	default:
		if err := m.st.UpdateTodo(t.ID, func(x *Todo, _ []Todo) error {
			x.Started = !x.Started
			return nil
		}); err != nil {
			m.setError(err.Error())
		}
		m.reloadTodos()
	}
}

// askAgent opens a box for a message to the agent that filed the selected
// task. For the user's own task, the box hands it to the dispatcher instead.
func (m *model) askAgent() {
	t, ok := m.selectedTodo()
	switch {
	case !ok:
	case t.Mine && t.Done:
		m.note("That task is already done")
	case t.Mine:
		d, ok := m.st.Dispatcher()
		if !ok {
			m.note("No dispatcher to hand it to. Run `rex-sidekick open --dispatcher` in the agent's pane to set one")
			return
		}
		ta := newTextInput("Note for " + dispatcherName(d) + " (optional)")
		ta.Focus()
		m.todoInput = &todoInput{asking: true, id: t.ID, handTo: &d, ta: ta}
	case t.NotifyBlock == "":
		m.note("That task has no agent pane to message")
	default:
		ta := newTextInput("Message for " + fromNameOf(t.From) + " about this task")
		ta.Focus()
		m.todoInput = &todoInput{asking: true, id: t.ID, ta: ta}
	}
}

// sendToAgent sends the user's message about a task to the agent's pane.
// The user's own task goes to the dispatcher, and the note is optional.
func (m *model) sendToAgent(in *todoInput, text string) tea.Cmd {
	if text == "" && in.handTo == nil {
		return nil
	}
	i := slices.IndexFunc(m.todos, func(t Todo) bool { return t.ID == in.id })
	if i < 0 {
		return nil
	}
	t := m.todos[i]
	e := Entry{NotifySession: t.NotifySession, NotifyBlock: t.NotifyBlock}
	name, msg := fromNameOf(t.From), FormatTodoMessage(t, text)
	if d := in.handTo; d != nil {
		e = Entry{NotifySession: d.Session, NotifyBlock: d.Block}
		name, msg = dispatcherName(*d), FormatTodoHandoff(t, text)
		// The dispatcher has it now.
		if err := m.st.UpdateTodo(t.ID, func(x *Todo, _ []Todo) error {
			x.Started = true
			return nil
		}); err != nil {
			m.setError(err.Error())
		}
		m.reloadTodos()
	}
	if !slices.ContainsFunc(m.answerBlocks, func(b Entry) bool { return b.NotifyBlock == e.NotifyBlock }) {
		m.answerBlocks = append(m.answerBlocks, e)
	}
	m.note("Sent to " + name)
	return func() tea.Msg { return notifiedMsg{msg, Notify(e, msg)} }
}

func dispatcherName(d Dispatcher) string {
	if d.Name == "" {
		return "the dispatcher"
	}
	return d.Name
}

func fromNameOf(from string) string {
	if from == "" {
		return "the agent"
	}
	return from
}

func (m *model) updateTodoInput(msg tea.KeyMsg) tea.Cmd {
	in := m.todoInput
	switch msg.String() {
	case "ctrl+c":
		return tea.Quit
	case "esc":
		m.todoInput = nil
		return nil
	case "enter":
		m.todoInput = nil
		text := strings.Join(strings.Fields(in.ta.Value()), " ")
		if in.scheduling {
			m.saveSchedule(in, text)
			return nil
		}
		if in.asking {
			return m.sendToAgent(in, text)
		}
		if in.newGroup || in.renameFrom != "" {
			m.saveGroupName(in, text)
			return nil
		}
		if in.newSub || in.renameSub {
			m.saveSubName(in, text)
			return nil
		}
		if in.backendFor != "" {
			m.saveBackend(in, strings.TrimSpace(in.ta.Value()))
			return nil
		}
		return m.saveTodoInput(in, text)
	}
	var cmd tea.Cmd
	in.ta, cmd = in.ta.Update(msg)
	return cmd
}

// saveSchedule hides the task until the time the user typed. If the time
// doesn't read, the box stays open to fix it.
func (m *model) saveSchedule(in *todoInput, text string) {
	var at time.Time
	if text != "" {
		var err error
		if at, err = parseWhen(text, time.Now()); err != nil {
			m.setError(err.Error())
			m.todoInput = in
			return
		}
	}
	if err := m.st.UpdateTodo(in.id, func(x *Todo, _ []Todo) error {
		x.ShowAt = at
		return nil
	}); err != nil {
		m.setError(err.Error())
	}
	m.reloadTodos()
	switch {
	case at.IsZero():
		m.note("Showing it now")
	case !at.After(time.Now()):
		m.note("That time has passed, so it shows now")
	default:
		m.note("Hidden until " + formatWhen(at, time.Now()))
	}
}

func (m *model) saveTodoInput(in *todoInput, text string) tea.Cmd {
	if in.adding {
		if text == "" {
			return nil
		}
		id := newTodoID()
		if err := m.st.AddTodos([]Todo{{ID: id, Mine: true, Group: in.group, Sub: in.sub, Text: text, CreatedAt: time.Now()}}); err != nil {
			m.setError(err.Error())
		}
		m.reloadTodos()
		if i := slices.IndexFunc(m.todoItems, func(t todoItem) bool { return t.ID == id }); i >= 0 {
			m.cursors[tabTodo] = i
		}
		return nil
	}
	var done Todo
	err := m.st.UpdateTodo(in.id, func(x *Todo, _ []Todo) error {
		x.Done, x.DoneAt, x.DoneBy, x.DoneNote = true, time.Now(), "user", text
		x.Started = false
		done = *x
		return nil
	})
	m.reloadTodos()
	if err != nil {
		m.setError(err.Error())
		return nil
	}
	if done.NotifyBlock == "" {
		return nil
	}
	e := Entry{NotifySession: done.NotifySession, NotifyBlock: done.NotifyBlock}
	if !slices.ContainsFunc(m.answerBlocks, func(b Entry) bool { return b.NotifyBlock == e.NotifyBlock }) {
		m.answerBlocks = append(m.answerBlocks, e)
	}
	text = FormatTodoNotice(done)
	return func() tea.Msg { return notifiedMsg{text, Notify(e, text)} }
}

func (m *model) removeTodo() {
	t, ok := m.selectedTodo()
	switch {
	case !ok:
	case !t.Mine && !t.Done:
		m.note("Only finished agent tasks can be removed here")
	default:
		if _, err := m.st.RemoveTodos(func(x Todo) bool { return x.ID == t.ID }); err != nil {
			m.setError(err.Error())
		}
		m.reloadTodos()
	}
}

// toggleCollapsed folds the list under the cursor's heading, or opens it.
func (m *model) toggleCollapsed(h todoItem) {
	if m.todoCollapsed == nil {
		m.todoCollapsed = map[string]bool{}
	}
	m.todoCollapsed[h.rowKey()] = !h.collapsed
	m.reloadTodos()
}

// addTodo opens a box for a new task on the user's list, or sub-group, that
// ends at row.
func (m *model) addTodo(row todoItem) {
	ta := newTextInput("New task")
	ta.Focus()
	m.todoInput = &todoInput{adding: true, group: row.Group, sub: row.Sub, ta: ta}
}

// nameSub opens a box for the name of a new sub-group, inside the list or
// sub-group that ends at row.
func (m *model) nameSub(row todoItem) {
	ta := newTextInput("Name of the new sub-group")
	ta.Focus()
	m.todoInput = &todoInput{newSub: true, group: row.Group, sub: row.Sub, ta: ta}
}

// saveSubName makes the sub-group the user named, or renames one. If the
// name doesn't work, the box stays open to fix it.
func (m *model) saveSubName(in *todoInput, name string) {
	var err error
	var path string
	switch {
	case name == "" || in.renameSub && name == subName(in.sub):
		return
	case in.newSub:
		err, path = m.st.AddSubGroup(in.group, in.sub, name), subJoin(in.sub, name)
	default:
		err, path = m.st.RenameSubGroup(in.group, in.sub, name), subJoin(subParent(in.sub), name)
	}
	if err != nil {
		m.setError(err.Error())
		m.todoInput = in
		return
	}
	// A renamed sub-group stays folded, or open, along with the ones inside.
	if in.renameSub {
		prefix := fmt.Sprintf("list:%v:%s\x1f", true, m.groupLabel(in.group))
		for k, v := range m.todoCollapsed {
			if p, ok := strings.CutPrefix(k, prefix); ok && inSub(p, in.sub) {
				delete(m.todoCollapsed, k)
				m.todoCollapsed[prefix+path+p[len(in.sub):]] = v
			}
		}
	}
	m.reloadTodos()
	if i := slices.IndexFunc(m.todoItems, func(t todoItem) bool { return t.heading && t.Mine && t.Group == in.group && t.Sub == path }); i >= 0 {
		m.cursors[tabTodo] = i
	}
}

// groupLabel is the heading of the user's list named group.
func (m *model) groupLabel(group string) string {
	if group == "" {
		return m.myList
	}
	return group
}

// nameGroup opens a box for a new list's name, or for a new name for the
// list of the user's under the cursor.
func (m *model) nameGroup(rename bool) {
	ta := newTextInput("Name of the new list")
	in := &todoInput{newGroup: true}
	if rename {
		h, ok := m.selectedHeading()
		if ok && h.Sub != "" {
			ta.Placeholder = "New name for " + subName(h.Sub)
			ta.SetValue(subName(h.Sub))
			ta.Focus()
			m.todoInput = &todoInput{renameSub: true, group: h.Group, sub: h.Sub, ta: ta}
			return
		}
		if !ok || !h.Mine || h.Group == "" {
			m.note("Only lists you made can be renamed")
			return
		}
		ta.Placeholder = "New name for " + h.Group
		ta.SetValue(h.Group)
		in = &todoInput{renameFrom: h.Group}
	}
	ta.Focus()
	in.ta = ta
	m.todoInput = in
}

// saveGroupName makes the list the user named, or renames one. If the name
// is taken, the box stays open to fix it.
func (m *model) saveGroupName(in *todoInput, name string) {
	if name == "" || name == in.renameFrom {
		return
	}
	// My list's names are free once it's gone.
	gone, err := m.st.MyListRemoved()
	switch {
	case err != nil:
	case !gone && (strings.EqualFold(name, "My list") || name == m.myList), name == laterList:
		err = errGroupExists
	case in.newGroup:
		err = m.st.AddMyGroup(name)
	default:
		err = m.st.RenameMyGroup(in.renameFrom, name)
	}
	if err != nil {
		m.setError(err.Error())
		m.todoInput = in
		return
	}
	m.reloadTodos()
	if i := slices.IndexFunc(m.todoItems, func(t todoItem) bool { return t.heading && t.Mine && t.Group == name }); i >= 0 {
		m.cursors[tabTodo] = i
	}
}

// todoBlocks draws the TODO tab, one block per task. A list's heading goes in
// the block of its first task.
func (m *model) todoBlocks(cur, w int) []string {
	inner := max(10, w-4)
	blocks := make([]string, len(m.todoItems))
	in := m.todoInput
	button := func(label string, selected bool, level int) string {
		if selected {
			return sSel.Render("▌ ") + indent(level) + sQuestion.Bold(true).Render(label) + "\n"
		}
		return "  " + indent(level) + sQuestion.Render(label) + "\n"
	}
	for i, t := range m.todoItems {
		if t.heading {
			block := ""
			if i > 0 && t.section == m.todoItems[i-1].section && t.Sub == "" {
				block = "\n"
			}
			block += m.todoHeading(t, i == cur, inner)
			if in != nil && t.Mine && t.Group != "" && t.Sub == "" && (t.Group == in.renameFrom || t.Group == in.backendFor) {
				block += m.renderTodoInput(inner, t.level)
			}
			if in != nil && in.renameSub && t.Mine && t.Group == in.group && t.Sub == in.sub {
				block += m.renderTodoInput(inner, t.level)
			}
			blocks[i] = block
			continue
		}
		if t.newGroupRow {
			if in != nil && in.newGroup {
				blocks[i] = m.renderTodoInput(inner, 0)
			} else {
				blocks[i] = button("+ New group", i == cur, 0)
			}
			continue
		}
		if t.addRow {
			if in != nil && in.adding && in.group == t.Group && in.sub == t.Sub {
				blocks[i] = m.renderTodoInput(inner, t.level)
			} else {
				blocks[i] = button("+ New task", i == cur, t.level)
			}
			continue
		}
		if t.newSubRow {
			if in != nil && in.newSub && in.group == t.Group && in.sub == t.Sub {
				blocks[i] = m.renderTodoInput(inner, t.level)
			} else {
				blocks[i] = button("+ New sub-group", i == cur, t.level)
			}
			continue
		}
		block := m.renderTodo(t, i == cur, inner)
		if mv := m.todoMove; mv != nil && mv.id == t.ID {
			block += mv.render(inner)
		}
		if in != nil && in.id != "" && in.id == t.ID {
			block += m.renderTodoInput(inner, t.level)
		}
		blocks[i] = block
	}
	for i, t := range m.todoItems {
		body := boxSides(blocks[i], w)
		if i == 0 || t.section != m.todoItems[i-1].section {
			top := boxTop(sectionLabel(t.section), w) + "\n"
			if i > 0 {
				top = "\n" + top
			}
			body = top + body
		}
		if i+1 == len(m.todoItems) || m.todoItems[i+1].section != t.section {
			body += boxBottom(w) + "\n"
		}
		blocks[i] = body
	}
	return blocks
}

func sectionLabel(section int) string {
	switch section {
	case todoAgents:
		return "Agents"
	case todoFinished:
		return "Completed"
	}
	return "Yours"
}

func (m *model) todoHeading(h todoItem, selected bool, w int) string {
	arrow := "▾"
	if h.collapsed {
		arrow = "▸"
	}
	group, name := h.group, h.group
	switch {
	case h.Sub != "":
		name = subName(h.Sub)
	case h.Mine && h.Group == "":
		name = "My list"
	}
	heading := fmt.Sprintf(" %s %s (%d)", arrow, name, h.open)
	if h.Sub == "" && h.Mine && m.todoBacks[h.Group].Git != "" {
		heading += " · git"
	}
	if n := m.todoLater[group]; n > 0 && h.Sub == "" {
		heading += fmt.Sprintf(" · %d later", n)
	}
	pad := indent(h.level)
	if selected {
		return sSel.Render("▌") + pad + sTopic.Render(truncate(heading[1:], w-1-len(pad))) + "\n"
	}
	return pad + sTopic.Render(truncate(heading, w-len(pad))) + "\n"
}

func (m *model) renderTodo(t todoItem, selected bool, w int) string {
	bar := "  "
	textStyle := lipgloss.NewStyle()
	if selected {
		bar = sSel.Render("▌ ")
		textStyle = textStyle.Bold(true)
	}
	mark := "○"
	switch {
	case t.Done:
		mark = sAdd.Render("✓")
		textStyle = sDim.Strikethrough(true)
	case t.Later:
		mark = sDim.Render("◷")
		textStyle = sDim
	case t.Started:
		mark = sQuestion.Render("◐")
	case t.Blocked:
		mark = sDim.Render("○")
		textStyle = sDim
	}
	lead := bar + indent(t.level) + strings.Repeat("  ", min(t.Depth, 4))
	if l := t.treeLead(); l != "" {
		lead += sDim.Render(l)
	}
	lead += mark + " "
	leadW := lipgloss.Width(lead)
	pad := strings.Repeat(" ", leadW)
	inner := max(10, w-leadW)

	// A task with details gets a marker after its text. The details
	// themselves show on their own screen, with i.
	const marker = " (i)"
	text := wrapWords(t.Text, inner)
	var b strings.Builder
	for i, l := range text {
		if i == 0 {
			b.WriteString(lead)
		} else {
			b.WriteString(pad)
		}
		b.WriteString(textStyle.Render(l))
		if i == len(text)-1 && t.Detail != "" {
			if lipgloss.Width(l)+len(marker) > inner {
				b.WriteString("\n" + pad + sInfo.Render(marker[1:]))
			} else {
				b.WriteString(sInfo.Render(marker))
			}
		}
		b.WriteString("\n")
	}
	if t.later(time.Now()) {
		for _, l := range wrapWords("shows "+formatWhen(t.ShowAt, time.Now()), inner) {
			b.WriteString(pad + sDim.Render(l) + "\n")
		}
	}
	if !selected {
		return b.String()
	}
	var extra []string
	var meta []string
	switch {
	case !t.Mine:
		meta = append(meta, fromNameOf(t.From))
	case t.From != "":
		meta = append(meta, "added by "+t.From)
	}
	meta = append(meta, ago(t.CreatedAt))
	if t.Blocked {
		meta = append(meta, "waiting on the task above")
	}
	extra = append(extra, strings.Join(meta, " · "))
	if t.Done && !t.Mine {
		by := "the agent"
		if t.DoneBy == "user" {
			by = "you"
		}
		done := "done by " + by
		if t.DoneNote != "" {
			done += ": " + t.DoneNote
		}
		extra = append(extra, done)
	}
	for _, e := range extra {
		for _, l := range wrapWords(e, inner) {
			b.WriteString(pad + sDim.Render(l) + "\n")
		}
	}
	return b.String()
}

// renderTodoInput draws the open text box, level sub-groups deep.
func (m *model) renderTodoInput(w, level int) string {
	in := m.todoInput
	// A new task takes a task's place in the list, so it lines up with its
	// siblings, and a new list or sub-group takes its button's place.
	// Anything else answers the row above and sits under it.
	ind := indent(level)
	lead, pad := "     "+ind, "     "+ind
	if in.adding {
		lead, pad = sSel.Render("▌ ")+ind+"○ ", "    "+ind
	} else if in.newGroup || in.newSub {
		lead, pad = sSel.Render("▌ ")+ind, "  "+ind
	}
	in.ta.SetWidth(max(10, w-1-len(pad)))
	in.ta.SetHeight(2)
	var b strings.Builder
	for i, l := range strings.Split(in.ta.View(), "\n") {
		if i == 0 {
			b.WriteString(lead + l + "\n")
		} else {
			b.WriteString(pad + l + "\n")
		}
	}
	return b.String()
}

// todoDetail is the screen for an agent's task's details: a plan or notes in
// Markdown. The user's own tasks open in their editor instead.
type todoDetail struct {
	id string
	// The view scrolls when the details are taller than the pane. top is the
	// first line shown, and maxTop the last line it can start at, as of the
	// last draw.
	top, maxTop int
}

// openTodoDetail edits the selected task, when it's the user's own, or shows
// an agent's details to read. Agents write their tasks' details, and their
// next `todo update` would overwrite the user's edits.
func (m *model) openTodoDetail() {
	t, ok := m.selectedTodo()
	switch {
	case !ok:
	case t.Mine:
		m.editTodo(t.Todo)
	case t.Detail == "":
		m.note("That task has no details")
	default:
		m.todoDetail = &todoDetail{id: t.ID}
	}
}

// detailTodo returns the task the details screen is for.
func (m *model) detailTodo() (Todo, bool) {
	if m.todoDetail == nil {
		return Todo{}, false
	}
	i := slices.IndexFunc(m.todos, func(t Todo) bool { return t.ID == m.todoDetail.id })
	if i < 0 {
		return Todo{}, false
	}
	return m.todos[i], true
}

func (m *model) updateTodoDetail(msg tea.KeyMsg) tea.Cmd {
	d := m.todoDetail
	switch msg.String() {
	case "ctrl+c":
		return tea.Quit
	case "esc", "q", "i":
		m.todoDetail = nil
	case "j", "down":
		d.top = min(d.top+1, d.maxTop)
	case "k", "up":
		d.top = max(d.top-1, 0)
	case "c":
		// Message the agent from here too, back on the list.
		m.todoDetail = nil
		m.askAgent()
	}
	return nil
}

// renderTodoDetail draws the details screen in h lines.
func (m *model) renderTodoDetail(h, w int) string {
	d := m.todoDetail
	t, _ := m.detailTodo()
	inner := max(10, w-2)
	var head []string
	for _, l := range wrapWords(cmp.Or(t.List, "an agent"), inner) {
		head = append(head, " "+sTopic.Render(l))
	}
	for _, l := range wrapWords(t.Text, inner) {
		head = append(head, " "+lipgloss.NewStyle().Bold(true).Render(l))
	}
	meta := []string{fromNameOf(t.From), ago(t.CreatedAt)}
	if t.Done {
		meta = append(meta, "done")
	}
	for _, l := range wrapWords(strings.Join(meta, " · "), inner) {
		head = append(head, " "+sDim.Render(l))
	}
	head = append(head, "")

	lines := head
	for _, l := range renderMarkdown(t.Detail, inner) {
		lines = append(lines, strings.TrimRight(" "+l, " "))
	}
	d.maxTop = max(0, len(lines)-h)
	d.top = min(d.top, d.maxTop)
	shown := lines[d.top:]
	if len(shown) > h {
		shown = shown[:h]
		below := len(lines) - d.top - h + 1
		shown[h-1] = " " + sDim.Render(fmt.Sprintf("↓ %d more %s (j)", below, plural(below, "line", "lines")))
	}
	return strings.Join(shown, "\n") + "\n"
}

// askBackend opens a box for the git repo that the list of the user's under
// the cursor keeps its tasks in. Clearing it moves the list back to the
// session.
func (m *model) askBackend() {
	h, ok := m.selectedHeading()
	if ok && h.Sub != "" {
		m.note("A sub-group lives wherever its list does")
		return
	}
	if !ok || !h.Mine || h.Group == "" {
		m.note("Only lists you made can live in a git repo")
		return
	}
	ta := newTextInput("Git repo for " + h.Group + ", like ~/todos (empty keeps it in this session)")
	ta.SetValue(m.todoBacks[h.Group].Git)
	ta.Focus()
	m.todoInput = &todoInput{backendFor: h.Group, ta: ta}
}

// saveBackend moves the list to the repo the user typed, or back to the
// session. If that fails, the box stays open to fix it.
func (m *model) saveBackend(in *todoInput, repo string) {
	if repo == m.todoBacks[in.backendFor].Git {
		return
	}
	if err := m.st.SetTodoBackend(in.backendFor, todoBackend{Git: repo}); err != nil {
		m.setError(err.Error())
		m.todoInput = in
		return
	}
	m.reloadTodos()
	if b := m.todoBacks[in.backendFor]; b.Git != "" {
		m.note(in.backendFor + " now lives in " + b.Git)
	} else {
		m.note(in.backendFor + " now lives in this session")
	}
}

// todoSyncedMsg says the git repos of the user's lists were pulled and
// pushed.
type todoSyncedMsg struct{ err error }

// syncTodos pulls and pushes the git repos the user's lists live in.
func (m *model) syncTodos() tea.Cmd {
	if len(gitRepos(m.todoBacks)) == 0 {
		return nil
	}
	st := m.st
	return func() tea.Msg { return todoSyncedMsg{st.SyncTodos()} }
}
