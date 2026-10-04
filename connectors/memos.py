#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.11"
# ///
"""office source connector for Apple Voice Memos, through the macos CLI.

Every memo not yet seen by this connector's macos-cli client is a signal: the
transcript is the instruction. macos-cli keeps one "seen" cursor per client; the
connector moves it only up to the cursor dossier handed back, so a memo is never
marked seen before its dossier exists.

Protocol 1: `memos.py describe|poll|transition`, JSON on stdin and stdout.
config: client (default "dossier"), engine (default "apple-speech", on device),
locale (default "fr_CH"), backlog (default false: the first poll starts from now
instead of opening one dossier per existing memo).
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


def poll(inp):
    cfg = inp.get("config") or {}
    client = cfg.get("client", "dossier")
    engine = cfg.get("engine", "apple-speech")
    locale = cfg.get("locale", "fr_CH")
    cursor = inp.get("cursor") or ""
    if not cursor and not cfg.get("backlog"):
        newest = macos("voice-memos", "list", "--sort-by", "created:desc", "--limit", "1").get("memos", [])
        return {"signals": [], "events": [], "cursor": newest[0]["createdAt"] if newest else ""}
    if cursor and not inp.get("dry_run"):
        macos("voice-memos", "mark-seen", "--to", cursor, "--client", client)
    memos, after = [], None
    while True:
        args = ["voice-memos", "list", "--unseen", "--client", client, "--sort-by", "created:asc", "--limit", "200"]
        if after:
            args += ["--after", after]
        r = macos(*args)
        memos += r.get("memos", [])
        page = r.get("page") or {}
        if not page.get("hasMore"):
            break
        after = page.get("nextAfter")
    signals, newest = [], cursor
    for m in memos:
        if cursor and m["createdAt"] <= cursor:
            continue
        t = macos("voice-memos", "transcribe", m["id"], "--engine", engine, "--locale", locale)
        text = ((t.get("transcript") or {}).get("text") or "").strip()
        title = m.get("title") or "Mémo"
        md = (f"---\ntype: Voice Memo\ntitle: {json.dumps(title, ensure_ascii=False)}\n"
              f"sources: [{{\"id\": \"memos:memo/{m['id']}\", \"last_modified\": \"{m['createdAt']}\"}}]\n"
              f"generated: {{\"by\": \"process:office-source-memos\", \"engine\": \"{engine}\"}}\n---\n"
              f"# {title}\n\nEnregistré le {m['createdAt']}, {round(m.get('duration', 0))} s.\n\n{text}\n")
        signals.append({
            "source_ref": f"memos:memo/{m['id']}",
            "thread_ref": "",
            "title": first_words(text) or title,
            "instruction": text,
            "summary": {"memo": title, "date": m["createdAt"], "seconds": round(m.get("duration", 0))},
            "files": [{"name": "memo.md", "content": md}],
            "url": "",
            "at": m["createdAt"],
        })
        newest = max(newest, m["createdAt"])
    return {"signals": signals, "events": [], "cursor": newest}


def first_words(text, n=8):
    words = text.replace("\n", " ").split()
    if not words:
        return ""
    s = " ".join(words[:n])
    return s + ("…" if len(words) > n else "")


def main():
    verb = sys.argv[1] if len(sys.argv) > 1 else ""
    try:
        inp = json.load(sys.stdin) if verb in ("poll", "transition") else {}
        if verb == "describe":
            out = {"name": "memos", "protocol": 1, "verbs": ["describe", "poll", "transition"]}
        elif verb == "poll":
            out = poll(inp)
        elif verb == "transition":
            out = {"ok": True, "detail": "a voice memo has no state to reflect"}
        else:
            raise Fail(f"unknown verb {verb!r}; expected describe, poll or transition")
    except Fail as e:
        json.dump({"error": {"message": str(e)}}, sys.stdout)
        sys.exit(1)
    json.dump(out, sys.stdout, ensure_ascii=False)


if __name__ == "__main__":
    main()
