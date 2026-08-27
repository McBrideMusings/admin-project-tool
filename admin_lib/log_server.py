"""Tiny HTTP server that receives browser console-log batches and appends them
to the admin process's log file.

Started by admin_lib.log_bridge when a long-running action has [log_bridge]
configured. Stdlib only — http.server + threading.
"""
from __future__ import annotations

import json
import re
import socket
import sys
import threading
import datetime
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Iterable
from urllib.parse import urlparse

from .log_format import normalize_timestamp

_SEEN_CLIENTS: set[str] = set()

# Optional per-line output sink. When set, emitted client lines go through it
# instead of a raw stdout write — `admin logs` uses this to render bridged
# [CLIENT] lines through the same LineRenderer as the tailed stream. The
# log-file append is unaffected. None restores the default stdout behavior.
_emit_sink = None


def set_log_server_sink(fn):
    """Set (or clear, with None) the per-line output sink. See `_emit_sink`."""
    global _emit_sink
    _emit_sink = fn

_LOG_BRIDGE_STATE = {
    "log_path": None,
    "hosts": [],
    "project": "",
    "pid": 0,
    "started_at": "",
    "port": 0,
}


def set_log_server_state(*, log_path, hosts, project, pid, started_at, port):
    _LOG_BRIDGE_STATE["log_path"] = log_path
    _LOG_BRIDGE_STATE["hosts"] = list(hosts)
    _LOG_BRIDGE_STATE["project"] = project
    _LOG_BRIDGE_STATE["pid"] = pid
    _LOG_BRIDGE_STATE["started_at"] = started_at
    _LOG_BRIDGE_STATE["port"] = port


# Console levels → the level words admin's formatter knows how to color
# (admin_lib/log_format._LEVEL_COLORS). `console.log` is the browser's default
# level and carries no severity of its own — it maps to INFO so a bridged line
# and a server line at the same severity print the same word in the same color.
_CONSOLE_LEVELS = {
    "log": "INFO",
    "info": "INFO",
    "warn": "WARN",
    "warning": "WARN",
    "error": "ERROR",
    "debug": "DEBUG",
    "trace": "TRACE",
}


# An app's own logger prefix, printed *into* the console message: a wall clock,
# then its own level. reddit-poker's is built at apps/devvit/src/lib/logger.ts,
# and the shape is common enough to be worth unwrapping — every such line
# otherwise arrives double-stamped and double-levelled, with the browser's level
# wrong on top of it (a logger that routes debug through console.log reports as
# `log`). Anything after the level — `[Messaging]`, a client id — is the app's
# own scope and is kept.
_INNER_PREFIX_RE = re.compile(
    r"^\d{2}:\d{2}:\d{2}(?:[.,]\d+)?\s+"
    r"\[(ERROR|ERR|FATAL|CRIT|CRITICAL|WARN|WARNING|INFO|DEBUG|TRACE|LOG)\]\s*",
    re.IGNORECASE,
)


def unwrap_inner_prefix(msg: str) -> tuple[str | None, str]:
    """Split an app-logger prefix off a console message.

    Returns (level, remainder) — (None, msg) when the message has no such
    prefix. The stripped timestamp is discarded: the entry's own ISO timestamp
    is the same instant and carries a date, so nothing is lost.
    """
    m = _INNER_PREFIX_RE.match(msg)
    if not m:
        return None, msg
    return m.group(1).upper(), msg[m.end():]


def format_log_bridge_entry(entry: dict) -> str:
    """One bridged console entry as one or more log lines.

    Shape is `<iso-timestamp> [LEVEL] [CLIENT] <message>` — the same
    `[LEVEL] <scope> message` order a server process prints, so both streams
    render identically once admin's formatter strips the timestamp and
    colorizes the level. The full ISO stamp stays in the file because the file
    is the only place the entry's own time survives; `admin logs` and the live
    terminal both re-render it as local `HH:MM:SS`.

    A message carrying newlines (a stack trace) becomes one stamped line per
    physical line, indentation preserved, with the level and `[CLIENT]` tag on
    the head line only — how a server's multi-line output already arrives, one
    pipe read per line.
    """
    ts = entry.get("timestamp") or datetime.datetime.now(datetime.timezone.utc).isoformat()
    raw = (entry.get("level") or "log").lower()
    level = _CONSOLE_LEVELS.get(raw, raw.upper())
    msg = (entry.get("message") or "").rstrip("\n")
    inner, msg = unwrap_inner_prefix(msg)
    if inner:
        # The app's own level beats the browser's: a logger that sends debug
        # through console.log is reported by the browser as `log`.
        level = _CONSOLE_LEVELS.get(inner.lower(), inner)
    head, *rest = msg.split("\n")
    out = f"{ts} [{level}] [CLIENT] {head}\n"
    for cont in rest:
        out += f"{ts} {cont}\n"
    return out


def _in_time_order(entries: Iterable[dict]) -> list[dict]:
    """Sort one POSTed batch by the entries' own timestamps.

    The userscript buffers from document-start but only starts flushing once
    its listener probe resolves, so a frame's first POST is a backlog whose
    entries can predate lines another frame already delivered. Sorting the batch
    puts that backlog in order; a stable sort with the previous valid key
    carried forward leaves an entry with an unparseable timestamp exactly where
    the client put it.
    """
    out = list(entries)
    keys = []
    last = ""
    for e in out:
        ts = e.get("timestamp")
        parsed = normalize_timestamp(ts, "%Y-%m-%dT%H:%M:%S.%f") if ts else None
        if parsed is not None:
            last = parsed
        keys.append(last)
    return [e for _, e in sorted(zip(keys, out), key=lambda p: p[0])]


def _entry_host(entry: dict) -> str:
    """Host of a forwarded entry. New clients send `host`; older ones sent the
    full `url` (which carried auth tokens) — parse the host out of that."""
    host = entry.get("host")
    if host:
        return host
    try:
        return urlparse(entry.get("url") or "").hostname or ""
    except ValueError:
        return ""


def append_log_bridge_entries(entries: Iterable[dict], *, user_agent: str = "") -> int:
    path = _LOG_BRIDGE_STATE["log_path"]
    if not path:
        return 0
    entries_list = _in_time_order(entries)
    lines = []
    host = _entry_host(entries_list[0]) if entries_list else ""
    # One connect banner per host (not per line) — notes the host + browser once.
    seen_key = host or user_agent
    if seen_key and seen_key not in _SEEN_CLIENTS:
        _SEEN_CLIENTS.add(seen_key)
        lines.append(f"[log-bridge] client connected: {host or 'unknown host'}  ({user_agent or 'unknown'})\n")
    for e in entries_list:
        # One physical line per element — an entry whose message spans several
        # lines expands here, so the sink (a line-at-a-time formatter) stamps
        # every one of them rather than only the first.
        lines.extend(format_log_bridge_entry(e).splitlines(keepends=True))
    with open(path, "a") as f:
        f.writelines(lines)
    if _emit_sink is not None:
        for ln in lines:
            _emit_sink(ln)
    else:
        sys.stdout.write("".join(lines))
        sys.stdout.flush()
    return len(entries_list)


class _LogBridgeHandler(BaseHTTPRequestHandler):
    def _send_json(self, status: int, body: dict):
        data = json.dumps(body).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self):
        if self.path != "/health":
            self.send_response(404)
            self.end_headers()
            return
        self._send_json(200, {
            "port": _LOG_BRIDGE_STATE["port"],
            "hosts": _LOG_BRIDGE_STATE["hosts"],
            "project": _LOG_BRIDGE_STATE["project"],
            "pid": _LOG_BRIDGE_STATE["pid"],
            "started_at": _LOG_BRIDGE_STATE["started_at"],
        })

    def do_POST(self):
        if self.path != "/log":
            self.send_response(404)
            self.end_headers()
            return
        length = int(self.headers.get("Content-Length") or 0)
        raw = self.rfile.read(length) if length else b""
        try:
            entries = json.loads(raw.decode("utf-8"))
            if not isinstance(entries, list):
                raise ValueError("expected JSON array")
        except (ValueError, UnicodeDecodeError) as e:
            self._send_json(400, {"error": str(e)})
            return
        ua = self.headers.get("User-Agent") or "unknown"
        count = append_log_bridge_entries(entries, user_agent=ua)
        self._send_json(200, {"written": count})

    def log_message(self, fmt, *args):
        # Suppress BaseHTTPRequestHandler's default stdout logging; admin has its own log routing.
        return


def start_log_server(host: str, port: int, sock=None) -> ThreadingHTTPServer:
    """Start the bridge server. When `sock` is given, adopt that already-bound
    socket instead of binding a fresh one — log_bridge binds inside its port-range
    scan and hands the bound socket here so acquisition is atomic (no window for
    another process to steal the port between a probe and the real bind)."""
    if sock is None:
        server = ThreadingHTTPServer((host, port), _LogBridgeHandler)
    else:
        server = ThreadingHTTPServer((host, port), _LogBridgeHandler, bind_and_activate=False)
        server.socket.close()          # discard the unbound socket TCPServer made
        server.socket = sock           # adopt the pre-bound one
        server.server_address = sock.getsockname()
        # server_bind() normally sets these two; the adopt path skips it, so
        # mirror them here for any handler/stdlib code that reads them.
        host_, port_ = server.server_address[:2]
        server.server_name = socket.getfqdn(host_)
        server.server_port = port_
        try:
            server.server_activate()   # listen() on the adopted socket
        except OSError:
            # Never leak the caller's bound port if listen() fails on adoption.
            server.server_close()
            raise
    threading.Thread(target=server.serve_forever, daemon=True).start()
    return server
