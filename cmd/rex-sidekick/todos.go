package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Todo is a task on the sidekick's TODO tab, filed by an agent or by the user.
type Todo struct {
	ID   string `json:"id"`
	List string `json:"list,omitempty"` // tasks with the same list are grouped together
	From string `json:"from,omitempty"` // the filing agent's Rex tab name
	// The Rex pane to tell when the user marks the task done.
	NotifySession string `json:"notify_session,omitempty"`
	NotifyBlock   string `json:"notify_block,omitempty"`
	Text          string `json:"text"`
	Detail        string `json:"detail,omitempty"`
	After         string `json:"after,omitempty"` // ID of the task this one waits on
	Mine          bool   `json:"mine,omitempty"`  // filed by the user in the sidekick
	// Which of the user's own lists holds the task. Empty means My list.
	Group string `json:"group,omitempty"`
	// The sub-group of the list that holds the task, as a path of names
	// like "Basement/Boiler". Empty means the list's own tasks.
	Sub     string    `json:"sub,omitempty"`
	ShowAt  time.Time `json:"show_at,omitzero"`  // the tab hides the task until then
	Started bool      `json:"started,omitempty"` // someone is working on it
	// An agent's list for the user to act on, such as reminders. It shows in
	// the user's section of the tab, above the agents' lists.
	ForUser   bool      `json:"for_user,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	Done      bool      `json:"done,omitempty"`
	DoneAt    time.Time `json:"done_at,omitzero"`
	DoneBy    string    `json:"done_by,omitempty"` // "user" or "agent"
	DoneNote  string    `json:"done_note,omitempty"`
}

func newTodoID() string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return "t-" + hex.EncodeToString(b)
}

func (s *Store) todosPath() string { return filepath.Join(s.dir, s.key+".todos.json") }

// withTodos runs fn on the TODO list while holding its lock, and saves the
// list if fn changed it. The list includes the tasks of the user's lists
// kept in git repos.
func (s *Store) withTodos(fn func(ts []Todo) ([]Todo, bool)) error {
	return s.editTodos(nil, func(tx *todoTx) (bool, error) {
		var changed bool
		tx.ts, changed = fn(tx.ts)
		return changed, nil
	})
}

func (s *Store) Todos() ([]Todo, error) {
	ts, _, err := s.TodosAndSubGroups()
	return ts, err
}

// TodosAndSubGroups returns the TODO list, and the sub-groups of the user's
// lists by list name.
func (s *Store) TodosAndSubGroups() ([]Todo, map[string][]string, error) {
	var ts []Todo
	var subs map[string][]string
	err := s.editTodos(nil, func(tx *todoTx) (bool, error) {
		ts, subs = tx.ts, tx.subs
		for _, t := range ts {
			if t.Mine && t.Group != "" && t.Sub != "" {
				if _, ok := subs[t.Group]; !ok {
					subs[t.Group] = nil
				}
			}
		}
		for name, ps := range subs {
			subs[name] = withTaskSubs(ps, myTasks(ts, name))
		}
		return false, nil
	})
	return ts, subs, err
}

// TodosModTime is the latest change to the TODO list, wherever its tasks
// are kept.
func (s *Store) TodosModTime() time.Time {
	latest := s.todoFilesModTime()
	for _, path := range []string{s.todosPath(), s.backendsPath(), s.subGroupsPath()} {
		if fi, err := os.Stat(path); err == nil && fi.ModTime().After(latest) {
			latest = fi.ModTime()
		}
	}
	return latest
}

// AddTodos appends ts. Each task's After must name a task that exists or
// comes earlier in ts.
func (s *Store) AddTodos(add []Todo) error {
	return s.withTodos(func(ts []Todo) ([]Todo, bool) {
		return append(ts, add...), true
	})
}

var errNoTodo = fmt.Errorf("no such task")

func (s *Store) myGroupsPath() string { return filepath.Join(s.dir, s.key+".todo-groups.json") }

// withMyGroups runs fn on the names of the user's own lists, other than My
// list, while holding their lock, and saves the names fn returns.
func (s *Store) withMyGroups(fn func(gs []string) ([]string, error)) error {
	return withFileLock(s.myGroupsPath()+".lock", func() error {
		var gs []string
		if err := readJSON(s.myGroupsPath(), &gs); err != nil {
			return err
		}
		gs, err := fn(gs)
		if err != nil {
			return err
		}
		return writeJSON(s.myGroupsPath(), gs)
	})
}

// MyGroups returns the names of the user's own lists after My list, in the
// order they were made.
func (s *Store) MyGroups() ([]string, error) {
	var gs []string
	err := withFileLock(s.myGroupsPath()+".lock", func() error { return readJSON(s.myGroupsPath(), &gs) })
	return gs, err
}

var errGroupExists = fmt.Errorf("there's already a list by that name")

// AddMyGroup adds one of the user's own lists, after the ones they have.
func (s *Store) AddMyGroup(name string) error {
	return s.withMyGroups(func(gs []string) ([]string, error) {
		if slices.Contains(gs, name) {
			return nil, errGroupExists
		}
		return append(gs, name), nil
	})
}

// RenameMyGroup renames one of the user's own lists and moves its tasks.
func (s *Store) RenameMyGroup(from, to string) error {
	return s.withMyGroups(func(gs []string) ([]string, error) {
		i := slices.Index(gs, from)
		switch {
		case from == to:
			return gs, nil
		case slices.Contains(gs, to):
			return nil, errGroupExists
		case i < 0:
			return nil, fmt.Errorf("no list named %q", from)
		}
		gs[i] = to
		return gs, s.editTodos(nil, func(tx *todoTx) (bool, error) {
			changed := false
			for i := range tx.ts {
				if tx.ts[i].Mine && tx.ts[i].Group == from {
					tx.ts[i].Group, changed = to, true
				}
			}
			if subs, ok := tx.subs[from]; ok {
				delete(tx.subs, from)
				tx.subs[to], changed = subs, true
			}
			// A list in a git repo moves to a folder under its new name.
			if b := tx.backs[from]; b.Git != "" {
				backs := maps.Clone(tx.backs)
				delete(backs, from)
				if other, ok := folderTaken(backs, b.Git, to); ok {
					return false, errFolderTaken(to, other)
				}
				if err := tx.load(b.Git, to, to); err != nil {
					return false, err
				}
				tx.backs[to] = b
				delete(tx.backs, from)
				tx.drop = append(tx.drop, repoGroup{b.Git, from})
				tx.backsChanged, changed = true, true
			}
			return changed, nil
		})
	})
}

// RemoveMyGroup drops one of the user's own lists. Its tasks are removed
// separately. A list in a git repo takes its file out of the repo.
func (s *Store) RemoveMyGroup(name string) error {
	return s.withMyGroups(func(gs []string) ([]string, error) {
		return slices.DeleteFunc(gs, func(g string) bool { return g == name }), s.editTodos(nil, func(tx *todoTx) (bool, error) {
			_, hadSubs := tx.subs[name]
			delete(tx.subs, name)
			b := tx.backs[name]
			if b.Git == "" {
				return hadSubs, nil
			}
			delete(tx.backs, name)
			tx.drop = append(tx.drop, repoGroup{b.Git, name})
			tx.backsChanged = true
			return true, nil
		})
	})
}

func (s *Store) myListPath() string { return filepath.Join(s.dir, s.key+".todo-mylist.json") }

type myListState struct {
	Removed bool `json:"removed"`
}

// MyListRemoved reports whether the user removed My list.
func (s *Store) MyListRemoved() (bool, error) {
	var st myListState
	err := readJSON(s.myListPath(), &st)
	return st.Removed, err
}

// RemoveMyList drops My list for good. Its tasks are removed separately.
func (s *Store) RemoveMyList() error {
	return writeJSON(s.myListPath(), myListState{Removed: true})
}

// UpdateTodo runs fn on the task with the given ID.
func (s *Store) UpdateTodo(id string, fn func(t *Todo, all []Todo) error) error {
	var ferr error
	found := false
	err := s.withTodos(func(ts []Todo) ([]Todo, bool) {
		for i := range ts {
			if ts[i].ID == id {
				found = true
				ferr = fn(&ts[i], ts)
				return ts, ferr == nil
			}
		}
		return ts, false
	})
	switch {
	case err != nil:
		return err
	case !found:
		return fmt.Errorf("%w: %s", errNoTodo, id)
	}
	return ferr
}

// RemoveTodos drops the tasks that match. Tasks that waited on a removed task
// move up to wait on whatever it waited on.
func (s *Store) RemoveTodos(match func(Todo) bool) (int, error) {
	n := 0
	err := s.withTodos(func(ts []Todo) ([]Todo, bool) {
		ts, n = dropTodos(ts, match)
		return ts, n > 0
	})
	return n, err
}

// dropTodos drops the tasks in ts that match, as RemoveTodos does, and
// says how many went.
func dropTodos(ts []Todo, match func(Todo) bool) ([]Todo, int) {
	parent := map[string]string{}
	for _, t := range ts {
		if match(t) {
			parent[t.ID] = t.After
		}
	}
	kept := ts[:0]
	for _, t := range ts {
		if _, gone := parent[t.ID]; gone {
			continue
		}
		for {
			p, gone := parent[t.After]
			if !gone || t.After == "" {
				break
			}
			t.After = p
		}
		kept = append(kept, t)
	}
	return kept, len(parent)
}

var errSubGroupExists = fmt.Errorf("there's already a sub-group by that name there")

// checkSubName reports whether name can be a sub-group's name next to
// siblings. Siblings can't share a folder in a git repo, so the check
// ignores case and punctuation.
func checkSubName(name string, siblings []string) error {
	switch {
	case name == "":
		return fmt.Errorf("a sub-group needs a name")
	case strings.Contains(name, "/"):
		return fmt.Errorf("a sub-group's name can't have a / in it")
	}
	for _, sib := range siblings {
		if slugify(subName(sib)) == slugify(name) {
			return errSubGroupExists
		}
	}
	return nil
}

// AddSubGroup adds a sub-group named name to one of the user's lists, inside
// the sub-group at parent, or at the top of the list when parent is empty.
// It goes after the sub-groups already there.
func (s *Store) AddSubGroup(group, parent, name string) error {
	if group == "" {
		return fmt.Errorf("make a list to use sub-groups")
	}
	return s.editTodos(nil, func(tx *todoTx) (bool, error) {
		subs := withTaskSubs(tx.subs[group], myTasks(tx.ts, group))
		if parent != "" && !slices.Contains(subs, parent) {
			return false, fmt.Errorf("no sub-group %q in %s", parent, group)
		}
		if err := checkSubName(name, subChildren(subs, parent)); err != nil {
			return false, err
		}
		tx.subs[group] = append(subs, subJoin(parent, name))
		return true, nil
	})
}

// RenameSubGroup renames the sub-group at path in one of the user's lists,
// along with the paths of its tasks and the sub-groups inside it.
func (s *Store) RenameSubGroup(group, path, name string) error {
	return s.editTodos(nil, func(tx *todoTx) (bool, error) {
		subs := withTaskSubs(tx.subs[group], myTasks(tx.ts, group))
		if !slices.Contains(subs, path) {
			return false, fmt.Errorf("no sub-group %q in %s", path, group)
		}
		if name == subName(path) {
			return false, nil
		}
		siblings := slices.DeleteFunc(subChildren(subs, subParent(path)), func(p string) bool { return p == path })
		if err := checkSubName(name, siblings); err != nil {
			return false, err
		}
		to := subJoin(subParent(path), name)
		move := func(p string) string {
			if p != "" && inSub(p, path) {
				return to + p[len(path):]
			}
			return p
		}
		for i, p := range subs {
			subs[i] = move(p)
		}
		tx.subs[group] = subs
		for i, t := range tx.ts {
			if t.Mine && t.Group == group {
				tx.ts[i].Sub = move(t.Sub)
			}
		}
		return true, nil
	})
}

// RemoveSubGroup drops the sub-group at path from one of the user's lists,
// with the sub-groups inside it and all their tasks, and says how many tasks
// went.
func (s *Store) RemoveSubGroup(group, path string) (int, error) {
	if path == "" {
		return 0, fmt.Errorf("no sub-group given")
	}
	n := 0
	err := s.editTodos(nil, func(tx *todoTx) (bool, error) {
		tx.ts, n = dropTodos(tx.ts, func(t Todo) bool { return t.Mine && t.Group == group && inSub(t.Sub, path) })
		before := len(tx.subs[group])
		tx.subs[group] = slices.DeleteFunc(tx.subs[group], func(p string) bool { return inSub(p, path) })
		return n > 0 || len(tx.subs[group]) != before, nil
	})
	return n, err
}

// myTasks returns the user's tasks on the list named group.
func myTasks(ts []Todo, group string) []Todo {
	var out []Todo
	for _, t := range ts {
		if t.Mine && t.Group == group {
			out = append(out, t)
		}
	}
	return out
}

// subJoin is the path of the sub-group named name inside the one at parent.
func subJoin(parent, name string) string {
	if parent == "" {
		return name
	}
	return parent + "/" + name
}

// subName is the name of the sub-group at path.
func subName(path string) string { return path[strings.LastIndex(path, "/")+1:] }

// subParent is the path of the sub-group that holds the one at path, or ""
// when the list itself does.
func subParent(path string) string {
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[:i]
	}
	return ""
}

// subDepth is how many sub-groups deep path is. The list itself is 0.
func subDepth(path string) int {
	if path == "" {
		return 0
	}
	return strings.Count(path, "/") + 1
}

// inSub reports whether the sub-group at p is the one at path or inside it.
// Everything is inside the list itself, at "".
func inSub(p, path string) bool {
	return path == "" || p == path || strings.HasPrefix(p, path+"/")
}

// subChildren returns the sub-groups directly inside the one at parent, in
// order.
func subChildren(subs []string, parent string) []string {
	var out []string
	for _, p := range subs {
		if p != "" && subParent(p) == parent {
			out = append(out, p)
		}
	}
	return out
}

// withTaskSubs returns subs with any sub-group that a task in ts is on but
// subs lacks, such as one named in a file edited by hand, and the
// sub-groups that hold it.
func withTaskSubs(subs []string, ts []Todo) []string {
	out := slices.Clone(subs)
	var need func(p string)
	need = func(p string) {
		if p == "" || slices.Contains(out, p) {
			return
		}
		need(subParent(p))
		out = append(out, p)
	}
	for _, p := range subs {
		need(subParent(p))
	}
	for _, t := range ts {
		need(t.Sub)
	}
	return out
}

// todoNode is a task placed in its list's tree. Depth is how many levels it
// nests. A chain of tasks, each waiting on the one before, stays at one level
// as numbered steps. Only a task with several dependents nests them.
type todoNode struct {
	Todo
	Depth   int
	Blocked bool // the task it waits on isn't done
	// The task, or one it waits on, is scheduled to show later. The tab hides
	// it until then.
	Later bool
	// Step is the task's number in its chain, from 1, or 0 when it isn't in
	// one. StepWidth is how many digits the chain's last number has.
	Step, StepWidth int
	chain           int // index in the tree of the chain's first step
}

// later reports whether t is scheduled to show after now.
func (t Todo) later(now time.Time) bool { return !t.Done && t.ShowAt.After(now) }

// todoTree orders one list's tasks so each one follows the task it waits on.
// A task's only dependent comes next as the following step at the same level.
// Several dependents nest one level deeper. Otherwise tasks keep the order
// they were filed.
func todoTree(ts []Todo, now time.Time) []todoNode {
	byID := map[string]Todo{}
	for _, t := range ts {
		byID[t.ID] = t
	}
	kids := map[string][]Todo{}
	var roots []Todo
	for _, t := range ts {
		if _, ok := byID[t.After]; ok && t.After != t.ID {
			kids[t.After] = append(kids[t.After], t)
		} else {
			roots = append(roots, t)
		}
	}
	var out []todoNode
	seen := map[string]bool{}
	var walk func(t Todo, depth int, blocked, later bool, step, chain int)
	walk = func(t Todo, depth int, blocked, later bool, step, chain int) {
		if seen[t.ID] {
			return
		}
		seen[t.ID] = true
		later = later || t.later(now)
		i := len(out)
		out = append(out, todoNode{Todo: t, Depth: depth, Blocked: blocked, Later: later, Step: step, chain: chain})
		blocked = blocked || !t.Done
		if ks := kids[t.ID]; len(ks) == 1 && !seen[ks[0].ID] {
			if step == 0 {
				step, chain = 1, i
				out[i].Step, out[i].chain = 1, i
			}
			walk(ks[0], depth, blocked, later, step+1, chain)
			return
		}
		for _, k := range kids[t.ID] {
			walk(k, depth+1, blocked, later, 0, 0)
		}
	}
	for _, r := range roots {
		walk(r, 0, false, false, 0, 0)
	}
	// Tasks caught in a loop of dependencies still show up.
	for _, t := range ts {
		if !seen[t.ID] {
			walk(t, 0, false, false, 0, 0)
		}
	}
	last := map[int]int{}
	for _, n := range out {
		if n.Step > 0 {
			last[n.chain] = max(last[n.chain], n.Step)
		}
	}
	for i, n := range out {
		if n.Step > 0 {
			out[i].StepWidth = len(strconv.Itoa(last[n.chain]))
		}
	}
	return out
}

// finishedListTTL is how long a list stays on the tab once every task on it
// is done.
const finishedListTTL = 15 * time.Minute

// finishedAt reports when the last task of a list was done, if every task on
// it is.
func finishedAt(list []Todo) (time.Time, bool) {
	var at time.Time
	for _, t := range list {
		if !t.Done {
			return time.Time{}, false
		}
		if t.DoneAt.After(at) {
			at = t.DoneAt
		}
	}
	return at, len(list) > 0
}

// expiredTodos returns the IDs of tasks on lists that finished more than
// finishedListTTL before now, and the soonest time another finished list
// expires.
func expiredTodos(ts []Todo, now time.Time) (ids map[string]bool, next time.Time) {
	names, groups, mine := todoGroups(ts)
	// Each sub-group of the user's lists finishes on its own, as a list does.
	byGroup := map[[2]string][]Todo{}
	for _, t := range mine {
		k := [2]string{t.Group, t.Sub}
		byGroup[k] = append(byGroup[k], t)
	}
	var lists [][]Todo
	for _, list := range byGroup {
		lists = append(lists, list)
	}
	for _, name := range names {
		lists = append(lists, groups[name])
	}
	ids = map[string]bool{}
	for _, list := range lists {
		at, ok := finishedAt(list)
		if !ok {
			continue
		}
		if expires := at.Add(finishedListTTL); !now.Before(expires) {
			for _, t := range list {
				ids[t.ID] = true
			}
		} else if next.IsZero() || expires.Before(next) {
			next = expires
		}
	}
	return ids, next
}

// todoGroups splits tasks into lists: agents' lists in the order they were
// started, then the user's own list.
func todoGroups(ts []Todo) (names []string, groups map[string][]Todo, mine []Todo) {
	groups = map[string][]Todo{}
	for _, t := range ts {
		if t.Mine {
			mine = append(mine, t)
			continue
		}
		name := t.List
		if name == "" {
			name = "an agent"
		}
		if _, ok := groups[name]; !ok {
			names = append(names, name)
		}
		groups[name] = append(groups[name], t)
	}
	return names, groups, mine
}

// myGroups splits the user's own tasks by list: My list's under "", then
// the rest in the order they first show up.
func myGroups(mine []Todo) (names []string, groups map[string][]Todo) {
	groups = map[string][]Todo{}
	for _, t := range mine {
		if _, ok := groups[t.Group]; !ok && t.Group != "" {
			names = append(names, t.Group)
		}
		groups[t.Group] = append(groups[t.Group], t)
	}
	return names, groups
}

// FormatTodoNotice is the message sent to the agent when the user finishes
// one of its tasks.
func FormatTodoNotice(t Todo) string {
	msg := fmt.Sprintf("[sidekick] todo %s %q done by the user.", t.ID, truncate(t.Text, 80))
	if t.DoneNote != "" {
		msg += fmt.Sprintf(" Note: %q", t.DoneNote)
	}
	return strings.Join(strings.Fields(msg), " ")
}

// FormatTodoMessage is the message sent to the agent when the user asks it
// something about one of its tasks.
func FormatTodoMessage(t Todo, text string) string {
	msg := fmt.Sprintf("[sidekick] todo %s %q, message from the user: %q", t.ID, truncate(t.Text, 80), text)
	return strings.Join(strings.Fields(msg), " ")
}

// FormatTodoHandoff is the message sent to the dispatcher when the user hands
// it a task from their own list.
func FormatTodoHandoff(t Todo, note string) string {
	from := "the user's own list"
	if t.Group != "" {
		from = fmt.Sprintf("the user's list %q", t.Group)
	}
	if t.Group != "" && t.Sub != "" {
		from += fmt.Sprintf(", in its sub-group %q", strings.ReplaceAll(t.Sub, "/", " / "))
	}
	msg := strings.Join(strings.Fields(fmt.Sprintf("[sidekick] todo %s %q, handed to you from %s to dispatch.", t.ID, truncate(t.Text, 200), from)), " ")
	// The details can be a plan in Markdown. Quoting keeps its line breaks
	// as \n while the message stays on one line.
	if d := strings.TrimSpace(t.Detail); d != "" {
		msg += fmt.Sprintf(" Detail: %q", d)
	}
	if note != "" {
		msg += " " + strings.Join(strings.Fields(fmt.Sprintf("Note from the user: %q", note)), " ")
	}
	return msg + fmt.Sprintf(" Mark it done with `rex-sidekick todo done %s` when the work is finished.", t.ID)
}

// myListName names the user's own list.
func myListName() string {
	return "My list"
}

// todoLines describes tasks for `todo ls`.
func todoLines(ts []Todo) []string {
	names, groups, mine := todoGroups(ts)
	now := time.Now()
	var out []string
	section := func(name string, list []Todo) {
		out = append(out, name)
		for _, n := range todoTree(list, now) {
			mark := "[ ]"
			switch {
			case n.Done:
				mark = "[x]"
			case n.Started:
				mark = "[~]"
			}
			line := fmt.Sprintf("  %s %s %s%s", n.ID, mark, strings.Repeat("  ", n.Depth), n.treeLead())
			line += n.Text
			if n.Detail != "" {
				line += " (details)"
			}
			if n.After != "" {
				line += " (after " + n.After + ")"
			}
			if n.later(now) {
				line += " (hidden until " + n.ShowAt.Format("2006-01-02 15:04") + ")"
			}
			if n.Done && n.DoneBy == "user" {
				line += ", done by the user"
				if n.DoneNote != "" {
					line += fmt.Sprintf(": %q", n.DoneNote)
				}
			}
			out = append(out, line)
		}
	}
	mineNames, mineGroups := myGroups(mine)
	if l := mineGroups[""]; len(l) > 0 {
		section(myListName(), l)
	}
	for _, name := range mineNames {
		// Each sub-group gets its own section, after the list's own tasks.
		var subs []string
		bySub := map[string][]Todo{}
		for _, t := range mineGroups[name] {
			if _, ok := bySub[t.Sub]; !ok && t.Sub != "" {
				subs = append(subs, t.Sub)
			}
			bySub[t.Sub] = append(bySub[t.Sub], t)
		}
		if l := bySub[""]; len(l) > 0 {
			section(myListName()+": "+name, l)
		}
		for _, sub := range subs {
			section(myListName()+": "+name+" / "+strings.ReplaceAll(sub, "/", " / "), bySub[sub])
		}
	}
	for _, name := range names {
		section(name, groups[name])
	}
	return out
}

// todoShow describes one task in full for `todo show`: its line from
// `todo ls`, then its details as written.
func todoShow(t Todo) string {
	out := strings.TrimSpace(todoLines([]Todo{t})[1])
	if t.Detail != "" {
		out += "\n\n" + strings.TrimRight(t.Detail, "\n")
	}
	return out + "\n"
}

func validAfter(after string, ts []Todo) error {
	if after == "" || slices.ContainsFunc(ts, func(t Todo) bool { return t.ID == after }) {
		return nil
	}
	return fmt.Errorf("%w: %s", errNoTodo, after)
}

// parseWhen reads a time to show a task. It takes a span from now ("90m",
// "2h", "3d", "1w"), a day ("today", "tomorrow", "fri", "2026-11-10"), a clock
// time ("9:30", "3pm"), a day and a clock time ("tomorrow 9am"), or RFC 3339.
// A day on its own means the start of that day. A clock time on its own means
// the next time the clock reads it.
func parseWhen(s string, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	s = strings.ToLower(s)
	if d, ok := parseSpan(s); ok {
		return now.Add(d), nil
	}
	bad := fmt.Errorf("can't read %q as a time. Try 2h, 3d, tomorrow, fri 9am, or 2026-11-10 14:00", s)
	fields := strings.Fields(s)
	if len(fields) == 0 || len(fields) > 2 {
		return time.Time{}, bad
	}
	y, mo, d := now.Date()
	day := func(offset int) time.Time { return time.Date(y, mo, d+offset, 0, 0, 0, 0, now.Location()) }
	var date time.Time
	switch f := fields[0]; {
	case f == "today":
		date = day(0)
	case f == "tomorrow":
		date = day(1)
	default:
		if wd, ok := parseWeekday(f); ok {
			// The next one, never today.
			date = day((int(wd)-int(now.Weekday())+6)%7 + 1)
		} else if t, err := time.ParseInLocation("2006-01-02", f, now.Location()); err == nil {
			date = t
		}
	}
	clock := fields
	if !date.IsZero() {
		clock = fields[1:]
	}
	switch {
	case len(clock) == 0:
		return date, nil
	case len(clock) > 1:
		return time.Time{}, bad
	}
	var c time.Time
	var err error
	for _, layout := range []string{"15:04", "3pm", "3:04pm"} {
		if c, err = time.Parse(layout, clock[0]); err == nil {
			break
		}
	}
	if err != nil {
		return time.Time{}, bad
	}
	if date.IsZero() {
		t := time.Date(y, mo, d, c.Hour(), c.Minute(), 0, 0, now.Location())
		if !t.After(now) {
			t = time.Date(y, mo, d+1, c.Hour(), c.Minute(), 0, 0, now.Location())
		}
		return t, nil
	}
	return time.Date(date.Year(), date.Month(), date.Day(), c.Hour(), c.Minute(), 0, 0, now.Location()), nil
}

// parseSpan reads a positive span like "45m", "1h30m", "3d", or "2w".
func parseSpan(s string) (time.Duration, bool) {
	if d, err := time.ParseDuration(s); err == nil {
		return d, d > 0
	}
	if len(s) < 2 {
		return 0, false
	}
	n, err := strconv.Atoi(s[:len(s)-1])
	if err != nil || n <= 0 {
		return 0, false
	}
	switch s[len(s)-1] {
	case 'd':
		return time.Duration(n) * 24 * time.Hour, true
	case 'w':
		return time.Duration(n) * 7 * 24 * time.Hour, true
	}
	return 0, false
}

func parseWeekday(s string) (time.Weekday, bool) {
	for wd := time.Sunday; wd <= time.Saturday; wd++ {
		name := strings.ToLower(wd.String())
		if s == name || s == name[:3] {
			return wd, true
		}
	}
	return 0, false
}

// formatWhen names a coming time briefly, like "16:30", "tomorrow 9:00",
// "Fri", or "Nov 10 14:00".
func formatWhen(t, now time.Time) string {
	t = t.In(now.Location())
	clock := " " + t.Format("15:04")
	if t.Hour() == 0 && t.Minute() == 0 {
		clock = ""
	}
	y, mo, d := now.Date()
	switch days := int(time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, now.Location()).Sub(time.Date(y, mo, d, 0, 0, 0, 0, now.Location())).Hours()+12) / 24; {
	case days <= 0 && clock != "":
		return clock[1:]
	case days <= 0:
		return "today"
	case days == 1:
		return "tomorrow" + clock
	case days < 7:
		return t.Format("Mon") + clock
	case t.Year() == y:
		return t.Format("Jan 2") + clock
	}
	return t.Format("Jan 2 2006") + clock
}

// treeLead is what goes before a task's mark: a "↳" when it nests under a
// task with several dependents, then its step number if it's in a chain.
// Later steps of a nested chain get spaces in place of the "↳", so the
// numbers line up.
func (n todoNode) treeLead() string {
	lead := ""
	switch {
	case n.Depth > 0 && n.Step <= 1:
		lead = "↳ "
	case n.Depth > 0:
		lead = "  "
	}
	if n.Step > 0 {
		lead += fmt.Sprintf("%*d. ", n.StepWidth, n.Step)
	}
	return lead
}

// preorderSubs returns the sub-groups inside the one at parent, in order,
// each followed by the ones inside it.
func preorderSubs(subs []string, parent string) []string {
	var out []string
	for _, k := range subChildren(subs, parent) {
		out = append(out, k)
		out = append(out, preorderSubs(subs, k)...)
	}
	return out
}
