#!/usr/bin/env bash
# Remove worktrees and local branches whose work is already on the default branch.
# Usage: clean-merged.sh [path inside the repo]   (defaults to the current directory)
# Works from the main checkout or any of its worktrees. Only touches local state, never the remote.
set -euo pipefail

common=$(git -C "${1:-.}" rev-parse --path-format=absolute --git-common-dir)
REPO=$(git -C "$common/.." rev-parse --show-toplevel)
cd "$REPO"

git fetch --prune -q origin
DEFAULT=$(git symbolic-ref --short refs/remotes/origin/HEAD 2>/dev/null | sed 's#^origin/##' || true)
[ -n "$DEFAULT" ] || DEFAULT=$(gh repo view --json defaultBranchRef --jq .defaultBranchRef.name)
merged=$(gh pr list --state merged --limit 1000 --json headRefName,headRefOid)
open=$(gh pr list --state open --limit 200 --json headRefName --jq '.[].headRefName')

# Prints why a branch is safe to delete, or nothing if it must stay.
merged_reason() {
  local b=$1 h n o
  h=$(git rev-parse "$b")
  grep -qxF "$b" <<<"$open" && return

  # Rebase merges rewrite SHAs, so match the branch against its merged PR's head.
  while IFS=: read -r n o; do
    [ -z "$n" ] && continue
    if [ "$h" = "$o" ] || git merge-base --is-ancestor "$h" "$o" 2>/dev/null; then
      echo "PR #$n merged"; return
    fi
  done < <(jq -r --arg b "$b" '.[] | select(.headRefName==$b) | "\(.number):\(.headRefOid)"' <<<"$merged")

  # A branch cut from main and never committed to is also an ancestor of main; its reflog has one entry.
  if git merge-base --is-ancestor "$h" "origin/$DEFAULT" && [ "$(git reflog show --format=%H "$b" | wc -l)" -gt 1 ]; then
    echo "already in $DEFAULT"; return
  fi

  local cherry
  cherry=$(git cherry "origin/$DEFAULT" "$b")
  if [ -n "$cherry" ] && ! grep -q '^+' <<<"$cherry"; then
    echo "every commit already in $DEFAULT"
  fi
}

while read -r path branch; do
  [ "$path" = "$REPO" ] && continue
  reason=$([ -n "$branch" ] && merged_reason "$branch" || true)
  if [ -n "$(git -C "$path" status --porcelain)" ]; then
    echo "KEPT worktree $path: uncommitted changes"
  elif [ -z "$reason" ]; then
    echo "KEPT worktree $path: ${branch:-detached} is not merged"
  else
    git worktree remove "$path" && echo "removed worktree $path ($reason)"
  fi
done < <(git worktree list --porcelain | awk '/^worktree /{p=$2} /^branch /{sub("refs/heads/","",$2); b=$2} /^$/{if(p) print p" "b; p=""; b=""} END{if(p) print p" "b}')
git worktree prune

current=$(git branch --show-current)
in_worktree=$(git worktree list --porcelain | awk '/^branch /{sub("refs/heads/","",$2); print $2}')
for b in $(git branch --format='%(refname:short)'); do
  [ "$b" = "$DEFAULT" ] || [ "$b" = "$current" ] && continue
  grep -qxF "$b" <<<"$in_worktree" && continue
  reason=$(merged_reason "$b")
  if [ -n "$reason" ]; then
    git branch -D "$b" >/dev/null && echo "deleted branch $b ($reason)"
  else
    echo "KEPT branch $b: not merged"
  fi
done
