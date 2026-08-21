"""`admin check` advisories — well-formed manifests that still misbehave.

The React Native CLI advisory exists because term's dev-phone action ran
`npx react-native run-ios`, which opened a stray Terminal tab for Metro and
failed the build with "Signing for TermPhone requires a development team"
despite IOS_DEVELOPMENT_TEAM being exported — the RN CLI never reads [apple].
"""
from admin_lib.manifest import Command
from admin_lib.validate import lint_commands


def _cmd(name, run="", targets=None):
    return Command(name=name, run=run, desc="", targets=targets or {})


def test_flags_react_native_run_ios():
    msgs = lint_commands({"dev-phone": _cmd(
        "dev-phone", run="cd apps/phone && npx react-native run-ios")})
    assert len(msgs) == 1
    assert "react-native run-ios" in msgs[0]
    assert "react_native" in msgs[0]


def test_flags_expo_and_android_variants():
    cmds = {
        "a": _cmd("a", run="npx react-native run-android"),
        "b": _cmd("b", run="npx expo run:ios"),
        "c": _cmd("c", run="npx expo run:android"),
    }
    assert len(lint_commands(cmds)) == 3


def test_flags_a_multi_target_body():
    msgs = lint_commands({"dev": _cmd(
        "dev", targets={"phone": "npx react-native run-ios", "web": "npm run dev"})})
    assert len(msgs) == 1
    assert "'dev'" in msgs[0]


def test_silent_on_the_react_native_module_path():
    body = 'ensure_metro_for(_rn)\ndev_ios(apple_config_for(_rn, _APPLE_CONFIG))'
    assert lint_commands({"dev-phone": _cmd("dev-phone", run=body)}) == []


def test_silent_on_unrelated_commands():
    assert lint_commands({"dev-web": _cmd("dev-web", run="cd apps/mobile && npm run dev")}) == []
