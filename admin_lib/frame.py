"""Pinned footer for long-running actions — URLs that never scroll away.

An action that declares `urls` gets a DECSTBM scroll region: its output scrolls
in the top rows while a footer holds the action label, elapsed time, the
herdr pane id (when running inside herdr), the absolute log path, and every
declared URL. Rows are OSC 8 hyperlinks, so the log path and the URLs are both
clickable.

The trade this makes: lines scrolled out of a *partial* scroll region do not
enter the terminal's scrollback. That is acceptable here only because every
framed action is already writing the same output to LOG_FILE, timestamped and
ANSI-stripped — the history moves from scrollback to a file, it is not lost.

Only one frame is active at a time (one process runs one dev loop, same
assumption `admin_lib/reload.py` makes). Every writer goes through `write()`
so the footer is redrawn after each chunk of child output.
"""

import atexit
import os
import shutil
import signal
import struct
import sys
import threading
import time
import urllib.parse

ESC = "\x1b"
_DIM = f"{ESC}[2m"
_BOLD = f"{ESC}[1m"
_CYAN = f"{ESC}[36m"
_GREEN = f"{ESC}[32m"
_RESET = f"{ESC}[0m"

_active = None
_lock = threading.RLock()


def _term_size():
    """(rows, cols). get_terminal_size returns (columns, lines) — read by name."""
    size = shutil.get_terminal_size((80, 24))
    return size.lines, size.columns


def _osc8(url, label):
    return f"{ESC}]8;;{url}{ESC}\\{label}{ESC}]8;;{ESC}\\"


def _file_url(path):
    return "file://" + urllib.parse.quote(os.path.abspath(path))


def _elapsed(seconds):
    s = int(seconds)
    if s < 60:
        return f"{s}s"
    if s < 3600:
        return f"{s // 60}m{s % 60:02d}s"
    return f"{s // 3600}h{(s % 3600) // 60:02d}m"


def _clip(text, width):
    return text if len(text) <= width else text[: max(0, width - 1)] + "…"


def _herdr_pane_id():
    """The herdr pane id, or None when not running inside herdr.

    HERDR_ENV=1 is the presence test; HERDR_PANE_ID set-but-empty still counts
    as "not in herdr" rather than printing an empty segment.
    """
    if os.environ.get("HERDR_ENV") != "1":
        return None
    return os.environ.get("HERDR_PANE_ID") or None


class Frame:
    """Scroll region + footer. Rows 1..N-h scroll; the footer owns the rest."""

    def __init__(self, label, urls, log_path):
        self.label = label
        self.urls = urls
        self.log_path = log_path
        self.started = time.monotonic()
        self.rows, self.cols = _term_size()
        self.active = False
        self._ticker = None
        self.pane_id = _herdr_pane_id()

    @property
    def height(self):
        # status/separator row + log-path row + one row per URL.
        return 2 + len(self.urls)

    @property
    def region_rows(self):
        """Rows available to the child. Never less than 2, or the region is unusable."""
        return max(2, self.rows - self.height)

    # -- lifecycle ---------------------------------------------------------

    def enter(self):
        with _lock:
            # Scroll the prompt up so the footer does not paint over real output.
            sys.stdout.write("\n" * self.height)
            self._set_region()
            self.active = True
            self._draw_locked()
        atexit.register(self.leave)
        try:
            signal.signal(signal.SIGWINCH, self._on_winch)
        except ValueError:
            pass  # not on the main thread — resize just won't reflow
        self._ticker = threading.Thread(target=self._tick, daemon=True)
        self._ticker.start()

    def leave(self):
        with _lock:
            if not self.active:
                return
            self.active = False
            # The terminal may have resized since enter() (or the last
            # reflow) computed rows/cols — a stale value here would
            # position the cursor on a row the footer (at the CURRENT
            # size) still owns, mangling whatever prints next (see
            # admin-0tx3, the Go port's teardown fix in
            # internal/frame/frame.go).
            self.rows, self.cols = _term_size()
            # Erase the footer rather than scrolling it into scrollback:
            # move to the footer's first row (at the freshly re-read
            # size), erase to end of screen, then reset the scroll
            # region. The cursor is left parked there — no trailing
            # newline — so the next thing printed starts exactly where
            # the footer used to be, instead of one row into scrollback
            # below it.
            first = self.region_rows + 1
            sys.stdout.write(f"{ESC}[{first};1H")
            sys.stdout.write(f"{ESC}[J")             # erase to end of screen
            sys.stdout.write(f"{ESC}[r")              # release the scroll region
            sys.stdout.flush()

    def _tick(self):
        while self.active:
            time.sleep(1.0)
            self.draw()

    def _on_winch(self, *_):
        with _lock:
            if not self.active:
                return
            self.rows, self.cols = _term_size()
            self._set_region()
            self._draw_locked()

    def _set_region(self):
        bottom = self.region_rows
        sys.stdout.write(f"{ESC}[1;{bottom}r")
        sys.stdout.write(f"{ESC}[{bottom};1H")
        sys.stdout.flush()

    # -- painting ----------------------------------------------------------

    def draw(self):
        with _lock:
            self._draw_locked()

    def _draw_locked(self):
        if not self.active:
            return
        first = self.region_rows + 1
        sys.stdout.write(f"{ESC}7")  # DECSC — save cursor
        for i, line in enumerate(self._rows()):
            sys.stdout.write(f"{ESC}[{first + i};1H{ESC}[2K{line}")
        sys.stdout.write(f"{ESC}8")  # DECRC — restore cursor
        sys.stdout.flush()

    def _rows(self):
        out = [self._status_row(), self._log_row()]
        # A bare-string URL labels itself; printing it in both columns is noise,
        # so those rows carry no label and do not widen the label column.
        labelled = [(label, url) for label, url in self.urls if label != url]
        width = max((len(label) for label, _ in labelled), default=0)
        for label, url in self.urls:
            col = f"{_BOLD}{label:<{width}}{_RESET}  " if label != url else " " * (width + 2)
            shown = _clip(url, self.cols - width - 6)
            out.append(f" {_GREEN}●{_RESET} {col}{_CYAN}{_osc8(url, shown)}{_RESET}")
        return out

    def _status_row(self):
        text = f" {self.label} · {_elapsed(time.monotonic() - self.started)}"
        if self.pane_id:
            text += f" · {self.pane_id}"
        text += " "
        rule = "─" * max(0, self.cols - len(text) - 2)
        return f"{_DIM}──{text}{rule}{_RESET}"

    def _log_row(self):
        shown = _clip(self.log_path, self.cols - 4)
        return f"   {_DIM}{_osc8(_file_url(self.log_path), shown)}{_RESET}"


# ---------------------------------------------------------------------------
# Module surface — every caller goes through these.
# ---------------------------------------------------------------------------

def activate(label, urls, log_path):
    """Start a frame. No-op (returns None) when framing can't or shouldn't apply.

    Skipped when: stdout is not a terminal (piped output, CI), ADMIN_NO_FRAME is
    set, or a frame is already up.

    An empty `urls` is NOT a reason to skip. The caller — interp._enter_frame —
    owns the should-there-be-a-footer question, and an action carrying
    `footer = true` deliberately arrives here with nothing to link; `_rows()`
    already degrades to the status rule plus the log path, and `height` to 2.
    """
    global _active
    if _active is not None:
        return None
    if os.environ.get("ADMIN_NO_FRAME"):
        return None
    if not sys.stdout.isatty():
        return None
    _active = Frame(label, urls, log_path)
    _active.enter()
    return _active


def deactivate(f):
    """Tear down the frame `f` started. Ignores any frame it does not own.

    A multi-target whose target delegates (`action = "dev-cf"`) puts two actions
    on the stack. The inner one's activate() is declined — a frame is already up
    — so it holds None, and None must mean "tear down nothing": an unconditional
    teardown would strip the outer footer while the outer action is still running.
    """
    global _active
    if f is None or _active is None or f is not _active:
        return
    _active.leave()
    _active = None


def write(text):
    """Write child output to the terminal, keeping the footer intact."""
    with _lock:
        sys.stdout.write(text)
        sys.stdout.flush()
        if _active is not None:
            _active._draw_locked()


def apply_winsize(fd):
    """Size a child's pty to the scroll region, not the screen.

    Without this a child that positions its own cursor (wrangler, vite, metro)
    writes into the footer rows, because it believes it owns the whole screen.
    """
    if _active is None:
        return
    import fcntl
    import termios
    try:
        fcntl.ioctl(fd, termios.TIOCSWINSZ,
                    struct.pack("HHHH", _active.region_rows, _active.cols, 0, 0))
    except OSError:
        pass


def validate_url_refs(actions, project_urls, exc):
    """Reject a `urls` entry that is neither a real URL nor a key of [urls].

    Without this a typo ("local" when the table calls it "dev-cf") silently
    becomes a literal, and the footer paints a row reading `● local` that goes
    nowhere. Runs at resolve time, so `admin check` catches it.
    """
    known = set(project_urls or {})
    for name, cmd in actions.items():
        groups = [(None, cmd.meta.get("urls") or [])]
        groups += [(t, u or []) for t, u in (cmd.meta.get("target_urls") or {}).items()]
        for target, entries in groups:
            for entry in entries:
                url = entry["url"] if isinstance(entry, dict) else entry
                if "://" in url or url in known:
                    continue
                where = f"action '{name}'" + (f" target '{target}'" if target else "")
                hint = f"known [urls] names: {sorted(known)}" if known else "no [urls] table is defined"
                raise exc(
                    f"{where}: urls entry '{url}' is not a URL and does not name "
                    f"an entry in the project's [urls] table — {hint}"
                )


def resolve_urls(raw):
    """Turn an action's declared `urls` into [(label, url)] for the footer.

    Two things happen here, both at dispatch time rather than parse time:

    1. An entry naming a key of the manifest's top-level [urls] table resolves to
       that entry, label and all — so an action reuses what the project already
       declared for `print_url_status` instead of restating the string.
    2. ${VAR} is expanded, so a URL can carry a port the action's env just set.
    """
    from admin_lib.core import get_urls, resolve_computed, resolve_env
    project = dict(get_urls())
    out = []
    for entry in raw or []:
        if isinstance(entry, str):
            label, url = entry, entry
        else:
            url = str(entry.get("url", ""))
            label = str(entry.get("label", url))
        if url in project:                 # a name from the top-level [urls] table
            label, url = url, project[url]
        # resolve_computed first: ${LAN_IP} and friends are derived, not env vars,
        # and resolve_env would leave them as literal text in the footer.
        out.append((label, resolve_env(resolve_computed(url))))
    return [(label, url) for label, url in out if url]
