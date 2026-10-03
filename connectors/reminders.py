#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.11"
# ///
"""dossier source connector for Apple Reminders flags, through the macos CLI.

An open reminder that is flagged or has any priority is a signal: its title is
the dossier title, its notes the instruction. Tags split the spheres: a store takes the reminders that carry
`require_tag`, or the ones that do not carry `exclude_tag`. Closing the dossier
completes the reminder and clears its flag and priority; reopening it
uncompletes it and sets the flag.

Protocol 1: `reminders.py describe|poll|transition|claim`, JSON on stdin and stdout.
claim: a dossier moved to this store; its reminder gets require_tag and loses exclude_tag.
config: require_tag or exclude_tag (tag names without '#'), list (optional).
"""
import json
import subprocess
import sys


class Fail(Exception):
    pass


def macos(*args):
    p = subprocess.run(["macos", *args, "--format", "json"], capture_output=True, text=True)
    out = p.stdout.strip()
    if not out:
        raise Fail(f"macos {' '.join(args)} returned nothing: {p.stderr.strip()[:300]}")
    d = json.loads(out)
    if not d.get("ok"):
        err = d.get("error") or {}
        raise Fail(f"macos {' '.join(args)}: {err.get('message', err)}")
    return d.get("result", {})


def tags_of(item):
    return {t.lstrip("#").lower() for t in item.get("tags") or []}


def belongs(item, cfg):
    tags = tags_of(item)
    if cfg.get("require_tag") and cfg["require_tag"].lstrip("#").lower() not in tags:
        return False
    if cfg.get("exclude_tag") and cfg["exclude_tag"].lstrip("#").lower() in tags:
        return False
    return True


def is_signal(item):
    return bool(item.get("flagged")) or bool(item.get("priority"))


def poll(inp):
    cfg = inp.get("config") or {}
    items, after = [], None
    while True:
        args = ["reminders", "items", "list", "--limit", "200",
                "--fields", "id,title,notes,tags,list,createdAt,modifiedAt,completed,flagged,priority"]
        if cfg.get("list"):
            args += ["--list", cfg["list"]]
        if after:
            args += ["--after", after]
        r = macos(*args)
        items += r.get("items", [])
        page = r.get("page") or {}
        if not page.get("hasMore"):
            break
        after = page.get("nextAfter")
    signals = []
    for it in items:
        if it.get("completed") or not is_signal(it) or not belongs(it, cfg):
            continue
        notes = (it.get("notes") or "").strip()
        title = it.get("title") or "Rappel"
        md = (f"---\ntype: Reminder\ntitle: {json.dumps(title, ensure_ascii=False)}\n"
              f"tags: {json.dumps(sorted(tags_of(it)), ensure_ascii=False)}\n"
              f"sources: [{{\"id\": \"reminders:item/{it['id']}\", \"last_modified\": \"{it.get('modifiedAt', '')}\"}}]\n"
              f"generated: {{\"by\": \"process:dossier-source-reminders\"}}\n---\n"
              f"# {title}\n\nListe : {it.get('list', '')}\n\n{notes}\n")
        signals.append({
            "source_ref": f"reminders:item/{it['id']}",
            "thread_ref": "",
            "title": title,
            "instruction": notes,
            "summary": {"list": it.get("list", ""), "tags": ", ".join(sorted(tags_of(it)))},
            "files": [{"name": "reminder.md", "content": md}],
            "url": "",
            "at": it.get("createdAt", ""),
        })
    return {"signals": signals, "events": [], "cursor": ""}


def transition(inp):
    ref, to, frm = inp["source_ref"], inp["to"], inp["from"]
    if not ref.startswith("reminders:item/"):
        raise Fail(f"not a reminder reference: {ref}")
    rid = ref.split("/", 1)[1]
    if to == "done":
        macos("reminders", "items", "complete", rid)
        macos("reminders", "items", "update", rid, "--clear-flagged", "--clear-priority")
        return {"ok": True, "detail": "reminder completed, flag and priority cleared"}
    if frm == "done" and to == "open":
        macos("reminders", "items", "uncomplete", rid)
        macos("reminders", "items", "update", rid, "--flagged")
        return {"ok": True, "detail": "reminder uncompleted, flag set"}
    return {"ok": True, "detail": "a flag has no waiting state"}


def claim(inp):
    """Make a reminder that now belongs to this store pass its filter: add
    require_tag, remove exclude_tag. The other tags stay."""
    cfg, ref = inp.get("config") or {}, inp["source_ref"]
    if not ref.startswith("reminders:item/"):
        raise Fail(f"not a reminder reference: {ref}")
    rid = ref.split("/", 1)[1]
    item = macos("reminders", "items", "get", rid, "--fields", "id,tags")
    tags = [t.lstrip("#") for t in item.get("tags") or []]
    want = list(tags)
    req, exc = (cfg.get("require_tag") or "").lstrip("#"), (cfg.get("exclude_tag") or "").lstrip("#")
    if req and req.lower() not in {t.lower() for t in want}:
        want.append(req)
    if exc:
        want = [t for t in want if t.lower() != exc.lower()]
    if want == tags:
        return {"ok": True, "detail": "already in this store's filter"}
    if want:
        macos("reminders", "items", "update", rid, "--tags", ",".join(want))
    else:
        macos("reminders", "items", "update", rid, "--clear-tags")
    return {"ok": True, "detail": "tags now " + ", ".join(want or ["none"])}


def main():
    verb = sys.argv[1] if len(sys.argv) > 1 else ""
    try:
        inp = json.load(sys.stdin) if verb in ("poll", "transition", "claim") else {}
        if verb == "describe":
            out = {"name": "reminders", "protocol": 1, "verbs": ["describe", "poll", "transition", "claim"]}
        elif verb == "poll":
            out = poll(inp)
        elif verb == "transition":
            out = transition(inp)
        elif verb == "claim":
            out = claim(inp)
        else:
            raise Fail(f"unknown verb {verb!r}; expected describe, poll, transition or claim")
    except Fail as e:
        json.dump({"error": {"message": str(e)}}, sys.stdout)
        sys.exit(1)
    json.dump(out, sys.stdout, ensure_ascii=False)


if __name__ == "__main__":
    main()
