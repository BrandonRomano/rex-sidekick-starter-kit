package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func todoModel(t *testing.T, st *Store) *model {
	t.Helper()
	m := newModel(st, time.Minute)
	m.myList = "My list"
	m.width, m.height = 63, 40
	m.Init()
	m.Update(key("2"))
	if m.tab != tabTodo {
		t.Fatalf("2 should open the TODO tab, got %d", m.tab)
	}
	return m
}

// selectTask puts the TODO cursor on the task with the given ID.
func selectTask(t *testing.T, m *model, id string) {
	t.Helper()
	i := slices.IndexFunc(m.todoItems, func(it todoItem) bool { return !it.heading && it.ID == id })
	if i < 0 {
		t.Fatalf("no task %s on the tab", id)
	}
	m.cursors[tabTodo] = i
}

// selectList puts the TODO cursor on a list's heading.
func selectList(t *testing.T, m *model, group string) {
	t.Helper()
	i := slices.IndexFunc(m.todoItems, func(it todoItem) bool { return it.heading && it.group == group })
	if i < 0 {
		t.Fatalf("no list %q on the tab", group)
	}
	m.cursors[tabTodo] = i
}

func TestTodoTreeNumbersChains(t *testing.T) {
	ts := []Todo{
		{ID: "a", Text: "Write migration", Done: true},
		{ID: "c", Text: "Drop old table", After: "b"},
		{ID: "b", Text: "Backfill", After: "a"},
		{ID: "d", Text: "Update docs"},
		// e has two dependents, so they nest under it. f starts a chain of
		// its own one level down.
		{ID: "e", Text: "Cut the release"},
		{ID: "f", Text: "Announce it", After: "e"},
		{ID: "g", Text: "Tag it", After: "e"},
		{ID: "h", Text: "Post the notes", After: "f"},
	}
	got := todoTree(ts, time.Now())
	want := []struct {
		id      string
		depth   int
		step    int
		blocked bool
		lead    string
	}{
		{"a", 0, 1, false, "1. "}, {"b", 0, 2, false, "2. "}, {"c", 0, 3, true, "3. "},
		{"d", 0, 0, false, ""},
		{"e", 0, 0, false, ""}, {"f", 1, 1, true, "↳ 1. "}, {"h", 1, 2, true, "  2. "}, {"g", 1, 0, true, "↳ "},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d nodes, want %d", len(got), len(want))
	}
	for i, w := range want {
		g := got[i]
		if g.ID != w.id || g.Depth != w.depth || g.Step != w.step || g.Blocked != w.blocked || g.treeLead() != w.lead {
			t.Errorf("node %d: got %s depth %d step %d blocked %v lead %q, want %+v", i, g.ID, g.Depth, g.Step, g.Blocked, g.treeLead(), w)
		}
	}
}

func TestLongChainNumbersLineUp(t *testing.T) {
	var ts []Todo
	for i := range 10 {
		td := Todo{ID: fmt.Sprint(i), Text: "step"}
		if i > 0 {
			td.After = fmt.Sprint(i - 1)
		}
		ts = append(ts, td)
	}
	got := todoTree(ts, time.Now())
	if a, b := got[0].treeLead(), got[9].treeLead(); a != " 1. " || b != "10. " {
		t.Errorf("leads %q and %q should be the same width", a, b)
	}
}

func TestRemoveTodoReattachesDependents(t *testing.T) {
	st := testStore(t)
	if err := st.AddTodos([]Todo{{ID: "a", Text: "one"}, {ID: "b", Text: "two", After: "a"}, {ID: "c", Text: "three", After: "b"}}); err != nil {
		t.Fatal(err)
	}
	if n, err := st.RemoveTodos(func(x Todo) bool { return x.ID == "b" }); err != nil || n != 1 {
		t.Fatalf("remove: %d, %v", n, err)
	}
	ts, _ := st.Todos()
	if len(ts) != 2 || ts[1].ID != "c" || ts[1].After != "a" {
		t.Errorf("c should now wait on a, got %+v", ts)
	}
}

func TestDoneNoteGoesToAgent(t *testing.T) {
	st := testStore(t)
	err := st.AddTodos([]Todo{
		{ID: "t-1", List: "api-agent", From: "api-agent", Text: "Rotate the staging key", CreatedAt: time.Now()},
		{ID: "t-2", List: "api-agent", From: "api-agent", Text: "Redeploy staging", After: "t-1", CreatedAt: time.Now()},
	})
	if err != nil {
		t.Fatal(err)
	}
	m := todoModel(t, st)
	selectTask(t, m, "t-1")
	view := m.View()
	for _, want := range []string{"TODO (2)", "api-agent (2)", "1. ○ Rotate the staging key", "2. ○ Redeploy staging", "My list (0)", "+ New task"} {
		if !strings.Contains(view, want) {
			t.Errorf("%q missing:\n%s", want, view)
		}
	}

	m.Update(key("space"))
	if m.todoInput == nil || m.todoInput.adding {
		t.Fatalf("space on an agent's task should ask for a note")
	}
	for _, r := range "rotated it" {
		m.Update(key(string(r)))
	}
	// No pane to notify, so the command just reports success.
	_, cmd := m.Update(key("enter"))
	ts, _ := st.Todos()
	if !ts[0].Done || ts[0].DoneBy != "user" || ts[0].DoneNote != "rotated it" {
		t.Errorf("task should be done with the note, got %+v", ts[0])
	}
	if cmd != nil {
		t.Errorf("a task with no pane shouldn't send anything")
	}
	if got, want := FormatTodoNotice(ts[0]), `[sidekick] todo t-1 "Rotate the staging key" done by the user. Note: "rotated it"`; got != want {
		t.Errorf("notice %q, want %q", got, want)
	}
	if !strings.Contains(m.View(), "TODO (1)") {
		t.Errorf("the tab count should drop to 1:\n%s", m.View())
	}
}

func TestMyListAddAndCheckOff(t *testing.T) {
	st := testStore(t)
	m := todoModel(t, st)
	m.cursors[tabTodo] = slices.IndexFunc(m.todoItems, func(i todoItem) bool { return i.addRow })
	m.Update(key("enter"))
	for _, r := range "call the dentist" {
		m.Update(key(string(r)))
	}
	m.Update(key("enter"))
	ts, _ := st.Todos()
	if len(ts) != 1 || !ts[0].Mine || ts[0].Text != "call the dentist" {
		t.Fatalf("want one reminder on my list, got %+v", ts)
	}
	if view := m.View(); !strings.Contains(view, "My list (1)") || !strings.Contains(view, "call the dentist") {
		t.Errorf("my list should show the reminder:\n%s", view)
	}

	_, cmd := m.Update(key("space"))
	ts, _ = st.Todos()
	if !ts[0].Done || cmd != nil || m.todoInput != nil {
		t.Errorf("space should check off my own task without a note or a message")
	}
	m.Update(key("x"))
	if ts, _ = st.Todos(); len(ts) != 0 {
		t.Errorf("x should remove my task, got %+v", ts)
	}
}

func TestTodoViewFitsWidth(t *testing.T) {
	st := testStore(t)
	long := strings.Repeat("a long task description ", 5)
	err := st.AddTodos([]Todo{
		{ID: "a", List: "agent-with-a-very-long-tab-name-for-testing", Text: long, Detail: long},
		{ID: "b", List: "agent-with-a-very-long-tab-name-for-testing", Text: long, After: "a"},
		{ID: "c", List: "agent-with-a-very-long-tab-name-for-testing", Text: long, After: "b"},
	})
	if err != nil {
		t.Fatal(err)
	}
	m := todoModel(t, st)
	for _, w := range []int{30, 45, 63} {
		m.width = w
		for i := range 3 {
			m.cursors[tabTodo] = i
			for _, line := range strings.Split(m.View(), "\n") {
				if lipgloss.Width(line) > w {
					t.Errorf("width %d: line is %d wide: %q", w, lipgloss.Width(line), line)
				}
			}
		}
	}
}

func TestParseWhen(t *testing.T) {
	loc := time.FixedZone("test", -5*3600)
	now := time.Date(2026, 11, 3, 14, 30, 0, 0, loc) // a Tuesday
	at := func(d, h, min int) time.Time { return time.Date(2026, 11, d, h, min, 0, 0, loc) }
	for in, want := range map[string]time.Time{
		"1h":                   now.Add(time.Hour),
		"90m":                  now.Add(90 * time.Minute),
		"3d":                   now.Add(72 * time.Hour),
		"1w":                   now.Add(7 * 24 * time.Hour),
		"tomorrow":             at(4, 0, 0),
		"Tomorrow 9am":         at(4, 9, 0),
		"today 17:00":          at(3, 17, 0),
		"fri":                  at(6, 0, 0),
		"tue 3:15pm":           at(10, 15, 15), // next Tuesday, not today
		"16:00":                at(3, 16, 0),
		"9:00":                 at(4, 9, 0), // already past today
		"2026-11-10":           at(10, 0, 0),
		"2026-11-10 14:00":     at(10, 14, 0),
		"2026-11-10T14:00:00Z": time.Date(2026, 11, 10, 14, 0, 0, 0, time.UTC),
	} {
		got, err := parseWhen(in, now)
		if err != nil || !got.Equal(want) {
			t.Errorf("parseWhen(%q) = %v, %v, want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "soon", "-1h", "0d", "tomorrow at 9", "25:00"} {
		if _, err := parseWhen(in, now); err == nil {
			t.Errorf("parseWhen(%q) should fail", in)
		}
	}
}

func TestFormatWhen(t *testing.T) {
	now := time.Date(2026, 11, 3, 14, 30, 0, 0, time.Local)
	for want, at := range map[string]time.Time{
		"16:00":          time.Date(2026, 11, 3, 16, 0, 0, 0, time.Local),
		"tomorrow":       time.Date(2026, 11, 4, 0, 0, 0, 0, time.Local),
		"tomorrow 09:00": time.Date(2026, 11, 4, 9, 0, 0, 0, time.Local),
		"Fri 09:00":      time.Date(2026, 11, 6, 9, 0, 0, 0, time.Local),
		"Nov 10 14:00":   time.Date(2026, 11, 10, 14, 0, 0, 0, time.Local),
		"Jan 5 2027":     time.Date(2027, 1, 5, 0, 0, 0, 0, time.Local),
	} {
		if got := formatWhen(at, now); got != want {
			t.Errorf("formatWhen(%v) = %q, want %q", at, got, want)
		}
	}
}

func TestScheduledTasksHideUntilDue(t *testing.T) {
	st := testStore(t)
	err := st.AddTodos([]Todo{
		{ID: "a", Mine: true, Text: "call the dentist", ShowAt: time.Now().Add(time.Hour)},
		{ID: "b", Mine: true, Text: "confirm the time", After: "a"},
		{ID: "c", Mine: true, Text: "water the plants"},
	})
	if err != nil {
		t.Fatal(err)
	}
	m := todoModel(t, st)
	view := m.View()
	if strings.Contains(view, "dentist") || strings.Contains(view, "confirm the time") {
		t.Errorf("a scheduled task and the one waiting on it should be hidden:\n%s", view)
	}
	for _, want := range []string{"TODO (1)", "My list (1)", "▸ Later (0) · 2 later", "water the plants", "z snooze"} {
		if !strings.Contains(view, want) {
			t.Errorf("%q missing:\n%s", want, view)
		}
	}

	// A bad time keeps the box open.
	m.cursors[tabTodo] = slices.IndexFunc(m.todoItems, func(i todoItem) bool { return i.ID == "c" })
	m.Update(key("z"))
	for _, r := range "someday" {
		m.Update(key(string(r)))
	}
	m.Update(key("enter"))
	if m.todoInput == nil {
		t.Error("a time that doesn't read should keep the box open")
	}
	m.Update(key("esc"))

	// Snooze the plants until tomorrow.
	m.Update(key("z"))
	if m.todoInput == nil || !m.todoInput.scheduling {
		t.Fatal("z should ask when to show the task")
	}
	for _, r := range "tomorrow" {
		m.Update(key(string(r)))
	}
	m.Update(key("enter"))
	ts, _ := st.Todos()
	if !ts[2].later(time.Now()) {
		t.Errorf("the plants should be pushed to tomorrow, got %+v", ts[2])
	}
	if view := m.View(); strings.Contains(view, "water the plants") || !strings.Contains(view, "TODO (0)") {
		t.Errorf("the snoozed task should be hidden:\n%s", view)
	}

	// When the time comes, the task shows up and the bell rings.
	if err := st.UpdateTodo("a", func(x *Todo, _ []Todo) error {
		x.ShowAt = time.Now().Add(50 * time.Millisecond)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	m.reloadTodos()
	if strings.Contains(m.View(), "dentist") {
		t.Fatal("the dentist should still be hidden")
	}
	time.Sleep(60 * time.Millisecond)
	if _, cmd := m.Update(tickMsg(time.Now())); cmd == nil {
		t.Error("the tick should return commands")
	}
	if view := m.View(); !strings.Contains(view, "call the dentist") || !strings.Contains(view, "confirm the time") {
		t.Errorf("the due task and the one waiting on it should show:\n%s", view)
	}
}

func TestTallTodoScrolls(t *testing.T) {
	st := testStore(t)
	long := strings.Repeat("a very long task that goes on and on ", 40) + "THE END"
	err := st.AddTodos([]Todo{
		{ID: "a", Mine: true, Text: long},
		{ID: "b", Mine: true, Text: "short one"},
	})
	if err != nil {
		t.Fatal(err)
	}
	m := todoModel(t, st)
	m.height = 20
	view := m.View()
	if n := strings.Count(view, "\n") + 1; n != m.height {
		t.Errorf("the view should fill the screen exactly, got %d lines:\n%s", n, view)
	}
	if !strings.Contains(view, "more lines (j)") || strings.Contains(view, "THE END") {
		t.Errorf("a tall task should show its top and a hint:\n%s", view)
	}

	// Rows 0 and 1 are the New group button and the list's heading, so the
	// tall task is row 2.
	for range 100 {
		m.Update(key("j"))
		m.View()
		if m.cursor() != 2 {
			break
		}
	}
	if m.cursor() != 3 {
		t.Fatalf("j should scroll through the task and then move on, cursor %d", m.cursor())
	}
	m.Update(key("k"))
	if view := m.View(); m.cursor() != 2 || strings.Contains(view, "THE END") {
		t.Errorf("k should go back to the top of the tall task:\n%s", view)
	}
	for range 100 {
		m.Update(key("j"))
		if m.View(); m.scroll == m.maxScroll {
			break
		}
	}
	if view := m.View(); !strings.Contains(view, "THE END") || !strings.Contains(view, "k to scroll back up") {
		t.Errorf("scrolled to the end, the task's last line should show:\n%s", view)
	}
}

func TestAskAgentAboutTask(t *testing.T) {
	st := testStore(t)
	err := st.AddTodos([]Todo{
		{ID: "t-1", List: "api-agent", From: "api-agent", Text: "Rotate the staging key", CreatedAt: time.Now()},
		{ID: "t-2", Mine: true, Text: "call the dentist", CreatedAt: time.Now()},
	})
	if err != nil {
		t.Fatal(err)
	}
	m := todoModel(t, st)
	selectTask(t, m, "t-1")
	m.Update(key("c"))
	if m.todoInput != nil {
		t.Fatal("a task with no agent pane shouldn't open the box")
	}

	if err := st.UpdateTodo("t-1", func(x *Todo, _ []Todo) error { x.NotifyBlock = "block:test"; return nil }); err != nil {
		t.Fatal(err)
	}
	m.reloadTodos()
	m.Update(key("c"))
	if m.todoInput == nil || !m.todoInput.asking {
		t.Fatal("c on an agent's task should open the box")
	}
	m.Update(key("esc"))
	if m.todoInput != nil {
		t.Fatal("esc should close the box")
	}
	if ts, _ := st.Todos(); ts[0].Done {
		t.Error("asking shouldn't finish the task")
	}
	if got, want := FormatTodoMessage(m.todos[0], "use the new vault path"), `[sidekick] todo t-1 "Rotate the staging key", message from the user: "use the new vault path"`; got != want {
		t.Errorf("message %q, want %q", got, want)
	}

	selectTask(t, m, "t-2")
	m.Update(key("c"))
	if m.todoInput != nil {
		t.Error("with no dispatcher, the user's own task has nobody to go to")
	}
	if !strings.Contains(m.View(), "--dispatcher") {
		t.Errorf("it should say how to set a dispatcher:\n%s", m.View())
	}
}

func TestHandOwnTaskToDispatcher(t *testing.T) {
	st := testStore(t)
	err := st.AddTodos([]Todo{{ID: "t-1", Mine: true, Text: "Fix the flaky login test", Detail: "fails on CI about 1 in 5", CreatedAt: time.Now()}})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetDispatcher(Dispatcher{Session: "session:test", Block: "block:dispatch", Name: "dusty-delta"}); err != nil {
		t.Fatal(err)
	}
	m := todoModel(t, st)
	if view := m.View(); !strings.Contains(view, "c hand off") {
		t.Errorf("the hints should offer the hand-off:\n%s", view)
	}
	m.Update(key("c"))
	if m.todoInput == nil || m.todoInput.handTo == nil || m.todoInput.handTo.Block != "block:dispatch" {
		t.Fatal("m on the user's own task should open a box for the dispatcher")
	}
	if !strings.Contains(m.View(), "Note for dusty-delta") {
		t.Errorf("the box should name the dispatcher:\n%s", m.View())
	}
	// The note is optional, so enter on an empty box still sends it.
	if _, cmd := m.Update(key("enter")); cmd == nil {
		t.Fatal("enter should hand the task off")
	}
	if !strings.Contains(m.View(), "Sent to dusty-delta") {
		t.Errorf("it should say where the task went:\n%s", m.View())
	}
	if !slices.ContainsFunc(m.answerBlocks, func(e Entry) bool { return e.NotifyBlock == "block:dispatch" }) {
		t.Error("the dispatcher's pane should be flushed like other notified panes")
	}
	if ts, _ := st.Todos(); ts[0].Done {
		t.Error("handing off shouldn't finish the task")
	}

	want := "[sidekick] todo t-1 \"Fix the flaky login test\", handed to you from the user's own list to dispatch." +
		" Detail: \"fails on CI about 1 in 5\" Note from the user: \"before Friday\"" +
		" Mark it done with `rex-sidekick todo done t-1` when the work is finished."
	if got := FormatTodoHandoff(m.todos[0], "before Friday"); got != want {
		t.Errorf("message\n%s\nwant\n%s", got, want)
	}
}

func TestRemoveWholeList(t *testing.T) {
	st := testStore(t)
	err := st.AddTodos([]Todo{
		{ID: "a", List: "api-agent", Text: "Rotate the staging key"},
		{ID: "b", List: "api-agent", Text: "Redeploy staging", Done: true},
		{ID: "c", List: "web-agent", Text: "Ship the banner", Done: true, DoneAt: time.Now()},
		{ID: "d", Mine: true, Text: "call the dentist", ShowAt: time.Now().Add(time.Hour)},
		{ID: "e", Mine: true, Text: "water the plants"},
	})
	if err != nil {
		t.Fatal(err)
	}
	m := todoModel(t, st)
	if _, ok := m.selectedTodo(); !ok {
		t.Fatal("the cursor should start on the first task, not a heading")
	}

	// A list with only finished tasks asks too, then goes on the second x.
	selectList(t, m, "web-agent")
	m.Update(key("x"))
	if ts, _ := st.Todos(); len(ts) != 5 || !strings.Contains(m.View(), "Remove web-agent and its 1 task? Press x again") {
		t.Fatalf("the first x on a finished list should only ask:\n%s", m.View())
	}
	m.Update(key("x"))
	if ts, _ := st.Todos(); len(ts) != 4 || slices.ContainsFunc(ts, func(x Todo) bool { return x.ID == "c" }) {
		t.Errorf("x x on web-agent's heading should remove its list, got %+v", ts)
	}

	// A list with open tasks asks first, and any other key cancels.
	selectList(t, m, "api-agent")
	m.Update(key("x"))
	if ts, _ := st.Todos(); len(ts) != 4 || !strings.Contains(m.View(), "Remove api-agent and its 2 tasks? Press x again") {
		t.Fatalf("the first x on a list with open tasks should only ask:\n%s", m.View())
	}
	m.Update(key("esc"))
	if strings.Contains(m.View(), "Press x again") {
		t.Errorf("cancelling should clear the remove prompt:\n%s", m.View())
	}
	m.Update(key("x"))
	m.Update(key("j"))
	m.Update(key("k"))
	m.Update(key("x"))
	if ts, _ := st.Todos(); len(ts) != 4 {
		t.Fatalf("moving should cancel the pending remove, got %+v", ts)
	}
	m.Update(key("x"))
	if ts, _ := st.Todos(); len(ts) != 2 || ts[0].ID != "d" {
		t.Fatalf("the second x should remove api-agent's list, got %+v", ts)
	}

	// The user's list takes its hidden tasks with it, and goes too.
	selectList(t, m, "My list")
	m.Update(key("x"))
	if !strings.Contains(m.View(), "Remove My list and its 2 tasks?") {
		t.Errorf("the prompt should count my list's hidden task:\n%s", m.View())
	}
	m.Update(key("x"))
	if ts, _ := st.Todos(); len(ts) != 0 {
		t.Errorf("x x on my list should remove every task, hidden ones too, got %+v", ts)
	}
	if strings.Contains(m.View(), "▾ My list") {
		t.Errorf("my list should be gone:\n%s", m.View())
	}
}

func TestRemoveMyList(t *testing.T) {
	st := testStore(t)
	m := todoModel(t, st)
	selectList(t, m, "My list")
	if !strings.Contains(m.View(), "x remove list") {
		t.Errorf("the footer should offer x on My list:\n%s", m.View())
	}
	m.Update(key("x"))
	if gone, _ := st.MyListRemoved(); gone || !strings.Contains(m.View(), "Remove My list? It has no tasks. Press x again") {
		t.Fatalf("the first x on an empty My list should only ask:\n%s", m.View())
	}
	m.Update(key("x"))
	view := m.View()
	if strings.Contains(view, "▾ My list") || strings.Contains(view, "+ New task") || !strings.Contains(view, "+ New group") {
		t.Fatalf("an empty My list should go on x x, leaving only + New group:\n%s", view)
	}

	// It stays gone after a restart.
	m = todoModel(t, st)
	if view := m.View(); strings.Contains(view, "My list") || !strings.Contains(view, "+ New group") {
		t.Fatalf("My list should stay gone after a restart:\n%s", view)
	}

	// Its names are free to use.
	m.cursors[tabTodo] = slices.IndexFunc(m.todoItems, func(i todoItem) bool { return i.newGroupRow })
	m.Update(key("enter"))
	typeLine(m, "My list")
	if gs, _ := st.MyGroups(); !slices.Equal(gs, []string{"My list"}) {
		t.Fatalf("a new list should be able to take the name My list, got %v", gs)
	}

	// A task with no list still shows, under My list.
	if err := st.AddTodos([]Todo{{ID: "c", Mine: true, Text: "call the dentist"}}); err != nil {
		t.Fatal(err)
	}
	m.reloadTodos()
	if view := m.View(); !strings.Contains(view, "call the dentist") {
		t.Errorf("a task left on My list should still show:\n%s", view)
	}
}

func TestInProgress(t *testing.T) {
	st := testStore(t)
	err := st.AddTodos([]Todo{
		{ID: "a", List: "api-agent", Text: "Rotate the staging key", Started: true},
		{ID: "b", Mine: true, Text: "call the dentist"},
	})
	if err != nil {
		t.Fatal(err)
	}
	m := todoModel(t, st)
	if view := m.View(); !strings.Contains(view, "◐ Rotate the staging key") {
		t.Errorf("an agent's started task should show ◐:\n%s", view)
	}
	selectTask(t, m, "a")
	m.Update(key("p"))
	if ts, _ := st.Todos(); !ts[0].Started {
		t.Error("p shouldn't change an agent's task")
	}

	selectTask(t, m, "b")
	if !strings.Contains(m.View(), "p in progress") {
		t.Errorf("my open task should offer p:\n%s", m.View())
	}
	m.Update(key("p"))
	if ts, _ := st.Todos(); !ts[1].Started || !strings.Contains(m.View(), "◐ call the dentist") {
		t.Fatalf("p should mark my task in progress:\n%s", m.View())
	}
	m.Update(key("space"))
	if ts, _ := st.Todos(); !ts[1].Done || ts[1].Started {
		t.Errorf("finishing a task should clear in progress, got %+v", ts[1])
	}
	if lines := todoLines([]Todo{{ID: "c", Text: "x", Started: true}}); !strings.Contains(lines[1], "[~]") {
		t.Errorf("todo ls should mark a started task [~], got %q", lines)
	}
}

func TestListOrderAndFinishedListsExpire(t *testing.T) {
	st := testStore(t)
	now := time.Now()
	err := st.AddTodos([]Todo{
		{ID: "a", List: "done-agent", Text: "Ship the banner", Done: true, DoneAt: now.Add(-5 * time.Minute)},
		{ID: "b", List: "old-agent", Text: "Rotate the key", Done: true, DoneAt: now.Add(-20 * time.Minute)},
		{ID: "c", List: "busy-agent", Text: "Backfill", Done: true, DoneAt: now.Add(-time.Hour)},
		{ID: "d", List: "busy-agent", Text: "Drop the old table", After: "c"},
		{ID: "e", Mine: true, Text: "call the dentist"},
	})
	if err != nil {
		t.Fatal(err)
	}
	m := todoModel(t, st)

	// A list that finished 15 minutes ago is gone, from the store too.
	ts, _ := st.Todos()
	if slices.ContainsFunc(ts, func(x Todo) bool { return x.ID == "b" }) {
		t.Errorf("old-agent's list finished 20 minutes ago and should be removed, got %+v", ts)
	}
	// A list with an open task stays, however old its finished tasks are.
	if !slices.ContainsFunc(ts, func(x Todo) bool { return x.ID == "c" }) {
		t.Errorf("busy-agent still has an open task, so its list should stay")
	}

	var order []string
	for _, it := range m.todoItems {
		if it.heading {
			order = append(order, it.group)
		}
	}
	if want := []string{"My list", "busy-agent", "done-agent"}; !slices.Equal(order, want) {
		t.Errorf("lists in order %q, want my list, then open agent lists, then finished ones %q", order, want)
	}

	// done-agent's list goes when its 15 minutes are up.
	if m.todoWake.IsZero() || m.todoWake.After(now.Add(11*time.Minute)) {
		t.Errorf("the tab should wake when done-agent's list expires, got %v", m.todoWake)
	}
	if err := st.UpdateTodo("a", func(x *Todo, _ []Todo) error { x.DoneAt = now.Add(-16 * time.Minute); return nil }); err != nil {
		t.Fatal(err)
	}
	m.reloadTodos()
	if strings.Contains(m.View(), "done-agent") {
		t.Errorf("done-agent's list should be gone:\n%s", m.View())
	}

	// My own finished tasks go the same way, but my list's heading stays.
	selectTask(t, m, "e")
	m.Update(key("space"))
	if err := st.UpdateTodo("e", func(x *Todo, _ []Todo) error { x.DoneAt = now.Add(-16 * time.Minute); return nil }); err != nil {
		t.Fatal(err)
	}
	m.reloadTodos()
	if view := m.View(); strings.Contains(view, "dentist") || !strings.Contains(view, "My list (0)") {
		t.Errorf("my finished task should go and my heading stay:\n%s", view)
	}
}

func TestAddReminderLandsInMyList(t *testing.T) {
	st := testStore(t)
	if err := st.AddTodos([]Todo{{ID: "a", List: "api-agent", Text: "Rotate the key"}}); err != nil {
		t.Fatal(err)
	}
	m := todoModel(t, st)
	m.cursors[tabTodo] = slices.IndexFunc(m.todoItems, func(i todoItem) bool { return i.addRow })
	m.Update(key("enter"))
	view := m.View()
	if strings.Index(view, "New task") > strings.Index(view, "api-agent") {
		t.Errorf("the add box should sit under my list, above the agents':\n%s", view)
	}
	for _, r := range "water the plants" {
		m.Update(key(string(r)))
	}
	m.Update(key("enter"))
	if it, ok := m.selectedTodo(); !ok || it.Text != "water the plants" {
		t.Errorf("the cursor should land on the new reminder, got %+v", it)
	}
}

func TestCollapseList(t *testing.T) {
	st := testStore(t)
	err := st.AddTodos([]Todo{
		{ID: "a", List: "api-agent", Text: "Rotate the key"},
		{ID: "b", List: "api-agent", Text: "Redeploy", Done: true, DoneAt: time.Now()},
		{ID: "c", List: "web-agent", Text: "Ship the banner", Done: true, DoneAt: time.Now()},
	})
	if err != nil {
		t.Fatal(err)
	}
	m := todoModel(t, st)
	view := m.View()
	if !strings.Contains(view, "┌─ Agents") || !strings.Contains(view, "┌─ Completed") {
		t.Errorf("the agent and completed sections should have boxes:\n%s", view)
	}
	selectList(t, m, "api-agent")
	if strings.Contains(m.View(), "enter collapse") {
		t.Errorf("the footer shouldn't spell out enter:\n%s", m.View())
	}
	m.Update(key("enter"))
	view = m.View()
	if strings.Contains(view, "Rotate the key") || !strings.Contains(view, "▸ api-agent (1)") {
		t.Errorf("enter should fold the list and keep its open count:\n%s", view)
	}
	if h, ok := m.selectedHeading(); !ok || h.group != "api-agent" {
		t.Errorf("the cursor should stay on the folded heading")
	}
	m.Update(key("space"))
	if !strings.Contains(m.View(), "Rotate the key") {
		t.Errorf("space should open it again:\n%s", m.View())
	}
	if ts, _ := st.Todos(); ts[0].Done {
		t.Error("folding shouldn't touch the tasks")
	}
}

func TestForUserListsJoinMySection(t *testing.T) {
	st := testStore(t)
	err := st.AddTodos([]Todo{
		{ID: "a", List: "api-agent", Text: "Rotate the key"},
		{ID: "b", List: "Reminders", Text: "Undo the Dataflow change", ForUser: true},
		{ID: "c", Mine: true, Text: "call the dentist"},
	})
	if err != nil {
		t.Fatal(err)
	}
	m := todoModel(t, st)
	view := m.View()
	rule := strings.Index(view, "┌─ Agents")
	if r := strings.Index(view, "Reminders"); r < 0 || r > rule || strings.Index(view, "My list") > r {
		t.Errorf("Reminders should sit under my list, above the Agents box:\n%s", view)
	}
	if a := strings.Index(view, "api-agent"); a < rule {
		t.Errorf("api-agent should stay under the Agents box:\n%s", view)
	}
}

func TestScheduledOnlyListStartsFolded(t *testing.T) {
	st := testStore(t)
	err := st.AddTodos([]Todo{
		{ID: "r", List: "Reminders", Text: "Undo the Dataflow change", ForUser: true, ShowAt: time.Now().Add(72 * time.Hour)},
		{ID: "a", List: "api-agent", Text: "Rotate the key"},
	})
	if err != nil {
		t.Fatal(err)
	}
	m := todoModel(t, st)
	view := m.View()
	if strings.Contains(view, "Undo the Dataflow change") || !strings.Contains(view, "▸ Reminders (0) · 1 later") {
		t.Fatalf("a list of only scheduled tasks should start folded:\n%s", view)
	}
	selectList(t, m, "Reminders")
	m.Update(key("enter"))
	view = m.View()
	if !strings.Contains(view, "Undo the Dataflow change") || !strings.Contains(view, "shows ") {
		t.Errorf("opening it should show the scheduled task and when it's due:\n%s", view)
	}
	m.Update(key("enter"))
	if strings.Contains(m.View(), "Undo the Dataflow change") {
		t.Errorf("enter should fold it again:\n%s", m.View())
	}
}

func TestNewTaskButtonAndMyListHeading(t *testing.T) {
	st := testStore(t)
	if err := st.AddTodos([]Todo{{ID: "c", Mine: true, Text: "call the dentist"}}); err != nil {
		t.Fatal(err)
	}
	m := todoModel(t, st)
	view := m.View()
	if !strings.Contains(view, "┌─ Yours") || !strings.Contains(view, "▾ My list (1)") || !strings.Contains(view, "+ New task") {
		t.Fatalf("my list should be a heading named My list, in the Yours box, with a New task button:\n%s", view)
	}
	selectList(t, m, "My list")
	m.Update(key("enter"))
	if view := m.View(); m.todoInput != nil || strings.Contains(view, "call the dentist") || strings.Contains(view, "+ New task") {
		t.Fatalf("enter on my list's heading should fold it, New task and all:\n%s", view)
	}
	m.Update(key("enter"))
	m.cursors[tabTodo] = slices.IndexFunc(m.todoItems, func(i todoItem) bool { return i.addRow })
	m.Update(key("enter"))
	if m.todoInput == nil || !m.todoInput.adding {
		t.Fatal("enter on New task should open the new task box")
	}
	for _, r := range "water the plants" {
		m.Update(key(string(r)))
	}
	m.Update(key("enter"))
	if ts, _ := st.Todos(); len(ts) != 2 || ts[1].Group != "" {
		t.Fatalf("the new task should be saved on my list, got %+v", ts)
	}
}

// typeLine types text into the open box and presses enter.
func typeLine(m *model, text string) {
	for _, r := range text {
		m.Update(key(string(r)))
	}
	m.Update(key("enter"))
}

func TestNewGroup(t *testing.T) {
	st := testStore(t)
	err := st.AddTodos([]Todo{
		{ID: "c", Mine: true, Text: "call the dentist"},
		{ID: "a", List: "api-agent", Text: "Rotate the key"},
	})
	if err != nil {
		t.Fatal(err)
	}
	m := todoModel(t, st)
	if m.todoItems[0].newGroupRow != true || !strings.Contains(m.View(), "+ New group") {
		t.Fatalf("New group should be the first row of the tab:\n%s", m.View())
	}
	m.cursors[tabTodo] = 0
	m.Update(key("enter"))
	if m.todoInput == nil || !m.todoInput.newGroup {
		t.Fatal("enter on New group should open a box for its name")
	}
	typeLine(m, "Groceries")
	if gs, _ := st.MyGroups(); !slices.Equal(gs, []string{"Groceries"}) {
		t.Fatalf("the new list should be saved, got %v", gs)
	}
	if h, ok := m.selectedHeading(); !ok || h.Group != "Groceries" {
		t.Errorf("the cursor should land on the new list's heading, got %+v", h)
	}

	// Each list has its own New task button.
	i := slices.IndexFunc(m.todoItems, func(i todoItem) bool { return i.addRow && i.Group == "Groceries" })
	if i < 0 {
		t.Fatal("the new list should have a New task button")
	}
	m.cursors[tabTodo] = i
	m.Update(key("enter"))
	typeLine(m, "buy milk")
	ts, _ := st.Todos()
	if len(ts) != 3 || ts[0].Group != "" || ts[2].Text != "buy milk" || ts[2].Group != "Groceries" || !ts[2].Mine {
		t.Fatalf("the task should go on Groceries and leave my list alone, got %+v", ts)
	}
	view := m.View()
	newGroup, mine, dentist, groceries, milk, agents := strings.Index(view, "+ New group"), strings.Index(view, "My list (1)"),
		strings.Index(view, "call the dentist"), strings.Index(view, "Groceries (1)"), strings.Index(view, "buy milk"), strings.Index(view, "┌─ Agents")
	if !(newGroup < mine && mine < dentist && dentist < groceries && groceries < milk && milk < agents) {
		t.Errorf("want New group, my list, then Groceries with its task, above the agents:\n%s", view)
	}

	// A taken name keeps the box open to fix it.
	m.cursors[tabTodo] = 0
	m.Update(key("enter"))
	typeLine(m, "Groceries")
	if m.todoInput == nil || !strings.Contains(m.View(), "already a list") {
		t.Errorf("a taken name should say so and keep the box open:\n%s", m.View())
	}
	m.Update(key("esc"))
	m.cursors[tabTodo] = 0
	m.Update(key("enter"))
	typeLine(m, "Later")
	if gs, _ := st.MyGroups(); len(gs) != 1 {
		t.Errorf("Later is taken too, got %v", gs)
	}
	m.Update(key("esc"))

	// The list and its task are still there next time.
	m = todoModel(t, st)
	if !strings.Contains(m.View(), "Groceries (1)") {
		t.Errorf("the list should survive a restart:\n%s", m.View())
	}
}

func TestEmptyGroupShows(t *testing.T) {
	st := testStore(t)
	if err := st.AddMyGroup("Errands"); err != nil {
		t.Fatal(err)
	}
	m := todoModel(t, st)
	if view := m.View(); !strings.Contains(view, "▾ Errands (0)") || strings.Count(view, "+ New task") != 2 {
		t.Errorf("an empty list should show with somewhere to add to it:\n%s", view)
	}
}

func TestRenameAndRemoveGroup(t *testing.T) {
	st := testStore(t)
	for _, g := range []string{"Errands", "Empty"} {
		if err := st.AddMyGroup(g); err != nil {
			t.Fatal(err)
		}
	}
	err := st.AddTodos([]Todo{
		{ID: "c", Mine: true, Text: "call the dentist"},
		{ID: "e", Mine: true, Group: "Errands", Text: "post office"},
		{ID: "s", Mine: true, Group: "Errands", Text: "car wash", ShowAt: time.Now().Add(time.Hour)},
	})
	if err != nil {
		t.Fatal(err)
	}
	m := todoModel(t, st)

	selectList(t, m, "My list")
	m.Update(key("e"))
	if m.todoInput != nil {
		t.Error("my list can't be renamed")
	}

	selectList(t, m, "Errands")
	if !strings.Contains(m.View(), "e rename") {
		t.Errorf("the footer should offer e on a list I made:\n%s", m.View())
	}
	m.Update(key("e"))
	if m.todoInput == nil || m.todoInput.ta.Value() != "Errands" {
		t.Fatal("e should open a box with the list's name")
	}
	m.todoInput.ta.SetValue("")
	typeLine(m, "Chores")
	ts, _ := st.Todos()
	if gs, _ := st.MyGroups(); !slices.Equal(gs, []string{"Chores", "Empty"}) || ts[1].Group != "Chores" || ts[2].Group != "Chores" {
		t.Fatalf("rename should move the list and its tasks, snoozed ones too, got %v %+v", gs, ts)
	}

	// x x removes the list and its tasks, hidden ones too, and leaves the others.
	selectList(t, m, "Chores")
	m.Update(key("x"))
	if ts, _ := st.Todos(); len(ts) != 3 {
		t.Fatalf("the first x should only ask, got %+v", ts)
	}
	m.Update(key("x"))
	if ts, _ := st.Todos(); len(ts) != 1 || ts[0].ID != "c" {
		t.Fatalf("x x should remove Chores's tasks and keep mine, got %+v", ts)
	}
	if gs, _ := st.MyGroups(); !slices.Equal(gs, []string{"Empty"}) || strings.Contains(m.View(), "▾ Chores") {
		t.Fatalf("Chores should be gone, got %v:\n%s", gs, m.View())
	}

	// An empty list asks too, and a cancel keeps it.
	selectList(t, m, "Empty")
	m.Update(key("x"))
	if gs, _ := st.MyGroups(); len(gs) != 1 || !strings.Contains(m.View(), "Remove Empty? It has no tasks. Press x again") {
		t.Fatalf("the first x on an empty list should only ask, got %v:\n%s", gs, m.View())
	}
	m.Update(key("esc"))
	m.Update(key("x"))
	if gs, _ := st.MyGroups(); len(gs) != 1 {
		t.Fatalf("esc should cancel the remove, got %v", gs)
	}
	m.Update(key("x"))
	if gs, _ := st.MyGroups(); len(gs) != 0 {
		t.Errorf("x x on an empty list should remove it, got %v", gs)
	}
}

func TestRemoveGitListNamesItsFile(t *testing.T) {
	gitEnv(t)
	st := testStore(t)
	if err := st.AddMyGroup("Work"); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(t.TempDir(), "todos")
	if err := st.SetTodoBackend("Work", todoBackend{Git: repo}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddTodos([]Todo{{ID: "w", Mine: true, Group: "Work", Text: "file the report"}}); err != nil {
		t.Fatal(err)
	}
	backs, _ := st.TodoBackends()
	repo = backs["Work"].Git
	m := todoModel(t, st)
	dir := filepath.Join(repo, "work")

	selectList(t, m, "Work")
	m.Update(key("x"))
	want := "Remove Work and its 1 task? This deletes work/ from " + repo + "."
	if !strings.HasPrefix(m.errMsg, want) || !strings.Contains(m.View(), "Press x again") {
		t.Errorf("the prompt should name the list's folder, want %q, got %q", want, m.errMsg)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("the first x should leave the work folder alone: %v", err)
	}
	m.Update(key("x"))
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("x x should delete the work folder, got %v", err)
	}
	if gs, _ := st.MyGroups(); len(gs) != 0 {
		t.Errorf("Work should be gone, got %v", gs)
	}
}

func TestGroupTaskSnoozesToLaterAndBack(t *testing.T) {
	st := testStore(t)
	if err := st.AddMyGroup("Errands"); err != nil {
		t.Fatal(err)
	}
	err := st.AddTodos([]Todo{
		{ID: "e", Mine: true, Group: "Errands", Text: "post office", ShowAt: time.Now().Add(time.Hour)},
		{ID: "f", Mine: true, Group: "Errands", Text: "car wash"},
	})
	if err != nil {
		t.Fatal(err)
	}
	m := todoModel(t, st)
	if view := m.View(); !strings.Contains(view, "Errands (1)") || !strings.Contains(view, "Later (0) · 1 later") {
		t.Fatalf("the snoozed task should wait in Later:\n%s", view)
	}
	if err := st.UpdateTodo("e", func(x *Todo, _ []Todo) error { x.ShowAt = time.Time{}; return nil }); err != nil {
		t.Fatal(err)
	}
	m.reloadTodos()
	if view := m.View(); strings.Contains(view, "Later") || !strings.Contains(view, "Errands (2)") {
		t.Errorf("when its time comes it should be back on Errands:\n%s", view)
	}
}

func TestFinishedGroupClearsOnItsOwn(t *testing.T) {
	old := time.Now().Add(-finishedListTTL - time.Minute)
	ids, _ := expiredTodos([]Todo{
		{ID: "c", Mine: true, Text: "call the dentist"},
		{ID: "e", Mine: true, Group: "Errands", Text: "post office", Done: true, DoneAt: old},
	}, time.Now())
	if !ids["e"] || ids["c"] {
		t.Errorf("a finished list of mine should clear while my other lists stay, got %v", ids)
	}
}

func TestGroupsInTodoLinesAndHandoff(t *testing.T) {
	ts := []Todo{
		{ID: "c", Mine: true, Text: "call the dentist"},
		{ID: "e", Mine: true, Group: "Errands", Text: "post office"},
	}
	out := strings.Join(todoLines(ts), "\n")
	if !strings.Contains(out, "list: Errands\n  e [ ] post office") || strings.Index(out, "dentist") > strings.Index(out, "Errands") {
		t.Errorf("todo ls should show each of my lists, My list first:\n%s", out)
	}
	if msg := FormatTodoHandoff(ts[1], ""); !strings.Contains(msg, `from the user's list "Errands"`) {
		t.Errorf("the hand-off should name the list, got %s", msg)
	}
}

func TestSnoozedTaskWaitsInLater(t *testing.T) {
	st := testStore(t)
	err := st.AddTodos([]Todo{
		{ID: "a", Mine: true, Text: "call the dentist", ShowAt: time.Now().Add(time.Hour)},
		{ID: "c", Mine: true, Text: "water the plants"},
	})
	if err != nil {
		t.Fatal(err)
	}
	m := todoModel(t, st)
	selectList(t, m, "Later")
	m.Update(key("enter"))
	view := m.View()
	mine, later, dentist := strings.Index(view, "My list"), strings.Index(view, "▾ Later"), strings.Index(view, "call the dentist")
	if mine < 0 || later < mine || dentist < later || !strings.Contains(view, "shows ") {
		t.Fatalf("the snoozed task should wait in Later with its time:\n%s", view)
	}
	if err := st.UpdateTodo("a", func(x *Todo, _ []Todo) error { x.ShowAt = time.Time{}; return nil }); err != nil {
		t.Fatal(err)
	}
	m.reloadTodos()
	view = m.View()
	if strings.Contains(view, "Later") || !strings.Contains(view, "My list (2)") {
		t.Errorf("when its time comes it should be back on my list:\n%s", view)
	}
}

func TestNewTaskBoxLinesUpWithTasks(t *testing.T) {
	st := testStore(t)
	if err := st.AddTodos([]Todo{{ID: "c", Mine: true, Text: "call the dentist"}}); err != nil {
		t.Fatal(err)
	}
	m := todoModel(t, st)
	m.cursors[tabTodo] = slices.IndexFunc(m.todoItems, func(i todoItem) bool { return i.addRow })
	m.Update(key("enter"))
	for _, r := range "water the plants" {
		m.Update(key(string(r)))
	}
	col := func(text string) int {
		for _, l := range strings.Split(m.View(), "\n") {
			if i := strings.Index(ansi.Strip(l), text); i >= 0 {
				return ansi.StringWidth(ansi.Strip(l)[:i])
			}
		}
		t.Fatalf("no %q in the view:\n%s", text, m.View())
		return 0
	}
	if task, typed := col("call the dentist"), col("water the plants"); typed != task {
		t.Errorf("the new task starts at column %d, its siblings at %d:\n%s", typed, task, m.View())
	}
}

// TestTodoViewFitsPane checks every row of the TODO tab. My list's title and
// New task have only an enter hint, which the footer leaves out, and an empty
// footer once pushed the tab bar off the top.
func TestTodoViewFitsPane(t *testing.T) {
	st := testStore(t)
	if err := st.AddTodos([]Todo{
		{ID: "a", Mine: true, Text: "call the dentist"},
		{ID: "b", List: "agent", Text: "ship it"},
	}); err != nil {
		t.Fatal(err)
	}
	m := todoModel(t, st)
	for i := range m.todoItems {
		m.cursors[tabTodo] = i
		view := m.View()
		if lines := strings.Count(view, "\n") + 1; lines != m.height {
			t.Errorf("row %d: view is %d lines in a %d-line pane:\n%s", i, lines, m.height, view)
		}
		if !strings.HasPrefix(view, " Inbox (") {
			t.Errorf("row %d: view should start with the tab bar:\n%s", i, view)
		}
	}
}

func TestEditOwnTaskInEditor(t *testing.T) {
	st := testStore(t)
	if err := st.AddTodos([]Todo{{ID: "t-1", Mine: true, Text: "Move the blog", CreatedAt: time.Now()}}); err != nil {
		t.Fatal(err)
	}
	var split []string
	rexSplit = func(args ...string) ([]byte, error) {
		split = args
		return []byte(`{"block_ids":["block:edit"]}`), nil
	}
	t.Cleanup(func() { rexSplit = func(args ...string) ([]byte, error) { return nil, nil } })
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "nvim -p")

	m := todoModel(t, st)
	selectTask(t, m, "t-1")
	if view := m.View(); !strings.Contains(view, "i edit") {
		t.Errorf("the hints should offer editing:\n%s", view)
	}
	m.Update(key("i"))
	if m.todoDetail != nil {
		t.Error("i on the user's own task shouldn't open the details screen")
	}
	if !slices.Contains(split, "--split=below") || !slices.Contains(split, "edit-pane") || !slices.Contains(split, "t-1") {
		t.Fatalf("i should split a pane below the sidekick to edit the task, got %q", split)
	}
	if view := m.View(); !strings.Contains(view, "Editing it in nvim") {
		t.Errorf("the tab should say which editor opened:\n%s", view)
	}

	// The pane saves the task, and the tab picks it up.
	if err := st.UpdateTodo("t-1", func(x *Todo, _ []Todo) error { x.Detail = "## Plan\n- swap DNS"; return nil }); err != nil {
		t.Fatal(err)
	}
	m.Update(tickMsg(time.Now()))
	if view := ansi.Strip(m.View()); !strings.Contains(view, "Move the blog (i)") {
		t.Errorf("the list should mark the task's new details:\n%s", view)
	}
}

func TestTodoFile(t *testing.T) {
	task := Todo{Text: "Move the blog", Detail: "## Plan\n\n- swap DNS"}
	if got := todoFile(task); got != "# Move the blog\n\n## Plan\n\n- swap DNS\n" {
		t.Errorf("todoFile = %q", got)
	}
	if got := todoFile(Todo{Text: "Move the blog"}); got != "# Move the blog\n" {
		t.Errorf("a task with no details should be one line, got %q", got)
	}
	for _, c := range []struct{ in, text, detail string }{
		{todoFile(task), task.Text, task.Detail},
		{todoFile(Todo{Text: "#42 crashes"}), "#42 crashes", ""},
		{"Move the blog", "Move the blog", ""},
		{"#   Move the blog  \n\n# Plan", "Move the blog", "# Plan"},
		{"#\tMove the blog", "Move the blog", ""},
		{"#42 crashes", "#42 crashes", ""},
		{"#\nnotes", "", "notes"},
		{"  Move it  \r\nnotes\r\n\n\n", "Move it", "notes"},
		{"\n\nJust notes\n", "", "Just notes"},
		{"Move it\n\n\n  indented\nmore  \n\n", "Move it", "  indented\nmore"},
	} {
		text, detail := parseTodoFile(c.in)
		if text != c.text || detail != c.detail {
			t.Errorf("parseTodoFile(%q) = %q, %q; want %q, %q", c.in, text, detail, c.text, c.detail)
		}
	}
}

func TestEditPane(t *testing.T) {
	st := testStore(t)
	err := st.AddTodos([]Todo{
		{ID: "t-1", Mine: true, Text: "Move the blog", Detail: "old notes", CreatedAt: time.Now()},
		{ID: "t-2", Mine: true, Text: "Renew the domain", CreatedAt: time.Now()},
	})
	if err != nil {
		t.Fatal(err)
	}
	// The editor is a script that replaces the file with what's in $NEW.
	script := filepath.Join(t.TempDir(), "ed")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s' \"$NEW\" > \"$1\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHELL", "/bin/sh")
	t.Setenv("VISUAL", script)

	t.Setenv("NEW", "# Move the blog to Hugo\n\n## Plan\n- swap DNS\n")
	if err := cmdEditPane([]string{"--session", st.Session, "t-1"}); err != nil {
		t.Fatal(err)
	}
	ts, _ := st.Todos()
	if ts[0].Text != "Move the blog to Hugo" || ts[0].Detail != "## Plan\n- swap DNS" {
		t.Errorf("the edit should be saved, got %q / %q", ts[0].Text, ts[0].Detail)
	}

	// Clearing the first line keeps the text, and clearing the rest drops
	// the details.
	t.Setenv("NEW", "\n")
	if err := cmdEditPane([]string{"--session", st.Session, "t-1"}); err != nil {
		t.Fatal(err)
	}
	ts, _ = st.Todos()
	if ts[0].Text != "Move the blog to Hugo" || ts[0].Detail != "" {
		t.Errorf("an empty file should keep the text and clear the details, got %q / %q", ts[0].Text, ts[0].Detail)
	}

	// A failed exit, like :cq in vim, throws the edit away.
	t.Setenv("VISUAL", "false")
	if err := cmdEditPane([]string{"--session", st.Session, "t-2"}); err != nil {
		t.Fatal(err)
	}
	if ts, _ = st.Todos(); ts[1].Text != "Renew the domain" {
		t.Errorf("a failed editor shouldn't change the task, got %q", ts[1].Text)
	}
}

func TestAgentTaskDetailsAreReadOnly(t *testing.T) {
	st := testStore(t)
	long := strings.Repeat("A line of the plan.\n", 60)
	err := st.AddTodos([]Todo{
		{ID: "t-1", List: "Fix login", From: "lucky-lark", Text: "Rotate the key", Detail: long, CreatedAt: time.Now()},
		{ID: "t-2", List: "Fix login", From: "lucky-lark", Text: "Deploy", CreatedAt: time.Now()},
	})
	if err != nil {
		t.Fatal(err)
	}
	m := todoModel(t, st)
	selectTask(t, m, "t-2")
	m.Update(key("i"))
	if m.todoDetail != nil || !strings.Contains(m.View(), "no details") {
		t.Error("i on an agent's task without details should just say so")
	}
	selectTask(t, m, "t-1")
	m.Update(key("i"))
	if m.todoDetail == nil {
		t.Fatal("i should open the agent's details to read")
	}
	view := m.View()
	if !strings.Contains(view, "lucky-lark") || !strings.Contains(view, "more lines (j)") || strings.Contains(view, "e edit") {
		t.Errorf("the details should name the agent, scroll, and not offer editing:\n%s", view)
	}
	if lines := strings.Count(view, "\n") + 1; lines != m.height {
		t.Errorf("the view should fill the pane exactly, got %d lines for %d", lines, m.height)
	}
	for _, w := range []int{24, 40} {
		m.width = w
		for _, l := range strings.Split(m.View(), "\n") {
			if lipgloss.Width(l) > w {
				t.Errorf("width %d: line is %d wide: %q", w, lipgloss.Width(l), l)
			}
		}
	}
	m.width = 63
	m.Update(key("j"))
	if m.todoDetail.top != 1 {
		t.Errorf("j should scroll the details, top is %d", m.todoDetail.top)
	}
	m.Update(key("q"))
	if m.todoDetail != nil {
		t.Error("q should go back to the list")
	}
}

func TestDetailsInHandoffAndCLI(t *testing.T) {
	task := Todo{ID: "t-1", Mine: true, Text: "Move the blog", Detail: "## Plan\n- quote the host\n- swap  DNS\n"}
	msg := FormatTodoHandoff(task, "")
	if strings.Contains(msg, "\n") || !strings.Contains(msg, `Detail: "## Plan\n- quote the host\n- swap  DNS" Mark it done`) {
		t.Errorf("the hand-off should carry the plan on one line, line breaks quoted:\n%s", msg)
	}
	if out := strings.Join(todoLines([]Todo{task}), "\n"); !strings.Contains(out, "Move the blog (details)") || strings.Contains(out, "quote the host") {
		t.Errorf("todo ls should mark the details without printing them:\n%s", out)
	}
	if got, want := todoShow(task), "t-1 [ ] Move the blog (details)\n\n"+strings.TrimSpace(task.Detail)+"\n"; got != want {
		t.Errorf("todo show printed\n%q\nwant\n%q", got, want)
	}
}
