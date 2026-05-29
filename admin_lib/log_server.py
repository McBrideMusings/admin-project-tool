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

_SEEN_CLIENTS: set[str] = set()

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
    url = entry.get("url") or ""
    return f"{ts} [client] [{level}] {msg}  ({url})\n"


def append_log_bridge_entries(entries: Iterable[dict], *, client_key: str = "") -> int:
    path = _LOG_BRIDGE_STATE["log_path"]
    if not path:
        return 0
    entries_list = list(entries)
    lines = []
    if client_key and client_key not in _SEEN_CLIENTS:
        _SEEN_CLIENTS.add(client_key)
        lines.append(f"[log-bridge] client connected: {client_key}\n")
    lines.extend(format_log_bridge_entry(e) for e in entries_list)
    with open(path, "a") as f:
        f.writelines(lines)
    out = "".join(lines)
    sys.stdout.write(out)
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
        count = append_log_bridge_entries(entries, client_key=ua)
        self._send_json(200, {"written": count})

    def log_message(self, fmt, *args):
        # Suppress BaseHTTPRequestHandler's default stdout logging; admin has its own log routing.
        return


def start_log_server(host: str, port: int) -> ThreadingHTTPServer:
    server = ThreadingHTTPServer((host, port), _LogBridgeHandler)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    return server
