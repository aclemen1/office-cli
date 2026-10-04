#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.11"
# ///
"""office source connector for Gmail, through the gws CLI.

Signals are yellow stars: every starred thread on the first poll, then the stars
added since (Gmail history). A Google Task linked to the email (Shift-T) is
optional: its notes become the instruction, and its message gets a star if the
thread has none. A new star or task counts only as it stands config.settle later
(e.g. "30s"; the poll waits that long and reads again), so that cycling through
the stars, or moving a task to another list, opens nothing on the way; a poll
with "now" takes them at once. Events are new replies, or a task added to a thread that
already has its dossier. Transitions move the star: yellow (open), purple (waiting),
none plus the "reviewed" label and the task checked (done).

Protocol 1: `gmail.py describe|poll|transition`, JSON on stdin and stdout.
The gws profile comes from GOOGLE_WORKSPACE_CLI_CONFIG_DIR.
"""
import base64
import hashlib
import html
import json
import os
import re
import subprocess
import sys
import time
import tempfile
from datetime import datetime, timezone

STARRED, PURPLE, YELLOW = "STARRED", "PURPLE_CIRCLE", "YELLOW_STAR"
MAX_ATTACHMENT = 20 * 1024 * 1024


class Fail(Exception):
    pass


# Every `fields` mask keeps one scalar the API always returns (historyId,
# resultSizeEstimate, etag): gws prints nothing for an empty response, and an
# empty list must stay distinguishable from a silent failure.
def gws(*args, params=None, body=None):
    cmd = ["gws", *args]
    if params is not None:
        cmd += ["--params", json.dumps(params)]
    if body is not None:
        cmd += ["--json", json.dumps(body)]
    p = subprocess.run(cmd, capture_output=True, text=True)
    out = p.stdout
    start = out.find("{")
    if start < 0:
        raise Fail(f"gws {' '.join(args)} returned no JSON: {(p.stderr or out).strip()[:400]}")
    data = json.loads(out[start:])
    if isinstance(data, dict) and "error" in data:
        err = data["error"]
        raise Fail(f"gws {' '.join(args)}: {err.get('message', err) if isinstance(err, dict) else err}")
    return data


def now_rfc3339():
    return datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.000Z")


def ms_of(rfc3339):
    return int(datetime.fromisoformat(rfc3339.replace("Z", "+00:00")).timestamp() * 1000)


def header(msg, name):
    for h in msg.get("payload", {}).get("headers", []):
        if h["name"].lower() == name.lower():
            return h["value"]
    return ""


def b64(data):
    return base64.urlsafe_b64decode(data + "=" * (-len(data) % 4))


def body_text(part):
    """Best text body of a message part: text/plain first, then stripped HTML."""
    plain, rich = [], []

    def walk(p):
        mt = p.get("mimeType", "")
        data = p.get("body", {}).get("data")
        if data and not p.get("filename"):
            text = b64(data).decode("utf-8", "replace")
            (plain if mt == "text/plain" else rich if mt == "text/html" else []).append(text)
        for c in p.get("parts", []) or []:
            walk(c)

    walk(part)
    if plain:
        return "\n".join(plain).strip()
    if rich:
        t = re.sub(r"(?is)<(script|style).*?</\1>", "", "\n".join(rich))
        t = re.sub(r"(?i)<br\s*/?>|</p>|</div>", "\n", t)
        t = html.unescape(re.sub(r"<[^>]+>", "", t))
        return re.sub(r"\n{3,}", "\n\n", t).strip()
    return ""


def attachments(part):
    out = []

    def walk(p):
        if p.get("filename") and p.get("body", {}).get("attachmentId"):
            out.append(p)
        for c in p.get("parts", []) or []:
            walk(c)

    walk(part)
    return out


def frontmatter(fields):
    lines = ["---"]
    for k, v in fields.items():
        lines.append(f"{k}: {json.dumps(v, ensure_ascii=False)}")
    lines.append("---")
    return "\n".join(lines) + "\n"


def message_url(mid):
    return f"https://mail.google.com/mail/#all/{mid}"


def render_messages(thread_id, subject, messages, kind="Email Thread"):
    first_from = header(messages[0], "From") if messages else ""
    last_date = header(messages[-1], "Date") if messages else ""
    fm = frontmatter({
        "type": kind,
        "title": subject,
        "description": f"{len(messages)} message(s), last from {header(messages[-1], 'From')}" if messages else "",
        "sources": [{"resource": message_url(messages[0]["id"]) if messages else "", "id": f"gmail:thread/{thread_id}",
                     "author": first_from, "last_modified": last_date}],
        "generated": {"by": "process:office-source-gmail", "at": now_rfc3339()},
    })
    parts = [fm, f"# {subject}\n"]
    for m in messages:
        parts.append(f"\n## {header(m, 'Date')} · {header(m, 'From')}\n")
        to = header(m, "To")
        if to:
            parts.append(f"To: {to}\n")
        cc = header(m, "Cc")
        if cc:
            parts.append(f"Cc: {cc}\n")
        names = [a["filename"] for a in attachments(m.get("payload", {}))]
        if names:
            parts.append("Attachments: " + ", ".join(names) + "\n")
        parts.append("\n" + body_text(m.get("payload", {})) + "\n")
    return "".join(parts)


def attachment_files(messages, tmpdir):
    files = []
    for m in messages:
        for a in attachments(m.get("payload", {})):
            size = a.get("body", {}).get("size", 0)
            name = re.sub(r"[^\w.\-]+", "-", a["filename"])
            if size > MAX_ATTACHMENT:
                continue
            data = gws("gmail", "users", "messages", "attachments", "get",
                       params={"userId": "me", "messageId": m["id"], "id": a["body"]["attachmentId"]})
            raw = b64(data.get("data", ""))
            path = os.path.join(tmpdir, name)
            with open(path, "wb") as f:
                f.write(raw)
            companion = frontmatter({
                "type": "Attachment",
                "title": a["filename"],
                "description": f"Attachment of the message of {header(m, 'Date')} from {header(m, 'From')}.",
                "resource": f"./{name}",
                "checksum": "sha256:" + hashlib.sha256(raw).hexdigest(),
                "size": len(raw),
                "sources": [{"resource": message_url(m["id"]), "author": header(m, "From"), "last_modified": header(m, "Date")}],
                "generated": {"by": "process:office-source-gmail", "at": now_rfc3339()},
            })
            files.append({"name": name, "path": path})
            files.append({"name": name + ".md", "content": companion})
    return files


OWN = {"SENT", "DRAFT", "SCHEDULED"}


def is_reply(m, me):
    """A message someone else wrote: not a draft, a scheduled or sent message, nor one from this account."""
    if OWN & set(m.get("labelIds") or []):
        return False
    return not (me and me in header(m, "From").lower())


def thread(tid, fmt="full"):
    return gws("gmail", "users", "threads", "get", params={"userId": "me", "id": tid, "format": fmt})


def task_message_id(task):
    for link in task.get("links", []) or []:
        if link.get("type") == "email":
            return link["link"].rstrip("/").rsplit("/", 1)[-1], link["link"]
    return None, None


def tasklists(cfg):
    """{tasklist id: alias of the dossier that includes its tasks, or ""} from config.tasklists ({title: alias}) or config.tasklist."""
    wanted = cfg.get("tasklists") or {cfg.get("tasklist", "@default"): ""}
    titles = {}
    if any(k != "@default" for k in wanted):
        page = gws("tasks", "tasklists", "list", params={"maxResults": 100, "fields": "items(id,title),etag"})
        titles = {l["title"]: l["id"] for l in page.get("items", [])}
    out = {}
    for title, holder in wanted.items():
        if title == "@default":
            out["@default"] = holder or ""
        elif title in titles:
            out[titles[title]] = holder or ""
        else:
            raise Fail(f"no Google Tasks list titled {title!r} on this account; lists: {', '.join(sorted(titles))}")
    return out


def list_tasks(tasklist, updated_min="", completed=False):
    params = {"tasklist": tasklist, "showCompleted": completed, "showHidden": completed, "maxResults": 100,
              "fields": "items(id,title,notes,links,updated,status),nextPageToken,etag"}
    if updated_min:
        params["updatedMin"] = updated_min
    items = []
    while True:
        page = gws("tasks", "tasks", "list", params=params)
        items += page.get("items", []) or []
        if not page.get("nextPageToken"):
            return items
        params["pageToken"] = page["nextPageToken"]


def modify(mid, add=(), remove=()):
    body = {}
    if add:
        body["addLabelIds"] = list(add)
    if remove:
        body["removeLabelIds"] = list(remove)
    if body:
        gws("gmail", "users", "messages", "modify", params={"userId": "me", "id": mid}, body=body)


def thread_labels(tid):
    t = gws("gmail", "users", "threads", "get",
            params={"userId": "me", "id": tid, "format": "minimal", "fields": "messages(id,labelIds)"})
    return [(m["id"], set(m.get("labelIds") or [])) for m in t.get("messages", [])]


def yellow(labels):
    return STARRED in labels and YELLOW in labels


def starred_threads():
    """Every thread holding a yellow-starred message, for the first poll."""
    params = {"userId": "me", "labelIds": [STARRED, YELLOW], "maxResults": 500, "fields": "messages(id,threadId),nextPageToken,resultSizeEstimate"}
    out = {}
    while True:
        page = gws("gmail", "users", "messages", "list", params=params)
        for m in page.get("messages", []) or []:
            out.setdefault(m["threadId"], m["id"])
        if not page.get("nextPageToken"):
            return out
        params["pageToken"] = page["nextPageToken"]


def starred_since(history_id):
    """Threads where a yellow star was added since history_id; None when it is too old."""
    params = {"userId": "me", "startHistoryId": history_id, "historyTypes": ["labelAdded"], "labelId": YELLOW,
              "maxResults": 500, "fields": "history(labelsAdded(message(id,threadId),labelIds)),nextPageToken,historyId"}
    out = {}
    while True:
        try:
            page = gws("gmail", "users", "history", "list", params=params)
        except Fail as e:
            if "404" in str(e) or "notFound" in str(e) or "Requested entity was not found" in str(e):
                return None
            raise
        for h in page.get("history", []) or []:
            for la in h.get("labelsAdded", []) or []:
                if YELLOW in (la.get("labelIds") or []):
                    m = la["message"]
                    out.setdefault(m["threadId"], m["id"])
        if not page.get("nextPageToken"):
            return out
        params["pageToken"] = page["nextPageToken"]


def seconds(duration):
    """'90s', '1m', '2h' or a number of seconds; 0 when empty."""
    d = str(duration or "").strip().lower()
    if not d:
        return 0
    unit = {"s": 1, "m": 60, "h": 3600}.get(d[-1])
    return int(float(d[:-1]) * unit) if unit else int(float(d))


def parse_cursor(c):
    if not c:
        return {}
    try:
        v = json.loads(c)
        return v if isinstance(v, dict) else {}
    except ValueError:
        return {}


def read_tasks(lists, cur):
    """Open Shift-T tasks by thread: their note, title, message and the dossiers their list names."""
    out = {"instructions": {}, "titles": {}, "threads": {}, "holders": {}}
    known = set(cur.get("lists") or [])
    for tasklist, holder in lists.items():
        # A list read for the first time is read whole, whatever the cursor says.
        since = cur.get("at", "") if (tasklist in known or not cur.get("lists") and tasklist == "@default") else ""
        for task in list_tasks(tasklist, since):
            if task.get("status") != "needsAction":
                continue
            mid, _ = task_message_id(task)
            if not mid:
                continue
            tid = gws("gmail", "users", "messages", "get",
                      params={"userId": "me", "id": mid, "format": "minimal", "fields": "threadId"})["threadId"]
            out["threads"][tid] = mid
            out["titles"].setdefault(tid, task.get("title") or "")
            if holder:
                out["holders"].setdefault(tid, set()).add(holder)
            if (task.get("notes") or "").strip():
                out["instructions"][tid] = task["notes"].strip()
    return out


def poll(inp):
    cfg = inp.get("config") or {}
    lists = tasklists(cfg)
    dry = bool(inp.get("dry_run"))
    cur = parse_cursor(inp.get("cursor"))
    watch = set(inp.get("watch") or [])
    started = now_rfc3339()
    profile = gws("gmail", "users", "getProfile", params={"userId": "me", "fields": "historyId,emailAddress"})
    history_now, me = profile["historyId"], profile.get("emailAddress", "").lower()
    tmpdir = tempfile.mkdtemp(prefix="office-gmail-")

    settle = 0 if inp.get("now") else seconds(cfg.get("settle"))

    # 1. Shift-T tasks: their notes are instructions, their list may name a
    # dossier that includes it.
    tasks = read_tasks(lists, cur)

    # 2. Yellow stars: every starred thread on the first poll, new stars afterwards.
    candidates = None
    if cur.get("history"):
        candidates = starred_since(cur["history"])
    if candidates is None:
        candidates = starred_threads()

    # A new star or task must still be in place after the settle delay: going
    # through the yellow star, or moving a task to another list, opens nothing
    # on the way. The tasks are read again after the wait.
    fresh = tasks["threads"] or any(f"gmail:thread/{tid}" not in watch for tid in candidates)
    if settle and fresh:
        time.sleep(settle)
        tasks = read_tasks(lists, cur)
    instructions, task_titles, task_threads, holders = tasks["instructions"], tasks["titles"], tasks["threads"], tasks["holders"]
    for tid, mid in task_threads.items():
        candidates.setdefault(tid, mid)
        # A task without a star gets one: the star is the signal.
        if not dry and not any(STARRED in labels for _, labels in thread_labels(tid)):
            modify(mid, add=[STARRED, YELLOW])

    signals, events, starred = [], [], []
    for tid, mid in candidates.items():
        ref = f"gmail:thread/{tid}"
        if ref in watch:
            if tid in instructions or tid in holders:
                summary = {"instruction": instructions[tid]} if tid in instructions else {}
                if tid in holders:
                    summary["in"] = ", ".join(sorted(holders[tid]))
                events.append({"thread_ref": ref, "kind": "instruction", "summary": summary,
                               "files": [], "at": started, "in": sorted(holders.get(tid, ()))})
            continue
        if tid in task_threads or any(yellow(l) for _, l in thread_labels(tid)):
            starred.append((tid, mid))

    for tid, mid in starred:
        ref = f"gmail:thread/{tid}"
        t = thread(tid)
        msgs = t.get("messages", [])
        if not msgs:
            continue
        subject = header(msgs[0], "Subject")
        atts = [n for m in msgs for n in (a["filename"] for a in attachments(m.get("payload", {})))]
        signals.append({
            "source_ref": ref,
            "thread_ref": ref,
            "title": task_titles.get(tid) or subject or "(no subject)",
            "instruction": instructions.get(tid, ""),
            "summary": {
                "from": header(msgs[-1], "From"),
                "date": header(msgs[-1], "Date"),
                "messages": len(msgs),
                **({"attachments": ", ".join(atts)} if atts else {}),
            },
            "files": [{"name": "thread.md", "content": render_messages(tid, subject, msgs)}, *attachment_files(msgs, tmpdir)],
            "url": message_url(mid),
            "at": started,
            "in": sorted(holders.get(tid, ())),
        })

    # 3. Replies on the threads dossier watches.
    if cur.get("at"):
        since, until = ms_of(cur["at"]), ms_of(started)
        seen = {s["thread_ref"] for s in signals}
        for ref in watch:
            if ref in seen or not ref.startswith("gmail:thread/"):
                continue
            tid = ref.split("/", 1)[1]
            new = [m for m in thread(tid).get("messages", [])
                   if since < int(m.get("internalDate", 0)) <= until and is_reply(m, me)]
            if not new:
                continue
            subject = header(new[-1], "Subject")
            events.append({
                "thread_ref": ref,
                "kind": "reply",
                "summary": {"from": header(new[-1], "From"), "date": header(new[-1], "Date"), "messages": len(new)},
                "files": [{"name": "reply.md", "content": render_messages(tid, subject, new, kind="Email Message")},
                          *attachment_files(new, tmpdir)],
                "at": header(new[-1], "Date"),
            })

    return {"signals": signals, "events": events,
            "cursor": json.dumps({"history": history_now, "at": started, "lists": sorted(lists)})}


_reviewed = {}


def reviewed_label():
    if "id" not in _reviewed:
        labels = gws("gmail", "users", "labels", "list", params={"userId": "me", "fields": "labels(id,name)"})
        ids = [l["id"] for l in labels.get("labels", []) if l["name"] == "reviewed"]
        if not ids:
            raise Fail("no Gmail label named 'reviewed' on this account; create it in Gmail first")
        _reviewed["id"] = ids[0]
    return _reviewed["id"]


def thread_tasks(lists, message_ids, completed):
    """(tasklist, task) for every task of the configured lists linked to the thread."""
    out = []
    for tasklist in lists:
        for task in list_tasks(tasklist, completed=completed):
            mid, _ = task_message_id(task)
            if mid in message_ids:
                out.append((tasklist, task))
    return out


def transition(inp):
    cfg = inp.get("config") or {}
    lists = tasklists(cfg)
    ref, to, frm = inp["source_ref"], inp["to"], inp["from"]
    if not ref.startswith("gmail:thread/"):
        raise Fail(f"not a Gmail thread reference: {ref}")
    tid = ref.split("/", 1)[1]
    msgs = thread_labels(tid)
    if not msgs:
        raise Fail(f"thread {tid} has no message")
    last = msgs[-1][0]
    starred = [mid for mid, l in msgs if STARRED in l]
    done = []

    if to == "waiting":
        for mid in starred or [last]:
            modify(mid, add=[STARRED, PURPLE], remove=[YELLOW])
        done.append("purple star")
    elif frm == "waiting" and to == "open":
        for mid in starred or [last]:
            modify(mid, add=[STARRED, YELLOW], remove=[PURPLE])
        done.append("yellow star")
    elif to == "done":
        for mid, l in msgs:
            if l & {STARRED, YELLOW, PURPLE}:
                modify(mid, remove=[STARRED, YELLOW, PURPLE])
        gws("gmail", "users", "threads", "modify", params={"userId": "me", "id": tid}, body={"addLabelIds": [reviewed_label()]})
        for tasklist, task in thread_tasks(lists, {m for m, _ in msgs}, completed=False):
            if task.get("status") != "completed":
                gws("tasks", "tasks", "patch", params={"tasklist": tasklist, "task": task["id"]}, body={"status": "completed"})
                done.append("task checked")
        done += ["stars removed", "label reviewed"]
    elif frm == "done" and to == "open":
        modify(last, add=[STARRED, YELLOW])
        for tasklist, task in thread_tasks(lists, {m for m, _ in msgs}, completed=True):
            if task.get("status") == "completed":
                gws("tasks", "tasks", "patch", params={"tasklist": tasklist, "task": task["id"]},
                    body={"status": "needsAction", "completed": None})
                done.append("task unchecked")
        done.append("yellow star")
    return {"ok": True, "detail": ", ".join(done) or "nothing to do"}


def main():
    verb = sys.argv[1] if len(sys.argv) > 1 else ""
    try:
        inp = json.load(sys.stdin) if verb != "describe" else {}
        if verb == "describe":
            out = {"name": "gmail", "protocol": 1, "verbs": ["describe", "poll", "transition"]}
        elif verb == "poll":
            out = poll(inp)
        elif verb == "transition":
            out = transition(inp)
        else:
            raise Fail(f"unknown verb {verb!r}; expected describe, poll or transition")
    except Fail as e:
        json.dump({"error": {"message": str(e)}}, sys.stdout)
        sys.exit(1)
    json.dump(out, sys.stdout, ensure_ascii=False)


if __name__ == "__main__":
    main()
