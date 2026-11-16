// Command rex-sidekick is a sidebar for Rex sessions. It runs in a background
// Rex tab and tells the agent that registered something when it changes.
// It watches pull requests and collects questions from agents.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

const usageText = `rex-sidekick: a sidebar for Rex sessions

Usage:
  rex-sidekick open           open the sidekick in a pane to the left of this one
                              if it isn't running (--dispatcher: and take the
                              user's own tasks when they hand them off)
  rex-sidekick ui             run the sidekick in this terminal

  rex-sidekick pr add [PR...] watch PRs (URL, owner/repo#N, number, or none for the
                              current branch) and open the sidekick if it isn't running
  rex-sidekick pr rm PR...    stop watching PRs
  rex-sidekick pr ls          list watched PRs and their last known status

  rex-sidekick ask QUESTION [OPTION...]
                              put a question in the sidekick's inbox for the user,
                              wait for the answer, and print it. The user can always
                              type their own answer instead of picking an option

  rex-sidekick tell MESSAGE   put a message in the sidekick's inbox for the user to
                              read and dismiss, such as a summary of finished work.
                              It has no options and doesn't wait

  rex-sidekick run COMMAND    ask the user to run a shell command. If they say yes,
                              it runs in a new pane under the sidekick, and you
                              get its exit status and the end of its output

  rex-sidekick withdraw [ID...]
                              take questions and messages back out of the inbox,
                              such as when the user answered in the thread instead

  rex-sidekick todo add TASK...
                              add tasks to this agent's list on the TODO tab, in
                              order, and print their IDs. With --mine, add them
                              to one of the user's own lists
  rex-sidekick todo update ID change a task
  rex-sidekick todo start ID...
                              mark tasks in progress
  rex-sidekick todo stop ID...
                              mark tasks not in progress
  rex-sidekick todo done ID...
                              mark tasks done
  rex-sidekick todo rm [ID...]
                              remove tasks
  rex-sidekick todo ls        list tasks this pane filed
  rex-sidekick todo show ID   print a task and its details, such as a plan
  rex-sidekick todo move ID LIST
                              move one of the user's tasks to another of their
                              lists, "My list" for theirs. A sub-group goes by
                              its list and path, like "Home / Basement". Tasks
                              waiting on it go along
  rex-sidekick todo backend [LIST git PATH | LIST local]
                              keep one of the user's lists in a git repo, which
                              commits every change, or back in this session.
                              With no arguments, print where lists are kept
  rex-sidekick todo sync      pull and push the git repos the user's lists use

  rex-sidekick worker add NAME --block ID
                              list a worker the dispatcher started on the
                              Workers tab. NAME is its Rex tab's name
  rex-sidekick worker update NAME
                              change a worker, or mark it done
  rex-sidekick worker rm NAME...
                              take workers off the Workers tab
  rex-sidekick worker ls      list workers and what each is doing: working,
                              idle, waiting on prompt (Claude Code waits on a
                              prompt in its pane), waiting on user (it asked in
                              the inbox), PR open, or done

  rex-sidekick wait           block until there are notices for this pane, print
                              them, and exit. While an agent keeps a wait running,
                              notices go here instead of being typed into the pane

Common flags:
  --session ID          Rex session the sidekick belongs to (default $REX_SESSION)

pr add flags:
  --notify-block ID     Rex pane to notify on changes (default $REX_BLOCK)
  --no-notify           watch without notifying any pane
  --no-open             don't open the sidekick

ask flags:
  --multi               let the user pick more than one option
  --detail TEXT         more context, shown when the user opens the question.
                        Markdown: headings, lists, **bold**, code spans and blocks
  --from NAME           who's asking (default the name of this Rex tab)
  --topic NAME          group the question with others under this name
                        (default the --from name, so one group per thread)
  --block ID            Rex pane asking (default $REX_BLOCK)
  --no-wait             return right away. The answer arrives later as a
                        [sidekick] message, like PR notices
  --no-open             don't open the sidekick

tell flags:
  --detail TEXT, --from NAME, --topic NAME, --block ID, --no-open
                        the same as for ask

run flags:
  --why TEXT            why you need it run, shown as the question
  --detail TEXT, --topic NAME, --block ID, --no-wait, --no-open
                        the same as for ask
  --cwd DIR             folder to run it in (default the current folder)

withdraw flags:
  --topic NAME          withdraw every question in this topic
  --all                 withdraw every question this pane asked (--block)

todo add flags:
  --list NAME           list to add to (default the name of this Rex tab)
  --mine LIST           add to one of the user's own lists instead, named as
                        for todo move: "My list", "Work", "Work / #launch".
                        The tasks are the user's from then on: marking them
                        done tells no agent, and todo ls leaves them out
  --after ID            the first task waits on task ID
  --chain               each task waits on the one before it
  --detail TEXT         more context, such as a plan in Markdown. The user
                        reads it by pressing i on the task
  --for-user            the list is for the user to act on, such as reminders,
                        so it shows in their section of the TODO tab
  --show-at TIME        hide the tasks until TIME: a span from now (90m, 2h,
                        3d, 1w), a day (tomorrow, fri, 2026-11-10), a clock
                        time (9:30, 3pm), both (tomorrow 9am), or RFC 3339
  --no-open             don't open the sidekick

todo update flags:
  --text TEXT, --detail TEXT
  --after ID            wait on task ID instead ("none" to stop waiting)
  --show-at TIME        hide the task until TIME instead ("none" to show it now)
  --reopen              mark a done task as not done
  --for-user[=false]    show the task's list in the user's section, or not

todo done flags:
  --note TEXT           what was done, shown under the task

todo rm flags:
  --done                remove this pane's finished tasks
  --all                 remove every task this pane filed

todo ls flags:
  --all                 list every agent's tasks, not just this pane's

todo flags:
  --block ID            Rex pane filing the tasks (default $REX_BLOCK)

worker add flags:
  --block ID            the pane Claude runs in (required)
  --window ID           its Rex tab (default the tab holding --block)
  --task TEXT           what it's doing, in one line
  --pr URL              a PR it's working on, besides the ones it registered
                        with pr add from its own pane (repeatable)
  --todo ID             the user's task it's working on

worker update flags:
  --task TEXT, --pr URL, --todo ID
                        the same as for add. --pr adds to the worker's PRs
  --rm-pr URL           unlink a PR (repeatable)
  --state STATE         done, or working to undo done. The sidekick works out
                        the other states itself

wait flags:
  --block ID            pane to wait for (default $REX_BLOCK)

open flags:
  --block ID            pane to open beside (default $REX_BLOCK; none opens a tab)
  --dispatcher          make that pane the session's dispatcher. Pressing m on
                        one of the user's own tasks hands it to the dispatcher

ui/open flags:
  --interval DUR        how often to poll GitHub (default 30s)
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usageText)
		os.Exit(2)
	}
	var err error
	switch cmd, args := os.Args[1], os.Args[2:]; cmd {
	case "open":
		err = cmdOpen(args)
	case "ui":
		err = cmdUI(args)
	case "pr":
		err = cmdPR(args)
	case "ask":
		err = cmdAsk(args)
	case "tell":
		err = cmdTell(args)
	case "withdraw":
		err = cmdWithdraw(args)
	case "run":
		err = cmdRun(args)
	case "run-pane":
		err = cmdRunPane(args)
	case "edit-pane":
		err = cmdEditPane(args)
	case "todo":
		err = cmdTodo(args)
	case "worker":
		err = cmdWorker(args)
	case "wait":
		err = cmdWait(args)
	case "help", "-h", "--help":
		fmt.Print(usageText)
	default:
		usageError("unknown command %q", cmd)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "rex-sidekick:", err)
		os.Exit(1)
	}
}

func usageError(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "rex-sidekick: "+format+"\n\n%s", append(args, usageText)...)
	os.Exit(2)
}

func cmdPR(args []string) error {
	if len(args) == 0 {
		usageError("pr needs a subcommand")
	}
	switch sub, rest := args[0], args[1:]; sub {
	case "add":
		return cmdPRAdd(rest)
	case "rm", "remove":
		return cmdPRRemove(rest)
	case "ls", "list":
		return cmdPRList(rest)
	default:
		usageError("unknown pr subcommand %q", sub)
	}
	return nil
}

func newFlags(name string) (*flag.FlagSet, *string) {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usageText) }
	session := fs.String("session", os.Getenv("REX_SESSION"), "")
	return fs, session
}

func cmdPRAdd(args []string) error {
	fs, session := newFlags("pr add")
	block := fs.String("notify-block", os.Getenv("REX_BLOCK"), "")
	noNotify := fs.Bool("no-notify", false, "")
	noOpen := fs.Bool("no-open", false, "")
	fs.Parse(args)
	st, err := OpenStore(*session)
	if err != nil {
		return err
	}
	refs := fs.Args()
	if len(refs) == 0 {
		refs = []string{""}
	}
	for _, ref := range refs {
		e, err := Resolve(ref)
		if err != nil {
			return err
		}
		e.AddedAt = time.Now()
		if !*noNotify {
			e.NotifySession, e.NotifyBlock = *session, *block
		}
		added, err := st.Add(e)
		if err != nil {
			return err
		}
		verb := "watching"
		if !added {
			verb = "already watching (updated)"
		}
		fmt.Printf("%s %s#%d %s\n", verb, e.Repo, e.Number, e.URL)
	}
	if *noOpen {
		return nil
	}
	opened, err := ensureOpen(st, os.Getenv("REX_BLOCK"), "")
	if opened {
		fmt.Println("opened the sidekick")
	}
	return err
}

func cmdPRRemove(args []string) error {
	fs, session := newFlags("pr rm")
	fs.Parse(args)
	st, err := OpenStore(*session)
	if err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return fmt.Errorf("pr rm needs at least one PR")
	}
	for _, ref := range fs.Args() {
		n, err := st.Remove(ref)
		if err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("not watching %s", ref)
		}
		fmt.Println("stopped watching", ref)
	}
	return nil
}

func cmdPRList(args []string) error {
	fs, session := newFlags("pr ls")
	fs.Parse(args)
	st, err := OpenStore(*session)
	if err != nil {
		return err
	}
	entries, err := st.List()
	if err != nil {
		return err
	}
	statuses := st.LoadStatuses()
	for _, e := range entries {
		s := statuses[e.URL]
		line := fmt.Sprintf("%s#%d\t%s", e.Repo, e.Number, e.URL)
		if !s.FetchedAt.IsZero() {
			line += "\t" + summaryLine(s)
		}
		fmt.Println(line)
	}
	if !st.UIRunning() {
		fmt.Fprintln(os.Stderr, "(sidekick not running; statuses may be stale. Run `rex-sidekick open`.)")
	}
	return nil
}

func cmdOpen(args []string) error {
	fs, session := newFlags("open")
	block := fs.String("block", os.Getenv("REX_BLOCK"), "")
	interval := fs.String("interval", "", "")
	dispatcher := fs.Bool("dispatcher", false, "")
	fs.Parse(args)
	st, err := OpenStore(*session)
	if err != nil {
		return err
	}
	if *dispatcher {
		// Recorded first, so it takes even when the sidekick is already up.
		if *block == "" {
			return fmt.Errorf("--dispatcher needs a pane: run it in Rex or pass --block")
		}
		d := Dispatcher{Session: st.Session, Block: *block, Name: tabName(st.Session, *block)}
		if err := st.SetDispatcher(d); err != nil {
			return err
		}
	}
	opened, err := ensureOpen(st, *block, *interval)
	if err != nil {
		return err
	}
	if opened {
		fmt.Println("opened the sidekick")
	} else {
		fmt.Println("sidekick already running")
	}
	return nil
}

// ensureOpen opens the sidekick unless it's running. It holds a lock until the
// new sidekick has taken its own lock, so two quick calls don't open two panes.
func ensureOpen(st *Store, block, interval string) (opened bool, err error) {
	err = st.withOpenLock(func() error {
		if st.UIRunning() {
			return nil
		}
		if err := OpenSidekick(st.Session, block, interval); err != nil {
			return err
		}
		opened = true
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
			if st.UIRunning() {
				return nil
			}
			time.Sleep(100 * time.Millisecond)
		}
		return fmt.Errorf("opened the sidekick, but it didn't start within 5s")
	})
	return opened, err
}

func cmdAsk(args []string) error {
	fs, session := newFlags("ask")
	multi := fs.Bool("multi", false, "")
	detail := fs.String("detail", "", "")
	from := fs.String("from", "", "")
	topic := fs.String("topic", "", "")
	block := fs.String("block", os.Getenv("REX_BLOCK"), "")
	noWait := fs.Bool("no-wait", false, "")
	noOpen := fs.Bool("no-open", false, "")
	pos := parseInterspersed(fs, args)
	if len(pos) == 0 || strings.TrimSpace(pos[0]) == "" {
		return fmt.Errorf("ask needs a question")
	}
	st, err := OpenStore(*session)
	if err != nil {
		return err
	}
	q := Question{
		ID:            newQuestionID(),
		From:          *from,
		Topic:         *topic,
		NotifySession: *session,
		NotifyBlock:   *block,
		Text:          pos[0],
		Detail:        *detail,
		Options:       pos[1:],
		Multi:         *multi,
		AskedAt:       time.Now(),
	}
	if q.From == "" {
		q.From = tabName(*session, *block)
	}
	if q.Topic == "" {
		q.Topic = q.From
	}
	return postQuestion(st, q, *block, *noWait, *noOpen)
}

func cmdTell(args []string) error {
	fs, session := newFlags("tell")
	detail := fs.String("detail", "", "")
	from := fs.String("from", "", "")
	topic := fs.String("topic", "", "")
	block := fs.String("block", os.Getenv("REX_BLOCK"), "")
	noOpen := fs.Bool("no-open", false, "")
	pos := parseInterspersed(fs, args)
	if len(pos) != 1 || strings.TrimSpace(pos[0]) == "" {
		return fmt.Errorf("tell needs one message (put more in --detail)")
	}
	st, err := OpenStore(*session)
	if err != nil {
		return err
	}
	q := Question{
		ID:            newQuestionID(),
		From:          *from,
		Topic:         *topic,
		NotifySession: *session,
		NotifyBlock:   *block,
		Text:          pos[0],
		Detail:        *detail,
		Info:          true,
		AskedAt:       time.Now(),
	}
	if q.From == "" {
		q.From = tabName(*session, *block)
	}
	if q.Topic == "" {
		q.Topic = q.From
	}
	if err := st.AddQuestion(q); err != nil {
		return err
	}
	fmt.Printf("told the user (%s)\n", q.ID)
	if !*noOpen {
		if _, err := ensureOpen(st, *block, ""); err != nil {
			fmt.Fprintln(os.Stderr, "rex-sidekick: opening the sidekick:", err)
		}
	}
	return nil
}

// postQuestion puts q in the inbox. Unless noWait is set, it waits for the
// answer and prints it.
func postQuestion(st *Store, q Question, block string, noWait, noOpen bool) error {
	if noWait {
		if err := st.AddQuestion(q); err != nil {
			return err
		}
		fmt.Printf("asked %s. The answer will arrive as a [sidekick] message.\n", q.ID)
	} else {
		fmt.Printf("asked %s, waiting for the answer\n", q.ID)
	}
	if !noOpen {
		if _, err := ensureOpen(st, block, ""); err != nil {
			fmt.Fprintln(os.Stderr, "rex-sidekick: opening the sidekick:", err)
		}
	}
	if noWait {
		return nil
	}
	// An agent that stops waiting no longer needs the answer, so take the
	// question back out of the inbox.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		<-sigs
		_, _ = st.Withdraw(func(x Question) bool { return x.ID == q.ID })
		os.Remove(st.askLockPath(q.ID))
		os.Exit(130)
	}()
	ans, err := st.Ask(q, 500*time.Millisecond)
	if errors.Is(err, errQuestionGone) {
		fmt.Println("The question was withdrawn before the user answered.")
		return nil
	}
	if err != nil {
		return err
	}
	fmt.Println(ans)
	return nil
}

func cmdWithdraw(args []string) error {
	fs, session := newFlags("withdraw")
	block := fs.String("block", os.Getenv("REX_BLOCK"), "")
	all := fs.Bool("all", false, "")
	topic := fs.String("topic", "", "")
	ids := parseInterspersed(fs, args)
	if len(ids) == 0 && !*all && *topic == "" {
		return fmt.Errorf("withdraw needs question IDs, --topic, or --all")
	}
	st, err := OpenStore(*session)
	if err != nil {
		return err
	}
	n, err := st.Withdraw(func(q Question) bool {
		switch {
		case slices.Contains(ids, q.ID):
			return true
		case *topic != "" && q.Topic == *topic:
			return !*all || q.NotifyBlock == *block
		case *all && *topic == "":
			return q.NotifyBlock == *block
		}
		return false
	})
	if err != nil {
		return err
	}
	if n == 0 && len(ids) > 0 {
		return fmt.Errorf("no such question: %s", strings.Join(ids, ", "))
	}
	fmt.Printf("withdrew %d question(s)\n", n)
	return nil
}

// parseInterspersed parses flags that may come before, between, or after the
// positional arguments, and returns the positional ones.
func parseInterspersed(fs *flag.FlagSet, args []string) []string {
	var pos []string
	for {
		fs.Parse(args)
		args = fs.Args()
		if len(args) == 0 {
			return pos
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

func cmdTodo(args []string) error {
	if len(args) == 0 {
		usageError("todo needs a subcommand")
	}
	sub, args := args[0], args[1:]
	fs, session := newFlags("todo " + sub)
	block := fs.String("block", os.Getenv("REX_BLOCK"), "")
	var (
		list, mine, after, detail, text  *string
		note                             *string
		showAt                           *string
		chain, noOpen, reopen, done, all *bool
		forUser                          *bool
	)
	switch sub {
	case "add":
		list = fs.String("list", "", "")
		mine = fs.String("mine", "", "")
		after = fs.String("after", "", "")
		detail = fs.String("detail", "", "")
		showAt = fs.String("show-at", "", "")
		chain = fs.Bool("chain", false, "")
		noOpen = fs.Bool("no-open", false, "")
		forUser = fs.Bool("for-user", false, "")
	case "update":
		text = fs.String("text", "", "")
		detail = fs.String("detail", "", "")
		after = fs.String("after", "", "")
		showAt = fs.String("show-at", "", "")
		reopen = fs.Bool("reopen", false, "")
		forUser = fs.Bool("for-user", false, "")
	case "done":
		note = fs.String("note", "", "")
	case "start", "stop":
	case "rm", "remove":
		done = fs.Bool("done", false, "")
		all = fs.Bool("all", false, "")
	case "ls", "list":
		all = fs.Bool("all", false, "")
	case "show", "move", "backend", "sync":
	default:
		usageError("unknown todo subcommand %q", sub)
	}
	pos := parseInterspersed(fs, args)
	st, err := OpenStore(*session)
	if err != nil {
		return err
	}
	ts, err := st.Todos()
	if err != nil {
		return err
	}

	switch sub {
	case "add":
		if len(pos) == 0 {
			return fmt.Errorf("todo add needs at least one task")
		}
		if err := validAfter(*after, ts); err != nil {
			return err
		}
		var at time.Time
		if *showAt != "" {
			if at, err = parseWhen(*showAt, time.Now()); err != nil {
				return err
			}
		}
		mineSet := false
		fs.Visit(func(f *flag.Flag) { mineSet = mineSet || f.Name == "mine" })
		var group, subGroup string
		if mineSet {
			if *list != "" || *forUser {
				return fmt.Errorf("--mine files to the user's own list, so it can't go with --list or --for-user")
			}
			if group, subGroup, err = st.myPlaceNamed(*mine); err != nil {
				return err
			}
			// No task waits on one in another list.
			if i := slices.IndexFunc(ts, func(t Todo) bool { return t.ID == *after }); i >= 0 &&
				(!ts[i].Mine || ts[i].Group != group || ts[i].Sub != subGroup) {
				return fmt.Errorf("--after %s: that task isn't in %s", *after, *mine)
			}
		}
		from := tabName(*session, *block)
		name := *list
		if name == "" {
			name = from
		}
		var add []Todo
		prev := *after
		for i, t := range pos {
			todo := Todo{ID: newTodoID(), List: name, From: from, NotifySession: *session, NotifyBlock: *block,
				Text: t, CreatedAt: time.Now(), ShowAt: at, ForUser: *forUser}
			if mineSet {
				// The user owns it, so marking it done tells no agent.
				todo = Todo{ID: todo.ID, From: from, Text: t, CreatedAt: todo.CreatedAt, ShowAt: at}
			}
			if i == 0 || *chain {
				todo.After = prev
			}
			if len(pos) == 1 {
				todo.Detail = *detail
			}
			add = append(add, todo)
			prev = todo.ID
		}
		if mineSet {
			err = st.AddMyTodos(group, subGroup, add)
		} else {
			err = st.AddTodos(add)
		}
		if err != nil {
			return err
		}
		for _, t := range add {
			fmt.Printf("%s  %s\n", t.ID, t.Text)
		}
		if !*noOpen {
			if _, err := ensureOpen(st, *block, ""); err != nil {
				fmt.Fprintln(os.Stderr, "rex-sidekick: opening the sidekick:", err)
			}
		}
		return nil

	case "update":
		if len(pos) != 1 {
			return fmt.Errorf("todo update needs one task ID")
		}
		return st.UpdateTodo(pos[0], func(t *Todo, all []Todo) error {
			if *text != "" {
				t.Text = *text
			}
			if *detail != "" {
				t.Detail = *detail
			}
			switch *after {
			case "":
			case "none":
				t.After = ""
			default:
				if *after == t.ID {
					return fmt.Errorf("a task can't wait on itself")
				}
				if err := validAfter(*after, all); err != nil {
					return err
				}
				t.After = *after
			}
			switch *showAt {
			case "":
			case "none", "now":
				t.ShowAt = time.Time{}
			default:
				at, err := parseWhen(*showAt, time.Now())
				if err != nil {
					return err
				}
				t.ShowAt = at
			}
			if *reopen {
				t.Done, t.DoneAt, t.DoneBy, t.DoneNote = false, time.Time{}, "", ""
			}
			fs.Visit(func(f *flag.Flag) {
				if f.Name == "for-user" {
					t.ForUser = *forUser
				}
			})
			return nil
		})

	case "done":
		if len(pos) == 0 {
			return fmt.Errorf("todo done needs task IDs")
		}
		for _, id := range pos {
			err := st.UpdateTodo(id, func(t *Todo, _ []Todo) error {
				t.Done, t.DoneAt, t.DoneBy, t.DoneNote = true, time.Now(), "agent", *note
				t.Started = false
				return nil
			})
			if err != nil {
				return err
			}
		}
		return nil

	case "start", "stop":
		if len(pos) == 0 {
			return fmt.Errorf("todo %s needs task IDs", sub)
		}
		for _, id := range pos {
			err := st.UpdateTodo(id, func(t *Todo, _ []Todo) error {
				if t.Done && sub == "start" {
					return fmt.Errorf("%s is done. Reopen it first with todo update %s --reopen", id, id)
				}
				t.Started = sub == "start"
				return nil
			})
			if err != nil {
				return err
			}
		}
		return nil

	case "rm", "remove":
		if len(pos) == 0 && !*done && !*all {
			return fmt.Errorf("todo rm needs task IDs, --done, or --all")
		}
		n, err := st.RemoveTodos(func(t Todo) bool {
			ours := !t.Mine && t.NotifyBlock == *block
			return slices.Contains(pos, t.ID) || (*all && ours) || (*done && ours && t.Done)
		})
		if err != nil {
			return err
		}
		fmt.Printf("removed %d task(s)\n", n)
		return nil

	case "show":
		if len(pos) != 1 {
			return fmt.Errorf("todo show needs one task ID")
		}
		i := slices.IndexFunc(ts, func(t Todo) bool { return t.ID == pos[0] })
		if i < 0 {
			return fmt.Errorf("%w: %s", errNoTodo, pos[0])
		}
		fmt.Print(todoShow(ts[i]))
		return nil

	case "move":
		if len(pos) != 2 {
			return fmt.Errorf("todo move takes a task ID and the list to move it to")
		}
		group, sub, err := st.myPlaceNamed(pos[1])
		if err != nil {
			return err
		}
		n, err := st.MoveTodo(pos[0], group, sub)
		if err != nil {
			return err
		}
		fmt.Printf("moved %d task(s)\n", n)
		return nil

	case "backend":
		switch {
		case len(pos) == 0:
			backs, err := st.TodoBackends()
			if err != nil {
				return err
			}
			for _, name := range sortedKeys(backs) {
				fmt.Printf("%s\tgit %s\n", name, backs[name].Git)
			}
			return nil
		case len(pos) == 2 && pos[1] == "local":
			return st.SetTodoBackend(pos[0], todoBackend{})
		case len(pos) == 3 && pos[1] == "git":
			return st.SetTodoBackend(pos[0], todoBackend{Git: pos[2]})
		}
		return fmt.Errorf("todo backend takes a list and git PATH, or a list and local")

	case "sync":
		return st.SyncTodos()

	default: // ls
		if !*all {
			ts = slices.DeleteFunc(ts, func(t Todo) bool { return t.Mine || t.NotifyBlock != *block })
		}
		for _, l := range todoLines(ts) {
			fmt.Println(l)
		}
		return nil
	}
}

func cmdWait(args []string) error {
	fs := flag.NewFlagSet("wait", flag.ExitOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usageText) }
	block := fs.String("block", os.Getenv("REX_BLOCK"), "")
	fs.Parse(args)
	if *block == "" {
		return fmt.Errorf("no Rex pane: run inside Rex or pass --block")
	}
	b, err := openInbox(*block)
	if err != nil {
		return err
	}
	lines, err := b.Wait(time.Second)
	if errors.Is(err, errAlreadyWaiting) {
		// Not a failure. The other wait will deliver the notices.
		fmt.Println(err)
		return nil
	}
	if err != nil {
		return err
	}
	fmt.Println(strings.Join(lines, "\n"))
	return nil
}

func cmdUI(args []string) error {
	fs, session := newFlags("ui")
	interval := fs.Duration("interval", 30*time.Second, "")
	fs.Parse(args)
	st, err := OpenStore(*session)
	if err != nil {
		return err
	}
	lock, err := st.LockUI()
	if err != nil {
		return err
	}
	defer lock.Close()
	_, err = tea.NewProgram(newModel(st, *interval), tea.WithAltScreen()).Run()
	return err
}

func summaryLine(s Status) string {
	if s.Err != "" {
		return "error: " + s.Err
	}
	state := s.State
	if s.IsDraft && state == "OPEN" {
		state = "DRAFT"
	}
	out := fmt.Sprintf("%s checks:%s", state, s.Checks)
	if s.ReviewDecision != "" {
		out += " review:" + humanReview(s.ReviewDecision)
	}
	if s.MergeState == "DIRTY" {
		out += " conflicts"
	}
	if len(s.Reviewers) > 0 {
		var rs []string
		for _, r := range s.Reviewers {
			rs = append(rs, r.Name+"("+strings.ToLower(r.State)+")")
		}
		out += " reviewers:" + strings.Join(rs, ",")
	}
	return out
}
