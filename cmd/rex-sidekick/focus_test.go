package main

import (
	"errors"
	"slices"
	"testing"
	"time"
)

const (
	focusSession = "session:00000000-0000-7000-8000-000000000001"
	focusApp     = "client:00000000-0000-7000-8000-000000000002"
)

// The fixture is real rex client ls output: the app, a client with no info
// or sessions, and the rex CLI.
func TestAppClientsFromRexClientLs(t *testing.T) {
	out := []byte(readFixture(t, "rex_client_ls.json"))
	if got := appClients(out, focusSession); !slices.Equal(got, []string{focusApp}) {
		t.Errorf("appClients = %v, want [%s]", got, focusApp)
	}
	if got := appClients(out, "session:other"); got != nil {
		t.Errorf("appClients for a session no app shows = %v, want none", got)
	}
}

func TestAppClients(t *testing.T) {
	out := []byte(`{"clients": [
		{"client_id": "client:a", "info": {"kind": "app"}, "session_ids": ["session:x", "session:y"]},
		{"client_id": "client:b", "info": {"kind": "app"}, "session_ids": ["session:z"]},
		{"client_id": "client:c", "info": {"kind": "cli"}, "session_ids": ["session:y"]},
		{"client_id": "client:d", "info": {"kind": "app"}, "session_ids": ["session:y"]},
		{"client_id": "client:e"}
	]}`)
	if got, want := appClients(out, "session:y"), []string{"client:a", "client:d"}; !slices.Equal(got, want) {
		t.Errorf("appClients = %v, want %v", got, want)
	}
	if got := appClients([]byte("not json"), "session:y"); got != nil {
		t.Errorf("appClients of bad output = %v, want none", got)
	}
}

func TestFocusCommands(t *testing.T) {
	focus := []string{"block", "focus", "-s", "session:s", "block:b"}
	cases := []struct {
		name    string
		window  string
		clients []string
		want    [][]string
	}{
		{"no app shows the session", "window:w", nil, [][]string{focus}},
		{"tab unknown", "", []string{"client:a"}, [][]string{focus}},
		{"two apps", "window:w", []string{"client:a", "client:b"}, [][]string{
			focus,
			{"do", "-C", "client:a", "session.select", "session_id=session:s", "window_id=window:w"},
			{"do", "-C", "client:b", "session.select", "session_id=session:s", "window_id=window:w"},
		}},
	}
	for _, c := range cases {
		got := focusCommands("session:s", c.window, "block:b", c.clients)
		if !slices.EqualFunc(got, c.want, slices.Equal) {
			t.Errorf("%s: focusCommands = %q, want %q", c.name, got, c.want)
		}
	}
}

// fakeRex stands in for rex. The block is in tab window:w, and client ls
// prints clients, or fails when clients is empty. It records the commands
// that change something.
func fakeRex(t *testing.T, clients string) *[][]string {
	t.Helper()
	var ran [][]string
	old := rexCmd
	t.Cleanup(func() { rexCmd = old })
	rexCmd = func(args ...string) ([]byte, error) {
		switch args[0] + " " + args[1] {
		case "block inspect":
			return []byte(`{"block": {"block_id": "` + args[4] + `", "window_id": "window:w"}}`), nil
		case "client ls":
			if clients == "" {
				return []byte("no server"), errors.New("exit status 1")
			}
			return []byte(clients), nil
		}
		ran = append(ran, args)
		return nil, nil
	}
	return &ran
}

const testApp = `{"clients": [{"client_id": "client:app", "info": {"kind": "app"}, "session_ids": ["session:test"]}]}`

func TestFocusingARunningCommandSwitchesTheAppsTab(t *testing.T) {
	st := testStore(t)
	if err := st.AddQuestion(Question{ID: "q-run", Text: "Run it?", Options: []string{runYes, runNo}, Command: "make migrate", RunBlock: "block:run", AskedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	ran := fakeRex(t, testApp)
	m := newModel(st, time.Minute)
	m.width, m.height = 60, 40
	m.Init()
	m.Update(key("enter"))
	want := [][]string{
		{"block", "focus", "-s", "session:test", "block:run"},
		{"do", "-C", "client:app", "session.select", "session_id=session:test", "window_id=window:w"},
	}
	if !slices.EqualFunc(*ran, want, slices.Equal) {
		t.Errorf("enter on a running command ran %q, want %q", *ran, want)
	}
}

func TestFocusingAWorkerSwitchesTheAppsTab(t *testing.T) {
	st := testStore(t)
	if err := st.AddWorker(Worker{Name: "fix-login", Window: "window:w", Block: "block:b1"}); err != nil {
		t.Fatal(err)
	}
	m := newModel(st, time.Minute)
	m.width, m.height = 60, 40
	m.Init()
	m.Update(key("4"))
	for _, k := range []string{"enter", "f", " "} {
		ran := fakeRex(t, testApp)
		_, cmd := m.Update(key(k))
		if cmd == nil {
			t.Fatalf("%q on a worker should focus it", k)
		}
		if msg, ok := cmd().(cmdErrMsg); !ok || msg.err != nil {
			t.Errorf("%q: focusing should succeed, got %+v", k, msg)
		}
		want := [][]string{
			{"block", "focus", "-s", "session:test", "block:b1"},
			{"do", "-C", "client:app", "session.select", "session_id=session:test", "window_id=window:w"},
		}
		if !slices.EqualFunc(*ran, want, slices.Equal) {
			t.Errorf("%q on a worker ran %q, want %q", k, *ran, want)
		}
	}
}

// When rex can't list clients, focusing falls back to focusing the block.
func TestFocusPaneWithoutClients(t *testing.T) {
	ran := fakeRex(t, "")
	if err := focusPane("session:test", "block:b1"); err != nil {
		t.Fatal(err)
	}
	if want := [][]string{{"block", "focus", "-s", "session:test", "block:b1"}}; !slices.EqualFunc(*ran, want, slices.Equal) {
		t.Errorf("ran %q, want %q", *ran, want)
	}
}
