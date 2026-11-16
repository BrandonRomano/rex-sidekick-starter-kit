package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestModelPicker(t *testing.T) {
	e := Entry{URL: "https://github.com/acme/api/pull/7", Repo: "acme/api", Number: 7, Title: "Mine", Author: "me"}
	m := &model{entries: []Entry{e}, me: "me", tab: tabPRs, width: 63, height: 30,
		statuses: map[string]Status{e.URL: {State: "OPEN"}}, inflight: map[string]bool{}, interval: time.Minute}
	press := func(k string) { m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}) }

	press("c")
	if m.modelPicker == nil {
		t.Fatal("c should open the model picker")
	}
	view := m.View()
	for _, rm := range fallbackReviewModels {
		if !strings.Contains(view, rm.Name) {
			t.Errorf("the picker should list %s:\n%s", rm.Name, view)
		}
	}
	if !strings.Contains(view, "› "+defaultReviewModel) || !strings.Contains(view, "j/k model") {
		t.Errorf("the cursor should start on %s:\n%s", defaultReviewModel, view)
	}
	press("j")
	if got := m.modelPicker.models[m.modelPicker.cur].ID; got != "sonnet" {
		t.Errorf("j should move to sonnet, got %s", got)
	}
	press("k")
	press("k")
	press("k")
	if m.modelPicker.cur != 0 {
		t.Errorf("k should stop at the top, got %d", m.modelPicker.cur)
	}

	// The picker starts on the model used last.
	m.modelPicker = nil
	m.lastReviewModel = "haiku"
	press("c")
	if got := m.modelPicker.models[m.modelPicker.cur].ID; got != "haiku" {
		t.Errorf("the picker should start on the last model, got %s", got)
	}
}

// writeCatalog writes a model catalog file like the claude CLI caches.
func writeCatalog(t *testing.T, dir, name string, fetchedAt int64, models string) {
	t.Helper()
	body := fmt.Sprintf(`{"version":2,"fetchedAt":%d,"staleAt":%d,"catalog":{"surface":"cc","config":{"id":"cc","models":[%s]}}}`,
		fetchedAt, fetchedAt+1000, models)
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReviewModelsFromCatalog(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	dir := filepath.Join(cfg, "cache", "model-catalog")

	// No catalog: the aliases.
	if got := reviewModels(); !slices.Equal(got, fallbackReviewModels) {
		t.Errorf("without a catalog, want the aliases, got %v", got)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	// A catalog that doesn't parse: still the aliases.
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := reviewModels(); !slices.Equal(got, fallbackReviewModels) {
		t.Errorf("with a broken catalog, want the aliases, got %v", got)
	}

	// The newest catalog wins, and only its main section shows, in its order.
	writeCatalog(t, dir, "old.json", 100, `{"id":"claude-fable-5-1","name":"Fable 5.1","short_name":"Fable","section":"main"}`)
	writeCatalog(t, dir, "new.json", 200, `
		{"id":"claude-opus-5-5","name":"Opus 5.5","short_name":"Opus","section":"main","quick_select":true},
		{"id":"claude-fable-5-2","name":"Fable 5.2","short_name":"Fable","section":"main"},
		{"id":"claude-haiku-5-5","name":"Haiku 5.5","short_name":"Haiku","section":"main"},
		{"id":"claude-fable-5-1","name":"Fable 5.1","short_name":"Fable","section":"overflow"}`)
	want := []reviewModel{
		{"claude-opus-5-5", "Opus 5.5", "opus"},
		{"claude-fable-5-2", "Fable 5.2", "fable"},
		{"claude-haiku-5-5", "Haiku 5.5", "haiku"},
	}
	if got := reviewModels(); !slices.Equal(got, want) {
		t.Errorf("reviewModels() = %v, want %v", got, want)
	}

	// The picker shows each name with its full ID, starts on the default
	// family, and remembers the family, not the version.
	e := Entry{URL: "https://github.com/acme/api/pull/7", Repo: "acme/api", Number: 7, Title: "Mine", Author: "me"}
	m := &model{entries: []Entry{e}, me: "me", tab: tabPRs, width: 63, height: 30,
		statuses: map[string]Status{e.URL: {State: "OPEN"}}, inflight: map[string]bool{}, interval: time.Minute}
	m.openModelPicker(e)
	view := m.modelPicker.render(80)
	for _, line := range []string{"Opus 5.5   claude-opus-5-5", "Fable 5.2  claude-fable-5-2"} {
		if !strings.Contains(view, line) {
			t.Errorf("the picker should show %q:\n%s", line, view)
		}
	}
	if got := m.modelPicker.models[m.modelPicker.cur].ID; got != "claude-opus-5-5" {
		t.Errorf("the picker should start on opus, got %s", got)
	}
	m.lastReviewModel = "fable"
	m.openModelPicker(e)
	if got := m.modelPicker.models[m.modelPicker.cur].ID; got != "claude-fable-5-2" {
		t.Errorf("the picker should start on the newest fable, got %s", got)
	}
}

func TestSelfReviewArgs(t *testing.T) {
	oldExists, oldDir := paneExists, paneDir
	defer func() { paneExists, paneDir = oldExists, oldDir }()
	open := map[string]bool{"block:worker": true}
	paneExists = func(_, b string) bool { return open[b] }
	paneDir = func(_, b string) string { return "/code/api" }

	e := Entry{URL: "https://github.com/acme/api/pull/7", Title: "Fix it", NotifySession: "s1", NotifyBlock: "block:worker"}
	claude := []string{"--", "claude", "--model", "opus", "--name", "Self-review: Fix it", "/code-review " + e.URL}

	got := selfReviewArgs("s1", e, "opus", "/state/reviews")
	want := append([]string{"split", "-s", "s1", "-b", "block:worker", "--split=right",
		"--label", "Self-review: Fix it", "--focus=false", "--cwd", "/code/api"}, claude...)
	if !slices.Equal(got, want) {
		t.Errorf("with the worker's pane open:\n got %q\nwant %q", got, want)
	}

	paneDir = func(_, _ string) string { return "" }
	if got := selfReviewArgs("s1", e, "opus", "/state/reviews"); slices.Contains(got, "--cwd") {
		t.Errorf("no folder from Rex should leave --cwd off: %q", got)
	}

	tab := append([]string{"window", "new", "-s", "s1", "Self-review: Fix it", "--focus=false", "--cwd", "/state/reviews"}, claude...)
	open["block:worker"] = false
	if got := selfReviewArgs("s1", e, "opus", "/state/reviews"); !slices.Equal(got, tab) {
		t.Errorf("with the worker's pane closed:\n got %q\nwant %q", got, tab)
	}
	open["block:worker"] = true
	if got := selfReviewArgs("s2", e, "opus", "/state/reviews"); got[0] != "window" {
		t.Errorf("a pane in another session should get a new tab: %q", got)
	}
	e.NotifyBlock = ""
	if got := selfReviewArgs("s1", e, "opus", "/state/reviews"); got[0] != "window" {
		t.Errorf("a PR with no pane should get a new tab: %q", got)
	}
}
