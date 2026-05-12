"""Tests for gen/bundler.strip_unreachable.

Covers the AST reachability pass: hard-keep symbols, cmd_* entry points,
string-literal dynamic dispatch, module-level statement references, and
the line-range removal that drops unreachable defs while preserving
formatting around them.
"""
from __future__ import annotations

import sys
import textwrap
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO))

import tempfile

from gen.bundler import BundlerError, inline_admin_lib, strip_unreachable  # noqa: E402


def _strip(src: str):
    return strip_unreachable(textwrap.dedent(src))


def _assert_raises_bundler_error(callable_, match: str):
    try:
        callable_()
    except BundlerError as e:
        assert match in str(e), f"expected {match!r} in {e!r}"
        return
    raise AssertionError(f"expected BundlerError matching {match!r}")


def test_rejects_from_dot_import_module():
    with tempfile.TemporaryDirectory() as d:
        d = Path(d)
        (d / "foo.py").write_text("from . import docker\n")
        (d / "docker.py").write_text("x = 1\n")
        _assert_raises_bundler_error(
            lambda: inline_admin_lib(d, ["foo"]),
            "from . import docker",
        )


def test_rejects_from_dot_import_module_as_alias():
    with tempfile.TemporaryDirectory() as d:
        d = Path(d)
        (d / "foo.py").write_text("from . import docker as _d\n_d.thing()\n")
        (d / "docker.py").write_text("def thing():\n    pass\n")
        _assert_raises_bundler_error(
            lambda: inline_admin_lib(d, ["foo"]),
            "docker as _d",
        )


def test_allows_from_dot_module_import_name():
    with tempfile.TemporaryDirectory() as d:
        d = Path(d)
        (d / "foo.py").write_text("from .docker import thing\n")
        (d / "docker.py").write_text("def thing():\n    pass\n")
        inline_admin_lib(d, ["foo", "docker"])


def test_drops_unreferenced_function():
    src = """
        def used():
            return 1

        def cmd_main(args):
            return used()

        def dead():
            return 2
    """
    out, n, dropped = _strip(src)
    assert n == 1
    assert dropped == ["dead"]
    assert "def dead" not in out
    assert "def used" in out
    assert "def cmd_main" in out


def test_keeps_hard_keep_symbols():
    src = """
        def boot(name):
            return name

        def dispatch_target(*a, **k):
            pass

        def random_helper():
            return 0
    """
    out, n, dropped = _strip(src)
    assert "def boot" in out
    assert "def dispatch_target" in out
    assert dropped == ["random_helper"]


def test_string_literal_keeps_symbol():
    # A function whose name appears as a string literal somewhere is reachable
    # via dynamic dispatch (getattr, registry table).
    src = """
        def cmd_main(args):
            handler = "do_thing"
            return handler

        def do_thing():
            return 1

        def truly_dead():
            return 2
    """
    out, n, dropped = _strip(src)
    assert "def do_thing" in out
    assert "def truly_dead" not in out
    assert dropped == ["truly_dead"]


def test_module_level_call_keeps_symbol():
    # register_cleanup(_cleanup_thing) at module level keeps _cleanup_thing.
    src = """
        def cmd_main(args):
            return 1

        def _cleanup_thing():
            pass

        def stranger():
            pass

        register_cleanup(_cleanup_thing)
    """
    out, n, dropped = _strip(src)
    assert "_cleanup_thing" in out
    assert "stranger" not in out
    assert "stranger" in dropped


def test_decorator_dropped_with_function():
    src = """
        def keep_me(args):
            return 1

        @some_decorator
        def cmd_used(args):
            return keep_me(args)

        @some_decorator
        def cmd_dead_via_decorator(args):
            return 0
    """
    out, n, dropped = _strip(src)
    # cmd_* are entry points, so neither cmd_used nor cmd_dead_via_decorator
    # should be dropped. This test exercises that decorators are *kept* with
    # the function, not stripped separately.
    assert "@some_decorator" in out
    assert "def cmd_used" in out
    assert "def cmd_dead_via_decorator" in out
    assert dropped == []


def test_transitive_reachability():
    src = """
        def cmd_main(args):
            return a()

        def a():
            return b()

        def b():
            return c()

        def c():
            return 1

        def orphan():
            return 0
    """
    out, n, dropped = _strip(src)
    assert dropped == ["orphan"]
    for keep in ("def a", "def b", "def c"):
        assert keep in out


def test_drops_unreferenced_constant():
    src = """
        DEAD_CONST = [1, 2, 3]
        LIVE_CONST = "hello"

        def cmd_main(args):
            return LIVE_CONST
    """
    out, n, dropped = _strip(src)
    assert "DEAD_CONST" not in out
    assert "LIVE_CONST" in out
    assert "DEAD_CONST" in dropped


def test_no_changes_when_all_reachable():
    src = """
        def cmd_main(args):
            return helper()

        def helper():
            return 1
    """
    out, n, dropped = _strip(src)
    assert n == 0
    assert dropped == []


if __name__ == "__main__":
    tests = [v for k, v in sorted(globals().items()) if k.startswith("test_")]
    failed = 0
    for fn in tests:
        try:
            fn()
            print(f"OK  {fn.__name__}")
        except Exception as e:
            print(f"FAIL {fn.__name__}: {e}")
            failed += 1
    if failed:
        sys.exit(failed)
    else:
        print(f"\nAll {len(tests)} tests passed.")
