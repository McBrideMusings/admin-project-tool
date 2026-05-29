"""End-to-end render tests for guards + requires.

Exercises the generator from `admin.toml` text → final `./admin` source,
asserting that:
- Built-in guard names produce no `register_shell_guard` emission (they
  register themselves at import).
- TOML `[guards.<name>]` entries produce one `register_shell_guard(...)` call.
- Flat (non-multi-target) actions with `requires` get a guard check at the
  top of their `_act_<name>` body.
- Multi-target actions emit a `_target_requires` map with resolved per-target
  lists and a dispatch-time `run_requires` call.
- The generated output is syntactically valid Python.
"""
from __future__ import annotations

import ast
import sys
try:
    import tomllib
except ImportError:
    import tomli as tomllib  # type: ignore[no-redef]
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO))

from gen.manifest import parse  # noqa: E402
from gen.archetypes import resolve  # noqa: E402
from gen.render import render  # noqa: E402

ARCH_DIR = REPO / "archetypes"
LIB_DIR = REPO / "admin_lib"


def _render(toml_text: str) -> str:
    m = parse(tomllib.loads(toml_text), source="<test>")
    resolved = resolve(m, ARCH_DIR)
    return render(resolved, LIB_DIR, generator_commit="test-sha", strip_dead_code=False)


def _act_body(src: str, action_name: str) -> str:
    """Extract the `def _act_<action>(args):` block from rendered source."""
    lines = src.split("\n")
    ident = action_name.replace("-", "_")
    start = next(
        (i for i, ln in enumerate(lines) if ln.startswith(f"def _act_{ident}(")),
        None,
    )
    assert start is not None, f"no _act_{ident} in rendered source"
    out = [lines[start]]
    for ln in lines[start + 1:]:
        if ln.startswith("register_action("):
            out.append(ln)
            break
        out.append(ln)
    return "\n".join(out)


# ---------------------------------------------------------------------------
# Output is valid Python in all relevant shapes
# ---------------------------------------------------------------------------

def test_rendered_output_is_valid_python_with_guards():
    src = _render('''
        project_name = "T"
        python_min   = "3.11"

        [guards.custom]
        run  = "test -f .ok"
        fail = "need .ok"

        [commands.deploy]
        desc  = "Deploy"
        steps = ["deploy"]

        [actions.deploy]
        kind     = "shell"
        run      = "echo deploy"
        requires = ["git-clean", "custom"]
    ''')
    ast.parse(src)


# ---------------------------------------------------------------------------
# [guards.X] emission
# ---------------------------------------------------------------------------

def test_user_guard_emits_register_shell_guard_call():
    src = _render('''
        project_name = "T"
        python_min   = "3.11"

        [guards.no-tag]
        run  = "test -z \\"$(git tag --points-at HEAD)\\""
        fail = "HEAD is already tagged."

        [commands.flat]
        desc  = "Flat"
        steps = ["flat"]

        [actions.flat]
        kind     = "shell"
        run      = "echo"
        requires = ["no-tag"]
    ''')
    assert 'register_shell_guard("no-tag"' in src
    assert "HEAD is already tagged." in src


def test_no_user_guards_means_no_register_shell_guard_calls():
    src = _render('''
        project_name = "T"
        python_min   = "3.11"

        [commands.flat]
        desc  = "Flat"
        steps = ["flat"]

        [actions.flat]
        kind     = "shell"
        run      = "echo"
        requires = ["git-clean"]
    ''')
    # The function definition and its docstring mention the name; the test
    # checks that no module-level CALL to `register_shell_guard(...)` was
    # emitted. Module-level calls start at column zero.
    call_lines = [ln for ln in src.split("\n") if ln.startswith("register_shell_guard(")]
    assert call_lines == [], f"unexpected register_shell_guard calls: {call_lines}"


# ---------------------------------------------------------------------------
# Flat-action wrapping
# ---------------------------------------------------------------------------

def test_flat_action_requires_emits_prefix_check():
    src = _render('''
        project_name = "T"
        python_min   = "3.11"

        [commands.deploy]
        desc  = "Deploy"
        steps = ["deploy"]

        [actions.deploy]
        kind     = "shell"
        run      = "echo deploy"
        requires = ["git-clean", "git-pushed"]
    ''')
    body = _act_body(src, "deploy")
    assert 'run_requires(["git-clean", "git-pushed"])' in body
    # The check must precede the action's own work — verify line order.
    requires_line = next(i for i, ln in enumerate(body.split("\n")) if "run_requires(" in ln)
    work_line = next(i for i, ln in enumerate(body.split("\n")) if "run_cmd(" in ln)
    assert requires_line < work_line, "requires check must run before the action body"


def test_flat_action_without_requires_has_no_prefix_check():
    src = _render('''
        project_name = "T"
        python_min   = "3.11"

        [commands.deploy]
        desc  = "Deploy"
        steps = ["deploy"]

        [actions.deploy]
        kind = "shell"
        run  = "echo deploy"
    ''')
    body = _act_body(src, "deploy")
    assert "run_requires" not in body


# ---------------------------------------------------------------------------
# Multi-target dispatch
# ---------------------------------------------------------------------------

def test_multi_target_emits_target_requires_map_with_resolved_lists():
    src = _render('''
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
    body = _act_body(src, "preview")
    # cf inherits the parent default; devvit overrides to empty.
    assert '"cf": ["git-clean", "git-pushed"]' in body
    assert '"devvit": []' in body
    # Runtime dispatch reads the map for the picked target.
    assert "_target_requires.get(picked)" in body
    assert "run_requires(_reqs)" in body


def test_multi_target_without_any_requires_omits_dispatch():
    src = _render('''
        project_name = "T"
        python_min   = "3.11"

        [commands.preview]
        desc  = "Preview"
        steps = ["preview"]

        [actions.preview]
        kind = "multi-target"

        [actions.preview.cf]
        run = "echo cf"

        [actions.preview.devvit]
        run = "echo devvit"
    ''')
    body = _act_body(src, "preview")
    assert "_target_requires" not in body
    assert "run_requires" not in body


def test_multi_target_per_target_only_requires_no_parent():
    src = _render('''
        project_name = "T"
        python_min   = "3.11"

        [commands.preview]
        desc  = "Preview"
        steps = ["preview"]

        [actions.preview]
        kind = "multi-target"

        [actions.preview.cf]
        run      = "echo cf"
        requires = ["git-clean"]

        [actions.preview.devvit]
        run = "echo devvit"
    ''')
    body = _act_body(src, "preview")
    # cf gets its explicit list; devvit inherits the (empty) parent default.
    assert '"cf": ["git-clean"]' in body
    assert '"devvit": []' in body


# ---------------------------------------------------------------------------
# Module injection: bundle pulls in admin_lib/guards only when used
# ---------------------------------------------------------------------------

def test_guards_module_pulled_in_when_requires_used():
    src = _render('''
        project_name = "T"
        python_min   = "3.11"

        [commands.flat]
        desc  = "Flat"
        steps = ["flat"]

        [actions.flat]
        kind     = "shell"
        run      = "echo"
        requires = ["git-clean"]
    ''')
    assert "# === admin_lib.guards ===" in src
    assert "def run_requires" in src


def test_guards_module_pulled_in_when_user_guard_defined():
    # Even if no action references the guard via `requires`, declaring a
    # [guards.<name>] still implies the user wants the registration plumbing.
    src = _render('''
        project_name = "T"
        python_min   = "3.11"

        [guards.idle]
        run  = "exit 0"
        fail = "unused"

        [commands.flat]
        desc  = "Flat"
        steps = ["flat"]

        [actions.flat]
        kind = "shell"
        run  = "echo"
    ''')
    assert "# === admin_lib.guards ===" in src


def test_guards_module_omitted_when_unused():
    src = _render('''
        project_name = "T"
        python_min   = "3.11"

        [commands.flat]
        desc  = "Flat"
        steps = ["flat"]

        [actions.flat]
        kind = "shell"
        run  = "echo"
    ''')
    assert "# === admin_lib.guards ===" not in src
