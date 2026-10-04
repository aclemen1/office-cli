#!/bin/bash

# @raycast.schemaVersion 1
# @raycast.title Dossiers To Do
# @raycast.mode inline
# @raycast.refreshTime 10m
# @raycast.packageName Office
# @raycast.icon ✅
# @raycast.description Open dossiers that need you, in every office.

export PATH="$HOME/go/bin:/opt/homebrew/bin:/usr/local/bin:$HOME/.local/bin:$PATH"
offices_json=$(office offices --format json)
office_field() { plutil -extract "result.$1.$2" raw -o - - <<<"$offices_json" 2>/dev/null; }
i=0 line=""
while sphere=$(office_field $i sphere); do
  line+="${line:+  ·  }$sphere $(office_field $i todo) to do, $(office_field $i waiting) waiting"
  i=$((i + 1))
done
echo "$line"
