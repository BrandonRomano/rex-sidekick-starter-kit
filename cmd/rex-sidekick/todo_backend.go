package main

import (
	"bytes"
	"cmp"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"
)

// A list of the user's own can keep its tasks in a git repo instead of with
// the session's other tasks. The repo holds a folder per list, named after
// the list, so several lists can share one, and any session that points a
// list at the repo shows the same tasks. The folder's data.json holds the
// list's own tasks, and each of its sub-groups is a folder inside it with a
// data.json of its own. Every change to the list commits its files, and
// pushes when the repo's branch has an upstream.

// todoBackend is where one of the user's lists keeps its tasks. The zero
// value keeps them with the session's other tasks.
type todoBackend struct {
	Git string `json:"git,omitempty"` // the repo's absolute path
}

// listFile is the data.json of a list, or of one of its sub-groups, in a
// git repo.
type listFile struct {
	Name string `json:"name"`
	// The names of the sub-groups directly inside it, in order. Each has a
	// folder of its own next to this file.
	Subgroups []string `json:"subgroups,omitempty"`
	Todos     []Todo   `json:"todos"`
}

// todoTx is the TODO list while withTodos edits it.
type todoTx struct {
	ts []Todo
	// The sub-groups of the user's lists, by list name. Each list's are paths
	// like "Basement/Boiler", with siblings in order.
	subs map[string][]string
	// The user's lists kept elsewhere, by name. Set backsChanged after
	// changing it.
	backs        map[string]todoBackend
	backsChanged bool
	// Lists whose files come out of their repos, such as a removed list.
	drop []repoGroup
	// The files read from repos, and what each held, to write only the ones
	// that change and take out the ones no longer needed.
	read []readFile
	was  map[string][]byte
}

type repoGroup struct{ repo, name string }

// readFile is a file of the list named list, read from repo.
type readFile struct{ repo, list, path string }

func (s *Store) backendsPath() string { return filepath.Join(s.dir, s.key+".todo-backends.json") }

// subGroupsPath holds the sub-groups of the user's lists that are kept with
// the session's tasks. A list in a git repo keeps its sub-groups there.
func (s *Store) subGroupsPath() string { return filepath.Join(s.dir, s.key+".todo-subgroups.json") }

// TodoBackends returns where the user's lists that aren't kept with the
// session's tasks keep them, by list name.
func (s *Store) TodoBackends() (map[string]todoBackend, error) {
	backs := map[string]todoBackend{}
	err := readJSON(s.backendsPath(), &backs)
	return backs, err
}

// gitRepos returns the repos the lists in backs keep their tasks in.
func gitRepos(backs map[string]todoBackend) []string {
	var repos []string
	for _, b := range backs {
		if b.Git != "" && !slices.Contains(repos, b.Git) {
			repos = append(repos, b.Git)
		}
	}
	slices.Sort(repos)
	return repos
}

// slugify turns a list's or sub-group's name into a folder name, like
// "home-renovation".
func slugify(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			dash = true
			continue
		}
		if dash && b.Len() > 0 {
			b.WriteByte('-')
		}
		b.WriteRune(r)
		dash = false
	}
	if b.Len() == 0 {
		return "list"
	}
	return b.String()
}

// listDir is the folder that the list named name keeps its tasks in, in
// repo. A sub-group's folder is inside it, at subDir.
func listDir(repo, name string) string { return filepath.Join(repo, slugify(name)) }

// subDir is the folder of the sub-group at path in the list folder dir.
func subDir(dir, path string) string {
	if path == "" {
		return dir
	}
	for _, name := range strings.Split(path, "/") {
		dir = filepath.Join(dir, slugify(name))
	}
	return dir
}

// legacyFilePath is where the list named name kept its tasks in repo before
// lists had folders: one file named after the list. Such a file still
// loads, and the next save moves it into the list's folder.
func legacyFilePath(repo, name string) string {
	base := strings.TrimLeft(strings.NewReplacer("/", "-", "\\", "-", "\x00", "").Replace(name), ".")
	if base == "" {
		base = "list"
	}
	return filepath.Join(repo, base+".json")
}

func readListFile(path string) ([]byte, listFile, error) {
	var f listFile
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, f, err
	}
	if len(bytes.TrimSpace(b)) > 0 {
		if err := json.Unmarshal(b, &f); err != nil {
			return nil, f, fmt.Errorf("reading %s: %w", path, err)
		}
	}
	return b, f, nil
}

// readListTree reads the list named name from its folder in repo, or from
// its file from before lists had folders. It returns each file as it was,
// to tell later whether it changed, the list's tasks, and its sub-groups.
// It returns nothing when the repo doesn't have the list yet.
func readListTree(repo, name string) (files map[string][]byte, ts []Todo, subs []string, err error) {
	files = map[string][]byte{}
	dir := listDir(repo, name)
	root := filepath.Join(dir, "data.json")
	b, f, err := readListFile(root)
	if errors.Is(err, os.ErrNotExist) {
		path := legacyFilePath(repo, name)
		if b, f, err = readListFile(path); errors.Is(err, os.ErrNotExist) {
			return files, nil, nil, nil
		} else if err != nil {
			return nil, nil, nil, err
		}
		files[path] = b
		for _, t := range f.Todos {
			t.Mine, t.Group, t.Sub = true, name, ""
			ts = append(ts, t)
		}
		return files, ts, nil, nil
	}
	if err != nil {
		return nil, nil, nil, err
	}
	var walk func(dir, path string, b []byte, f listFile) error
	walk = func(dir, path string, b []byte, f listFile) error {
		files[filepath.Join(dir, "data.json")] = b
		if path != "" {
			subs = append(subs, path)
		}
		for _, t := range f.Todos {
			t.Mine, t.Group, t.Sub = true, name, path
			ts = append(ts, t)
		}
		// The sub-groups the file names come first, in its order, then any
		// other folder with a data.json, such as one copied in by hand.
		var kids []string
		for _, n := range f.Subgroups {
			if d := filepath.Join(dir, slugify(n)); !slices.Contains(kids, d) {
				kids = append(kids, d)
			}
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if d := filepath.Join(dir, e.Name()); e.IsDir() && !slices.Contains(kids, d) {
				kids = append(kids, d)
			}
		}
		seen := map[string]bool{}
		for _, d := range kids {
			kb, kf, err := readListFile(filepath.Join(d, "data.json"))
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return err
			}
			// A name from the file can't hold the separator of a path.
			kname := strings.ReplaceAll(cmp.Or(strings.TrimSpace(kf.Name), filepath.Base(d)), "/", "-")
			if seen[kname] {
				continue
			}
			seen[kname] = true
			if err := walk(d, subJoin(path, kname), kb, kf); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(dir, "", b, f); err != nil {
		return nil, nil, nil, err
	}
	return files, ts, subs, nil
}

// encodeListTree is the files for the list named name in repo, holding ts
// in the sub-groups subs. The files leave out which list and sub-group the
// tasks are on, so moving a folder by hand moves them.
func encodeListTree(repo, name string, ts []Todo, subs []string) (map[string][]byte, error) {
	dir := listDir(repo, name)
	files := map[string][]byte{}
	for _, path := range append([]string{""}, subs...) {
		f := listFile{Name: name, Todos: []Todo{}}
		if path != "" {
			f.Name = subName(path)
		}
		for _, kid := range subChildren(subs, path) {
			f.Subgroups = append(f.Subgroups, subName(kid))
		}
		for _, t := range ts {
			if t.Sub == path {
				t.Mine, t.Group, t.Sub = false, "", ""
				f.Todos = append(f.Todos, t)
			}
		}
		b, err := json.MarshalIndent(f, "", "  ")
		if err != nil {
			return nil, err
		}
		files[filepath.Join(subDir(dir, path), "data.json")] = append(b, '\n')
	}
	return files, nil
}

// withRepoLocks runs fn while holding a lock on each repo, so sessions
// sharing a repo take turns with it.
func withRepoLocks(repos []string, fn func() error) error {
	if len(repos) == 0 {
		return fn()
	}
	sum := sha1.Sum([]byte(repos[0]))
	lock := filepath.Join(stateDir(), "git-"+hex.EncodeToString(sum[:6])+".lock")
	return withFileLock(lock, func() error { return withRepoLocks(repos[1:], fn) })
}

// editTodos runs fn on the TODO list, with the tasks of the user's lists kept
// in git repos among them, while holding their locks. If fn reports a
// change, it saves the list: each task back where its list keeps it, with a
// commit in each repo whose files changed. extraRepos are repos fn may move
// lists into.
func (s *Store) editTodos(extraRepos []string, fn func(tx *todoTx) (bool, error)) error {
	return withFileLock(s.todosPath()+".lock", func() error {
		backs, err := s.TodoBackends()
		if err != nil {
			return err
		}
		repos := gitRepos(backs)
		for _, r := range extraRepos {
			if r != "" && !slices.Contains(repos, r) {
				repos = append(repos, r)
			}
		}
		slices.Sort(repos)
		return withRepoLocks(repos, func() error {
			tx := &todoTx{backs: backs, subs: map[string][]string{}, was: map[string][]byte{}}
			if err := readJSON(s.todosPath(), &tx.ts); err != nil {
				return err
			}
			if err := readJSON(s.subGroupsPath(), &tx.subs); err != nil {
				return err
			}
			localSubs := cloneSubs(tx.subs)
			for _, name := range sortedKeys(backs) {
				if backs[name].Git == "" {
					continue
				}
				// The repo has the say over a list kept there.
				delete(tx.subs, name)
				if err := tx.load(backs[name].Git, name, name); err != nil {
					return err
				}
			}
			changed, err := fn(tx)
			if err != nil || !changed {
				return err
			}
			return s.saveTodos(tx, localSubs)
		})
	})
}

// load adds the list named file in repo to the list named group: its
// tasks, other than ones already there, ahead of the group's own, and its
// sub-groups.
func (tx *todoTx) load(repo, file, group string) error {
	files, ts, subs, err := readListTree(repo, file)
	if err != nil {
		return err
	}
	for _, path := range sortedKeys(files) {
		tx.read = append(tx.read, readFile{repo, group, path})
		tx.was[path] = files[path]
	}
	ts = slices.DeleteFunc(ts, func(t Todo) bool {
		return slices.ContainsFunc(tx.ts, func(x Todo) bool { return x.ID == t.ID })
	})
	for i := range ts {
		ts[i].Group = group
	}
	at := slices.IndexFunc(tx.ts, func(x Todo) bool { return x.Mine && x.Group == group })
	if at < 0 {
		at = len(tx.ts)
	}
	tx.ts = slices.Insert(tx.ts, at, ts...)
	for _, p := range subs {
		if !slices.Contains(tx.subs[group], p) {
			tx.subs[group] = append(tx.subs[group], p)
		}
	}
	return nil
}

func cloneSubs(subs map[string][]string) map[string][]string {
	out := make(map[string][]string, len(subs))
	for k, v := range subs {
		out[k] = slices.Clone(v)
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// saveTodos writes the tasks back where their lists keep them. localSubs is
// what the session held for its lists' sub-groups before.
func (s *Store) saveTodos(tx *todoTx, localSubs map[string][]string) error {
	var local []Todo
	inRepo := map[string][]Todo{}
	for _, t := range tx.ts {
		if t.Mine && t.Group != "" && tx.backs[t.Group].Git != "" {
			inRepo[t.Group] = append(inRepo[t.Group], t)
		} else {
			local = append(local, t)
		}
	}
	if err := writeJSON(s.todosPath(), local); err != nil {
		return err
	}
	if tx.backsChanged {
		if err := writeJSON(s.backendsPath(), tx.backs); err != nil {
			return err
		}
	}
	keep := map[string][]string{}
	for name, subs := range tx.subs {
		if len(subs) > 0 && tx.backs[name].Git == "" {
			keep[name] = subs
		}
	}
	if !equalSubs(keep, localSubs) {
		if err := writeJSON(s.subGroupsPath(), keep); err != nil {
			return err
		}
	}
	// The lists whose files changed, and the files, by repo.
	touched, paths := map[string][]string{}, map[string][]string{}
	written := map[string]bool{}
	for _, name := range sortedKeys(tx.backs) {
		repo := tx.backs[name].Git
		if repo == "" {
			continue
		}
		files, err := encodeListTree(repo, name, inRepo[name], withTaskSubs(tx.subs[name], inRepo[name]))
		if err != nil {
			return err
		}
		for _, path := range sortedKeys(files) {
			written[path] = true
			if prev, ok := tx.was[path]; ok && bytes.Equal(prev, files[path]) {
				continue
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			if err := writeFileAtomic(path, files[path]); err != nil {
				return err
			}
			touched[repo] = append(touched[repo], name)
			paths[repo] = append(paths[repo], path)
		}
	}
	// What a list no longer needs comes out of its repo: a removed list's
	// files, a removed sub-group's, and a file from before lists had
	// folders. A list that moved to another repo or back to the session
	// leaves its files for the other sessions that use them.
	for _, r := range tx.read {
		dropped := slices.Contains(tx.drop, repoGroup{r.repo, r.list})
		if written[r.path] || !dropped && tx.backs[r.list].Git != r.repo {
			continue
		}
		if err := os.Remove(r.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		// Empty folders go too, up to the repo.
		for d := filepath.Dir(r.path); d != r.repo && strings.HasPrefix(d, r.repo); d = filepath.Dir(d) {
			if os.Remove(d) != nil {
				break
			}
		}
		touched[r.repo] = append(touched[r.repo], r.list)
		paths[r.repo] = append(paths[r.repo], r.path)
	}
	var errs []error
	for _, repo := range sortedKeys(touched) {
		if err := gitCommit(repo, paths[repo], touched[repo]); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func equalSubs(a, b map[string][]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || !slices.Equal(v, w) {
			return false
		}
	}
	return true
}

func writeFileAtomic(path string, b []byte) error {
	tmp := fmt.Sprintf("%s.tmp%d", path, os.Getpid())
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// git runs git in repo. It never prompts, so a push that needs a password
// fails rather than hangs.
func git(repo string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("git %s in %s: %s", args[0], repo, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// gitCommit commits the lists' files at paths, and nothing else in the
// repo, with the time in the message. It pushes in the background when the
// branch has an upstream.
func gitCommit(repo string, paths, names []string) error {
	// Staging a file that was never committed and is now gone would fail.
	var add []string
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			add = append(add, p)
		} else if _, err := git(repo, "rm", "-q", "--cached", "--ignore-unmatch", "--", p); err != nil {
			return err
		}
	}
	if len(add) > 0 {
		if _, err := git(repo, append([]string{"add", "--"}, add...)...); err != nil {
			return err
		}
	}
	out, err := git(repo, append([]string{"diff", "--cached", "--name-only", "--"}, paths...)...)
	if err != nil {
		return err
	}
	if strings.TrimSpace(out) == "" {
		return nil
	}
	slices.Sort(names)
	msg := fmt.Sprintf("rex-sidekick: update %s (%s)", strings.Join(slices.Compact(names), ", "), time.Now().Format("2006-01-02 15:04:05 -0700"))
	if _, err := git(repo, append([]string{"commit", "-q", "-m", msg, "--"}, paths...)...); err != nil {
		return err
	}
	if hasUpstream(repo) {
		cmd := exec.Command("git", "-C", repo, "push", "-q")
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		if cmd.Start() == nil {
			go cmd.Wait()
		}
	}
	return nil
}

func hasUpstream(repo string) bool {
	_, err := git(repo, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}")
	return err == nil
}

// prepareRepo returns the absolute path of the git repo at path. It makes a
// new repo when nothing is there yet, or the directory is empty.
func prepareRepo(path string) (string, error) {
	if rest, ok := strings.CutPrefix(path, "~"); ok && (rest == "" || rest[0] == '/') {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = home + rest
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(path)
	switch {
	case errors.Is(err, os.ErrNotExist) || err == nil && len(entries) == 0:
		if err := os.MkdirAll(path, 0o755); err != nil {
			return "", err
		}
		if _, err := git(path, "init", "-q"); err != nil {
			return "", err
		}
	case err != nil:
		return "", err
	}
	if _, err := git(path, "rev-parse", "--show-toplevel"); err != nil {
		return "", fmt.Errorf("%s isn't a git repo. Run git init there, or clone one", path)
	}
	// The lists' files go where the user pointed, which may be a folder
	// inside the repo. Resolving links keeps one folder under one path.
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	return path, nil
}

// errFolderTaken says two lists in one repo would share a folder.
func errFolderTaken(name, other string) error {
	return fmt.Errorf("%s would share a folder with %s in that repo. Rename one of them first", name, other)
}

// folderTaken returns another list in backs whose folder in repo is the one
// the list named name would get.
func folderTaken(backs map[string]todoBackend, repo, name string) (string, bool) {
	for _, other := range sortedKeys(backs) {
		if other != name && backs[other].Git == repo && slugify(other) == slugify(name) {
			return other, true
		}
	}
	return "", false
}

// SetTodoBackend moves one of the user's lists to b. Moving it into a git
// repo brings along its tasks and sub-groups, joined by ID with any the
// repo's folder for the list already holds. Moving it out copies them into
// the session and leaves the repo's files alone, for other sessions that use
// them.
func (s *Store) SetTodoBackend(group string, b todoBackend) error {
	if group == "" {
		return fmt.Errorf("My list stays with the session. Make a list to keep it in a git repo")
	}
	if b.Git != "" {
		repo, err := prepareRepo(b.Git)
		if err != nil {
			return err
		}
		b.Git = repo
	}
	// The list may be new to this session, when another one made it.
	err := s.withMyGroups(func(gs []string) ([]string, error) {
		if !slices.Contains(gs, group) {
			gs = append(gs, group)
		}
		return gs, nil
	})
	if err != nil {
		return err
	}
	return s.editTodos([]string{b.Git}, func(tx *todoTx) (bool, error) {
		if tx.backs[group] == b {
			return false, nil
		}
		if b.Git != "" {
			if other, ok := folderTaken(tx.backs, b.Git, group); ok {
				return false, errFolderTaken(group, other)
			}
			if err := tx.load(b.Git, group, group); err != nil {
				return false, err
			}
			tx.backs[group] = b
		} else {
			delete(tx.backs, group)
		}
		tx.backsChanged = true
		return true, nil
	})
}

// SyncTodos pulls the git repos that the user's lists keep their tasks in,
// then pushes their commits, for repos whose branch has an upstream. When
// the pull can't rebase the commits onto the remote's, it leaves the repo as
// it was for the user to sort out.
func (s *Store) SyncTodos() error {
	backs, err := s.TodoBackends()
	if err != nil {
		return err
	}
	var errs []error
	for _, repo := range gitRepos(backs) {
		errs = append(errs, withRepoLocks([]string{repo}, func() error {
			if !hasUpstream(repo) {
				return nil
			}
			if _, err := git(repo, "pull", "-q", "--rebase", "--autostash"); err != nil {
				if _, statErr := git(repo, "rev-parse", "-q", "--verify", "REBASE_HEAD"); statErr == nil {
					_, _ = git(repo, "rebase", "--abort")
					return fmt.Errorf("%s conflicts with its remote. Fix it there, then press r again", repo)
				}
				return err
			}
			_, err := git(repo, "push", "-q")
			return err
		}))
	}
	return errors.Join(errs...)
}

// todoFilesModTime is the latest change to the files of the user's lists
// kept in git repos.
func (s *Store) todoFilesModTime() time.Time {
	var latest time.Time
	see := func(fi fs.FileInfo) {
		if fi.ModTime().After(latest) {
			latest = fi.ModTime()
		}
	}
	backs, _ := s.TodoBackends()
	for name, b := range backs {
		if b.Git == "" {
			continue
		}
		if fi, err := os.Stat(legacyFilePath(b.Git, name)); err == nil {
			see(fi)
		}
		// Folders count too, for a sub-group that came or went.
		_ = filepath.WalkDir(listDir(b.Git, name), func(_ string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if fi, err := d.Info(); err == nil {
				see(fi)
			}
			return nil
		})
	}
	return latest
}
