# Raycast Script Commands

Raycast → Settings → Extensions → Script Commands → Add Directories → this directory.

| Command | Does |
|---|---|
| New Dossier | `office open` with a title, an instruction and an office (sphere name, optional) |
| Search Dossiers | `office search` in every office |
| Open Dossier | `office attach`: focuses the dossier's agent tab; set `DOSSIER_TERMINAL_APP` to bring the terminal to the front |
| Dossiers To Do | inline count of what needs you and what waits, refreshed every 10 minutes |

The scripts expect `office` in `~/go/bin`, `/opt/homebrew/bin`, `/usr/local/bin` or `~/.local/bin`.
