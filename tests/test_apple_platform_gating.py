"""The apple archetype must offer only the platforms a manifest declares.

Regression cover for the bug where an iOS-only project (no `mac_scheme` in
`[apple]`) was still shown a `mac` target by `admin build`, `admin dev` and
`admin deploy`; picking it failed deep inside `deploy_mac` with
"apple.mac_scheme required" instead of never being offered.

Each test execs the archetype's real `kind = "python"` body in a stubbed
namespace and asserts on the menu handed to `pick_target`, or on the error a
platform the project does not have produces.
"""
from __future__ import annotations

import sys
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO))

from admin_lib.archetypes import load_archetype  # noqa: E402
from admin_lib.apple import apple_platforms, resolve_apple_platform  # noqa: E402

APPLE = load_archetype(REPO / "archetypes", "apple")

IOS_ONLY = {"project_path": "App.xcodeproj", "ios_scheme": "App"}
MAC_ONLY = {"project_path": "App.xcodeproj", "mac_scheme": "App"}
BOTH = {"project_path": "App.xcodeproj", "mac_scheme": "App", "ios_scheme": "App"}


class _Exit(Exception):
    """Stand-in for sys.exit so a test can assert the body bailed out."""


def _run(action, args, apple_cfg, server_cfg=None):
    """Exec an archetype action body; return what it did.

    Returns a dict with `menu` (what pick_target was offered, None if the body
    never got that far), `errors`, and `dispatched` (which platform handler ran).
    """
    seen = {"menu": None, "errors": [], "infos": [], "dispatched": []}

    def _pick(menu, prompt, order=None):
        seen["menu"] = dict(menu)
        return None  # cancel: the body returns without dispatching

    def _record(name):
        return lambda *a, **k: seen["dispatched"].append(name)

    class _Sys:
        def exit(self, code=0):
            raise _Exit(code)

    ns = {
        "args": list(args),
        "_APPLE_CONFIG": dict(apple_cfg),
        "_SERVER_CONFIG": dict(server_cfg or {}),
        "apple_platforms": apple_platforms,
        "resolve_apple_platform": resolve_apple_platform,
        "pick_target": _pick,
        "set_log_context": lambda *a, **k: None,
        "err": lambda m: seen["errors"].append(m),
        "info": lambda m: seen["infos"].append(m),
        "ok": lambda m: None,
        "sys": _Sys(),
        "os": __import__("os"),
    }
    for fn in ("build_mac", "build_ios", "build_tv", "build_server",
               "dev_mac", "dev_ios", "dev_tv", "dev_device", "dev_server",
               "deploy_mac", "deploy_ios", "deploy_server_docker", "deploy_bazzite_server"):
        ns[fn] = _record(fn)

    body = APPLE.actions[action]["run"]
    # The bodies use bare `return`, so they have to run inside a function.
    wrapped = "def _body():\n" + "".join("    " + line + "\n" for line in body.splitlines())
    exec(wrapped, ns)  # noqa: S102 — executing the archetype's own template is the point
    try:
        ns["_body"]()
    except _Exit:
        pass
    return seen


# --- the helper itself -------------------------------------------------------

def test_apple_platforms_lists_only_declared_schemes():
    assert apple_platforms(IOS_ONLY) == ["ios"]
    assert apple_platforms(MAC_ONLY) == ["mac"]
    assert apple_platforms(BOTH) == ["mac", "ios"]
    assert apple_platforms({}) == []
    print("OK: apple_platforms reports only the platforms a manifest declares")


def test_apple_platforms_only_filter():
    cfg = {"mac_scheme": "A", "ios_scheme": "A", "tv_scheme": "A"}
    assert apple_platforms(cfg) == ["mac", "ios", "tv"]
    assert apple_platforms(cfg, only=("mac", "ios")) == ["mac", "ios"]
    print("OK: apple_platforms `only` drops platforms a command cannot run")


def test_resolve_apple_platform_aliases():
    assert resolve_apple_platform("macos") == "mac"
    assert resolve_apple_platform("iPhone") == "ios"
    assert resolve_apple_platform("appletv") == "tv"
    assert resolve_apple_platform("android") is None
    print("OK: resolve_apple_platform maps CLI aliases to platform keys")


# --- the pickers -------------------------------------------------------------

def test_build_menu_omits_mac_on_ios_only_project():
    seen = _run("build", [], IOS_ONLY)
    assert seen["menu"] == {"ios": "iOS client"}, seen["menu"]
    print("OK: build menu on an iOS-only project offers ios and nothing else")


def test_dev_menu_omits_mac_on_ios_only_project():
    seen = _run("dev", [], IOS_ONLY)
    assert "mac" not in seen["menu"], seen["menu"]
    assert "ios" in seen["menu"] and "device" in seen["menu"], seen["menu"]
    print("OK: dev menu on an iOS-only project offers ios + device, not mac")


def test_deploy_menu_omits_mac_on_ios_only_project():
    seen = _run("deploy", [], IOS_ONLY)
    assert seen["menu"] == {"ios": "ios"}, seen["menu"]
    print("OK: deploy menu on an iOS-only project offers ios and nothing else")


def test_deploy_menu_omits_ios_on_mac_only_project():
    seen = _run("deploy", [], MAC_ONLY)
    assert seen["menu"] == {"mac": "mac"}, seen["menu"]
    print("OK: deploy menu on a mac-only project offers mac and nothing else")


def test_dev_device_absent_without_ios_scheme():
    seen = _run("dev", [], MAC_ONLY)
    assert "device" not in seen["menu"], seen["menu"]
    print("OK: dev's iOS-device target is absent when the project has no iOS scheme")


def test_deploy_menu_keeps_server_targets():
    server = {"dir": "server", "bazzite_cross_target": "x86_64-unknown-linux-gnu"}
    seen = _run("deploy", [], IOS_ONLY, server_cfg=server)
    assert set(seen["menu"]) == {"ios", "server", "bazzite"}, seen["menu"]
    print("OK: gating platforms leaves the server/bazzite deploy targets alone")


# --- explicit args -----------------------------------------------------------

def test_deploy_mac_arg_rejected_on_ios_only_project():
    seen = _run("deploy", ["mac"], IOS_ONLY)
    assert seen["dispatched"] == [], seen["dispatched"]
    assert any("no mac deploy target" in e for e in seen["errors"]), seen["errors"]
    assert any("ios" in e for e in seen["errors"]), seen["errors"]
    print("OK: `deploy mac` on an iOS-only project errors up front, naming what it does have")


def test_build_mac_arg_rejected_on_ios_only_project():
    seen = _run("build", ["mac"], IOS_ONLY)
    assert seen["dispatched"] == [], seen["dispatched"]
    assert any("no mac build target" in e for e in seen["errors"]), seen["errors"]
    print("OK: `build mac` on an iOS-only project errors up front")


def test_dev_mac_arg_rejected_on_ios_only_project():
    seen = _run("dev", ["mac"], IOS_ONLY)
    assert seen["dispatched"] == [], seen["dispatched"]
    assert any("no mac dev target" in e for e in seen["errors"]), seen["errors"]
    print("OK: `dev mac` on an iOS-only project errors up front")


def test_declared_platform_still_dispatches():
    assert _run("deploy", ["ios"], IOS_ONLY)["dispatched"] == ["deploy_ios"]
    assert _run("build", ["ios"], IOS_ONLY)["dispatched"] == ["build_ios"]
    assert _run("dev", ["ios"], IOS_ONLY)["dispatched"] == ["dev_ios"]
    assert _run("deploy", ["macos"], MAC_ONLY)["dispatched"] == ["deploy_mac"]
    print("OK: a platform the project does declare still runs")


def test_no_apple_scheme_at_all_errors():
    for action in ("build", "dev", "deploy"):
        seen = _run(action, [], {"project_path": "App.xcodeproj"})
        assert seen["menu"] is None, (action, seen["menu"])
        assert any("configured" in e for e in seen["errors"]), (action, seen["errors"])
    print("OK: an [apple] table with no scheme key errors instead of offering targets")


def test_unknown_arg_still_rejected():
    seen = _run("deploy", ["nintendo"], IOS_ONLY)
    assert any("unknown arg" in e for e in seen["errors"]), seen["errors"]
    print("OK: an unknown deploy arg is still an error")


if __name__ == "__main__":
    for name, fn in sorted(globals().items()):
        if name.startswith("test_") and callable(fn):
            fn()
