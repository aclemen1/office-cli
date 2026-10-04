#!/bin/bash

# @raycast.schemaVersion 1
# @raycast.title Search Dossiers
# @raycast.mode fullOutput
# @raycast.packageName Office
# @raycast.icon 🔎
# @raycast.argument1 { "type": "text", "placeholder": "Words" }
# @raycast.description Search the dossiers of every office, open or closed.

export PATH="$HOME/go/bin:/opt/homebrew/bin:/usr/local/bin:$HOME/.local/bin:$PATH"
offices_json=$(office offices --format json)
office_field() { plutil -extract "result.$1.$2" raw -o - - <<<"$offices_json" 2>/dev/null; }
i=0
while root=$(office_field $i root); do
  echo "== $(office_field $i sphere)"
  office search "$1" --status all --office "$root" --format text 2>&1
  echo
  i=$((i + 1))
done
