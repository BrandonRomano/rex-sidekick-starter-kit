#!/bin/sh
# SessionStart hook. Tells Claude to name its Rex tab after the work it's doing.
# Prints nothing outside Rex, so it's a no-op in other terminals.
[ -n "$REX_SESSION" ] && [ -n "$REX_BLOCK" ] || exit 0
command -v rex >/dev/null 2>&1 || exit 0

window=$(rex block inspect -s "$REX_SESSION" "$REX_BLOCK" --json 2>/dev/null | jq -r '.block.window_id // empty')
[ -n "$window" ] || exit 0
label=$(rex window ls -s "$REX_SESSION" --json 2>/dev/null | jq -r --arg w "$window" '.windows[] | select(.window_id == $w) | .label // empty' 2>/dev/null)

cat <<EOF
This Claude session runs in Rex tab \`$window\` (current label: "${label:-unknown}").

Name the tab after the work. When the first request makes the task clear, run this
without asking and without announcing it:

  rex window rename -s "\$REX_SESSION" $window <label>

Use two to four lowercase words joined by hyphens (\`fix-login-redirect\`). If the current label
already describes the work (a dispatcher worker's tab, say), leave it. Rename it
again only if the work changes. Don't rename the Rex session itself; other tabs
share it.
EOF
