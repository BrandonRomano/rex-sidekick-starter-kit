package main

import (
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
)

var (
	colOpen   = lipgloss.Color("#3fb950")
	colDraft  = lipgloss.Color("#8b949e")
	colMerged = lipgloss.Color("#a371f7")
	colClosed = lipgloss.Color("#f85149")
	colWarn   = lipgloss.Color("#d29922")

	sDim = lipgloss.NewStyle().Foreground(lipgloss.Color("#8b949e"))
	sAdd = lipgloss.NewStyle().Foreground(colOpen)
	sDel = lipgloss.NewStyle().Foreground(colClosed)
	sSel = lipgloss.NewStyle().Foreground(colMerged).Bold(true)
)

const (
	tabInbox = iota
	tabTodo
	tabPRs
	tabWorkers
	numTabs
)

var tabNames = [numTabs]string{"Inbox", "TODO", "PRs", "Workers"}

type tickMsg time.Time

// fitMsg reports a step of fitting the sidekick's width.
type fitMsg struct {
	done    bool
	resized time.Time
}

type archivedMsg struct {
	ref       string
	commented bool // posted the user's comment too
	err       error
}

type reviewsMsg struct {
	entries  []Entry
	statuses map[string]Status
	err      error
}

type fetchedMsg map[string]Status // by PR URL

type loginMsg struct {
	login string
	err   error
}

type notifiedMsg struct {
	text string
	err  error
}

type model struct {
	st       *Store
	interval time.Duration
	me       string // our GitHub login, empty until known

	entries []Entry // the watch list
	// Merged and closed PRs, newest first. They're off the watch list and
	// only last as long as this process. They show at the bottom of their tab.
	closed []Entry
	// Open PRs that ask us for a review, and when we last looked.
	reviews         []Entry
	reviewsAt       time.Time
	reviewsBusy     bool
	reviewing       map[string]bool // PRs we started a Claude review for
	reviewInput     *reviewInput    // the context box for a review about to start
	modelPicker     *modelPicker    // the model choice for a self-review about to start
	lastReviewModel string          // the family ("opus") of the model the last review ran on
	archived        map[string]bool // review requests hidden with e
	// The PR waiting on y to merge, and the merges still running.
	mergeConfirm *Entry
	merging      map[string]bool
	listMod      time.Time
	statuses     map[string]Status
	inflight     map[string]bool
	nextAt       map[string]time.Time // when to check each PR next
	// GitHub said we hit its rate limit, so don't poll until then.
	pausedUntil time.Time
	// When we last looked for inbox notices nobody picked up.
	flushedAt time.Time

	questions   []Question // unanswered, oldest first
	qMod        time.Time
	qLoaded     bool
	hasAnswered bool // answers wait in the file for their agent
	delivering  bool
	// Panes we sent answers or TODO notes to, so their inbox notices get
	// flushed too.
	answerBlocks []Entry
	ans          *answerView // the question being answered, if any

	todos   []Todo
	todoMod time.Time
	// Where the user's lists that aren't kept with the session keep their
	// tasks, by name.
	todoBacks map[string]todoBackend
	todoItems []todoItem // the TODO tab's rows, in order
	// The sub-groups of the user's lists, by list name, as paths like
	// "Basement/Boiler".
	todoSubs  map[string][]string
	todoInput *todoInput // the open text box on the TODO tab, if any
	todoMove  *todoMover // the open picker for where a task moves, if any
	myList    string     // the user's own list's name
	todoOpen  int        // unfinished tasks showing now
	// Tasks hidden until later, by list, and the soonest one's time.
	todoLater  map[string]int
	todoWake   time.Time
	todoReload time.Time // when the TODO tab was last built
	// The list waiting on a second x before it goes.
	confirmList string
	// The lists the user folded with enter, by heading row key.
	todoCollapsed map[string]bool
	// The task whose details fill the TODO tab, if any.
	todoDetail *todoDetail
	// The dispatcher's workers, what their panes last showed by block, and
	// when the panes were last read.
	workers     []Worker
	workerMod   time.Time
	panes       map[string]*paneWatch
	workersBusy bool
	workersAt   time.Time
	// Fitting the sidekick's own width. It stops once the width is right, so
	// it never fights the user resizing the pane.
	fitDone, fitBusy bool
	fitAt, fitSince  time.Time

	tab           int
	cursors       [numTabs]int // each tab keeps its own cursor
	width, height int
	errMsg        string // shown at the bottom for a minute
	errAt         time.Time
	errIsNote     bool // errMsg is news, not an error
	// How many lines the selected item is scrolled, when it's too tall to
	// fit. View sets maxScroll, and resets scroll when the selection changes.
	scroll, maxScroll int
	scrolledItem      [2]int // the tab and cursor that scroll belongs to
}

func newModel(st *Store, interval time.Duration) *model {
	statuses := st.LoadStatuses()
	for url, s := range statuses {
		// Statuses saved by older versions have no change time.
		if s.LastChange.IsZero() {
			s.LastChange = s.FetchedAt
			statuses[url] = s
		}
	}
	return &model{
		st:       st,
		interval: interval,
		statuses: statuses,
		inflight: map[string]bool{},
		nextAt:   map[string]time.Time{},
		tab:      tabPRs,
		myList:   myListName(),
	}
}

func tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m *model) Init() tea.Cmd {
	m.reload()
	m.reloadTodos()
	m.reloadWorkers()
	if m.reloadQuestions(); len(m.questions) > 0 {
		m.tab = tabInbox
	}
	login := func() tea.Msg {
		l, err := Login()
		return loginMsg{l, err}
	}
	return tea.Batch(tick(), m.poll(false), login)
}

func (m *model) reload() {
	// Stat before reading. A write after the read then still shows up as a
	// new mtime on the next tick.
	mod := m.st.ListModTime()
	entries, err := m.st.List()
	if err != nil {
		m.setError("reading watch list: " + err.Error())
		return
	}
	m.keepSelection(func() {
		m.entries = entries
		m.listMod = mod
		m.archiveDone()
	})
	m.dropStalePicker()
}

// done reports whether the PR was last seen merged or closed.
func (m *model) done(url string) bool {
	s, ok := m.statuses[url]
	return ok && s.Err == "" && (s.State == "MERGED" || s.State == "CLOSED")
}

// archiveDone moves merged and closed PRs off the watch list and onto the
// Closed tab.
func (m *model) archiveDone() {
	kept := m.entries[:0]
	for _, e := range m.entries {
		if !m.done(e.URL) {
			kept = append(kept, e)
			continue
		}
		if _, err := m.st.Remove(e.URL); err != nil {
			m.setError("removing closed PR: " + err.Error())
		}
		closed := []Entry{e}
		for _, c := range m.closed {
			if c.URL != e.URL {
				closed = append(closed, c)
			}
		}
		m.closed = closed
	}
	// Leave listMod alone. Removing a PR changes the mtime, so the next tick
	// reloads and picks up anything else written in the meantime.
	m.entries = kept
}

// poll fetches every open PR that is due, or every open PR when force is set,
// in one request. Once something is due it also brings along PRs due within
// the next base interval. They add nothing to the cost of the request, and it
// keeps PRs on a shared schedule instead of each firing its own.
func (m *model) poll(force bool) tea.Cmd {
	now := time.Now()
	if !force && now.Before(m.pausedUntil) {
		return nil
	}
	var urls []string
	due := force
	for _, e := range m.entries {
		if m.inflight[e.URL] || m.done(e.URL) {
			continue
		}
		next := m.nextAt[e.URL]
		if !now.Before(next) {
			due = true
		}
		if force || now.Add(m.interval).After(next) {
			urls = append(urls, e.URL)
		}
	}
	if !due || len(urls) == 0 {
		return nil
	}
	for _, u := range urls {
		m.inflight[u] = true
	}
	return func() tea.Msg { return fetchedMsg(FetchAll(urls)) }
}

// mine reports whether we wrote the PR. It's false while we don't know yet.
func (m *model) mine(e Entry) bool {
	author := e.Author
	if s, ok := m.statuses[e.URL]; ok && s.Author != "" {
		author = s.Author
	}
	return m.me != "" && strings.EqualFold(author, m.me)
}

func (m *model) isClosed(url string) bool {
	return slices.ContainsFunc(m.closed, func(c Entry) bool { return c.URL == url })
}

// tabLen counts what's on tab t.
func (m *model) tabLen(t int) int {
	switch t {
	case tabInbox:
		return len(m.questions)
	case tabTodo:
		return len(m.todoItems)
	case tabWorkers:
		return len(m.workers)
	}
	return len(m.prItems())
}

// openCount is the number shown on tab t's label, leaving out closed PRs.
func (m *model) openCount(t int) int {
	switch t {
	case tabInbox:
		return len(m.questions)
	case tabTodo:
		return m.todoOpen
	case tabWorkers:
		n := 0
		for _, w := range m.workers {
			if !w.Done {
				n++
			}
		}
		return n
	}
	n := 0
	for _, p := range m.prItems() {
		if !m.isClosed(p.URL) {
			n++
		}
	}
	return n
}

// groupByAuthor keeps each author's PRs together. Authors keep the order of
// their first PR, so whoever has the latest activity comes first.
func groupByAuthor(entries []Entry) []Entry {
	order := map[string]int{}
	for _, e := range entries {
		if _, ok := order[e.Author]; !ok {
			order[e.Author] = len(order)
		}
	}
	slices.SortStableFunc(entries, func(a, b Entry) int { return order[a.Author] - order[b.Author] })
	return entries
}

// requestedMe reports whether the PR asks us for a review by name.
func (m *model) requestedMe(e Entry) bool {
	return slices.ContainsFunc(m.statuses[e.URL].Reviewers, func(r Reviewer) bool {
		return r.State == "PENDING" && m.me != "" && strings.EqualFold(r.Name, m.me)
	})
}

// archiveSelected archives a review request. One that names us first asks
// for an optional comment to leave on the PR.
func (m *model) archiveSelected(e Entry) tea.Cmd {
	if !m.requestedMe(e) {
		return m.archive(e, "")
	}
	ta := newTextInput("Comment for the PR (optional), enter to unassign")
	ta.Focus()
	m.reviewInput = &reviewInput{e: e, ta: ta, archiving: true}
	return nil
}

// archive hides a review request for the rest of the session and, when it
// names us directly, takes us off the PR's requested reviewers and posts the
// user's comment, if they wrote one.
func (m *model) archive(e Entry, comment string) tea.Cmd {
	if m.archived == nil {
		m.archived = map[string]bool{}
	}
	m.archived[e.URL] = true
	m.reviews = slices.DeleteFunc(m.reviews, func(r Entry) bool { return r.URL == e.URL })
	ref := fmt.Sprintf("%s#%d", e.Repo, e.Number)
	if !m.requestedMe(e) {
		m.note("Hid " + ref + " (the request went to a team, so there was nothing to unassign)")
		return nil
	}
	login := m.me
	return func() tea.Msg {
		if err := removeReviewRequest(e, login); err != nil {
			return archivedMsg{ref: ref, err: err}
		}
		if comment == "" {
			return archivedMsg{ref: ref}
		}
		return archivedMsg{ref: ref, commented: true, err: postComment(e, comment)}
	}
}

// fetchReviews looks for PRs waiting on our review when it's time, or right
// away when force is set.
func (m *model) fetchReviews(force bool) tea.Cmd {
	if m.reviewsBusy || (!force && (time.Since(m.reviewsAt) < reviewEvery || time.Now().Before(m.pausedUntil))) {
		return nil
	}
	m.reviewsBusy = true
	return func() tea.Msg {
		entries, statuses, err := SearchReviewRequests()
		return reviewsMsg{entries, statuses, err}
	}
}

// fitWidth takes the next step of fitting the sidekick's width, every few
// seconds until it's right.
func (m *model) fitWidth() tea.Cmd {
	block := os.Getenv("REX_BLOCK")
	if block == "" || m.fitDone || m.fitBusy || time.Since(m.fitAt) < 2*time.Second {
		return nil
	}
	m.fitBusy, m.fitAt = true, time.Now()
	session, since := m.st.Session, m.fitSince
	return func() tea.Msg {
		done, resized := fitStep(session, block, since)
		return fitMsg{done, resized}
	}
}

// cursor returns the current tab's cursor, kept inside its list.
func (m *model) cursor() int {
	n := m.tabLen(m.tab)
	m.cursors[m.tab] = max(0, min(m.cursors[m.tab], n-1))
	return m.cursors[m.tab]
}

// switchesTabs reports whether k switches tabs even with a box open on the
// current tab. tab and shift+tab always do. h, l, the arrows, and the number
// keys do too, unless you're typing in the box (an answer's Other row, or a
// reply to a message) or the numbers pick one of a question's options.
func (m *model) switchesTabs(k string) bool {
	switch k {
	case "tab", "shift+tab":
		return true
	case "h", "l", "left", "right", "1", "2", "3", "4":
	default:
		return false
	}
	digit := k[0] >= '1' && k[0] <= '9'
	switch m.tab {
	case tabInbox:
		a := m.ans
		if a == nil {
			return true
		}
		if a.q.Info {
			return !a.replying
		}
		return !a.onOther() && !digit
	case tabTodo:
		return m.todoInput == nil
	case tabPRs:
		return m.reviewInput == nil
	}
	return true
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height

	case fitMsg:
		m.fitBusy, m.fitDone = false, msg.done
		if !msg.resized.IsZero() {
			m.fitSince = msg.resized
		}

	case tickMsg:
		cmds := []tea.Cmd{tick(), m.fitWidth()}
		if !m.st.ListModTime().Equal(m.listMod) {
			m.reload()
		}
		if !m.st.TodosModTime().Equal(m.todoMod) || (!m.todoWake.IsZero() && !time.Now().Before(m.todoWake)) {
			if m.reloadTodos() {
				// A scheduled task just came due.
				cmds = append(cmds, bell)
			}
		}
		if !m.st.QuestionsModTime().Equal(m.qMod) && m.reloadQuestions() {
			cmds = append(cmds, bell)
		}
		if !m.st.WorkersModTime().Equal(m.workerMod) {
			m.reloadWorkers()
		}
		cmds = append(cmds, m.poll(false), m.deliverAnswers(), m.fetchReviews(false), m.pollWorkers())
		if time.Since(m.flushedAt) > 30*time.Second {
			m.flushedAt = time.Now()
			cmds = append(cmds, m.flushInboxes())
		}
		return m, tea.Batch(cmds...)

	case fetchedMsg:
		var cmds []tea.Cmd
		for url, cur := range msg {
			if cmd := m.apply(url, cur); cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
		m.archiveDone()
		m.save()
		return m, tea.Batch(cmds...)

	case reviewsMsg:
		m.reviewsBusy, m.reviewsAt = false, time.Now()
		if msg.err != nil {
			if strings.Contains(strings.ToLower(msg.err.Error()), "rate limit") {
				m.pausedUntil = time.Now().Add(5 * time.Minute)
			}
			m.setError("looking for review requests: " + msg.err.Error())
			break
		}
		m.keepSelection(func() {
			m.reviews = groupByAuthor(slices.DeleteFunc(msg.entries, func(e Entry) bool { return m.archived[e.URL] }))
		})
		for url, s := range msg.statuses {
			m.statuses[url] = s
		}
		if in := m.reviewInput; in != nil && !slices.ContainsFunc(m.reviews, func(e Entry) bool { return e.URL == in.e.URL }) {
			m.reviewInput = nil
			m.note("The review request went away, so the review didn't start")
		}
		m.dropStalePicker()

	case archivedMsg:
		switch {
		case msg.err != nil && msg.commented:
			m.setError("removed your review request on " + msg.ref + ", but posting your comment failed: " + msg.err.Error())
		case msg.err != nil:
			m.setError("removing your review request on " + msg.ref + ": " + msg.err.Error())
		case msg.commented:
			m.note("Unassigned you from " + msg.ref + " and commented")
		default:
			m.note("Unassigned you from " + msg.ref)
		}

	case loginMsg:
		if msg.err != nil {
			m.setError("looking up GitHub login: " + msg.err.Error())
			break
		}
		m.me = msg.login

	case notifiedMsg:
		if msg.err != nil {
			m.setError("notify failed: " + msg.err.Error())
		}

	case deliveredMsg:
		m.delivering = false
		if len(msg.errs) > 0 {
			m.setError(strings.Join(msg.errs, "; "))
		}
		for _, e := range msg.blocks {
			if !slices.ContainsFunc(m.answerBlocks, func(b Entry) bool { return b.NotifyBlock == e.NotifyBlock }) {
				m.answerBlocks = append(m.answerBlocks, e)
			}
		}
		m.reloadQuestions()

	case todoSyncedMsg:
		m.reloadTodos()
		if msg.err != nil {
			m.setError(msg.err.Error())
		} else {
			m.note("Pulled your lists' git repos")
		}

	case mergedMsg:
		return m, m.merged(msg)

	case workersMsg:
		return m, m.gotWorkers(msg)

	case cmdErrMsg:
		if msg.err != nil {
			m.setError(msg.what + ": " + msg.err.Error())
		}

	case tea.KeyMsg:
		// A box open on this tab, such as a half-typed answer, takes the keys
		// except the ones that switch tabs. It belongs to its tab and is
		// still there when you come back.
		switching := m.switchesTabs(msg.String())
		if m.ans != nil && m.tab == tabInbox && !switching {
			return m, m.updateAnswer(msg)
		}
		if m.todoInput != nil && m.tab == tabTodo && !switching {
			return m, m.updateTodoInput(msg)
		}
		if m.todoMove != nil && m.tab == tabTodo && !switching {
			return m, m.updateMover(msg)
		}
		if m.todoDetail != nil && m.tab == tabTodo && !switching {
			return m, m.updateTodoDetail(msg)
		}
		if m.reviewInput != nil && m.tab == tabPRs && !switching {
			return m, m.updateReviewInput(msg)
		}
		if m.modelPicker != nil && m.tab == tabPRs && !switching {
			return m, m.updateModelPicker(msg)
		}
		if m.mergeConfirm != nil {
			// Any key but y cancels, tab included, so a merge never waits
			// on a tab you can't see.
			if cmd := m.updateMergeConfirm(msg); !switching {
				return m, cmd
			}
		}
		if m.tab == tabPRs {
			m.cursor()
			p, ok := m.selectedPR()
			switch msg.String() {
			case "m":
				if ok && p.group == groupMine {
					m.askMerge()
				}
				return m, nil
			case "c":
				if ok && (p.group == groupReview || p.group == groupMine && !m.isClosed(p.URL)) {
					m.openModelPicker(p.Entry)
				}
				return m, nil
			case "u", "e":
				if ok && p.group == groupReview {
					return m, m.archiveSelected(p.Entry)
				}
				return m, nil
			case "x", "d":
				if ok && p.group != groupReview {
					m.removePR(p.Entry)
				}
				return m, nil
			}
		}
		if m.tab == tabTodo {
			confirm := m.confirmList
			m.confirmList = ""
			if k := msg.String(); confirm != "" && k != "x" && k != "d" && m.errIsNote {
				m.errMsg = ""
			}
			switch msg.String() {
			case " ", "enter":
				if m.onNewGroupRow() {
					m.nameGroup(false)
					return m, nil
				}
				if row, ok := m.selectedAddRow(); ok {
					m.addTodo(row)
					return m, nil
				}
				if row, ok := m.selectedNewSubRow(); ok {
					m.nameSub(row)
					return m, nil
				}
				if h, ok := m.selectedHeading(); ok {
					m.toggleCollapsed(h)
					return m, nil
				}
				return m, m.toggleTodo()
			case "z":
				m.scheduleTodo()
				return m, nil
			case "i":
				m.openTodoDetail()
				return m, nil
			case "e":
				if _, ok := m.selectedHeading(); ok {
					m.nameGroup(true)
				}
				return m, nil
			case "g":
				if _, ok := m.selectedHeading(); ok {
					m.askBackend()
				}
				return m, nil
			case "r":
				return m, tea.Batch(m.syncTodos(), m.poll(true), m.fetchReviews(true))
			case "p":
				m.toggleStarted()
				return m, nil
			case "m":
				m.openMover()
				return m, nil
			case "c":
				m.askAgent()
				return m, nil
			case "x", "d":
				if h, ok := m.selectedHeading(); ok {
					m.removeList(h, confirm)
				} else {
					m.removeTodo()
				}
				return m, nil
			}
		}
		if m.tab == tabWorkers {
			v, ok := m.selectedWorker()
			switch msg.String() {
			case "enter", "f", " ":
				if ok {
					return m, m.focusWorker(v)
				}
				return m, nil
			case "x", "d":
				if ok {
					m.removeWorker(v)
				}
				return m, nil
			}
		}
		if m.tab == tabInbox {
			switch msg.String() {
			case "enter", "o", " ":
				if q, ok := m.selectedQuestion(); ok {
					m.openAnswer(q)
				}
				return m, nil
			case "x", "d":
				return m, m.dismissSelected()
			}
		}
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "j", "down":
			// A tall item scrolls to its end before the cursor moves on.
			if m.scroll < m.maxScroll {
				m.scroll++
			} else {
				m.cursors[m.tab] = m.cursor() + 1
			}
		case "k", "up":
			if m.scroll > 0 {
				m.scroll--
			} else {
				m.cursors[m.tab] = max(0, m.cursor()-1)
			}
		case "tab", "l", "right", "shift+tab", "h", "left":
			step := 1
			if k := msg.String(); k == "shift+tab" || k == "h" || k == "left" {
				step = -1
			}
			m.tab = (m.tab + step + numTabs) % numTabs
		case "1", "2", "3", "4":
			m.tab = int(msg.String()[0] - '1')
		case "r":
			return m, tea.Batch(m.poll(true), m.fetchReviews(true))
		case "enter":
			if e, ok := m.selected(); ok {
				_ = exec.Command("open", e.URL).Start()
			}
		}
	}
	return m, nil
}

// apply records a freshly fetched status and returns a command that tells the
// PR's pane about any change worth a ping.
func (m *model) apply(url string, cur Status) tea.Cmd {
	delete(m.inflight, url)
	old := m.statuses[url]
	now := time.Now()
	if strings.Contains(strings.ToLower(cur.Err), "rate limit") {
		m.pausedUntil = now.Add(5 * time.Minute)
		m.setError("GitHub rate limit hit, pausing checks for 5 minutes")
	}
	switch {
	case cur.Err != "" && !old.FetchedAt.IsZero():
		// Keep the last good status so a blip doesn't count as a change.
		old.Err = cur.Err
		cur = old
	case cur.Err == "" && !old.FetchedAt.IsZero() && sameStatus(old, cur):
		cur.LastChange = old.LastChange
	default:
		cur.LastChange = now
	}
	m.nextAt[url] = now.Add(pollInterval(m.interval, cur, now))
	changes := Diff(old, cur)
	m.statuses[url] = cur
	if len(changes) == 0 {
		return nil
	}
	e, ok := m.entry(url)
	if !ok {
		return nil
	}
	text := FormatNotice(e, cur.Title, changes)
	d, haveDispatcher := m.st.Dispatcher()
	return func() tea.Msg { return notifiedMsg{text, Notify(prNoticeTarget(e, d, haveDispatcher), text)} }
}

// flushInboxes types out notices sitting in inboxes that no agent is
// waiting on, once per pane.
func (m *model) flushInboxes() tea.Cmd {
	seen := map[string]bool{}
	var entries []Entry
	for _, e := range append(slices.Clone(m.entries), m.answerBlocks...) {
		if e.NotifyBlock != "" && !seen[e.NotifyBlock] {
			seen[e.NotifyBlock] = true
			entries = append(entries, e)
		}
	}
	return func() tea.Msg {
		for _, e := range entries {
			if err := flushStale(e); err != nil {
				return notifiedMsg{err: err}
			}
		}
		return nil
	}
}

func (m *model) entry(url string) (Entry, bool) {
	for _, e := range m.entries {
		if e.URL == url {
			return e, true
		}
	}
	return Entry{}, false
}

// selected returns the PR under the cursor, when the PRs tab is open.
func (m *model) selected() (Entry, bool) {
	if m.tab != tabPRs {
		return Entry{}, false
	}
	m.cursor()
	p, ok := m.selectedPR()
	return p.Entry, ok
}

func (m *model) setError(s string) {
	m.errMsg, m.errAt, m.errIsNote = s, time.Now(), false
}

// note shows s at the bottom for a minute, like an error but dimmed.
func (m *model) note(s string) {
	m.errMsg, m.errAt, m.errIsNote = s, time.Now(), true
}

// save writes statuses for PRs still on the list.
func (m *model) save() {
	keep := map[string]Status{}
	for _, e := range m.entries {
		if s, ok := m.statuses[e.URL]; ok {
			keep[e.URL] = s
		}
	}
	if err := m.st.SaveStatuses(keep); err != nil {
		m.setError("saving status: " + err.Error())
	}
}

func (m *model) View() string {
	w := m.width
	if w <= 0 {
		w = sidebarCols
	}

	right := ""
	if time.Now().Before(m.pausedUntil) {
		right = "paused"
	} else if len(m.inflight) > 0 {
		right = "refreshing…"
	} else if next, ok := m.nextCheck(); ok {
		right = "next " + shortDuration(time.Until(next))
	}
	var tabs []string
	for t := range numTabs {
		label := fmt.Sprintf("%s (%d)", tabNames[t], m.openCount(t))
		switch {
		case t == m.tab:
			tabs = append(tabs, lipgloss.NewStyle().Foreground(colWarn).Bold(true).Underline(true).Render(label))
		case t == tabWorkers && m.blockedWorkers() > 0:
			// So does a worker stuck on a prompt in its own tab.
			tabs = append(tabs, sBlocked.Render(label))
		case t == tabInbox && m.tabLen(t) > 0:
			// Unanswered questions stand out from any tab.
			tabs = append(tabs, sQuestion.Render(label))
		default:
			tabs = append(tabs, sDim.Render(label))
		}
	}
	tabLines := wrapItems(tabs, "  ", w-2)
	last := len(tabLines) - 1
	if gap := w - 1 - lipgloss.Width(tabLines[last]) - runewidth.StringWidth(right); gap >= 2 {
		tabLines[last] += strings.Repeat(" ", gap) + sDim.Render(right)
	} else if right != "" {
		tabLines = append(tabLines, strings.Repeat(" ", max(0, w-1-runewidth.StringWidth(right)))+sDim.Render(right))
	}
	header := " " + strings.Join(tabLines, "\n ") + "\n\n"

	var footer []string
	if m.errMsg != "" && time.Since(m.errAt) < time.Minute {
		style := lipgloss.NewStyle().Foreground(colClosed)
		if m.errIsNote {
			style = sDim
		}
		for _, l := range wrapWords(m.errMsg, w-2) {
			footer = append(footer, style.Render(" "+l))
		}
	}
	cur := m.cursor()
	hints := []string{"r refresh"}
	switch {
	case m.ans != nil && m.tab == tabInbox:
		hints = m.answerHints()
	case m.tab == tabInbox:
		hints = []string{"enter answer", "x dismiss"}
	case m.tab == tabWorkers:
		hints = []string{"f focus its tab", "x remove"}
	case m.reviewInput != nil && m.tab == tabPRs && m.reviewInput.archiving:
		hints = []string{"enter unassign", "esc cancel"}
	case m.reviewInput != nil && m.tab == tabPRs:
		hints = []string{"enter start review", "esc cancel"}
	case m.modelPicker != nil && m.tab == tabPRs:
		hints = []string{"j/k model", "esc cancel"}
	case m.mergeConfirm != nil:
		hints = []string{"y merge " + ref(*m.mergeConfirm), "any other key cancels"}
	case m.tab == tabPRs:
		hints = m.prHints()
	case m.tab == tabTodo && m.todoDetail != nil:
		hints = []string{"esc back", "c message"}
	case m.todoMove != nil:
		hints = []string{"j/k list", "enter move", "esc cancel"}
	case m.todoInput != nil && m.todoInput.asking:
		hints = []string{"enter send", "esc cancel"}
	case m.todoInput != nil:
		hints = []string{"enter save", "esc cancel"}
	case m.tab == tabTodo && m.confirmList != "":
		what := "the list"
		if strings.Contains(m.confirmList, "\x1f") {
			what = "the sub-group"
		}
		hints = []string{"x remove " + what, "any other key cancels"}
	case m.tab == tabTodo:
		hints = []string{"space done", "z snooze", "x remove", "c hand off"}
		if t, ok := m.selectedTodo(); ok && t.Mine {
			hints = append(hints, "m move", "i edit")
		} else if ok && t.Detail != "" {
			hints = append(hints, "i details")
		}
		if t, ok := m.selectedTodo(); ok && t.Mine && !t.Done {
			progress := "p in progress"
			if t.Started {
				progress = "p not in progress"
			}
			hints = slices.Insert(hints, slices.Index(hints, "z snooze"), progress)
		}
		if h, ok := m.selectedHeading(); ok {
			fold := "enter collapse"
			if h.collapsed {
				fold = "enter expand"
			}
			hints = []string{fold, "x remove list"}
			switch {
			case h.Sub != "":
				hints = []string{fold, "x remove sub-group", "e rename"}
			case h.Mine && h.Group != "":
				hints = append(hints, "e rename", "g git repo")
			}
			if m.todoBacks[h.Group].Git != "" && h.Mine && h.Sub == "" {
				hints = append(hints, "r pull")
			}
		}
		if _, ok := m.selectedAddRow(); ok {
			hints = []string{"enter new task"}
		}
		if m.onNewGroupRow() {
			hints = []string{"enter new group"}
		}
		if _, ok := m.selectedNewSubRow(); ok {
			hints = []string{"enter new sub-group"}
		}
	}
	// Enter and space are assumed, so their hints stay out of the footer.
	hints = slices.DeleteFunc(hints, func(h string) bool {
		return strings.HasPrefix(h, "enter ") || strings.HasPrefix(h, "space ")
	})
	for _, l := range wrapItems(hints, " · ", w-2) {
		footer = append(footer, sDim.Render(" "+l))
	}
	if len(footer) == 0 {
		// The padding below counts on a footer line. Without one the view runs
		// a line past the pane, and the terminal drops the tab bar to fit it.
		footer = []string{""}
	}

	var body strings.Builder
	var blocks []string
	avail := m.height - strings.Count(header, "\n") - len(footer) - 1
	switch {
	case m.ans != nil && m.tab == tabInbox:
		body.WriteString(m.scrolledAnswer(avail, w))
	case m.tab == tabTodo && m.todoDetail != nil:
		body.WriteString(m.renderTodoDetail(avail, w))
	case m.tab == tabTodo:
		blocks = m.todoBlocks(cur, w)
	case m.tab == tabInbox:
		blocks = m.inboxBlocks(cur, w)
	case m.tab == tabWorkers:
		blocks = m.workerBlocks(cur, w)
	default:
		blocks = m.prBlocks(cur, w)
	}
	if (m.ans == nil || m.tab != tabInbox) && (m.todoDetail == nil || m.tab != tabTodo) && len(blocks) == 0 {
		empty := "Nothing watched yet. Add one with `rex-sidekick pr add <PR>`."
		switch {
		case m.tab == tabInbox:
			empty = "Nothing from agents."
		case m.tab == tabWorkers:
			empty = "No workers. A dispatcher lists the agents it starts with `rex-sidekick worker add`."
		case len(m.entries) > 0:
			empty = fmt.Sprintf("Nothing here. Switch tabs with h/l or 1-%d.", numTabs)
		}
		for _, l := range wrapWords(empty, w-2) {
			body.WriteString(sDim.Render(" "+l) + "\n")
		}
	}

	// Items wrap to different heights. Start from the first one that still
	// leaves the cursor's item fully on screen.
	heights := make([]int, len(blocks))
	for i, b := range blocks {
		heights[i] = strings.Count(b, "\n")
	}
	first, used := 0, 0
	for i := 0; i <= cur && i < len(blocks); i++ {
		used += heights[i]
	}
	for used > avail && first < cur {
		used -= heights[first]
		first++
	}
	if item := [2]int{m.tab, cur}; item != m.scrolledItem {
		m.scrolledItem, m.scroll = item, 0
	}
	m.maxScroll = 0
	used = 0
	for i := first; i < len(blocks); i++ {
		if used+heights[i] > avail && i > cur {
			break
		}
		if i == cur && heights[i] > avail {
			body.WriteString(m.scrolledBlock(blocks[i], avail, w))
			break
		}
		body.WriteString(blocks[i])
		used += heights[i]
	}

	out := header + body.String()
	if pad := m.height - strings.Count(out, "\n") - len(footer); pad > 0 {
		out += strings.Repeat("\n", pad)
	}
	return out + strings.Join(footer, "\n")
}

// scrolledBlock shows the part of a block too tall for the screen that
// m.scroll points at, with a line at the bottom saying how much is left.
func (m *model) scrolledBlock(block string, avail, w int) string {
	lines := strings.Split(strings.TrimSuffix(block, "\n"), "\n")
	rows := max(1, avail-1) // one row for the hint
	m.maxScroll = max(0, len(lines)-rows)
	m.scroll = min(m.scroll, m.maxScroll)
	below := len(lines) - rows - m.scroll
	hint := fmt.Sprintf("↓ %d more lines (j)", below)
	switch {
	case below == 1:
		hint = "↓ 1 more line (j)"
	case below <= 0:
		hint = "↑ k to scroll back up"
	}
	if m.scroll > 0 && below > 0 {
		hint += " · ↑ (k)"
	}
	return strings.Join(lines[m.scroll:m.scroll+rows], "\n") + "\n" + sDim.Render(truncate(" "+hint, w)) + "\n"
}

// nextCheck returns the soonest time any open PR is due for a check.
func (m *model) nextCheck() (time.Time, bool) {
	var next time.Time
	for _, e := range m.entries {
		s := m.statuses[e.URL]
		if s.State == "MERGED" || s.State == "CLOSED" {
			continue
		}
		if t, ok := m.nextAt[e.URL]; ok && (next.IsZero() || t.Before(next)) {
			next = t
		}
	}
	return next, !next.IsZero()
}

func shortDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(max(0, d.Seconds())))
	}
	return fmt.Sprintf("%dm", int(d.Round(time.Minute).Minutes()))
}

// link makes text a clickable OSC 8 hyperlink to url.
func link(url, text string) string {
	return "\x1b]8;;" + url + "\x1b\\" + text + "\x1b]8;;\x1b\\"
}

// renderEntry draws one PR from the given PRs tab group, wrapped to w
// columns, followed by a blank line.
func (m *model) renderEntry(e Entry, group int, selected bool, w int) string {
	s, known := m.statuses[e.URL]
	title := e.Title
	if s.Title != "" {
		title = s.Title
	}

	icon, col := "⎇", colOpen
	switch {
	case !known:
		col = colDraft
	case s.State == "MERGED":
		col = colDraft
	case s.State == "CLOSED":
		icon, col = "⊘", colDraft
	case s.IsDraft:
		col = colDraft
	default:
		col = reviewColor(s)
	}

	bar := "  "
	titleStyle := lipgloss.NewStyle()
	if selected {
		bar = sSel.Render("▌ ")
		titleStyle = titleStyle.Bold(true)
	}
	const indent = "    "
	inner := max(10, w-len(indent))

	var lines []string
	for i, l := range wrapWords(title, inner) {
		lead := indent
		if i == 0 {
			lead = bar + lipgloss.NewStyle().Foreground(col).Render(icon) + " "
		}
		lines = append(lines, lead+link(e.URL, titleStyle.Render(l)))
	}

	repoName := e.Repo[strings.LastIndex(e.Repo, "/")+1:]
	parts := []string{link(e.URL, sDim.Underline(true).Render(fmt.Sprintf("%s #%d", repoName, e.Number)))}
	if known && s.Additions+s.Deletions > 0 {
		parts = append(parts, sAdd.Render("+"+commas(s.Additions))+" "+sDel.Render("−"+commas(s.Deletions)))
	}
	parts = append(parts, statusBadges(s, known)...)
	if m.reviewing[e.URL] {
		parts = append(parts, sQuestion.Render("Claude reviewing"))
	}
	for _, l := range wrapItems(parts, sDim.Render(" · "), inner) {
		lines = append(lines, indent+l)
	}

	for _, l := range reviewerLines(s, known, inner) {
		lines = append(lines, indent+l)
	}
	return strings.Join(lines, "\n") + "\n\n"
}

// reviewerLines shows each reviewer with a mark for their review state.
func reviewerLines(s Status, known bool, w int) []string {
	if !known {
		return nil
	}
	if len(s.Reviewers) == 0 {
		return []string{sDim.Render("no reviewers")}
	}
	var items []string
	for _, r := range s.Reviewers {
		mark, style := reviewMark(r.State)
		items = append(items, style.Render(mark)+" "+r.Name)
	}
	return wrapItems(items, "  ", w)
}

// wrapWords breaks plain text into lines of at most w columns, splitting
// words that are longer than a line.
func wrapWords(s string, w int) []string {
	w = max(w, 1)
	var lines []string
	line := ""
	for _, word := range strings.Fields(s) {
		for runewidth.StringWidth(word) > w {
			if line != "" {
				lines = append(lines, line)
				line = ""
			}
			head := runewidth.Truncate(word, w, "")
			if head == "" {
				// A wide character won't fit at all. Give it a line anyway.
				head = string([]rune(word)[:1])
			}
			lines = append(lines, head)
			word = word[len(head):]
		}
		switch {
		case line == "":
			line = word
		case runewidth.StringWidth(line)+1+runewidth.StringWidth(word) <= w:
			line += " " + word
		default:
			lines = append(lines, line)
			line = word
		}
	}
	if line != "" || len(lines) == 0 {
		lines = append(lines, line)
	}
	return lines
}

// wrapItems joins styled items with sep, starting a new line before an item
// that would pass w columns. An item only gets split when it is wider than a
// whole line.
func wrapItems(items []string, sep string, w int) []string {
	var lines []string
	line, used := "", 0
	sepW := lipgloss.Width(sep)
	for _, it := range items {
		iw := lipgloss.Width(it)
		if iw > w {
			if line != "" {
				lines = append(lines, line)
				line, used = "", 0
			}
			parts := strings.Split(ansi.Hardwrap(it, w, true), "\n")
			lines = append(lines, parts[:len(parts)-1]...)
			it = parts[len(parts)-1]
			iw = lipgloss.Width(it)
		}
		if line != "" && used+sepW+iw > w {
			lines = append(lines, line)
			line, used = "", 0
		}
		if line != "" {
			line += sep
			used += sepW
		}
		line += it
		used += iw
	}
	if line != "" {
		lines = append(lines, line)
	}
	return lines
}

func reviewMark(state string) (string, lipgloss.Style) {
	switch state {
	case "APPROVED":
		return "✓", sAdd
	case "CHANGES_REQUESTED":
		return "✗", sDel
	case "COMMENTED":
		return "◦", sDim
	case "DISMISSED":
		return "–", sDim
	}
	return "●", lipgloss.NewStyle().Foreground(colWarn)
}

// reviewColor is red when changes are requested, green when approved, and
// yellow while the PR waits for review. Repos that don't require reviews have
// no review decision, so it falls back to the reviewers' own states.
func reviewColor(s Status) lipgloss.Color {
	decision := s.ReviewDecision
	if decision == "" {
		for _, r := range s.Reviewers {
			switch r.State {
			case "CHANGES_REQUESTED":
				decision = r.State
			case "APPROVED":
				if decision == "" {
					decision = r.State
				}
			}
		}
	}
	switch decision {
	case "CHANGES_REQUESTED":
		return colClosed
	case "APPROVED":
		return colOpen
	}
	return colWarn
}

func statusBadges(s Status, known bool) []string {
	if !known {
		return []string{sDim.Render("loading…")}
	}
	var out []string
	if s.Err != "" {
		out = append(out, lipgloss.NewStyle().Foreground(colClosed).Render("⚠ gh error"))
	}
	switch s.State {
	case "MERGED":
		return append(out, lipgloss.NewStyle().Foreground(colMerged).Render("merged"))
	case "CLOSED":
		return append(out, lipgloss.NewStyle().Foreground(colClosed).Render("closed"))
	}
	if s.IsDraft {
		out = append(out, sDim.Render("draft"))
	}
	switch s.Checks {
	case "passing":
		out = append(out, sAdd.Render("✓ checks"))
	case "failing":
		out = append(out, sDel.Render(fmt.Sprintf("✗ %d failing", len(s.FailingChecks))))
	case "pending":
		out = append(out, lipgloss.NewStyle().Foreground(colWarn).Render("● checks"))
	}
	switch s.ReviewDecision {
	case "APPROVED":
		out = append(out, sAdd.Render("approved"))
	case "CHANGES_REQUESTED":
		out = append(out, sDel.Render("changes requested"))
	}
	if s.MergeState == "DIRTY" {
		out = append(out, sDel.Render("conflicts"))
	}
	return out
}

func truncate(s string, w int) string {
	if w <= 1 {
		return ""
	}
	return runewidth.Truncate(s, w, "…")
}

func commas(n int) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
