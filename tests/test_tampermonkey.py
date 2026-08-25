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


def test_write_policy_imports_a_plist_with_real_booleans(monkeypatch):
    seen = {}

    def fake_run(cmd, check=False, **kw):
        assert cmd[:2] == ["defaults", "import"]
        seen["domain"] = cmd[2]
        seen["plist"] = plistlib.load(open(cmd[3], "rb"))

    monkeypatch.setattr(tm.subprocess, "run", fake_run)
    entry = {"hash": "1:ab", "url": "data:x", "haltOnError": False, "installAsSystemScripts": False}
    domain = tm.write_policy("company.thebrowser.Browser", entry)
    assert domain == seen["domain"] == "company.thebrowser.Browser.extensions." + tm.TAMPERMONKEY_ID
    assert seen["plist"] == {"jsonImport": [entry]}
    assert seen["plist"]["jsonImport"][0]["haltOnError"] is False


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


def test_provision_quits_running_browser_writes_then_relaunches(monkeypatch, tmp_path):
    p = tmp_path / "demo.user.js"
    p.write_text(SCRIPT, encoding="utf-8")
    app = tmp_path / "Fake.app"
    app.mkdir()
    monkeypatch.setattr(tm, "BROWSERS", {"Fake": ("fake.bundle", app, tmp_path)})
    order = []
    monkeypatch.setattr(tm, "is_running", lambda a: True)
    monkeypatch.setattr(tm, "quit_browser", lambda a, timeout=30.0: order.append("quit") or True)
    monkeypatch.setattr(tm, "write_policy", lambda b, e: order.append("write") or "fake.bundle.extensions.x")
    monkeypatch.setattr(tm, "launch_browser", lambda a: order.append("launch"))
    assert tm.provision([p]) == 0
    assert order == ["quit", "write", "launch"]


def test_provision_no_restart_leaves_browser_alone(monkeypatch, tmp_path):
    p = tmp_path / "demo.user.js"
    p.write_text(SCRIPT, encoding="utf-8")
    app = tmp_path / "Fake.app"
    app.mkdir()
    monkeypatch.setattr(tm, "BROWSERS", {"Fake": ("fake.bundle", app, tmp_path)})
    order = []
    monkeypatch.setattr(tm, "is_running", lambda a: True)
    monkeypatch.setattr(tm, "quit_browser", lambda a, timeout=30.0: order.append("quit") or True)
    monkeypatch.setattr(tm, "write_policy", lambda b, e: order.append("write") or "d")
    monkeypatch.setattr(tm, "launch_browser", lambda a: order.append("launch"))
    assert tm.provision([p], restart=False) == 0
    assert order == ["write"]
