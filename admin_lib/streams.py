"""Pump a subprocess's output to the terminal and the log file at once.

The shape every live-log dev loop needs: a child is already running, and its
output must reach the terminal (formatted, for a human watching) and the log
file (for reading afterwards) from one read of one pipe. Splitting the pump
from the argv-building keeps the platform-specific part — `simctl spawn … log
stream` for a simulator, `devicectl … --console` for a device, `adb logcat` for
Android — in the module that owns that platform, while the pumping and teardown
live here once.

`OutputPump` is that one copy. It used to be five: both loops inside
`core.run_cmd` (pty and pipe), the daemon-thread loop below, `reload.py`'s
`run_with_reload`, and both the select-loop and drain-on-exit halves of
`keybindings.run_with_keybindings`. Each caller differs in real ways — pty vs
pipe, whether a formatter is set, whether the log gets the raw or the
formatted text, whether a partial line must be flushed on exit — so those
differences are `OutputPump`'s constructor parameters, not five bodies. Every
caller still writes to the terminal through `frame.write`, now in exactly one
place, which is what keeps the pinned-URL footer repainting after every chunk
without each caller having to know that.

`stream_subprocess` is the thin daemon-thread wrapper other modules call
directly; it was previously private to apple.py (`stream_console_output`/
`stop_os_log`) despite having nothing Apple-specific in it, which is the same
misfiling that hid `run_with_reload` inside docker.py: android's logcat loop
went and grew its own copy rather than find them.
"""
from __future__ import annotations

import threading

from . import frame
from .core import _children


class OutputPump:
    """Feed child-process output to the terminal, a log file, and/or a
    collector list — the one place every dev-loop pump writes through.

    log_fh:            open file handle to append to, or None to skip logging.
    formatter:          callable(line_with_newline) -> line. When set, output is
                        buffered until a complete line is seen so the formatter
                        (which may lay out columns) always gets a whole line.
                        When unset, chunks reach the terminal/log un-split —
                        required for newline-less output (progress bars, a
                        bare prompt) to appear live.
    collect:            a list to .append() each raw chunk/line onto, or None.
    log_transform:      callable(text) -> text applied before writing to
                        log_fh. Defaults to writing the raw text. Pass the
                        formatter itself here to log the formatted text
                        instead (stream_subprocess's historical behaviour).
    display_transform:  callable(text) -> text applied before the unformatted
                        terminal write. No effect when `formatter` is set —
                        the formatter is display_transform in that mode.
    """

    def __init__(self, log_fh=None, formatter=None, collect=None,
                 log_transform=None, display_transform=None):
        self.log_fh = log_fh
        self.formatter = formatter
        self.collect = collect
        self.log_transform = log_transform
        self.display_transform = display_transform
        self._line_buf = ""
        # How much of _line_buf flush_partial() has already put on screen. A
        # formatter lays out whole lines, so anything shown early is shown raw
        # and must not be re-emitted when the line finally completes.
        self._emitted = 0

    def write(self, text):
        """Feed one chunk (a whole line or an arbitrary read) through the pump."""
        if self.log_fh:
            self.log_fh.write(self.log_transform(text) if self.log_transform else text)
            self.log_fh.flush()
        if self.collect is not None:
            self.collect.append(text)
        if self.formatter:
            self._line_buf += text
            while "\n" in self._line_buf:
                line, self._line_buf = self._line_buf.split("\n", 1)
                if self._emitted:
                    frame.write(line[self._emitted:] + "\n")
                    self._emitted = 0
                else:
                    frame.write(self.formatter(line + "\n"))
        else:
            frame.write(self.display_transform(text) if self.display_transform else text)

    def flush_partial(self):
        """Show a newline-less tail while the child is STILL RUNNING.

        A prompt is the case this exists for: `write("Type \"deploy\": ")` has no
        newline, so the formatter path holds it and the user stares at a blank
        terminal while the child blocks on stdin. Callers drive this from their
        idle path (a select timeout), which is the only moment that reliably
        means "the child has stopped talking and may be waiting on us".

        Shown raw, not formatted — the line is incomplete, so there is nothing
        for a column-laying formatter to lay out yet.
        """
        if self.formatter and len(self._line_buf) > self._emitted:
            frame.write(self._line_buf[self._emitted:])
            self._emitted = len(self._line_buf)

    def flush(self):
        """Emit a trailing, newline-less partial line once the child is gone.

        Only meaningful with a formatter (the no-formatter path already writes
        every chunk immediately in `write`, partial line included). No-op when
        there is no formatter or nothing buffered.
        """
        if self.formatter and self._line_buf:
            if self._emitted:
                frame.write(self._line_buf[self._emitted:] + "\n")
            else:
                frame.write(self.formatter(self._line_buf + "\n"))
            self._line_buf = ""
            self._emitted = 0


def stream_subprocess(proc, log_file=None, formatter=None):
    """Stream proc's stdout to the terminal and log_file in a daemon thread.

    proc:      a Popen with stdout=PIPE (text mode) already running.
    log_file:  path to append to; None streams to the terminal only.
    formatter: optional callable(line) -> line, applied before both writes.
    Returns (thread, log_fh) — pass log_fh to stop_stream() to close it.
    """
    log_fh = open(log_file, "a") if log_file else None
    pump = OutputPump(log_fh, formatter=formatter, log_transform=formatter)

    def _pump():
        try:
            for line in proc.stdout:
                pump.write(line)
            # proc.stdout iteration already yields whole lines, but the final
            # one may lack a trailing newline (EOF with no \n) — flush it so
            # a formatter still gets it rather than losing it in the buffer.
            pump.flush()
        except (IOError, ValueError):
            # The pipe closing under us on teardown is the normal exit path.
            pass

    thread = threading.Thread(target=_pump, daemon=True)
    thread.start()
    return thread, log_fh


def stop_stream(proc, log_fh=None):
    """Terminate a streaming subprocess and close its log file."""
    from .reload import _wait_or_kill

    proc.terminate()
    _wait_or_kill(proc, timeout=3)
    if proc in _children:
        _children.remove(proc)
    if log_fh:
        log_fh.close()
