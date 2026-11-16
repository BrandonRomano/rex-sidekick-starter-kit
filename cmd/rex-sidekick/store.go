package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Entry is one PR on the watch list.
type Entry struct {
	URL    string `json:"url"`
	Repo   string `json:"repo"` // owner/name
	Number int    `json:"number"`
	Title  string `json:"title"`
	Author string `json:"author,omitempty"` // GitHub login
	// The Rex pane to tell about changes. Empty means nobody is told.
	NotifySession string    `json:"notify_session,omitempty"`
	NotifyBlock   string    `json:"notify_block,omitempty"`
	AddedAt       time.Time `json:"added_at"`
}

type watchList struct {
	PRs []Entry `json:"prs"`
}

// Store holds the files for one Rex session's sidekick.
//
// <key>.prs.json         the PR watch list, written by pr add/rm and the TUI
// <key>.prs-status.json  last seen status per PR, written only by the TUI
// <key>.ui.lock          held by the running TUI
// <key>.open.lock        held while opening the TUI
// <key>.dispatcher.json  the agent pane the user's own tasks are handed to
type Store struct {
	Session string
	dir     string
	key     string

	afterListRead func() // tests use it to write between a read and the next step
}

func stateDir() string {
	if d := os.Getenv("REX_SIDEKICK_STATE_DIR"); d != "" {
		return d
	}
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "rex-sidekick")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "rex-sidekick")
}

func OpenStore(session string) (*Store, error) {
	if session == "" {
		return nil, errors.New("no Rex session: run inside Rex or pass --session")
	}
	dir := stateDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	key := strings.NewReplacer(":", "_", "/", "_").Replace(session)
	return &Store{Session: session, dir: dir, key: key}, nil
}

func (s *Store) listPath() string   { return filepath.Join(s.dir, s.key+".prs.json") }
func (s *Store) statusPath() string { return filepath.Join(s.dir, s.key+".prs-status.json") }
func (s *Store) uiLockPath() string { return filepath.Join(s.dir, s.key+".ui.lock") }

// withListLock runs fn while holding an exclusive lock on the watch list.
func (s *Store) withListLock(fn func() error) error {
	f, err := os.OpenFile(s.listPath()+".lock", os.O_CREATE|os.O_RDWR, 0o644)
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

// withOpenLock runs fn while holding the lock that guards opening the sidekick.
func (s *Store) withOpenLock(fn func() error) error {
	f, err := os.OpenFile(filepath.Join(s.dir, s.key+".open.lock"), os.O_CREATE|os.O_RDWR, 0o644)
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

func (s *Store) List() ([]Entry, error) {
	var wl watchList
	if err := readJSON(s.listPath(), &wl); err != nil {
		return nil, err
	}
	if s.afterListRead != nil {
		s.afterListRead()
	}
	return wl.PRs, nil
}

// ListModTime lets the TUI notice when another process changed the list.
func (s *Store) ListModTime() time.Time {
	fi, err := os.Stat(s.listPath())
	if err != nil {
		return time.Time{}
	}
	return fi.ModTime()
}

// Add inserts e, or updates the entry with the same URL. It reports whether e is new.
func (s *Store) Add(e Entry) (added bool, err error) {
	err = s.withListLock(func() error {
		var wl watchList
		if err := readJSON(s.listPath(), &wl); err != nil {
			return err
		}
		for i := range wl.PRs {
			if wl.PRs[i].URL == e.URL {
				e.AddedAt = wl.PRs[i].AddedAt
				wl.PRs[i] = e
				return writeJSON(s.listPath(), wl)
			}
		}
		added = true
		wl.PRs = append(wl.PRs, e)
		return writeJSON(s.listPath(), wl)
	})
	return added, err
}

// Remove drops every entry matching ref (a URL, owner/repo#N, or #N).
func (s *Store) Remove(ref string) (int, error) {
	n := 0
	err := s.withListLock(func() error {
		var wl watchList
		if err := readJSON(s.listPath(), &wl); err != nil {
			return err
		}
		kept := wl.PRs[:0]
		for _, e := range wl.PRs {
			if matchesRef(e, ref) {
				n++
				continue
			}
			kept = append(kept, e)
		}
		wl.PRs = kept
		return writeJSON(s.listPath(), wl)
	})
	return n, err
}

func matchesRef(e Entry, ref string) bool {
	ref = strings.TrimSuffix(strings.TrimSpace(ref), "/")
	switch {
	case ref == e.URL:
		return true
	case ref == fmt.Sprintf("%s#%d", e.Repo, e.Number):
		return true
	case ref == fmt.Sprintf("#%d", e.Number) || ref == fmt.Sprint(e.Number):
		return true
	}
	return false
}

func (s *Store) LoadStatuses() map[string]Status {
	m := map[string]Status{}
	_ = readJSON(s.statusPath(), &m)
	return m
}

func (s *Store) SaveStatuses(m map[string]Status) error {
	return writeJSON(s.statusPath(), m)
}

// LockUI takes the single-TUI lock. The lock lasts until the process exits.
func (s *Store) LockUI() (*os.File, error) {
	f, err := os.OpenFile(s.uiLockPath(), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, errUIRunning
	}
	return f, nil
}

var errUIRunning = errors.New("the sidekick is already running for this session")

func (s *Store) UIRunning() bool {
	f, err := s.LockUI()
	if err != nil {
		return errors.Is(err, errUIRunning)
	}
	f.Close()
	return false
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) || len(b) == 0 {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// writeJSON writes to a temp file and renames it, so readers never see half a file.
func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.tmp%d", path, os.Getpid())
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Dispatcher is the agent pane that takes the user's own tasks, set by
// `open --dispatcher`.
type Dispatcher struct {
	Session string `json:"session"`
	Block   string `json:"block"`
	Name    string `json:"name,omitempty"` // its Rex tab's name
}

func (s *Store) dispatcherPath() string { return filepath.Join(s.dir, s.key+".dispatcher.json") }

func (s *Store) SetDispatcher(d Dispatcher) error { return writeJSON(s.dispatcherPath(), d) }

// Dispatcher returns the recorded dispatcher, if there is one.
func (s *Store) Dispatcher() (Dispatcher, bool) {
	var d Dispatcher
	if readJSON(s.dispatcherPath(), &d) != nil || d.Block == "" {
		return Dispatcher{}, false
	}
	return d, true
}
