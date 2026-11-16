package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestMoveTodoAcrossBackends moves one task through every kind of list: the
// session's, a git repo's, another list in the same repo, and one in another
// repo, then back. It keeps everything but its list, and each repo commits
// what changed.
func TestMoveTodoAcrossBackends(t *testing.T) {
	gitEnv(t)
	st := testStore(t)
	for _, g := range []string{"Home", "Work", "Side", "Other"} {
		if err := st.AddMyGroup(g); err != nil {
			t.Fatal(err)
		}
	}
	repoA := filepath.Join(t.TempDir(), "a")
	repoB := filepath.Join(t.TempDir(), "b")
	for g, repo := range map[string]string{"Work": repoA, "Side": repoA, "Other": repoB} {
		if err := st.SetTodoBackend(g, todoBackend{Git: repo}); err != nil {
			t.Fatal(err)
		}
	}
	repoA, _ = filepath.EvalSymlinks(repoA)
	repoB, _ = filepath.EvalSymlinks(repoB)

	task := Todo{
		ID: "t", Mine: true, Text: "renew the passport", Detail: "## Plan\n- photos\n",
		ShowAt: time.Date(2030, 1, 2, 9, 0, 0, 0, time.UTC), Started: true,
		CreatedAt: time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC),
	}
	err := st.AddTodos([]Todo{task, {ID: "w", Mine: true, Group: "Work", Text: "stays in Work"}})
	if err != nil {
		t.Fatal(err)
	}

	commitsIn := map[string]int{repoA: len(commits(t, repoA)), repoB: len(commits(t, repoB))}
	steps := []struct {
		to      string
		commits []string // repos that should gain a commit
	}{
		{"Home", nil},                     // local to local
		{"Work", []string{repoA}},         // local to git
		{"Side", []string{repoA}},         // git to git, same repo
		{"Other", []string{repoA, repoB}}, // git to git, other repo
		{"", []string{repoB}},             // git to local
	}
	for _, s := range steps {
		n, err := st.MoveTodo("t", s.to, "")
		if err != nil || n != 1 {
			t.Fatalf("moving to %q: %d %v", s.to, n, err)
		}
		ts, _ := st.Todos()
		i := slices.IndexFunc(ts, func(x Todo) bool { return x.ID == "t" })
		want := task
		want.Group = s.to
		if got, _ := json.Marshal(ts[i]); string(got) != string(mustJSON(t, want)) {
			t.Errorf("after the move to %q the task should only change list:\n got %s\nwant %s", s.to, got, mustJSON(t, want))
		}
		// The task is in exactly one place: its list's file, or the session.
		for _, g := range []string{"Work", "Side"} {
			_, inRepo, _, _ := readListTree(repoA, g)
			if has := slices.ContainsFunc(inRepo, func(x Todo) bool { return x.ID == "t" }); has != (g == s.to) {
				t.Errorf("after the move to %q, %s.json holding it is %v", s.to, g, has)
			}
		}
		_, inB, _, _ := readListTree(repoB, "Other")
		if has := slices.ContainsFunc(inB, func(x Todo) bool { return x.ID == "t" }); has != (s.to == "Other") {
			t.Errorf("after the move to %q, Other.json holding it is %v", s.to, has)
		}
		var local []Todo
		readJSON(st.todosPath(), &local)
		if has := slices.ContainsFunc(local, func(x Todo) bool { return x.ID == "t" }); has != (s.to == "" || s.to == "Home") {
			t.Errorf("after the move to %q, the session holding it is %v", s.to, has)
		}
		// The same-repo move commits both lists in one go.
		if cs := commits(t, repoA); s.to == "Side" && !strings.HasPrefix(cs[0], "rex-sidekick: update Side, Work (") {
			t.Errorf("the move from Work to Side should commit both files together, got %q", cs[0])
		}
		for _, repo := range []string{repoA, repoB} {
			if slices.Contains(s.commits, repo) {
				commitsIn[repo]++
			}
			if got := len(commits(t, repo)); got != commitsIn[repo] {
				t.Errorf("after the move to %q, %s should have %d commits, got %q", s.to, repo, commitsIn[repo], commits(t, repo))
			}
			if out := run(t, repo, "git", "status", "--porcelain"); out != "" {
				t.Errorf("after the move to %q, %s should have nothing left to commit, got %q", s.to, repo, out)
			}
		}
	}
	// A list the move emptied keeps an empty file.
	if b, _ := os.ReadFile(filepath.Join(repoB, "other", "data.json")); !strings.Contains(string(b), `"todos": []`) {
		t.Errorf("Other.json should be left empty, got:\n%s", b)
	}
	ts, _ := st.Todos()
	if i := slices.IndexFunc(ts, func(x Todo) bool { return x.ID == "w" }); ts[i].Group != "Work" {
		t.Errorf("the other task in Work shouldn't move, got %+v", ts[i])
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestMoveTodoBringsDependents(t *testing.T) {
	st := testStore(t)
	if err := st.AddMyGroup("Home"); err != nil {
		t.Fatal(err)
	}
	err := st.AddTodos([]Todo{
		{ID: "a", Mine: true, Text: "pick a date"},
		{ID: "b", Mine: true, Text: "book the venue", After: "a"},
		{ID: "c", Mine: true, Text: "send invites", After: "b"},
		{ID: "d", Mine: true, Text: "unrelated"},
		{ID: "h", Mine: true, Group: "Home", Text: "already home"},
		{ID: "x", List: "agent", Text: "an agent's task"},
	})
	if err != nil {
		t.Fatal(err)
	}
	n, err := st.MoveTodo("b", "Home", "")
	if err != nil || n != 2 {
		t.Fatalf("b and c should move, got %d %v", n, err)
	}
	ts, _ := st.Todos()
	byID := map[string]Todo{}
	for _, x := range ts {
		byID[x.ID] = x
	}
	if byID["a"].Group != "" || byID["d"].Group != "" || byID["b"].Group != "Home" || byID["c"].Group != "Home" {
		t.Errorf("only b and the task waiting on it should move, got %+v", ts)
	}
	if byID["b"].After != "" || byID["c"].After != "b" {
		t.Errorf("b should stop waiting on a, and c keep waiting on b, got %+v %+v", byID["b"], byID["c"])
	}
	// The moved tasks go to the end of Home.
	_, groups := myGroups(ts)
	if ids := todoIDs(groups["Home"]); !slices.Equal(ids, []string{"h", "b", "c"}) {
		t.Errorf("Home should end with the moved tasks, got %v", ids)
	}

	if _, err := st.MoveTodo("x", "Home", ""); err == nil {
		t.Error("an agent's task shouldn't move")
	}
	if _, err := st.MoveTodo("a", "Nowhere", ""); err == nil {
		t.Error("a move to a list that doesn't exist should fail")
	}
	if _, err := st.MoveTodo("gone", "Home", ""); err == nil {
		t.Error("a move of a task that doesn't exist should fail")
	}
	if n, err := st.MoveTodo("h", "Home", ""); err != nil || n != 0 {
		t.Errorf("a move to the task's own list should do nothing, got %d %v", n, err)
	}
}

func TestMoveTodoNoCommitForSameList(t *testing.T) {
	gitEnv(t)
	st := testStore(t)
	if err := st.AddMyGroup("Work"); err != nil {
		t.Fatal(err)
	}
	if err := st.AddTodos([]Todo{{ID: "w", Mine: true, Group: "Work", Text: "ship"}}); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(t.TempDir(), "todos")
	if err := st.SetTodoBackend("Work", todoBackend{Git: repo}); err != nil {
		t.Fatal(err)
	}
	before := len(commits(t, repo))
	if _, err := st.MoveTodo("w", "Work", ""); err != nil {
		t.Fatal(err)
	}
	if got := len(commits(t, repo)); got != before {
		t.Errorf("moving a task to its own list shouldn't commit, went from %d to %d", before, got)
	}
}

func TestMoveTodoPicker(t *testing.T) {
	gitEnv(t)
	st := testStore(t)
	for _, g := range []string{"Errands", "Work"} {
		if err := st.AddMyGroup(g); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SetTodoBackend("Work", todoBackend{Git: filepath.Join(t.TempDir(), "todos")}); err != nil {
		t.Fatal(err)
	}
	err := st.AddTodos([]Todo{
		{ID: "c", Mine: true, Text: "call the dentist"},
		{ID: "a", List: "api-agent", Text: "Rotate the key"},
	})
	if err != nil {
		t.Fatal(err)
	}
	m := todoModel(t, st)

	selectTask(t, m, "a")
	m.Update(key("m"))
	if m.todoMove != nil || !strings.Contains(m.errMsg, "Only your own tasks") {
		t.Errorf("m on an agent's task should only say why not, got %+v %q", m.todoMove, m.errMsg)
	}

	selectTask(t, m, "c")
	if v := m.View(); !strings.Contains(v, "m move") {
		t.Errorf("the footer should offer m on my own task:\n%s", v)
	}
	m.Update(key("m"))
	if m.todoMove == nil {
		t.Fatal("m should open the picker")
	}
	v := m.View()
	if !strings.Contains(v, "Move to:") || !strings.Contains(v, "› Errands") || !strings.Contains(v, "Work · git") {
		t.Errorf("the picker should list my other lists, git ones marked, with the first picked:\n%s", v)
	}
	if strings.Contains(v, "  My list\n") || len(m.todoMove.dests) != 2 {
		t.Errorf("the picker shouldn't offer the task's own list, got %+v", m.todoMove.dests)
	}
	if !strings.Contains(v, "esc cancel") {
		t.Errorf("the footer should say how to cancel:\n%s", v)
	}

	m.Update(key("esc"))
	if m.todoMove != nil {
		t.Fatal("esc should close the picker")
	}
	if ts, _ := st.Todos(); ts[0].Group != "" {
		t.Fatalf("esc shouldn't move the task, got %+v", ts[0])
	}

	m.Update(key("m"))
	m.Update(key("j"))
	m.Update(key("enter"))
	ts, _ := st.Todos()
	i := slices.IndexFunc(ts, func(x Todo) bool { return x.ID == "c" })
	if m.todoMove != nil || ts[i].Group != "Work" {
		t.Fatalf("enter should move the task to Work, got %+v", ts[i])
	}
	if got, ok := m.selectedTodo(); !ok || got.ID != "c" || got.Group != "Work" {
		t.Errorf("the cursor should follow the task to Work, got %+v", got)
	}
	if !strings.Contains(m.errMsg, "Moved it to Work") {
		t.Errorf("the move should say where it went, got %q", m.errMsg)
	}

	// The picker closes if its task leaves the tab, such as when someone
	// snoozes it from the CLI.
	m.Update(key("m"))
	if err := st.UpdateTodo("c", func(x *Todo, _ []Todo) error { x.ShowAt = time.Now().Add(time.Hour); return nil }); err != nil {
		t.Fatal(err)
	}
	m.reloadTodos()
	if m.todoMove != nil {
		t.Error("the picker should close when its task leaves the tab")
	}
	if err := st.UpdateTodo("c", func(x *Todo, _ []Todo) error { x.ShowAt = time.Time{}; return nil }); err != nil {
		t.Fatal(err)
	}
	m.reloadTodos()
	selectTask(t, m, "c")
	m.Update(key("m"))
	if _, err := st.RemoveTodos(func(x Todo) bool { return x.ID == "c" }); err != nil {
		t.Fatal(err)
	}
	m.reloadTodos()
	if m.todoMove != nil {
		t.Error("the picker should close when its task is removed")
	}
}

func TestMoveTodoPickerNeedsAnotherList(t *testing.T) {
	st := testStore(t)
	if err := st.AddTodos([]Todo{{ID: "c", Mine: true, Text: "call the dentist"}}); err != nil {
		t.Fatal(err)
	}
	m := todoModel(t, st)
	selectTask(t, m, "c")
	m.Update(key("m"))
	if m.todoMove != nil || !strings.Contains(m.errMsg, "New group") {
		t.Errorf("with one list, m should point at + New group, got %+v %q", m.todoMove, m.errMsg)
	}
}

func TestTodoMoveCLI(t *testing.T) {
	gitEnv(t)
	st := testStore(t)
	if err := st.AddMyGroup("Work"); err != nil {
		t.Fatal(err)
	}
	if err := st.AddTodos([]Todo{{ID: "c", Mine: true, Text: "call the dentist"}}); err != nil {
		t.Fatal(err)
	}
	if err := cmdTodo([]string{"move", "--session", "session:test", "c", "Work"}); err != nil {
		t.Fatal(err)
	}
	if ts, _ := st.Todos(); ts[0].Group != "Work" {
		t.Fatalf("todo move should move the task to Work, got %+v", ts[0])
	}
	// todo ls names it "My list: Work".
	if err := cmdTodo([]string{"move", "--session", "session:test", "c", "my list"}); err != nil {
		t.Fatal(err)
	}
	if err := cmdTodo([]string{"move", "--session", "session:test", "c", "My list: Work"}); err != nil {
		t.Fatal(err)
	}
	if ts, _ := st.Todos(); ts[0].Group != "Work" {
		t.Fatalf("todo ls's name for Work should work too, got %+v", ts[0])
	}
	if err := cmdTodo([]string{"move", "--session", "session:test", "c", "my list"}); err != nil {
		t.Fatal(err)
	}
	if ts, _ := st.Todos(); ts[0].Group != "" {
		t.Fatalf("\"my list\" should mean My list, got %+v", ts[0])
	}
	err := cmdTodo([]string{"move", "--session", "session:test", "c", "Nowhere"})
	if err == nil || !strings.Contains(err.Error(), "My list, Work") {
		t.Errorf("an unknown list should name the ones there are, got %v", err)
	}
	if err := cmdTodo([]string{"move", "--session", "session:test", "c"}); err == nil {
		t.Error("todo move without a list should fail")
	}
}
