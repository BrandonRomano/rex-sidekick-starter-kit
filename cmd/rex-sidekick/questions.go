package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
)

// Question is something an agent asked the user through the sidekick's inbox,
// or, with Info set, something it wants the user to know.
type Question struct {
	ID   string `json:"id"`
	From string `json:"from,omitempty"` // the asking agent's Rex tab name
	// Questions with the same topic are grouped together in the inbox.
	Topic string `json:"topic,omitempty"`
	// The Rex pane to send the answer to if `ask` is no longer waiting.
	NotifySession string   `json:"notify_session,omitempty"`
	NotifyBlock   string   `json:"notify_block,omitempty"`
	Text          string   `json:"text"`
	Detail        string   `json:"detail,omitempty"`
	Options       []string `json:"options,omitempty"`
	Multi         bool     `json:"multi,omitempty"` // pick any number of options
	// Info marks a message from `rex-sidekick tell`. It has no options, and
	// nobody waits on it: the user reads it and dismisses it.
	Info bool `json:"info,omitempty"`
	// Replies the user sent the agent from a message, oldest first.
	Replies []string `json:"replies,omitempty"`
	// A shell command the agent wants the user to run, from `rex-sidekick run`,
	// and the folder to run it in.
	Command string `json:"command,omitempty"`
	Cwd     string `json:"cwd,omitempty"`
	// The pane the command is running in, once the user said to run it.
	RunBlock string    `json:"run_block,omitempty"`
	AskedAt  time.Time `json:"asked_at"`
	Answer   *Answer   `json:"answer,omitempty"`
}

type Answer struct {
	Choices   []string   `json:"choices,omitempty"`
	Other     string     `json:"other,omitempty"` // typed by the user
	Dismissed bool       `json:"dismissed,omitempty"`
	Ran       *RunResult `json:"ran,omitempty"` // the user ran the command
	At        time.Time  `json:"at"`
}

// String is the answer as the agent sees it.
func (a Answer) String() string {
	if a.Dismissed {
		return "The user dismissed the question without answering."
	}
	if a.Ran != nil {
		return a.Ran.String()
	}
	parts := slices.Clone(a.Choices)
	if a.Other != "" {
		parts = append(parts, fmt.Sprintf("typed answer: %q", a.Other))
	}
	return "Answer: " + strings.Join(parts, "; ")
}

// FormatAnswerNotice is the one-line message sent to the agent's pane when
// `ask` isn't around to print the answer.
func FormatAnswerNotice(q Question) string {
	msg := fmt.Sprintf("[sidekick] answer to %q (%s): %s", truncate(q.Text, 80), q.ID, q.Answer)
	return strings.Join(strings.Fields(msg), " ")
}

// FormatInfoReply is the one-line message sent to the agent's pane when the
// user replies to something it told them with `tell`.
func FormatInfoReply(q Question, text string) string {
	msg := fmt.Sprintf("[sidekick] reply to your message %q (%s), from the user: %q", truncate(q.Text, 80), q.ID, text)
	return strings.Join(strings.Fields(msg), " ")
}

func newQuestionID() string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return "q-" + hex.EncodeToString(b)
}

func (s *Store) questionsPath() string { return filepath.Join(s.dir, s.key+".questions.json") }
func (s *Store) askLockPath(id string) string {
	return filepath.Join(s.dir, s.key+".ask-"+id+".lock")
}

// withQuestions runs fn on the question list while holding its lock, and
// saves the list if fn changed it.
func (s *Store) withQuestions(fn func(qs []Question) ([]Question, bool)) error {
	return withFileLock(s.questionsPath()+".lock", func() error {
		var qs []Question
		if err := readJSON(s.questionsPath(), &qs); err != nil {
			return err
		}
		qs, changed := fn(qs)
		if !changed {
			return nil
		}
		return writeJSON(s.questionsPath(), qs)
	})
}

func (s *Store) AddQuestion(q Question) error {
	return s.withQuestions(func(qs []Question) ([]Question, bool) {
		return append(qs, q), true
	})
}

func (s *Store) Questions() ([]Question, error) {
	var qs []Question
	err := s.withQuestions(func(cur []Question) ([]Question, bool) {
		qs = cur
		return cur, false
	})
	return qs, err
}

func (s *Store) QuestionsModTime() time.Time {
	fi, err := os.Stat(s.questionsPath())
	if err != nil {
		return time.Time{}
	}
	return fi.ModTime()
}

// SetRunBlock records the pane a question's command is running in.
func (s *Store) SetRunBlock(id, block string) error {
	return s.withQuestions(func(qs []Question) ([]Question, bool) {
		for i := range qs {
			if qs[i].ID == id && qs[i].Answer == nil {
				qs[i].RunBlock = block
				return qs, true
			}
		}
		return qs, false
	})
}

// AddReply records a reply the user sent from a message. It's a no-op if the
// message is gone.
func (s *Store) AddReply(id, text string) error {
	return s.withQuestions(func(qs []Question) ([]Question, bool) {
		for i := range qs {
			if qs[i].ID == id {
				qs[i].Replies = append(qs[i].Replies, text)
				return qs, true
			}
		}
		return qs, false
	})
}

// AnswerQuestion records the user's answer. It's a no-op if the question is
// gone or already answered.
func (s *Store) AnswerQuestion(id string, a Answer) error {
	return s.withQuestions(func(qs []Question) ([]Question, bool) {
		for i := range qs {
			if qs[i].ID == id && qs[i].Answer == nil {
				qs[i].Answer = &a
				return qs, true
			}
		}
		return qs, false
	})
}

var errQuestionGone = errors.New("question was removed without an answer")

// TakeAnswer removes the question and returns its answer once it has one.
func (s *Store) TakeAnswer(id string) (*Answer, error) {
	var ans *Answer
	found := false
	err := s.withQuestions(func(qs []Question) ([]Question, bool) {
		for i, q := range qs {
			if q.ID != id {
				continue
			}
			found = true
			if q.Answer == nil {
				return qs, false
			}
			ans = q.Answer
			return slices.Delete(qs, i, i+1), true
		}
		return qs, false
	})
	if err == nil && !found {
		err = errQuestionGone
	}
	return ans, err
}

// TakeOrphanAnswers removes and returns answered questions whose `ask` is no
// longer waiting, so their answers can go to the agent's pane instead.
func (s *Store) TakeOrphanAnswers() ([]Question, error) {
	var out []Question
	err := s.withQuestions(func(qs []Question) ([]Question, bool) {
		kept := qs[:0]
		for _, q := range qs {
			if q.Answer != nil && !s.askWaiting(q.ID) {
				out = append(out, q)
				continue
			}
			kept = append(kept, q)
		}
		return kept, len(out) > 0
	})
	return out, err
}

// askWaiting reports whether an `ask` still holds the question's lock.
func (s *Store) askWaiting(id string) bool {
	f, err := os.OpenFile(s.askLockPath(id), os.O_RDWR, 0o644)
	if err != nil {
		return false
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return true
	}
	syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return false
}

// Withdraw removes the questions that match and reports how many it removed.
func (s *Store) Withdraw(match func(Question) bool) (int, error) {
	n := 0
	err := s.withQuestions(func(qs []Question) ([]Question, bool) {
		kept := qs[:0]
		for _, q := range qs {
			if match(q) {
				n++
				continue
			}
			kept = append(kept, q)
		}
		return kept, n > 0
	})
	return n, err
}

// Ask puts q in the inbox and blocks until the user answers it. It returns
// errQuestionGone if the question is withdrawn first.
func (s *Store) Ask(q Question, poll time.Duration) (*Answer, error) {
	lockPath := s.askLockPath(q.ID)
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	defer os.Remove(lockPath)
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return nil, err
	}
	if err := s.AddQuestion(q); err != nil {
		return nil, err
	}
	for {
		ans, err := s.TakeAnswer(q.ID)
		if err != nil || ans != nil {
			return ans, err
		}
		time.Sleep(poll)
	}
}

// tabName looks up the name of the Rex tab holding block, falling back to the
// pane's own label.
func tabName(session, block string) string {
	if block == "" {
		return ""
	}
	var b struct {
		Block struct {
			Label    string `json:"label"`
			WindowID string `json:"window_id"`
		} `json:"block"`
	}
	out, err := exec.Command("rex", "block", "inspect", "-s", session, block, "--json").Output()
	if err != nil || json.Unmarshal(out, &b) != nil {
		return ""
	}
	var w struct {
		Windows []struct {
			ID    string `json:"window_id"`
			Label string `json:"label"`
		} `json:"windows"`
	}
	out, err = exec.Command("rex", "window", "ls", "-s", session, "--json").Output()
	if err == nil && json.Unmarshal(out, &w) == nil {
		for _, win := range w.Windows {
			if win.ID == b.Block.WindowID && win.Label != "" {
				return win.Label
			}
		}
	}
	return b.Block.Label
}

// rexCmd runs a rex command and returns its output. Tests swap it out.
var rexCmd = func(args ...string) ([]byte, error) {
	return exec.Command("rex", args...).CombinedOutput()
}

// focusPane brings the Rex tab and pane holding block to the front. Focusing
// the block moves the server's active tab, but the Rex app doesn't follow it,
// so each app showing the session is also told to go to the block's tab.
// Without the tab or an app to tell, it only focuses the block.
func focusPane(session, block string) error {
	var clients []string
	window, err := blockWindow(session, block)
	if err == nil {
		if out, err := rexCmd("client", "ls", "--json"); err == nil {
			clients = appClients(out, session)
		}
	}
	var errs []error
	for _, args := range focusCommands(session, window, block, clients) {
		if out, err := rexCmd(args...); err != nil {
			errs = append(errs, fmt.Errorf("rex %s: %s", args[0], strings.TrimSpace(string(out))))
		}
	}
	return errors.Join(errs...)
}

// focusCommands returns the rex commands that bring block, in tab window, to
// the front of each app client in clients.
func focusCommands(session, window, block string, clients []string) [][]string {
	cmds := [][]string{{"block", "focus", "-s", session, block}}
	if window == "" {
		return cmds
	}
	for _, c := range clients {
		cmds = append(cmds, []string{"do", "-C", c, "session.select", "session_id=" + session, "window_id=" + window})
	}
	return cmds
}

// appClients returns the IDs of the Rex apps showing session, from the output
// of rex client ls --json.
func appClients(clientsJSON []byte, session string) []string {
	var ls struct {
		Clients []struct {
			ID   string `json:"client_id"`
			Info struct {
				Kind string `json:"kind"`
			} `json:"info"`
			Sessions []string `json:"session_ids"`
		} `json:"clients"`
	}
	if json.Unmarshal(clientsJSON, &ls) != nil {
		return nil
	}
	var ids []string
	for _, c := range ls.Clients {
		if c.Info.Kind == "app" && slices.Contains(c.Sessions, session) {
			ids = append(ids, c.ID)
		}
	}
	return ids
}

// withFileLock runs fn while holding an exclusive lock on path.
func withFileLock(path string, fn func() error) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return fn()
}
