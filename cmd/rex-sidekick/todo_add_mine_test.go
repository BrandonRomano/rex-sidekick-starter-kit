package main

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// addMine runs todo add --mine for the test session, with no pane.
func addMine(args ...string) error {
	return cmdTodo(append([]string{"add", "--session", "session:test", "--block", "", "--no-open"}, args...))
}

func todoByText(t *testing.T, st *Store, text string) Todo {
	t.Helper()
	ts, err := st.Todos()
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(ts, func(x Todo) bool { return x.Text == text })
	if i < 0 {
		t.Fatalf("no task %q in %+v", text, ts)
	}
	return ts[i]
}

func TestTodoAddMineToMyList(t *testing.T) {
	st := testStore(t)
	if err := addMine("--mine", "My list", "--detail", "## Plan", "call the dentist"); err != nil {
		t.Fatal(err)
	}
	got := todoByText(t, st, "call the dentist")
	if !got.Mine || got.Group != "" || got.Sub != "" || got.List != "" || got.NotifyBlock != "" || got.Detail != "## Plan" {
		t.Errorf("want the user's own task on My list, got %+v", got)
	}
}

func TestTodoAddMineToGitList(t *testing.T) {
	gitEnv(t)
	st := testStore(t)
	if err := st.AddMyGroup("Work"); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(t.TempDir(), "repo")
	if err := st.SetTodoBackend("Work", todoBackend{Git: repo}); err != nil {
		t.Fatal(err)
	}
	repo, _ = filepath.EvalSymlinks(repo)
	before := len(commits(t, repo))

	if err := addMine("--mine", "Work", "--chain", "write the plan", "ship it"); err != nil {
		t.Fatal(err)
	}
	first, second := todoByText(t, st, "write the plan"), todoByText(t, st, "ship it")
	if !first.Mine || first.Group != "Work" || second.After != first.ID {
		t.Errorf("want two chained tasks on Work, got %+v and %+v", first, second)
	}
	_, inRepo, _, err := readListTree(repo, "Work")
	if err != nil {
		t.Fatal(err)
	}
	if len(inRepo) != 2 {
		t.Errorf("the repo should hold both tasks, got %+v", inRepo)
	}
	if n := len(commits(t, repo)); n != before+1 {
		t.Errorf("adding should make one commit, got %d new", n-before)
	}
}

func TestTodoAddMineToSubGroup(t *testing.T) {
	st := testStore(t)
	if err := st.AddMyGroup("Work"); err != nil {
		t.Fatal(err)
	}
	if err := st.AddSubGroup("Work", "", "#launch"); err != nil {
		t.Fatal(err)
	}
	if err := addMine("--mine", "Work / #launch", "--show-at", "tomorrow", "group the email logs"); err != nil {
		t.Fatal(err)
	}
	got := todoByText(t, st, "group the email logs")
	if !got.Mine || got.Group != "Work" || got.Sub != "#launch" || got.ShowAt.IsZero() {
		t.Errorf("want the task in Work / #launch, hidden until tomorrow, got %+v", got)
	}

	// It can wait on a task in the same place, but not on one elsewhere.
	if err := addMine("--mine", "Work / #launch", "--after", got.ID, "then wipe"); err != nil {
		t.Error(err)
	}
	if err := addMine("--mine", "Work", "--after", got.ID, "elsewhere"); err == nil {
		t.Error("--after a task in another list should fail")
	}
}

func TestTodoAddMineRejects(t *testing.T) {
	st := testStore(t)
	if err := st.AddMyGroup("Work"); err != nil {
		t.Fatal(err)
	}
	err := addMine("--mine", "Nowhere", "a task")
	if err == nil || !strings.Contains(err.Error(), "My list, Work") {
		t.Errorf("an unknown list should name the ones there are, got %v", err)
	}
	if err := addMine("--mine", "Work / Nowhere", "a task"); err == nil {
		t.Error("an unknown sub-group should fail")
	}
	if err := addMine("--mine", "Work", "--list", "Mine", "a task"); err == nil {
		t.Error("--mine with --list should fail")
	}
	if err := addMine("--mine", "Work", "--for-user", "a task"); err == nil {
		t.Error("--mine with --for-user should fail")
	}
	if ts, _ := st.Todos(); len(ts) != 0 {
		t.Errorf("nothing should be added, got %+v", ts)
	}
}
