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

def run_cmd(cmd, shell=True, capture_log=True):
    """Run a command with line-buffered output, tee to tmp/run.log if capture_log is True."""
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
            sys.stdout.write(line)
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
# Command registry
# ---------------------------------------------------------------------------

# Core command names in canonical TUI order.
# Projects implement whichever apply — none are mandatory.
#   build  — Compile/produce artifacts. Don't install, don't run.
#   dev    — Development build + run with hot reload loop.
#   deploy — Build for production and install/ship.
CORE_COMMANDS = ("build", "dev", "deploy")

_core_commands = OrderedDict()    # name → {func, desc}
_custom_commands = OrderedDict()  # name → {func, desc}
_subcommand_help = {}


def _all_commands():
    """Return core commands (in order) + custom commands, for iteration."""
    merged = OrderedDict()
    for name in CORE_COMMANDS:
        if name in _core_commands:
            merged[name] = _core_commands[name]
    merged.update(_custom_commands)
    return merged


def core(name, description):
    """Decorator to register a core command (build/dev/deploy).

    Core commands appear first in the TUI in a fixed order, visually
    separated from project-specific commands.
    """
    if name not in CORE_COMMANDS:
        raise ValueError(
            f"'{name}' is not a core command. "
            f"Use @command() for project-specific commands, "
            f"or choose from: {', '.join(CORE_COMMANDS)}"
        )
    def decorator(func):
        _core_commands[name] = {"func": func, "desc": description}
        return func
    return decorator


def command(name, description):
    """Decorator to register a project-specific command.

    These appear after the core commands in the TUI.
    """
    def decorator(func):
        _custom_commands[name] = {"func": func, "desc": description}
        return func
    return decorator


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
    """Build the menu item list with an optional separator between standard and custom.

    Returns (items, separator_index) where items is a list of command names
    and separator_index is the position of the visual divider (or -1 if none).
    """
    std = [n for n in CORE_COMMANDS if n in _core_commands]
    cust = list(_custom_commands.keys())
    if std and cust:
        return std + cust, len(std)
    return std + cust, -1


def _tui_menu():
    """Inline interactive menu for selecting a command."""
    if not sys.stdout.isatty():
        _print_help()
        return

    import termios

    all_cmds = _all_commands()
    items, sep_idx = _build_menu_layout()
    sel = 0
    num = len(items)

    if num == 0:
        warn("No commands registered.")
        return

    fd = sys.stdin.fileno()
    old_settings = termios.tcgetattr(fd)

    # Total lines: items + separator line (if present) + blank + footer
    has_sep = sep_idx > 0
    total_lines = num + (1 if has_sep else 0) + 2

    def draw():
        if draw.rendered:
            sys.stdout.write(f"\033[{total_lines}F")
        sys.stdout.write("\033[J")
        for i, name in enumerate(items):
            if has_sep and i == sep_idx:
                sys.stdout.write("\n")
            desc = all_cmds[name]["desc"]
            if i == sel:
                sys.stdout.write(f"  {C.CYAN}{C.BOLD}> {name:<14}{C.RESET} {desc}\n")
            else:
                sys.stdout.write(f"    {C.DIM}{name:<14}{C.RESET} {desc}\n")
        sys.stdout.write(f"\n{C.DIM}  [arrows/j/k] navigate  [enter] run  [q/esc] quit{C.RESET}\n")
        sys.stdout.flush()
        draw.rendered = True

    draw.rendered = False
    print(f"{C.BOLD}{_project_name}{C.RESET}\n")

    try:
        while True:
            draw()
            key = read_key()
            if key in ("q", "Q", "esc"):
                print()
                return
            elif key in ("up", "k"):
                sel = (sel - 1) % num
            elif key in ("down", "j"):
                sel = (sel + 1) % num
            elif key in ("\r", "\n"):
                sys.stdout.write(f"\033[{total_lines}F\033[J")
                info(f"./admin {items[sel]}")
                print()
                os.makedirs(LOG_DIR, exist_ok=True)
                open(LOG_FILE, "w").close()
                all_cmds[items[sel]]["func"]([])
                return
    except (KeyboardInterrupt, EOFError):
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


# ---------------------------------------------------------------------------
# Help
# ---------------------------------------------------------------------------

def _print_help():
    """Print help text (non-TTY fallback and --help)."""
    all_cmds = _all_commands()
    _, sep_idx = _build_menu_layout()
    items = list(all_cmds.keys())

    print(f"{C.BOLD}{_project_name}{C.RESET}")
    print(f"Usage: ./admin <command> [args...]\n")
    print("Commands:")
    for i, name in enumerate(items):
        if sep_idx > 0 and i == sep_idx:
            print()
        print(f"  {C.CYAN}{name:<16}{C.RESET} {all_cmds[name]['desc']}")
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
