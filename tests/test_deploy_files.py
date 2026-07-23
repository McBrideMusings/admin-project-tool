"""Tests for the [deploy] mirror-directory sync.

Covers the three layers the feature spans: `core.sync`'s delete/dry-run flags,
`remote.deploy_files` reading the manifest table, and the manifest validator
that makes `admin check` reject a malformed [deploy].
"""
from __future__ import annotations

import pytest

from admin_lib import core, remote
from admin_lib.manifest import ManifestError, parse


# --- core.sync flags -------------------------------------------------------

def test_sync_remote_omits_delete_when_disabled(tmp_path, monkeypatch):
    src = tmp_path / "src"
    src.mkdir()
    seen = []
    monkeypatch.setattr(core, "run_cmd", lambda cmd, **kw: seen.append(cmd) or 0)

    core.sync(str(src), "root@host:/mnt/appdata", delete=False)

    assert "--delete" not in seen[0]
    assert seen[0].startswith("rsync -avz")


def test_sync_remote_keeps_delete_by_default(tmp_path, monkeypatch):
    src = tmp_path / "src"
    src.mkdir()
    seen = []
    monkeypatch.setattr(core, "run_cmd", lambda cmd, **kw: seen.append(cmd) or 0)

    core.sync(str(src), "root@host:/mnt/appdata")

    assert "--delete" in seen[0]


def test_sync_dry_run_passes_flag_and_changes_nothing(tmp_path, monkeypatch):
    src = tmp_path / "src"
    src.mkdir()
    (src / "a.txt").write_text("hello")
    seen = []
    monkeypatch.setattr(core, "run_cmd", lambda cmd, **kw: seen.append(cmd) or 0)

    core.sync(str(src), "root@host:/mnt/appdata", dry_run=True)

    assert "--dry-run" in seen[0]


def test_sync_local_dry_run_does_not_write(tmp_path):
    src = tmp_path / "src"
    src.mkdir()
    (src / "a.txt").write_text("hello")
    dest = tmp_path / "dest"

    rc = core.sync(str(src), str(dest), dry_run=True)

    assert rc == 0
    assert not dest.exists()


def test_sync_local_keeps_extraneous_when_delete_disabled(tmp_path):
    src = tmp_path / "src"
    src.mkdir()
    (src / "config.yaml").write_text("new")
    dest = tmp_path / "dest"
    dest.mkdir()
    (dest / "state.json").write_text("runtime state")

    core.sync(str(src), str(dest), delete=False)

    assert (dest / "state.json").read_text() == "runtime state"
    assert (dest / "config.yaml").read_text() == "new"


# --- remote.deploy_files ---------------------------------------------------

def test_deploy_files_builds_remote_target(tmp_path, monkeypatch):
    src = tmp_path / "appdata"
    src.mkdir()
    seen = {}

    def fake_sync(s, d, **kw):
        seen.update(src=s, dest=d, **kw)
        return 0

    monkeypatch.setattr(core, "sync", fake_sync)
    monkeypatch.setattr(remote, "_is_local_host", lambda h: False)

    rc = remote.deploy_files({
        "src": str(src),
        "dest": "/mnt/user/appdata/app",
        "host": "root@tower",
    })

    assert rc == 0
    assert seen["dest"] == "root@tower:/mnt/user/appdata/app"
    assert seen["delete"] is False


def test_deploy_files_drops_host_when_running_on_the_target(tmp_path, monkeypatch):
    src = tmp_path / "appdata"
    src.mkdir()
    seen = {}
    monkeypatch.setattr(core, "sync", lambda s, d, **kw: seen.update(dest=d) or 0)
    monkeypatch.setattr(remote, "_is_local_host", lambda h: True)

    remote.deploy_files({"src": str(src), "dest": "/mnt/app", "host": "root@tower"})

    assert seen["dest"] == "/mnt/app"


def test_deploy_files_resolves_env_placeholders(tmp_path, monkeypatch):
    src = tmp_path / "appdata"
    src.mkdir()
    monkeypatch.setenv("TEST_APPDATA", "/mnt/user/appdata/thing")
    seen = {}
    monkeypatch.setattr(core, "sync", lambda s, d, **kw: seen.update(dest=d) or 0)
    monkeypatch.setattr(remote, "_is_local_host", lambda h: False)

    remote.deploy_files({
        "src": str(src),
        "dest": "${TEST_APPDATA}",
        "host": "root@tower",
    })

    assert seen["dest"] == "root@tower:/mnt/user/appdata/thing"


def test_deploy_files_rejects_unknown_key(tmp_path):
    src = tmp_path / "appdata"
    src.mkdir()
    assert remote.deploy_files({"src": str(src), "dest": "/x", "delet": True}) == 1


def test_deploy_files_requires_src_and_dest():
    assert remote.deploy_files({"dest": "/x"}) == 1
    assert remote.deploy_files({}) == 1


def test_deploy_files_errors_when_src_missing(tmp_path):
    assert remote.deploy_files({"src": str(tmp_path / "nope"), "dest": "/x"}) == 1


# --- deploy_dispatch routing -----------------------------------------------

@pytest.fixture
def dispatch(monkeypatch):
    """deploy_dispatch with every side effect replaced by a call recorder."""
    from admin_lib import docker, unraid

    calls = []
    monkeypatch.setattr(remote, "deploy_files",
                        lambda cfg, dry_run=False: calls.append(("files", dry_run)) or 0)
    monkeypatch.setattr(unraid, "ensure_docker_template", lambda cfg: calls.append(("template",)))
    monkeypatch.setattr(docker, "docker_build",
                        lambda *a, **kw: calls.append(("build",)) or 0)
    monkeypatch.setattr(docker, "remote_recreate_container",
                        lambda *a, **kw: calls.append(("recreate", kw.get("dry_run"))) or 0)
    monkeypatch.setenv("APP_IMAGE", "test-app")
    monkeypatch.setenv("UNRAID_HOST", "tower")
    return docker.deploy_dispatch, calls


def test_dispatch_files_skips_the_image(dispatch):
    deploy_dispatch, calls = dispatch
    assert deploy_dispatch(["files"], deploy_cfg={"src": "d", "dest": "/x"}) == 0
    assert [c[0] for c in calls] == ["files"]


def test_dispatch_all_syncs_before_rebuilding(dispatch):
    deploy_dispatch, calls = dispatch
    assert deploy_dispatch(["all"], deploy_cfg={"src": "d", "dest": "/x"}) == 0
    assert [c[0] for c in calls] == ["files", "template", "build", "recreate"]


def test_dispatch_files_without_deploy_table_errors(dispatch):
    deploy_dispatch, calls = dispatch
    assert deploy_dispatch(["files"], deploy_cfg=None) == 1
    assert calls == []


def test_dispatch_all_aborts_the_image_when_the_sync_fails(dispatch, monkeypatch):
    deploy_dispatch, calls = dispatch
    monkeypatch.setattr(remote, "deploy_files", lambda cfg, dry_run=False: 1)
    assert deploy_dispatch(["all"], deploy_cfg={"src": "d", "dest": "/x"}) == 1
    assert calls == []


def test_dispatch_dry_run_never_builds(dispatch):
    deploy_dispatch, calls = dispatch
    assert deploy_dispatch(["all", "--dry-run"], deploy_cfg={"src": "d", "dest": "/x"}) == 0
    assert [c[0] for c in calls] == ["files", "recreate"]
    assert calls[0][1] is True   # sync told it was a dry run
    assert calls[1][1] is True   # so was the container recreate


# --- manifest validation ---------------------------------------------------

def _manifest(body: str, tmp_path):
    import tomllib
    p = tmp_path / "admin.toml"
    p.write_text('project_name = "T"\n' + body)
    return parse(tomllib.loads(p.read_text()), source=str(p))


def test_manifest_accepts_valid_deploy_table(tmp_path):
    m = _manifest(
        '[deploy]\n'
        'src = "deploy/appdata"\n'
        'dest = "/mnt/user/appdata/app"\n'
        'host = "root@tower"\n'
        'exclude = ["*.log"]\n'
        'delete = false\n',
        tmp_path,
    )
    assert m.deploy["src"] == "deploy/appdata"


def test_manifest_rejects_unknown_deploy_key(tmp_path):
    with pytest.raises(ManifestError, match="unknown key"):
        _manifest('[deploy]\nsrc = "a"\ndest = "/b"\nhsot = "x"\n', tmp_path)


def test_manifest_requires_deploy_src(tmp_path):
    with pytest.raises(ManifestError, match=r"\[deploy\].src"):
        _manifest('[deploy]\ndest = "/b"\n', tmp_path)


def test_manifest_rejects_non_bool_delete(tmp_path):
    with pytest.raises(ManifestError, match="true or false"):
        _manifest('[deploy]\nsrc = "a"\ndest = "/b"\ndelete = "yes"\n', tmp_path)


def test_dispatch_unknown_target_refuses_to_deploy(dispatch):
    """A typo must not fall through to a full image deploy."""
    deploy_dispatch, calls = dispatch
    assert deploy_dispatch(["fiels"], deploy_cfg={"src": "d", "dest": "/x"}) == 1
    assert calls == []


def test_dispatch_leading_flag_still_means_image(dispatch):
    """`admin deploy --dry-run` keeps working — a flag is not a sub-target."""
    deploy_dispatch, calls = dispatch
    assert deploy_dispatch(["--dry-run"], deploy_cfg=None) == 0
    assert [c[0] for c in calls] == ["recreate"]


def test_dispatch_bare_asks_when_there_is_a_real_choice(dispatch, monkeypatch):
    """With a [deploy] table and a TTY, bare `admin deploy` asks rather than
    assuming — same convention as `admin logs`."""
    from admin_lib import docker

    deploy_dispatch, calls = dispatch
    monkeypatch.setattr(docker.sys.stdout, "isatty", lambda: True)
    monkeypatch.setattr(docker, "pick_target", lambda menu, prompt: "files")

    assert deploy_dispatch([], deploy_cfg={"src": "d", "dest": "/x"}) == 0
    assert [c[0] for c in calls] == ["files"]


def test_dispatch_bare_skips_the_picker_without_a_deploy_table(dispatch, monkeypatch):
    """One thing to deploy means no question to ask — unchanged behaviour."""
    from admin_lib import docker

    deploy_dispatch, calls = dispatch
    monkeypatch.setattr(docker.sys.stdout, "isatty", lambda: True)
    monkeypatch.setattr(docker, "pick_target", lambda menu, prompt: pytest.fail("asked"))

    assert deploy_dispatch([], deploy_cfg=None) == 0
    assert [c[0] for c in calls] == ["template", "build", "recreate"]


def test_dispatch_bare_without_a_tty_deploys_the_image(dispatch, monkeypatch):
    """A deploy from a script means ship the code; nobody is there to answer."""
    from admin_lib import docker

    deploy_dispatch, calls = dispatch
    monkeypatch.setattr(docker.sys.stdout, "isatty", lambda: False)
    monkeypatch.setattr(docker, "pick_target", lambda menu, prompt: pytest.fail("asked"))

    assert deploy_dispatch([], deploy_cfg={"src": "d", "dest": "/x"}) == 0
    assert [c[0] for c in calls] == ["template", "build", "recreate"]


def test_dispatch_cancelled_picker_does_nothing(dispatch, monkeypatch):
    from admin_lib import docker

    deploy_dispatch, calls = dispatch
    monkeypatch.setattr(docker.sys.stdout, "isatty", lambda: True)
    monkeypatch.setattr(docker, "pick_target", lambda menu, prompt: None)

    assert deploy_dispatch([], deploy_cfg={"src": "d", "dest": "/x"}) == 0
    assert calls == []
