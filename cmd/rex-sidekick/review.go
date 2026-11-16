package main

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
)

// reviewQuery finds open PRs that ask us, by name, for a review. Team
// requests are left out.
const reviewQuery = "is:open is:pr user-review-requested:@me archived:false sort:updated-desc"

// reviewEvery is how often the PRs tab looks for new requests.
const reviewEvery = 2 * time.Minute

// SearchReviewRequests returns the PRs waiting on our review, newest activity
// first, with their statuses.
func SearchReviewRequests() ([]Entry, map[string]Status, error) {
	q := fmt.Sprintf(`query {
  search(query: %q, type: ISSUE, first: 50) {
    nodes { ... on PullRequest { ...pr repository { nameWithOwner } } }
  }
}
%s`, reviewQuery, prFields)
	cmd := exec.Command("gh", "api", "graphql",
		"-H", "Accept: application/vnd.github.merge-info-preview+json",
		"-f", "query="+q)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	body, runErr := cmd.Output()
	var resp struct {
		Data struct {
			Search struct {
				Nodes []struct {
					ghPR
					Repository struct {
						NameWithOwner string `json:"nameWithOwner"`
					} `json:"repository"`
				} `json:"nodes"`
			} `json:"search"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, nil, fmt.Errorf("gh api graphql: %s", msg)
		}
		if runErr != nil {
			return nil, nil, fmt.Errorf("gh api graphql: %w", runErr)
		}
		return nil, nil, fmt.Errorf("gh api graphql: %w", err)
	}
	if len(resp.Errors) > 0 {
		return nil, nil, fmt.Errorf("gh api graphql: %s", resp.Errors[0].Message)
	}
	now := time.Now()
	var entries []Entry
	statuses := map[string]Status{}
	for _, n := range resp.Data.Search.Nodes {
		if n.URL == "" {
			continue
		}
		s := statusOf(n.ghPR, now)
		s.LastChange = now
		entries = append(entries, Entry{URL: n.URL, Repo: n.Repository.NameWithOwner, Number: n.Number, Title: n.Title, Author: n.Author.Login})
		statuses[n.URL] = s
	}
	return entries, statuses, nil
}

// reviewPrompt is what the review agent starts with. %[1]s is the PR's URL,
// %[2]s its owner/repo#N, and %[3]s the user's context, if they gave any.
const reviewPrompt = `Review the pull request %[1]s for me. I've been asked to review it, and I want you to triage it.%[3]s

1. Read the PR: its description, the full diff (gh pr diff), its checks (gh pr checks), and the reviews and comments so far (gh pr view --comments). Clone the repo or read surrounding files only when the diff alone doesn't tell you what a change does.

2. Rate the PR on two scales and tell me both ratings with a one-line reason each:
   - Significance: how much it changes. Low means docs, comments, tests only, renames, small refactors with no change in behavior, or patch-level dependency bumps. High means new features, changed behavior users or other services depend on, or large diffs.
   - Risk: what could break if it's wrong. Low means a mistake would be obvious and easy to undo. High means anything touching auth, permissions, secrets, billing, data migrations or deletes, infrastructure, public APIs, concurrency, or anything hard to roll back.

3. Never run gh pr review or gh pr comment yourself. Anything posted to the PR goes through rex-sidekick run, so I approve it with one click in the inbox and the sidekick runs it under my account. Write the text to a file and show me the same text word for word in --detail:
   rex-sidekick run --topic "Review: %[2]s" --why "Approve %[2]s" --detail "<the review text>" 'gh pr review %[1]s --approve --body-file <file>'
   Use --request-changes or --comment in place of --approve for those, and pass the command as one quoted argument. Run it in the background and wait for its result before you tell me it posted.

   If significance and risk are both low, the PR isn't a draft, and no checks are failing, send me the approval this way right away, with one or two sentences on what you checked.

4. Otherwise, don't approve, comment, or request changes on your own. Ask me through the sidekick inbox instead (see the rex-sidekick skill). Use rex-sidekick ask with --topic "Review: %[2]s" for every question so they're grouped, and run each ask in the background. Ask one question per decision you need from me, such as whether a risky change is acceptable, whether a design choice is right, or what my overall verdict is.

   I'll have very little context when I read these, so make each question stand on its own:
   - Keep the question itself short. Put everything else in --detail.
   - In --detail, say what the PR does in a sentence or two, then exactly what I'm weighing in on: the file and lines, a short excerpt of the code if it helps, what could go wrong, and what the author seems to intend.
   - Give your own recommendation and why.
   - Make the options concrete actions, such as "Approve", "Request changes: add a test for the empty case", or "Ask the author why the retry was removed". I can always type my own answer.

5. Act on my answers. When I pick an option that posts something, like "Approve", send the post with rex-sidekick run as in step 3. My OK on that run is my approval of the exact text, so don't also ask "Post it?" first. If I answer one of your questions in this thread instead, withdraw it from the inbox with rex-sidekick withdraw.

6. When you're done, close this tab. Wait until every question you asked has an answer and you've acted on it. If I've typed anything in this tab, ask me here before closing instead. Then close it:
   rex window close -s "$REX_SESSION" "$(rex whoami --json | jq -r .window_id)"`

// postComment leaves a comment on the PR.
func postComment(e Entry, body string) error {
	out, err := exec.Command("gh", "pr", "comment", e.URL, "--body", body).CombinedOutput()
	if err != nil {
		return fmt.Errorf("gh pr comment: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// removeReviewRequest takes login off the PR's requested reviewers.
func removeReviewRequest(e Entry, login string) error {
	path := fmt.Sprintf("repos/%s/pulls/%d/requested_reviewers", e.Repo, e.Number)
	out, err := exec.Command("gh", "api", "-X", "DELETE", path, "-f", "reviewers[]="+login).CombinedOutput()
	if err != nil {
		return fmt.Errorf("gh api: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// reviewPromptFor fills in reviewPrompt for the PR. The user's context goes
// right after the opening line, so Claude reads it before the steps.
func reviewPromptFor(e Entry, context string) string {
	if context = strings.TrimSpace(context); context != "" {
		context = "\n\nHere's some context from me to keep in mind while you review:\n" + context
	}
	return fmt.Sprintf(reviewPrompt, e.URL, fmt.Sprintf("%s#%d", e.Repo, e.Number), context)
}

// reviewSessionName names the review's Claude session and Rex tab after the
// PR, like "[octocat] Review: Fix the login redirect".
func reviewSessionName(e Entry) string {
	name := "Review: " + e.Title
	if e.Author != "" {
		name = "[" + e.Author + "] " + name
	}
	return name
}

// launchReview opens a new Rex tab with Claude reviewing the PR on model.
func launchReview(session string, e Entry, model, context string) (string, error) {
	label := reviewSessionName(e)
	// Claude never saves trust for the home folder, so reviews run in a folder
	// of their own. The user trusts it once, at the first review.
	dir := filepath.Join(stateDir(), "reviews")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	out, err := exec.Command("rex", "window", "new", "-s", session, label,
		"--focus=false", "--cwd", dir,
		"--", "claude", "--model", model, "--name", label, reviewPromptFor(e, context)).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("rex window new: %s", strings.TrimSpace(string(out)))
	}
	return label, nil
}

// reviewInput is the box for context to give Claude before a review starts,
// or for a comment to leave when archiving a review request.
type reviewInput struct {
	e         Entry
	ta        textarea.Model
	archiving bool
	model     string // the model the review runs on
}

func (m *model) openReviewInput(e Entry, model string) {
	ta := newTextInput("Context for Claude (optional), enter to start")
	ta.Focus()
	m.reviewInput = &reviewInput{e: e, ta: ta, model: model}
}

func (m *model) updateReviewInput(msg tea.KeyMsg) tea.Cmd {
	in := m.reviewInput
	switch msg.String() {
	case "ctrl+c":
		return tea.Quit
	case "esc":
		m.reviewInput = nil
		return nil
	case "enter":
		m.reviewInput = nil
		if in.archiving {
			return m.archive(in.e, strings.TrimSpace(in.ta.Value()))
		}
		m.startReview(in.e, in.model, in.ta.Value())
		return nil
	}
	var cmd tea.Cmd
	in.ta, cmd = in.ta.Update(msg)
	return cmd
}

// startReview opens the review tab and marks the PR as being reviewed.
func (m *model) startReview(e Entry, model, context string) {
	// The fetched status has the PR's current title and author.
	if s := m.statuses[e.URL]; s.Title != "" || s.Author != "" {
		e.Title, e.Author = cmp.Or(s.Title, e.Title), cmp.Or(s.Author, e.Author)
	}
	label, err := launchReview(m.st.Session, e, model, context)
	if err != nil {
		m.setError(err.Error())
		return
	}
	if m.reviewing == nil {
		m.reviewing = map[string]bool{}
	}
	m.reviewing[e.URL] = true
	m.note("Started a Claude review in the Rex tab " + label)
}

func (in *reviewInput) render(w int) string {
	in.ta.SetWidth(max(10, w-6))
	in.ta.SetHeight(3)
	var b strings.Builder
	for _, l := range strings.Split(in.ta.View(), "\n") {
		b.WriteString("    " + l + "\n")
	}
	return b.String()
}
