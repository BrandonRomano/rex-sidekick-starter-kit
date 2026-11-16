package main

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
)

func TestDiff(t *testing.T) {
	now := time.Now()
	base := Status{State: "OPEN", Checks: "pending", FetchedAt: now}

	cases := []struct {
		name string
		old  Status
		cur  Status
		want []string
	}{
		{"first fetch is a baseline", Status{}, Status{State: "OPEN", Checks: "failing"}, nil},
		{"no change", base, base, nil},
		{"checks start again", Status{State: "OPEN", Checks: "passing", FetchedAt: now}, base, nil},
		{"checks fail", base, Status{State: "OPEN", Checks: "failing", FailingChecks: []string{"lint", "test"}},
			[]string{"checks failing (lint, test)"}},
		{"merged", base, Status{State: "MERGED", Checks: "pending"}, []string{"merged"}},
		{"approved with review", base,
			Status{State: "OPEN", Checks: "pending", ReviewDecision: "APPROVED", Activity: 1,
				Recent: []Event{{Author: "ana", Kind: "APPROVED"}}},
			[]string{"review: approved", "new approval from @ana"}},
		{"conflicts", base, Status{State: "OPEN", Checks: "pending", MergeState: "DIRTY"},
			[]string{"has merge conflicts"}},
		{"gh error", base, Status{Err: "boom"}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Diff(c.old, c.cur); !reflect.DeepEqual(got, c.want) {
				t.Errorf("Diff = %q, want %q", got, c.want)
			}
		})
	}
}

func TestReviewers(t *testing.T) {
	var pr ghPR
	if err := json.Unmarshal([]byte(`{
		"author": {"login": "me"},
		"latestReviews": {"nodes": [
			{"author": {"login": "ana"}, "state": "APPROVED"},
			{"author": {"login": "bo"}, "state": "CHANGES_REQUESTED"},
			{"author": {"login": "me"}, "state": "COMMENTED"}]},
		"reviewRequests": {"nodes": [
			{"requestedReviewer": {"__typename": "User", "login": "bo"}},
			{"requestedReviewer": {"__typename": "Team", "slug": "core"}}]}}`), &pr); err != nil {
		t.Fatal(err)
	}
	// bo was asked again after requesting changes, so bo is pending.
	// The author's own review is left out.
	want := []Reviewer{{"ana", "APPROVED"}, {"bo", "PENDING"}, {"core", "PENDING"}}
	if got := reviewers(pr); !reflect.DeepEqual(got, want) {
		t.Errorf("reviewers = %v, want %v", got, want)
	}
}

func TestStatusOf(t *testing.T) {
	var pr ghPR
	if err := json.Unmarshal([]byte(`{
		"state": "OPEN",
		"commits": {"nodes": [{"commit": {"statusCheckRollup": {"contexts": {"nodes": [
			{"__typename": "CheckRun", "name": "lint", "status": "COMPLETED", "conclusion": "FAILURE"},
			{"__typename": "CheckRun", "name": "test", "status": "IN_PROGRESS"},
			{"__typename": "StatusContext", "context": "ci/build", "state": "SUCCESS"}]}}}}]},
		"comments": {"totalCount": 12, "nodes": [{"author": {"login": "ana"}, "createdAt": "2026-01-01T00:00:02Z"}]},
		"reviews": {"totalCount": 3, "nodes": [{"author": {"login": "bo"}, "state": "APPROVED", "submittedAt": "2026-01-01T00:00:01Z"}]}}`), &pr); err != nil {
		t.Fatal(err)
	}
	st := statusOf(pr, time.Now())
	if st.Checks != "failing" || !reflect.DeepEqual(st.FailingChecks, []string{"lint"}) {
		t.Errorf("checks = %s %v, want failing [lint]", st.Checks, st.FailingChecks)
	}
	// Activity counts everything, not just the few events we fetched.
	if st.Activity != 15 {
		t.Errorf("activity = %d, want 15", st.Activity)
	}
	if len(st.Recent) != 2 || st.Recent[0].Author != "bo" {
		t.Errorf("recent should be oldest first, got %v", st.Recent)
	}
}

func TestPollInterval(t *testing.T) {
	now := time.Now()
	base := 30 * time.Second
	for _, c := range []struct {
		quiet time.Duration
		want  time.Duration
	}{
		{time.Minute, base},
		{20 * time.Minute, time.Minute},
		{time.Hour, 2 * time.Minute},
		{3 * time.Hour, 5 * time.Minute},
	} {
		if got := pollInterval(base, Status{LastChange: now.Add(-c.quiet)}, now); got != c.want {
			t.Errorf("quiet %v: got %v, want %v", c.quiet, got, c.want)
		}
	}
}

func TestSameStatusIgnoresFetchTime(t *testing.T) {
	a := Status{State: "OPEN", Checks: "passing", FetchedAt: time.Now()}
	b := a
	b.FetchedAt = a.FetchedAt.Add(time.Minute)
	if !sameStatus(a, b) {
		t.Error("statuses that differ only in fetch time should match")
	}
	b.Checks = "pending"
	if sameStatus(a, b) {
		t.Error("a change in checks should count")
	}
}

func TestReviewColor(t *testing.T) {
	cases := []struct {
		name string
		s    Status
		want lipgloss.Color
	}{
		{"waiting on review", Status{ReviewDecision: "REVIEW_REQUIRED"}, colWarn},
		{"approved", Status{ReviewDecision: "APPROVED"}, colOpen},
		{"changes requested", Status{ReviewDecision: "CHANGES_REQUESTED"}, colClosed},
		{"no decision, no reviewers", Status{}, colWarn},
		{"no decision, approved", Status{Reviewers: []Reviewer{{"ana", "APPROVED"}, {"bo", "PENDING"}}}, colOpen},
		{"no decision, changes requested", Status{Reviewers: []Reviewer{{"ana", "APPROVED"}, {"bo", "CHANGES_REQUESTED"}}}, colClosed},
	}
	for _, c := range cases {
		if got := reviewColor(c.s); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
