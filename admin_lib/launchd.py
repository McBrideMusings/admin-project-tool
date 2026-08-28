"""Always-on user-service management, driven by the `[launchd]` table in
admin.toml — macOS launchd locally, Linux systemd --user locally, or either
one on a remote host over SSH via `service_deploy_remote`.

Any project that ships a long-running process the developer wants alive at
login (or wants running as standing infra on another machine) declares
`[launchd]` and wires a `service` command; the plist/unit is generated here
rather than committed, because every value in one is machine-specific (the
binary lives wherever the toolchain installed it, the log dir is under $HOME).

Orthogonal to the stack archetypes on purpose. A Go supervisor, a Python daemon
and a shell script all register with launchd/systemd identically, so this is a
module a project opts into (`modules = ["launchd"]`), not an archetype.

The `program` key is always the INSTALLED path the service runs from. An
optional `source` key names a freshly built binary elsewhere; when set,
`install` atomically swaps it into `program` (see `_atomic_install_binary`)
before touching the service — the safe way to redeploy a binary that may
currently be running. Without `source`, `program` is assumed already built in
place, exactly as before this key existed.

For a remote host, `service_deploy_remote` picks the right architecture out of
a local dist/ directory, pushes the binary plus the portable
`resources/install_service_remote.sh` installer over scp, and runs it there —
no Python or the admin tool needs to be present on the remote host.

Five things here are less obvious than they look, and all were paid for once:

Minimal environment and login-shell-only dotenv files. launchd hands an agent
`/usr/bin:/bin:/usr/sbin:/sbin` and HOME and nothing else — no secrets a
developer keeps in `~/.claude/.env` as `export FOO=bar` lines sourced by
`~/.zshrc` for *interactive* shells only. A non-interactive `admin deploy`
(the shape every agent and CI run uses) sees zero of those vars, so
`env = { FOO = "${FOO}" }` in `[launchd]` — which resolves through
`resolve_env` against `os.environ` — silently resolves to nothing, or hard-
fails if the var has no default. `env_file` + `env_file_keys` exist to read
the dotenv file directly at generation time instead of trusting the shell to
have sourced it, tolerating the `export ` prefix that a plain `KEY=value`
parser (and `core._load_dotenv`, which only reads `<project_root>/.env`
anyway) rejects.

PATH. launchd hands an agent `/usr/bin:/bin:/usr/sbin:/sbin` and nothing else. A
process that shells out to anything installed by Homebrew, a language toolchain,
or the user's own bin directory starts fine under that PATH and then fails on its
first real unit of work — which reads as the program being broken rather than the
environment being empty. `inherit_path` (default true) writes the PATH of the
shell running `admin` into the plist, which is the developer's real login PATH.

Re-registration. macOS 13+ Background Task Management records every launchd item
and posts an "App Background Activity" notification the first time it sees one.
It keys off the plist being installed, not off the process starting — so a deploy
that rewrites an unchanged plist and does bootout+bootstrap produces a fresh
notification on every single deploy. `install` therefore renders the plist to a
string first and compares it with what is already on disk: byte-identical and
already loaded means the only correct action is `launchctl kickstart -k`, which
swaps the running process for the newly built binary and touches no BTM record.
The plist is written and re-registered only when it genuinely changed, when it
was missing, or when the agent is not loaded — and a notification then is right,
because it really is a new item.

Binary swap under a live daemon. `cp`/write-through-inode invalidates a running
daemon's mapped text pages: macOS refuses to spawn the new file with
`OS_REASON_CODESIGNING` even though `codesign -v` on it passes; Linux hits
ETXTBSY. `_atomic_install_binary` writes a sibling temp file (its own inode) and
renames it into place, so nothing currently mapped is touched, then ad-hoc
re-signs on macOS so the kernel accepts the swapped-in file regardless of how it
was produced.
"""

import filecmp
import os
import platform
import plistlib
import shlex
import shutil
import subprocess

from .core import err, info, ok, resolve_env, warn

_KNOWN_KEYS = {
    "label", "program", "source", "args", "env", "inherit_path", "inherit_home",
    "stdout", "stderr", "working_dir", "keep_alive", "run_at_load",
    "throttle", "process_type", "nice", "start_interval",
    "env_file", "env_file_keys",
}

_USAGE = """usage: admin service <install|uninstall|status|restart|stop|start>

  install    write the LaunchAgent plist and load it (idempotent)
  uninstall  unload the agent and remove the plist
  status     report whether the agent is loaded, its pid and last exit code
  restart    kickstart the agent, replacing the running process
  stop       unload the agent but leave the plist in place
  start      load an already-installed agent"""


# ---------------------------------------------------------------------------
# Config
# ---------------------------------------------------------------------------

def _cfg_error(msg):
    err(f"[launchd]: {msg}")
    raise SystemExit(1)


def _resolve(value):
    """Expand ${VAR} through the manifest's own resolver, recursively."""
    if isinstance(value, str):
        return resolve_env(value)
    if isinstance(value, list):
        return [_resolve(v) for v in value]
    if isinstance(value, dict):
        return {k: _resolve(v) for k, v in value.items()}
    return value


def _parse_env_file(path):
    """Read KEY=value lines out of a dotenv-style file (e.g. ~/.claude/.env).

    Export-prefix-tolerant and quote-stripping, unlike `core._load_dotenv`
    (which only reads `<project_root>/.env` and whose `_ENV_KEY_RE` rejects a
    key of `export FOO`) and `docker.py`'s `_parse_env_text` (which does
    neither). A missing file is not fatal — mirrors push.go's own posture
    that a bad or absent value degrades a feature, never the process — it
    just means the caller's allowlisted keys resolve to nothing.
    """
    if not path or not os.path.isfile(path):
        if path:
            warn(f"[launchd]: env_file {path!r} not found — no values loaded from it")
        return {}
    out = {}
    with open(path, "r", encoding="utf-8") as fh:
        for line in fh:
            line = line.strip()
            if not line or line.startswith("#"):
                continue
            if line.startswith("export "):
                line = line[len("export "):].lstrip()
            if "=" not in line:
                continue
            key, _, value = line.partition("=")
            key = key.strip()
            value = value.strip()
            if len(value) >= 2 and value[0] == value[-1] and value[0] in "\"'":
                value = value[1:-1]
            if key:
                out[key] = value
    return out


def _read_config(cfg):
    """Validate and normalise the [launchd] table into the values used below."""
    cfg = cfg or {}
    unknown = set(cfg) - _KNOWN_KEYS
    if unknown:
        _cfg_error(
            f"unknown keys: {sorted(unknown)}, valid: {sorted(_KNOWN_KEYS)}"
        )
    label = _resolve(cfg.get("label") or "")
    program = _resolve(cfg.get("program") or "")
    source = _resolve(cfg.get("source") or "")
    if not label:
        _cfg_error("'label' is required (e.g. com.example.myserver)")
    if not program:
        _cfg_error("'program' is required (absolute path to the executable)")
    program = os.path.expanduser(program)
    if not os.path.isabs(program):
        _cfg_error(f"'program' must be an absolute path, got {program!r}")
    if source:
        source = os.path.expanduser(source)

    out = {
        "label": label,
        "program": program,
        "source": source,
        "args": [str(a) for a in _resolve(cfg.get("args") or [])],
        "env": {str(k): str(v) for k, v in (_resolve(cfg.get("env") or {})).items()},
        "inherit_path": bool(cfg.get("inherit_path", True)),
        "inherit_home": bool(cfg.get("inherit_home", True)),
        "keep_alive": bool(cfg.get("keep_alive", True)),
        "run_at_load": bool(cfg.get("run_at_load", True)),
        "throttle": cfg.get("throttle"),
        "process_type": _resolve(cfg.get("process_type") or "Background"),
        "nice": cfg.get("nice"),
        "start_interval": cfg.get("start_interval"),
    }
    for key in ("stdout", "stderr", "working_dir"):
        value = _resolve(cfg.get(key) or "")
        out[key] = os.path.expanduser(value) if value else ""

    # env_file/env_file_keys: lift an ALLOWLISTED set of names out of a dotenv
    # file (e.g. ~/.claude/.env) at generation time, rather than trusting the
    # deploying shell to have sourced it — see the module docstring. The
    # allowlist is mandatory: without env_file_keys, nothing is lifted, since
    # a dotenv file used this way typically holds many unrelated secrets that
    # must never end up in a LaunchAgent plist wholesale.
    env_file = _resolve(cfg.get("env_file") or "")
    env_file_keys = [str(k) for k in (cfg.get("env_file_keys") or [])]
    if env_file and env_file_keys:
        loaded = _parse_env_file(os.path.expanduser(env_file))
        lifted = {k: loaded[k] for k in env_file_keys if loaded.get(k)}
        # Explicit `env` wins over anything lifted from the file.
        out["env"] = {**lifted, **out["env"]}

    return out


def _plist_path(label):
    return os.path.join(os.path.expanduser("~/Library/LaunchAgents"), f"{label}.plist")


def _domain():
    return f"gui/{os.getuid()}"


# ---------------------------------------------------------------------------
# Rendering
# ---------------------------------------------------------------------------

def _render(c):
    """Return the plist bytes for this config. Pure — no filesystem writes."""
    env = dict(c["env"])
    if c["inherit_path"]:
        env.setdefault("PATH", os.environ.get("PATH", ""))
    if c["inherit_home"]:
        env.setdefault("HOME", os.path.expanduser("~"))

    doc = {
        "Label": c["label"],
        "ProgramArguments": [c["program"], *c["args"]],
        "RunAtLoad": c["run_at_load"],
        "KeepAlive": c["keep_alive"],
    }
    if env:
        doc["EnvironmentVariables"] = env
    if c["throttle"] is not None:
        doc["ThrottleInterval"] = int(c["throttle"])
    if c["start_interval"] is not None:
        doc["StartInterval"] = int(c["start_interval"])
    if c["nice"] is not None:
        doc["Nice"] = int(c["nice"])
    if c["working_dir"]:
        doc["WorkingDirectory"] = c["working_dir"]
    if c["stdout"]:
        doc["StandardOutPath"] = c["stdout"]
    if c["stderr"]:
        doc["StandardErrorPath"] = c["stderr"]
    if c["process_type"]:
        doc["ProcessType"] = c["process_type"]
    return plistlib.dumps(doc, sort_keys=True)


# ---------------------------------------------------------------------------
# launchctl
# ---------------------------------------------------------------------------

def _launchctl(*args, check=True, quiet=False):
    r = subprocess.run(
        ["launchctl", *args], capture_output=True, text=True,
    )
    if r.returncode != 0 and check and not quiet:
        detail = (r.stderr or r.stdout).strip()
        err(f"launchctl {' '.join(args)} failed ({r.returncode}): {detail}")
        raise SystemExit(r.returncode)
    return r


def _is_loaded(label):
    return _launchctl("print", f"{_domain()}/{label}", check=False).returncode == 0


def _bootout(label, quiet=True):
    _launchctl("bootout", f"{_domain()}/{label}", check=False, quiet=quiet)


def _wait_unloaded(label, tries=25, interval=0.2):
    """Block until `label` is really gone from launchd's tables.

    `bootout` of a KeepAlive job is async — the old process is still tearing
    down when it returns. Bootstrapping (or swapping the binary) immediately
    races it: a bootstrap can fail with EIO, and a binary swap can still land
    under the old running image. Same shape on the systemd side, though
    `systemctl stop` is synchronous there so it isn't needed.
    """
    import time
    for _ in range(tries):
        if not _is_loaded(label):
            return
        time.sleep(interval)


def _files_identical(a, b):
    try:
        return filecmp.cmp(a, b, shallow=False)
    except OSError:
        return False


def _atomic_install_binary(src, dest):
    """Copy `src` over `dest`, which may be a currently-running program.

    See the module docstring for why this can't be a plain `cp`/`shutil.copy`.
    """
    tmp = f"{dest}.new.{os.getpid()}"
    try:
        shutil.copy2(src, tmp)
        os.chmod(tmp, 0o755)
        if platform.system() == "Darwin" and shutil.which("codesign"):
            r = subprocess.run(
                ["codesign", "--force", "--sign", "-", tmp],
                capture_output=True, text=True,
            )
            if r.returncode != 0:
                err(f"could not ad-hoc sign {tmp}: {(r.stderr or r.stdout).strip()}")
                raise SystemExit(1)
        os.replace(tmp, dest)
    finally:
        if os.path.exists(tmp):
            os.remove(tmp)


# ---------------------------------------------------------------------------
# Verbs (macOS / launchd)
# ---------------------------------------------------------------------------

def _install(c):
    source = c["source"]
    binary_changed = True
    if source:
        if not os.path.exists(source):
            err(f"{source} does not exist — build it first")
            raise SystemExit(1)
        binary_changed = not (
            os.path.exists(c["program"]) and _files_identical(source, c["program"])
        )

    plist = _plist_path(c["label"])
    desired = _render(c)
    current = None
    if os.path.exists(plist):
        with open(plist, "rb") as fh:
            current = fh.read()

    if source and not binary_changed and current == desired and _is_loaded(c["label"]):
        ok(f"{os.path.basename(c['program'])} unchanged and already loaded — nothing to do")
        return

    if source and binary_changed:
        # STOP before the binary is replaced: swapping the file under a live
        # daemon is what corrupts it (see the module docstring), so the old
        # process must be fully gone before install_binary touches its path.
        if _is_loaded(c["label"]):
            _bootout(c["label"])
            _wait_unloaded(c["label"])
        os.makedirs(os.path.dirname(c["program"]), exist_ok=True)
        _atomic_install_binary(source, c["program"])

    if not os.path.exists(c["program"]):
        err(f"{c['program']} does not exist — build and install the binary first")
        raise SystemExit(1)
    if not os.access(c["program"], os.X_OK):
        err(f"{c['program']} is not executable")
        raise SystemExit(1)

    for path in (c["stdout"], c["stderr"]):
        if path:
            os.makedirs(os.path.dirname(path), exist_ok=True)

    os.makedirs(os.path.dirname(plist), exist_ok=True)

    if current == desired and _is_loaded(c["label"]):
        # Unchanged and already registered: restarting in place picks up a
        # freshly built binary without creating a new Background Task
        # Management record (see the module docstring).
        _launchctl("kickstart", "-k", f"{_domain()}/{c['label']}")
        ok(f"restarted {c['label']} (plist unchanged)")
        info(f"  binary: {c['program']}")
        if c["stdout"]:
            info(f"  log:    {c['stdout']}")
        return

    with open(plist, "wb") as fh:
        fh.write(desired)
    # bootout first so a changed plist replaces the running agent instead of
    # failing with "service already loaded".
    _bootout(c["label"])
    _launchctl("bootstrap", _domain(), plist)
    _launchctl("enable", f"{_domain()}/{c['label']}", check=False)
    verb = "reloaded" if current is not None else "installed and loaded"
    ok(f"{verb} {c['label']}")
    info(f"  binary: {c['program']}")
    if c["stdout"]:
        info(f"  log:    {c['stdout']}")
    if current is None:
        warn(
            "macOS will show a one-time 'App Background Activity' notification "
            "for this agent; it is listed under Login Items & Extensions."
        )


def _uninstall(c):
    plist = _plist_path(c["label"])
    _bootout(c["label"])
    if os.path.exists(plist):
        os.remove(plist)
    ok(f"unloaded and removed {c['label']}")


def _stop(c):
    _bootout(c["label"])
    ok(f"unloaded {c['label']} (plist left in place — `admin service start` to bring it back)")


def _start(c):
    plist = _plist_path(c["label"])
    if not os.path.exists(plist):
        err(f"{plist} does not exist — run `admin service install` first")
        raise SystemExit(1)
    _launchctl("bootstrap", _domain(), plist)
    ok(f"loaded {c['label']}")


def _restart(c):
    _launchctl("kickstart", "-k", f"{_domain()}/{c['label']}")
    ok(f"restarted {c['label']}")


def _status(c):
    plist = _plist_path(c["label"])
    if not os.path.exists(plist):
        info(f"not installed (no {plist})")
        return
    r = _launchctl("print", f"{_domain()}/{c['label']}", check=False)
    if r.returncode != 0:
        info(f"installed but not loaded ({plist})")
        return
    # `launchctl print` is verbose; these three lines answer "is it up, as what
    # pid, and did it die". Only the top level counts — the nested dictionaries
    # (endpoints, domain) carry their own `state = active` lines, and printing
    # those alongside the service's own state reads as three conflicting answers
    # to one question. launchctl indents top-level keys with exactly one tab.
    wanted = ("state = ", "pid = ", "last exit code = ")
    for line in r.stdout.splitlines():
        if line.startswith("\t") and not line.startswith("\t\t"):
            if line.lstrip().startswith(wanted):
                print(line.strip())


_VERBS = {
    "install": _install,
    "uninstall": _uninstall,
    "status": _status,
    "restart": _restart,
    "stop": _stop,
    "start": _start,
}


# ---------------------------------------------------------------------------
# Verbs (Linux / systemd --user)
# ---------------------------------------------------------------------------

def _systemd_unit_path(label):
    return os.path.join(os.path.expanduser("~/.config/systemd/user"), f"{label}.service")


def _render_systemd_unit(c):
    env = dict(c["env"])
    if c["inherit_path"]:
        env.setdefault("PATH", os.environ.get("PATH", ""))

    exec_line = " ".join(shlex.quote(p) for p in (c["program"], *c["args"]))
    lines = [
        "[Unit]", f"Description={c['label']}", "After=network-online.target", "",
        "[Service]", f"ExecStart={exec_line}",
    ]
    for k, v in env.items():
        lines.append(f"Environment={k}={v}")
    if c["working_dir"]:
        lines.append(f"WorkingDirectory={c['working_dir']}")
    if c["stdout"]:
        lines.append(f"StandardOutput=append:{c['stdout']}")
    if c["stderr"]:
        lines.append(f"StandardError=append:{c['stderr']}")
    if c["keep_alive"]:
        lines.append("Restart=always")
        lines.append(f"RestartSec={int(c['throttle']) if c['throttle'] is not None else 5}")
    lines += ["", "[Install]", "WantedBy=default.target"]
    return ("\n".join(lines) + "\n").encode()


def _systemctl(*args, check=True, quiet=False):
    r = subprocess.run(["systemctl", "--user", *args], capture_output=True, text=True)
    if r.returncode != 0 and check and not quiet:
        detail = (r.stderr or r.stdout).strip()
        err(f"systemctl --user {' '.join(args)} failed ({r.returncode}): {detail}")
        raise SystemExit(r.returncode)
    return r


def _systemd_is_active(label):
    return _systemctl("is-active", "--quiet", f"{label}.service", check=False).returncode == 0


def _install_systemd(c):
    source = c["source"]
    binary_changed = True
    if source:
        if not os.path.exists(source):
            err(f"{source} does not exist — build it first")
            raise SystemExit(1)
        binary_changed = not (
            os.path.exists(c["program"]) and _files_identical(source, c["program"])
        )

    unit_path = _systemd_unit_path(c["label"])
    desired = _render_systemd_unit(c)
    current = None
    if os.path.exists(unit_path):
        with open(unit_path, "rb") as fh:
            current = fh.read()

    if source and not binary_changed and current == desired and _systemd_is_active(c["label"]):
        ok(f"{os.path.basename(c['program'])} unchanged and already running — nothing to do")
        return

    if source and binary_changed:
        if _systemd_is_active(c["label"]):
            _systemctl("stop", f"{c['label']}.service", check=False)
        os.makedirs(os.path.dirname(c["program"]), exist_ok=True)
        _atomic_install_binary(source, c["program"])

    if not os.path.exists(c["program"]):
        err(f"{c['program']} does not exist — build and install the binary first")
        raise SystemExit(1)
    if not os.access(c["program"], os.X_OK):
        err(f"{c['program']} is not executable")
        raise SystemExit(1)
    for path in (c["stdout"], c["stderr"]):
        if path:
            os.makedirs(os.path.dirname(path), exist_ok=True)

    os.makedirs(os.path.dirname(unit_path), exist_ok=True)
    with open(unit_path, "wb") as fh:
        fh.write(desired)
    _systemctl("daemon-reload")
    _systemctl("enable", "--now", f"{c['label']}.service")
    # Persist the service across logout without an active session.
    subprocess.run(["loginctl", "enable-linger", os.environ.get("USER", "")],
                    capture_output=True)
    verb = "reloaded" if current is not None else "installed and started"
    ok(f"{verb} {c['label']} (systemd --user)")
    info(f"  binary: {c['program']}")
    if c["stdout"]:
        info(f"  log:    {c['stdout']}")


def _uninstall_systemd(c):
    unit_path = _systemd_unit_path(c["label"])
    _systemctl("disable", "--now", f"{c['label']}.service", check=False)
    if os.path.exists(unit_path):
        os.remove(unit_path)
    _systemctl("daemon-reload", check=False)
    ok(f"uninstalled {c['label']}.service (systemd --user)")


def _stop_systemd(c):
    _systemctl("stop", f"{c['label']}.service", check=False)
    ok(f"stopped {c['label']}.service (unit left in place — `admin service start` to bring it back)")


def _start_systemd(c):
    unit_path = _systemd_unit_path(c["label"])
    if not os.path.exists(unit_path):
        err(f"{unit_path} does not exist — run `admin service install` first")
        raise SystemExit(1)
    _systemctl("start", f"{c['label']}.service")
    ok(f"started {c['label']}.service")


def _restart_systemd(c):
    _systemctl("restart", f"{c['label']}.service")
    ok(f"restarted {c['label']}.service")


def _status_systemd(c):
    unit_path = _systemd_unit_path(c["label"])
    if not os.path.exists(unit_path):
        info(f"not installed (no {unit_path})")
        return
    r = _systemctl("show", f"{c['label']}.service",
                    "--property=ActiveState,SubState,MainPID,ExecMainStatus",
                    check=False)
    print(r.stdout.strip())


_LINUX_VERBS = {
    "install": _install_systemd,
    "uninstall": _uninstall_systemd,
    "status": _status_systemd,
    "restart": _restart_systemd,
    "stop": _stop_systemd,
    "start": _start_systemd,
}


def launchd_service(cfg, args):
    """Entry point for a `service` command: dispatch one verb against [launchd].

    macOS runs the launchd verbs; Linux runs the systemd --user verbs. Same
    config table, same verb names, same behavior either way.
    """
    verb = (args or [None])[0]
    if verb not in _VERBS:
        if verb:
            err(f"unknown service verb: {verb}")
        print(_USAGE)
        raise SystemExit(0 if not verb else 1)
    verbs = _LINUX_VERBS if platform.system() == "Linux" else _VERBS
    verbs[verb](_read_config(cfg))


# ---------------------------------------------------------------------------
# Remote install (either platform, over SSH)
# ---------------------------------------------------------------------------

_ARCH_MAP = {
    "Darwin arm64": "darwin-arm64",
    "Darwin x86_64": "darwin-amd64",
    "Linux x86_64": "linux-amd64",
    "Linux aarch64": "linux-arm64",
}

_REMOTE_INSTALLER = os.path.join(os.path.dirname(__file__), "resources", "install_service_remote.sh")


def service_deploy_remote(cfg, host, dist_dir, binary):
    """Push a [go_dist]-built binary for `host`'s architecture, plus the
    portable resources/install_service_remote.sh installer, and run it there
    over SSH — installing the same [launchd] service remotely with no Python
    or the admin tool required on the remote host.

    `dist_dir`/`binary` match the [go_dist] config that built the matrix
    (binary-<os>-<arch> files + VERSION); `host` is user@host or a bare host.
    """
    from .remote import _is_local_host

    if _is_local_host(host):
        err("service_deploy_remote: host resolves to this machine — use `admin service install` instead")
        raise SystemExit(1)

    c = _read_config(cfg)

    r = subprocess.run(["ssh", host, "uname -sm"], capture_output=True, text=True)
    if r.returncode != 0:
        err(f"cannot ssh to {host}: {r.stderr.strip()}")
        raise SystemExit(1)
    rkey = _ARCH_MAP.get(r.stdout.strip())
    if not rkey:
        err(f"unsupported host platform: {r.stdout.strip()!r}")
        raise SystemExit(1)

    src_bin = os.path.join(dist_dir, f"{binary}-{rkey}")
    if not os.path.isfile(src_bin):
        err(f"no bundled binary for {rkey} ({src_bin}) — build the dist matrix first")
        raise SystemExit(1)

    remote_dir = f".admin-service/{c['label']}"

    if subprocess.call(["ssh", host, f"mkdir -p {remote_dir}"]) != 0:
        err(f"could not create {remote_dir} on {host}"); raise SystemExit(1)
    # scp to a temp name and rename, for the same reason _atomic_install_binary
    # does on the local path: the remote daemon may be running from that path,
    # and writing through its inode corrupts the running image.
    if subprocess.call(["scp", "-q", src_bin, f"{host}:{remote_dir}/{binary}.new"]) != 0:
        err(f"scp of the binary to {host} failed"); raise SystemExit(1)
    if subprocess.call(["scp", "-q", _REMOTE_INSTALLER, f"{host}:{remote_dir}/install_service_remote.sh"]) != 0:
        err(f"scp of the installer to {host} failed"); raise SystemExit(1)
    rename_cmd = (
        f"chmod +x {remote_dir}/{binary}.new {remote_dir}/install_service_remote.sh && "
        f"mv -f {remote_dir}/{binary}.new {remote_dir}/{binary}"
    )
    if subprocess.call(["ssh", host, rename_cmd]) != 0:
        err(f"installing the binary on {host} failed"); raise SystemExit(1)

    # $HOME is expanded by the REMOTE shell (this string travels as one ssh
    # argv element, no local shell involved) — literal, not locally quoted.
    flags = ["--label", shlex.quote(c["label"]), "--bin", f"$HOME/{remote_dir}/{binary}"]
    for a in c["args"]:
        flags += ["--arg", shlex.quote(a)]
    for k, v in c["env"].items():
        flags += ["--env", shlex.quote(f"{k}={v}")]
    flags += ["--stdout", f"$HOME/{remote_dir}/run.log", "--stderr", f"$HOME/{remote_dir}/run.log"]

    remote_cmd = f"bash {remote_dir}/install_service_remote.sh " + " ".join(flags)
    if subprocess.call(["ssh", host, remote_cmd]) != 0:
        err(f"remote install on {host} failed"); raise SystemExit(1)
    ok(f"installed {c['label']} on {host}")


def service_uninstall_remote(cfg, host):
    """Uninstall the [launchd] service on a remote host over SSH.

    No binary to push — just the portable installer, run with --uninstall.
    """
    from .remote import _is_local_host

    if _is_local_host(host):
        err("service_uninstall_remote: host resolves to this machine — use `admin service uninstall` instead")
        raise SystemExit(1)

    c = _read_config(cfg)
    remote_dir = f".admin-service/{c['label']}"

    if subprocess.call(["ssh", host, f"mkdir -p {remote_dir}"]) != 0:
        err(f"could not create {remote_dir} on {host}"); raise SystemExit(1)
    if subprocess.call(["scp", "-q", _REMOTE_INSTALLER, f"{host}:{remote_dir}/install_service_remote.sh"]) != 0:
        err(f"scp of the installer to {host} failed"); raise SystemExit(1)
    cmd = (
        f"chmod +x {remote_dir}/install_service_remote.sh && "
        f"bash {remote_dir}/install_service_remote.sh --label {shlex.quote(c['label'])} --uninstall"
    )
    if subprocess.call(["ssh", host, cmd]) != 0:
        err(f"remote uninstall on {host} failed"); raise SystemExit(1)
    ok(f"uninstalled {c['label']} on {host}")
