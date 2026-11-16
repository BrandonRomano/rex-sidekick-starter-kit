package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
)

func readFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The fixtures are captures of real Claude Code panes.
func TestReadPane(t *testing.T) {
	cases := []struct {
		file   string
		prompt string
		busy   bool
	}{
		{"claude_permission.txt", "Bash command: touch probe-file.txt — Do you want to proceed?", false},
		{"claude_question.txt", "Color — Which color do you prefer?", false},
		{"claude_trust.txt", "Accessing workspace — Is this a project you created or one you trust?", false},
		{"claude_working.txt", "", true},
		{"claude_idle.txt", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		screen := ""
		if c.file != "" {
			screen = readFixture(t, c.file)
		}
		got := readPane(screen)
		if got.Prompt != c.prompt || got.Busy != c.busy {
			t.Errorf("%s: got %+v, want prompt %q busy %v", c.file, got, c.prompt, c.busy)
		}
	}
}

func TestViewWorkerStates(t *testing.T) {
	w := Worker{Name: "fix-login", Block: "b1", Window: "w1"}
	pr := Entry{URL: "https://github.com/acme/api/pull/7", Repo: "acme/api", Number: 7, NotifyBlock: "b1"}
	asked := []Question{{ID: "q", NotifyBlock: "b1"}}
	told := []Question{{ID: "i", NotifyBlock: "b1", Info: true}}
	prompt := paneState{Prompt: "Bash command — Do you want to proceed?"}
	done := w
	done.Done = true
	cases := []struct {
		name    string
		w       Worker
		pane    paneState
		read    bool
		qs      []Question
		watched []Entry
		want    string
	}{
		{"not read yet", w, paneState{}, false, nil, nil, stWorking},
		{"spinner", w, paneState{Busy: true}, true, nil, []Entry{pr}, stWorking},
		{"quiet", w, paneState{}, true, told, nil, stIdle},
		{"quiet with a PR", w, paneState{}, true, nil, []Entry{pr}, stPR},
		{"asked in the inbox", w, paneState{Busy: true}, true, asked, nil, stQuestion},
		{"prompt beats everything", done, prompt, true, asked, []Entry{pr}, stPrompt},
		{"done", done, paneState{}, true, nil, []Entry{pr}, stDone},
	}
	for _, c := range cases {
		if got := viewWorker(c.w, c.pane, c.read, c.qs, c.watched, nil).State; got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}

	// PRs link by the pane that registered them, or by hand.
	w.PRs = []string{"https://github.com/acme/web/pull/9"}
	ts := []Todo{{ID: "t1", NotifyBlock: "b1", Done: true}, {ID: "t2", NotifyBlock: "b1"}, {ID: "t3", NotifyBlock: "b2"}}
	v := viewWorker(w, paneState{}, true, nil, []Entry{pr}, ts)
	if len(v.Linked) != 2 || v.Linked[1].Repo != "acme/web" || v.Linked[1].Number != 9 {
		t.Errorf("linked PRs: %+v", v.Linked)
	}
	if v.Steps != 2 || v.StepsDone != 1 {
		t.Errorf("plan: %d/%d, want 1/2", v.StepsDone, v.Steps)
	}
}

func TestWorkerStore(t *testing.T) {
	st := testStore(t)
	add := func(name, window string) {
		if err := st.AddWorker(Worker{Name: name, Window: window, Block: "b-" + name, AddedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	add("a", "w1")
	add("b", "w2")
	add("a", "w3") // replaces a
	if ws, _ := st.Workers(); len(ws) != 2 || ws[0].Window != "w3" {
		t.Fatalf("adding a name again should replace it: %+v", ws)
	}
	if err := st.UpdateWorker("a", func(w *Worker) error { w.Done = true; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateWorker("nope", func(*Worker) error { return nil }); err == nil {
		t.Error("updating a missing worker should fail")
	}

	// Workers whose tab closed drop off.
	old := liveWindows
	t.Cleanup(func() { liveWindows = old })
	liveWindows = func(string) (map[string]bool, error) { return map[string]bool{"w3": true}, nil }
	ws, err := dropClosedWorkers(st)
	if err != nil || len(ws) != 1 || ws[0].Name != "a" || !ws[0].Done {
		t.Fatalf("only a's tab is open: %+v %v", ws, err)
	}
	// When Rex can't list tabs, nobody is dropped.
	liveWindows = func(string) (map[string]bool, error) { return nil, os.ErrNotExist }
	if ws, _ := dropClosedWorkers(st); len(ws) != 1 {
		t.Errorf("a failed listing shouldn't drop workers: %+v", ws)
	}
}

func TestPRURL(t *testing.T) {
	for ref, want := range map[string]string{
		"https://github.com/acme/api/pull/7/": "https://github.com/acme/api/pull/7",
		"acme/api#7":                          "https://github.com/acme/api/pull/7",
	} {
		if got, err := prURL(ref); got != want || err != nil {
			t.Errorf("prURL(%q) = %q, %v", ref, got, err)
		}
	}
	if _, err := prURL("7"); err == nil {
		t.Error("a bare number has no repo, so it should fail")
	}
}

func TestWorkersTab(t *testing.T) {
	st := testStore(t)
	for _, w := range []Worker{
		{Name: "fix-login", Window: "w1", Block: "b1", Task: "Fix the login redirect"},
		{Name: "add-banner", Window: "w2", Block: "b2", Task: "Add a banner"},
		{Name: "gone", Window: "w3", Block: "b3"},
	} {
		if err := st.AddWorker(w); err != nil {
			t.Fatal(err)
		}
	}
	m := newModel(st, time.Minute)
	m.width, m.height = 63, 40
	m.Init()
	m.Update(key("4"))
	if m.tab != tabWorkers {
		t.Fatalf("4 should open the Workers tab, got %d", m.tab)
	}

	permission := readFixture(t, "claude_permission.txt")
	read := func() workersMsg {
		return workersMsg{
			screens: map[string]string{"b1": readFixture(t, "claude_working.txt"), "b2": permission},
			live:    map[string]bool{"w1": true, "w2": true},
		}
	}
	if m.gotWorkers(read()) != nil {
		t.Error("the bell shouldn't ring on the first read of a prompt")
	}
	if ws, _ := st.Workers(); len(ws) != 2 {
		t.Errorf("the worker whose tab closed should drop off: %+v", ws)
	}
	if m.gotWorkers(read()) == nil {
		t.Error("a prompt still there on the next read should ring the bell")
	}
	if m.gotWorkers(read()) != nil {
		t.Error("the bell should ring once per prompt")
	}

	view := m.View()
	for _, want := range []string{"Waiting on a prompt (1)", "add-banner", "Bash command: touch", "Workers (1)", "fix-login", "working"} {
		if !strings.Contains(view, want) {
			t.Errorf("%q missing:\n%s", want, view)
		}
	}
	if strings.Index(view, "add-banner") > strings.Index(view, "fix-login") {
		t.Errorf("the worker on a prompt should come first:\n%s", view)
	}
	for _, line := range strings.Split(view, "\n") {
		if lipgloss.Width(line) > m.width {
			t.Errorf("line is %d wide: %q", lipgloss.Width(line), line)
		}
	}
	if v, ok := m.selectedWorker(); !ok || v.Name != "add-banner" {
		t.Errorf("the cursor should start on add-banner, got %+v", v)
	}

	// x takes the selected worker off the tab.
	m.Update(key("x"))
	if ws, _ := st.Workers(); len(ws) != 1 || ws[0].Name != "fix-login" {
		t.Errorf("x should remove add-banner: %+v", ws)
	}
	if m.blockedWorkers() != 0 {
		t.Error("with add-banner gone, nobody waits on a prompt")
	}
}
