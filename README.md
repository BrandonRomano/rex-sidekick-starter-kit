# rex-sidekick starter kit

## What is this

`rex-sidekick` is a TUI built for [Rex](https://www.superlogical.com/rex). It
runs in a pane next to your Claude Code agents and gives you one place to
follow their PRs, answer their questions, and track their work.

This repo is a starting point, not a finished product. It reflects one way of
running agents, and yours will differ. Fork it and change the TUI and the
skills to fit how you work.

The kit has two parts:

- **`rex-sidekick`**, a small TUI that sits in a narrow Rex pane next to your
  agents. It watches their pull requests, collects their questions in one
  inbox, keeps TODO lists you and they can share, and shows which worker
  agents are stuck on a prompt.
- **Claude Code skills** that teach agents to use it: a dispatcher that hands
  work to worker sessions in their own Rex tabs, the sidekick's own skill, and
  a task lifecycle that workers run from brief to merged PR.

## Prerequisites

- [Rex](https://www.superlogical.com/rex), with the `rex` CLI on PATH
- [Claude Code](https://claude.com/claude-code) (`claude` on PATH)
- [GitHub CLI](https://cli.github.com) (`gh`), logged in with `gh auth login`
- Go 1.27 or newer, to build the sidekick
- `jq` and `git`

## Setup

1. Fork this repo, then clone your fork. The kit is a starting point you're
   meant to change to fit your workflow, so keep it in a repo you own:
   ```sh
   git clone https://github.com/<you>/rex-sidekick-starter-kit
   cd rex-sidekick-starter-kit
   ```
2. Build the sidekick with `go install ./cmd/rex-sidekick`, and make sure
   `$(go env GOPATH)/bin` is on your PATH. The dispatcher skips the sidekick
   if `rex-sidekick` isn't found.
3. Run `./install.sh` to link the skills and hooks into `~/.claude`.
4. Run `rex skill setup` to install Rex's own `rex` skill.
5. Optional: merge `hooks/settings.example.json` into `~/.claude/settings.json`.
   It adds tab status and banners for each agent, and names tabs after their
   work. See [Hooks](#hooks-optional).
6. Check it: start `claude` inside Rex, run `/dispatcher`, and confirm the
   Sidekick pane opens.

## What install.sh does

`install.sh` symlinks each folder in `skills/` into `~/.claude/skills/` and
each script in `hooks/` into `~/.claude/hooks/`. It skips anything already
there, so it won't overwrite your own skills. Because they're symlinks, a
`git pull` updates them. To do it by hand:

```sh
mkdir -p ~/.claude/skills ~/.claude/hooks
for s in skills/*/; do ln -s "$PWD/$s" ~/.claude/skills/; done
ln -s "$PWD"/hooks/*.sh ~/.claude/hooks/
```

The dispatcher and workers use Rex's `rex` skill to drive tabs and panes.

## Hooks (optional)

`hooks/` holds two Claude Code hooks that make agents nicer to run in Rex:

- `rex-notify.sh` reports each agent's state to Rex, so a tab shows when its
  agent is working, done, or needs you, and you get a banner when it asks
  something.
- `rex-tab-name.sh` tells each new session to name its Rex tab after its
  work.

Both do nothing outside Rex. To turn them on, merge
`hooks/settings.example.json` into the `hooks` section of
`~/.claude/settings.json`.

## Skills

| Skill | What it's for |
|---|---|
| `rex-sidekick` | How an agent uses the sidekick: register PRs, listen for `[sidekick]` messages, ask the user questions in the inbox, post messages, ask the user to run a command, and keep TODO lists. Agents load it on their own when they run inside Rex. |
| `dispatcher` | Run `/dispatcher` in a Claude session inside Rex. That session becomes the one you hand work to. It starts a worker Claude session in a new Rex tab for each task, registers it on the sidekick's Workers tab, and closes the tab when the work is done. `worker-protocol.md` is the system prompt each worker starts with. |
| `work-on-task` | `/work-on-task <brief>` takes one task from a plain brief to a merged PR: read the brief and the code, post a plan, branch (in its own worktree when a dispatcher started it), build, open a PR, follow up on review and CI, and clean up once it merges. `scripts/clean-merged.sh` removes worktrees and branches whose PRs merged. |

A typical session:

1. Open a Rex session and start `claude` in it.
2. Run `/dispatcher`. The sidekick opens beside it.
3. Ask for work: "fix the flaky login test in ~/code/api", "add retries to
   the uploader", "find out why the nightly job is slow". The dispatcher starts a worker for
   each.
4. Answer the workers' questions and read their results in the sidekick's
   **Inbox** tab. Their PRs show on the **PRs** tab, and the **Workers** tab
   shows what each one is doing and which are stuck on a prompt.

The skills don't set commit, PR, or review style. Workers follow whatever the
repo's `AGENTS.md`, `CLAUDE.md`, or `CONTRIBUTING.md` says. Put your own
preferences in `~/.claude/CLAUDE.md` or in your repos.

## Using the sidekick

```sh
rex-sidekick pr add <PR>   # watch a PR, opening the Sidekick pane if needed
rex-sidekick pr ls
rex-sidekick pr rm <PR>
rex-sidekick open          # open the Sidekick pane
rex-sidekick open --dispatcher   # ...and hand your own tasks to this pane
rex-sidekick worker add NAME --block ID --task "..."   # list a worker on the Workers tab
rex-sidekick worker ls
```

In the pane: `tab` (or `h`/`l`, or a number) switches tabs, `j`/`k` move, `enter` opens
the PR, `x` stops watching, `r` refreshes, `q` quits. With a box open, those
keys still switch tabs, and the box stays open on its own tab, as you left it.
The exceptions: while you type (on an answer's **Other** row, in a reply to a
message, or in a new task, snooze, or comment box), `h`, `l`, and numbers are text and only `tab`
and `shift+tab` switch; on a question's options, numbers pick an option. On one of your own PRs,
`m` merges it after you confirm with `y`, using the merge method GitHub defaults to
for you in that repo.
`c` on one of your own open PRs starts a Claude self-review: pick a model
(`fable`, `opus`, `sonnet` or `haiku`, which the claude CLI resolves to the latest
of each) and press enter. It runs `/code-review <PR>` in a pane to the right of
the agent that registered the PR, in that agent's folder, or in a new tab if
that pane is gone. Findings stay in that pane; nothing is posted to GitHub.
Click a PR's title or `repo #N` to open it.

The **TODO** tab shows your own lists at the top, then task lists from agents,
one list per agent. Agents add and update tasks with `rex-sidekick todo`. A list
whose tasks are all done moves to the bottom, and it disappears 15 minutes
after its last task finishes. Your own finished tasks go the same way, though
your lists' headings stay. Boxes split the tab into **Yours**, **Agents**, and
**Completed**. Press `enter` on a list's heading to fold it, and again to open
it.
A chain of tasks, each waiting on the one before, shows as numbered steps.
When several tasks wait on the same one, they show under it with `↳`. Press `space` to mark an
agent's task done. You can add a note, and the agent gets it as a
`[sidekick]` message. Press `c` to message the agent about a task without
finishing it. Press Enter on a list's **+ New task** to add a reminder to
it, which never messages anyone. You start with **My list**. To keep more lists, press Enter on
**+ New group** at the top of the tab and name the list. `e` on one of those
lists' headings renames it, and `x` removes it with its tasks. `x` removes My list
the same way, and it stays gone. A list you made can hold sub-groups, and a
sub-group can hold sub-groups of its own, as deep as you like. Press Enter on
**+ New sub-group** at the end of a list, or at the end of a sub-group, and
name it. Each sub-group shows its own heading, indented, after the list's own
tasks, with its own **+ New task**. `enter` on its heading folds it, `e`
renames it, and `x` removes it with its tasks and the sub-groups inside it,
asking first the way a list does. A heading counts the open tasks in the
sub-groups inside it too. Sub-group names can't hold a `/`, and two side by
side can't differ only in case or punctuation. My list has no sub-groups.
Each sub-group finishes on its own: 15 minutes after its last task is done,
its finished tasks clear, and the sub-group stays. Press `c` on one of your own tasks to hand it to the
session's dispatcher, the agent pane that ran `rex-sidekick open --dispatcher`.
It gets the task and your optional note, and marks the task done when the
work is finished. A task someone is working on shows `◐`. Agents mark their
own, a handed-off task gets it right away, and `p` toggles it on your own
tasks. `x` removes your own tasks and finished agent tasks.
`x` on a list's heading removes the whole list, after asking first, even when
the list is empty. The prompt names the list and how many tasks it holds.
Press `x` again to remove it, or any other key to cancel.
Press `i` on one of your own tasks to edit it in your editor (`$VISUAL`, then
`$EDITOR`, then `vi`), in a pane that opens under the sidekick. The file has
the task's text as a Markdown title (`# Fix the login redirect`) on the first line, then
a blank line, then its details, such as a plan, in Markdown. The `#` is
optional when you save. Save and quit to update the task. If you clear the
first line, the task keeps its text. Quitting with an error, such as `:cq` in
vim, leaves the task as it was. On an agent's task, `i` shows its
details, drawn as Markdown, and `j`/`k` scroll them. A task that has details
shows `(i)` after its text. When you hand a task to the dispatcher with `c`,
its details go along.
Press `m` on one of your own tasks to move it to another of your lists, or into
one of their sub-groups (or out of one, back to its list). Pick the place with
`j`/`k` and press Enter, or `esc` to cancel. A folded list's sub-groups are on
offer too. The task keeps its
ID, text, details, snooze, and in-progress state, and tasks waiting on it go
with it. A move works between any two of your lists, whether they live in the
session or in git repos, and commits the list files it changes on both sides.
From the command line, `rex-sidekick todo move <id> <list>` does the same, with
`"My list"` for My list, and a sub-group by its path as `todo ls` shows it,
like `"Home / Basement / Boiler"`. An agent adds a task to one of your lists the
same way with `rex-sidekick todo add --mine <list> <task>`.
Press `z` to snooze a task, or to push one back: type `1h`,
`tomorrow`, `fri 9am`, or `2026-11-10 14:00`. The task comes back at that time
with a bell. Leave the box empty to show it now. While it's snoozed, a task
from your lists waits in **Later**, along with any reminders agents left for
you, and goes back to its list when its time comes. A list's heading counts
its snoozed tasks. A list holding nothing but snoozed tasks starts folded, and
opening it shows them with the time they come back.

A list you made can keep its tasks in a git repo instead of in the Rex
session. Press `g` on its heading and type the repo's path (a folder that
doesn't exist yet, or an empty one, gets `git init`). The list gets a folder in
the repo named after it, like `home-renovation/` for Home
Renovation, and its heading shows `· git`. The folder's `data.json` holds the
list's own tasks and the order of its sub-groups. Each sub-group is a folder
inside it, like `home-renovation/basement/`, with a `data.json` of its
own. Several lists can share a repo, as long as their folder names differ. A
list kept in a `<list name>.json` file, the way older versions stored it,
still loads, and its next change moves it into its folder. Every change to the
list, from the tab, the editor, or an agent, commits its files and nothing else, with the time in the message,
like `rex-sidekick: update Work (2026-11-13 14:02:05 -0800)`. When the repo's
branch has an upstream, each commit is pushed in the background. Press `r` on
the TODO tab to pull (`git pull --rebase`), push, and reload. If the pull
conflicts with your commits, the sidekick aborts it, keeps your commits, and
says which repo to fix by hand. Renaming the list or a sub-group renames its
folder, and removing one deletes its folder, which the remove prompt says.
Press `g` and clear the box to move the list back into the session, sub-groups
and all; that leaves the repo's files alone. `g` on a sub-group's heading
does nothing, since a sub-group lives wherever its list does.

Because the repo doesn't belong to a session, pointing a list in a new session
at the same repo picks it up, joined by task ID with any tasks that list
already had there. From the command line:

```sh
rex-sidekick todo backend Work git ~/todos   # makes the list Work if it's missing
rex-sidekick todo backend Work local         # back into the session
rex-sidekick todo backend                    # where each list lives
rex-sidekick todo sync                       # what r does
```

My list always stays with the session.

The **PRs** tab puts each group in its own box: **Needs review**, then
**Mine**, then **Others** (watched PRs someone else wrote). Empty groups are
hidden. The keys follow the selected PR's group. Once a PR is
merged or closed, the sidekick stops watching it and moves it below a rule at
the bottom of its box, with a gray icon. Those stay until the sidekick quits,
or until you clear one with `x`.

**Needs review** lists open PRs that ask you by name for a review, across all
repos, checked every 2 minutes. Press `c` on one to open a new Rex tab where
Claude reviews it. First pick the model, as for a self-review, then a box opens for any context you want Claude to have;
press enter to start, with or without it, or esc to cancel. Claude rates the PR's significance and risk. If both are low, it sends the
approval to your Inbox. For anything else it sends you questions there,
with enough detail to answer without opening the PR. Everything it would post
on the PR, approvals included, arrives as a `rex-sidekick run` request showing
the exact text. Saying yes runs it under your account, so the review agent
never needs permission to post. When it's done, it closes its tab. The prompt is
`reviewPrompt` in `review.go`.
Press `u` to unassign yourself from a PR you won't review. If you were
requested by name, the sidekick removes you from its reviewers on GitHub. A PR
requested from a team can't be unassigned, so it's just hidden. Either way it
drops off the list for the rest of the session. It first asks for an optional comment, like
"Deferring to Mike on this one", and posts it on the PR. Leave it empty to skip
the comment, or press `esc` to keep the PR.

The **Inbox** tab collects questions from agents, so you can answer
everyone's questions in one place. An agent asks with:

```sh
rex-sidekick ask "Squash or rebase PR #42?" Squash Rebase   # --multi for checkboxes
```

An agent with something to tell you, and nothing to ask, such as a PR that's
ready for review or a summary of finished work, posts it with:

```sh
rex-sidekick tell "PR ready for review: Fix login redirect" --detail "..."
```

It shows with a blue `i` instead of a `?`. Open it to read it, and press `x`
to dismiss it. The agent isn't waiting on it, so dismissing it sends nothing
back. To answer it anyway, press `c` on the open message, type, and press
`enter`. The reply goes to the agent's pane as a `[sidekick]` message, shows
under the message, and leaves the message open; `esc` closes the box without
sending.

`--detail` takes Markdown, for questions and messages alike. The sidekick
draws headings, lists, quotes, **bold**, *italic*, `code`, code blocks, and
links, wrapped to the pane. A single line break stays a line break, as in a
GitHub comment. When the detail is taller than the pane, `j` and `k` scroll it.

An agent that needs you to run a command, like a `sudo` step or an
interactive login, asks with `rex-sidekick run --why "..." "<command>"`. The
inbox shows the exact command. Pick **Run it** and it runs in a new pane under
the sidekick, where you can type into it. When it finishes, the agent gets the
exit status and the end of the output, and the request leaves the inbox. The
pane stays open until you close it.

Open a question with Enter, pick an option (or check several), or type your
own answer on the **Other** row. `x` dismisses a question or message. You can
switch tabs mid-answer, and the answer is still there when you come back. Questions are grouped by topic (`--topic`, or the agent's Rex tab name by
default). After you answer or dismiss one, the next item in its topic opens.

`ask` waits and prints the answer. If it's gone by the time you
answer, the answer goes to the agent's pane as a `[sidekick]` message. An
agent takes a question back with `rex-sidekick withdraw <id>`, such as when
you answered it in the thread, or by stopping its `ask`. The
sidekick rings the terminal bell when a question arrives.

The **Workers** tab lists the agents a dispatcher started, each in its own
Rex tab. The dispatcher registers each one with
`rex-sidekick worker add NAME --block ID --task "..."`, and the sidekick works
out the rest. Every 3 seconds it reads each worker's pane. When Claude Code
sits on a permission prompt, a question, or a folder to trust, nobody else
can see it, since the worker can't send anything while it waits. The sidekick
moves that worker to a red **Waiting on a prompt** box at the top, shows what
the prompt asks and how long it has waited, turns the tab's label red, and
rings the bell. Other workers show as working (Claude is mid-turn), waiting on
user (a question in the inbox), PR open, idle, or done, with their task, PRs,
progress through their TODO steps, and when their pane last changed. Press
`enter` or `f` to bring a worker's pane to the front, and `x` to take it off
the tab. A worker drops off on its own when its tab closes.
`rex-sidekick worker ls` prints the same for the dispatcher.

Each PR is checked every 30 seconds while it's active. Once it has been quiet
for 10 minutes the checks slow down, reaching every 5 minutes after 2 hours,
and any change speeds them back up. If GitHub reports a rate limit, the
sidekick pauses for 5 minutes.

State lives in `~/.local/state/rex-sidekick/`, one set of files per Rex session,
apart from lists kept in git repos.

To have an agent use it, add something like this to its workflow: "Once you
push a PR, run `rex-sidekick pr add <url>`." The skill in `skills/rex-sidekick/` explains the
rest to the agent.

Messages normally get typed into the agent's pane. While the agent keeps
`rex-sidekick wait` running in the background, they go to the wait instead,
so they don't get typed over whatever you're writing. If nobody has waited
for 15 minutes, queued messages get typed after all.

## License

MIT. See `LICENSE`.
