package main

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestViewWrapsToWidth(t *testing.T) {
	url := "https://github.com/acme/widgets/pull/42"
	m := &model{
		entries: []Entry{{URL: url, Repo: "acme/widgets", Number: 42,
			Title: "A very long pull request title that cannot possibly fit on one narrow line"}},
		statuses: map[string]Status{url: {
			State: "OPEN", Checks: "failing", FailingChecks: []string{"lint"},
			ReviewDecision: "CHANGES_REQUESTED", MergeState: "DIRTY",
			Additions: 12345, Deletions: 678, FetchedAt: time.Now(),
			Reviewers: []Reviewer{{"alice", "APPROVED"}, {"bob", "CHANGES_REQUESTED"},
				{"carol-with-a-long-handle", "PENDING"}, {"dan", "COMMENTED"}},
		}},
		inflight: map[string]bool{},
		interval: time.Minute,
		// We don't know our login, so the PR counts as someone else's.
		tab:    tabPRs,
		errMsg: "notify failed: rex send: block block:00000000-0000-7000-8000-000000000005 not found",
		errAt:  time.Now(),
	}
	for _, w := range []int{24, 30, 45} {
		m.width, m.height = w, 40
		view := m.View()
		for _, line := range strings.Split(view, "\n") {
			if lipgloss.Width(line) > w {
				t.Errorf("width %d: line is %d wide: %q", w, lipgloss.Width(line), line)
			}
		}
		// Below 45 columns the long handle is split across lines inside the
		// box, so only look for it when it fits.
		wants := []string{"narrow", "conflicts", "notify failed"}
		if w >= 45 {
			wants = append(wants, "carol-with-a-long-handle")
		}
		for _, want := range wants {
			if !strings.Contains(view, want) {
				t.Errorf("width %d: %q missing, so something was cut instead of wrapped", w, want)
			}
		}
	}
}

func TestPRsTabGroups(t *testing.T) {
	review := Entry{URL: "https://github.com/acme/api/pull/1", Repo: "acme/api", Number: 1, Title: "Review me", Author: "alice"}
	ours := Entry{URL: "https://github.com/acme/widgets/pull/2", Repo: "acme/widgets", Number: 2, Title: "Our fix"}
	theirs := Entry{URL: "https://github.com/acme/widgets/pull/3", Repo: "acme/widgets", Number: 3, Title: "Their fix", Author: "bob"}
	m := &model{
		entries: []Entry{theirs, ours},
		reviews: []Entry{review},
		// The entry has no author, so this comes from the fetched status.
		statuses: map[string]Status{
			ours.URL:   {State: "OPEN", Author: "Me", FetchedAt: time.Now()},
			theirs.URL: {State: "OPEN", FetchedAt: time.Now()},
		},
		inflight: map[string]bool{},
		tab:      tabPRs,
		width:    63, height: 60,
	}
	m.Update(loginMsg{login: "me"})

	view := m.View()
	at := 0
	for _, want := range []string{"PRs (3)", "┌─ Needs review", "Review me", "┌─ My PRs (1)", "Our fix", "┌─ Others", "Their fix"} {
		i := strings.Index(view[at:], want)
		if i < 0 {
			t.Fatalf("%q missing or out of order in:\n%s", want, view)
		}
		at += i
	}

	press := func(k string) { m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}) }
	hints := func() string { v := m.View(); return v[strings.LastIndex(v, "\n"):] }

	// On the review request, m does nothing, and c and e work.
	if h := hints(); !strings.Contains(h, "c Claude review") || strings.Contains(h, "m merge") {
		t.Errorf("hints for a review request: %q", h)
	}
	press("m")
	if m.mergeConfirm != nil {
		t.Errorf("m shouldn't merge a review request")
	}
	press("c")
	if m.modelPicker == nil || m.modelPicker.e.URL != review.URL {
		t.Fatalf("c should open the model picker on a review request")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.reviewInput == nil || m.reviewInput.e.URL != review.URL || m.reviewInput.model != defaultReviewModel {
		t.Fatalf("enter in the picker should open the context box with the model")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})

	// On our PR, m merges and c picks a model for a self-review.
	press("j")
	if h := hints(); !strings.Contains(h, "m merge") || !strings.Contains(h, "c self-review") || strings.Contains(h, "c Claude review") {
		t.Errorf("hints for our PR: %q", h)
	}
	press("c")
	if m.modelPicker == nil || m.modelPicker.e.URL != ours.URL || m.reviewInput != nil {
		t.Fatalf("c should open the model picker on our PR")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	press("e")
	if m.modelPicker != nil || m.reviewInput != nil || len(m.reviews) != 1 {
		t.Errorf("esc should close the picker, and e should do nothing on our PR")
	}
	press("m")
	if m.mergeConfirm == nil || m.mergeConfirm.URL != ours.URL {
		t.Fatalf("m should ask to merge our PR")
	}
	press("n")

	// On their PR, none of m, c or e act.
	press("j")
	if h := hints(); strings.Contains(h, "m merge") || strings.Contains(h, "u unassign") || !strings.Contains(h, "x remove") {
		t.Errorf("hints for their PR: %q", h)
	}
	press("m")
	press("c")
	press("e")
	if m.mergeConfirm != nil || m.reviewInput != nil || len(m.reviews) != 1 {
		t.Errorf("m, c and e should do nothing on their PR")
	}
}

func TestMergedPRSinksBelowRule(t *testing.T) {
	t.Setenv("REX_SIDEKICK_STATE_DIR", t.TempDir())
	st, err := OpenStore("session:test")
	if err != nil {
		t.Fatal(err)
	}
	done := Entry{URL: "https://github.com/acme/widgets/pull/3", Repo: "acme/widgets", Number: 3, Title: "Done fix"}
	open := Entry{URL: "https://github.com/acme/widgets/pull/4", Repo: "acme/widgets", Number: 4, Title: "Open fix"}
	for _, e := range []Entry{done, open} {
		if _, err := st.Add(e); err != nil {
			t.Fatal(err)
		}
	}
	m := newModel(st, time.Minute)
	m.width, m.height = 63, 40
	m.reload()

	m.Update(fetchedMsg{done.URL: {State: "MERGED", Title: "Done fix"}, open.URL: {State: "OPEN", Title: "Open fix"}})
	if list, _ := st.List(); len(list) != 1 || list[0].URL != open.URL {
		t.Errorf("merged PR should leave the watch list, got %v", list)
	}

	// We don't know our login, so both count as someone else's.
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")})
	view := m.View()
	at := 0
	for _, want := range []string{"PRs (1)", "┌─ Others", "Open fix", "───", "Done fix"} {
		i := strings.Index(view[at:], want)
		if i < 0 {
			t.Fatalf("%q missing or out of order in:\n%s", want, view)
		}
		at += i
	}

	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if len(m.closed) != 0 {
		t.Errorf("x should clear the merged PR from the list")
	}
	if list, _ := st.List(); len(list) != 1 {
		t.Errorf("clearing a merged PR shouldn't touch the watch list, got %v", list)
	}
}

func TestNeedsReviewGroupsByAuthor(t *testing.T) {
	m := &model{statuses: map[string]Status{}, inflight: map[string]bool{}, tab: tabPRs, width: 63, height: 40}
	pr := func(author string, n int) Entry {
		return Entry{URL: fmt.Sprintf("https://github.com/acme/api/pull/%d", n), Repo: "acme/api", Number: n, Author: author, Title: fmt.Sprintf("%s change %d", author, n)}
	}
	m.Update(reviewsMsg{entries: []Entry{pr("alice", 1), pr("carol", 2), pr("alice", 3)}, statuses: map[string]Status{}})
	view := m.View()
	if strings.Contains(view, "by alice") {
		t.Errorf("the author heading should replace the by line:\n%s", view)
	}
	at := 0
	for _, want := range []string{"alice (2)", "alice change 1", "alice change 3", "carol (1)", "carol change 2"} {
		i := strings.Index(view[at:], want)
		if i < 0 {
			t.Fatalf("%q missing or out of order in:\n%s", want, view)
		}
		at += i
	}
}

func TestNeedsReviewTab(t *testing.T) {
	t.Setenv("REX_SIDEKICK_STATE_DIR", t.TempDir())
	st, err := OpenStore("session:test")
	if err != nil {
		t.Fatal(err)
	}
	m := newModel(st, time.Minute)
	m.width, m.height = 63, 40
	if view := m.View(); strings.Contains(view, "Needs review") {
		t.Errorf("with nothing to review, the box should be hidden:\n%s", view)
	}

	pr := Entry{URL: "https://github.com/acme/api/pull/9", Repo: "acme/api", Number: 9, Title: "Rotate signing keys", Author: "alice"}
	m.Update(reviewsMsg{entries: []Entry{pr}, statuses: map[string]Status{pr.URL: {State: "OPEN", Title: pr.Title, Checks: "passing", FetchedAt: time.Now()}}})
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")})
	view := m.View()
	for _, want := range []string{"PRs (1)", "┌─ Needs review", "Rotate signing keys", "c Claude review", "u unassign"} {
		if !strings.Contains(view, want) {
			t.Errorf("%q missing:\n%s", want, view)
		}
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if len(m.reviews) != 1 {
		t.Errorf("x shouldn't drop a review request")
	}

	// The status names no reviewers, so archiving only hides it here.
	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")}); cmd != nil {
		t.Errorf("archiving without a direct request shouldn't call GitHub")
	}
	if view := m.View(); len(m.reviews) != 0 || !strings.Contains(view, "Hid acme/api#9") {
		t.Errorf("e should archive the PR:\n%s", view)
	}
	if view := m.View(); m.tab != tabPRs || strings.Contains(view, "Needs review") {
		t.Errorf("archiving the last review request should hide its box (tab %d):\n%s", m.tab, view)
	}
	// Search results can lag, so it stays hidden when the next one still has it.
	m.Update(reviewsMsg{entries: []Entry{pr}, statuses: map[string]Status{}})
	if len(m.reviews) != 0 {
		t.Errorf("an archived PR came back after the next search")
	}

	// With us requested by name, archiving also asks GitHub to drop the request.
	other := Entry{URL: "https://github.com/acme/api/pull/10", Repo: "acme/api", Number: 10, Title: "Bump deps"}
	m.me = "me"
	m.Update(reviewsMsg{entries: []Entry{other}, statuses: map[string]Status{other.URL: {State: "OPEN", Reviewers: []Reviewer{{"Me", "PENDING"}}}}})
	// It asks for an optional comment first, and esc keeps the request.
	press := func(k string) tea.Cmd {
		_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)})
		return cmd
	}
	if cmd := press("e"); cmd != nil || m.reviewInput == nil || !m.reviewInput.archiving {
		t.Fatalf("e on a direct request should ask for a comment first")
	}
	if view := m.View(); !strings.Contains(view, "Comment for the PR") {
		t.Errorf("the box should say what enter does:\n%s", view)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.reviewInput != nil || len(m.reviews) != 1 {
		t.Fatalf("esc should cancel the archive")
	}
	press("e")
	for _, r := range "Deferring to Mike" {
		press(string(r))
	}
	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter}); cmd == nil || len(m.reviews) != 0 {
		t.Errorf("enter should archive and remove the request on GitHub")
	}
}

func TestSidebarFitsTabs(t *testing.T) {
	m := &model{width: sidebarCols, height: 30, inflight: map[string]bool{}, interval: time.Minute}
	for i := range 99 {
		m.questions = append(m.questions, Question{ID: fmt.Sprint(i), Text: "q"})
	}
	m.entries = []Entry{{URL: "https://github.com/acme/api/pull/1", Repo: "acme/api", Number: 1}}
	m.reviews = []Entry{{URL: "https://github.com/acme/api/pull/2", Repo: "acme/api", Number: 2}}
	header, _, _ := strings.Cut(m.View(), "\n")
	for _, name := range tabNames {
		if !strings.Contains(header, name) {
			t.Errorf("at %d columns the %q tab wrapped off the first row: %q", sidebarCols, name, header)
		}
	}
}

func TestOthersBoxHidesWhenEmpty(t *testing.T) {
	t.Setenv("REX_SIDEKICK_STATE_DIR", t.TempDir())
	st, err := OpenStore("session:test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Add(Entry{URL: "https://github.com/acme/api/pull/1", Repo: "acme/api", Number: 1, Title: "Theirs"}); err != nil {
		t.Fatal(err)
	}
	m := newModel(st, time.Minute)
	m.width, m.height = 63, 40
	m.reload()
	key := func(s string) { m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}) }

	if m.tab != tabPRs {
		t.Fatalf("the sidekick should start on the PRs tab, got %d", m.tab)
	}
	// We don't know our login, so the PR counts as someone else's.
	if view := m.View(); !strings.Contains(view, "PRs (1)") || !strings.Contains(view, "┌─ Others") {
		t.Fatalf("the PR should be in the Others box:\n%s", view)
	}
	key("x")
	if view := m.View(); m.tab != tabPRs || strings.Contains(view, "Others") {
		t.Errorf("removing the last PR should hide the Others box (tab %d):\n%s", m.tab, view)
	}
	// l goes on to Workers and then wraps to the Inbox, and h wraps back.
	key("l")
	key("l")
	if m.tab != tabInbox {
		t.Errorf("l should wrap to the Inbox, got tab %d", m.tab)
	}
	key("h")
	if m.tab != tabWorkers {
		t.Errorf("h should wrap back to Workers, got tab %d", m.tab)
	}
	key("2")
	if m.tab != tabTodo {
		t.Errorf("2 should open the TODO tab, got tab %d", m.tab)
	}
	key("5")
	if m.tab != tabTodo {
		t.Errorf("a number past the tabs should do nothing, got tab %d", m.tab)
	}
}

func TestMergeAsksFirst(t *testing.T) {
	e := Entry{URL: "https://github.com/acme/api/pull/7", Repo: "acme/api", Number: 7, Title: "Mine", Author: "me"}
	m := &model{entries: []Entry{e}, me: "me", tab: tabPRs, width: 63, height: 30,
		statuses: map[string]Status{e.URL: {State: "OPEN"}}, inflight: map[string]bool{}, interval: time.Minute}
	key := func(s string) tea.Cmd {
		_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)})
		return cmd
	}
	if view := m.View(); !strings.Contains(view, "m merge") {
		t.Errorf("our PR should offer m merge:\n%s", view)
	}

	key("m")
	if view := m.View(); m.mergeConfirm == nil || !strings.Contains(view, "y merge acme/api#7") {
		t.Fatalf("m should ask before merging:\n%s", view)
	}
	if cmd := key("n"); cmd != nil || m.mergeConfirm != nil || !strings.Contains(m.View(), "Didn't merge acme/api#7") {
		t.Errorf("any key but y should cancel without merging:\n%s", m.View())
	}

	key("m")
	if cmd := key("y"); cmd == nil || !m.merging[e.URL] {
		t.Errorf("y should start the merge")
	}
	key("m")
	if m.mergeConfirm != nil {
		t.Errorf("m shouldn't ask again while the merge is running")
	}
	m.Update(mergedMsg{e: e, method: "SQUASH", err: fmt.Errorf("gh pr merge: not mergeable")})
	if view := m.View(); m.merging[e.URL] || !strings.Contains(view, "not mergeable") {
		t.Errorf("a failed merge should show why:\n%s", view)
	}

	m.statuses[e.URL] = Status{State: "OPEN", IsDraft: true}
	if key("m"); m.mergeConfirm != nil {
		t.Errorf("drafts can't be merged")
	}

	// Once the PR isn't ours, it moves to Others, where m doesn't merge.
	m.statuses[e.URL] = Status{State: "OPEN"}
	m.me = "someone-else"
	if key("m"); m.mergeConfirm != nil {
		t.Errorf("m should only merge our own PRs")
	}
}

// A half-typed TODO box waits on its tab while tab takes you elsewhere.
func TestTabLeavesTodoBox(t *testing.T) {
	st := testStore(t)
	m := todoModel(t, st)
	m.cursors[tabTodo] = slices.IndexFunc(m.todoItems, func(it todoItem) bool { return it.addRow })
	m.Update(key("enter"))
	if m.todoInput == nil || !m.todoInput.adding {
		t.Fatal("enter on New task should open the new task box")
	}
	for _, r := range "buy milk" {
		m.Update(key(string(r)))
	}
	m.Update(key("tab"))
	if m.tab != tabPRs || m.todoInput == nil {
		t.Fatalf("tab should switch to PRs and keep the box, got tab %d", m.tab)
	}
	if view := m.View(); strings.Contains(view, "buy milk") || strings.Contains(view, "esc cancel") {
		t.Errorf("the PRs tab shouldn't show the TODO box:\n%s", view)
	}
	m.Update(key("h")) // a tab switch here, not text
	if m.tab != tabTodo || m.todoInput.ta.Value() != "buy milk" {
		t.Fatalf("h should switch back to the TODO tab with the box as left, got tab %d and %q", m.tab, m.todoInput.ta.Value())
	}
	// Back in the box, h, l and numbers are text again.
	m.Update(key("h"))
	m.Update(key("l"))
	m.Update(key("2"))
	if m.tab != tabTodo || m.todoInput.ta.Value() != "buy milkhl2" {
		t.Fatalf("h, l and 2 should type in the box, got tab %d and %q", m.tab, m.todoInput.ta.Value())
	}
	for range "hl2" {
		m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	}
	m.Update(key("!"))
	m.Update(key("enter"))
	if ts, _ := st.Todos(); len(ts) != 1 || ts[0].Text != "buy milk!" {
		t.Errorf("the task should save as typed, got %+v", ts)
	}
}

// Tab cancels a pending merge, so y on another tab never merges.
func TestTabCancelsMerge(t *testing.T) {
	e := Entry{URL: "https://github.com/acme/api/pull/7", Repo: "acme/api", Number: 7, Title: "Mine", Author: "me"}
	m := &model{entries: []Entry{e}, me: "me", tab: tabPRs, width: 63, height: 30,
		statuses: map[string]Status{e.URL: {State: "OPEN"}}, inflight: map[string]bool{}, interval: time.Minute}
	m.Update(key("m"))
	if m.mergeConfirm == nil {
		t.Fatal("m should ask before merging")
	}
	if _, cmd := m.Update(key("tab")); cmd != nil || m.mergeConfirm != nil || m.tab != tabWorkers {
		t.Fatalf("tab should cancel the merge and switch tabs, got tab %d", m.tab)
	}
	m.Update(key("y"))
	if m.merging[e.URL] {
		t.Error("y on another tab shouldn't merge")
	}
}

// The move picker waits on the TODO tab too, and keys elsewhere don't reach it.
func TestTabLeavesMovePicker(t *testing.T) {
	st := testStore(t)
	if err := st.AddMyGroup("Home"); err != nil {
		t.Fatal(err)
	}
	if err := st.AddTodos([]Todo{{ID: "a", Mine: true, Text: "pick a date"}}); err != nil {
		t.Fatal(err)
	}
	m := todoModel(t, st)
	selectTask(t, m, "a")
	m.Update(key("m"))
	if m.todoMove == nil {
		t.Fatal("m should open the move picker")
	}
	m.Update(key("tab"))
	if m.tab != tabPRs || m.todoMove == nil {
		t.Fatalf("tab should switch to PRs and keep the picker, got tab %d", m.tab)
	}
	m.Update(key("enter")) // the PRs tab's enter, not the picker's
	if ts, _ := st.Todos(); m.todoMove == nil || ts[0].Group != "" {
		t.Fatalf("enter on PRs shouldn't move the task, got %+v", ts)
	}
	m.Update(key("h"))
	if m.tab != tabTodo || m.todoMove == nil {
		t.Fatalf("h should switch back to the TODO tab with the picker open, got tab %d", m.tab)
	}
	if m.Update(key("l")); m.tab != tabPRs {
		t.Fatalf("l should switch away from the picker, got tab %d", m.tab)
	}
	m.Update(key("2"))
	if m.Update(key("3")); m.tab != tabPRs || m.todoMove == nil {
		t.Fatalf("2 should switch to the TODO tab, and 3 away from the picker, got tab %d", m.tab)
	}
	m.Update(key("2"))
	m.Update(key("enter"))
	if ts, _ := st.Todos(); m.todoMove != nil || ts[0].Group != "Home" {
		t.Errorf("enter back on the TODO tab should move it to Home, got %+v", ts)
	}
}

// A write that lands between reading the list and noting its mtime used to
// leave the TUI on the old list for good.
func TestReloadCatchesWriteDuringRead(t *testing.T) {
	t.Setenv("REX_SIDEKICK_STATE_DIR", t.TempDir())
	st, err := OpenStore("session:test")
	if err != nil {
		t.Fatal(err)
	}
	a := Entry{URL: "https://github.com/acme/widgets/pull/1", Repo: "acme/widgets", Number: 1}
	b := Entry{URL: "https://github.com/acme/widgets/pull/2", Repo: "acme/widgets", Number: 2}
	if _, err := st.Add(a); err != nil {
		t.Fatal(err)
	}
	m := newModel(st, time.Minute)
	st.afterListRead = func() {
		st.afterListRead = nil
		time.Sleep(10 * time.Millisecond) // so the mtime moves
		if _, err := st.Add(b); err != nil {
			t.Fatal(err)
		}
	}
	m.reload()
	if len(m.entries) != 1 {
		t.Fatalf("first reload should see only the first PR, got %v", m.entries)
	}
	m.Update(tickMsg{})
	if len(m.entries) != 2 {
		t.Errorf("the next tick should pick up the second PR, got %v", m.entries)
	}
}

// A poll result must not hide a write that the next tick hasn't seen yet.
func TestFetchDoesNotHideNewEntry(t *testing.T) {
	t.Setenv("REX_SIDEKICK_STATE_DIR", t.TempDir())
	st, err := OpenStore("session:test")
	if err != nil {
		t.Fatal(err)
	}
	a := Entry{URL: "https://github.com/acme/widgets/pull/1", Repo: "acme/widgets", Number: 1}
	b := Entry{URL: "https://github.com/acme/widgets/pull/2", Repo: "acme/widgets", Number: 2}
	if _, err := st.Add(a); err != nil {
		t.Fatal(err)
	}
	m := newModel(st, time.Minute)
	m.reload()
	time.Sleep(10 * time.Millisecond)
	if _, err := st.Add(b); err != nil {
		t.Fatal(err)
	}
	m.Update(fetchedMsg{a.URL: {State: "OPEN"}})
	m.Update(tickMsg{})
	if len(m.entries) != 2 {
		t.Errorf("the tick after a poll should pick up the second PR, got %v", m.entries)
	}
}
