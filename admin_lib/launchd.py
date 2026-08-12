"""macOS LaunchAgent management, driven by the `[launchd]` table in admin.toml.

Any project that ships a long-running process the developer wants alive at login
declares `[launchd]` and wires a `service` command; the plist is generated here
rather than committed, because every value in one is machine-specific (the binary
lives wherever the toolchain installed it, the log dir is under $HOME).

Orthogonal to the stack archetypes on purpose. A Go supervisor, a Python daemon
and a shell script all register with launchd identically, so this is a module a
project opts into (`modules = ["launchd"]`), not an archetype.

Two things here are less obvious than they look, and both were paid for once:

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
"""

import os
import plistlib
import subprocess

from .core import err, info, ok, resolve_env, warn

_KNOWN_KEYS = {
    "label", "program", "args", "env", "inherit_path", "inherit_home",
    "stdout", "stderr", "working_dir", "keep_alive", "run_at_load",
    "throttle", "process_type", "nice", "start_interval",
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
    if not label:
        _cfg_error("'label' is required (e.g. com.example.myserver)")
    if not program:
        _cfg_error("'program' is required (absolute path to the executable)")
    program = os.path.expanduser(program)
    if not os.path.isabs(program):
        _cfg_error(f"'program' must be an absolute path, got {program!r}")

    out = {
        "label": label,
        "program": program,
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


# ---------------------------------------------------------------------------
# Verbs
# ---------------------------------------------------------------------------

def _install(c):
    if not os.path.exists(c["program"]):
        err(f"{c['program']} does not exist — build and install the binary first")
        raise SystemExit(1)
    if not os.access(c["program"], os.X_OK):
        err(f"{c['program']} is not executable")
        raise SystemExit(1)

    for path in (c["stdout"], c["stderr"]):
        if path:
            os.makedirs(os.path.dirname(path), exist_ok=True)

    plist = _plist_path(c["label"])
    os.makedirs(os.path.dirname(plist), exist_ok=True)
    desired = _render(c)
    current = None
    if os.path.exists(plist):
        with open(plist, "rb") as fh:
            current = fh.read()

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


def launchd_service(cfg, args):
    """Entry point for a `service` command: dispatch one verb against [launchd]."""
    verb = (args or [None])[0]
    if verb not in _VERBS:
        if verb:
            err(f"unknown service verb: {verb}")
        print(_USAGE)
        raise SystemExit(0 if not verb else 1)
    _VERBS[verb](_read_config(cfg))
