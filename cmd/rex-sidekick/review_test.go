package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestReviewPromptContext(t *testing.T) {
	e := Entry{URL: "https://github.com/acme/widgets/pull/7", Repo: "acme/widgets", Number: 7}
	plain := reviewPromptFor(e, "  ")
	if strings.Contains(plain, "context from me") || strings.Contains(plain, "%!") {
		t.Errorf("an empty context should leave the prompt alone:\n%s", plain)
	}
	p := reviewPromptFor(e, "Focus on the 100% retry path")
	head, _, _ := strings.Cut(p, "\n\n1.")
	if !strings.Contains(head, "Focus on the 100% retry path") {
		t.Errorf("the context should come before the steps:\n%s", p)
	}
	if !strings.Contains(p, "--topic \"Review: acme/widgets#7\"") {
		t.Errorf("the PR ref is missing:\n%s", p)
	}
}

func TestReviewContextBox(t *testing.T) {
	e := Entry{URL: "https://github.com/acme/widgets/pull/7", Repo: "acme/widgets", Number: 7, Title: "Fix it"}
	m := &model{reviews: []Entry{e}, tab: tabPRs, width: 50, height: 30}
	m.Update(key("c"))
	m.Update(key("j"))
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.reviewInput == nil || m.reviewInput.model != "sonnet" || m.reviewInput.e.URL != e.URL {
		t.Fatal("c should open the context box for the selected PR")
	}
	for _, r := range "auth" {
		m.Update(key(string(r)))
	}
	if view := m.View(); !strings.Contains(view, "auth") || !strings.Contains(view, "esc cancel") {
		t.Errorf("the box should show what was typed:\n%s", view)
	}
	m.Update(key("esc"))
	if m.reviewInput != nil || m.reviewing[e.URL] {
		t.Error("esc should close the box without starting a review")
	}
}

func TestReviewSessionName(t *testing.T) {
	e := Entry{Title: "Fix the login redirect", Author: "octocat"}
	if got, want := reviewSessionName(e), "[octocat] Review: Fix the login redirect"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	e.Author = ""
	if got, want := reviewSessionName(e), "Review: Fix the login redirect"; got != want {
		t.Errorf("with no author, got %q, want %q", got, want)
	}
}
