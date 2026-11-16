package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// reviewModel is a model a Claude review can run on. ID goes to claude
// --model, Name is what the picker shows, and Family ("opus") is what the
// picker remembers, so a new version keeps the choice.
type reviewModel struct {
	ID, Name, Family string
}

// fallbackReviewModels are used when the claude CLI's model catalog can't be
// read. The CLI resolves each alias to a model of that family.
var fallbackReviewModels = []reviewModel{
	{"fable", "fable", "fable"},
	{"opus", "opus", "opus"},
	{"sonnet", "sonnet", "sonnet"},
	{"haiku", "haiku", "haiku"},
}

// defaultReviewModel is the family the picker's cursor starts on.
const defaultReviewModel = "opus"

// claudeDir is the claude CLI's config folder.
func claudeDir() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude")
}

// reviewModels lists the models in the main section of the claude CLI's
// /model menu, in its order, from the catalog the CLI caches. That cache isn't
// documented, so it falls back to the family aliases when it can't be read.
func reviewModels() []reviewModel {
	if ms := catalogModels(filepath.Join(claudeDir(), "cache", "model-catalog")); len(ms) > 0 {
		return ms
	}
	return fallbackReviewModels
}

// catalogModels reads the newest catalog file in dir, or returns nil.
func catalogModels(dir string) []reviewModel {
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	var newest []reviewModel
	var newestAt int64
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var c struct {
			FetchedAt int64 `json:"fetchedAt"`
			Catalog   struct {
				Config struct {
					Models []struct {
						ID        string `json:"id"`
						Name      string `json:"name"`
						ShortName string `json:"short_name"`
						Section   string `json:"section"`
					} `json:"models"`
				} `json:"config"`
			} `json:"catalog"`
		}
		if json.Unmarshal(data, &c) != nil {
			continue
		}
		var ms []reviewModel
		for _, cm := range c.Catalog.Config.Models {
			if cm.Section == "main" && cm.ID != "" && cm.Name != "" && cm.ShortName != "" {
				ms = append(ms, reviewModel{cm.ID, cm.Name, strings.ToLower(cm.ShortName)})
			}
		}
		if len(ms) > 0 && (newest == nil || c.FetchedAt > newestAt) {
			newest, newestAt = ms, c.FetchedAt
		}
	}
	return newest
}

// modelPicker picks the model for a Claude review: a self-review of one of
// our PRs, or a review someone asked us for, which goes on to the context box.
type modelPicker struct {
	e      Entry
	models []reviewModel
	cur    int
}

func (m *model) openModelPicker(e Entry) {
	models := reviewModels()
	inFamily := func(f string) func(reviewModel) bool {
		return func(r reviewModel) bool { return r.Family == f }
	}
	i := slices.IndexFunc(models, inFamily(m.lastReviewModel))
	if i < 0 {
		i = slices.IndexFunc(models, inFamily(defaultReviewModel))
	}
	m.modelPicker = &modelPicker{e: e, models: models, cur: max(0, i)}
}

// dropStalePicker closes the model picker once its PR is no longer listed.
func (m *model) dropStalePicker() {
	p := m.modelPicker
	if p == nil {
		return
	}
	listed := func(e Entry) bool { return e.URL == p.e.URL }
	if !slices.ContainsFunc(m.entries, listed) && !slices.ContainsFunc(m.reviews, listed) {
		m.modelPicker = nil
	}
}

func (m *model) updateModelPicker(msg tea.KeyMsg) tea.Cmd {
	p := m.modelPicker
	switch msg.String() {
	case "ctrl+c":
		return tea.Quit
	case "esc", "q":
		m.modelPicker = nil
	case "j", "down":
		p.cur = min(p.cur+1, len(p.models)-1)
	case "k", "up":
		p.cur = max(p.cur-1, 0)
	case "enter":
		m.modelPicker = nil
		rm := p.models[p.cur]
		m.lastReviewModel = rm.Family
		if slices.ContainsFunc(m.reviews, func(e Entry) bool { return e.URL == p.e.URL }) {
			m.openReviewInput(p.e, rm.ID)
			return nil
		}
		m.startSelfReview(p.e, rm)
	}
	return nil
}

func (p *modelPicker) render(w int) string {
	var b strings.Builder
	b.WriteString("    " + sDim.Render("Review with:") + "\n")
	nameW := 0
	for _, rm := range p.models {
		nameW = max(nameW, len(rm.Name))
	}
	for i, rm := range p.models {
		// Each row shows the full ID claude runs, next to the name /model uses.
		label := rm.ID
		if rm.Name != rm.ID {
			label = fmt.Sprintf("%-*s  %s", nameW, rm.Name, rm.ID)
		}
		line := "  " + label
		if i == p.cur {
			line = sSel.Render("› " + label)
		}
		b.WriteString("    " + truncate(line, max(10, w-4)) + "\n")
	}
	return b.String()
}

// selfReviewName names a self-review's Claude session and pane.
func selfReviewName(e Entry) string {
	return "Self-review: " + e.Title
}

// selfReviewCommand is the claude command that reviews the PR.
func selfReviewCommand(e Entry, model string) []string {
	return []string{"claude", "--model", model, "--name", selfReviewName(e), "/code-review " + e.URL}
}

// paneDir returns the folder the program in a pane runs in, or "" if Rex
// can't tell. A variable so tests can stand in for Rex.
var paneDir = func(session, block string) string {
	out, err := exec.Command("rex", "block", "call", "-s", session, "-b", block, "process").Output()
	if err != nil {
		return ""
	}
	var r struct {
		Child struct {
			Cwd string `json:"cwd"`
		} `json:"child"`
	}
	if json.Unmarshal(out, &r) != nil {
		return ""
	}
	return r.Child.Cwd
}

// selfReviewArgs builds the rex command for a self-review. When the pane that
// registered the PR is still open, the review splits to its right, in that
// pane's folder when Rex knows it. Otherwise it gets a tab of its own.
func selfReviewArgs(session string, e Entry, model, fallbackDir string) []string {
	label := selfReviewName(e)
	cmd := selfReviewCommand(e, model)
	if e.NotifyBlock != "" && (e.NotifySession == "" || e.NotifySession == session) && paneExists(session, e.NotifyBlock) {
		args := []string{"split", "-s", session, "-b", e.NotifyBlock, "--split=right",
			"--label", label, "--focus=false"}
		if dir := paneDir(session, e.NotifyBlock); dir != "" {
			args = append(args, "--cwd", dir)
		}
		return append(append(args, "--"), cmd...)
	}
	args := []string{"window", "new", "-s", session, label, "--focus=false", "--cwd", fallbackDir, "--"}
	return append(args, cmd...)
}

// startSelfReview starts Claude reviewing one of our own PRs on rm.
func (m *model) startSelfReview(e Entry, rm reviewModel) {
	if s := m.statuses[e.URL]; s.Title != "" {
		e.Title = s.Title
	}
	dir := filepath.Join(stateDir(), "reviews")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		m.setError(err.Error())
		return
	}
	args := selfReviewArgs(m.st.Session, e, rm.ID, dir)
	if out, err := exec.Command("rex", args...).CombinedOutput(); err != nil {
		m.setError(fmt.Sprintf("rex %s: %s", args[0], strings.TrimSpace(string(out))))
		return
	}
	where := "a new tab"
	if args[0] == "split" {
		where = "a pane beside the PR's worker"
	}
	m.note(fmt.Sprintf("Started a %s self-review in %s", rm.Name, where))
}
