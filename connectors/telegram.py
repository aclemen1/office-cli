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

Protocol 1: `telegram.py describe|poll|transition|opened|send|wait|progress|serve`, JSON on
stdin and stdout; `serve` answers the other verbs, one JSON line each, for office listen.
`send` writes a text (Markdown, split under 4096 characters) and files to the
allowed_users only; a reply in its thread comes back as an event. `wait` long-polls
the bot without consuming, for `office listen`.
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


_TOKENS = {}


def token(cfg):
    service = cfg.get("token_service")
    if service in _TOKENS:
        return _TOKENS[service]
    if not service:
        raise Fail("config.token_service is missing: the keychain item that holds the bot token")
    p = subprocess.run(["security", "find-generic-password", "-a", os.environ.get("USER", ""), "-s", service, "-w"],
                       capture_output=True, text=True)
    if p.returncode != 0 or not p.stdout.strip():
        raise Fail(f"no bot token in the keychain item {service!r}; add it with "
                   f"`security add-generic-password -a \"$USER\" -s {service} -w`")
    _TOKENS[service] = p.stdout.strip()
    return _TOKENS[service]


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


AUDIO_EXT = {"audio/mp4": ".m4a", "audio/x-m4a": ".m4a", "audio/m4a": ".m4a", "audio/aac": ".aac",
             "audio/mpeg": ".mp3", "audio/ogg": ".ogg", "audio/opus": ".ogg", "audio/wav": ".wav",
             "audio/x-wav": ".wav", "audio/flac": ".flac", "audio/webm": ".webm"}


def audio_ext(item, default):
    name = item.get("file_name") or ""
    if "." in name:
        return "." + name.rsplit(".", 1)[1].lower()
    return AUDIO_EXT.get(item.get("mime_type") or "", default)


def as_wav(audio):
    """A 16 kHz mono WAV of any audio ffmpeg reads; the transcriber's reader
    (libsndfile) knows neither M4A nor AAC. Without ffmpeg, the file as is."""
    wav = audio + ".wav"
    try:
        p = subprocess.run(["ffmpeg", "-y", "-loglevel", "error", "-i", audio, "-ac", "1", "-ar", "16000", wav],
                           capture_output=True, text=True, timeout=300)
    except (OSError, subprocess.TimeoutExpired):
        return audio
    return wav if p.returncode == 0 and os.path.exists(wav) else audio


def transcribe(cfg, audio):
    base = audio + ".transcript"
    cmd = [cfg.get("stt_command") or "mlx_audio.stt.generate",
           "--model", cfg.get("stt_model") or "mlx-community/whisper-large-v3-turbo",
           "--audio", as_wav(audio), "--output", base, "--format", "txt"]
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
            ext = ".ogg" if key == "voice" else ".mp4" if key == "video_note" else audio_ext(msg[key], ".m4a")
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
            events.append({"thread_ref": thread, "kind": kind,
                           "summary": {"kind": kind, "text": text[:200], "message_ref": refs(chat, mid)[0]},
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


LIMIT = 4000  # under Telegram's 4096, room for Markdown escapes


def chunks(text):
    """Split text under LIMIT: by paragraph, then by line, then hard."""
    out, cur = [], ""
    for para in text.split("\n\n"):
        pieces = [para]
        if len(para) > LIMIT:
            pieces, line_buf = [], ""
            for line in para.split("\n"):
                while len(line) > LIMIT:
                    pieces.append(line[:LIMIT])
                    line = line[LIMIT:]
                if line_buf and len(line_buf) + 1 + len(line) > LIMIT:
                    pieces.append(line_buf)
                    line_buf = line
                else:
                    line_buf = f"{line_buf}\n{line}" if line_buf else line
            if line_buf:
                pieces.append(line_buf)
        for p in pieces:
            if cur and len(cur) + 2 + len(p) > LIMIT:
                out.append(cur)
                cur = p
            else:
                cur = f"{cur}\n\n{p}" if cur else p
    if cur:
        out.append(cur)
    return out


def send_text(tok, chat, text, reply_to=None):
    try:
        return call(tok, "sendMessage", chat_id=chat, text=text, parse_mode="Markdown",
                    reply_to_message_id=reply_to, allow_sending_without_reply=True)
    except Fail as e:
        if "parse" not in str(e).lower() and "entit" not in str(e).lower():
            raise
        return call(tok, "sendMessage", chat_id=chat, text=text,
                    reply_to_message_id=reply_to, allow_sending_without_reply=True)


def send_file(tok, chat, path, name, reply_to=None):
    boundary = "office" + os.urandom(8).hex()
    with open(path, "rb") as f:
        data = f.read()
    fields = {"chat_id": str(chat)}
    if reply_to:
        fields["reply_to_message_id"] = str(reply_to)
        fields["allow_sending_without_reply"] = "true"
    body = b""
    for k, v in fields.items():
        body += f"--{boundary}\r\nContent-Disposition: form-data; name=\"{k}\"\r\n\r\n{v}\r\n".encode()
    body += (f"--{boundary}\r\nContent-Disposition: form-data; name=\"document\"; filename=\"{name}\"\r\n"
             f"Content-Type: application/octet-stream\r\n\r\n").encode() + data + f"\r\n--{boundary}--\r\n".encode()
    req = urllib.request.Request(f"{API}/bot{tok}/sendDocument", data=body,
                                 headers={"Content-Type": f"multipart/form-data; boundary={boundary}"})
    try:
        with urllib.request.urlopen(req, timeout=120) as r:
            out = json.load(r)
    except urllib.error.HTTPError as e:
        out = json.load(e)
    except OSError as e:
        raise Fail(f"Telegram sendDocument: {e}")
    if not out.get("ok"):
        raise Fail(f"Telegram sendDocument: {out.get('description', out)}")
    return out["result"]


def send(inp):
    """Send a text (Markdown) and files to the user: always the allowed_users,
    never anyone else. Every message sent belongs to one thread, so that a
    reply comes back as an event of the sender."""
    cfg = inp.get("config") or {}
    users = [int(u) for u in cfg.get("allowed_users") or []]
    if not users:
        raise Fail("config.allowed_users is empty: nobody to send to")
    text = (inp.get("text") or "").strip()
    files = inp.get("files") or []
    if not text and not files:
        raise Fail("nothing to send")
    tok = token(cfg)
    st = load_state(cfg)
    roots = st.setdefault("roots", {})
    # A placeholder of `progress` (one per chat) becomes the first part of the answer.
    replace = {}
    for r in inp.get("replace") or []:
        c, p = r.split("/", 1)[1].split("/")
        replace[int(c)] = int(p)
    threads, sent = [], 0
    for chat in users:
        root, parts = None, chunks(text)
        if chat in replace and parts:
            pid = replace[chat]
            try:
                edit_text(tok, chat, pid, parts[0])
                root = roots.get(f"{chat}/{pid}") or pid
                parts = parts[1:]
                sent += 1
            except Fail:
                pass
        for part in parts:
            m = send_text(tok, chat, part, reply_to=root)
            root = root or m["message_id"]
            roots[f"{chat}/{m['message_id']}"] = root
            sent += 1
        for f in files:
            m = send_file(tok, chat, os.path.expanduser(f["path"]), f.get("name") or os.path.basename(f["path"]), reply_to=root)
            root = root or m["message_id"]
            roots[f"{chat}/{m['message_id']}"] = root
            sent += 1
        threads.append(refs(chat, root)[1])
    save_state(cfg, st)
    return {"ok": True, "thread_refs": threads, "sent": sent}


def edit_text(tok, chat, mid, text):
    """Replace a message's text, Markdown first, plain if Telegram refuses it.
    An unchanged text is no error."""
    for mode in ("Markdown", None):
        try:
            return call(tok, "editMessageText", chat_id=chat, message_id=mid, text=text, parse_mode=mode)
        except Fail as e:
            msg = str(e).lower()
            if "not modified" in msg:
                return None
            if mode and ("parse" in msg or "entit" in msg):
                continue
            raise


def progress(inp):
    """A placeholder that tells the user their message is being handled:
    start answers it with ⏳, update shows the agent's steps, end closes it
    (✓ by default). send with replace turns it into the answer."""
    cfg = inp.get("config") or {}
    tok = token(cfg)
    op = inp.get("op")
    if op == "start":
        ref = inp.get("ref") or ""
        if not ref.startswith("telegram:message/"):
            raise Fail(f"not a Telegram message: {ref}")
        chat, mid = ref.split("/", 1)[1].split("/")
        sent = call(tok, "sendMessage", chat_id=chat, text="⏳ …", reply_to_message_id=mid,
                    allow_sending_without_reply=True, disable_notification=True)
        st = load_state(cfg)
        roots = st.setdefault("roots", {})
        roots[f"{chat}/{sent['message_id']}"] = roots.get(f"{chat}/{mid}") or int(mid)
        save_state(cfg, st)
        return {"ok": True, "placeholder": refs(chat, sent["message_id"])[0]}
    ph = inp.get("placeholder") or ""
    if not ph.startswith("telegram:message/"):
        raise Fail(f"not a placeholder: {ph}")
    chat, pid = ph.split("/", 1)[1].split("/")
    if op == "update":
        text = (inp.get("text") or "").strip()
        edit_text(tok, chat, pid, ("⏳ " + text)[:LIMIT] if text else "⏳ …")
    elif op == "end":
        edit_text(tok, chat, pid, (inp.get("text") or "✓").strip()[:LIMIT])
    else:
        raise Fail(f"progress: op {op!r}; expected start, update or end")
    return {"ok": True, "placeholder": ph}


def wait(inp):
    """Long polling: block until the bot holds an update after the cursor, or
    until the timeout. getUpdates with the cursor's offset confirms only what
    poll already took, so nothing is consumed here."""
    cfg = inp.get("config") or {}
    tok = token(cfg)
    cur = json.loads(inp.get("cursor") or "{}") if (inp.get("cursor") or "").startswith("{") else {}
    offset = cur.get("offset")
    if offset is None:
        return {"ready": True}
    timeout = max(1, min(int(inp.get("timeout") or 50), 50))
    try:
        with urllib.request.urlopen(f"{API}/bot{tok}/getUpdates?" + urllib.parse.urlencode(
                {"offset": offset, "timeout": timeout, "limit": 1, "allowed_updates": json.dumps(["message"])}),
                timeout=timeout + 15) as r:
            out = json.load(r)
    except urllib.error.HTTPError as e:
        out = json.load(e)
    except OSError as e:
        raise Fail(f"Telegram getUpdates: {e}")
    if not out.get("ok"):
        raise Fail(f"Telegram getUpdates: {out.get('description', out)}")
    return {"ready": bool(out["result"])}


VERBS = {}  # filled below: verb → handler


def describe(_inp):
    return {"name": "telegram", "protocol": 1,
            "verbs": ["describe", "poll", "transition", "opened", "send", "wait", "progress", "serve"]}


def serve(_inp):
    """A lasting process for office listen: one JSON request per line on stdin,
    {"id", "verb", "input"}, one answer per line on stdout, {"id", "result"} or
    {"id", "error"}. Requests run side by side; only wait runs without the state
    lock, since it blocks for up to a minute."""
    import threading
    out_lock, state_lock = threading.Lock(), threading.Lock()

    def answer(msg):
        with out_lock:
            sys.stdout.write(json.dumps(msg, ensure_ascii=False) + "\n")
            sys.stdout.flush()

    def handle(req):
        rid, verb = req.get("id"), req.get("verb")
        fn = VERBS.get(verb)
        try:
            if fn is None or verb == "serve":
                raise Fail(f"unknown verb {verb!r}")
            if verb == "wait":
                result = fn(req.get("input") or {})
            else:
                with state_lock:
                    result = fn(req.get("input") or {})
            answer({"id": rid, "result": result})
        except Fail as e:
            answer({"id": rid, "error": {"message": str(e)}})
        except Exception as e:  # a bug must not stop the server
            answer({"id": rid, "error": {"message": f"{type(e).__name__}: {e}"}})

    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        try:
            req = json.loads(line)
        except ValueError:
            continue
        threading.Thread(target=handle, args=(req,), daemon=True).start()
    return {"ok": True}


def main():
    VERBS.update({"describe": describe, "poll": poll, "transition": transition, "opened": opened, "send": send,
                  "wait": wait, "progress": progress})
    verb = sys.argv[1] if len(sys.argv) > 1 else ""
    if verb == "serve":
        serve({})
        return
    try:
        inp = json.load(sys.stdin) if verb in ("poll", "transition", "opened", "send", "wait", "progress") else {}
        if verb == "describe":
            out = {"name": "telegram", "protocol": 1, "verbs": ["describe", "poll", "transition", "opened", "send", "wait", "progress", "serve"]}
        elif verb == "wait":
            out = wait(inp)
        elif verb == "progress":
            out = progress(inp)
        elif verb == "poll":
            out = poll(inp)
        elif verb == "transition":
            out = transition(inp)
        elif verb == "opened":
            out = opened(inp)
        elif verb == "send":
            out = send(inp)
        else:
            raise Fail(f"unknown verb {verb!r}; expected describe, poll, transition, opened, send, wait, progress or serve")
    except Fail as e:
        json.dump({"error": {"message": str(e)}}, sys.stdout)
        sys.exit(1)
    json.dump(out, sys.stdout, ensure_ascii=False)


if __name__ == "__main__":
    main()
