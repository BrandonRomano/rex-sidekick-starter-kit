#!/bin/sh
# Links this kit's skills into ~/.claude/skills and its hooks into
# ~/.claude/hooks. Anything already there is left alone.
set -eu
kit=$(cd "$(dirname "$0")" && pwd)
mkdir -p "$HOME/.claude/skills" "$HOME/.claude/hooks"

link() {
  if [ -e "$2" ] || [ -L "$2" ]; then
    echo "skipped $2: already exists"
  else
    ln -s "$1" "$2" && echo "linked $2"
  fi
}

# Remove links into the kit whose target is gone, such as a renamed skill.
for l in "$HOME"/.claude/skills/* "$HOME"/.claude/hooks/*; do
  [ -L "$l" ] && [ ! -e "$l" ] || continue
  case $(readlink "$l") in
    "$kit"/*) rm "$l" && echo "removed $l: its target is gone" ;;
  esac
done

for s in "$kit"/skills/*/; do
  s=${s%/}
  link "$s" "$HOME/.claude/skills/$(basename "$s")"
done
for h in "$kit"/hooks/*.sh; do
  link "$h" "$HOME/.claude/hooks/$(basename "$h")"
done

command -v rex-sidekick >/dev/null 2>&1 || echo "rex-sidekick isn't on PATH yet. Run: go install ./cmd/rex-sidekick"
