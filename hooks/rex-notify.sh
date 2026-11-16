#!/bin/sh
# Reports Claude's state to Rex so tabs show when they need attention.
# Writes Rex's program status code (OSC 7501) on every event, plus an OSC 9
# banner when Claude needs you. Hooks have no tty of their own, so this walks
# up the process tree to find the pane's tty.
[ -n "$REX_SESSION" ] || exit 0
input=$(cat)

j() { printf '%s' "$input" | jq -r "$1"; }
event=$(j '.hook_event_name // empty')
kind=""
banner=""
case "$event" in
  Notification)
    state=blocked
    case "$(j '.notification_type // empty')" in
      permission_prompt) kind=permission ;;
      *) kind=question ;;
    esac
    banner=$(j '.message // "Claude needs your attention"') ;;
  PreToolUse)
    state=blocked; kind=question
    banner=$(j '.tool_input.questions[0].question // "Claude has a question"') ;;
  Stop)
    state=done
    banner="Claude finished and is waiting for you" ;;
  StopFailure) state=error ;;
  UserPromptSubmit | PostToolUse) state=working ;;
  SessionEnd) state=clear ;;
  *) exit 0 ;;
esac

# Find the nearest ancestor with a real tty.
tty=""
pid=$$
while [ "$pid" -gt 1 ]; do
  t=$(ps -o tty= -p "$pid" 2>/dev/null | tr -d ' ')
  if [ -n "$t" ] && [ "$t" != "??" ]; then tty="/dev/$t"; break; fi
  pid=$(ps -o ppid= -p "$pid" 2>/dev/null | tr -d ' ')
  [ -n "$pid" ] || break
done
[ -n "$tty" ] || exit 0

status="state=$state:app=claude-code"
[ -n "$kind" ] && status="$status:kind=$kind"
if [ -n "$banner" ]; then
  msg=$(printf '%s' "$banner" | tr -d '\000-\037\177' | cut -c1-200 | base64 | tr -d '\n')
  status="$status:msg=$msg"
fi
printf '\033]7501;%s\007' "$status" > "$tty" 2>/dev/null

[ -n "$banner" ] || exit 0
w=$(rex block inspect -s "$REX_SESSION" "$REX_BLOCK" --json 2>/dev/null | jq -r '.block.window_id // empty')
label=$(rex window ls -s "$REX_SESSION" --json 2>/dev/null | jq -r --arg w "$w" '.windows[] | select(.window_id == $w) | .label // empty' 2>/dev/null)
[ -n "$label" ] && banner="$label: $banner"
# Strip control characters and the OSC separator so the text can't break the sequence.
banner=$(printf '%s' "$banner" | tr -d '\000-\037\177;' | cut -c1-200)
printf '\033]9;%s\007' "$banner" > "$tty" 2>/dev/null
exit 0
