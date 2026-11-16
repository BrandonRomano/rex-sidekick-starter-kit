package main

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var (
	sQuestion = lipgloss.NewStyle().Foreground(colWarn)
	sInfo     = lipgloss.NewStyle().Foreground(lipgloss.Color("#58a6ff"))
	sTopic    = lipgloss.NewStyle().Foreground(lipgloss.Color("#8b949e")).Bold(true)
)

func topicOf(q Question) string {
	if q.Topic != "" {
		return q.Topic
	}
	return fromName(q)
}

// answerView is the screen for answering one question. Its rows are the
// question's options followed by an Other row, where the user types their own
// answer.
type answerView struct {
	q       Question
	cursor  int // len(q.Options) is the Other row
	checked map[int]bool
	other   textarea.Model
	// On a message, the reply box is open: other holds a reply for the agent.
	replying bool
	// The screen scrolls when the question is taller than the pane. top is
	// the first line shown, and rows are where each row starts and ends, as
	// of the last draw.
	top, height int
	rows        [][2]int
}

// showCursor scrolls the cursor's row into view.
func (a *answerView) showCursor() {
	if a.cursor >= len(a.rows) || a.height == 0 {
		return
	}
	r := a.rows[a.cursor]
	if r[1] > a.top+a.height {
		a.top = r[1] - a.height
	}
	a.top = min(a.top, r[0])
}

func (a *answerView) onOther() bool { return a.cursor == len(a.q.Options) }

func (a *answerView) setCursor(i int) {
	if a.q.Info {
		return
	}
	a.cursor = max(0, min(i, len(a.q.Options)))
	a.showCursor()
	if a.onOther() {
		a.other.Focus()
	} else {
		a.other.Blur()
	}
}

type deliveredMsg struct {
	errs   []string
	blocks []Entry // panes we sent answers to
}

type cmdErrMsg struct {
	what string
	err  error
}

// reloadQuestions reads the inbox and reports whether a question arrived
// since the last read.
func (m *model) reloadQuestions() (arrived bool) {
	mod := m.st.QuestionsModTime() // before the read, like reload
	qs, err := m.st.Questions()
	if err != nil {
		m.setError("reading inbox: " + err.Error())
		return false
	}
	m.qMod = mod
	// Not selectedQuestion: the cursor stays put while you're on another tab.
	var sel Question
	cur := m.cursors[tabInbox]
	hadSel := cur < len(m.questions)
	if hadSel {
		sel = m.questions[cur]
	}
	known := map[string]bool{}
	for _, q := range m.questions {
		known[q.ID] = true
	}
	var open []Question
	m.hasAnswered = false
	for _, q := range qs {
		if q.Answer != nil {
			m.hasAnswered = true
			continue
		}
		if m.qLoaded && !known[q.ID] {
			arrived = true
		}
		open = append(open, q)
	}
	// Keep each topic together, in the order its first question came in.
	order := map[string]int{}
	for _, q := range open {
		if _, ok := order[topicOf(q)]; !ok {
			order[topicOf(q)] = len(order)
		}
	}
	slices.SortStableFunc(open, func(a, b Question) int { return order[topicOf(a)] - order[topicOf(b)] })
	m.questions, m.qLoaded = open, true
	if hadSel {
		if i := slices.IndexFunc(open, func(q Question) bool { return q.ID == sel.ID }); i >= 0 {
			m.cursors[tabInbox] = i
		}
	}
	if m.ans != nil && !slices.ContainsFunc(open, func(q Question) bool { return q.ID == m.ans.q.ID }) {
		m.note(fmt.Sprintf("%s withdrew %q", fromName(m.ans.q), truncate(m.ans.q.Text, 40)))
		m.ans = nil
	}
	return arrived
}

// topicCount reports how many open questions share q's topic, and q's place
// among them, counting from 1.
func (m *model) topicCount(q Question) (pos, total int) {
	for _, o := range m.questions {
		if topicOf(o) == topicOf(q) {
			total++
			if o.ID == q.ID {
				pos = total
			}
		}
	}
	return pos, total
}

// bell rings the terminal bell so Rex can flag the sidekick's pane.
func bell() tea.Msg {
	_, _ = os.Stdout.WriteString("\a")
	return nil
}

// deliverAnswers sends answers whose `ask` stopped waiting to the asking
// agent's pane.
func (m *model) deliverAnswers() tea.Cmd {
	if !m.hasAnswered || m.delivering {
		return nil
	}
	m.delivering = true
	st := m.st
	return func() tea.Msg {
		qs, err := st.TakeOrphanAnswers()
		var msg deliveredMsg
		if err != nil {
			msg.errs = append(msg.errs, "sending answers: "+err.Error())
		}
		for _, q := range qs {
			e := Entry{NotifySession: q.NotifySession, NotifyBlock: q.NotifyBlock}
			if e.NotifyBlock == "" {
				continue
			}
			if err := Notify(e, FormatAnswerNotice(q)); err != nil {
				msg.errs = append(msg.errs, fmt.Sprintf("sending answer to %s: %v", q.From, err))
			}
			msg.blocks = append(msg.blocks, e)
		}
		return msg
	}
}

func (m *model) openAnswer(q Question) {
	if q.RunBlock != "" {
		if err := focusPane(m.st.Session, q.RunBlock); err != nil {
			m.setError(err.Error())
		}
		return
	}
	m.ans = &answerView{q: q, checked: map[int]bool{}, other: newTextInput("Type your own answer")}
	m.ans.setCursor(0)
}

// submit saves the answer. With next set, it then opens the next question
// in the same topic, if there is one.
func (m *model) submit(a Answer, next bool) tea.Cmd {
	a.At = time.Now()
	q := m.ans.q
	m.ans = nil
	if err := m.st.AnswerQuestion(q.ID, a); err != nil {
		m.setError("saving answer: " + err.Error())
	}
	m.reloadQuestions()
	if i := slices.IndexFunc(m.questions, func(o Question) bool { return topicOf(o) == topicOf(q) }); next && i >= 0 {
		m.cursors[tabInbox] = i
		m.openAnswer(m.questions[i])
	}
	return m.deliverAnswers()
}

func (m *model) dismissSelected() tea.Cmd {
	q, ok := m.selectedQuestion()
	if !ok {
		return nil
	}
	if q.Info {
		m.dismissInfo(q, false)
		return nil
	}
	m.ans = &answerView{q: q}
	return m.submit(Answer{Dismissed: true}, false)
}

// dismissInfo takes an info item out of the inbox. Nobody is waiting on it,
// so the agent isn't told. With next set, it then opens the next item in the
// same topic, if there is one.
func (m *model) dismissInfo(q Question, next bool) {
	m.ans = nil
	if _, err := m.st.Withdraw(func(o Question) bool { return o.ID == q.ID }); err != nil {
		m.setError("dismissing: " + err.Error())
	}
	m.reloadQuestions()
	if i := slices.IndexFunc(m.questions, func(o Question) bool { return topicOf(o) == topicOf(q) }); next && i >= 0 {
		m.cursors[tabInbox] = i
		m.openAnswer(m.questions[i])
	}
}

func (m *model) selectedQuestion() (Question, bool) {
	if m.tab != tabInbox {
		return Question{}, false
	}
	if i := m.cursor(); i < len(m.questions) {
		return m.questions[i], true
	}
	return Question{}, false
}

// sendAnswer submits whatever the user picked or typed.
func (m *model) sendAnswer() tea.Cmd {
	a := m.ans
	opts := a.q.Options
	other := strings.Join(strings.Fields(a.other.Value()), " ")
	var choices []string
	switch {
	case a.q.Multi:
		for i, o := range opts {
			if a.checked[i] {
				choices = append(choices, o)
			}
		}
		// Enter on an option with nothing checked picks that option.
		if len(choices) == 0 && other == "" && !a.onOther() {
			choices = []string{opts[a.cursor]}
		}
	case a.onOther():
	default:
		choices, other = []string{opts[a.cursor]}, ""
	}
	if len(choices) == 0 && other == "" {
		return nil
	}
	if a.q.Command != "" && other == "" && slices.Equal(choices, []string{runYes}) {
		m.ans = nil
		m.runCommand(a.q)
		return nil
	}
	return m.submit(Answer{Choices: choices, Other: other}, true)
}

func (m *model) updateAnswer(msg tea.KeyMsg) tea.Cmd {
	a := m.ans
	key := msg.String()
	switch key {
	case "ctrl+c":
		return tea.Quit
	case "esc":
		if a.replying {
			a.closeReply()
			return nil
		}
		m.ans = nil
		return nil
	}
	if a.q.Info && a.replying {
		switch key {
		case "enter":
			return m.sendReply()
		case "up":
			a.scrollBy(-1)
			return nil
		case "down":
			a.scrollBy(1)
			return nil
		}
		a.top = maxTop // keep the box in view as you type
		var cmd tea.Cmd
		a.other, cmd = a.other.Update(msg)
		return cmd
	}
	if a.q.Info {
		switch key {
		case "enter", "x", "d":
			m.dismissInfo(a.q, true)
		case "c":
			m.startReply()
		case "q":
			m.ans = nil
		case "down", "j":
			a.scrollBy(1)
		case "up", "k":
			a.scrollBy(-1)
		}
		return nil
	}
	switch key {
	case "up":
		a.moveUp()
		return nil
	case "down":
		a.moveDown()
		return nil
	case "enter":
		return m.sendAnswer()
	}
	if a.onOther() {
		var cmd tea.Cmd
		a.other, cmd = a.other.Update(msg)
		return cmd
	}
	switch key {
	case "k":
		a.moveUp()
	case "j":
		a.moveDown()
	case " ", "x":
		if a.q.Multi {
			a.checked[a.cursor] = !a.checked[a.cursor]
		}
	case "q":
		m.ans = nil
	default:
		if len(key) == 1 && key[0] >= '1' && key[0] <= '9' {
			i := int(key[0] - '1')
			if i <= len(a.q.Options) {
				a.setCursor(i)
				if a.q.Multi && !a.onOther() {
					a.checked[i] = !a.checked[i]
				}
			}
		}
	}
	return nil
}

// maxTop scrolls the answer screen to its end. scrolledAnswer clamps it.
const maxTop = 1 << 30

// startReply opens a box on a message for a reply to the agent that sent it.
func (m *model) startReply() {
	a := m.ans
	if a.q.NotifyBlock == "" {
		m.note("That message has no agent pane to reply to")
		return
	}
	a.replying = true
	a.other.Reset()
	a.other.Placeholder = "Reply to " + fromName(a.q)
	a.other.Focus()
	a.top = maxTop
}

func (a *answerView) closeReply() {
	a.replying = false
	a.other.Reset()
	a.other.Blur()
}

// sendReply sends the typed reply to the agent's pane. The message stays
// open, and the agent gets it like any other sidekick notice.
func (m *model) sendReply() tea.Cmd {
	a := m.ans
	text := strings.Join(strings.Fields(a.other.Value()), " ")
	if text == "" {
		return nil
	}
	a.closeReply()
	if err := m.st.AddReply(a.q.ID, text); err != nil {
		m.setError("saving reply: " + err.Error())
	}
	a.q.Replies = append(a.q.Replies, text)
	m.reloadQuestions()
	a.top = maxTop
	e := Entry{NotifySession: a.q.NotifySession, NotifyBlock: a.q.NotifyBlock}
	if !slices.ContainsFunc(m.answerBlocks, func(b Entry) bool { return b.NotifyBlock == e.NotifyBlock }) {
		m.answerBlocks = append(m.answerBlocks, e)
	}
	m.note("Sent to " + fromName(a.q))
	msg := FormatInfoReply(a.q, text)
	return func() tea.Msg { return notifiedMsg{msg, Notify(e, msg)} }
}

// moveUp scrolls up to the detail above the first row, then moves the
// cursor up.
func (a *answerView) moveUp() {
	if len(a.rows) > 0 && (a.cursor == 0 || a.rows[a.cursor][0] < a.top) && a.top > 0 {
		a.scrollBy(-1)
		return
	}
	a.setCursor(a.cursor - 1)
}

// moveDown scrolls down until the cursor's row is fully shown, then moves the
// cursor down.
func (a *answerView) moveDown() {
	if len(a.rows) > 0 && a.rows[a.cursor][1] > a.top+a.height {
		a.scrollBy(1)
		return
	}
	a.setCursor(a.cursor + 1)
}

func (a *answerView) scrollBy(n int) { a.top = max(0, a.top+n) }

func ago(t time.Time) string {
	return agoDuration(time.Since(t))
}

// agoDuration spells an age as days, hours and minutes, leaving out the
// units that are zero: "28m ago", "3h 28m ago", "1d 5m ago".
func agoDuration(d time.Duration) string {
	if d < time.Minute {
		return "just now"
	}
	mins := int(d / time.Minute)
	var parts []string
	for _, u := range []struct {
		n    int
		name string
	}{{mins / (24 * 60), "d"}, {mins / 60 % 24, "h"}, {mins % 60, "m"}} {
		if u.n > 0 {
			parts = append(parts, fmt.Sprintf("%d%s", u.n, u.name))
		}
	}
	return strings.Join(parts, " ") + " ago"
}

func questionKind(q Question) string {
	switch {
	case q.Info:
		return "info"
	case q.RunBlock != "":
		return "running"
	case q.Command != "":
		return "run a command"
	case len(q.Options) == 0:
		return "type an answer"
	case q.Multi:
		return "pick any"
	}
	return "pick one"
}

func fromName(q Question) string {
	if q.From != "" {
		return q.From
	}
	return "an agent"
}

// metaItems describes who asked q and when. The asker is left out when the
// topic already names them.
func metaItems(q Question) []string {
	var items []string
	if fromName(q) != topicOf(q) {
		items = append(items, sQuestion.Render(fromName(q)))
	}
	return append(items, sDim.Render(ago(q.AskedAt)), sDim.Render(questionKind(q)))
}

// inboxBlocks draws the inbox, one block per question, with each agent's
// (or topic's) questions in a box titled with its name.
func (m *model) inboxBlocks(cur, w int) []string {
	inner := max(10, w-4)
	blocks := make([]string, len(m.questions))
	for i, q := range m.questions {
		first := i == 0 || topicOf(q) != topicOf(m.questions[i-1])
		last := i+1 == len(m.questions) || topicOf(m.questions[i+1]) != topicOf(q)
		block := m.renderQuestion(q, i == cur, inner)
		if last {
			block = strings.TrimSuffix(block, "\n")
		}
		block = boxSides(block, w)
		if first {
			_, total := m.topicCount(q)
			top := boxTop(fmt.Sprintf("%s (%d)", topicOf(q), total), w) + "\n"
			if i > 0 {
				top = "\n" + top
			}
			block = top + block
		}
		if last {
			block += boxBottom(w) + "\n"
		}
		blocks[i] = block
	}
	return blocks
}

// renderQuestion draws one question in the inbox list, followed by a blank line.
func (m *model) renderQuestion(q Question, selected bool, w int) string {
	bar, titleStyle := "  ", lipgloss.NewStyle()
	if selected {
		bar, titleStyle = sSel.Render("▌ "), titleStyle.Bold(true)
	}
	const indent = "    "
	inner := max(10, w-len(indent))
	var lines []string
	for i, l := range wrapWords(q.Text, inner) {
		lead := indent
		if i == 0 {
			mark := sQuestion.Render("?")
			if q.Info {
				mark = sInfo.Render("i")
			}
			lead = bar + mark + " "
		}
		lines = append(lines, lead+titleStyle.Render(l))
	}
	for _, l := range wrapItems(metaItems(q), sDim.Render(" · "), inner) {
		lines = append(lines, indent+l)
	}
	lines = append(lines, commandLines(q.Command, indent, inner)...)
	if q.RunBlock != "" {
		return strings.Join(lines, "\n") + "\n\n"
	}
	var opts []string
	for i, o := range q.Options {
		opts = append(opts, sDim.Render(fmt.Sprintf("%d. %s", i+1, o)))
	}
	for _, l := range wrapItems(opts, "  ", inner) {
		lines = append(lines, indent+l)
	}
	return strings.Join(lines, "\n") + "\n\n"
}

// renderAnswer draws the answer screen.
func (m *model) renderAnswer(w int) string {
	a := m.ans
	inner := max(10, w-2)
	var b strings.Builder
	head := topicOf(a.q)
	if pos, total := m.topicCount(a.q); total > 1 {
		head += fmt.Sprintf(" · %d of %d", pos, total)
	}
	for _, l := range wrapWords(head, inner) {
		b.WriteString(" " + sTopic.Render(l) + "\n")
	}
	for _, l := range wrapItems(metaItems(a.q), sDim.Render(" · "), inner) {
		b.WriteString(" " + l + "\n")
	}
	for _, l := range wrapWords(a.q.Text, inner) {
		b.WriteString(" " + lipgloss.NewStyle().Bold(true).Render(l) + "\n")
	}
	if a.q.Command != "" {
		b.WriteString("\n")
		for _, l := range commandLines(a.q.Command, " ", inner) {
			b.WriteString(l + "\n")
		}
		if a.q.Cwd != "" {
			for _, l := range wrapHard("in "+a.q.Cwd, inner) {
				b.WriteString(" " + sDim.Render(l) + "\n")
			}
		}
	}
	if a.q.Detail != "" {
		b.WriteString("\n")
		for _, l := range renderMarkdown(a.q.Detail, inner) {
			b.WriteString(strings.TrimRight(" "+l, " ") + "\n")
		}
	}
	if a.q.Info {
		m.renderReplies(&b, inner)
		return b.String()
	}
	b.WriteString("\n")
	a.rows = make([][2]int, len(a.q.Options)+1)

	row := func(i int, label string) {
		a.rows[i][0] = strings.Count(b.String(), "\n")
		defer func() { a.rows[i][1] = strings.Count(b.String(), "\n") }()
		bar := "  "
		if i == a.cursor {
			bar = sSel.Render("▌ ")
		}
		mark := ""
		if a.q.Multi && i < len(a.q.Options) {
			mark = sDim.Render("[ ] ")
			if a.checked[i] {
				mark = sAdd.Render("[x] ")
			}
		} else if a.q.Multi {
			mark = "    "
		}
		text := fmt.Sprintf("%d. %s", i+1, label)
		style := lipgloss.NewStyle()
		if i == a.cursor {
			style = style.Bold(true)
		}
		pad := strings.Repeat(" ", 2+lipgloss.Width(mark))
		for j, l := range wrapWords(text, max(10, w-len(pad))) {
			lead := pad
			if j == 0 {
				lead = bar + mark
			}
			b.WriteString(lead + style.Render(l) + "\n")
		}
	}
	for i, o := range a.q.Options {
		row(i, o)
	}
	n := len(a.q.Options)
	label := "Other"
	if n == 0 {
		label = "Your answer"
	}
	row(n, label)

	pad := 5
	if a.q.Multi {
		pad = 9
	}
	a.other.SetWidth(max(10, w-pad-1))
	height := 1
	if a.onOther() || a.other.Value() != "" {
		height = 3
	}
	a.other.SetHeight(height)
	for _, l := range strings.Split(a.other.View(), "\n") {
		b.WriteString(strings.Repeat(" ", pad) + l + "\n")
	}
	a.rows[n][1] = strings.Count(b.String(), "\n")
	return b.String()
}

// renderReplies draws the replies sent from a message, and the reply box
// when it's open.
func (m *model) renderReplies(b *strings.Builder, inner int) {
	a := m.ans
	for _, r := range a.q.Replies {
		b.WriteString("\n")
		for i, l := range wrapWords("You: "+r, max(10, inner-2)) {
			lead := "   "
			if i == 0 {
				lead = " " + sSel.Render("↳") + " "
			}
			b.WriteString(lead + sDim.Render(l) + "\n")
		}
	}
	if !a.replying {
		return
	}
	b.WriteString("\n")
	for _, l := range wrapWords("Reply to "+fromName(a.q), inner) {
		b.WriteString(" " + sTopic.Render(l) + "\n")
	}
	a.other.SetWidth(max(10, inner-3))
	a.other.SetHeight(3)
	for _, l := range strings.Split(a.other.View(), "\n") {
		b.WriteString("   " + l + "\n")
	}
}

// scrolledAnswer shows the part of the answer screen that fits in avail
// rows.
func (m *model) scrolledAnswer(avail, w int) string {
	a := m.ans
	lines := strings.Split(strings.TrimSuffix(m.renderAnswer(w), "\n"), "\n")
	if len(lines) <= avail {
		a.top, a.height = 0, avail
		return strings.Join(lines, "\n") + "\n"
	}
	a.height = max(1, avail-1) // one row for the hint
	if a.onOther() {
		// The box grows when it gets the cursor, so keep all of it in view.
		a.showCursor()
	}
	a.top = max(0, min(a.top, len(lines)-a.height))
	below := len(lines) - a.height - a.top
	// While you type a reply, j and k are text, so the arrows scroll.
	down, up := "j", "k"
	if a.replying {
		down, up = "↓", "↑"
	}
	var hint string
	switch {
	case below > 1:
		hint = fmt.Sprintf("↓ %d more lines (%s)", below, down)
	case below == 1:
		hint = fmt.Sprintf("↓ 1 more line (%s)", down)
	default:
		hint = fmt.Sprintf("↑ %s to scroll back up", up)
	}
	if a.top > 0 && below > 0 {
		hint += fmt.Sprintf(" · ↑ (%s)", up)
	}
	return strings.Join(lines[a.top:a.top+a.height], "\n") + "\n" + sDim.Render(truncate(" "+hint, w)) + "\n"
}

func (m *model) answerHints() []string {
	switch {
	case m.ans.q.Info && m.ans.replying:
		return []string{"enter send", "esc cancel"}
	case m.ans.q.Info && m.ans.q.NotifyBlock != "":
		return []string{"c reply", "x dismiss", "esc back"}
	case m.ans.q.Info:
		return []string{"x dismiss", "esc back"}
	case m.ans.onOther():
		return []string{"enter send", "↑ options", "esc back"}
	case m.ans.q.Multi:
		return []string{"space toggle", "enter send", "esc back"}
	}
	return []string{"enter choose", "esc back"}
}

// commandLines shows a command in full, wrapped but never cut short, since
// the user is deciding whether to run exactly this.
func commandLines(command, indent string, w int) []string {
	if command == "" {
		return nil
	}
	var out []string
	for i, l := range wrapHard("$ "+command, w) {
		if i > 0 {
			l = "  " + l
		}
		out = append(out, indent+sQuestion.Render(l))
	}
	return out
}

// wrapHard wraps s at w columns, breaking inside words when it has to, and
// keeps its own line breaks.
func wrapHard(s string, w int) []string {
	w = max(4, w-2)
	var out []string
	for _, line := range strings.Split(s, "\n") {
		r := []rune(line)
		for len(r) > w {
			out = append(out, string(r[:w]))
			r = r[w:]
		}
		out = append(out, string(r))
	}
	return out
}
