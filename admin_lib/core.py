"""Layer 1 — universal admin infrastructure shared by all project types.

Provides: colors, output helpers, logging, run_cmd, command registry,
TUI menu, pick_target, process management, boot() entry point.
"""

import atexit
import datetime
import glob
import os
import re
import signal
import subprocess
import sys
from collections import OrderedDict


# ---------------------------------------------------------------------------
# Colors & output
# ---------------------------------------------------------------------------

class C:
    """ANSI color codes."""
    RED = "\033[31m"
    GREEN = "\033[32m"
    YELLOW = "\033[33m"
    BLUE = "\033[34m"
    MAGENTA = "\033[35m"
    CYAN = "\033[36m"
    BOLD = "\033[1m"
    DIM = "\033[2m"
    RESET = "\033[0m"


# Every ANSI escape, not just the SGR colour codes: log files should carry no
# escape sequences at all, and cursor moves / erases from a progress-bar-style
# child would otherwise survive into the file and scramble it when catted.
_ANSI_ESCAPE_RE = re.compile(r"\x1b(?:[@-Z\\-_]|\[[0-?]*[ -/]*[@-~])")


def _log_write(text: str):
    """Append plain text to LOG_FILE. No-op if logging is disabled or LOG_FILE unset.

    Must never call info/ok/warn/err — those call back into _log_write.
    """
    if LOG_CONFIG.get("enabled") is False:
        return
    _materialize_log_context()
    if not LOG_FILE:
        return
    plain = _ANSI_ESCAPE_RE.sub("", text)
    if not plain.endswith("\n"):
        plain += "\n"
    try:
        with open(LOG_FILE, "a") as f:
            f.write(plain)
    except OSError:
        pass


def info(msg):
    print(f"{C.CYAN}{msg}{C.RESET}")
    _log_write(msg)


def ok(msg):
    print(f"{C.GREEN}{msg}{C.RESET}")
    _log_write(msg)


def warn(msg):
    print(f"{C.YELLOW}{msg}{C.RESET}", file=sys.stderr)
    _log_write(f"WARNING: {msg}")


def err(msg):
    print(f"{C.RED}{msg}{C.RESET}", file=sys.stderr)
    _log_write(f"ERROR: {msg}")


# ---------------------------------------------------------------------------
# Secret-looking env var names (issue #436)
# ---------------------------------------------------------------------------
#
# A `.env` value reaches the manifest as `${VAR}` specifically to keep it out
# of anything read, copied, pasted, or shared — a `--dry-run` preview, an
# `admin env` table, or a run-config drift report are exactly that. Detect on
# the name (no manifest `secret = true` key — that's a schema change nobody
# has asked for) and mask by value substitution everywhere those get
# rendered for a human. The real value still reaches the actually-executed
# `docker run`; only display/log surfaces are masked.

MASK_VALUE = "••••"

_SECRET_NAME_SUFFIXES = ("_TOKEN", "_KEY", "_SECRET", "_PASSWORD")


def is_secret_env_name(name):
    """True if `name` looks like it holds a credential, by suffix convention
    (*_TOKEN, *_KEY, *_SECRET, *_PASSWORD). Case-insensitive since env var
    casing isn't enforced. Doesn't inspect the value — a name-only heuristic,
    so it can't catch a credential in an oddly-named var, and it can't be
    wrong about the value (there's no value analysis to be wrong about).
    """
    if not name:
        return False
    return name.upper().endswith(_SECRET_NAME_SUFFIXES)


# ---------------------------------------------------------------------------
# URL status helpers
# ---------------------------------------------------------------------------

_urls = []  # list of (label, url) tuples set by projects


_pending_urls = []  # raw (label, url) before env resolution


def set_urls(urls):
    """Register project URLs. Raw ${VAR} strings are stored and resolved later by boot()."""
    global _pending_urls
    _pending_urls = list(urls)

def _resolve_pending_urls():
    """Resolve env vars in pending URLs and activate them."""
    global _urls
    _urls = [(label, resolve_env(url)) for label, url in _pending_urls]


def get_urls():
    """Return the resolved (label, url) list.

    Late-binding accessor: modules must call this rather than importing `_urls`
    by value, since boot() reassigns the module global after they import (under
    interpreter mode a value-import would freeze the empty initial list).
    """
    return _urls


def check_url(url, timeout=1):
    """Return True if url responds with 2xx."""
    try:
        from urllib.parse import urlparse
        import http.client
        p = urlparse(url)
        if p.scheme == "https":
            import ssl
            ctx = ssl.create_default_context()
            ctx.check_hostname = False
            ctx.verify_mode = ssl.CERT_NONE
            conn = http.client.HTTPSConnection(p.hostname, p.port or 443, context=ctx, timeout=timeout)
        else:
            conn = http.client.HTTPConnection(p.hostname, p.port or 80, timeout=timeout)
        try:
            conn.request("GET", p.path or "/")
            resp = conn.getresponse()
            return 200 <= resp.status < 400
        finally:
            conn.close()
    except Exception:
        return False


def format_url_status(label, url, live, label_width=6):
    """Format a URL line with colored live/down indicator."""
    dot = f"{C.GREEN}\u25cf{C.RESET}" if live else f"{C.DIM}\u25cb{C.RESET}"
    status = f"{C.GREEN}LIVE{C.RESET}" if live else f"{C.DIM}DOWN{C.RESET}"
    return f"  {label:<{label_width}}{C.DIM}{url}{C.RESET}  {dot} {status}"


def print_url_status():
    """Check and print status for all registered URLs. Checks in parallel."""
    if not _urls:
        return
    from concurrent.futures import ThreadPoolExecutor
    label_width = max(len(label) for label, _ in _urls) + 2
    health_urls = [(label, url, url.rstrip("/") + "/health") for label, url in _urls]
    with ThreadPoolExecutor(max_workers=len(health_urls)) as pool:
        results = list(pool.map(lambda h: check_url(h[2]), health_urls))
    for (label, url, _), live in zip(health_urls, results):
        print(format_url_status(label, url, live, label_width))
    print()


# ---------------------------------------------------------------------------
# Module-level state (set by _init / boot)
# ---------------------------------------------------------------------------

_project_name = ""
_project_root = ""
LOG_DIR = ""
LOG_FILE = ""
LOG_CONFIG: dict = {}
_log_context_active = False


def _init(project_name, project_root=None):
    """Initialize module state. Called by boot()."""
    global _project_name, _project_root, LOG_DIR, LOG_FILE, _log_context_active, _pending_context
    _project_name = project_name
    _project_root = project_root or os.getcwd()
    LOG_DIR = os.path.join(_project_root, "tmp")
    LOG_FILE = os.path.join(LOG_DIR, "run.log")
    _log_context_active = False
    _pending_context = None


def get_log_dir() -> str:
    """Return the current LOG_DIR (late-binding for package import contexts)."""
    return LOG_DIR


def get_log_file() -> str:
    """Return the current LOG_FILE (late-binding for package import contexts).

    Asking for the path is a use, so it settles a pending name (see
    _materialize_log_context) — a caller that got a path and then found the log
    somewhere else would be worse than announcing slightly early.
    """
    _materialize_log_context()
    return LOG_FILE


def get_project_root() -> str:
    """Return the current project root (late-binding for package import contexts)."""
    return _project_root


def configure_logging(cfg: dict):
    """Store the [logging] config from admin.toml. Called at module level in generated scripts."""
    global LOG_CONFIG
    LOG_CONFIG = cfg


def _apply_log_config():
    """Apply LOG_CONFIG overrides to LOG_DIR/LOG_FILE. Called from boot() after _init()."""
    global LOG_DIR, LOG_FILE
    d = LOG_CONFIG.get("dir")
    if d:
        LOG_DIR = os.path.join(_project_root, d)
        LOG_FILE = os.path.join(LOG_DIR, "run.log")


def _prune_logs(stem, retain=None):
    """Delete oldest timestamped log files for `stem`, keeping at most `retain`."""
    n = retain if retain is not None else LOG_CONFIG.get("retain", 5)
    if n == 0:
        return
    pattern = os.path.join(LOG_DIR, glob.escape(stem) + ".*.log")
    existing = sorted(glob.glob(pattern))
    for old in existing[:-n]:
        try:
            os.remove(old)
        except OSError:
            pass


def _rotate_log(path, retain=None):
    """Rotate a log file using numbered suffixes (used by set_log_context_path)."""
    n = retain if retain is not None else LOG_CONFIG.get("retain", 5)
    if n == 0 or not os.path.exists(path):
        return
    for i in range(n - 1, 0, -1):
        src = f"{path}.{i}"
        if os.path.exists(src):
            os.rename(src, f"{path}.{i + 1}")
    os.rename(path, f"{path}.1")


def _prepare_log_file(path=None):
    """Rotate and truncate a log file, respecting enabled flag."""
    if LOG_CONFIG.get("enabled") is False:
        return
    p = path or LOG_FILE
    if not p:
        return
    os.makedirs(os.path.dirname(p), exist_ok=True)
    _rotate_log(p)
    open(p, "w").close()


def _announce_log_start():
    """Print the active LOG_FILE to stderr at the start of a logged command."""
    print(f"{C.DIM}→ logging to: {os.path.relpath(LOG_FILE)}{C.RESET}", file=sys.stderr)


def _announce_log_end():
    """Print the log file path to stderr after a command completes."""
    global _log_context_active
    if not _log_context_active or not LOG_FILE:
        return
    print(f"{C.DIM}→ wrote: {os.path.relpath(LOG_FILE)}{C.RESET}", file=sys.stderr)
    _log_context_active = False


_pending_context = None  # (kind, value, retain) named but not yet created


def _log_context_taken():
    """True if a log context is already live and owns LOG_FILE.

    A nested set_log_context / set_log_context_path call (a multi-target
    action re-scoping under a step-runner, an @-ref step, a kind="python"
    action declaring its own log_file) must inherit the outer context instead
    of replacing it — otherwise the outer command announces a log path,
    creates the file, and the inner call immediately abandons it, leaving the
    first-announced file permanently empty (issue #474). Whichever call sets
    the context first wins; every nested call is a no-op.
    """
    return _log_context_active and bool(LOG_FILE)


def set_log_context(*parts, retain=None):
    """Name this run's log file — deferred, not created yet.

    Example: set_log_context("dev", "cf") → tmp/dev-cf.2026-05-01_143022.log

    Nothing is created or announced here. `admin dev cf` passes through two
    layers that each name a log — the command's step-runner says "dev", then the
    multi-target target says "dev-cf" — and the *innermost* name is the useful
    one, because it says which target actually ran. So the name is held pending
    and the last one set before the log is first used wins. Materialising
    eagerly is what created two files and announced both (issue #474);
    announcing the outer name and keeping it would lose the target from every
    filename instead.

    Once the file exists, a later call inherits it rather than re-pointing at a
    new one — see _log_context_taken.
    """
    global _pending_context
    if LOG_CONFIG.get("enabled") is False:
        return
    if _log_context_taken():
        return
    name = "-".join(str(p) for p in parts if p)
    _pending_context = ("stem", name, retain)


def set_log_context_path(path, retain=None):
    """Name an explicit log path for this run (a `log_file` override) — deferred.

    Same rule as set_log_context: held pending, innermost wins, created on first
    use. A no-op once the file exists.
    """
    global _pending_context
    if LOG_CONFIG.get("enabled") is False:
        return
    if _log_context_taken():
        return
    _pending_context = ("path", path, retain)


def _materialize_log_context():
    """Create and announce the pending log file. Called on first actual use.

    This is the moment the name stops changing: whoever asks for LOG_FILE, or
    writes the first line, fixes it. Every entry point that touches the log has
    to come through here first, or it reads the stale path.
    """
    global LOG_FILE, _log_context_active, _pending_context
    if _pending_context is None or _log_context_active:
        return
    kind, value, retain = _pending_context
    _pending_context = None
    if kind == "stem":
        ts = datetime.datetime.now().strftime("%Y-%m-%d_%H%M%S")
        LOG_FILE = os.path.join(LOG_DIR, f"{value}.{ts}.log")
        os.makedirs(LOG_DIR, exist_ok=True)
        open(LOG_FILE, "w").close()
        _prune_logs(value, retain)
    else:
        LOG_FILE = value if os.path.isabs(value) else os.path.join(_project_root, value)
        os.makedirs(os.path.dirname(LOG_FILE), exist_ok=True)
        _rotate_log(LOG_FILE, retain)
        open(LOG_FILE, "w").close()
    _log_context_active = True
    _announce_log_start()


# ---------------------------------------------------------------------------
# .env loading
# ---------------------------------------------------------------------------

_ENV_KEY_RE = re.compile(r"^[A-Za-z_][A-Za-z0-9_]*$")


def _load_dotenv(root=None):
    """Load .env file from project root if it exists.

    Tolerates a non-text .env (e.g. a git-crypt-encrypted file on a machine
    that hasn't unlocked the repo): the git-crypt magic header is detected and
    the file skipped, undecodable bytes are otherwise ignored, and only
    well-formed KEY=value lines are applied. A locked/binary .env never crashes
    boot.
    """
    root = root or _project_root or os.getcwd()
    env_path = os.path.join(root, ".env")
    if not os.path.isfile(env_path):
        return
    with open(env_path, "rb") as f:
        raw = f.read()
    # git-crypt prefixes encrypted files with "\0GITCRYPT\0"; nothing parseable
    # follows when the repo is locked, so skip the file entirely.
    if raw.startswith(b"\x00GITCRYPT\x00"):
        return
    for line in raw.decode("utf-8", "ignore").splitlines():
        line = line.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        key, _, val = line.partition("=")
        key = key.strip()
        if not _ENV_KEY_RE.match(key):
            continue
        os.environ.setdefault(key, val.strip())


# ---------------------------------------------------------------------------
# Process management
# ---------------------------------------------------------------------------

_children = []
_cleanup_hooks = []


def register_cleanup(fn):
    """Register a cleanup function called on SIGINT/SIGTERM and at exit."""
    _cleanup_hooks.append(fn)


def _cleanup_children(signum=None, frame=None):
    for fn in _cleanup_hooks:
        try:
            fn()
        except Exception:
            pass
    for p in _children:
        try:
            p.terminate()
        except OSError:
            pass
    if signum is not None:
        sys.exit(1)


signal.signal(signal.SIGINT, _cleanup_children)
signal.signal(signal.SIGTERM, _cleanup_children)
atexit.register(_cleanup_children)
atexit.register(_announce_log_end)


# ---------------------------------------------------------------------------
# run_cmd
# ---------------------------------------------------------------------------

# ---------------------------------------------------------------------------
# Environment variable interpolation
# ---------------------------------------------------------------------------

_NAME_RE = re.compile(r"[A-Za-z_][A-Za-z0-9_]*\Z")
_referenced_env_vars: dict[str, dict] = {}


def iter_env_refs(s):
    """Yield (start, end, name, default) for each ${VAR} / ${VAR:-default} in s.

    Brace-matched rather than regex-matched, so a default may itself contain a
    placeholder: `${GOPATH_BIN:-${HOME}/go/bin}` yields one ref whose default is
    the whole `${HOME}/go/bin`, not the truncated `${HOME`. The regex this
    replaced stopped the default at the first `}` and produced a silently wrong
    string — `${HOME/go/bin}` — which is the worst failure shape available,
    because the caller gets a plausible path instead of an error.

    `end` is exclusive. Anything that isn't a well-formed `${NAME}` or
    `${NAME:-…}` — an unbalanced brace, a shell construct like `${x%.c}` — is
    skipped, leaving it to whoever consumes the string next.
    """
    if not isinstance(s, str):
        return
    i = 0
    while True:
        start = s.find("${", i)
        if start < 0:
            return
        depth = 0
        close = -1
        for j in range(start + 1, len(s)):
            if s[j] == "{":
                depth += 1
            elif s[j] == "}":
                depth -= 1
                if depth == 0:
                    close = j
                    break
        if close < 0:
            return  # unbalanced from here on; nothing further is well-formed
        inner = s[start + 2:close]
        sep = _find_default_sep(inner)
        name = inner if sep < 0 else inner[:sep]
        default = None if sep < 0 else inner[sep + 2:]
        if _NAME_RE.match(name):
            yield start, close + 1, name, default
        i = close + 1


def _find_default_sep(inner):
    """Index of the `:-` separating name from default, at brace depth 0."""
    depth = 0
    for i, ch in enumerate(inner):
        if ch == "{":
            depth += 1
        elif ch == "}":
            depth -= 1
        elif ch == ":" and depth == 0 and inner[i + 1:i + 2] == "-":
            return i
    return -1


#
# Origin-string format (stable contract)
# ----------------------------------------------------------------------------
# `used_by` origin strings are a cross-component compatibility contract between
# the manifest scanner (admin_lib/kinds.py::scan_env_vars) and the runtime `env`
# command (cmd_env, below). The interpreter registers these strings live via
# register_env_var; a compiled ./admin embeds the same strings in its pickled
# manifest. Changing the format changes how origins display in the `env` listing.
#
# Currently defined prefixes:
#   cmd:<name>              — var appears in a top-level command's run string
#   cmd:<name>:<target>     — var appears in a command's per-target override
#   url:<key>               — var appears in a [urls] entry
#   logs.<target>.<env>     — var appears in a [logs.<target>.<env>] field
#
# Treat additions as additive; any format change needs coordinated updates in
# admin_lib/kinds.py and a migration plan for existing compiled ./admin artifacts.
# ----------------------------------------------------------------------------
def register_env_var(name, default=None, used_by=None):
    """Record that the generated admin script references this env var.

    Used by the `env` command to list all referenced vars. `used_by` is a list
    of origin strings (e.g. "cmd:deploy", "url:prod", "logs.app.prod") that get
    unioned across repeat registrations of the same var.
    """
    entry = _referenced_env_vars.setdefault(name, {"default": default, "used_by": []})
    if entry["default"] is None and default is not None:
        entry["default"] = default
    for origin in used_by or ():
        if origin not in entry["used_by"]:
            entry["used_by"].append(origin)


def lan_ip():
    """The address another device on this network reaches this machine at.

    Asking the routing table which source address it would use to reach an
    off-link host, rather than guessing at interface names. `ipconfig getifaddr
    en0 || ipconfig getifaddr en1` — what projects were hand-rolling — is macOS
    only and wrong whenever the active interface isn't one of those two (a
    second Ethernet, a dock, a VM bridge).

    No traffic is sent: a UDP socket has no handshake, so connect() only does the
    route lookup. The target is TEST-NET-1, which is reserved and unroutable.
    """
    import socket

    s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    try:
        s.connect(("192.0.2.1", 1))
        return s.getsockname()[0]
    except OSError:
        # No route at all (offline, or every interface down).
        try:
            return socket.gethostbyname(socket.gethostname())
        except OSError:
            return "127.0.0.1"
    finally:
        s.close()


# Vars resolved by computing them rather than reading the environment. They are
# never "required" — `admin env` must not ask the user to set LAN_IP — so the
# manifest scanner skips them too (see kinds.scan_env_vars).
COMPUTED_VARS = {
    "LAN_IP": lan_ip,
}


def resolve_env(s):
    """Expand ${VAR} and ${VAR:-default} in s using os.environ.

    A few names (COMPUTED_VARS, e.g. ${LAN_IP}) are computed instead of read from
    the environment; an explicit environment value still wins, so a project can
    pin one when the automatic answer is wrong.

    A default may itself contain placeholders — `${GOPATH_BIN:-${HOME}/go/bin}`
    is expanded the way it reads — and is only expanded when it is actually used.

    Fails fast with a clear error if a referenced var is unset and has no default.
    **A var set to the empty string counts as unset**, matching what POSIX `:-`
    already means: `${VAR:-default}` takes the default, and a bare `${VAR}` is an
    error rather than a silent "". Anything else lets a caller receive an empty
    string it cannot distinguish from an absent key, and then quietly drop it —
    which is how `development_team = "${HERMES_DEV_TEAM}"` reached xcodebuild as
    no DEVELOPMENT_TEAM at all, downgrading a signed build to ad-hoc and stripping
    every entitlement that needs a provisioning profile (oa-jsy.2). An explicitly
    written empty default (`${VAR:-}`) is still a deliberate empty and is kept.

    Returns the expanded string. Idempotent on strings with no placeholders.
    """
    if not isinstance(s, str) or "${" not in s:
        return s

    out = []
    pos = 0
    for start, end, name, default in iter_env_refs(s):
        out.append(s[pos:start])
        val = os.environ.get(name)
        if val:
            out.append(val)
        elif name in COMPUTED_VARS:
            out.append(COMPUTED_VARS[name]())
        elif default is not None:
            out.append(resolve_env(default))
        else:
            state = "set but empty" if val == "" else "not set"
            err(f"required env var {name} is {state}")
            sys.exit(2)
        pos = end
    out.append(s[pos:])
    return "".join(out)


def resolve_computed(s):
    """Expand ONLY ${COMPUTED_VARS} (e.g. ${LAN_IP}); leave every other ${VAR} alone.

    For strings that are handed to a shell. `resolve_env` is wrong there: it
    exits(2) on any name it can't resolve, so a `run` containing a shell-local
    like `${x}` from `x=$(...)` would kill the action. The shell can expand its
    own variables — what it cannot do is compute LAN_IP. So substitute just those
    and pass the rest through untouched.

    An explicit environment value still wins, matching resolve_env.

    A placeholder that isn't ours is handed on verbatim, except that a computed
    var nested inside its default is still substituted — the shell can expand its
    own `${FOO}`, but there is nobody else who can answer `${LAN_IP}`.
    """
    if not isinstance(s, str) or "${" not in s:
        return s

    out = []
    pos = 0
    for start, end, name, default in iter_env_refs(s):
        out.append(s[pos:start])
        if name in COMPUTED_VARS:
            out.append(os.environ.get(name) or COMPUTED_VARS[name]())
        elif default is None:
            out.append(s[start:end])
        else:
            out.append("${" + name + ":-" + resolve_computed(default) + "}")
        pos = end
    out.append(s[pos:])
    return "".join(out)


def _apply_cmd_env(env_map):
    """Inject command-level env vars before running a command's steps.

    Values support ${VAR:-default} expansion. Called by generated command
    functions when the admin.toml [commands.X] table has an `env` entry.
    """
    for key, expr in env_map.items():
        os.environ[key] = resolve_env(str(expr))


def cmd_env(args):
    """Built-in: list every env var the admin script references and show its state.

    Pass -v/--verbose to print the full origin list for vars referenced from
    more than three places; by default the list is truncated with `+N more`.
    """
    verbose = any(a in ("-v", "--verbose") for a in args)
    if not _referenced_env_vars:
        info("no env vars referenced by this admin script")
        return
    info("environment variables referenced by this project:")
    width = max(len(name) for name in _referenced_env_vars)
    for name, entry in _referenced_env_vars.items():
        default = entry["default"]
        used_by = entry["used_by"]
        val = os.environ.get(name)
        # An empty value is not a value — resolve_env treats it as unset (ADR-0013),
        # so listing it green as "set" would tell you the manifest is fine while
        # every command using it exits 2. EMPTY is called out separately from UNSET
        # because the fix differs: one has a .env line to correct, the other has
        # none to add.
        if val:
            status = f"{C.GREEN}set{C.RESET}"
            if is_secret_env_name(name):
                shown = MASK_VALUE
            else:
                shown = val if len(val) < 60 else val[:57] + "..."
            line = f"  {name:<{width}}  {status}  = {shown}"
        elif default is not None:
            line = f"  {name:<{width}}  {C.YELLOW}default{C.RESET}  = {default}"
        elif val == "":
            line = f"  {name:<{width}}  {C.RED}EMPTY{C.RESET}  (set to \"\" — required)"
        else:
            line = f"  {name:<{width}}  {C.RED}UNSET{C.RESET}  (required)"
        if used_by:
            shown_origins = used_by[:3]
            suffix = ", ".join(shown_origins)
            if len(used_by) > 3:
                suffix += f", +{len(used_by) - 3} more"
            line += f"  (used by: {suffix})"
        print(line)
        if verbose and len(used_by) > 3:
            indent = " " * (4 + width)
            print(f"{indent}{C.DIM}full used_by: {', '.join(used_by)}{C.RESET}")


def _stamp_log(text):
    """Prefix a streamed line with an absolute wall-clock timestamp (HH:MM:SS.mmm).

    Applied to both the dated log file and the live console echo, but only for
    unformatted output — a formatter (LogFormatter) already stamps its own lines,
    so console writes that go through one are left alone to avoid a double stamp.
    Diffing consecutive stamps localises a slow step inside a long-running command:
    its cost shows up as the gap before the next line (e.g. electron-builder
    printing `signing …` then going quiet for 70s). The HH:MM:SS.mmm form matches
    strip_log_timestamp() in log_format, so `admin logs` still renders cleanly.
    Blank lines pass through unstamped to keep output free of trailing-whitespace
    noise.

    A line that already opens with its own timestamp is passed through untouched.
    Stamping it would prepend the time admin *read* the line in front of the time
    the event *happened* — and those diverge exactly when it matters: tailing a
    file written earlier, or one that bursts after a stall. The reader's stamp
    would then measure tail's I/O and mask the very gap being hunted. Sources
    that arrive pre-stamped (`log stream`'s microsecond os_log output, docker
    logs -t, a re-tailed admin log) keep their own, higher-precision event time.
    """
    if not text.strip():
        return text
    # Local import: log_format imports C from this module, so a module-level
    # import here would be circular.
    from .log_format import strip_log_timestamp
    ts_own, _ = strip_log_timestamp(_ANSI_ESCAPE_RE.sub("", text))
    if ts_own:
        return text
    ts = datetime.datetime.now().strftime("%H:%M:%S.%f")[:-3]
    return f"{ts} {text}"


def _cmd_pump(log_fh, formatter, collect):
    """Build the OutputPump shared by run_cmd's pty and pipe paths: the
    terminal gets timestamped text (ANSI intact), the log gets the same
    text with escape sequences stripped.
    """
    from .streams import OutputPump

    return OutputPump(
        log_fh, formatter=formatter, collect=collect,
        log_transform=lambda t: _stamp_log(_ANSI_ESCAPE_RE.sub("", t)),
        display_transform=_stamp_log,
    )


def _decode_utf8_prefix(buf):
    """Decode as much of `buf` as forms whole UTF-8 characters.

    Returns (text, remainder). A 4096-byte read can land mid-character, and
    decoding that with errors="replace" turns one character into two U+FFFD and
    corrupts the log permanently. Holding the incomplete tail until its rest
    arrives costs one iteration of the loop.
    """
    for cut in range(len(buf), max(len(buf) - 4, -1), -1):
        try:
            return buf[:cut].decode("utf-8"), buf[cut:]
        except UnicodeDecodeError:
            continue
    return buf.decode("utf-8", errors="replace"), b""


def _run_cmd_pty(cmd, log_fh, formatter, collect=None):
    """Run cmd inside a pseudo-TTY so isatty() checks pass (e.g. wrangler dev).

    Output is displayed to the terminal with ANSI codes intact; the log file
    receives the same text with escape sequences stripped.
    """
    import pty
    import select

    from . import frame

    master_fd, slave_fd = pty.openpty()
    # Size the child to the scroll region when a frame is up, so a child that
    # positions its own cursor cannot paint over the pinned footer.
    frame.apply_winsize(master_fd)
    proc = subprocess.Popen(
        cmd, shell=True,
        stdin=slave_fd, stdout=slave_fd, stderr=slave_fd,
        close_fds=True,
    )
    os.close(slave_fd)
    _children.append(proc)

    pump = _cmd_pump(log_fh, formatter, collect)

    buf = b""
    # Forward the user's keystrokes into the pty. Without this the child's stdin
    # is a slave fd nobody ever writes to, so anything that prompts hangs
    # forever — the parent owns the real terminal and was dropping every byte.
    # Not gated on isatty(): a piped stdin (`printf 'deploy\n' | admin …`, a CI
    # runner, a test) must reach the child too. EOF unregisters it below so a
    # closed or /dev/null stdin cannot spin the loop.
    try:
        stdin_fd = sys.stdin.fileno()
    except (AttributeError, ValueError, OSError):
        stdin_fd = None
    watch = [master_fd] + ([stdin_fd] if stdin_fd is not None else [])
    try:
        while True:
            try:
                r, _, _ = select.select(watch, [], [], 0.05)
            except (OSError, ValueError):
                break
            if not r:
                # Idle: show a buffered newline-less tail, which is what a
                # prompt looks like from out here.
                pump.flush_partial()
            if stdin_fd is not None and stdin_fd in r:
                try:
                    key = os.read(stdin_fd, 4096)
                except OSError:
                    key = b""
                if key:
                    try:
                        os.write(master_fd, key)
                    except OSError:
                        pass
                else:
                    # EOF. Stop watching it — a closed stdin stays "readable"
                    # forever and would peg a core. Leave the pty open: the
                    # child may still have plenty to say.
                    watch = [fd for fd in watch if fd != stdin_fd]
                    stdin_fd = None
            if master_fd in r:
                try:
                    data = os.read(master_fd, 4096)
                except OSError:
                    break
                # Split on newlines inside the pump, not here: it is the pump
                # that has to hold a newline-less tail so flush_partial() can
                # show it while the child waits. Decode on a boundary-safe
                # buffer so a multi-byte character split across two reads is
                # not mangled.
                buf += data
                text, buf = _decode_utf8_prefix(buf)
                if text:
                    pump.write(text.replace("\r\n", "\n"))
            if proc.poll() is not None:
                try:
                    while True:
                        r, _, _ = select.select([master_fd], [], [], 0.05)
                        if not r:
                            break
                        data = os.read(master_fd, 4096)
                        if not data:
                            break
                        buf += data
                except OSError:
                    pass
                break
    finally:
        try:
            os.close(master_fd)
        except OSError:
            pass
        proc.wait()
        if proc in _children:
            _children.remove(proc)
        if buf:
            pump.write(buf.decode("utf-8", errors="replace").replace("\r\n", "\n"))
        pump.flush()  # a trailing chunk has no \n of its own to trigger it

    return proc.returncode


def run_cmd(cmd, shell=True, capture_log=True, formatter=None, pty=False, collect=None):
    """Run a command with line-buffered output, tee to tmp/run.log if capture_log is True.

    Strings are passed through resolve_env() first so ${VAR} and ${VAR:-default}
    placeholders are expanded from os.environ at execution time.

    If pty=True, the command runs in a pseudo-TTY so programs that check isatty()
    (e.g. wrangler dev) output Worker logs as if run interactively.
    """
    cmd = resolve_env(cmd)
    os.makedirs(LOG_DIR, exist_ok=True)
    _materialize_log_context()
    log_fh = None
    if capture_log and LOG_CONFIG.get("enabled") is not False and LOG_FILE:
        log_fh = open(LOG_FILE, "a")

    if pty:
        rc = _run_cmd_pty(cmd, log_fh, formatter, collect=collect)
        if log_fh:
            log_fh.close()
        return rc

    proc = subprocess.Popen(
        cmd, shell=shell, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
        bufsize=1, universal_newlines=True,
    )
    _children.append(proc)
    pump = _cmd_pump(log_fh, formatter, collect)
    try:
        for line in proc.stdout:
            pump.write(line)
    finally:
        proc.wait()
        _children.remove(proc)
        if log_fh:
            log_fh.close()
    return proc.returncode


# ---------------------------------------------------------------------------
# Sync deploy
# ---------------------------------------------------------------------------

def _prune_extraneous(src_dir, dest, ignore=None):
    """Remove entries under `dest` that have no counterpart under `src_dir`.

    The local half of rsync --delete. Only walks `dest`, and only ever removes
    paths that are absent from `src_dir`, so it cannot touch anything outside
    the destination tree.
    """
    import shutil

    for root, dirs, files in os.walk(dest, topdown=True):
        rel = os.path.relpath(root, dest)
        src_root = src_dir if rel == "." else os.path.join(src_dir, rel)
        skip = ignore(root, dirs + files) if ignore else set()
        for name in list(dirs):
            if name in skip:
                dirs.remove(name)
                continue
            if not os.path.isdir(os.path.join(src_root, name)):
                shutil.rmtree(os.path.join(root, name), ignore_errors=True)
                dirs.remove(name)
        for name in files:
            if name in skip:
                continue
            if not os.path.exists(os.path.join(src_root, name)):
                try:
                    os.remove(os.path.join(root, name))
                except OSError:
                    pass


def sync(src_dir, dest, excludes=None, post_sync=None, delete=True, dry_run=False, chown=None):
    """Copy a directory to a local or remote destination via rsync or shutil.

    src_dir: local directory to sync from
    dest: local path, or user@host:/path for remote rsync
    excludes: list of rsync --exclude patterns (applied to both branches)
    post_sync: callable run after successful sync (receives dest as arg)
    delete: mirror the source exactly, removing anything at `dest` the source
        lacks (rsync --delete). Default True — the historical behaviour, right
        for a dest the source fully owns (a plugin dir, a build output). Pass
        False when the destination also holds files written *there* at runtime
        (databases, state files, logs): mirroring would delete them.
    dry_run: print what would be copied, change nothing.
    chown: "UID:GID" the receiving side should own synced files/dirs as,
        passed straight through as rsync's own --chown (remote branch only).
        Without it, rsync's -a carries the *sending* machine's owner/group
        over the wire and the remote receiver (running as root over SSH)
        honours it — stamping the local user onto directories a container
        running as a different uid then can't write into. Has no effect on
        a local (non-remote) dest — a shutil copy always lands owned by
        whoever runs it — and logs a warning if set there, since silently
        ignoring it would look like the setting worked.
    Returns exit code.
    """
    is_remote = ":" in dest and "@" in dest.split(":")[0]

    if is_remote:
        exclude_flags = ""
        if excludes:
            exclude_flags = " ".join(f"--exclude '{e}'" for e in excludes)
        delete_flag = " --delete" if delete else ""
        chown_flag = f" --chown={chown}" if chown else ""
        dry_flag = " --dry-run" if dry_run else ""
        info(f"Syncing to {dest}" + (" (dry run)" if dry_run else ""))
        rc = run_cmd(
            f"rsync -avz{delete_flag}{chown_flag}{dry_flag} {exclude_flags} {src_dir}/ {dest}/".strip()
        )
        if rc != 0:
            err("rsync failed")
            return rc
        ok(f"Synced: {dest}")
    else:
        import shutil
        if chown:
            warn(f"chown={chown} has no effect on a local copy — files stay owned by whoever runs the sync")
        ignore = shutil.ignore_patterns(*excludes) if excludes else None
        if dry_run:
            info(f"Would copy {src_dir}/ -> {dest}/ (dry run)")
            return 0
        os.makedirs(dest, exist_ok=True)
        info(f"Copying to {dest}")
        # Delete extraneous files, matching the --delete the remote branch has
        # always used. Without this the two halves of `sync` mean different
        # things: a renamed or removed source file lingers forever in a local
        # dest, and a plugin dir keeps loading the stale copy.
        if delete:
            _prune_extraneous(src_dir, dest, ignore)
        shutil.copytree(src_dir, dest, dirs_exist_ok=True, ignore=ignore)
        ok(f"Synced: {dest}")
        rc = 0

    if rc == 0 and post_sync:
        post_sync(dest)

    return rc


# ---------------------------------------------------------------------------
# Command registry
# ---------------------------------------------------------------------------

# Ordered registry: entries are either {"func": func, "desc": desc} for
# commands or {"spacer": True} for blank rows in the TUI/help output.
# Registration order == display order; the generator emits @command(...) and
# add_spacer() calls in whatever order admin.toml's `order` field dictates.
_commands = OrderedDict()         # name/sentinel → entry dict
_actions = {}                     # name → callable; registered actions (not shown in TUI/help)
_subcommand_help = {}
_spacer_counter = 0


def _all_commands():
    """Return only real commands (spacers filtered) for dispatch/iteration."""
    return OrderedDict(
        (name, entry) for name, entry in _commands.items() if "func" in entry
    )


def run_command(name, args=None):
    """Invoke a registered command or action by name. Used by sequences and @-refs."""
    if args is None:
        args = []
    if name in _actions:
        _actions[name](args)
        return
    cmds = _all_commands()
    if name not in cmds:
        err(f"run_command: unknown action or command '{name}'")
        sys.exit(1)
    cmds[name]["func"](args)


def register_action(name, fn):
    """Register an action. Actions are callable via run_command but hidden from the TUI and --help."""
    _actions[name] = fn


def command(name, description):
    """Decorator to register a command. TUI order follows registration order."""
    def decorator(func):
        _commands[name] = {"func": func, "desc": description}
        return func
    return decorator


def add_spacer():
    """Insert a blank row into the TUI/help menu at the current position."""
    global _spacer_counter
    _spacer_counter += 1
    _commands[f"__spacer_{_spacer_counter}__"] = {"spacer": True}


def subcommand_help(name):
    """Decorator to register a subcommand help function."""
    def decorator(func):
        _subcommand_help[name] = func
        return func
    return decorator


# ---------------------------------------------------------------------------
# TUI menu (draw.rendered approach)
# ---------------------------------------------------------------------------

def _read_cursor_row(fd):
    """Query terminal cursor row via ANSI DSR (ESC[6n). Returns 1-based row or None.

    Drains all response bytes from the TTY buffer (including any late-arriving tail)
    so nothing leaks into subsequent read_key() calls.
    """
    import re
    import select
    import tty
    import termios
    old = termios.tcgetattr(fd)
    try:
        tty.setraw(fd)
        sys.stdout.write("\033[6n")
        sys.stdout.flush()
        buf = b""
        while True:
            if not select.select([fd], [], [], 0.3)[0]:
                break
            buf += os.read(fd, 32)
            if b"R" in buf:
                break
        # Drain any bytes that arrived after we stopped reading (e.g. late CPR tail)
        while select.select([fd], [], [], 0)[0]:
            buf += os.read(fd, 32)
        m = re.search(rb"\x1b\[(\d+);\d+R", buf)
        return int(m.group(1)) if m else None
    except Exception:
        return None
    finally:
        termios.tcsetattr(fd, termios.TCSADRAIN, old)


def _make_home(fd, menu_lines):
    """Pre-allocate menu_lines rows then return a _home() callable that resets to them.

    Writes menu_lines blank lines to reserve scroll space (so the first draw never
    causes the terminal to scroll and invalidate the saved position), moves the cursor
    back up, then queries the absolute row via DSR for reliable subsequent jumps.
    Falls back to DECSC/DECRC if DSR is unavailable.
    """
    for _ in range(menu_lines):
        sys.stdout.write("\n")
    sys.stdout.write(f"\033[{menu_lines}A")
    sys.stdout.flush()

    start_row = _read_cursor_row(fd)
    if start_row is not None:
        def _home():
            sys.stdout.write(f"\033[{start_row};1H\033[J")
        return _home

    sys.stdout.write("\0337")  # DECSC — save position for DECRC restore below
    sys.stdout.flush()
    def _home():
        sys.stdout.write("\0338\033[J")
    return _home


def read_key():
    """Read a single keypress, handling arrow key escape sequences."""
    import select
    import tty
    import termios

    fd = sys.stdin.fileno()
    old_settings = termios.tcgetattr(fd)
    try:
        tty.setraw(fd)
        ch = os.read(fd, 1)
        if ch == b"\x03":
            raise KeyboardInterrupt
        if ch == b"\x1b":
            # Wait briefly for arrow key sequence bytes to arrive.
            # Arrow keys send \x1b[A / \x1b[B — bare Esc sends just \x1b.
            if select.select([fd], [], [], 0.1)[0]:
                seq = os.read(fd, 2)
                if seq == b"[A":
                    return "up"
                if seq == b"[B":
                    return "down"
            return "esc"
        return ch.decode("utf-8", errors="replace")
    finally:
        termios.tcsetattr(fd, termios.TCSADRAIN, old_settings)


def _build_menu_layout():
    """Return the full ordered list of menu entry keys (commands + spacer keys)."""
    return list(_commands.keys())


def _is_spacer(key):
    return _commands.get(key, {}).get("spacer") is True


def _char_width(ch):
    """Terminal column width of one character (East-Asian wide/fullwidth = 2)."""
    import unicodedata
    return 2 if unicodedata.east_asian_width(ch) in ("W", "F") else 1


def _display_width(s):
    """Terminal column width of s, accounting for wide characters."""
    return sum(_char_width(ch) for ch in s)


def _fit_display(s, width):
    """Truncate s to at most `width` terminal columns, adding an ellipsis if cut."""
    if width <= 0:
        return ""
    if _display_width(s) <= width:
        return s
    out, w = "", 0
    for ch in s:
        cw = _char_width(ch)
        if w + cw > width - 1:
            break
        out += ch
        w += cw
    return out + "…"


def _pad_display(s, width):
    """Left-justify s to `width` terminal columns (display-width aware)."""
    return s + " " * max(0, width - _display_width(s))


def _tui_menu():
    """Inline interactive menu for selecting a command."""
    if not sys.stdout.isatty():
        _print_help()
        return

    import shutil
    import termios

    items = _build_menu_layout()
    num = len(items)

    if num == 0 or all(_is_spacer(k) for k in items):
        warn("No commands registered.")
        return

    # Start selection on the first non-spacer item.
    sel = next(i for i, k in enumerate(items) if not _is_spacer(k))

    def move(delta):
        nonlocal sel
        i = sel
        for _ in range(num):
            i = (i + delta) % num
            if not _is_spacer(items[i]):
                sel = i
                return

    fd = sys.stdin.fileno()
    old_settings = termios.tcgetattr(fd)

    env_hint = "  [e] open .env" if os.path.exists(os.path.join(_project_root, ".env")) else ""

    # Check URL statuses once upfront (parallel); cache results for draw() so
    # navigation keypresses don't re-probe the network.
    url_statuses = []
    url_label_width = 6
    if _urls:
        from concurrent.futures import ThreadPoolExecutor
        url_label_width = max(len(label) for label, _ in _urls) + 2
        with ThreadPoolExecutor(max_workers=len(_urls)) as pool:
            results = pool.map(lambda lu: check_url(lu[1].rstrip("/") + "/health"), _urls)
        url_statuses = [(label, url, live) for (label, url), live in zip(_urls, results)]

    # Fixed rows draw() always emits around the scrollable command list —
    # name+blank, URL status block, blank+hint — must match exactly so
    # _make_home reserves the right amount of space. The command list itself
    # is windowed to whatever's left of the terminal, the same fix
    # pick_target got for the identical unbounded-reservation bug: writing
    # more newlines than the terminal has rows scrolls the viewport out from
    # under the reserved position, and every redraw after that paints over
    # stale content instead of replacing it.
    n_url_rows = len(url_statuses) + (1 if url_statuses else 0)  # status lines + blank after
    fixed_rows = 2 + n_url_rows + 2  # name+blank, urls, blank+hint

    term_rows = shutil.get_terminal_size((80, 24)).lines
    window = _window_size(num, term_rows, reserved=fixed_rows)
    _home = _make_home(fd, fixed_rows + window)
    offset = 0  # items index of the window's first visible row

    def draw():
        nonlocal offset
        cols = shutil.get_terminal_size((80, 24)).columns
        prefix_width = 4 if cols > 4 else 0
        gap = 1
        min_desc = 8
        command_names = [name for name in items if not _is_spacer(name)]
        name_width = max((_display_width(name) for name in command_names), default=0)
        name_width = min(name_width, max(0, cols - prefix_width - gap - min_desc))
        max_desc = max(0, cols - prefix_width - name_width - gap)
        counter = f"[{sel + 1}/{num}]  " if num > window else ""
        hint = f"  {counter}[arrows/j/k] navigate  [enter] run  [o] open config{env_hint}  [q/esc] quit"[
            :cols
        ]

        offset = _scroll_offset(offset, sel, window, num)

        _home()
        sys.stdout.write(f"{C.BOLD}{_project_name}{C.RESET}\n\n")
        for label, url, live in url_statuses:
            sys.stdout.write(format_url_status(label, url, live, url_label_width) + "\n")
        if url_statuses:
            sys.stdout.write("\n")
        for i, name in enumerate(items[offset : offset + window], start=offset):
            if _is_spacer(name):
                sys.stdout.write("\n")
                continue
            desc = _fit_display(_commands[name]["desc"], max_desc)
            label = _pad_display(_fit_display(name, name_width), name_width)
            if i == sel:
                prefix = "  > " if prefix_width else ""
                sys.stdout.write(f"{prefix}{C.CYAN}{C.BOLD}{label}{C.RESET}{' ' * gap}{desc}\n")
            else:
                prefix = "    " if prefix_width else ""
                sys.stdout.write(f"{prefix}{C.DIM}{label}{C.RESET}{' ' * gap}{desc}\n")
        sys.stdout.write(f"\n{C.DIM}{hint}{C.RESET}\n")
        sys.stdout.flush()

    try:
        while True:
            draw()
            key = read_key()
            if key in ("q", "Q", "esc"):
                print()
                return
            elif key == "o":
                toml_path = os.path.join(_project_root, "admin.toml")
                opener = "open" if sys.platform == "darwin" else "xdg-open"
                subprocess.Popen([opener, toml_path], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            elif key == "e":
                env_path = os.path.join(_project_root, ".env")
                if os.path.exists(env_path):
                    opener = "open" if sys.platform == "darwin" else "xdg-open"
                    subprocess.Popen([opener, env_path], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            elif key in ("up", "k"):
                move(-1)
            elif key in ("down", "j"):
                move(1)
            elif key in ("\r", "\n"):
                _home()  # clear menu before command output
                sys.stdout.flush()
                info(f"admin {items[sel]}")
                print()
                from admin_lib.guards import run_global_preflight
                if run_global_preflight(items[sel]) != 0:
                    return
                _commands[items[sel]]["func"]([])
                _announce_log_end()
                return
    except (KeyboardInterrupt, EOFError):
        print()
        return
    finally:
        termios.tcsetattr(fd, termios.TCSADRAIN, old_settings)


# ---------------------------------------------------------------------------
# pick_target — sub-TUI for selecting among options
# ---------------------------------------------------------------------------

def _window_size(total_rows, term_rows, reserved=3):
    """How many rows of a scrollable list to show at once: the terminal's own
    height minus `reserved` (rows the caller always draws around the
    scrollable region — a prompt line and a hint line, by default), floored
    at 1 so a pathologically short terminal still gets a picker rather than a
    zero- or negative-size reservation. Never more than `total_rows` — a
    short list doesn't grow a window bigger than itself.

    `reserved` is a parameter, not a baked-in 3, because `_tui_menu` reserves
    a different fixed amount around its own scrollable region (a header line,
    a URL status block, a hint line) than `pick_target` does (just a prompt
    and a hint) — same windowing math, different fixed overhead.
    """
    return max(1, min(total_rows, term_rows - reserved))


def _scroll_offset(offset, sel_row, window, total_rows):
    """Slide `offset` the minimum amount needed to keep `sel_row` inside
    `[offset, offset+window)`, then clamp to the valid range.

    Pure and TTY-free on purpose — this is the part of `pick_target`'s
    windowing that a test can exercise without a real terminal. Moving by the
    minimum (rather than re-centering on every move) is what makes paging
    past the visible window one item at a time scroll one row at a time
    instead of jumping a whole page.
    """
    if sel_row < offset:
        offset = sel_row
    elif sel_row >= offset + window:
        offset = sel_row - window + 1
    return max(0, min(offset, max(0, total_rows - window)))


def pick_target(targets, prompt="Select target", order=None):
    """Show an inline TUI picker for sub-targets. Returns the chosen key or None.

    targets: dict of {name: description}
    order: optional list of names; "" entries render as blank spacer rows

    There is no picker without a TTY, so returns None — but says so first.
    Callers typically fall through every branch on None and exit 0 having done
    nothing, which is indistinguishable from success in a script; the message is
    what makes that visible. (pick_target_or_none() reports its own tailored
    message and returns before reaching here, so this never double-prints.)

    Scrolls rather than growing past the terminal: `_make_home` reserves a
    fixed number of rows up front and every redraw re-homes to that exact
    spot, so a list taller than the terminal — writing more newlines than the
    screen has rows — scrolls the viewport out from under the reserved
    position. Every subsequent redraw then lands on the wrong row and paints
    over old content instead of replacing it, which is what produced
    duplicated/ghosted rows for a 60+ item list (etv-station#watch). Windowing
    to the terminal's height keeps the reserved row count within what a
    single screen can actually hold.
    """
    display_order = order if order is not None else list(targets.keys())
    items = [x for x in display_order if x]  # selectable entries only
    # display_order index of each selectable item, parallel to `items` — lets
    # the scroll window track the selection's row position even when spacer
    # ("") entries are mixed in among the selectable ones.
    item_rows = [i for i, e in enumerate(display_order) if e]

    if not sys.stdout.isatty():
        expected = f" (expected one of: {', '.join(items)})" if items else ""
        err(f"{prompt}: no target given{expected} — stdout is not a TTY, pass the target as an argument")
        return None

    import tty
    import termios
    import shutil

    num = len(items)
    if num == 0:
        return None
    sel = 0

    fd = sys.stdin.fileno()
    old_settings = termios.tcgetattr(fd)

    sys.stdout.write(f"{C.BOLD}{prompt}{C.RESET}\n")
    sys.stdout.flush()

    # Reserve at most what the terminal can show in one screen (leaving room
    # for the prompt line already written above and the hint line below), not
    # the full list — the fixed height `_make_home` reserves must never
    # exceed what a single redraw can actually paint.
    term_rows = shutil.get_terminal_size((80, 24)).lines
    window = _window_size(len(display_order), term_rows)
    _home = _make_home(fd, window + 1)  # one row per visible entry + hint
    offset = 0  # display_order index of the window's first visible row

    def draw():
        nonlocal offset
        cols = shutil.get_terminal_size((80, 24)).columns
        prefix_width = 4 if cols > 4 else 0  # "  > " / "    "
        gap = 2
        min_desc = 8
        name_width = max((_display_width(e) for e in items), default=0)
        name_width = min(name_width, max(0, cols - prefix_width - gap - min_desc))
        max_desc = max(0, cols - prefix_width - name_width - gap)

        offset = _scroll_offset(offset, item_rows[sel], window, len(display_order))

        _home()
        for entry in display_order[offset : offset + window]:
            if not entry:
                sys.stdout.write("\n")
                continue
            name = _pad_display(_fit_display(entry, name_width), name_width)
            desc = _fit_display(targets[entry], max_desc)
            if entry == items[sel]:
                prefix = "  > " if prefix_width else ""
                sys.stdout.write(f"{prefix}{C.CYAN}{C.BOLD}{name}{C.RESET}{' ' * gap}{desc}\n")
            else:
                prefix = "    " if prefix_width else ""
                sys.stdout.write(f"{prefix}{C.DIM}{name}{C.RESET}{' ' * gap}{desc}\n")
        # Gated on whether the *display* overflows the window (spacer rows
        # count too, since they still take up a scrolled-past row), not on
        # `num` (selectable items only) — a list padded with enough spacers
        # can scroll while `num <= window`, and the counter must still show.
        counter = f"[{sel + 1}/{num}]  " if len(display_order) > window else ""
        hint = f"  {counter}[arrows/j/k] navigate  [enter] select  [q/esc] cancel"[:cols]
        sys.stdout.write(f"{C.DIM}{hint}{C.RESET}\n")
        sys.stdout.flush()

    try:
        while True:
            draw()
            key = read_key()
            if key in ("q", "Q", "esc"):
                print()
                return None
            elif key in ("up", "k"):
                sel = (sel - 1) % num
            elif key in ("down", "j"):
                sel = (sel + 1) % num
            elif key in ("\r", "\n"):
                _home()  # clear picker before caller output
                sys.stdout.flush()
                return items[sel]
    except (KeyboardInterrupt, EOFError):
        print()
        return None
    finally:
        termios.tcsetattr(fd, termios.TCSADRAIN, old_settings)


def _require_interactive(message):
    """Print an error if stdout is not a TTY. Returns True if interactive."""
    if not sys.stdout.isatty():
        err(message)
        return False
    return True


def pick_target_or_none(menu, prompt, non_tty_msg):
    """Non-TTY-guarded pick_target.

    Returns the chosen key, or None if the caller should treat the situation as
    an exit-1: either stdout is not a TTY (prints non_tty_msg) or the user
    cancelled the picker.
    """
    if not _require_interactive(non_tty_msg):
        return None
    return pick_target(menu, prompt)


def dispatch_target(targets, cmd_name, args, handlers):
    """Route a command to the correct target handler.

    targets is an OrderedDict {name: description} feeding pick_target. handlers
    is {name: callable(rest_args)}. Skips the menu when len(targets)==1.
    """
    target = args[0] if args and not args[0].startswith("-") else ""
    rest = args[1:] if target else (args or [])

    if not target:
        if len(targets) == 1:
            target = next(iter(targets))
        else:
            target = pick_target(targets, f"{cmd_name.title()} which target?")
            if not target:
                return 0

    if target not in handlers:
        err(f"Unknown {cmd_name} target: {target}")
        return 1

    return handlers[target](rest)


# ---------------------------------------------------------------------------
# Help
# ---------------------------------------------------------------------------

def _print_help():
    """Print help text (non-TTY fallback and --help)."""
    print(f"{C.BOLD}{_project_name}{C.RESET}")
    print_url_status()
    print(f"Usage: admin <command> [args...]\n")
    print("Commands:")
    for name in _build_menu_layout():
        if _is_spacer(name):
            print()
            continue
        entry = _commands[name]
        print(f"  {C.CYAN}{name:<16}{C.RESET} {entry['desc']}")
    print(f"\nRun {C.DIM}admin{C.RESET} with no args for interactive menu.")


# ---------------------------------------------------------------------------
# boot() — single entry point
# ---------------------------------------------------------------------------

def boot(project_name, load_dotenv=True, project_root=None, argv=None):
    """Entry point for per-project admin files and the runtime interpreter.

    Call this from __main__. Handles chdir, argv parsing, TUI or dispatch.

    When `project_root` is None it is inferred from the caller's __file__ (the
    per-project ./admin script — the bundled/compiled path). The interpreter
    (admin_lib.interp) passes an explicit `project_root` and `argv` instead,
    since the caller is the installed tool, not a script living in the project.
    """
    if project_root is None:
        # Determine project root from the caller's __file__ (the admin script)
        import inspect
        caller_frame = inspect.stack()[1]
        admin_file = caller_frame.filename
        project_root = os.path.dirname(os.path.abspath(admin_file))

    if argv is None:
        argv = sys.argv

    os.chdir(project_root)
    _init(project_name, project_root)
    _apply_log_config()

    _load_dotenv(project_root)
    _resolve_pending_urls()

    # Top-level KeyboardInterrupt guard so shell/http/python commands that
    # don't go through run_with_reload exit 130 cleanly instead of dumping a
    # traceback. run_with_reload keeps its own inner handler for signal
    # restoration and pidfile cleanup before bubbling up here.
    try:
        if len(argv) < 2:
            _tui_menu()
            return

        cmd_name = argv[1]
        if cmd_name in ("-h", "--help", "help"):
            _print_help()
            return

        if cmd_name == "env":
            cmd_env(argv[2:])
            return

        all_cmds = _all_commands()
        if cmd_name not in all_cmds:
            err(f"Unknown command: {cmd_name}")
            _print_help()
            sys.exit(1)

        args = argv[2:]

        # Check for subcommand help
        if ("-h" in args or "--help" in args) and cmd_name in _subcommand_help:
            _subcommand_help[cmd_name]()
            return

        from admin_lib.guards import run_global_preflight
        if run_global_preflight(cmd_name) != 0:
            sys.exit(1)

        all_cmds[cmd_name]["func"](args)
        _announce_log_end()
    except KeyboardInterrupt:
        sys.exit(130)


