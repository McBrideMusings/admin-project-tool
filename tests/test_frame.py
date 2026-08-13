"""Tests for the pinned URL footer (admin_lib/frame.py) and its manifest surface.

Covers the three seams the feature spans: `urls` schema validation, the
parent/child inheritance the archetype merger builds, and the footer itself —
what it paints, how tall it is, and when it declines to activate at all.
"""
from __future__ import annotations

import sys
from pathlib import Path

import pytest

REPO = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO))

from admin_lib import core, frame  # noqa: E402
from admin_lib.archetypes import ArchetypeError, _to_command  # noqa: E402
from admin_lib.validate import ValidationError, validate_action  # noqa: E402


# ---------------------------------------------------------------------------
# Schema
# ---------------------------------------------------------------------------

def _validate(data):
    validate_action("action 'x' from test", data, ArchetypeError)


def test_urls_accepts_bare_strings_and_label_tables():
    _validate({"run": "true", "urls": ["http://localhost:8787"]})
    _validate({"run": "true", "urls": [{"label": "Dev", "url": "http://localhost:8787"}]})


def test_urls_rejects_a_bare_string_instead_of_a_list():
    with pytest.raises(ValidationError, match="'urls' must be a list"):
        _validate({"run": "true", "urls": "http://localhost:8787"})


def test_urls_rejects_a_table_entry_with_no_url():
    with pytest.raises(ValidationError, match="needs a non-empty 'url'"):
        _validate({"run": "true", "urls": [{"label": "Dev"}]})


def test_urls_rejects_an_unknown_key_in_an_entry():
    with pytest.raises(ValidationError, match=r"unknown key\(s\) \['port'\]"):
        _validate({"run": "true", "urls": [{"url": "http://x", "port": 8787}]})


def test_urls_is_validated_on_inline_targets_too():
    with pytest.raises(ValidationError, match="target 'cf'"):
        _validate({"kind": "multi-target", "cf": {"run": "true", "urls": "nope"}})


# ---------------------------------------------------------------------------
# Inheritance — parent default, child override, child opt-out
# ---------------------------------------------------------------------------

def _meta(data):
    return _to_command("dev", data, "test", is_action=True).meta


def test_parent_urls_become_the_action_default():
    meta = _meta({"kind": "multi-target", "urls": ["local"], "cf": {"run": "true"}})
    assert meta["urls"] == [{"label": "local", "url": "local"}]
    # No child overrides, so no per-target table is emitted at all.
    assert "target_urls" not in meta


def test_a_child_override_is_recorded_and_siblings_inherit():
    meta = _meta({
        "kind": "multi-target",
        "urls": ["local"],
        "cf": {"run": "true", "urls": ["local", "lan"]},
        "dv": {"run": "true"},
    })
    assert meta["target_urls"]["cf"] == [
        {"label": "local", "url": "local"}, {"label": "lan", "url": "lan"},
    ]
    assert meta["target_urls"]["dv"] is None  # None means "inherit the parent"


def test_a_child_can_opt_out_with_an_empty_list():
    meta = _meta({
        "kind": "multi-target",
        "urls": ["local"],
        "cf": {"run": "true", "urls": []},
        "dv": {"run": "true"},
    })
    assert meta["target_urls"]["cf"] == []
    assert meta["target_urls"]["dv"] is None


# ---------------------------------------------------------------------------
# Dispatch-time resolution
# ---------------------------------------------------------------------------

def test_an_entry_naming_a_project_url_resolves_to_it(monkeypatch):
    monkeypatch.setattr(core, "_urls", [("local", "http://localhost:8787")])
    assert frame.resolve_urls([{"label": "local", "url": "local"}]) == [
        ("local", "http://localhost:8787"),
    ]


def test_an_entry_that_names_nothing_is_used_as_a_literal_url(monkeypatch):
    monkeypatch.setattr(core, "_urls", [("local", "http://localhost:8787")])
    assert frame.resolve_urls(["http://127.0.0.1:9999"]) == [
        ("http://127.0.0.1:9999", "http://127.0.0.1:9999"),
    ]


def test_env_vars_expand_at_dispatch_time(monkeypatch):
    monkeypatch.setattr(core, "_urls", [])
    monkeypatch.setenv("FRAME_TEST_PORT", "4321")
    assert frame.resolve_urls([{"label": "Dev", "url": "http://localhost:${FRAME_TEST_PORT}"}]) == [
        ("Dev", "http://localhost:4321"),
    ]


# ---------------------------------------------------------------------------
# Activation guards
# ---------------------------------------------------------------------------

def test_activate_declines_when_stdout_is_not_a_terminal():
    # pytest captures stdout, so it is already not a tty here.
    assert frame.activate("dev", [("Dev", "http://x")], "/tmp/x.log") is None
    assert frame._active is None


def test_activate_declines_when_there_are_no_urls():
    assert frame.activate("dev", [], "/tmp/x.log") is None


def test_activate_declines_when_admin_no_frame_is_set(monkeypatch):
    monkeypatch.setenv("ADMIN_NO_FRAME", "1")
    monkeypatch.setattr(sys.stdout, "isatty", lambda: True, raising=False)
    assert frame.activate("dev", [("Dev", "http://x")], "/tmp/x.log") is None


def test_write_passes_through_untouched_when_no_frame_is_active(capsys):
    frame.write("hello\n")
    assert capsys.readouterr().out == "hello\n"


# ---------------------------------------------------------------------------
# The footer itself
# ---------------------------------------------------------------------------

def _frame(urls, log="/tmp/dev-cf.log", cols=100, rows=24):
    f = frame.Frame("dev:cf", urls, log)
    f.rows, f.cols = rows, cols
    return f


def test_height_is_the_status_row_plus_the_log_row_plus_one_row_per_url():
    assert _frame([("a", "http://a"), ("b", "http://b")]).height == 4


def test_the_region_leaves_the_footer_rows_free():
    f = _frame([("a", "http://a")])          # height 3
    assert f.region_rows == 21               # 24 - 3


def test_the_region_never_collapses_below_two_rows():
    f = _frame([("a", f"http://{i}") for i in range(40)], rows=10)
    assert f.region_rows == 2


def test_the_second_row_always_carries_the_absolute_log_path():
    f = _frame([("a", "http://a")], log="/tmp/dev-cf.log")
    assert "/tmp/dev-cf.log" in f._rows()[1]
    assert "file:///tmp/dev-cf.log" in f._rows()[1]


def test_the_status_row_names_the_action_and_the_elapsed_time():
    row = _frame([("a", "http://a")])._rows()[0]
    assert "dev:cf" in row and "0s" in row


def test_a_self_labelled_url_is_not_printed_twice():
    row = _frame([("http://localhost:8787", "http://localhost:8787")])._rows()[2]
    assert row.count("http://localhost:8787") == 2  # the OSC 8 target and its text, no label


def test_a_long_url_is_clipped_to_the_terminal_width():
    long_url = "http://localhost:8787/" + "x" * 200
    row = _frame([("Dev", long_url)], cols=60)._rows()[2]
    assert "…" in row
    assert long_url in row  # the hyperlink target stays whole, only the shown text clips


def test_elapsed_formats_seconds_minutes_and_hours():
    assert frame._elapsed(9) == "9s"
    assert frame._elapsed(125) == "2m05s"
    assert frame._elapsed(7325) == "2h02m"


# ---------------------------------------------------------------------------
# Cross-reference validation (runs at `admin check`)
# ---------------------------------------------------------------------------

class _Err(Exception):
    pass


def _actions(**metas):
    return {name: _to_command(name, data, "test", is_action=True) for name, data in metas.items()}


def test_an_entry_naming_a_row_of_the_urls_table_passes():
    actions = _actions(dev={"run": "true", "urls": ["dev-cf"]})
    frame.validate_url_refs(actions, {"dev-cf": "http://localhost:8787"}, _Err)


def test_a_literal_url_passes_even_with_no_urls_table():
    actions = _actions(dev={"run": "true", "urls": ["http://localhost:3000"]})
    frame.validate_url_refs(actions, {}, _Err)


def test_a_name_that_matches_nothing_is_an_error_not_a_literal():
    actions = _actions(dev={"run": "true", "urls": ["local"]})
    with pytest.raises(_Err, match="is not a URL and does not name"):
        frame.validate_url_refs(actions, {"dev-cf": "http://localhost:8787"}, _Err)


def test_the_error_names_the_target_the_bad_entry_is_on():
    actions = _actions(dev={"kind": "multi-target", "cf": {"run": "true", "urls": ["lan"]}})
    with pytest.raises(_Err, match="target 'cf'"):
        frame.validate_url_refs(actions, {"dev-cf": "http://x"}, _Err)


# ---------------------------------------------------------------------------
# A delegating target still gets a footer
# ---------------------------------------------------------------------------

def test_deactivate_ignores_a_frame_it_does_not_own(monkeypatch):
    """An inner action's teardown must not rip down the outer action's footer."""
    outer = frame.Frame("dev:cf", [("a", "http://a")], "/tmp/x.log")
    monkeypatch.setattr(frame, "_active", outer)
    frame.deactivate(None)          # inner action never activated — leaves outer alone
    assert frame._active is outer
    monkeypatch.setattr(frame, "_active", None)
