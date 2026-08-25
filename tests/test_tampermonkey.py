"""admin_lib.tampermonkey — provisioning document, TM's structural hash, policy plist."""
import base64
import hashlib
import json
import plistlib
import struct
from pathlib import Path

import pytest

from admin_lib import tampermonkey as tm

SCRIPT = """// ==UserScript==
// @name         demo bridge
// @namespace    https://example.test
// @version      1.2.3
// @match        *://*/*
// ==/UserScript==
console.log("hi");
"""


def _sha(s):
    return hashlib.sha256(s.encode()).hexdigest()


def test_hash_leaves_follow_tm_typeof_prefix():
    assert tm.tm_hash("x") == _sha("string:x")
    assert tm.tm_hash(1) == _sha("number:1")
    assert tm.tm_hash(True) == _sha("boolean:true")
    assert tm.tm_hash(None) == _sha("object:null")


def test_hash_objects_concatenate_child_hashes_in_sorted_key_order_without_keys():
    expect = _sha(_sha("number:1") + _sha("string:b"))
    assert tm.tm_hash({"z": "b", "a": 1}) == expect
    assert tm.tm_hash({"a": 1, "z": "b"}) == expect
    # keys are not part of the hash — TM's port hashes values only
    assert tm.tm_hash({"q": 1, "r": "b"}) == expect


def test_hash_prints_integral_floats_like_js():
    assert tm.tm_hash(1.0) == _sha("number:1")
    assert tm.tm_hash(1.5) == _sha("number:1.5")


def test_leveldb_log_kv_survives_a_torn_tail(tmp_path):
    db = tmp_path / "db"
    db.mkdir()
    good = _log_record(1, [(b"a", b"v")])
    torn = struct.pack("<IHB", 0, 40, 1) + struct.pack("<QI", 2, 3) + b"\x01\x05ab"  # truncated batch
    (db / "000001.log").write_bytes(good + torn)
    assert tm.leveldb_log_kv(db) == {b"a": b"v"}


def test_hash_arrays_are_ordered():
    assert tm.tm_hash([1, "b"]) == _sha(_sha("number:1") + _sha("string:b"))
    assert tm.tm_hash([1, "b"]) != tm.tm_hash(["b", 1])


def test_build_provisioning_reads_name_and_base64_utf8_source(tmp_path):
    p = tmp_path / "demo.user.js"
    p.write_text(SCRIPT, encoding="utf-8")
    doc = tm.build_provisioning([p])
    assert doc["version"] == "1"
    (s,) = doc["scripts"]
    assert s["name"] == "demo bridge" and s["enabled"] is True
    assert base64.b64decode(s["source"]).decode("utf-8") == SCRIPT


def test_build_provisioning_requires_name(tmp_path):
    p = tmp_path / "x.user.js"
    p.write_text("// ==UserScript==\n// @version 1\n// ==/UserScript==\n")
    with pytest.raises(ValueError):
        tm.build_provisioning([p])


def test_policy_entry_embeds_document_as_data_url_and_hashes_parsed_value(tmp_path):
    p = tmp_path / "demo.user.js"
    p.write_text(SCRIPT, encoding="utf-8")
    doc = tm.build_provisioning([p])
    e = tm.policy_entry(doc)
    assert e["hash"] == "1:" + tm.tm_hash(doc)
    assert e["haltOnError"] is False and e["installAsSystemScripts"] is False
    prefix = "data:application/json;base64,"
    assert e["url"].startswith(prefix)
    assert json.loads(base64.b64decode(e["url"][len(prefix):])) == doc


def test_write_policies_installs_forced_plists_root_owned_in_one_sudo(monkeypatch, tmp_path):
    monkeypatch.setattr(tm, "MANAGED_PREFS_DIR", tmp_path / "managed")
    calls = []

    def fake_run(cmd, check=False, **kw):
        calls.append(cmd)
        if cmd[:2] == ["sudo", "install"]:  # the file install: capture the staged plists
            for src in cmd[cmd.index("wheel") + 1:-1]:
                calls.append(("staged", Path(src).name, plistlib.load(open(src, "rb"))))

    monkeypatch.setattr(tm.subprocess, "run", fake_run)
    entry = {"hash": "1:ab", "url": "data:x", "haltOnError": False, "installAsSystemScripts": False}
    paths = tm.write_policies(entry, ["company.thebrowser.Browser", "com.brave.Browser"])
    assert paths == [tmp_path / "managed" / f"company.thebrowser.Browser.extensions.{tm.TAMPERMONKEY_ID}.plist",
                     tmp_path / "managed" / f"com.brave.Browser.extensions.{tm.TAMPERMONKEY_ID}.plist"]
    sudo_calls = [c for c in calls if isinstance(c, list)]
    assert [c[:2] for c in sudo_calls] == [["sudo", "-p"], ["sudo", "install"]]
    assert sudo_calls[1][-1] == str(tmp_path / "managed") and "-o" in sudo_calls[1] and "root" in sudo_calls[1]
    staged = [c for c in calls if isinstance(c, tuple)]
    assert [s[1] for s in staged] == [p.name for p in paths]
    assert all(s[2] == {"jsonImport": [entry]} and s[2]["jsonImport"][0]["haltOnError"] is False for s in staged)


def test_read_policy_reads_the_forced_plist(monkeypatch, tmp_path):
    monkeypatch.setattr(tm, "MANAGED_PREFS_DIR", tmp_path)
    entry = {"hash": "1:ab", "url": "data:x"}
    with open(tm.policy_path("x.y"), "wb") as f:
        plistlib.dump({"jsonImport": [entry]}, f)
    assert tm.read_policy("x.y") == [entry]
    assert tm.read_policy("nope") == []


def _log_record(seq, puts):
    """One LevelDB log block holding a WriteBatch of (key, value|None) puts."""
    body = struct.pack("<QI", seq, len(puts))
    for k, v in puts:
        if v is None:
            body += b"\x00" + bytes([len(k)]) + k
        else:
            body += b"\x01" + bytes([len(k)]) + k + bytes([len(v)]) + v
    return struct.pack("<IHB", 0, len(body), 1) + body


def test_leveldb_log_kv_takes_newest_write_and_honours_deletes(tmp_path):
    db = tmp_path / "db"
    db.mkdir()
    (db / "000001.log").write_bytes(
        _log_record(1, [(b"a", b"old"), (b"b", b"keep")])
        + _log_record(3, [(b"a", b"new"), (b"b", None)]))
    assert tm.leveldb_log_kv(db) == {b"a": b"new", b"b": None}


def test_profile_status_reads_delivery_and_installed_scripts(tmp_path):
    prof = tmp_path / "Profile 1"
    managed = prof / "Managed Extension Settings" / tm.TAMPERMONKEY_ID
    local = prof / "Local Extension Settings" / tm.TAMPERMONKEY_ID
    managed.mkdir(parents=True)
    local.mkdir(parents=True)
    (managed / "000003.log").write_bytes(_log_record(1, [
        (b"jsonImport", json.dumps([{"hash": "1:abc", "url": "data:x"}]).encode())]))
    meta = json.dumps({"value": {"name": "demo bridge", "version": "1.2.3"}}).encode()
    (local / "000026.log").write_bytes(_log_record(5, [(b"!extdb.@meta#uuid-1", meta)]))
    st = tm.profile_status(tmp_path, "Profile 1")
    assert st["delivered"] == ["1:abc"]
    assert st["installed"] == {"demo bridge": "1.2.3"}


def _fake_provision_env(monkeypatch, tmp_path, running=True):
    p = tmp_path / "demo.user.js"
    p.write_text(SCRIPT, encoding="utf-8")
    app = tmp_path / "Fake.app"
    app.mkdir()
    monkeypatch.setattr(tm, "BROWSERS", {"Fake": ("fake.bundle", app, tmp_path)})
    order = []
    monkeypatch.setattr(tm, "is_running", lambda a: running)
    monkeypatch.setattr(tm, "quit_browser", lambda a, timeout=30.0: order.append("quit") or True)
    monkeypatch.setattr(tm, "write_policies", lambda e, ids: order.append("write") or [tmp_path / f"{b}.plist" for b in ids])
    monkeypatch.setattr(tm, "read_policy", lambda b: [])
    monkeypatch.setattr(tm, "launch_browser", lambda a: order.append("launch"))
    return p, order


def test_provision_is_a_no_op_when_the_forced_policy_already_carries_the_hash(monkeypatch, tmp_path):
    p, order = _fake_provision_env(monkeypatch, tmp_path)
    current = tm.policy_entry(tm.build_provisioning([p]))["hash"]
    monkeypatch.setattr(tm, "read_policy", lambda b: [{"hash": current, "url": "data:x"}])
    assert tm.provision([p]) == 0
    assert order == []  # no sudo, no quit, no relaunch


def test_provision_reports_a_failed_sudo_instead_of_raising(monkeypatch, tmp_path):
    p, order = _fake_provision_env(monkeypatch, tmp_path)

    def boom(e, ids):
        raise tm.subprocess.CalledProcessError(1, ["sudo"])

    monkeypatch.setattr(tm, "write_policies", boom)
    assert tm.provision([p]) == 1
    assert order == []


def test_provision_writes_then_quits_and_relaunches_a_running_browser(monkeypatch, tmp_path):
    p, order = _fake_provision_env(monkeypatch, tmp_path)
    assert tm.provision([p]) == 0
    assert order == ["write", "quit", "launch"]


def test_provision_no_restart_leaves_browser_alone(monkeypatch, tmp_path):
    p, order = _fake_provision_env(monkeypatch, tmp_path)
    assert tm.provision([p], restart=False) == 0
    assert order == ["write"]


def test_provision_does_not_launch_a_browser_that_was_not_running(monkeypatch, tmp_path):
    p, order = _fake_provision_env(monkeypatch, tmp_path, running=False)
    assert tm.provision([p]) == 0
    assert order == ["write"]
