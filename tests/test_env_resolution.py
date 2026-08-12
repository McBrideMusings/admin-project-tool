"""Tests for ${VAR} / ${VAR:-default} expansion in core.

The interesting case is a default that itself contains a placeholder:
`${GOPATH_BIN:-${HOME}/go/bin}`. The regex this replaced stopped the default at
the first `}`, so it read the reference as name=GOPATH_BIN, default="${HOME"
and produced `${HOME/go/bin}` — a plausible-looking path, silently wrong, with
no error anywhere. A manifest that used it got a launchd plist pointing at a
directory that does not exist.
"""
from __future__ import annotations

import sys
from pathlib import Path

import pytest

REPO = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO))

from admin_lib.core import iter_env_refs, resolve_computed, resolve_env  # noqa: E402


# ---------------------------------------------------------------------------
# Scanning
# ---------------------------------------------------------------------------

def _refs(s):
    return [(name, default) for _s, _e, name, default in iter_env_refs(s)]


def test_plain_and_defaulted_refs():
    assert _refs("${A}") == [("A", None)]
    assert _refs("${A:-fallback}") == [("A", "fallback")]
    assert _refs("x${A}y${B:-1}z") == [("A", None), ("B", "1")]


def test_nested_default_is_kept_whole():
    assert _refs("${GOPATH_BIN:-${HOME}/go/bin}") == [("GOPATH_BIN", "${HOME}/go/bin")]


def test_two_levels_of_nesting():
    assert _refs("${A:-${B:-${C}/x}}") == [("A", "${B:-${C}/x}")]


def test_unbalanced_brace_yields_nothing():
    assert _refs("${A") == []


def test_shell_constructs_are_skipped():
    # ${x%.c} and ${1} are not manifest placeholders; leave them to the shell.
    assert _refs("${x%.c}") == []
    assert _refs("${1}") == []


def test_empty_default_is_a_default_not_a_missing_one():
    assert _refs("${A:-}") == [("A", "")]


# ---------------------------------------------------------------------------
# resolve_env
# ---------------------------------------------------------------------------

def test_nested_default_expands_when_the_outer_var_is_unset(monkeypatch):
    monkeypatch.delenv("GOPATH_BIN", raising=False)
    monkeypatch.setenv("HOME", "/Users/example")
    assert resolve_env("${GOPATH_BIN:-${HOME}/go/bin}/x") == "/Users/example/go/bin/x"


def test_outer_value_wins_and_the_default_is_never_expanded(monkeypatch):
    monkeypatch.setenv("GOPATH_BIN", "/opt/gobin")
    monkeypatch.delenv("MISSING_ONE", raising=False)
    # If the unused default were expanded, the unset var inside it would exit(2).
    assert resolve_env("${GOPATH_BIN:-${MISSING_ONE}}") == "/opt/gobin"


def test_missing_var_inside_a_used_default_still_fails_loudly(monkeypatch):
    monkeypatch.delenv("OUTER_ONE", raising=False)
    monkeypatch.delenv("INNER_ONE", raising=False)
    with pytest.raises(SystemExit) as e:
        resolve_env("${OUTER_ONE:-${INNER_ONE}}")
    assert e.value.code == 2


def test_missing_var_without_a_default_exits(monkeypatch):
    monkeypatch.delenv("DEFINITELY_UNSET_XYZ", raising=False)
    with pytest.raises(SystemExit):
        resolve_env("${DEFINITELY_UNSET_XYZ}")


def test_literal_text_around_refs_is_preserved(monkeypatch):
    monkeypatch.setenv("A", "1")
    assert resolve_env("pre-${A}-mid-${B:-2}-post") == "pre-1-mid-2-post"


def test_idempotent_on_plain_strings():
    assert resolve_env("/no/placeholders/here") == "/no/placeholders/here"


def test_computed_var_beats_a_default(monkeypatch):
    monkeypatch.delenv("LAN_IP", raising=False)
    assert resolve_env("${LAN_IP:-0.0.0.0}") != "0.0.0.0"


# ---------------------------------------------------------------------------
# resolve_computed
# ---------------------------------------------------------------------------

def test_resolve_computed_leaves_other_vars_verbatim():
    assert resolve_computed("${SHELL_LOCAL}") == "${SHELL_LOCAL}"


def test_resolve_computed_substitutes_a_computed_var_inside_a_default(monkeypatch):
    monkeypatch.delenv("LAN_IP", raising=False)
    out = resolve_computed("${HOSTNAME_OVERRIDE:-${LAN_IP}}")
    assert out.startswith("${HOSTNAME_OVERRIDE:-")
    assert "${LAN_IP}" not in out


def test_resolve_computed_does_not_touch_a_plain_nested_default():
    assert resolve_computed("${A:-${B}/x}") == "${A:-${B}/x}"
