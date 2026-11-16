package main

import (
	"fmt"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// MoveTodo moves the user's task with the given ID to their list named
// group, "" for My list, wherever each list keeps its tasks. sub is the
// sub-group of the list it goes in, "" for the list's own tasks. Tasks
// waiting on it go along, as they do when it's snoozed, so no task waits on
// one in another list. It returns how many tasks moved.
func (s *Store) MoveTodo(id, group, sub string) (int, error) {
	made, err := s.MyGroups()
	if err != nil {
		return 0, err
	}
	n := 0
	err = s.editTodos(nil, func(tx *todoTx) (bool, error) {
		i := slices.IndexFunc(tx.ts, func(t Todo) bool { return t.ID == id })
		switch {
		case i < 0:
			return false, fmt.Errorf("%w: %s", errNoTodo, id)
		case !tx.ts[i].Mine:
			return false, fmt.Errorf("only the user's own tasks move between lists")
		case tx.ts[i].Group == group && tx.ts[i].Sub == sub:
			return false, nil
		}
		if err := tx.checkMyPlace(made, group, sub); err != nil {
			return false, err
		}
		from, fromSub := tx.ts[i].Group, tx.ts[i].Sub
		moving := map[string]bool{id: true}
		for changed := true; changed; {
			changed = false
			for _, t := range tx.ts {
				if t.Mine && t.Group == from && t.Sub == fromSub && !moving[t.ID] && t.After != "" && moving[t.After] {
					moving[t.ID], changed = true, true
				}
			}
		}
		// The moved tasks go to the end of their new list, like new ones.
		var stay, moved []Todo
		for _, t := range tx.ts {
			if !moving[t.ID] {
				stay = append(stay, t)
				continue
			}
			t.Group, t.Sub = group, sub
			if !moving[t.After] {
				t.After = ""
			}
			moved = append(moved, t)
		}
		tx.ts, n = append(stay, moved...), len(moved)
		return true, nil
	})
	return n, err
}

// checkMyPlace returns an error unless group is one of the user's lists and
// sub is "" or a sub-group of it. made is the lists from MyGroups.
func (tx *todoTx) checkMyPlace(made []string, group, sub string) error {
	switch {
	case sub != "" && group == "":
		return fmt.Errorf("My list has no sub-groups")
	case sub != "" && !slices.Contains(withTaskSubs(tx.subs[group], myTasks(tx.ts, group)), sub):
		return fmt.Errorf("no sub-group %q in %s", sub, group)
	}
	// Another session may have made the list, with its tasks in a repo.
	known := group == "" || slices.Contains(made, group) || tx.backs[group].Git != "" ||
		slices.ContainsFunc(tx.ts, func(t Todo) bool { return t.Mine && t.Group == group })
	if !known {
		return fmt.Errorf("no list of the user's named %q", group)
	}
	return nil
}

// AddMyTodos adds ts to the user's list named group, "" for My list, in its
// sub-group sub, wherever the list keeps its tasks. It sets each task's Mine,
// Group, and Sub.
func (s *Store) AddMyTodos(group, sub string, add []Todo) error {
	made, err := s.MyGroups()
	if err != nil {
		return err
	}
	return s.editTodos(nil, func(tx *todoTx) (bool, error) {
		if err := tx.checkMyPlace(made, group, sub); err != nil {
			return false, err
		}
		for _, t := range add {
			t.Mine, t.Group, t.Sub = true, group, sub
			tx.ts = append(tx.ts, t)
		}
		return true, nil
	})
}

// myGroupNamed finds the user's list that name means: one they made, or My
// list by "My list". It returns "" for My list. A list they made can also go by its name in todo ls, like
// "My list: Groceries".
func (s *Store) myGroupNamed(name string) (string, error) {
	made, err := s.MyGroups()
	if rest, ok := strings.CutPrefix(name, myListName()+": "); ok && slices.Contains(made, rest) {
		name = rest
	}
	switch {
	case err != nil:
		return "", err
	case slices.Contains(made, name):
		return name, nil
	case name == "" || strings.EqualFold(name, "My list") || strings.EqualFold(name, myListName()):
		return "", nil
	}
	names := append([]string{"My list"}, made...)
	return "", fmt.Errorf("no list named %q. The user's lists: %s", name, strings.Join(names, ", "))
}

// myPlaceNamed finds the user's list, or a sub-group of one, that name
// means. A sub-group goes by its list's name and its path, as todo ls shows
// them, like "Home / Basement / Boiler". It returns "" for My list, and for a
// list's own tasks.
func (s *Store) myPlaceNamed(name string) (group, sub string, err error) {
	group, err = s.myGroupNamed(name)
	parts := strings.Split(name, " / ")
	if err == nil || len(parts) < 2 {
		return group, "", err
	}
	group, gerr := s.myGroupNamed(parts[0])
	if gerr != nil {
		return "", "", err
	}
	sub = strings.Join(parts[1:], "/")
	if _, subs, serr := s.TodosAndSubGroups(); serr != nil {
		return "", "", serr
	} else if !slices.Contains(subs[group], sub) {
		return "", "", fmt.Errorf("no sub-group %q in %s", strings.Join(parts[1:], " / "), group)
	}
	return group, sub, nil
}

// todoDest is a list, or a sub-group of one, that the picker can move a task
// to.
type todoDest struct {
	label string // as the picker shows it
	name  string // as the note after the move names it
	group string // "" for My list
	sub   string
}

// todoMover picks the list to move one of the user's tasks to.
type todoMover struct {
	id    string
	dests []todoDest
	cur   int
}

// todoDests lists the user's lists and their sub-groups that the task t can
// move to, in the order the tab shows them. A list's sub-groups count even
// while it's folded.
func (m *model) todoDests(t Todo) []todoDest {
	var out []todoDest
	for _, it := range m.todoItems {
		if !it.heading || !it.Mine || it.Sub != "" {
			continue
		}
		name := it.group
		if it.Group == "" {
			name = "My list"
		}
		label := name
		if m.todoBacks[it.Group].Git != "" {
			label += " · git"
		}
		if it.Group != t.Group || t.Sub != "" {
			out = append(out, todoDest{label: label, name: name, group: it.Group})
		}
		if it.Group == "" {
			continue
		}
		for _, sub := range preorderSubs(m.todoSubs[it.Group], "") {
			if it.Group == t.Group && sub == t.Sub {
				continue
			}
			out = append(out, todoDest{
				label: strings.Repeat("  ", subDepth(sub)) + subName(sub),
				name:  name + " / " + strings.ReplaceAll(sub, "/", " / "),
				group: it.Group,
				sub:   sub,
			})
		}
	}
	return out
}

// openMover opens the picker for where to move the selected task.
func (m *model) openMover() {
	t, ok := m.selectedTodo()
	switch {
	case !ok:
	case !t.Mine:
		m.note("Only your own tasks move between lists")
	default:
		dests := m.todoDests(t.Todo)
		if len(dests) == 0 {
			m.note("No other list to move it to. Make one with + New group")
			return
		}
		m.todoMove = &todoMover{id: t.ID, dests: dests}
	}
}

func (m *model) updateMover(msg tea.KeyMsg) tea.Cmd {
	mv := m.todoMove
	switch msg.String() {
	case "ctrl+c":
		return tea.Quit
	case "esc", "q", "m":
		m.todoMove = nil
	case "j", "down":
		mv.cur = min(mv.cur+1, len(mv.dests)-1)
	case "k", "up":
		mv.cur = max(mv.cur-1, 0)
	case "enter", " ":
		m.todoMove = nil
		d := mv.dests[mv.cur]
		n, err := m.st.MoveTodo(mv.id, d.group, d.sub)
		m.reloadTodos()
		if err != nil {
			m.setError(err.Error())
			return nil
		}
		note := "Moved it to " + d.name
		if n > 1 {
			note += fmt.Sprintf(", with the %d %s waiting on it", n-1, plural(n-1, "task", "tasks"))
		}
		m.note(note)
	}
	return nil
}

func (mv *todoMover) render(w int) string {
	var b strings.Builder
	b.WriteString("    " + sDim.Render("Move to:") + "\n")
	for i, d := range mv.dests {
		line := "  " + d.label
		if i == mv.cur {
			line = sSel.Render("› " + d.label)
		}
		b.WriteString("    " + truncate(line, max(10, w-4)) + "\n")
	}
	return b.String()
}
