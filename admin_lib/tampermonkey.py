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
plist per browser into `/Library/Managed Preferences/<user>/<domain>.plist`
(one sudo — that directory is the *forced* layer, and Chromium only hands
MANDATORY policy to chrome.storage.managed; a plain `defaults write` lands as
RECOMMENDED and is silently dropped). No server is needed — the JSON travels
inside a `data:` URL. Chromium reads the policy at launch, so a running browser
is quit (it restores its windows) and relaunched after the write; TM then
applies the policy on its next service worker start. Every profile of the
browser sees the same policy, which is the point: one command, every profile.

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
import getpass
import glob
import hashlib
import json
import os
import plistlib
import struct
import subprocess
import sys
import tempfile
import time
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
        if isinstance(value, float) and value.is_integer():
            value = int(value)  # JS prints 1.0 as "1"
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

#: Where macOS keeps *forced* preferences for the current user. Chromium tags a
#: value from here MANDATORY; the same key in ~/Library/Preferences (a plain
#: `defaults write`) is only RECOMMENDED, and chrome.storage.managed drops
#: everything but MANDATORY (policy_value_store.cc: "Only include mandatory
#: policies for now"). Root-owned, hence the sudo below.
MANAGED_PREFS_DIR = Path("/Library/Managed Preferences") / getpass.getuser()


def policy_domain(bundle_id: str, ext_id: str = TAMPERMONKEY_ID) -> str:
    return f"{bundle_id}.extensions.{ext_id}"


def policy_path(bundle_id: str) -> Path:
    return MANAGED_PREFS_DIR / f"{policy_domain(bundle_id)}.plist"


def write_policies(entry: dict, bundle_ids: list[str]) -> list[Path]:
    """Install {jsonImport: [entry]} as a forced preference for each bundle id.

    One `sudo` prompt covers every browser: the plists are staged in a temp
    dir and installed root-owned in a single call.
    """
    staged: list[Path] = []
    with tempfile.TemporaryDirectory() as tmp:
        for b in bundle_ids:
            p = Path(tmp) / policy_path(b).name
            with open(p, "wb") as f:
                plistlib.dump({"jsonImport": [entry]}, f)
            staged.append(p)
        subprocess.run(["sudo", "-p", f"[admin tampermonkey] sudo password (writes {MANAGED_PREFS_DIR}): ",
                        "install", "-d", "-m", "755", "-o", "root", "-g", "wheel", str(MANAGED_PREFS_DIR)],
                       check=True)
        subprocess.run(["sudo", "install", "-m", "644", "-o", "root", "-g", "wheel",
                        *map(str, staged), str(MANAGED_PREFS_DIR)], check=True)
    return [policy_path(b) for b in bundle_ids]


def read_policy(bundle_id: str) -> list[dict]:
    p = policy_path(bundle_id)
    try:
        with open(p, "rb") as f:
            return list(plistlib.load(f).get("jsonImport", []))
    except (OSError, ValueError):
        return []


def clear_policies(bundle_ids: list[str]) -> None:
    paths = [str(policy_path(b)) for b in bundle_ids if policy_path(b).exists()]
    if paths:
        subprocess.run(["sudo", "rm", "-f", *paths], check=True)


# ---------------------------------------------------------------------------
# Browser lifecycle: Chromium reads user-level policy at launch
# ---------------------------------------------------------------------------

def is_running(app: Path) -> bool:
    """True when this user's main browser process (not a helper) is up."""
    r = subprocess.run(["pgrep", "-u", str(os.getuid()), "-f", f"^{app}/Contents/MacOS/[^/ ]+( |$)"],
                       capture_output=True)
    return r.returncode == 0


def quit_browser(app: Path, timeout: float = 30.0) -> bool:
    """Ask the app to quit (it restores its windows on relaunch); wait for exit."""
    subprocess.run(["osascript", "-e", f'quit app "{app}"'], capture_output=True)
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if not is_running(app):
            return True
        time.sleep(0.5)
    return False


def launch_browser(app: Path) -> None:
    subprocess.run(["open", "-a", str(app)], check=False)


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
        try:
            for batch in _log_batches(Path(log)):
                _merge_batch(batch, latest)
        except (IndexError, struct.error):
            continue  # torn tail — the browser is mid-write; keep what parsed
    return {k: v for k, (_, v) in latest.items()}


def _merge_batch(batch: bytes, latest: dict[bytes, tuple[int, bytes | None]]) -> None:
    if len(batch) < 12:
        return
    seq, cnt = struct.unpack("<QI", batch[:12])
    i = 12
    for n in range(cnt):
        if i >= len(batch):
            return
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

def provision(script_paths: list[Path], names: list[str] | None = None,
              restart: bool = True) -> int:
    """Write the policy for every installed target browser.

    With `restart` (the default) a running browser is quit first and relaunched
    after the write, because Chromium loads user-level policy at launch; without
    it the policy waits for the browser's next launch or periodic refresh.
    """
    doc = build_provisioning(script_paths)
    entry = policy_entry(doc)
    targets = [(n, b, app) for n, (b, app, _) in BROWSERS.items()
               if app.exists() and (not names or n in names)]
    if not targets:
        err("no supported browser installed (Arc, Chrome, Brave)")
        return 1
    stale = []
    for name, bundle, app in targets:
        if [e.get("hash") for e in read_policy(bundle)] == [entry["hash"]]:
            ok(f"{name}: policy already current ({entry['hash'][:12]}…) — nothing to do")
        else:
            stale.append((name, bundle, app))
    if not stale:
        return 0
    try:
        paths = write_policies(entry, [b for _, b, _ in stale])
    except subprocess.CalledProcessError as e:
        err(f"could not write {MANAGED_PREFS_DIR} (sudo failed or no tty): {e}")
        return 1
    for (name, _, _), p in zip(stale, paths):
        ok(f"{name}: jsonImport policy written to {p} ({entry['hash'][:12]}…)")
    rc = 0
    for name, _, app in stale:
        if not (restart and is_running(app)):
            continue
        info(f"{name}: quitting so the policy is read at launch…")
        if quit_browser(app):
            launch_browser(app)
            ok(f"{name}: relaunched")
        else:
            err(f"{name}: did not quit within 30s — relaunch it yourself")
            rc = 1
    for s in doc["scripts"]:
        info(f"  script: {s['name']}")
    info("Tampermonkey applies the policy when its service worker next starts. "
         "Check with: admin tampermonkey-status")
    return rc


def print_status(names: list[str] | None = None) -> int:
    st = status(names)
    if not st:
        warn("no supported browser installed")
        return 1
    for name, b in st.items():
        info(f"{name}: policy hashes in Managed Preferences: {[str(h)[:12] + '…' for h in b['policy']] or 'none'}")
        for label, p in b["profiles"].items():
            d = [str(h)[:12] + "…" for h in p["delivered"]] or "none"
            c = [str(h)[:12] + "…" for h in p["consumed"]] or "none"
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
    ap.add_argument("--no-restart", action="store_true",
                    help="write the policy without quitting/relaunching a running browser")
    a = ap.parse_args(argv)
    if a.clear:
        chosen = [(n, b) for n, (b, app, _) in BROWSERS.items()
                  if app.exists() and (not a.browser or n in a.browser)]
        clear_policies([b for _, b in chosen])
        for n, _ in chosen:
            ok(f"{n}: policy removed")
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
    return provision(a.scripts, a.browser, restart=not a.no_restart)


if __name__ == "__main__":
    sys.exit(main())
