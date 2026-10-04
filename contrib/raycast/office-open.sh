#!/bin/bash

# @raycast.schemaVersion 1
# @raycast.title Open Dossier
# @raycast.mode compact
# @raycast.packageName Office
# @raycast.icon 🗂
# @raycast.argument1 { "type": "text", "placeholder": "Id or alias (D-0042, RDIR)" }
# @raycast.description Focus the dossier's agent tab, resuming its session if needed.

export PATH="$HOME/go/bin:/opt/homebrew/bin:/usr/local/bin:$HOME/.local/bin:$PATH"
# The terminal app that runs herdr, brought to the front after the jump.
TERMINAL_APP="${DOSSIER_TERMINAL_APP:-}"
if out=$(office attach "$1" --format text 2>&1); then
  [ -n "$TERMINAL_APP" ] && open -a "$TERMINAL_APP"
  echo "$1: pane focused"
else
  echo "$out" | head -1
fi
