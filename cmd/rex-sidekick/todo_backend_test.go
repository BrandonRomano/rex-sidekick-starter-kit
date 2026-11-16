package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// gitEnv gives git an identity and keeps the user's config out of tests.
func gitEnv(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, k := range []string{"GIT_AUTHOR_NAME", "GIT_COMMITTER_NAME"} {
		t.Setenv(k, "Test")
	}
	for _, k := range []string{"GIT_AUTHOR_EMAIL", "GIT_COMMITTER_EMAIL"} {
		t.Setenv(k, "test@example.com")
	}
}

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %v\n%s", args, err, out)
	}
	return string(out)
}

func commits(t *testing.T, repo string) []string {
	t.Helper()
	return strings.Split(strings.TrimSpace(run(t, repo, "git", "log", "--format=%s")), "\n")
}

func TestGitBackendKeepsListInRepo(t *testing.T) {
	gitEnv(t)
	st := testStore(t)
	if err := st.AddMyGroup("Work"); err != nil {
		t.Fatal(err)
	}
	err := st.AddTodos([]Todo{
		{ID: "m", Mine: true, Text: "call the dentist"},
		{ID: "w1", Mine: true, Group: "Work", Text: "ship the backend"},
		{ID: "w2", Mine: true, Group: "Work", Text: "write the docs", After: "w1"},
		{ID: "a", List: "agent", Text: "an agent's task"},
	})
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(t.TempDir(), "todos")
	if err := st.SetTodoBackend("Work", todoBackend{Git: repo}); err != nil {
		t.Fatal(err)
	}
	repo, _ = filepath.EvalSymlinks(repo)

	// The list's tasks moved into the repo, and the rest stayed.
	var local []Todo
	if err := readJSON(st.todosPath(), &local); err != nil {
		t.Fatal(err)
	}
	if ids := todoIDs(local); !slices.Equal(ids, []string{"m", "a"}) {
		t.Errorf("the session should keep only the other tasks, got %v", ids)
	}
	_, inRepo, _, err := readListTree(repo, "Work")
	if err != nil || !slices.Equal(todoIDs(inRepo), []string{"w1", "w2"}) || inRepo[1].After != "w1" {
		t.Fatalf("work/data.json should hold the list's tasks, got %+v %v", inRepo, err)
	}
	if b, _ := os.ReadFile(filepath.Join(repo, "work", "data.json")); strings.Contains(string(b), `"group"`) {
		t.Errorf("the file shouldn't name its list on every task:\n%s", b)
	}
	ts, _ := st.Todos()
	if len(ts) != 4 || ts[2].Group != "Work" || !ts[2].Mine {
		t.Fatalf("the tab should see every task, got %+v", ts)
	}
	if cs := commits(t, repo); len(cs) != 1 || !strings.HasPrefix(cs[0], "rex-sidekick: update Work (20") {
		t.Fatalf("moving the list should commit it with the time, got %q", cs)
	}

	// Every change commits, and nothing else in the repo goes along.
	os.WriteFile(filepath.Join(repo, "notes.txt"), []byte("mine"), 0o644)
	if err := st.UpdateTodo("w1", func(x *Todo, _ []Todo) error { x.Done = true; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RemoveTodos(func(x Todo) bool { return x.ID == "w2" }); err != nil {
		t.Fatal(err)
	}
	if err := st.AddTodos([]Todo{{ID: "w3", Mine: true, Group: "Work", Text: "review"}}); err != nil {
		t.Fatal(err)
	}
	if cs := commits(t, repo); len(cs) != 4 {
		t.Errorf("each change should make a commit, got %q", cs)
	}
	if out := run(t, repo, "git", "status", "--porcelain"); out != "?? notes.txt\n" {
		t.Errorf("only the list's file should be committed, got %q", out)
	}
	// A change to another list doesn't touch the repo.
	if err := st.UpdateTodo("m", func(x *Todo, _ []Todo) error { x.Done = true; return nil }); err != nil {
		t.Fatal(err)
	}
	if cs := commits(t, repo); len(cs) != 4 {
		t.Errorf("a change elsewhere shouldn't commit, got %q", cs)
	}

	// Another session picks the list up from the repo, with its own tasks
	// for a list of that name joining it.
	t2, err := OpenStore("session:other")
	if err != nil {
		t.Fatal(err)
	}
	if err := t2.AddTodos([]Todo{{ID: "o", Mine: true, Group: "Work", Text: "from the other session"}}); err != nil {
		t.Fatal(err)
	}
	if err := t2.SetTodoBackend("Work", todoBackend{Git: repo}); err != nil {
		t.Fatal(err)
	}
	if gs, _ := t2.MyGroups(); !slices.Equal(gs, []string{"Work"}) {
		t.Errorf("the other session should get the list, got %v", gs)
	}
	ts, _ = st.Todos()
	if ids := todoIDs(ts); !slices.Equal(ids, []string{"m", "a", "w1", "w3", "o"}) {
		t.Errorf("both sessions should share the list, got %v", ids)
	}

	// Renaming moves the folder. Removing the list takes it out of the repo.
	if err := st.RenameMyGroup("Work", "Job"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repo, "work")); !os.IsNotExist(err) {
		t.Error("renaming should remove the work folder")
	}
	if _, inRepo, _, _ := readListTree(repo, "Job"); len(inRepo) != 3 {
		t.Errorf("job/data.json should hold the list, got %+v", inRepo)
	}
	if out := run(t, repo, "git", "status", "--porcelain"); out != "?? notes.txt\n" {
		t.Errorf("the rename should be committed, got %q", out)
	}
	if err := st.RemoveMyGroup("Job"); err != nil {
		t.Fatal(err)
	}
	if out := run(t, repo, "git", "ls-files"); out != "" {
		t.Errorf("removing the list should take its file out, got %q", out)
	}
	if backs, _ := st.TodoBackends(); len(backs) != 0 {
		t.Errorf("a removed list should forget its repo, got %v", backs)
	}
}

func todoIDs(ts []Todo) []string {
	var ids []string
	for _, t := range ts {
		ids = append(ids, t.ID)
	}
	return ids
}

func TestGitBackendBackToSession(t *testing.T) {
	gitEnv(t)
	st := testStore(t)
	repo := filepath.Join(t.TempDir(), "todos")
	if err := st.SetTodoBackend("Work", todoBackend{Git: repo}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddTodos([]Todo{{ID: "w", Mine: true, Group: "Work", Text: "ship it"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetTodoBackend("Work", todoBackend{}); err != nil {
		t.Fatal(err)
	}
	var local []Todo
	readJSON(st.todosPath(), &local)
	if len(local) != 1 || local[0].Group != "Work" {
		t.Errorf("the tasks should come back to the session, got %+v", local)
	}
	if _, inRepo, _, _ := readListTree(repo, "Work"); len(inRepo) != 1 {
		t.Errorf("the repo's file should stay for other sessions, got %+v", inRepo)
	}

	if err := st.SetTodoBackend("", todoBackend{Git: repo}); err == nil {
		t.Error("My list should stay with the session")
	}
	full := t.TempDir()
	os.WriteFile(filepath.Join(full, "x"), nil, 0o644)
	if err := st.SetTodoBackend("Work", todoBackend{Git: full}); err == nil || !strings.Contains(err.Error(), "isn't a git repo") {
		t.Errorf("a folder with files that isn't a repo should be refused, got %v", err)
	}
}

func TestSyncTodosPullsAndPushes(t *testing.T) {
	gitEnv(t)
	dir := t.TempDir()
	remote := filepath.Join(dir, "remote.git")
	run(t, dir, "git", "init", "-q", "--bare", "-b", "main", remote)
	seed := filepath.Join(dir, "seed")
	run(t, dir, "git", "clone", "-q", remote, seed)
	run(t, seed, "git", "commit", "-q", "--allow-empty", "-m", "start")
	run(t, seed, "git", "push", "-q", "origin", "HEAD:main")
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	run(t, dir, "git", "clone", "-q", remote, a)
	run(t, dir, "git", "clone", "-q", remote, b)

	st := testStore(t)
	if err := st.SetTodoBackend("Work", todoBackend{Git: a}); err != nil {
		t.Fatal(err)
	}
	if err := st.SyncTodos(); err != nil {
		t.Fatal(err)
	}
	other, _ := OpenStore("session:other")
	if err := other.SetTodoBackend("Work", todoBackend{Git: b}); err != nil {
		t.Fatal(err)
	}
	if err := other.SyncTodos(); err != nil {
		t.Fatal(err)
	}
	if err := other.AddTodos([]Todo{{ID: "w", Mine: true, Group: "Work", Text: "from b"}}); err != nil {
		t.Fatal(err)
	}
	if err := other.SyncTodos(); err != nil {
		t.Fatal(err)
	}
	if err := st.SyncTodos(); err != nil {
		t.Fatal(err)
	}
	if ts, _ := st.Todos(); len(ts) != 1 || ts[0].Text != "from b" {
		t.Fatalf("sync should pull the other clone's task, got %+v", ts)
	}

	// Both change the same task: the pull can't rebase, and the repo stays
	// as it was.
	other.UpdateTodo("w", func(x *Todo, _ []Todo) error { x.Text = "b's text"; return nil })
	if err := other.SyncTodos(); err != nil {
		t.Fatal(err)
	}
	st.UpdateTodo("w", func(x *Todo, _ []Todo) error { x.Text = "a's text"; return nil })
	err := st.SyncTodos()
	if err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("a conflict should be reported, got %v", err)
	}
	if out := run(t, a, "git", "status", "--porcelain"); out != "" {
		t.Errorf("the repo should be left clean, got %q", out)
	}
	if ts, _ := st.Todos(); ts[0].Text != "a's text" {
		t.Errorf("the local change should stay, got %+v", ts)
	}
}

func TestGitBackendFromTab(t *testing.T) {
	gitEnv(t)
	st := testStore(t)
	if err := st.AddMyGroup("Work"); err != nil {
		t.Fatal(err)
	}
	m := todoModel(t, st)
	selectList(t, m, "My list")
	m.Update(key("g"))
	if m.todoInput != nil {
		t.Error("My list can't move to a repo")
	}

	selectList(t, m, "Work")
	if v := m.View(); !strings.Contains(v, "g git repo") {
		t.Errorf("the footer should offer g on a list I made:\n%s", v)
	}
	m.Update(key("g"))
	if m.todoInput == nil || m.todoInput.backendFor != "Work" {
		t.Fatal("g should open a box for the repo")
	}
	repo := filepath.Join(t.TempDir(), "todos")
	typeLine(m, repo)
	if v := m.View(); !strings.Contains(v, "Work (0) · git") {
		t.Errorf("the heading should say the list is in git:\n%s", v)
	}
	if backs, _ := st.TodoBackends(); backs["Work"].Git == "" {
		t.Errorf("the list should be in the repo, got %v", backs)
	}

	// Clearing the box moves it back.
	selectList(t, m, "Work")
	m.Update(key("g"))
	if !strings.Contains(m.todoInput.ta.Value(), "todos") {
		t.Errorf("the box should start with the repo, got %q", m.todoInput.ta.Value())
	}
	m.todoInput.ta.SetValue("")
	m.Update(key("enter"))
	if backs, _ := st.TodoBackends(); len(backs) != 0 {
		t.Errorf("an empty box should move the list back, got %v", backs)
	}
}

func TestGitBackendMovesOldFileIntoFolder(t *testing.T) {
	gitEnv(t)
	st := testStore(t)
	repo := filepath.Join(t.TempDir(), "todos")
	if err := st.SetTodoBackend("Home Renovation", todoBackend{Git: repo}); err != nil {
		t.Fatal(err)
	}
	repo, _ = filepath.EvalSymlinks(repo)
	// A file from before lists had folders, as an older build wrote it.
	os.RemoveAll(filepath.Join(repo, "home-renovation"))
	old := filepath.Join(repo, "Home Renovation.json")
	os.WriteFile(old, []byte(`{
  "name": "Home Renovation",
  "todos": [
    {
      "id": "t-1",
      "text": "Patch the drywall",
      "created_at": "2026-11-10T11:30:32-08:00"
    }
  ]
}
`), 0o644)
	run(t, repo, "git", "add", "-A")
	run(t, repo, "git", "commit", "-qm", "older build")

	ts, _ := st.Todos()
	if len(ts) != 1 || ts[0].Group != "Home Renovation" || ts[0].Text != "Patch the drywall" {
		t.Fatalf("the old file should still load, got %+v", ts)
	}
	if out := run(t, repo, "git", "status", "--porcelain"); out != "" {
		t.Errorf("reading shouldn't change the repo, got %q", out)
	}

	was, _ := os.ReadFile(old)
	// Any change saves, even one to another list.
	if err := st.AddTodos([]Todo{{ID: "m", Mine: true, Text: "elsewhere"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("the next save should take out the old file")
	}
	b, err := os.ReadFile(filepath.Join(repo, "home-renovation", "data.json"))
	if err != nil || string(b) != string(was) {
		t.Fatalf("the list should move into its folder as it was, got %s %v", b, err)
	}
	if strings.Contains(string(b), "subgroups") {
		t.Errorf("a list without sub-groups shouldn't list any:\n%s", b)
	}
	if out := run(t, repo, "git", "status", "--porcelain"); out != "" {
		t.Errorf("the move should be committed, got %q", out)
	}
	if out := run(t, repo, "git", "ls-files"); out != "home-renovation/data.json\n" {
		t.Errorf("only the folder's file should be left, got %q", out)
	}
}

func TestSubGroupsInGitRepo(t *testing.T) {
	gitEnv(t)
	st := testStore(t)
	if err := st.AddMyGroup("Home"); err != nil {
		t.Fatal(err)
	}
	for _, sg := range [][2]string{{"", "Basement"}, {"", "Attic"}, {"Basement", "Boiler"}} {
		if err := st.AddSubGroup("Home", sg[0], sg[1]); err != nil {
			t.Fatal(err)
		}
	}
	err := st.AddTodos([]Todo{
		{ID: "a", Mine: true, Group: "Home", Text: "paint"},
		{ID: "b", Mine: true, Group: "Home", Sub: "Basement", Text: "insulation"},
		{ID: "c", Mine: true, Group: "Home", Sub: "Basement/Boiler", Text: "boiler"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, subs, _ := st.TodosAndSubGroups(); !slices.Equal(subs["Home"], []string{"Basement", "Attic", "Basement/Boiler"}) {
		t.Fatalf("the session should keep the sub-groups, got %v", subs)
	}

	repo := filepath.Join(t.TempDir(), "todos")
	if err := st.SetTodoBackend("Home", todoBackend{Git: repo}); err != nil {
		t.Fatal(err)
	}
	repo, _ = filepath.EvalSymlinks(repo)
	if out := run(t, repo, "git", "ls-files"); out != "home/attic/data.json\nhome/basement/boiler/data.json\nhome/basement/data.json\nhome/data.json\n" {
		t.Fatalf("each sub-group should get a folder, got %q", out)
	}
	b, _ := os.ReadFile(filepath.Join(repo, "home", "data.json"))
	if !strings.Contains(string(b), "\"subgroups\": [\n    \"Basement\",\n    \"Attic\"\n  ]") {
		t.Errorf("the list's file should name its sub-groups in order:\n%s", b)
	}
	if b, _ := os.ReadFile(filepath.Join(repo, "home", "basement", "boiler", "data.json")); !strings.Contains(string(b), `"boiler"`) || strings.Contains(string(b), `"sub"`) {
		t.Errorf("a sub-group's file should hold its tasks, and not name the sub-group on each:\n%s", b)
	}
	var local map[string][]string
	readJSON(st.subGroupsPath(), &local)
	if len(local) != 0 {
		t.Errorf("a list in a repo keeps its sub-groups there, got %v", local)
	}

	// Another session gets the sub-groups from the repo.
	other, _ := OpenStore("session:other")
	if err := other.SetTodoBackend("Home", todoBackend{Git: repo}); err != nil {
		t.Fatal(err)
	}
	ts, subs, _ := other.TodosAndSubGroups()
	if !slices.Equal(subs["Home"], []string{"Basement", "Basement/Boiler", "Attic"}) {
		t.Errorf("the other session should see the sub-groups, got %v", subs)
	}
	if i := slices.IndexFunc(ts, func(x Todo) bool { return x.ID == "c" }); i < 0 || ts[i].Sub != "Basement/Boiler" {
		t.Errorf("a task should come back in its sub-group, got %+v", ts)
	}

	// Renaming a sub-group moves its folder, with the ones inside it.
	if err := st.RenameSubGroup("Home", "Basement", "Garage"); err != nil {
		t.Fatal(err)
	}
	if out := run(t, repo, "git", "ls-files"); out != "home/attic/data.json\nhome/data.json\nhome/garage/boiler/data.json\nhome/garage/data.json\n" {
		t.Errorf("renaming should move the folders, got %q", out)
	}
	if _, err := os.Stat(filepath.Join(repo, "home", "basement")); !os.IsNotExist(err) {
		t.Error("the old folder should be gone")
	}
	ts, _ = st.Todos()
	if i := slices.IndexFunc(ts, func(x Todo) bool { return x.ID == "c" }); ts[i].Sub != "Garage/Boiler" {
		t.Errorf("the task should move with the sub-group, got %+v", ts[i])
	}
	if err := st.RenameSubGroup("Home", "Garage", "attic!"); err != errSubGroupExists {
		t.Errorf("a name that shares a sibling's folder should be refused, got %v", err)
	}

	// Removing one takes its tasks and folders.
	n, err := st.RemoveSubGroup("Home", "Garage")
	if err != nil || n != 2 {
		t.Fatalf("removing should take both tasks, got %d %v", n, err)
	}
	if out := run(t, repo, "git", "ls-files"); out != "home/attic/data.json\nhome/data.json\n" {
		t.Errorf("removing should take out the folders, got %q", out)
	}
	if out := run(t, repo, "git", "status", "--porcelain"); out != "" {
		t.Errorf("every change should be committed, got %q", out)
	}

	// Back in the session, the sub-groups come along.
	if err := st.SetTodoBackend("Home", todoBackend{}); err != nil {
		t.Fatal(err)
	}
	readJSON(st.subGroupsPath(), &local)
	if !slices.Equal(local["Home"], []string{"Attic"}) {
		t.Errorf("the session should keep the sub-groups now, got %v", local)
	}
}

func TestSubGroupNames(t *testing.T) {
	st := testStore(t)
	if err := st.AddSubGroup("", "", "Inbox"); err == nil {
		t.Error("My list shouldn't take sub-groups")
	}
	st.AddMyGroup("Work")
	for _, bad := range []string{"", "a/b"} {
		if err := st.AddSubGroup("Work", "", bad); err == nil {
			t.Errorf("%q shouldn't be a sub-group's name", bad)
		}
	}
	if err := st.AddSubGroup("Work", "Nope", "Child"); err == nil {
		t.Error("a sub-group needs its parent to exist")
	}
	st.AddSubGroup("Work", "", "Basement")
	if err := st.AddSubGroup("Work", "", "BASEMENT"); err != errSubGroupExists {
		t.Errorf("siblings can't share a name, got %v", err)
	}
	// The same name is fine elsewhere.
	if err := st.AddSubGroup("Work", "Basement", "Basement"); err != nil {
		t.Error(err)
	}
	// Renaming the list carries its sub-groups, and removing it drops them.
	if err := st.RenameMyGroup("Work", "Job"); err != nil {
		t.Fatal(err)
	}
	if _, subs, _ := st.TodosAndSubGroups(); !slices.Equal(subs["Job"], []string{"Basement", "Basement/Basement"}) || subs["Work"] != nil {
		t.Errorf("the sub-groups should follow the list, got %v", subs)
	}
	st.RemoveMyGroup("Job")
	if _, subs, _ := st.TodosAndSubGroups(); len(subs) != 0 {
		t.Errorf("removing the list should drop its sub-groups, got %v", subs)
	}
}

func TestSlugify(t *testing.T) {
	for in, want := range map[string]string{
		"Home Renovation":  "home-renovation",
		"  Art & Sound!! ": "art-sound",
		"Café 2":           "café-2",
		"!!!":              "list",
	} {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestListFolderTaken(t *testing.T) {
	gitEnv(t)
	st := testStore(t)
	repo := filepath.Join(t.TempDir(), "todos")
	if err := st.SetTodoBackend("Work", todoBackend{Git: repo}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetTodoBackend("work!", todoBackend{Git: repo}); err == nil || !strings.Contains(err.Error(), "share a folder") {
		t.Errorf("two lists shouldn't share a folder, got %v", err)
	}
}
