package main

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

type mergedMsg struct {
	e      Entry
	method string
	err    error
}

// mergeFlags maps GitHub's merge methods to gh pr merge's flags.
var mergeFlags = map[string]string{"MERGE": "--merge", "SQUASH": "--squash", "REBASE": "--rebase"}

// defaultMergeMethod asks GitHub how we usually merge in repo: MERGE, SQUASH,
// or REBASE. It's the method GitHub's merge button starts on for us.
func defaultMergeMethod(repo string) (string, error) {
	owner, name, _ := strings.Cut(repo, "/")
	out, err := ghJSON("api", "graphql",
		"-f", "query=query($owner: String!, $name: String!) { repository(owner: $owner, name: $name) { viewerDefaultMergeMethod } }",
		"-f", "owner="+owner, "-f", "name="+name)
	if err != nil {
		return "", err
	}
	var resp struct {
		Data struct {
			Repository struct {
				ViewerDefaultMergeMethod string `json:"viewerDefaultMergeMethod"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return "", err
	}
	method := resp.Data.Repository.ViewerDefaultMergeMethod
	if _, ok := mergeFlags[method]; !ok {
		return "", fmt.Errorf("unknown merge method %q for %s", method, repo)
	}
	return method, nil
}

// mergePR merges the PR the way we usually merge in its repo.
func mergePR(e Entry) (method string, err error) {
	if method, err = defaultMergeMethod(e.Repo); err != nil {
		return "", err
	}
	out, err := exec.Command("gh", "pr", "merge", e.URL, mergeFlags[method]).CombinedOutput()
	if err != nil {
		return method, fmt.Errorf("gh pr merge: %s", strings.TrimSpace(string(out)))
	}
	return method, nil
}

func ref(e Entry) string { return fmt.Sprintf("%s#%d", e.Repo, e.Number) }

// askMerge asks to confirm merging the selected PR, one of ours on the PRs tab.
func (m *model) askMerge() {
	e, ok := m.selected()
	switch {
	case !ok || m.isClosed(e.URL):
	case m.merging[e.URL]:
		m.note("Already merging " + ref(e))
	case m.statuses[e.URL].IsDraft:
		m.note(ref(e) + " is a draft, so it can't be merged yet")
	default:
		m.mergeConfirm = &e
	}
}

// updateMergeConfirm handles the key after m: y merges, anything else cancels.
func (m *model) updateMergeConfirm(msg tea.KeyMsg) tea.Cmd {
	e := *m.mergeConfirm
	m.mergeConfirm = nil
	if k := msg.String(); k != "y" && k != "Y" {
		m.note("Didn't merge " + ref(e))
		return nil
	}
	if m.merging == nil {
		m.merging = map[string]bool{}
	}
	m.merging[e.URL] = true
	m.note("Merging " + ref(e) + "…")
	return func() tea.Msg {
		method, err := mergePR(e)
		return mergedMsg{e, method, err}
	}
}

// merged reports how the merge went and checks the PR again, so it moves
// below the rule once GitHub has it as merged.
func (m *model) merged(msg mergedMsg) tea.Cmd {
	delete(m.merging, msg.e.URL)
	if msg.err != nil {
		m.setError("merging " + ref(msg.e) + ": " + msg.err.Error())
		return nil
	}
	m.note(fmt.Sprintf("Merged %s (%s)", ref(msg.e), strings.ToLower(msg.method)))
	if m.nextAt == nil {
		return nil
	}
	m.nextAt[msg.e.URL] = time.Time{}
	return m.poll(false)
}
