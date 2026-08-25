"""Install a userscript into Tampermonkey without touching the browser.

Tampermonkey 5.5+ reads a `jsonImport` list from the extension's *managed*
storage (`chrome.storage.managed`) every time its service worker starts. Each
entry names a provisioning JSON (the format of its Utilities → Export) plus a
content hash; TM downloads the JSON, checks the hash, installs every script in
it (`replace: true`, so a re-provision updates in place) and remembers the hash
so the same file is applied once. Chromium fills managed storage from browser
policy, and on macOS a per-extension policy is an ordinary preference domain:

    <browser bundle id>.extensions.<extension id>   →   { jsonImport = [...] }

So installing a script is: build the JSON, hash it the way TM does, write one
plist into that domain with `defaults import`. No server is needed — the JSON
travels inside a `data:` URL. Chromium re-reads user-level policy on launch
and on its periodic policy refresh; TM then applies it on its next service
worker start. Every profile of the browser sees the same policy, which is the
point: one command, every profile.

The hash (`1:<sha256>`) is TM's structural hash of the *parsed* JSON, not of
the bytes: leaves hash as sha256("<typeof>:<value>"), arrays hash the
concatenation of their children's hashes, objects the same in sorted-key order
with the keys themselves omitted. Ported from background.js (`No`/`Do`, 5.5.0).

Read-back: `status()` reports, per browser profile, whether the policy has
reached the extension (the `Managed Extension Settings` LevelDB) and whether
the script is installed (`@meta#` records in `Local Extension Settings`). Only
the LevelDB write-ahead log is parsed — fresh writes always land there, but a
record Chromium has compacted into an .ldb table is invisible, so "not seen"
means exactly that, not "absent".
"""
from __future__ import annotations

import argparse
import base64
import glob
import hashlib
import json
import os
import plistlib
import struct
import subprocess
import sys
import tempfile
from pathlib import Path

from .core import err, info, ok, warn

TAMPERMONKEY_ID = "dhdgffkkebhmkfjojejmpbldmpobfkfo"  # Chrome Web Store, stable

_APP_SUPPORT = Path.home() / "Library" / "Application Support"

#: name → (bundle id, app path, user-data dir). Only installed apps are acted on.
BROWSERS: dict[str, tuple[str, Path, Path]] = {
    "Arc": ("company.thebrowser.Browser", Path("/Applications/Arc.app"),
            _APP_SUPPORT / "Arc" / "User Data"),
    "Chrome": ("com.google.Chrome", Path("/Applications/Google Chrome.app"),
               _APP_SUPPORT / "Google" / "Chrome"),
    "Brave": ("com.brave.Browser", Path("/Applications/Brave Browser.app"),
              _APP_SUPPORT / "BraveSoftware" / "Brave-Browser"),
}


# ---------------------------------------------------------------------------
# Provisioning JSON + TM's hash
# ---------------------------------------------------------------------------

def _sha(s: str) -> str:
    return hashlib.sha256(s.encode("utf-8")).hexdigest()


def tm_hash(value) -> str:
    """Tampermonkey's structural hash of a parsed JSON value (hex sha256)."""
    if value is None:
        return _sha("object:null")
    if isinstance(value, bool):
        return _sha("boolean:true" if value else "boolean:false")
    if isinstance(value, (int, float)):
        return _sha(f"number:{value}")
    if isinstance(value, str):
        return _sha(f"string:{value}")
    if isinstance(value, list):
        return _sha("".join(tm_hash(v) for v in value))
    if isinstance(value, dict):
        return _sha("".join(tm_hash(value[k]) for k in sorted(value)))
    raise TypeError(f"unhashable JSON value: {type(value).__name__}")


def script_header(source: str) -> dict[str, str]:
    """First value of each `// @key value` line inside ==UserScript==."""
    out: dict[str, str] = {}
    inside = False
    for line in source.splitlines():
        s = line.strip()
        if s == "// ==UserScript==":
            inside = True
        elif s == "// ==/UserScript==":
            break
        elif inside and s.startswith("// @"):
            parts = s[4:].split(None, 1)
            out.setdefault(parts[0], parts[1].strip() if len(parts) > 1 else "")
    return out


def build_provisioning(script_paths: list[Path]) -> dict:
    """TM export-format document carrying the given scripts, enabled."""
    scripts = []
    for p in script_paths:
        src = p.read_text(encoding="utf-8")
        hdr = script_header(src)
        if "name" not in hdr:
            raise ValueError(f"{p}: no @name in ==UserScript== header")
        scripts.append({
            "name": hdr["name"],
            "enabled": True,
            "source": base64.b64encode(src.encode("utf-8")).decode("ascii"),
        })
    return {"version": "1", "scripts": scripts}


def policy_entry(doc: dict) -> dict:
    """One `jsonImport` entry: the document inline as a data: URL plus its hash."""
    raw = json.dumps(doc, separators=(",", ":")).encode("utf-8")
    return {
        "hash": "1:" + tm_hash(doc),
        "url": "data:application/json;base64," + base64.b64encode(raw).decode("ascii"),
        "haltOnError": False,
        "installAsSystemScripts": False,  # user can still edit/remove it
    }


# ---------------------------------------------------------------------------
# Policy write / read (macOS preference domains)
# ---------------------------------------------------------------------------

def policy_domain(bundle_id: str, ext_id: str = TAMPERMONKEY_ID) -> str:
    return f"{bundle_id}.extensions.{ext_id}"


def write_policy(bundle_id: str, entry: dict) -> str:
    """Replace the domain's `jsonImport` with [entry]. Returns the domain."""
    domain = policy_domain(bundle_id)
    with tempfile.NamedTemporaryFile(suffix=".plist", delete=False) as f:
        plistlib.dump({"jsonImport": [entry]}, f)
        path = f.name
    try:
        subprocess.run(["defaults", "import", domain, path], check=True)
    finally:
        os.unlink(path)
    return domain


def read_policy(bundle_id: str) -> list[dict]:
    domain = policy_domain(bundle_id)
    r = subprocess.run(["defaults", "export", domain, "-"], capture_output=True)
    if r.returncode != 0 or not r.stdout.strip():
        return []
    try:
        return list(plistlib.loads(r.stdout).get("jsonImport", []))
    except Exception:
        return []


def clear_policy(bundle_id: str) -> None:
    subprocess.run(["defaults", "delete", policy_domain(bundle_id)],
                   capture_output=True)


# ---------------------------------------------------------------------------
# Read-back: LevelDB write-ahead log
# ---------------------------------------------------------------------------

_LOG_BLOCK = 32768
_LOG_HEADER = 7  # crc32 (4) + length (2) + type (1)
_FULL, _FIRST, _MIDDLE, _LAST = 1, 2, 3, 4


def _varint(buf: bytes, i: int) -> tuple[int, int]:
    n = shift = 0
    while True:
        b = buf[i]
        i += 1
        n |= (b & 0x7F) << shift
        if not b & 0x80:
            return n, i
        shift += 7


def _log_batches(path: Path):
    data = path.read_bytes()
    pos, pending = 0, b""
    while pos + _LOG_HEADER <= len(data):
        room = _LOG_BLOCK - (pos % _LOG_BLOCK)
        if room < _LOG_HEADER:
            pos += room
            continue
        _crc, ln, typ = struct.unpack("<IHB", data[pos:pos + _LOG_HEADER])
        if ln == 0 and typ == 0:
            break
        payload = data[pos + _LOG_HEADER:pos + _LOG_HEADER + ln]
        if len(payload) < ln:
            break
        pos += _LOG_HEADER + ln
        if typ == _FULL:
            yield payload
        elif typ == _FIRST:
            pending = payload
        elif typ == _MIDDLE:
            pending += payload
        elif typ == _LAST:
            yield pending + payload
            pending = b""


def leveldb_log_kv(db_dir: Path) -> dict[bytes, bytes | None]:
    """Newest value per key from a LevelDB's *.log files; None = deleted."""
    latest: dict[bytes, tuple[int, bytes | None]] = {}
    for log in sorted(glob.glob(str(db_dir / "*.log"))):
        for batch in _log_batches(Path(log)):
            if len(batch) < 12:
                continue
            seq, cnt = struct.unpack("<QI", batch[:12])
            i = 0 + 12
            for n in range(cnt):
                if i >= len(batch):
                    break
                kind = batch[i]
                i += 1
                klen, i = _varint(batch, i)
                key = batch[i:i + klen]
                i += klen
                val = None
                if kind == 1:
                    vlen, i = _varint(batch, i)
                    val = batch[i:i + vlen]
                    i += vlen
                prev = latest.get(key)
                if prev is None or seq + n > prev[0]:
                    latest[key] = (seq + n, val)
    return {k: v for k, (_, v) in latest.items()}


def profiles(user_data: Path) -> dict[str, str]:
    """Profile directory → display name, from the browser's Local State."""
    try:
        state = json.loads((user_data / "Local State").read_text())
        cache = state["profile"]["info_cache"]
    except Exception:
        return {}
    return {d: str(v.get("name", d)) for d, v in cache.items()
            if not str(v.get("name", "")).startswith("__")}


def profile_status(user_data: Path, profile_dir: str) -> dict:
    base = user_data / profile_dir
    managed = leveldb_log_kv(base / "Managed Extension Settings" / TAMPERMONKEY_ID)
    delivered = []
    raw = managed.get(b"jsonImport")
    if raw:
        try:
            delivered = [e.get("hash") for e in json.loads(raw)]
        except Exception:
            delivered = ["<unparseable>"]
    local = leveldb_log_kv(base / "Local Extension Settings" / TAMPERMONKEY_ID)
    installed: dict[str, str] = {}
    consumed: list[str] = []
    for k, v in local.items():
        if v is None:
            continue
        if b"@meta#" in k:
            try:
                meta = json.loads(v)["value"]
                installed[str(meta.get("name"))] = str(meta.get("version"))
            except Exception:
                pass
        elif b"managed" in k and b"consumed" in k:
            try:
                consumed = list(json.loads(v).get("value", {}).keys())
            except Exception:
                pass
    return {"delivered": delivered, "installed": installed, "consumed": consumed}


def status(names: list[str] | None = None) -> dict:
    out: dict = {}
    for name, (bundle, app, user_data) in BROWSERS.items():
        if names and name not in names:
            continue
        if not app.exists():
            continue
        out[name] = {
            "policy": [e.get("hash") for e in read_policy(bundle)],
            "profiles": {label: profile_status(user_data, d)
                         for d, label in profiles(user_data).items()
                         if (user_data / d / "Local Extension Settings" / TAMPERMONKEY_ID).is_dir()},
        }
    return out


# ---------------------------------------------------------------------------
# CLI: python3 -m admin_lib.tampermonkey [--status] [--browser NAME] SCRIPT...
# ---------------------------------------------------------------------------

def provision(script_paths: list[Path], names: list[str] | None = None) -> int:
    doc = build_provisioning(script_paths)
    entry = policy_entry(doc)
    targets = [(n, b) for n, (b, app, _) in BROWSERS.items()
               if app.exists() and (not names or n in names)]
    if not targets:
        err("no supported browser installed (Arc, Chrome, Brave)")
        return 1
    for name, bundle in targets:
        domain = write_policy(bundle, entry)
        ok(f"{name}: jsonImport policy written to {domain} ({entry['hash'][:12]}…)")
    for s in doc["scripts"]:
        info(f"  script: {s['name']}")
    info("the browser re-reads user policy on launch or its next periodic refresh; "
         "Tampermonkey applies it when its service worker next starts. "
         "Check with: python3 -m admin_lib.tampermonkey --status")
    return 0


def print_status(names: list[str] | None = None) -> int:
    st = status(names)
    if not st:
        warn("no supported browser installed")
        return 1
    for name, b in st.items():
        info(f"{name}: policy hashes in defaults: {[h[:12] + '…' for h in b['policy']] or 'none'}")
        for label, p in b["profiles"].items():
            d = [h[:12] + "…" for h in p["delivered"]] or "none"
            c = [h[:12] + "…" for h in p["consumed"]] or "none"
            print(f"  [{label}] delivered: {d}  consumed: {c}")
            for n, v in sorted(p["installed"].items()):
                print(f"           installed (seen in log): {n} {v}")
    return 0


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(prog="admin_lib.tampermonkey", description=__doc__.split("\n\n")[0])
    ap.add_argument("scripts", nargs="*", type=Path, help="*.user.js files to provision")
    ap.add_argument("--browser", action="append", help="limit to this browser (repeatable)")
    ap.add_argument("--status", action="store_true", help="report policy/delivery/install state and exit")
    ap.add_argument("--clear", action="store_true", help="remove the policy domain(s) and exit")
    ap.add_argument("--json", action="store_true", help="with --status: machine-readable output")
    a = ap.parse_args(argv)
    if a.clear:
        for n, (b, app, _) in BROWSERS.items():
            if app.exists() and (not a.browser or n in a.browser):
                clear_policy(b)
                ok(f"{n}: policy domain removed")
        return 0
    if a.status:
        if a.json:
            print(json.dumps(status(a.browser), indent=2))
            return 0
        return print_status(a.browser)
    if not a.scripts:
        ap.error("give at least one *.user.js, or --status / --clear")
    missing = [p for p in a.scripts if not p.is_file()]
    if missing:
        err(f"not a file: {', '.join(map(str, missing))}")
        return 1
    return provision(a.scripts, a.browser)


if __name__ == "__main__":
    sys.exit(main())
