#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.11"
# ///
"""office source connector for a private Telegram bot, one bot per office.

A message to the bot is a signal: its first line is the dossier title, the
whole text the instruction ("D-42: ..." goes to that dossier). A voice message
is transcribed on this Mac; photos and documents join the dossier's context. A
reply to a message of the conversation reaches the same dossier as an event.
The bot answers which dossier a message reached, and tells when it waits, is
resumed, closed or reopened. Bot commands (/start) are ignored, and the first
poll only marks what the bot already received as read.

Protocol 1: `telegram.py describe|poll|transition|opened`, JSON on stdin and stdout.
config:
  token_service  keychain item holding the bot token (security add-generic-password -s <it> -w)
  allowed_users  Telegram user ids allowed to write to the bot; a poll without them lists who wrote
  lang           "en" (default) or "fr", for the bot's answers
  stt_command    transcriber, default mlx_audio.stt.generate (mlx-audio)
  stt_model      default mlx-community/whisper-large-v3-turbo
"""
import json
import os
import subprocess
import sys
import tempfile
import urllib.error
import urllib.parse
import urllib.request
from datetime import datetime, timezone

API = "https://api.telegram.org"


class Fail(Exception):
    pass


def now():
    return datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def token(cfg):
    service = cfg.get("token_service")
    if not service:
        raise Fail("config.token_service is missing: the keychain item that holds the bot token")
    p = subprocess.run(["security", "find-generic-password", "-a", os.environ.get("USER", ""), "-s", service, "-w"],
                       capture_output=True, text=True)
    if p.returncode != 0 or not p.stdout.strip():
        raise Fail(f"no bot token in the keychain item {service!r}; add it with "
                   f"`security add-generic-password -a \"$USER\" -s {service} -w`")
    return p.stdout.strip()


def call(tok, method, **params):
    data = urllib.parse.urlencode({k: json.dumps(v) if isinstance(v, (list, dict)) else v
                                   for k, v in params.items() if v is not None}).encode()
    try:
        with urllib.request.urlopen(f"{API}/bot{tok}/{method}", data=data, timeout=60) as r:
            out = json.load(r)
    except urllib.error.HTTPError as e:
        out = json.load(e)
    except OSError as e:
        raise Fail(f"Telegram {method}: {e}")
    if not out.get("ok"):
        raise Fail(f"Telegram {method}: {out.get('description', out)}")
    return out["result"]


def download(tok, file_id, dest):
    path = call(tok, "getFile", file_id=file_id)["file_path"]
    with urllib.request.urlopen(f"{API}/file/bot{tok}/{path}", timeout=120) as r, open(dest, "wb") as f:
        f.write(r.read())
    return dest


def transcribe(cfg, audio):
    base = audio + ".transcript"
    cmd = [cfg.get("stt_command") or "mlx_audio.stt.generate",
           "--model", cfg.get("stt_model") or "mlx-community/whisper-large-v3-turbo",
           "--audio", audio, "--output", base, "--format", "txt"]
    try:
        p = subprocess.run(cmd, capture_output=True, text=True, timeout=600)
    except (OSError, subprocess.TimeoutExpired) as e:
        return f"(transcription failed: {e})"
    try:
        with open(base + ".txt") as f:
            return f.read().strip()
    except OSError:
        return f"(transcription failed: {p.stderr.strip()[-300:]})"


# The conversation map lives beside the cursor: transition and opened, which
# have no cursor, add the bot's own messages to it.
def state_path(cfg):
    base = os.path.expanduser("~/Library/Application Support/dossier/telegram")
    os.makedirs(base, exist_ok=True)
    return os.path.join(base, (cfg.get("token_service") or "bot") + ".json")


def load_state(cfg):
    try:
        with open(state_path(cfg)) as f:
            return json.load(f)
    except (OSError, ValueError):
        return {"roots": {}}


def save_state(cfg, st):
    roots = st.get("roots", {})
    if len(roots) > 2000:
        st["roots"] = dict(list(roots.items())[-2000:])
    tmp = state_path(cfg) + ".tmp"
    with open(tmp, "w") as f:
        json.dump(st, f)
    os.replace(tmp, state_path(cfg))


def refs(chat, root):
    return f"telegram:message/{chat}/{root}", f"telegram:chat/{chat}/{root}"


def content(tok, cfg, msg, tmpdir):
    """The text of a message, voice transcribed, and its files."""
    text = (msg.get("text") or msg.get("caption") or "").strip()
    files, kind = [], "message"
    mid = msg["message_id"]
    for key in ("voice", "audio", "video_note"):
        if key in msg:
            kind = "voice"
            ext = ".ogg" if key == "voice" else ".mp4" if key == "video_note" else ".audio"
            audio = download(tok, msg[key]["file_id"], os.path.join(tmpdir, f"{mid}-{key}{ext}"))
            spoken = transcribe(cfg, audio)
            text = (text + "\n\n" + spoken).strip() if text else spoken
            files.append({"name": f"{key}{ext}", "path": audio})
    if "photo" in msg:
        kind = "photo" if kind == "message" else kind
        best = max(msg["photo"], key=lambda p: p.get("file_size", 0))
        files.append({"name": "photo.jpg", "path": download(tok, best["file_id"], os.path.join(tmpdir, f"{mid}-photo.jpg"))})
    if "document" in msg:
        doc = msg["document"]
        name = doc.get("file_name") or "document"
        files.append({"name": name, "path": download(tok, doc["file_id"], os.path.join(tmpdir, f"{mid}-{name}"))})
    return text, files, kind


def poll(inp):
    cfg = inp.get("config") or {}
    tok = token(cfg)
    allowed = {int(u) for u in cfg.get("allowed_users") or []}
    cur = json.loads(inp.get("cursor") or "{}") if (inp.get("cursor") or "").startswith("{") else {}
    offset = cur.get("offset")
    updates = call(tok, "getUpdates", offset=offset, timeout=0, allowed_updates=["message"])
    if not allowed:
        seen = {(u["message"]["from"]["id"], u["message"]["from"].get("first_name", "")) for u in updates if "message" in u}
        raise Fail("config.allowed_users is empty. Messages came from: " +
                   (", ".join(f"{i} ({n})" for i, n in sorted(seen)) or "nobody yet; write to the bot first"))
    if offset is None:
        # The first poll starts from now: what the bot received before is not a signal.
        last = max((u["update_id"] for u in updates), default=None)
        return {"signals": [], "events": [], "cursor": json.dumps({"offset": last + 1 if last is not None else 0})}
    st = load_state(cfg)
    roots = st.setdefault("roots", {})
    tmpdir = tempfile.mkdtemp(prefix="office-telegram-")
    signals, events = [], []
    for u in updates:
        offset = u["update_id"] + 1
        msg = u.get("message")
        if not msg or msg.get("from", {}).get("id") not in allowed:
            continue
        if (msg.get("text") or "").startswith("/"):
            continue
        chat, mid = msg["chat"]["id"], msg["message_id"]
        text, files, kind = content(tok, cfg, msg, tmpdir)
        reply = (msg.get("reply_to_message") or {}).get("message_id")
        root = roots.get(f"{chat}/{reply}") if reply else None
        md = f"# {kind} · {datetime.fromtimestamp(msg['date'], timezone.utc).isoformat()}\n\n{text}\n"
        if root:
            roots[f"{chat}/{mid}"] = root
            _, thread = refs(chat, root)
            events.append({"thread_ref": thread, "kind": kind, "summary": {"kind": kind, "text": text[:200]},
                           "files": [{"name": "message.md", "content": md}, *files], "at": now()})
            continue
        roots[f"{chat}/{mid}"] = mid
        source, thread = refs(chat, mid)
        first = text.splitlines()[0].strip() if text else ""
        title = (first[:77] + "…") if len(first) > 78 else first
        signals.append({
            "source_ref": source, "thread_ref": thread,
            "title": title or {"voice": "Voice message", "photo": "Photo"}.get(kind, "Telegram message"),
            "instruction": text,
            "summary": {"kind": kind, "from": msg["from"].get("first_name", "")},
            "files": [{"name": "message.md", "content": md}, *files],
            "url": "", "at": now(),
        })
    if not inp.get("dry_run"):
        save_state(cfg, st)
    return {"signals": signals, "events": events, "cursor": json.dumps({"offset": offset})}


WORDS = {
    "en": {"created": "Dossier {id} opened: {title}", "reopened": "Dossier {id} reopened: {title}",
           "routed": "Added to {id}: {title}", "event": "Added to {id}: {title}",
           "waiting": "{id} waits on {on}", "resumed": "{id} resumed", "done": "{id} closed", "reopen": "{id} reopened",
           "deleted": "{id} deleted"},
    "fr": {"created": "Dossier {id} ouvert : {title}", "reopened": "Dossier {id} rouvert : {title}",
           "routed": "Ajouté à {id} : {title}", "event": "Ajouté à {id} : {title}",
           "waiting": "{id} en attente de {on}", "resumed": "{id} repris", "done": "{id} clos", "reopen": "{id} rouvert",
           "deleted": "{id} supprimé"},
}


def say(inp, key, extra=""):
    cfg = inp.get("config") or {}
    words = WORDS.get(cfg.get("lang") or "en", WORDS["en"])
    ref = inp.get("source_ref") or inp.get("thread_ref") or ""
    if not ref.startswith("telegram:"):
        raise Fail(f"not a Telegram reference: {ref}")
    chat, root = ref.split("/", 1)[1].split("/")
    d = inp.get("dossier") or {}
    text = words[key].format(id=d.get("id", "?"), title=d.get("title", ""), on=d.get("waiting_on") or "?")
    if extra:
        text += " · " + extra
    tok = token(cfg)
    sent = call(tok, "sendMessage", chat_id=chat, text=text, reply_to_message_id=root, allow_sending_without_reply=True)
    st = load_state(cfg)
    st.setdefault("roots", {})[f"{chat}/{sent['message_id']}"] = int(root)
    save_state(cfg, st)


def opened(inp):
    if inp.get("outcome") in WORDS["en"]:
        say(inp, inp["outcome"])
    return {"ok": True}


def transition(inp):
    frm, to = inp.get("from"), inp.get("to")
    note = (inp.get("note") or "").strip()
    key = {"waiting": "waiting", "done": "done"}.get(to) or ("reopen" if frm == "done" else "resumed")
    if to == "done" and note.startswith("dossier deleted"):
        key, note = "deleted", note.removeprefix("dossier deleted").lstrip(" ·")
    say(inp, key, note)
    return {"ok": True, "detail": "told the chat"}


def main():
    verb = sys.argv[1] if len(sys.argv) > 1 else ""
    try:
        inp = json.load(sys.stdin) if verb in ("poll", "transition", "opened") else {}
        if verb == "describe":
            out = {"name": "telegram", "protocol": 1, "verbs": ["describe", "poll", "transition", "opened"]}
        elif verb == "poll":
            out = poll(inp)
        elif verb == "transition":
            out = transition(inp)
        elif verb == "opened":
            out = opened(inp)
        else:
            raise Fail(f"unknown verb {verb!r}; expected describe, poll, transition or opened")
    except Fail as e:
        json.dump({"error": {"message": str(e)}}, sys.stdout)
        sys.exit(1)
    json.dump(out, sys.stdout, ensure_ascii=False)


if __name__ == "__main__":
    main()
