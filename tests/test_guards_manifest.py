"""Manifest parsing + archetype audit for `[guards.<name>]` and `requires`.

Covers:
- `[guards.<name>]` schema (required `run` + `fail`, name collisions with built-ins).
- `requires` on flat actions and multi-target actions.
- Parent default vs per-target override resolution.
- Audit-time errors for unknown guard names referenced from `requires`.
"""
from __future__ import annotations

import sys
try:
    import tomllib
except ImportError:
    import tomli as tomllib  # type: ignore[no-redef]
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO))

from admin_lib.manifest import GUARDS_BUILTIN, ManifestError, parse  # noqa: E402
from admin_lib.archetypes import ArchetypeError, resolve  # noqa: E402

ARCH_DIR = REPO / "archetypes"


def _parse(toml_text: str):
    return parse(tomllib.loads(toml_text), source="<test>")


# ---------------------------------------------------------------------------
# [guards.<name>] schema
# ---------------------------------------------------------------------------

def test_guards_table_parses_with_run_and_fail():
    m = _parse('''
        project_name = "T"
        python_min   = "3.11"

        [guards.no-tag]
        run  = "test -z \\"$(git tag --points-at HEAD)\\""
        fail = "HEAD is already tagged — bump the version first."
    ''')
    assert "no-tag" in m.guards
    assert m.guards["no-tag"]["run"].startswith("test -z")
    assert "bump the version" in m.guards["no-tag"]["fail"]


def test_guards_missing_run_rejected():
    try:
        _parse('''
            project_name = "T"
            python_min   = "3.11"
            [guards.bad]
            fail = "missing run"
        ''')
    except ManifestError as e:
        assert "missing required key 'run'" in str(e)
    else:
        raise AssertionError("expected ManifestError for missing 'run'")


def test_guards_missing_fail_rejected():
    try:
        _parse('''
            project_name = "T"
            python_min   = "3.11"
            [guards.bad]
            run = "exit 0"
        ''')
    except ManifestError as e:
        assert "missing required key 'fail'" in str(e)
    else:
        raise AssertionError("expected ManifestError for missing 'fail'")


def test_guards_empty_string_rejected():
    try:
        _parse('''
            project_name = "T"
            python_min   = "3.11"
            [guards.bad]
            run  = ""
            fail = "nope"
        ''')
    except ManifestError as e:
        assert "non-empty string" in str(e)
    else:
        raise AssertionError("expected ManifestError for empty run")


def test_guards_unknown_key_rejected():
    try:
        _parse('''
            project_name = "T"
            python_min   = "3.11"
            [guards.bad]
            run     = "exit 0"
            fail    = "nope"
            timeout = 30
        ''')
    except ManifestError as e:
        assert "unknown keys" in str(e)
        assert "timeout" in str(e)
    else:
        raise AssertionError("expected ManifestError for unknown guard key")


def test_guards_builtin_name_collision_rejected():
    for builtin in GUARDS_BUILTIN:
        try:
            _parse(f'''
                project_name = "T"
                python_min   = "3.11"
                [guards.{builtin}]
                run  = "exit 0"
                fail = "shouldnt happen"
            ''')
        except ManifestError as e:
            assert "built-in" in str(e)
        else:
            raise AssertionError(f"expected collision rejection for built-in '{builtin}'")


# ---------------------------------------------------------------------------
# requires on actions and targets
# ---------------------------------------------------------------------------

def test_requires_on_flat_action_landed_in_meta():
    m = _parse('''
        project_name = "T"
        python_min   = "3.11"

        [commands.flat]
        desc  = "Flat"
        steps = ["flat"]

        [actions.flat]
        kind     = "shell"
        run      = "echo flat"
        requires = ["git-clean"]
    ''')
    resolved = resolve(m, ARCH_DIR)
    assert resolved.actions["flat"].meta.get("requires") == ["git-clean"]


def test_requires_parent_default_inherited_by_targets():
    m = _parse('''
        project_name = "T"
        python_min   = "3.11"

        [commands.preview]
        desc  = "Preview"
        steps = ["preview"]

        [actions.preview]
        kind     = "multi-target"
        requires = ["git-clean", "git-pushed"]

        [actions.preview.cf]
        run = "echo cf"

        [actions.preview.devvit]
        run = "echo devvit"
    ''')
    resolved = resolve(m, ARCH_DIR)
    action = resolved.actions["preview"]
    assert action.meta["requires"] == ["git-clean", "git-pushed"]
    # No child overrides → target_requires is absent or every entry is None.
    target_requires = action.meta.get("target_requires", {})
    if target_requires:
        assert all(v is None for v in target_requires.values())


def test_requires_child_override_replaces_parent():
    m = _parse('''
        project_name = "T"
        python_min   = "3.11"

        [commands.preview]
        desc  = "Preview"
        steps = ["preview"]

        [actions.preview]
        kind     = "multi-target"
        requires = ["git-clean", "git-pushed"]

        [actions.preview.cf]
        run = "echo cf"

        [actions.preview.devvit]
        run      = "echo devvit"
        requires = []
    ''')
    resolved = resolve(m, ARCH_DIR)
    target_requires = resolved.actions["preview"].meta["target_requires"]
    assert target_requires["cf"] is None
    assert target_requires["devvit"] == []


def test_requires_non_list_rejected():
    try:
        m = _parse('''
            project_name = "T"
            python_min   = "3.11"

            [commands.flat]
            desc  = "Flat"
            steps = ["flat"]

            [actions.flat]
            kind     = "shell"
            run      = "echo"
            requires = "git-clean"
        ''')
        resolve(m, ARCH_DIR)
    except ArchetypeError as e:
        assert "list of guard-name strings" in str(e)
    else:
        raise AssertionError("expected ArchetypeError for non-list requires")


# ---------------------------------------------------------------------------
# audit: requires must resolve to a known guard
# ---------------------------------------------------------------------------

def test_audit_unknown_parent_requires_rejected():
    m = _parse('''
        project_name = "T"
        python_min   = "3.11"

        [commands.flat]
        desc  = "Flat"
        steps = ["flat"]

        [actions.flat]
        kind     = "shell"
        run      = "echo"
        requires = ["nope"]
    ''')
    try:
        resolve(m, ARCH_DIR)
    except ArchetypeError as e:
        assert "unknown guard 'nope'" in str(e)
        assert "git-clean" in str(e)
    else:
        raise AssertionError("expected ArchetypeError")


def test_audit_unknown_target_requires_rejected():
    m = _parse('''
        project_name = "T"
        python_min   = "3.11"

        [commands.preview]
        desc  = "Preview"
        steps = ["preview"]

        [actions.preview]
        kind = "multi-target"

        [actions.preview.cf]
        run      = "echo"
        requires = ["nope"]
    ''')
    try:
        resolve(m, ARCH_DIR)
    except ArchetypeError as e:
        assert "target 'cf'" in str(e)
        assert "unknown guard 'nope'" in str(e)
    else:
        raise AssertionError("expected ArchetypeError")


def test_audit_accepts_user_guard_referenced_by_requires():
    m = _parse('''
        project_name = "T"
        python_min   = "3.11"

        [guards.my-guard]
        run  = "exit 0"
        fail = "stop"

        [commands.flat]
        desc  = "Flat"
        steps = ["flat"]

        [actions.flat]
        kind     = "shell"
        run      = "echo"
        requires = ["my-guard"]
    ''')
    resolved = resolve(m, ARCH_DIR)
    assert resolved.actions["flat"].meta["requires"] == ["my-guard"]
