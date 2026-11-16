package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Status is the part of a PR we track for changes.
type Status struct {
	Title          string     `json:"title"`
	Author         string     `json:"author,omitempty"`
	State          string     `json:"state"` // OPEN, MERGED, CLOSED
	IsDraft        bool       `json:"is_draft"`
	ReviewDecision string     `json:"review_decision"` // APPROVED, CHANGES_REQUESTED, REVIEW_REQUIRED, ""
	MergeState     string     `json:"merge_state"`     // CLEAN, DIRTY, BLOCKED, BEHIND, UNSTABLE, ...
	Checks         string     `json:"checks"`          // none, pending, passing, failing
	FailingChecks  []string   `json:"failing_checks,omitempty"`
	Additions      int        `json:"additions"`
	Deletions      int        `json:"deletions"`
	Activity       int        `json:"activity"` // comments + reviews
	Recent         []Event    `json:"recent,omitempty"`
	Reviewers      []Reviewer `json:"reviewers,omitempty"`
	FetchedAt      time.Time  `json:"fetched_at"`
	LastChange     time.Time  `json:"last_change"` // when any tracked field last changed
	Err            string     `json:"err,omitempty"`
}

// Reviewer is someone asked to review the PR, or who has reviewed it.
type Reviewer struct {
	Name  string `json:"name"`  // login, or org/team for a team
	State string `json:"state"` // PENDING, APPROVED, CHANGES_REQUESTED, COMMENTED, DISMISSED
}

// Event is a comment or review on the PR.
type Event struct {
	Author string    `json:"author"`
	Kind   string    `json:"kind"` // comment, or the review state (APPROVED, COMMENTED, ...)
	At     time.Time `json:"at"`
}

// prFields is what we read for each PR. Comments and reviews only need the
// latest few plus a total, since Recent keeps at most five events.
const prFields = `
fragment pr on PullRequest {
  number title url state isDraft reviewDecision mergeStateStatus additions deletions
  author { login }
  reviewRequests(first: 100) { nodes { requestedReviewer {
    __typename
    ... on User { login }
    ... on Bot { login }
    ... on Mannequin { login }
    ... on Team { slug name }
  } } }
  latestReviews(first: 100) { nodes { author { login } state } }
  commits(last: 1) { nodes { commit { statusCheckRollup { contexts(first: 100) { nodes {
    __typename
    ... on CheckRun { name status conclusion }
    ... on StatusContext { context state }
  } } } } } }
  comments(last: 5) { totalCount nodes { author { login } createdAt } }
  reviews(last: 5) { totalCount nodes { author { login } state submittedAt } }
}`

type login struct {
	Login string `json:"login"`
}

type ghPR struct {
	Number           int    `json:"number"`
	Title            string `json:"title"`
	URL              string `json:"url"`
	State            string `json:"state"`
	IsDraft          bool   `json:"isDraft"`
	ReviewDecision   string `json:"reviewDecision"`
	MergeStateStatus string `json:"mergeStateStatus"`
	Additions        int    `json:"additions"`
	Deletions        int    `json:"deletions"`
	Author           login  `json:"author"`
	ReviewRequests   struct {
		Nodes []struct {
			RequestedReviewer struct {
				Typename string `json:"__typename"`
				Login    string `json:"login"`
				Slug     string `json:"slug"`
				Name     string `json:"name"`
			} `json:"requestedReviewer"`
		} `json:"nodes"`
	} `json:"reviewRequests"`
	LatestReviews struct {
		Nodes []struct {
			Author login  `json:"author"`
			State  string `json:"state"`
		} `json:"nodes"`
	} `json:"latestReviews"`
	Commits struct {
		Nodes []struct {
			Commit struct {
				StatusCheckRollup *struct {
					Contexts struct {
						Nodes []checkContext `json:"nodes"`
					} `json:"contexts"`
				} `json:"statusCheckRollup"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"commits"`
	Comments struct {
		TotalCount int `json:"totalCount"`
		Nodes      []struct {
			Author    login     `json:"author"`
			CreatedAt time.Time `json:"createdAt"`
		} `json:"nodes"`
	} `json:"comments"`
	Reviews struct {
		TotalCount int `json:"totalCount"`
		Nodes      []struct {
			Author      login     `json:"author"`
			State       string    `json:"state"`
			SubmittedAt time.Time `json:"submittedAt"`
		} `json:"nodes"`
	} `json:"reviews"`
}

type checkContext struct {
	Typename   string `json:"__typename"`
	Name       string `json:"name"`
	Context    string `json:"context"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	State      string `json:"state"`
}

func ghJSON(args ...string) ([]byte, error) {
	cmd := exec.Command("gh", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("gh %s: %s", args[0]+" "+args[1], msg)
	}
	return out, nil
}

var (
	urlRe   = regexp.MustCompile(`^https://github\.com/([^/]+/[^/]+)/pull/(\d+)`)
	shortRe = regexp.MustCompile(`^([\w.-]+/[\w.-]+)#(\d+)$`)
)

// Resolve turns a PR reference into a watch list entry. ref may be a URL,
// owner/repo#N, a number or branch in the current repo, or empty for the
// current branch's PR.
func Resolve(ref string) (Entry, error) {
	args := []string{"pr", "view"}
	if m := shortRe.FindStringSubmatch(ref); m != nil {
		args = append(args, m[2], "-R", m[1])
	} else if ref != "" {
		args = append(args, ref)
	}
	args = append(args, "--json", "url,number,title,author")
	out, err := ghJSON(args...)
	if err != nil {
		return Entry{}, err
	}
	var pr struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		URL    string `json:"url"`
		Author login  `json:"author"`
	}
	if err := json.Unmarshal(out, &pr); err != nil {
		return Entry{}, err
	}
	m := urlRe.FindStringSubmatch(pr.URL)
	if m == nil {
		return Entry{}, fmt.Errorf("unexpected PR URL %q", pr.URL)
	}
	return Entry{URL: pr.URL, Repo: m[1], Number: pr.Number, Title: pr.Title, Author: pr.Author.Login}, nil
}

// Login returns the GitHub login gh is signed in as.
func Login() (string, error) {
	out, err := ghJSON("api", "user", "--jq", ".login")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// FetchAll reads the status of every PR in urls with a single GraphQL query,
// so a poll costs one request no matter how many PRs are due. A PR that can't
// be read gets a Status with Err set.
func FetchAll(urls []string) map[string]Status {
	now := time.Now()
	out := map[string]Status{}
	if len(urls) == 0 {
		return out
	}
	var q strings.Builder
	q.WriteString("query {\n")
	alias := map[string]string{}
	for i, u := range urls {
		m := urlRe.FindStringSubmatch(u)
		if m == nil {
			out[u] = Status{Err: "not a GitHub PR URL", FetchedAt: now}
			continue
		}
		owner, name, _ := strings.Cut(m[1], "/")
		a := fmt.Sprintf("p%d", i)
		alias[a] = u
		fmt.Fprintf(&q, "  %s: repository(owner: %q, name: %q) { pullRequest(number: %s) { ...pr } }\n", a, owner, name, m[2])
	}
	q.WriteString("}\n" + prFields)

	fail := func(msg string) map[string]Status {
		for _, u := range alias {
			out[u] = Status{Err: msg, FetchedAt: now}
		}
		return out
	}
	if len(alias) == 0 {
		return out
	}

	// gh exits non-zero when any part of the query fails, but still prints
	// the data it got, so read stdout either way.
	cmd := exec.Command("gh", "api", "graphql",
		"-H", "Accept: application/vnd.github.merge-info-preview+json",
		"-f", "query="+q.String())
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	body, runErr := cmd.Output()

	var resp struct {
		Data map[string]*struct {
			PullRequest *ghPR `json:"pullRequest"`
		} `json:"data"`
		Errors []struct {
			Path    []any  `json:"path"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &resp); err != nil || (resp.Data == nil && len(resp.Errors) == 0) {
		msg := strings.TrimSpace(stderr.String())
		switch {
		case msg != "":
		case runErr != nil:
			msg = runErr.Error()
		case err != nil:
			msg = err.Error()
		default:
			msg = "empty response from GitHub"
		}
		return fail("gh api graphql: " + msg)
	}

	errs := map[string]string{}
	for _, e := range resp.Errors {
		if len(e.Path) > 0 {
			if a, ok := e.Path[0].(string); ok && alias[a] != "" {
				errs[a] = e.Message
				continue
			}
		}
		// An error not tied to one PR, like a rate limit, applies to all.
		return fail("gh api graphql: " + e.Message)
	}
	for a, u := range alias {
		var pr *ghPR
		if r := resp.Data[a]; r != nil {
			pr = r.PullRequest
		}
		switch {
		case pr != nil:
			out[u] = statusOf(*pr, now)
		case errs[a] != "":
			out[u] = Status{Err: errs[a], FetchedAt: now}
		default:
			out[u] = Status{Err: "PR not found", FetchedAt: now}
		}
	}
	return out
}

// statusOf turns one PR from the GraphQL response into a Status.
func statusOf(pr ghPR, now time.Time) Status {
	st := Status{
		Title:          pr.Title,
		Author:         pr.Author.Login,
		State:          pr.State,
		IsDraft:        pr.IsDraft,
		ReviewDecision: pr.ReviewDecision,
		MergeState:     pr.MergeStateStatus,
		Additions:      pr.Additions,
		Deletions:      pr.Deletions,
		FetchedAt:      now,
	}

	var checks []checkContext
	if n := pr.Commits.Nodes; len(n) > 0 && n[0].Commit.StatusCheckRollup != nil {
		checks = n[0].Commit.StatusCheckRollup.Contexts.Nodes
	}
	pending, passed := 0, 0
	seen := map[string]bool{}
	for _, c := range checks {
		name := c.Name
		if name == "" {
			name = c.Context
		}
		var result string
		if c.Typename == "StatusContext" {
			result = c.State
		} else if c.Status != "COMPLETED" {
			result = "PENDING"
		} else {
			result = c.Conclusion
		}
		switch result {
		case "SUCCESS", "NEUTRAL", "SKIPPED":
			passed++
		case "PENDING", "EXPECTED", "QUEUED", "IN_PROGRESS", "WAITING", "REQUESTED", "":
			pending++
		default: // FAILURE, ERROR, CANCELLED, TIMED_OUT, ACTION_REQUIRED, STARTUP_FAILURE, STALE
			if !seen[name] {
				seen[name] = true
				st.FailingChecks = append(st.FailingChecks, name)
			}
		}
	}
	switch {
	case len(st.FailingChecks) > 0:
		st.Checks = "failing"
	case pending > 0:
		st.Checks = "pending"
	case passed > 0:
		st.Checks = "passing"
	default:
		st.Checks = "none"
	}

	st.Reviewers = reviewers(pr)

	var events []Event
	for _, c := range pr.Comments.Nodes {
		events = append(events, Event{Author: c.Author.Login, Kind: "comment", At: c.CreatedAt})
	}
	for _, r := range pr.Reviews.Nodes {
		events = append(events, Event{Author: r.Author.Login, Kind: r.State, At: r.SubmittedAt})
	}
	sort.Slice(events, func(i, j int) bool { return events[i].At.Before(events[j].At) })
	st.Activity = pr.Comments.TotalCount + pr.Reviews.TotalCount
	if len(events) > 5 {
		events = events[len(events)-5:]
	}
	st.Recent = events
	return st
}

// sameStatus reports whether a and b match on every field we track.
func sameStatus(a, b Status) bool {
	strip := func(s Status) Status {
		s.FetchedAt, s.LastChange, s.Err, s.Recent = time.Time{}, time.Time{}, "", nil
		return s
	}
	return reflect.DeepEqual(strip(a), strip(b))
}

// pollInterval says how long to wait before checking a PR again. A PR that
// has been quiet for a while gets checked less often, which keeps a
// long-running sidekick well under GitHub's rate limit. Any change, such as a
// new push starting checks, brings it back to base.
func pollInterval(base time.Duration, s Status, now time.Time) time.Duration {
	switch quiet := now.Sub(s.LastChange); {
	case quiet < 10*time.Minute:
		return base
	case quiet < 30*time.Minute:
		return max(base, time.Minute)
	case quiet < 2*time.Hour:
		return max(base, 2*time.Minute)
	default:
		return max(base, 5*time.Minute)
	}
}

// Diff describes what changed between two statuses, in the order a reader
// should care about. It returns nothing for changes not worth a ping, like
// checks starting to run again.
func Diff(old, cur Status) []string {
	if cur.Err != "" || old.FetchedAt.IsZero() {
		return nil
	}
	var out []string
	if old.State != cur.State {
		out = append(out, strings.ToLower(cur.State))
	}
	if old.IsDraft && !cur.IsDraft && cur.State == "OPEN" {
		out = append(out, "marked ready for review")
	}
	if !old.IsDraft && cur.IsDraft {
		out = append(out, "converted to draft")
	}
	if old.Checks != cur.Checks {
		switch cur.Checks {
		case "failing":
			out = append(out, "checks failing ("+strings.Join(cur.FailingChecks, ", ")+")")
		case "passing":
			out = append(out, "checks passing")
		}
	}
	if old.ReviewDecision != cur.ReviewDecision && cur.ReviewDecision != "" {
		out = append(out, "review: "+humanReview(cur.ReviewDecision))
	}
	if n := cur.Activity - old.Activity; n > 0 {
		out = append(out, describeActivity(cur.Recent, n))
	}
	if old.MergeState != "DIRTY" && cur.MergeState == "DIRTY" && cur.State == "OPEN" {
		out = append(out, "has merge conflicts")
	}
	return out
}

func describeActivity(recent []Event, n int) string {
	if n > len(recent) {
		n = len(recent)
	}
	var parts []string
	for _, e := range recent[len(recent)-n:] {
		kind := "comment"
		switch e.Kind {
		case "APPROVED":
			kind = "approval"
		case "CHANGES_REQUESTED":
			kind = "changes requested"
		case "COMMENTED":
			kind = "review"
		}
		parts = append(parts, kind+" from @"+e.Author)
	}
	return "new " + strings.Join(parts, ", ")
}

func humanReview(d string) string {
	switch d {
	case "APPROVED":
		return "approved"
	case "CHANGES_REQUESTED":
		return "changes requested"
	case "REVIEW_REQUIRED":
		return "review required"
	}
	return strings.ToLower(d)
}

// reviewers lists each reviewer once with their latest state. Someone with a
// pending request shows as PENDING even if they reviewed before, since a new
// request means their earlier review is out of date.
func reviewers(pr ghPR) []Reviewer {
	var out []Reviewer
	index := map[string]int{}
	for _, r := range pr.LatestReviews.Nodes {
		if r.Author.Login == "" || r.Author.Login == pr.Author.Login {
			continue
		}
		index[r.Author.Login] = len(out)
		out = append(out, Reviewer{Name: r.Author.Login, State: r.State})
	}
	for _, n := range pr.ReviewRequests.Nodes {
		rr := n.RequestedReviewer
		name := rr.Login
		if rr.Typename == "Team" {
			name = rr.Slug
			if name == "" {
				name = rr.Name
			}
		}
		if name == "" {
			continue
		}
		if i, ok := index[name]; ok {
			out[i].State = "PENDING"
			continue
		}
		out = append(out, Reviewer{Name: name, State: "PENDING"})
	}
	return out
}
