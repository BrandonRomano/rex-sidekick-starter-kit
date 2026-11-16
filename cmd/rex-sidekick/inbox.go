package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// An inbox holds notices for one Rex pane, so an agent can pick them up with
// `rex-sidekick wait` instead of having them typed over whatever the user is
// writing. Files live in <state dir>/inbox/:
//
// <block>.inbox  pending notices, one per line
// <block>.lock   guards reads and writes of the inbox
// <block>.wait   held by a running `wait`. Its mtime is when one last ran.
type inbox struct {
	dir, key string
}

// waitGrace is how long after a `wait` exits we keep queueing notices instead
// of typing them. It covers the agent handling one notice before it starts
// waiting again.
const waitGrace = 15 * time.Minute

func openInbox(block string) (*inbox, error) {
	dir := filepath.Join(stateDir(), "inbox")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	key := strings.NewReplacer(":", "_", "/", "_").Replace(block)
	return &inbox{dir: dir, key: key}, nil
}

func (b *inbox) path(ext string) string { return filepath.Join(b.dir, b.key+"."+ext) }

func (b *inbox) withLock(fn func() error) error {
	f, err := os.OpenFile(b.path("lock"), os.O_CREATE|os.O_RDWR, 0o644)
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

// Push adds a notice to the inbox.
func (b *inbox) Push(msg string) error {
	return b.withLock(func() error {
		f, err := os.OpenFile(b.path("inbox"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintln(f, msg); err != nil {
			f.Close()
			return err
		}
		return f.Close()
	})
}

// Unshift puts notices back at the front of the inbox, ahead of any that
// arrived since they were taken.
func (b *inbox) Unshift(msgs []string) error {
	return b.withLock(func() error {
		rest, err := os.ReadFile(b.path("inbox"))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		data := strings.Join(msgs, "\n") + "\n" + string(rest)
		return os.WriteFile(b.path("inbox"), []byte(data), 0o644)
	})
}

// Take empties the inbox and returns what was in it.
func (b *inbox) Take() ([]string, error) {
	var lines []string
	err := b.withLock(func() error {
		f, err := os.Open(b.path("inbox"))
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			if l := strings.TrimSpace(sc.Text()); l != "" {
				lines = append(lines, l)
			}
		}
		f.Close()
		if err := sc.Err(); err != nil {
			return err
		}
		return os.Remove(b.path("inbox"))
	})
	return lines, err
}

// Listening reports whether an agent is waiting on this inbox, or was within
// waitGrace and will likely wait again.
func (b *inbox) Listening(now time.Time) bool {
	fi, err := os.Stat(b.path("wait"))
	if err != nil {
		return false
	}
	if now.Sub(fi.ModTime()) < waitGrace {
		return true
	}
	f, err := os.OpenFile(b.path("wait"), os.O_RDWR, 0o644)
	if err != nil {
		return false
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return true // a wait holds it
	}
	syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return false
}

var errAlreadyWaiting = errors.New("another wait is already running for this pane")

// Wait blocks until the inbox has notices, then takes and returns them. Only
// one Wait runs per pane at a time.
func (b *inbox) Wait(poll time.Duration) ([]string, error) {
	f, err := os.OpenFile(b.path("wait"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return nil, errAlreadyWaiting
	}
	touch := func() { now := time.Now(); _ = os.Chtimes(b.path("wait"), now, now) }
	touch()
	defer touch()
	for {
		lines, err := b.Take()
		if err != nil || len(lines) > 0 {
			return lines, err
		}
		time.Sleep(poll)
	}
}

// Stale reports whether the inbox has notices that nobody is going to pick up.
func (b *inbox) Stale(now time.Time) bool {
	if _, err := os.Stat(b.path("inbox")); err != nil {
		return false
	}
	return !b.Listening(now)
}
