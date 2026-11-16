package main

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"
)

const (
	runYes = "Run it"
	runNo  = "Don't run it"
)

// RunResult is what happened when the user ran an agent's command.
type RunResult struct {
	Exit   int    `json:"exit"`
	Killed bool   `json:"killed,omitempty"` // the pane closed before it finished
	Output string `json:"output,omitempty"` // the end of what it printed
}

func (r RunResult) String() string {
	msg := fmt.Sprintf("The user ran the command. It exited with status %d.", r.Exit)
	if r.Killed {
		msg = "The user ran the command, but its pane closed before it finished."
	}
	if r.Output != "" {
		msg += " Last output:\n" + r.Output
	}
	return msg
}

// rexSplit runs `rex split`. Tests swap it out.
var rexSplit = func(args ...string) ([]byte, error) {
	return exec.Command("rex", append([]string{"split"}, args...)...).CombinedOutput()
}

// runOutputLines is how much of a command's output goes back to the agent.
const runOutputLines = 40

func cmdRun(args []string) error {
	fs, session := newFlags("run")
	why := fs.String("why", "", "")
	detail := fs.String("detail", "", "")
	topic := fs.String("topic", "", "")
	cwd := fs.String("cwd", "", "")
	block := fs.String("block", os.Getenv("REX_BLOCK"), "")
	noWait := fs.Bool("no-wait", false, "")
	noOpen := fs.Bool("no-open", false, "")
	pos := parseInterspersed(fs, args)
	command := strings.TrimSpace(strings.Join(pos, " "))
	if command == "" {
		return fmt.Errorf("run needs a command")
	}
	if *cwd == "" {
		*cwd, _ = os.Getwd()
	}
	st, err := OpenStore(*session)
	if err != nil {
		return err
	}
	q := Question{
		ID:            newQuestionID(),
		Topic:         *topic,
		NotifySession: *session,
		NotifyBlock:   *block,
		Text:          *why,
		Detail:        *detail,
		Options:       []string{runYes, runNo},
		Command:       command,
		Cwd:           *cwd,
		AskedAt:       time.Now(),
	}
	if q.Text == "" {
		q.Text = "Can you run this command?"
	}
	q.From = tabName(*session, *block)
	if q.Topic == "" {
		q.Topic = q.From
	}
	return postQuestion(st, q, *block, *noWait, *noOpen)
}

// runCommand opens a pane under the sidekick that runs q's command, and
// records the pane on the question. The pane reports back when it's done.
func (m *model) runCommand(q Question) {
	self, err := os.Executable()
	if err != nil {
		m.setError(err.Error())
		return
	}
	args := []string{"-s", m.st.Session, "--split=below", "--label", "Run: " + truncate(q.Command, 40), "--json"}
	if b := os.Getenv("REX_BLOCK"); b != "" {
		args = append(args, "-b", b)
	}
	if q.Cwd != "" {
		args = append(args, "--cwd", q.Cwd)
	}
	args = append(args, "--", self, "run-pane", "--session", m.st.Session, q.ID)
	out, err := rexSplit(args...)
	if err != nil {
		m.setError("rex split: " + strings.TrimSpace(string(out)))
		return
	}
	var created struct {
		BlockIDs []string `json:"block_ids"`
	}
	if json.Unmarshal(out, &created) == nil && len(created.BlockIDs) > 0 {
		if err := m.st.SetRunBlock(q.ID, created.BlockIDs[0]); err != nil {
			m.setError(err.Error())
		}
	}
	m.note("Running it in a new pane")
	m.reloadQuestions()
}

// cmdRunPane runs a question's command in the pane the sidekick opened for
// it, sends the result to the agent, and then leaves a shell open so the user
// can look around and close the pane when they're done.
func cmdRunPane(args []string) error {
	fs, session := newFlags("run-pane")
	fs.Parse(args)
	if fs.NArg() != 1 {
		return fmt.Errorf("run-pane needs a question ID")
	}
	id := fs.Arg(0)
	st, err := OpenStore(*session)
	if err != nil {
		return err
	}
	qs, err := st.Questions()
	if err != nil {
		return err
	}
	i := slices.IndexFunc(qs, func(q Question) bool { return q.ID == id })
	if i < 0 || qs[i].Answer != nil {
		fmt.Println("That request is gone, so there's nothing to run.")
		return keepShell("")
	}
	q := qs[i]
	shell := cmp.Or(os.Getenv("SHELL"), "/bin/sh")
	fmt.Printf("\x1b[2m%s asked you to run this in %s:\x1b[0m\n$ %s\n\n", fromName(q), q.Cwd, q.Command)
	before := len(capturePane(*session))

	// The command gets ctrl+c from the terminal. Catching the signals here,
	// rather than ignoring them, leaves them at their defaults in the command.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGQUIT)
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP, syscall.SIGTERM)

	cmd := exec.Command(shell, "-c", q.Command)
	cmd.Dir = q.Cwd
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	res := RunResult{}
	if err := cmd.Start(); err != nil {
		fmt.Println(err)
		res.Exit = 127
	} else {
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case err = <-done:
		case <-hup:
			// The pane is closing. Report what we can before we go.
			_ = cmd.Process.Signal(syscall.SIGHUP)
			res.Killed = true
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			res.Exit = exit.ExitCode()
		}
	}
	signal.Stop(sigs)
	lines := capturePane(*session)
	if before <= len(lines) {
		lines = lines[before:]
	}
	if len(lines) > runOutputLines {
		lines = lines[len(lines)-runOutputLines:]
	}
	res.Output = strings.TrimSpace(strings.Join(lines, "\n"))
	if err := st.AnswerQuestion(id, Answer{Choices: []string{runYes}, Ran: &res, At: time.Now()}); err != nil {
		fmt.Println("Couldn't send the result:", err)
	}
	if res.Killed {
		return nil
	}
	fmt.Printf("\n\x1b[2mExited with status %d. Sent that to %s.\nThis pane stays open. Type exit to close it.\x1b[0m\n", res.Exit, fromName(q))
	return keepShell(q.Cwd)
}

// capturePane returns the lines this pane shows so far.
func capturePane(session string) []string {
	b := os.Getenv("REX_BLOCK")
	if b == "" {
		return nil
	}
	out, err := exec.Command("rex", "block", "capture", "-s", session, "-b", b, "--unwrap", "--trim").Output()
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimRight(string(out), "\n"), "\n")
}

// keepShell replaces this process with the user's shell, so the pane stays
// open until they close it.
func keepShell(dir string) error {
	shell := cmp.Or(os.Getenv("SHELL"), "/bin/sh")
	if dir != "" {
		_ = os.Chdir(dir)
	}
	return syscall.Exec(shell, []string{shell, "-l"}, os.Environ())
}
