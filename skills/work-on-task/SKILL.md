---
name: work-on-task
description: Take one task from a plain brief to a merged PR. Read the brief and the code it touches, post a plan, cut a branch (in its own worktree when a dispatcher started it), build, open a PR, follow up on review and CI, and clean up after it merges. Use this whenever the user hands over a piece of work that should end in a PR ("/work-on-task fix the flaky login test in ~/code/api", "work on adding retries to the uploader"), or comes back to a thread that is already mid-task to check on its PR, land it, or note follow-ups. Do not launch an AI review unless the user explicitly asks for one.
---

# Work on a task

One task, one thread, one branch, one PR.

The argument is a plain brief: what to do, and where if it isn't the current
repo. For example, `/work-on-task fix the flaky login test in ~/code/api`.

## The ground you're standing on

| | |
|---|---|
| Repo | the git repo the brief names, or the one you were started in: `git rev-parse --show-toplevel`. Call its absolute path `<repo>` below. |
| Default branch | `main` in the examples below. If the repo's is different (`gh repo view --json defaultBranchRef`), use that. |
| Conventions | whichever of `AGENTS.md`, `CLAUDE.md`, or `CONTRIBUTING.md` sits in the repo root is the source of truth. Read it; don't work from memory of it. Commit style, comment style, generated files, and how much to touch usually live there. |

## 1. Read the brief, then name the thread

Pick a two-to-four-word kebab-case slug from the brief (`login-test-flake`).
You'll use it for the branch, the worktree, and the thread title.

If a session-title tool is available (`ToolSearch: set_session_title`), set
the thread's title to the slug or a short phrase. Skip it if a dispatcher
started you; it already named the session.

## 2. Cut a branch

Never commit on `main`. Where the branch goes depends on how you were started:

1. **Already in a linked worktree** (`git rev-parse --git-dir` differs from
   `git rev-parse --git-common-dir`): someone set it up for you. Use it and
   don't make another.
2. **Started by a dispatcher** (a worker protocol is in your system prompt):
   other workers may share this checkout, so make your own worktree, detached
   at `origin/main`, and work only there:
   ```bash
   git -C <repo> fetch origin
   ```
   ```bash
   git -C <repo> worktree add --detach <repo>/.claude/worktrees/<slug> origin/main
   ```
   If `git -C <repo> check-ignore -q .claude/worktrees/x` fails, add
   `.claude/worktrees/` to `<repo>/.git/info/exclude` so the worktree doesn't
   show up in `git status`. Copy in any untracked local env files (`.env` and
   the like) that the work needs. From here on, `<repo>` means the worktree.
3. **Otherwise**, branch in the checkout you're in. Nothing else is working
   in it in parallel.

If the working tree is dirty, stop and ask. Don't stash, and don't carry
someone else's edits onto the new branch.

**Run git as separate `Bash` calls, in this shape:** `git -C <repo> …` with a
literal absolute path, unquoted, no shell variables, no pipes, no redirections.
Permission allow rules match on that prefix.

Name the branch after the slug, following the pattern of the repo's recent
branches (`git -C <repo> branch -r --sort=-committerdate`). Branch from
`origin/main`, not from whatever happens to be checked out:

```bash
git -C <repo> fetch origin
```
```bash
git -C <repo> checkout -b <branch> origin/main
```

If the branch already exists (you're resuming), drop the `-b` and check it
out instead. If you're already on it, leave it.

## 3. Check the brief against the code, then post a plan

Read the code the brief is about before you plan. Worth stopping for:

- the brief describes code that has since moved or changed
- its approach cuts against a pattern the rest of the codebase follows
- it's ambiguous in a way that changes what you'd build, not just how
- it's really two or three PRs in one brief

When one of those hits, ask, and always give a recommendation, not an
open-ended "what do you think?". The user can settle it in one word, but only
if you tell them what you'd do. Use `AskUserQuestion`, or the sidekick inbox
if a dispatcher started you.

Design taste you happen to disagree with is not on that list. Build it.

Then write up the plan: what you understand the task to want, which files you
expect to touch, and anything in the code that surprised you. End the turn
there, so the user can redirect the plan before any code exists to throw away.

If a dispatcher started you, ask for the go in the sidekick inbox instead, as
its protocol describes. Put the plan, any judgment calls, and your
recommendation in `--detail`:

```sh
rex-sidekick ask --no-open --detail "<plan, judgment calls, recommendation>" \
  "Approve the plan for <slug>?" "Approve"
```

Run it in the background and wait for the answer. The user approves, or types
a comment instead. Fold any change into the plan before you build.

## 4. Build it

Read the conventions file from the repo root first. Nothing loads it for you,
and it is what governs here.

**Breaking a pattern needs the user's sign-off.** If the work only lands
cleanly by doing something the codebase doesn't already do (a new dependency,
a new directory, a second way to do something that already has a way), stop
and ask, with your recommendation first. Mentioning it in the PR is not a
substitute for asking before you build it.

Run what CI runs before you consider a chunk finished. Open the workflow files
(`.github/workflows/*` or the repo's equivalent) and run locally whatever they
run for the areas you touched. Generated files (IDs, lockfiles, codegen) come
from the repo's own scripts; the conventions file says which.

**Do not launch an AI review.** Not after a chunk, not before the PR. Review
only if the user explicitly asks. Then follow
[On request: AI review](#on-request-ai-review).

## 5. Commit

Follow the conventions file for every commit: style, when you may commit, and
any attribution it asks for. Where it says something, it wins over this section.

**Commit and push after each meaningful change.** That includes the first
implementation and every follow-up the user asks for in the same thread.
Don't batch edits, and don't leave a working tree dirty at the end of a turn.
The pull request is where the commits get reviewed.

Unless the repo says otherwise:

- Conventional prefix with a scope where one fits (`feat(ui):`,
  `fix(auth):`), subject lowercase and imperative, no trailing period. If the
  repo's history uses a different style, match the history.
- A single subject line is the default. Add a body only when a reader cannot
  get the point from the diff.

## 6. Open the PR

```bash
git push -u origin <branch>
gh pr create --title "<title>" --body-file <file>
```

The body says **what** the change does and, when it matters, **why**, not
how. A reader should come away knowing what changed for the people using the
software and which judgment call is worth a second opinion. Include anything
that came out different from the brief, and what the user should look at when
they try it.

## 7. Hand it to the user, then follow up

Give the user what they need to test it without asking:

- the PR link
- the branch, and the checkout it's in (the worktree path if you made one)
- what to actually look at, in a sentence

Send a `PushNotification` too (`ToolSearch: select:PushNotification`), since
these threads run long and the user is usually elsewhere by now. If a
dispatcher started you, post the PR to the sidekick inbox and send the
dispatcher its `PR opened` note instead, as its protocol says.

Then stop. Don't poll for the merge.

When the thread wakes up with a change to the PR (failing checks, review
comments, a conflict), fix it on the branch, commit and push as in step 5, and
say what you did. Re-run a check that failed for reasons unrelated to your
change once; if it fails again, tell the user.

## 8. When it merges

Whenever the thread wakes up again (the user says it merged, or a PR watcher
reports it), confirm rather than assume:

```bash
gh pr view <n> --json state,mergedAt,mergeCommit
```

Once it's actually merged:

1. **If you're in a worktree**, stop here: you can't clean up the directory
   you're standing in. Tell whoever set it up (the dispatcher with its
   `CLOSED OUT` note, or the user) that it merged and the worktree is safe to
   remove, and skip to step 3. They run step 2 from the main checkout.

   **Otherwise**, switch the checkout back to `main` and pull:
   ```bash
   git -C <repo> checkout main
   ```
   ```bash
   git -C <repo> pull origin main
   ```
2. Clean up merged worktrees and branches, this task's and any others left
   behind by earlier threads:
   ```bash
   bash ~/.claude/skills/work-on-task/scripts/clean-merged.sh <repo>
   ```
   It removes a worktree or local branch only when its PR merged (or its
   commits are already in `main`) and it has no uncommitted changes or open
   PR. Everything else is printed as `KEPT` with the reason. Report anything
   `KEPT` rather than forcing it. It never touches the remote.
3. Summarize in the thread: what landed, what's outstanding if anything, and
   whether the thread is safe to archive. Only say it's safe when nothing is
   left.

## 9. Follow-ups

At the end, list any leftovers you noticed and left alone while working, plus
out-of-scope findings if the user asked for a review. Ask whether they want
any of them picked up as new work.

## On request: AI review

**Only when the user explicitly asks for a review.** Do not run this as part
of the default flow, before a PR, or after each chunk.

Hand the diff to a review subagent:

```
Agent
  subagent_type: "general-purpose"
  description: "Review <slug> diff"
  run_in_background: false
  prompt: |
    Review the change on branch <branch> in <repo>.

    The task it implements: <paste the brief>

    Start with `git diff origin/main...HEAD`. Then read the conventions file
    in the repo root (<AGENTS.md | CLAUDE.md | CONTRIBUTING.md>) before
    judging anything. It holds this project's conventions, and a change that
    breaks one is a finding. Read enough surrounding code to judge whether
    the change fits how this codebase already does things.

    Report findings ranked most-serious first. For each: the file and line,
    what is wrong, and a concrete scenario where it bites. Correctness first,
    then convention breaks, then simplifications. Say so plainly if you find
    nothing worth raising. A clean review is a real outcome.
```

Then go back and forth with it, using `SendMessage` addressed to the agent id
from the spawn result, until you agree on every finding. Each one ends one of
three ways:

- **you fix it**, then tell the reviewer what you changed
- **you push back with evidence**: the file says otherwise, the test covers
  it, it misread the flow. Show the evidence rather than asserting
- **you both agree it's real but out of scope**: note it as a follow-up for
  step 9

Don't close the loop while a finding is still contested. If you and the
reviewer deadlock, put both positions in front of the user with your
recommendation.
