package main

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// selectRow puts the TODO cursor on the first row that match picks.
func selectRow(t *testing.T, m *model, what string, match func(todoItem) bool) {
	t.Helper()
	i := slices.IndexFunc(m.todoItems, match)
	if i < 0 {
		t.Fatalf("no %s on the tab", what)
	}
	m.cursors[tabTodo] = i
}

func selectSub(t *testing.T, m *model, group, sub string) {
	t.Helper()
	selectRow(t, m, "sub-group "+sub, func(it todoItem) bool { return it.heading && it.Group == group && it.Sub == sub })
}

func selectNewSub(t *testing.T, m *model, group, sub string) {
	t.Helper()
	selectRow(t, m, "new sub-group row", func(it todoItem) bool { return it.newSubRow && it.Group == group && it.Sub == sub })
}

func selectAddRow(t *testing.T, m *model, group, sub string) {
	t.Helper()
	selectRow(t, m, "new task row", func(it todoItem) bool { return it.addRow && it.Group == group && it.Sub == sub })
}

func TestSubGroupsOnTheTab(t *testing.T) {
	st := testStore(t)
	if err := st.AddMyGroup("Home"); err != nil {
		t.Fatal(err)
	}
	if err := st.AddTodos([]Todo{{ID: "a", Mine: true, Group: "Home", Text: "Paint the hallway"}}); err != nil {
		t.Fatal(err)
	}
	m := todoModel(t, st)

	// My list doesn't take sub-groups. A list you made does.
	if slices.ContainsFunc(m.todoItems, func(it todoItem) bool { return it.newSubRow && it.Group == "" }) {
		t.Error("My list shouldn't offer a sub-group")
	}
	selectNewSub(t, m, "Home", "")
	if v := m.View(); !strings.Contains(v, "+ New sub-group") {
		t.Fatalf("a list should end with a new sub-group row:\n%s", v)
	}
	m.Update(key("enter"))
	if m.todoInput == nil || !m.todoInput.newSub {
		t.Fatal("enter should open a box for the sub-group's name")
	}
	typeLine(m, "Basement")
	if h, ok := m.selectedHeading(); !ok || h.Sub != "Basement" {
		t.Fatalf("the cursor should land on the new sub-group, got %+v", h)
	}
	// A sub-group can hold its own.
	selectNewSub(t, m, "Home", "Basement")
	m.Update(key("enter"))
	typeLine(m, "Boiler")

	// Each one has its own new task row.
	selectAddRow(t, m, "Home", "Basement")
	m.Update(key("enter"))
	typeLine(m, "Patch the drywall")
	selectAddRow(t, m, "Home", "Basement/Boiler")
	m.Update(key("enter"))
	typeLine(m, "Bleed the radiators")
	ts, _ := st.Todos()
	if i := slices.IndexFunc(ts, func(x Todo) bool { return x.Text == "Bleed the radiators" }); i < 0 || ts[i].Sub != "Basement/Boiler" || ts[i].Group != "Home" {
		t.Fatalf("the task should go in its sub-group, got %+v", ts)
	}

	selectList(t, m, "Home")
	v := m.View()
	for _, want := range []string{
		"▾ Home (3)",
		"  ○ Paint the hallway",
		"    ▾ Basement (2)",
		"    ○ Patch the drywall",
		"      ▾ Boiler (1)",
		"      ○ Bleed the radiators",
	} {
		if !strings.Contains(v, want) {
			t.Errorf("the tab should show %q:\n%s", want, v)
		}
	}
	// The list's own tasks come first, then its sub-groups, then the button
	// for another.
	order := []string{"Paint the hallway", "Basement (2)", "Boiler (1)", "Bleed the radiators"}
	last := -1
	for _, s := range order {
		i := strings.Index(v, s)
		if i < last {
			t.Errorf("%q is out of order:\n%s", s, v)
		}
		last = i
	}

	// Folding a sub-group hides the ones inside it, and its count still
	// counts them.
	selectSub(t, m, "Home", "Basement")
	m.Update(key("enter"))
	v = m.View()
	if !strings.Contains(v, "▸ Basement (2)") || strings.Contains(v, "Boiler") || strings.Contains(v, "Patch the drywall") {
		t.Errorf("folding Basement should hide what's in it:\n%s", v)
	}
	if !strings.Contains(v, "x remove sub-group · e rename") {
		t.Errorf("the footer should offer a sub-group's keys:\n%s", v)
	}

	// Renaming keeps the fold and the tasks.
	m.Update(key("e"))
	if m.todoInput == nil || !m.todoInput.renameSub || m.todoInput.ta.Value() != "Basement" {
		t.Fatal("e should open a box with the sub-group's name")
	}
	m.todoInput.ta.SetValue("")
	typeLine(m, "Garage")
	if v := m.View(); !strings.Contains(v, "▸ Garage (2)") {
		t.Errorf("the sub-group should be renamed and stay folded:\n%s", v)
	}
	ts, _ = st.Todos()
	if i := slices.IndexFunc(ts, func(x Todo) bool { return x.Text == "Bleed the radiators" }); ts[i].Sub != "Garage/Boiler" {
		t.Errorf("the tasks inside should follow, got %+v", ts[i])
	}

	// A sub-group lives with its list.
	m.Update(key("g"))
	if m.todoInput != nil || !strings.Contains(m.errMsg, "lives wherever its list does") {
		t.Errorf("g on a sub-group should say it lives with its list, got %q", m.errMsg)
	}

	// Removing asks first, like a list, then takes its tasks.
	selectSub(t, m, "Home", "Garage")
	m.Update(key("x"))
	if want := "Remove Garage and its 1 sub-group and its 2 tasks? Press x again to confirm."; m.errMsg != want {
		t.Errorf("the first x should ask, want %q, got %q", want, m.errMsg)
	}
	if v := m.View(); !strings.Contains(v, "x remove the sub-group") {
		t.Errorf("the footer should say what x removes:\n%s", v)
	}
	m.Update(key("x"))
	ts, subs, _ := st.TodosAndSubGroups()
	if len(ts) != 1 || len(subs["Home"]) != 0 {
		t.Errorf("the sub-group should be gone with its tasks, got %+v %v", ts, subs)
	}
	if !strings.Contains(m.errMsg, "Removed Garage (2 tasks)") {
		t.Errorf("the note should say what went, got %q", m.errMsg)
	}
}

func TestSubGroupNameRefusedKeepsBox(t *testing.T) {
	st := testStore(t)
	st.AddMyGroup("Home")
	st.AddSubGroup("Home", "", "Basement")
	m := todoModel(t, st)
	selectNewSub(t, m, "Home", "")
	m.Update(key("enter"))
	typeLine(m, "basement")
	if m.todoInput == nil || !strings.Contains(m.errMsg, "already a sub-group") {
		t.Errorf("a taken name should keep the box open, got %q", m.errMsg)
	}
}

func TestMoveTodoIntoSubGroup(t *testing.T) {
	st := testStore(t)
	st.AddMyGroup("Home")
	st.AddSubGroup("Home", "", "Basement")
	st.AddSubGroup("Home", "Basement", "Boiler")
	err := st.AddTodos([]Todo{
		{ID: "a", Mine: true, Group: "Home", Text: "boiler"},
		{ID: "b", Mine: true, Group: "Home", Text: "boiler's second valve", After: "a"},
	})
	if err != nil {
		t.Fatal(err)
	}
	m := todoModel(t, st)
	// Folded, a sub-group's sub-groups are still somewhere to move to.
	selectSub(t, m, "Home", "Basement")
	m.Update(key("enter"))
	selectTask(t, m, "a")
	m.Update(key("m"))
	if m.todoMove == nil {
		t.Fatal("m should open the picker")
	}
	var labels []string
	for _, d := range m.todoMove.dests {
		labels = append(labels, d.label)
	}
	if want := []string{"My list", "  Basement", "    Boiler"}; !slices.Equal(labels, want) {
		t.Fatalf("the picker should offer the sub-groups, not the task's own place, want %q got %q", want, labels)
	}
	m.Update(key("j"))
	m.Update(key("j"))
	m.Update(key("enter"))
	if m.errMsg != "Moved it to Home / Basement / Boiler, with the 1 task waiting on it" {
		t.Errorf("the note should name the sub-group, got %q", m.errMsg)
	}
	ts, _ := st.Todos()
	for _, x := range ts {
		if x.Sub != "Basement/Boiler" || x.Group != "Home" {
			t.Errorf("both tasks should move into Boiler, got %+v", x)
		}
	}

	// From a sub-group, the list itself is somewhere to go.
	selectSub(t, m, "Home", "Basement")
	m.Update(key("enter"))
	selectTask(t, m, "a")
	m.Update(key("m"))
	labels = nil
	for _, d := range m.todoMove.dests {
		labels = append(labels, d.label)
	}
	if want := []string{"My list", "Home", "  Basement"}; !slices.Equal(labels, want) {
		t.Errorf("want %q got %q", want, labels)
	}

	if _, err := st.MoveTodo("a", "Home", "Nowhere"); err == nil {
		t.Error("a move to a sub-group that doesn't exist should fail")
	}
	if _, err := st.MoveTodo("a", "", "Basement"); err == nil {
		t.Error("My list has no sub-groups to move to")
	}
	if g, sub, err := st.myPlaceNamed("Home / Basement / Boiler"); err != nil || g != "Home" || sub != "Basement/Boiler" {
		t.Errorf("todo move should find a sub-group by its path, got %q %q %v", g, sub, err)
	}
	if _, _, err := st.myPlaceNamed("Home / Attic"); err == nil {
		t.Error("a sub-group that doesn't exist shouldn't be found")
	}
}

func TestSubGroupFinishesOnItsOwn(t *testing.T) {
	now := time.Now()
	done := now.Add(-time.Hour)
	ts := []Todo{
		{ID: "a", Mine: true, Group: "Home", Text: "open"},
		{ID: "b", Mine: true, Group: "Home", Sub: "Basement", Text: "done", Done: true, DoneAt: done},
		{ID: "c", Mine: true, Group: "Home", Sub: "Attic", Text: "done", Done: true, DoneAt: done},
		{ID: "d", Mine: true, Group: "Home", Sub: "Attic", Text: "open"},
	}
	ids, _ := expiredTodos(ts, now)
	if !ids["b"] || ids["c"] || ids["a"] || ids["d"] {
		t.Errorf("only a finished sub-group's tasks should clear, got %v", ids)
	}
}

func TestSubGroupsInTextOutput(t *testing.T) {
	ts := []Todo{
		{ID: "a", Mine: true, Group: "Home", Text: "paint"},
		{ID: "b", Mine: true, Group: "Home", Sub: "Basement/Boiler", Text: "boiler"},
	}
	out := strings.Join(todoLines(ts), "\n")
	if !strings.Contains(out, ": Home\n  a [ ] paint") || !strings.Contains(out, ": Home / Basement / Boiler\n  b [ ] boiler") {
		t.Errorf("todo ls should give each sub-group a section:\n%s", out)
	}
	if msg := FormatTodoHandoff(ts[1], ""); !strings.Contains(msg, `from the user's list "Home", in its sub-group "Basement / Boiler"`) {
		t.Errorf("the handoff should name the sub-group, got %s", msg)
	}
}
