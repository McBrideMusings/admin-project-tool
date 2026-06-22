"""Tiny HTTP server that receives browser console-log batches and appends them
to the admin process's log file.

Started by admin_lib.log_bridge when a long-running action has [log_bridge]
configured. Stdlib only — http.server + threading.
"""
from __future__ import annotations

import json
import sys
import threading
import datetime
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Iterable
from urllib.parse import urlparse

_SEEN_CLIENTS: set[str] = set()

# Optional per-line output sink. When set, emitted client lines go through it
# instead of a raw stdout write — `admin logs` uses this to render bridged
# [client] lines through the same LineRenderer as the tailed stream. The
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


def format_log_bridge_entry(entry: dict) -> str:
    ts = entry.get("timestamp") or datetime.datetime.now(datetime.timezone.utc).isoformat()
    level = (entry.get("level") or "log").lower()
    msg = entry.get("message") or ""
    return f"{ts} [client] [{level}] {msg}\n"


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
    entries_list = list(entries)
    lines = []
    host = _entry_host(entries_list[0]) if entries_list else ""
    # One connect banner per host (not per line) — notes the host + browser once.
    seen_key = host or user_agent
    if seen_key and seen_key not in _SEEN_CLIENTS:
        _SEEN_CLIENTS.add(seen_key)
        lines.append(f"[log-bridge] client connected: {host or 'unknown host'}  ({user_agent or 'unknown'})\n")
    lines.extend(format_log_bridge_entry(e) for e in entries_list)
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


def start_log_server(host: str, port: int) -> ThreadingHTTPServer:
    server = ThreadingHTTPServer((host, port), _LogBridgeHandler)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    return server
