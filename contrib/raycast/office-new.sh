#!/bin/bash

# @raycast.schemaVersion 1
# @raycast.title New Dossier
# @raycast.mode compact
# @raycast.packageName Office
# @raycast.icon 📁
# @raycast.argument1 { "type": "text", "placeholder": "Title" }
# @raycast.argument2 { "type": "text", "placeholder": "Instruction", "optional": true }
# @raycast.argument3 { "type": "text", "placeholder": "Office (sphere)", "optional": true }
# @raycast.description Open a dossier and start its agent on the instruction.

export PATH="$HOME/go/bin:/opt/homebrew/bin:/usr/local/bin:$HOME/.local/bin:$PATH"
args=(open --title "$1" --format text)
[ -n "$2" ] && args+=(--instruction "$2")
[ -n "$3" ] && args+=(--office "$3")
office "${args[@]}" 2>&1 | head -1
