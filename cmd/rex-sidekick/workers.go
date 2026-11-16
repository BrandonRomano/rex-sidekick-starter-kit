package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Worker is an agent session the dispatcher started in its own Rex tab. The
// dispatcher registers it with `rex-sidekick worker add`, and the Workers tab
// shows what it's doing.
type Worker struct {
	Name   string `json:"name"` // its Rex tab's name
	Window string `json:"window"`
	Block  string `json:"block"` // the pane Claude runs in
	Task   string `json:"task,omitempty"`
	// PRs the dispatcher linked by hand. PRs the worker registered from its
	// own pane are linked without this.
	PRs       []string  `json:"prs,omitempty"`
	Todo      string    `json:"todo,omitempty"` // the user's task it's working on
	Done      bool      `json:"done,omitempty"`
	AddedAt   time.Time `json:"added_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (s *Store) workersPath() string { return filepath.Join(s.dir, s.key+".workers.json") }

// withWorkers runs fn on the worker list while holding its lock, and saves the
// list if fn changed it.
func (s *Store) withWorkers(fn func(ws []Worker) ([]Worker, bool, error)) error {
	return withFileLock(s.workersPath()+".lock", func() error {
		var ws []Worker
		if err := readJSON(s.workersPath(), &ws); err != nil {
			return err
		}
		ws, changed, err := fn(ws)
		if err != nil || !changed {
			return err
		}
		return writeJSON(s.workersPath(), ws)
	})
}

func (s *Store) Workers() ([]Worker, error) {
	var out []Worker
	err := s.withWorkers(func(ws []Worker) ([]Worker, bool, error) {
		out = ws
		return ws, false, nil
	})
	return out, err
}

func (s *Store) WorkersModTime() time.Time {
	fi, err := os.Stat(s.workersPath())
	if err != nil {
		return time.Time{}
	}
	return fi.ModTime()
}

// AddWorker adds w, or replaces the worker with the same name.
func (s *Store) AddWorker(w Worker) error {
	return s.withWorkers(func(ws []Worker) ([]Worker, bool, error) {
		if i := slices.IndexFunc(ws, func(x Worker) bool { return x.Name == w.Name }); i >= 0 {
			w.AddedAt = ws[i].AddedAt
			ws[i] = w
			return ws, true, nil
		}
		return append(ws, w), true, nil
	})
}

var errNoWorker = fmt.Errorf("no such worker")

func (s *Store) UpdateWorker(name string, fn func(w *Worker) error) error {
	return s.withWorkers(func(ws []Worker) ([]Worker, bool, error) {
		i := slices.IndexFunc(ws, func(x Worker) bool { return x.Name == name })
		if i < 0 {
			return ws, false, fmt.Errorf("%w: %s", errNoWorker, name)
		}
		if err := fn(&ws[i]); err != nil {
			return ws, false, err
		}
		ws[i].UpdatedAt = time.Now()
		return ws, true, nil
	})
}

func (s *Store) RemoveWorkers(match func(Worker) bool) (int, error) {
	n := 0
	err := s.withWorkers(func(ws []Worker) ([]Worker, bool, error) {
		before := len(ws)
		ws = slices.DeleteFunc(ws, match)
		n = before - len(ws)
		return ws, n > 0, nil
	})
	return n, err
}

// The states a worker can be in, most urgent first.
const (
	stPrompt   = "waiting on prompt" // Claude Code waits on a prompt in the worker's pane
	stQuestion = "waiting on user"   // it asked the user something in the inbox
	stWorking  = "working"
	stPR       = "PR open" // nothing running, with a PR still open
	stIdle     = "idle"
	stDone     = "done"
)

// paneState is what a worker's pane shows.
type paneState struct {
	Prompt string // what the prompt Claude Code waits on asks, if there is one
	Busy   bool   // Claude is in the middle of a turn
}

var (
	// The hint under a Claude Code prompt that waits for a choice: a
	// permission request, a question from the agent, or a folder to trust.
	promptFooterRe = regexp.MustCompile(`(?i)esc to cancel|enter to (confirm|select)`)
	// The line Claude Code shows while it works: a spinner glyph, a word
	// ending in "…", and the elapsed time in parentheses.
	spinnerRe = regexp.MustCompile(`^[^\p{L}\p{N}\s]\s+\S.*…\s*\(`)
)

// readPane works out from a capture of a Claude Code pane, as plain text,
// whether Claude is waiting on a prompt or in the middle of a turn.
func readPane(screen string) paneState {
	lines := strings.Split(strings.TrimRight(screen, "\n"), "\n")
	for i := range lines {
		lines[i] = strings.TrimSpace(lines[i])
	}
	var st paneState
	for i := len(lines) - 1; i >= max(0, len(lines)-12); i-- {
		if spinnerRe.MatchString(lines[i]) {
			st.Busy = true
			break
		}
	}

	// The prompt's footer is among the last few lines.
	footer, seen := -1, 0
	for i := len(lines) - 1; i >= 0 && seen < 4; i-- {
		if lines[i] == "" {
			continue
		}
		seen++
		if promptFooterRe.MatchString(lines[i]) {
			footer = i
			break
		}
	}
	if footer < 0 {
		return st
	}
	// Above it is the selected choice, and above that the rule the prompt
	// starts under.
	sel := -1
	for i := footer - 1; i >= max(0, footer-40); i-- {
		if strings.HasPrefix(lines[i], "❯ ") {
			sel = i
			break
		}
	}
	if sel < 0 {
		return st
	}
	top := 0
	for i := sel - 1; i >= 0; i-- {
		if strings.HasPrefix(lines[i], "───") {
			top = i + 1
			break
		}
	}
	st.Prompt = promptSummary(lines[top:sel])
	if st.Prompt == "" {
		st.Prompt = "a prompt"
	}
	st.Busy = false
	return st
}

// promptSummary sums up a prompt from the lines above its choices: what it's
// about (its first line, like "Bash command"), the command or other detail set
// off between dashed rules, and the question it asks.
func promptSummary(lines []string) string {
	var kind, detail, question string
	inDetail := false
	for _, l := range lines {
		switch {
		case strings.HasPrefix(l, "╌"):
			inDetail = !inDetail
		case l == "":
		case kind == "":
			kind = strings.TrimSuffix(strings.TrimLeft(l, "☐☒ "), ":")
		case inDetail && detail == "":
			detail = l
		case strings.Contains(l, "?"):
			// The question's sentence, without what comes after it.
			q := l[:strings.LastIndex(l, "?")+1]
			if i := max(strings.LastIndex(q, ". "), strings.LastIndex(q, ": ")); i >= 0 {
				q = q[i+2:]
			}
			question = q
		}
	}
	out := kind
	if detail != "" {
		out += ": " + detail
	}
	if question != "" && question != kind {
		if out != "" {
			out += " — "
		}
		out += question
	}
	return out
}

// capturePaneText returns what a pane shows, as plain text. A variable so
// tests can stand in for Rex.
var capturePaneText = func(session, block string) (string, error) {
	out, err := exec.Command("rex", "capture", "-s", session, "-b", block, "--trim").Output()
	return string(out), err
}

// liveWindows returns the IDs of the session's Rex tabs. A variable so tests
// can stand in for Rex.
var liveWindows = func(session string) (map[string]bool, error) {
	out, err := exec.Command("rex", "window", "ls", "-s", session, "--json").Output()
	if err != nil {
		return nil, err
	}
	var w struct {
		Windows []struct {
			ID string `json:"window_id"`
		} `json:"windows"`
	}
	if err := json.Unmarshal(out, &w); err != nil {
		return nil, err
	}
	ids := map[string]bool{}
	for _, win := range w.Windows {
		ids[win.ID] = true
	}
	return ids, nil
}

// blockWindow returns the Rex tab holding block.
func blockWindow(session, block string) (string, error) {
	out, err := rexCmd("block", "inspect", "-s", session, block, "--json")
	if err != nil {
		return "", fmt.Errorf("rex block inspect: %s", strings.TrimSpace(string(out)))
	}
	var b struct {
		Block struct {
			WindowID string `json:"window_id"`
		} `json:"block"`
	}
	if err := json.Unmarshal(out, &b); err != nil || b.Block.WindowID == "" {
		return "", fmt.Errorf("rex block inspect: no tab for %s", block)
	}
	return b.Block.WindowID, nil
}

// workerView is a worker with what the sidekick knows about it.
type workerView struct {
	Worker
	State  string
	Prompt string  // what it's waiting on, in the waiting on prompt state
	Linked []Entry // its PRs
	// Its steps on the TODO tab, and how many are done.
	Steps, StepsDone int
}

// viewWorker works out a worker's state from its pane, the inbox, the watch
// list, and the TODO tab. A worker whose pane hasn't been read yet counts as
// working.
func viewWorker(w Worker, pane paneState, read bool, qs []Question, watched []Entry, ts []Todo) workerView {
	v := workerView{Worker: w, Prompt: pane.Prompt}
	for _, e := range watched {
		if (w.Block != "" && e.NotifyBlock == w.Block) || slices.Contains(w.PRs, e.URL) {
			v.Linked = append(v.Linked, e)
		}
	}
	open := len(v.Linked) > 0
	for _, u := range w.PRs {
		if !slices.ContainsFunc(v.Linked, func(e Entry) bool { return e.URL == u }) {
			v.Linked = append(v.Linked, entryFromURL(u))
		}
	}
	for _, t := range ts {
		if !t.Mine && w.Block != "" && t.NotifyBlock == w.Block {
			v.Steps++
			if t.Done {
				v.StepsDone++
			}
		}
	}
	asked := slices.ContainsFunc(qs, func(q Question) bool {
		return !q.Info && q.Answer == nil && w.Block != "" && q.NotifyBlock == w.Block
	})
	switch {
	case pane.Prompt != "":
		v.State = stPrompt
	case asked:
		v.State = stQuestion
	case w.Done:
		v.State = stDone
	case pane.Busy || !read:
		v.State = stWorking
	case open:
		v.State = stPR
	default:
		v.State = stIdle
	}
	return v
}

// entryFromURL makes a watch list entry for a PR URL, without asking GitHub.
func entryFromURL(u string) Entry {
	e := Entry{URL: u, Repo: u}
	if m := urlRe.FindStringSubmatch(u); m != nil {
		e.Repo = m[1]
		fmt.Sscan(m[2], &e.Number)
	}
	return e
}

// prURL turns a PR URL or owner/repo#N into a URL.
func prURL(ref string) (string, error) {
	ref = strings.TrimSuffix(strings.TrimSpace(ref), "/")
	if urlRe.MatchString(ref) {
		return ref, nil
	}
	if m := shortRe.FindStringSubmatch(ref); m != nil {
		return fmt.Sprintf("https://github.com/%s/pull/%s", m[1], m[2]), nil
	}
	return "", fmt.Errorf("%q isn't a PR URL or owner/repo#N", ref)
}

// stringsFlag collects a flag given more than once.
type stringsFlag []string

func (f *stringsFlag) String() string     { return strings.Join(*f, ",") }
func (f *stringsFlag) Set(v string) error { *f = append(*f, v); return nil }

func cmdWorker(args []string) error {
	if len(args) == 0 {
		usageError("worker needs a subcommand")
	}
	sub, args := args[0], args[1:]
	fs, session := newFlags("worker " + sub)
	var (
		window, block, task, todo, state *string
		prs, unPRs                       stringsFlag
	)
	switch sub {
	case "add":
		window = fs.String("window", "", "")
		block = fs.String("block", "", "")
		task = fs.String("task", "", "")
		todo = fs.String("todo", "", "")
		fs.Var(&prs, "pr", "")
	case "update":
		task = fs.String("task", "", "")
		todo = fs.String("todo", "", "")
		state = fs.String("state", "", "")
		fs.Var(&prs, "pr", "")
		fs.Var(&unPRs, "rm-pr", "")
	case "rm", "remove", "ls", "list":
	default:
		usageError("unknown worker subcommand %q", sub)
	}
	pos := parseInterspersed(fs, args)
	st, err := OpenStore(*session)
	if err != nil {
		return err
	}
	toURLs := func(refs []string) ([]string, error) {
		var out []string
		for _, r := range refs {
			u, err := prURL(r)
			if err != nil {
				return nil, err
			}
			out = append(out, u)
		}
		return out, nil
	}

	switch sub {
	case "add":
		if len(pos) != 1 || strings.TrimSpace(pos[0]) == "" {
			return fmt.Errorf("worker add needs one name: the worker's tab name")
		}
		if *block == "" {
			return fmt.Errorf("worker add needs --block, the pane the worker runs in")
		}
		if *window == "" {
			if *window, err = blockWindow(st.Session, *block); err != nil {
				return err
			}
		}
		urls, err := toURLs(prs)
		if err != nil {
			return err
		}
		now := time.Now()
		w := Worker{Name: pos[0], Window: *window, Block: *block, Task: oneLine(*task),
			PRs: urls, Todo: *todo, AddedAt: now, UpdatedAt: now}
		if err := st.AddWorker(w); err != nil {
			return err
		}
		fmt.Println("added worker", w.Name)
		return nil

	case "update":
		if len(pos) != 1 {
			return fmt.Errorf("worker update needs one worker name")
		}
		add, err := toURLs(prs)
		if err != nil {
			return err
		}
		drop, err := toURLs(unPRs)
		if err != nil {
			return err
		}
		return st.UpdateWorker(pos[0], func(w *Worker) error {
			if *task != "" {
				w.Task = oneLine(*task)
			}
			fs.Visit(func(f *flag.Flag) {
				if f.Name == "todo" {
					w.Todo = *todo
				}
			})
			switch *state {
			case "":
			case "done":
				w.Done = true
			case "working":
				w.Done = false
			default:
				return fmt.Errorf("--state is done or working. The sidekick works out the rest from the worker's pane, inbox and PRs")
			}
			for _, u := range add {
				if !slices.Contains(w.PRs, u) {
					w.PRs = append(w.PRs, u)
				}
			}
			w.PRs = slices.DeleteFunc(w.PRs, func(u string) bool { return slices.Contains(drop, u) })
			return nil
		})

	case "rm", "remove":
		if len(pos) == 0 {
			return fmt.Errorf("worker rm needs worker names")
		}
		n, err := st.RemoveWorkers(func(w Worker) bool { return slices.Contains(pos, w.Name) })
		if err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("%w: %s", errNoWorker, strings.Join(pos, ", "))
		}
		fmt.Printf("removed %d worker(s)\n", n)
		return nil

	default: // ls
		ws, err := dropClosedWorkers(st)
		if err != nil {
			return err
		}
		qs, _ := st.Questions()
		watched, _ := st.List()
		ts, _ := st.Todos()
		for _, w := range ws {
			screen, err := capturePaneText(st.Session, w.Block)
			v := viewWorker(w, readPane(screen), err == nil, qs, watched, ts)
			fmt.Println(workerLine(v))
		}
		return nil
	}
}

// dropClosedWorkers takes workers whose Rex tab has closed off the list, and
// returns the rest. When Rex can't list the tabs, it keeps them all.
func dropClosedWorkers(st *Store) ([]Worker, error) {
	live, err := liveWindows(st.Session)
	if err == nil {
		if _, err := st.RemoveWorkers(func(w Worker) bool { return !live[w.Window] }); err != nil {
			return nil, err
		}
	}
	return st.Workers()
}

// workerLine is one worker in `worker ls`.
func workerLine(v workerView) string {
	parts := []string{v.Name, v.State}
	if v.Prompt != "" {
		parts = append(parts, "prompt: "+v.Prompt)
	}
	if v.Task != "" {
		parts = append(parts, "task: "+v.Task)
	}
	for _, e := range v.Linked {
		parts = append(parts, "pr: "+e.URL)
	}
	if v.Todo != "" {
		parts = append(parts, "todo: "+v.Todo)
	}
	if v.Steps > 0 {
		parts = append(parts, fmt.Sprintf("plan: %d/%d done", v.StepsDone, v.Steps))
	}
	return strings.Join(parts, "\t")
}

// oneLine joins text onto one line.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
