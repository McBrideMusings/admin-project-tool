"""Layer 1 — universal admin infrastructure shared by all project types.

Provides: colors, output helpers, logging, run_cmd, command registry,
TUI menu, pick_target, process management, boot() entry point.
"""

import atexit
import os
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


def info(msg):
    print(f"{C.CYAN}{msg}{C.RESET}")


def ok(msg):
    print(f"{C.GREEN}{msg}{C.RESET}")


def warn(msg):
    print(f"{C.YELLOW}{msg}{C.RESET}", file=sys.stderr)


def err(msg):
    print(f"{C.RED}{msg}{C.RESET}", file=sys.stderr)


# ---------------------------------------------------------------------------
# URL status helpers
# ---------------------------------------------------------------------------

_urls = []  # list of (label, url) tuples set by projects


def set_urls(urls):
    """Set project URLs for display in TUI. urls: list of (label, url) tuples.
    Env vars in URLs (${VAR} / ${VAR:-default}) are resolved at call time."""
    global _urls
    _urls = [(label, resolve_env(url)) for label, url in urls]


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


def _init(project_name, project_root=None):
    """Initialize module state. Called by boot()."""
    global _project_name, _project_root, LOG_DIR, LOG_FILE
    _project_name = project_name
    _project_root = project_root or os.getcwd()
    LOG_DIR = os.path.join(_project_root, "tmp")
    LOG_FILE = os.path.join(LOG_DIR, "run.log")


def set_log_context(*parts):
    """Redirect LOG_FILE to a named log based on the command path.

    Example: set_log_context("dev", "macos") → tmp/dev-macos.log
    The file is created/truncated immediately so callers don't need to.
    """
    global LOG_FILE
    name = "-".join(str(p) for p in parts if p)
    LOG_FILE = os.path.join(LOG_DIR, f"{name}.log")
    os.makedirs(LOG_DIR, exist_ok=True)
    open(LOG_FILE, "w").close()


# ---------------------------------------------------------------------------
# .env loading
# ---------------------------------------------------------------------------

def _load_dotenv(root=None):
    """Load .env file from project root if it exists."""
    root = root or _project_root or os.getcwd()
    env_path = os.path.join(root, ".env")
    if not os.path.isfile(env_path):
        return
    with open(env_path) as f:
        for line in f:
            line = line.strip()
            if not line or line.startswith("#") or "=" not in line:
                continue
            key, _, val = line.partition("=")
            os.environ.setdefault(key.strip(), val.strip())


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


# ---------------------------------------------------------------------------
# run_cmd
# ---------------------------------------------------------------------------

# ---------------------------------------------------------------------------
# Environment variable interpolation
# ---------------------------------------------------------------------------

import re as _re_env

_ENV_VAR_RE = _re_env.compile(r"\$\{([A-Za-z_][A-Za-z0-9_]*)(?::-([^}]*))?\}")
_referenced_env_vars: list[tuple[str, str | None]] = []


def register_env_var(name, default=None):
    """Record that the generated admin script references this env var.

    Used by the `env` command to list all referenced vars.
    """
    pair = (name, default)
    if pair not in _referenced_env_vars:
        _referenced_env_vars.append(pair)


def resolve_env(s):
    """Expand ${VAR} and ${VAR:-default} in s using os.environ.

    Fails fast with a clear error if a referenced var is unset and has no default.
    Returns the expanded string. Idempotent on strings with no placeholders.
    """
    if not isinstance(s, str) or "${" not in s:
        return s

    def _sub(match):
        name = match.group(1)
        default = match.group(2)
        val = os.environ.get(name)
        if val is not None:
            return val
        if default is not None:
            return default
        err(f"required env var {name} is not set")
        sys.exit(2)

    return _ENV_VAR_RE.sub(_sub, s)


def cmd_env(args):
    """Built-in: list every env var the admin script references and show its state."""
    if not _referenced_env_vars:
        info("no env vars referenced by this admin script")
        return
    info("environment variables referenced by ./admin:")
    width = max(len(name) for name, _ in _referenced_env_vars)
    for name, default in _referenced_env_vars:
        val = os.environ.get(name)
        if val is not None:
            status = f"{C.GREEN}set{C.RESET}"
            shown = val if len(val) < 60 else val[:57] + "..."
            print(f"  {name:<{width}}  {status}  = {shown}")
        elif default is not None:
            print(f"  {name:<{width}}  {C.YELLOW}default{C.RESET}  = {default}")
        else:
            print(f"  {name:<{width}}  {C.RED}UNSET{C.RESET}  (required)")


def run_cmd(cmd, shell=True, capture_log=True, formatter=None):
    """Run a command with line-buffered output, tee to tmp/run.log if capture_log is True.

    Strings are passed through resolve_env() first so ${VAR} and ${VAR:-default}
    placeholders are expanded from os.environ at execution time.
    """
    cmd = resolve_env(cmd)
    os.makedirs(LOG_DIR, exist_ok=True)
    log_fh = None
    if capture_log:
        log_fh = open(LOG_FILE, "a")

    proc = subprocess.Popen(
        cmd, shell=shell, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
        bufsize=1, universal_newlines=True,
    )
    _children.append(proc)
    try:
        for line in proc.stdout:
            display = formatter(line) if formatter else line
            sys.stdout.write(display)
            sys.stdout.flush()
            if log_fh:
                log_fh.write(line)
                log_fh.flush()
    finally:
        proc.wait()
        _children.remove(proc)
        if log_fh:
            log_fh.close()
    return proc.returncode


# ---------------------------------------------------------------------------
# Sync deploy
# ---------------------------------------------------------------------------

def sync_deploy(src_dir, dest, excludes=None, post_sync=None):
    """Copy built files to a local or remote destination.

    src_dir: local directory to sync from
    dest: local path, or user@host:/path for remote rsync
    excludes: list of rsync --exclude patterns (remote only)
    post_sync: callable run after successful sync (receives dest as arg)
    Returns exit code.
    """
    is_remote = ":" in dest and "@" in dest.split(":")[0]

    if is_remote:
        exclude_flags = ""
        if excludes:
            exclude_flags = " ".join(f"--exclude '{e}'" for e in excludes)
        info(f"Syncing to {dest}")
        rc = run_cmd(f"rsync -avz --delete {exclude_flags} {src_dir}/ {dest}/".strip())
        if rc != 0:
            err("rsync failed")
            return rc
        ok(f"Remote deploy complete: {dest}")
    else:
        import shutil
        os.makedirs(dest, exist_ok=True)
        info(f"Copying to {dest}")
        ignore = shutil.ignore_patterns(*excludes) if excludes else None
        shutil.copytree(src_dir, dest, dirs_exist_ok=True, ignore=ignore)
        ok(f"Local deploy complete: {dest}")
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
_subcommand_help = {}
_spacer_counter = 0


def _all_commands():
    """Return only real commands (spacers filtered) for dispatch/iteration."""
    return OrderedDict(
        (name, entry) for name, entry in _commands.items() if "func" in entry
    )


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


def _tui_menu():
    """Inline interactive menu for selecting a command."""
    if not sys.stdout.isatty():
        _print_help()
        return

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

    menu_lines = num + 2

    print(f"{C.BOLD}{_project_name}{C.RESET}\n")
    print_url_status()

    for _ in range(menu_lines):
        sys.stdout.write("\n")
    sys.stdout.write(f"\033[{menu_lines}A")
    sys.stdout.flush()

    def draw():
        sys.stdout.write("\033[s")
        for i, name in enumerate(items):
            sys.stdout.write("\033[2K")
            if _is_spacer(name):
                sys.stdout.write("\n")
                continue
            entry = _commands[name]
            desc = entry["desc"]
            if i == sel:
                sys.stdout.write(f"  {C.CYAN}{C.BOLD}> {name:<14}{C.RESET} {desc}\n")
            else:
                sys.stdout.write(f"    {C.DIM}{name:<14}{C.RESET} {desc}\n")
        sys.stdout.write(f"\033[2K\n\033[2K{C.DIM}  [arrows/j/k] navigate  [enter] run  [q/esc] quit{C.RESET}\n")
        sys.stdout.write("\033[u")
        sys.stdout.flush()

    try:
        while True:
            draw()
            key = read_key()
            if key in ("q", "Q", "esc"):
                sys.stdout.write(f"\033[{menu_lines}B")
                sys.stdout.flush()
                print()
                return
            elif key in ("up", "k"):
                move(-1)
            elif key in ("down", "j"):
                move(1)
            elif key in ("\r", "\n"):
                sys.stdout.write("\033[J")
                sys.stdout.flush()
                info(f"./admin {items[sel]}")
                print()
                os.makedirs(LOG_DIR, exist_ok=True)
                open(LOG_FILE, "w").close()
                _commands[items[sel]]["func"]([])
                return
    except (KeyboardInterrupt, EOFError):
        sys.stdout.write(f"\033[{menu_lines}B")
        sys.stdout.flush()
        print()
        return
    finally:
        termios.tcsetattr(fd, termios.TCSADRAIN, old_settings)


# ---------------------------------------------------------------------------
# pick_target — sub-TUI for selecting among options
# ---------------------------------------------------------------------------

def pick_target(targets, prompt="Select target"):
    """Show an inline TUI picker for sub-targets. Returns the chosen key or None.

    targets: OrderedDict of {name: description}
    """
    if not sys.stdout.isatty():
        return None

    import tty
    import termios

    items = list(targets.keys())
    sel = 0
    num = len(items)

    fd = sys.stdin.fileno()
    old_settings = termios.tcgetattr(fd)

    menu_lines = num + 1

    sys.stdout.write(f"{C.BOLD}{prompt}{C.RESET}\n")
    for _ in range(menu_lines):
        sys.stdout.write("\n")
    sys.stdout.flush()
    sys.stdout.write(f"\033[{menu_lines}A")
    sys.stdout.flush()

    def draw():
        sys.stdout.write("\033[s")
        for i, name in enumerate(items):
            desc = targets[name]
            sys.stdout.write("\033[2K")
            if i == sel:
                sys.stdout.write(f"  {C.CYAN}{C.BOLD}> {name:<14}{C.RESET} {desc}")
            else:
                sys.stdout.write(f"    {C.DIM}{name:<14}{C.RESET} {desc}")
            sys.stdout.write("\n")
        sys.stdout.write("\033[2K")
        sys.stdout.write(f"{C.DIM}  [arrows/j/k] navigate  [enter] select  [q/esc] cancel{C.RESET}")
        sys.stdout.write("\033[u")
        sys.stdout.flush()

    try:
        while True:
            draw()
            key = read_key()
            if key in ("q", "Q", "esc"):
                sys.stdout.write(f"\033[{menu_lines}B\n")
                sys.stdout.flush()
                return None
            elif key in ("up", "k"):
                sel = (sel - 1) % num
            elif key in ("down", "j"):
                sel = (sel + 1) % num
            elif key in ("\r", "\n"):
                for _ in range(menu_lines):
                    sys.stdout.write("\033[2K\n")
                sys.stdout.write(f"\033[{menu_lines}A")
                sys.stdout.flush()
                return items[sel]
    except (KeyboardInterrupt, EOFError):
        sys.stdout.write(f"\033[{menu_lines}B\n")
        sys.stdout.flush()
        return None
    finally:
        termios.tcsetattr(fd, termios.TCSADRAIN, old_settings)


def dispatch_target(targets, cmd_name, args, handlers):
    """Route a command to the correct target handler.

    targets:  OrderedDict of {name: description} for pick_target menu
    cmd_name: command name for error messages (e.g. "build")
    args:     argv args passed to the command
    handlers: dict of {target_name: callable(rest_args)}

    Single-target shortcut: if len(targets) == 1, skips the menu.
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
    print(f"Usage: ./admin <command> [args...]\n")
    print("Commands:")
    for name in _build_menu_layout():
        if _is_spacer(name):
            print()
            continue
        entry = _commands[name]
        print(f"  {C.CYAN}{name:<16}{C.RESET} {entry['desc']}")
    print(f"\nRun {C.DIM}./admin{C.RESET} with no args for interactive menu.")


# ---------------------------------------------------------------------------
# boot() — single entry point
# ---------------------------------------------------------------------------

def boot(project_name, load_dotenv=False):
    """Entry point for per-project admin files.

    Call this from __main__. Handles chdir, argv parsing, TUI or dispatch.
    """
    # Determine project root from the caller's __file__ (the admin script)
    import inspect
    caller_frame = inspect.stack()[1]
    admin_file = caller_frame.filename
    project_root = os.path.dirname(os.path.abspath(admin_file))

    os.chdir(project_root)
    _init(project_name, project_root)

    if load_dotenv:
        _load_dotenv(project_root)

    if len(sys.argv) < 2:
        _tui_menu()
        return

    cmd_name = sys.argv[1]
    if cmd_name in ("-h", "--help", "help"):
        _print_help()
        return

    if cmd_name == "env":
        cmd_env(sys.argv[2:])
        return

    all_cmds = _all_commands()
    if cmd_name not in all_cmds:
        err(f"Unknown command: {cmd_name}")
        _print_help()
        sys.exit(1)

    args = sys.argv[2:]

    # Check for subcommand help
    if ("-h" in args or "--help" in args) and cmd_name in _subcommand_help:
        _subcommand_help[cmd_name]()
        return

    # Clear log file for fresh run
    os.makedirs(LOG_DIR, exist_ok=True)
    open(LOG_FILE, "w").close()

    all_cmds[cmd_name]["func"](args)
