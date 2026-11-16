package main

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	t.Setenv("REX_SIDEKICK_STATE_DIR", t.TempDir())
	st, err := OpenStore("session:test")
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func key(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "shift+tab":
		return tea.KeyMsg{Type: tea.KeyShiftTab}
	case "space":
		return tea.KeyMsg{Type: tea.KeySpace, Runes: []rune(" ")}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

// inboxModel loads q into a sidekick and opens it for answering.
func inboxModel(t *testing.T, st *Store, q Question) *model {
	t.Helper()
	m := newModel(st, time.Minute)
	m.width, m.height = 63, 40
	m.Init()
	if m.tab != tabInbox || len(m.questions) != 1 {
		t.Fatalf("want the inbox open with 1 question, got tab %d with %d", m.tab, len(m.questions))
	}
	m.Update(key("enter"))
	if m.ans == nil || m.ans.q.ID != q.ID {
		t.Fatalf("enter should open the question")
	}
	return m
}

func TestAskGetsPickedOption(t *testing.T) {
	st := testStore(t)
	q := Question{ID: "q-1", From: "api-agent", Text: "Squash or rebase?", Options: []string{"Squash", "Rebase"}}
	got := make(chan string, 1)
	go func() {
		ans, err := st.Ask(q, 10*time.Millisecond)
		if err != nil {
			got <- err.Error()
			return
		}
		got <- ans.String()
	}()
	for deadline := time.Now().Add(2 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if qs, _ := st.Questions(); len(qs) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("question never reached the inbox")
		}
	}

	m := inboxModel(t, st, q)
	if view := m.View(); !strings.Contains(view, "api-agent") || !strings.Contains(view, "2. Rebase") {
		t.Errorf("answer view should show who asked and the options:\n%s", view)
	}
	m.Update(key("2"))
	m.Update(key("enter"))

	select {
	case s := <-got:
		if s != "Answer: Rebase" {
			t.Errorf("got %q", s)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ask never got the answer")
	}
	if qs, _ := st.Questions(); len(qs) != 0 {
		t.Errorf("ask should remove the answered question, left %v", qs)
	}
}

func TestMultiAnswerWithTypedText(t *testing.T) {
	st := testStore(t)
	q := Question{ID: "q-2", Text: "Which checks?", Options: []string{"lint", "test", "build"}, Multi: true}
	if err := st.AddQuestion(q); err != nil {
		t.Fatal(err)
	}
	m := inboxModel(t, st, q)
	m.Update(key("space")) // lint
	m.Update(key("3"))     // build
	m.Update(key("down"))  // Other row
	for _, r := range "also e2e" {
		m.Update(key(string(r)))
	}
	m.Update(key("enter"))

	qs, _ := st.Questions()
	if len(qs) != 1 || qs[0].Answer == nil {
		t.Fatalf("want the answer saved for the agent, got %+v", qs)
	}
	if got, want := qs[0].Answer.String(), `Answer: lint; build; typed answer: "also e2e"`; got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	// Nobody is waiting on it, so it goes to the agent's pane instead.
	orphans, err := st.TakeOrphanAnswers()
	if err != nil || len(orphans) != 1 {
		t.Fatalf("want 1 orphan answer, got %v, %v", orphans, err)
	}
	notice := FormatAnswerNotice(orphans[0])
	if !strings.HasPrefix(notice, `[sidekick] answer to "Which checks?" (q-2): Answer: lint; build`) {
		t.Errorf("notice: %q", notice)
	}
}

func TestDismissQuestion(t *testing.T) {
	st := testStore(t)
	if err := st.AddQuestion(Question{ID: "q-3", Text: "Deploy now?", Options: []string{"Yes", "No"}}); err != nil {
		t.Fatal(err)
	}
	m := newModel(st, time.Minute)
	m.width, m.height = 63, 40
	m.Init()
	m.Update(key("x"))
	qs, _ := st.Questions()
	if len(qs) != 1 || qs[0].Answer == nil || !qs[0].Answer.Dismissed {
		t.Fatalf("x should dismiss the question, got %+v", qs)
	}
	if len(m.questions) != 0 {
		t.Errorf("a dismissed question should leave the inbox")
	}
}

func TestAnswerViewFitsWidth(t *testing.T) {
	st := testStore(t)
	q := Question{ID: "q-4", From: "a-very-long-agent-tab-name", Text: strings.Repeat("Should this really long question wrap? ", 4),
		Detail: "Some context.\nMore context on its own line.", Options: []string{strings.Repeat("long option ", 8), "short"}, Multi: true}
	if err := st.AddQuestion(q); err != nil {
		t.Fatal(err)
	}
	m := inboxModel(t, st, q)
	for _, w := range []int{30, 45, 63} {
		m.width = w
		for _, down := range []bool{false, true} {
			if down {
				m.ans.setCursor(len(q.Options))
			}
			for _, line := range strings.Split(m.View(), "\n") {
				if lipgloss.Width(line) > w {
					t.Errorf("width %d: line is %d wide: %q", w, lipgloss.Width(line), line)
				}
			}
		}
		m.ans.setCursor(0)
	}
}

func TestWithdrawStopsAskAndClosesAnswerView(t *testing.T) {
	st := testStore(t)
	q := Question{ID: "q-5", From: "api-agent", Text: "Bump the version?", Options: []string{"Yes", "No"}}
	done := make(chan error, 1)
	go func() {
		_, err := st.Ask(q, 10*time.Millisecond)
		done <- err
	}()
	for deadline := time.Now().Add(2 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if qs, _ := st.Questions(); len(qs) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("question never reached the inbox")
		}
	}
	m := inboxModel(t, st, q)

	// The user answered in the agent's thread, so the agent takes it back.
	n, err := st.Withdraw(func(x Question) bool { return x.ID == q.ID })
	if err != nil || n != 1 {
		t.Fatalf("withdraw: %d, %v", n, err)
	}
	select {
	case err := <-done:
		if err != errQuestionGone {
			t.Errorf("ask should report the question gone, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ask kept waiting after the question was withdrawn")
	}

	m.Update(tickMsg(time.Now()))
	if m.ans != nil || len(m.questions) != 0 {
		t.Errorf("the withdrawn question should leave the sidekick")
	}
	if view := m.View(); !strings.Contains(view, "api-agent withdrew") {
		t.Errorf("the sidekick should say the question was withdrawn:\n%s", view)
	}
}

func TestTopicsGroupAndAdvance(t *testing.T) {
	st := testStore(t)
	now := time.Now()
	for _, q := range []Question{
		{ID: "a1", From: "api", Topic: "Release", Text: "Cut it today?", Options: []string{"Yes", "No"}, AskedAt: now},
		{ID: "b1", From: "web", Text: "Which font?", Options: []string{"Inter", "Plex"}, AskedAt: now},
		{ID: "a2", From: "api", Topic: "Release", Text: "Tag as v2?", Options: []string{"Yes", "No"}, AskedAt: now},
	} {
		if err := st.AddQuestion(q); err != nil {
			t.Fatal(err)
		}
	}
	m := newModel(st, time.Minute)
	m.width, m.height = 63, 40
	m.Init()

	view := m.View()
	order := []string{"Release (2)", "Cut it today?", "Tag as v2?", "web (1)", "Which font?"}
	at := 0
	for _, want := range order {
		i := strings.Index(view[at:], want)
		if i < 0 {
			t.Fatalf("%q missing or out of order in:\n%s", want, view)
		}
		at += i
	}

	m.Update(key("enter"))
	if !strings.Contains(m.View(), "Release · 1 of 2") {
		t.Errorf("answer view should show the question's place in its topic:\n%s", m.View())
	}
	m.Update(key("enter")) // Yes
	if m.ans == nil || m.ans.q.ID != "a2" {
		t.Fatalf("answering should open the next question in the topic, got %+v", m.ans)
	}
	m.Update(key("enter"))
	if m.ans != nil {
		t.Errorf("the topic is done, so the answer view should close, got %s", m.ans.q.ID)
	}
}

func TestRunCommandRequest(t *testing.T) {
	st := testStore(t)
	q := Question{ID: "q-run", From: "api-agent", Text: "Apply the migration?", Options: []string{runYes, runNo},
		Command: "make migrate ENV=staging", Cwd: "/tmp", AskedAt: time.Now()}
	if err := st.AddQuestion(q); err != nil {
		t.Fatal(err)
	}
	var split []string
	rexSplit = func(args ...string) ([]byte, error) {
		split = args
		return []byte(`{"block_ids":["block:run"]}`), nil
	}
	t.Cleanup(func() { rexSplit = func(args ...string) ([]byte, error) { return nil, nil } })

	m := newModel(st, time.Minute)
	m.width, m.height = 60, 40
	m.Init()
	if view := m.View(); !strings.Contains(view, "$ make migrate ENV=staging") || !strings.Contains(view, "run a command") {
		t.Errorf("the inbox should show the command:\n%s", view)
	}
	m.Update(key("enter"))
	if view := m.View(); !strings.Contains(view, "in /tmp") {
		t.Errorf("the answer screen should say where it runs:\n%s", view)
	}
	m.Update(key("enter")) // Run it
	if !slices.Contains(split, "run-pane") || !slices.Contains(split, "q-run") || !slices.Contains(split, "/tmp") {
		t.Fatalf("Run it should split a pane for the command, got %q", split)
	}
	qs, _ := st.Questions()
	if qs[0].Answer != nil || qs[0].RunBlock != "block:run" {
		t.Fatalf("the request should wait on its pane, got %+v", qs[0])
	}
	if view := m.View(); !strings.Contains(view, "running") || strings.Contains(view, "1. Run it") {
		t.Errorf("a running request should say so, without options:\n%s", view)
	}

	// The pane reports back, and the answer goes to the agent.
	res := RunResult{Exit: 2, Output: "migrate: table exists"}
	if err := st.AnswerQuestion("q-run", Answer{Choices: []string{runYes}, Ran: &res}); err != nil {
		t.Fatal(err)
	}
	qs, _ = st.Questions()
	if got := FormatAnswerNotice(qs[0]); !strings.Contains(got, "exited with status 2") || !strings.Contains(got, "table exists") {
		t.Errorf("the agent should hear how it went, got %q", got)
	}
}

func TestDontRunCommand(t *testing.T) {
	st := testStore(t)
	if err := st.AddQuestion(Question{ID: "q-run", Text: "Run it?", Options: []string{runYes, runNo}, Command: "rm -rf build", AskedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	rexSplit = func(args ...string) ([]byte, error) { t.Fatal("Don't run it shouldn't open a pane"); return nil, nil }
	m := newModel(st, time.Minute)
	m.width, m.height = 60, 40
	m.Init()
	m.Update(key("enter"))
	m.Update(key("j"))
	m.Update(key("enter"))
	qs, _ := st.Questions()
	if len(qs) != 1 || qs[0].Answer == nil || qs[0].Answer.String() != "Answer: "+runNo {
		t.Errorf("Don't run it should be an ordinary answer, got %+v", qs)
	}
}

func TestInboxBoxesEachAgent(t *testing.T) {
	st := testStore(t)
	for _, q := range []Question{
		{ID: "q-1", Topic: "billing-agent", Text: "Raise the retry window?", Options: []string{"Yes", "No"}},
		{ID: "q-2", Topic: "billing-agent", Text: "What should the plan be called?"},
		{ID: "q-3", Topic: "docs-agent", Text: "Ship today?", Options: []string{"Today", "Monday"}},
	} {
		if err := st.AddQuestion(q); err != nil {
			t.Fatal(err)
		}
	}
	m := newModel(st, time.Minute)
	m.width, m.height = 63, 60
	m.Init()
	m.reloadQuestions()
	m.tab = tabInbox
	view := m.View()
	b, d := strings.Index(view, "┌─ billing-agent (2)"), strings.Index(view, "┌─ docs-agent (1)")
	if b < 0 || d < 0 || d < strings.Index(view, "What should the plan be called?") {
		t.Fatalf("each agent's questions should sit in their own box:\n%s", view)
	}
}

func TestInfoItemOpensAndDismisses(t *testing.T) {
	st := testStore(t)
	now := time.Now()
	for _, q := range []Question{
		{ID: "i-1", From: "api", Topic: "Release", Text: "PR ready for review", Detail: "Look at **the retry loop**.", Info: true, NotifyBlock: "block:api", AskedAt: now},
		{ID: "i-2", From: "api", Topic: "Release", Text: "Tagged v2", Info: true, AskedAt: now},
		{ID: "q-1", From: "web", Text: "Which font?", Options: []string{"Inter", "Plex"}, AskedAt: now},
	} {
		if err := st.AddQuestion(q); err != nil {
			t.Fatal(err)
		}
	}
	m := newModel(st, time.Minute)
	m.width, m.height = 63, 40
	m.Init()

	view := m.View()
	if !strings.Contains(view, "i PR ready for review") || !strings.Contains(view, "? Which font?") {
		t.Errorf("info items should show an i where questions show a ?:\n%s", view)
	}
	if !strings.Contains(view, "info") {
		t.Errorf("an info item should say it's info:\n%s", view)
	}

	m.Update(key("enter"))
	view = m.View()
	if m.ans == nil || m.ans.q.ID != "i-1" {
		t.Fatalf("enter should open the info item")
	}
	if !strings.Contains(view, "Look at the retry loop.") || strings.Contains(view, "Other") || strings.Contains(view, "Your answer") {
		t.Errorf("an open info item shows its detail and no answer rows:\n%s", view)
	}
	if !strings.Contains(view, "x dismiss") {
		t.Errorf("the footer should offer x dismiss:\n%s", view)
	}

	m.Update(key("x"))
	if m.ans == nil || m.ans.q.ID != "i-2" {
		t.Fatalf("dismissing should open the next item in the topic, got %+v", m.ans)
	}
	m.Update(key("x"))
	if m.ans != nil {
		t.Errorf("the topic is done, so the view should close, got %s", m.ans.q.ID)
	}
	qs, _ := st.Questions()
	if len(qs) != 1 || qs[0].ID != "q-1" {
		t.Fatalf("dismissed info items should leave the store, without an answer to deliver, got %+v", qs)
	}
	if m.hasAnswered {
		t.Errorf("dismissing an info item shouldn't queue a notice for the agent")
	}
}

func TestDismissInfoFromList(t *testing.T) {
	st := testStore(t)
	if err := st.AddQuestion(Question{ID: "i-1", Text: "Done with the migration", Info: true}); err != nil {
		t.Fatal(err)
	}
	m := newModel(st, time.Minute)
	m.width, m.height = 63, 40
	m.Init()
	m.Update(key("x"))
	if qs, _ := st.Questions(); len(qs) != 0 {
		t.Fatalf("x should remove the info item, got %+v", qs)
	}
	if !strings.Contains(m.View(), "Nothing from agents.") {
		t.Errorf("the inbox should be empty:\n%s", m.View())
	}
}

func TestLongDetailScrolls(t *testing.T) {
	st := testStore(t)
	var detail []string
	for i := range 30 {
		detail = append(detail, fmt.Sprintf("- point %d", i))
	}
	q := Question{ID: "q-1", Text: "Ship it?", Detail: strings.Join(detail, "\n"), Options: []string{"Yes", "No"}}
	if err := st.AddQuestion(q); err != nil {
		t.Fatal(err)
	}
	m := newModel(st, time.Minute)
	m.width, m.height = 40, 20
	m.Init()
	m.Update(key("enter"))

	view := m.View()
	if n := strings.Count(view, "\n") + 1; n > m.height {
		t.Errorf("the view is %d lines, taller than the %d-line pane:\n%s", n, m.height, view)
	}
	if !strings.Contains(view, "point 0") || !strings.Contains(view, "more lines (j)") {
		t.Fatalf("a long question should open at the top with a scroll hint:\n%s", view)
	}
	// j scrolls down through the detail until the first option shows, and
	// only then moves the cursor.
	for range 40 {
		if strings.Contains(m.View(), "1. Yes") {
			break
		}
		m.Update(key("j"))
	}
	view = m.View()
	if !strings.Contains(view, "1. Yes") || m.ans.cursor != 0 {
		t.Fatalf("j should scroll to the options before moving the cursor (cursor %d):\n%s", m.ans.cursor, view)
	}
	m.Update(key("j"))
	if m.ans.cursor != 1 || !strings.Contains(m.View(), "2. No") {
		t.Errorf("once the options show, j moves the cursor, got %d", m.ans.cursor)
	}
	// k at the first option scrolls back up to the detail.
	m.Update(key("k"))
	for range 40 {
		if strings.Contains(m.View(), "point 0") {
			break
		}
		m.Update(key("k"))
	}
	if !strings.Contains(m.View(), "point 0") {
		t.Errorf("k should scroll back to the top:\n%s", m.View())
	}
}

func TestAgoDuration(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{-time.Second, "just now"},
		{59 * time.Second, "just now"},
		{time.Minute, "1m ago"},
		{28*time.Minute + 59*time.Second, "28m ago"},
		{time.Hour, "1h ago"},
		{3*time.Hour + 28*time.Minute, "3h 28m ago"},
		{24 * time.Hour, "1d ago"},
		{24*time.Hour + 5*time.Minute, "1d 5m ago"},
		{27*time.Hour + 28*time.Minute, "1d 3h 28m ago"},
		{40 * 24 * time.Hour, "40d ago"},
	}
	for _, c := range cases {
		if got := agoDuration(c.d); got != c.want {
			t.Errorf("agoDuration(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

// Tab leaves a half-written answer for another tab, and the answer is still
// there, on the same question, when you come back.
func TestTabLeavesAnswerAndComesBack(t *testing.T) {
	st := testStore(t)
	qs := []Question{
		{ID: "q-a", Text: "First?", Options: []string{"Yes", "No"}},
		{ID: "q-b", Text: "Squash or rebase?", Options: []string{"Squash", "Rebase"}, Multi: true},
		{ID: "q-c", Text: "Third?", Options: []string{"Yes", "No"}},
	}
	for _, q := range qs {
		if err := st.AddQuestion(q); err != nil {
			t.Fatal(err)
		}
	}
	m := newModel(st, time.Minute)
	m.width, m.height = 63, 40
	m.Init()
	m.Update(key("j"))
	m.Update(key("enter"))
	if m.ans == nil || m.ans.q.ID != "q-b" {
		t.Fatal("enter should open the second question")
	}
	m.Update(key("space")) // check Squash
	m.Update(key("3"))     // the Other row
	// On the Other row, h, l and digits are text, not tab switches.
	for _, r := range "hl 12" {
		m.Update(key(string(r)))
	}
	if m.tab != tabInbox || m.ans.other.Value() != "hl 12" {
		t.Fatalf("h, l and digits should type on the Other row, got tab %d and %q", m.tab, m.ans.other.Value())
	}

	m.Update(key("tab"))
	view := m.View()
	if m.tab != tabTodo || m.ans == nil {
		t.Fatalf("tab should switch to the TODO tab and keep the answer, got tab %d", m.tab)
	}
	if strings.Contains(view, "Squash or rebase?") || strings.Contains(view, "enter send") || !strings.Contains(view, "+ New task") {
		t.Errorf("the TODO tab should show the TODO list, not the answer:\n%s", view)
	}
	// Keys on the TODO tab are the TODO tab's, not the answer's.
	m.Update(key("x"))
	m.Update(key("esc"))
	// The first question goes away while you're on the TODO tab.
	if _, err := st.Withdraw(func(q Question) bool { return q.ID == "q-a" }); err != nil {
		t.Fatal(err)
	}
	m.reloadQuestions()

	m.Update(key("shift+tab"))
	view = m.View()
	if m.tab != tabInbox || m.ans == nil || m.ans.q.ID != "q-b" {
		t.Fatalf("shift+tab should come back to the open answer, got tab %d, %+v", m.tab, m.ans)
	}
	if !m.ans.onOther() || m.ans.other.Value() != "hl 12" || !m.ans.checked[0] {
		t.Errorf("the half-written answer should be as you left it, got cursor %d, %q, %v", m.ans.cursor, m.ans.other.Value(), m.ans.checked)
	}
	if !strings.Contains(view, "Squash or rebase?") || !strings.Contains(view, "hl 12") {
		t.Errorf("the answer screen should be back:\n%s", view)
	}
	if q, ok := m.selectedQuestion(); !ok || q.ID != "q-b" {
		t.Errorf("the inbox cursor should stay on the open question, got %+v", q)
	}

	m.Update(key("enter"))
	saved, _ := st.Questions()
	i := slices.IndexFunc(saved, func(q Question) bool { return q.ID == "q-b" })
	if i < 0 || saved[i].Answer == nil || saved[i].Answer.String() != `Answer: Squash; typed answer: "hl 12"` {
		t.Errorf("the answer should send as written, got %+v", saved)
	}
}

// Off the Other row nothing is being typed, so h and l switch tabs there.
// The number keys still pick options.
func TestHLSwitchTabsOnOptionRows(t *testing.T) {
	st := testStore(t)
	q := Question{ID: "q-1", Text: "Which checks?", Options: []string{"lint", "test", "build"}, Multi: true}
	if err := st.AddQuestion(q); err != nil {
		t.Fatal(err)
	}
	m := inboxModel(t, st, q)
	m.Update(key("2"))
	if m.tab != tabInbox || m.ans.cursor != 1 || !m.ans.checked[1] {
		t.Fatalf("2 should check the second option, got tab %d, cursor %d, %v", m.tab, m.ans.cursor, m.ans.checked)
	}
	m.Update(key("l"))
	if m.tab != tabTodo || m.ans == nil {
		t.Fatalf("l on an option row should switch to the TODO tab, got tab %d", m.tab)
	}
	m.Update(key("h"))
	if m.tab != tabInbox || m.ans == nil || m.ans.cursor != 1 || !m.ans.checked[1] {
		t.Fatalf("h should come back to the answer as left, got tab %d, %+v", m.tab, m.ans)
	}
	if view := m.View(); !strings.Contains(view, "Which checks?") {
		t.Errorf("the answer screen should be back:\n%s", view)
	}

	// A message has nothing to type or pick, so the number keys switch too.
	info := Question{ID: "i-1", Text: "Shipped it", Info: true}
	if err := st.AddQuestion(info); err != nil {
		t.Fatal(err)
	}
	m.Update(key("esc"))
	m.reloadQuestions()
	m.cursors[tabInbox] = slices.IndexFunc(m.questions, func(o Question) bool { return o.ID == "i-1" })
	m.Update(key("enter"))
	if m.ans == nil || !m.ans.q.Info {
		t.Fatal("enter should open the message")
	}
	m.Update(key("3"))
	if m.tab != tabPRs || m.ans == nil {
		t.Errorf("3 on a message should switch to the PRs tab, got tab %d", m.tab)
	}
}

// c on a message opens a box for a reply to the agent. Sending it leaves the
// message open, and the reply reaches the agent's pane inbox like any notice.
func TestReplyToMessage(t *testing.T) {
	st := testStore(t)
	q := Question{ID: "i-1", From: "api-agent", NotifySession: "session:test", NotifyBlock: "block:agent",
		Text: "PR ready for review: Fix login", Info: true, AskedAt: time.Now()}
	if err := st.AddQuestion(q); err != nil {
		t.Fatal(err)
	}
	// An agent is listening with `wait`, so the reply queues instead of being
	// typed into a pane.
	b, err := openInbox(q.NotifyBlock)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b.path("wait"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	m := inboxModel(t, st, q)
	if view := m.View(); !strings.Contains(view, "c reply") {
		t.Errorf("the hints should offer a reply:\n%s", view)
	}
	m.Update(key("c"))
	if !m.ans.replying {
		t.Fatal("c should open the reply box")
	}
	if view := m.View(); !strings.Contains(view, "Reply to api-agent") || !strings.Contains(view, "esc cancel") {
		t.Errorf("the reply box should show:\n%s", view)
	}

	// Keys that dismiss, close, or switch tabs on a message are text here.
	for _, k := range []string{"h", "l", "2", "x", "d", "q", "j", "c"} {
		m.Update(key(k))
	}
	if m.tab != tabInbox || m.ans == nil || m.ans.other.Value() != "hl2xdqjc" {
		t.Fatalf("keys should type in the reply, got tab %d and %+v", m.tab, m.ans)
	}
	// tab switches away, and the reply is still there when you come back.
	m.Update(key("tab"))
	if m.tab == tabInbox {
		t.Fatal("tab should switch tabs while replying")
	}
	m.Update(key("shift+tab"))
	if m.tab != tabInbox || m.ans == nil || !m.ans.replying || m.ans.other.Value() != "hl2xdqjc" {
		t.Fatalf("the reply should be as left, got tab %d and %+v", m.tab, m.ans)
	}
	for range "hl2xdqjc" {
		m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	}
	m.Update(key("use the  new vault path"))
	_, cmd := m.Update(key("enter"))
	if cmd == nil {
		t.Fatal("enter should send the reply")
	}
	if msg, ok := cmd().(notifiedMsg); !ok || msg.err != nil {
		t.Fatalf("sending should succeed, got %+v", msg)
	}
	if m.ans == nil || m.ans.replying || m.ans.q.ID != "i-1" {
		t.Fatalf("the message should stay open with the box closed, got %+v", m.ans)
	}
	want := `[sidekick] reply to your message "PR ready for review: Fix login" (i-1), from the user: "use the new vault path"`
	if got, err := b.Take(); err != nil || !slices.Equal(got, []string{want}) {
		t.Errorf("the agent's inbox got %q (%v), want %q", got, err, want)
	}
	qs, _ := st.Questions()
	if len(qs) != 1 || qs[0].Answer != nil || !slices.Equal(qs[0].Replies, []string{"use the new vault path"}) {
		t.Errorf("the message should stay in the inbox with its reply, got %+v", qs)
	}
	if view := m.View(); !strings.Contains(view, "You: use the new vault path") || !strings.Contains(view, "Sent to api-agent") {
		t.Errorf("the message should show the reply and where it went:\n%s", view)
	}

	// An empty reply sends nothing, and esc closes the box, then the message.
	m.Update(key("c"))
	if _, cmd := m.Update(key("enter")); cmd != nil || !m.ans.replying {
		t.Error("enter on an empty reply should do nothing")
	}
	m.Update(key("esc"))
	if m.ans == nil || m.ans.replying {
		t.Fatal("esc should close only the reply box")
	}
	m.Update(key("esc"))
	if m.ans != nil {
		t.Fatal("a second esc should close the message")
	}
	if qs, _ := st.Questions(); len(qs) != 1 {
		t.Errorf("closing shouldn't dismiss the message, got %+v", qs)
	}

	// Reopened, it still shows the reply.
	m.Update(key("enter"))
	if view := m.View(); m.ans == nil || !strings.Contains(view, "You: use the new vault path") {
		t.Errorf("the reply should show when the message is reopened:\n%s", view)
	}
}

// A message from no pane in particular has nobody to reply to.
func TestReplyNeedsAPane(t *testing.T) {
	st := testStore(t)
	q := Question{ID: "i-1", Text: "Shipped it", Info: true, AskedAt: time.Now()}
	if err := st.AddQuestion(q); err != nil {
		t.Fatal(err)
	}
	m := inboxModel(t, st, q)
	if view := m.View(); strings.Contains(view, "c reply") {
		t.Errorf("the hints shouldn't offer a reply:\n%s", view)
	}
	m.Update(key("c"))
	if m.ans == nil || m.ans.replying {
		t.Fatal("c shouldn't open a reply box")
	}
	if view := m.View(); !strings.Contains(view, "no agent pane to reply to") {
		t.Errorf("it should say why:\n%s", view)
	}
}
