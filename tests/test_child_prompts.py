"""A child that prompts on stdin must be answerable through admin.

Both runner paths took the terminal away from the child and never gave the
bytes back, so any command that asked a question hung forever with a blank
screen. The prompt is written without a trailing newline, which is also what
kept it invisible: the formatter path holds a partial line.

Covered here because the failure is an interaction between three files
(core's pty loop, keybindings' selector loop, streams' line buffering) and
unit tests on any one of them all passed while the feature was broken.
"""
from __future__ import annotations

import os
import subprocess
import sys
import textwrap
from pathlib import Path

import pytest

REPO = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO))

CHILD = textwrap.dedent(
    """
    import sys
    sys.stdout.write('Type "deploy" to continue: ')
    sys.stdout.flush()
    ans = sys.stdin.readline().strip()
    print("GOT:" + ans)
    """
)

# Driven in a subprocess so the real fd 0 is a pipe we control. Importing the
# runner in-process would inherit pytest's captured stdin, which is not the
# thing under test.
DRIVER = textwrap.dedent(
    """
    import sys, os
    sys.path.insert(0, {repo!r})
    from admin_lib import core
    core.LOG_DIR = {tmp!r}
    core.LOG_FILE = os.path.join({tmp!r}, "run.log")
    {body}
    print("rc=", rc)
    """
)


def _drive(tmp_path, body):
    child = tmp_path / "child.py"
    child.write_text(CHILD)
    driver = tmp_path / "driver.py"
    driver.write_text(
        DRIVER.format(
            repo=str(REPO),
            tmp=str(tmp_path),
            body=body.format(cmd=f"python3 {child}"),
        )
    )
    return subprocess.run(
        [sys.executable, str(driver)],
        input=b"deploy\n",
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        timeout=60,
        cwd=str(REPO),
        env={**os.environ, "PYTHONUNBUFFERED": "1"},
    )


def test_pty_path_forwards_stdin_to_the_child(tmp_path):
    """core.run_cmd(pty=True): the child's stdin is the pty slave, so the parent
    has to pump the real stdin into the master or nothing ever arrives."""
    r = _drive(tmp_path, 'rc = core.run_cmd({cmd!r}, capture_log=True, pty=True)')
    out = r.stdout.decode()
    assert "GOT:deploy" in out, out
    assert "rc= 0" in out, out


def test_pty_path_shows_the_newline_less_prompt(tmp_path):
    """The prompt has to appear while the child waits, not after it exits."""
    r = _drive(tmp_path, 'rc = core.run_cmd({cmd!r}, capture_log=True, pty=True)')
    assert 'Type "deploy" to continue:' in r.stdout.decode()


@pytest.mark.xfail(
    reason="run_with_keybindings (kind = \"interactive-shell\") still takes the "
    "terminal from the child: it registers stdin in its own selector and reads "
    "it one byte at a time, so parent and child race for the answer. Not fixed "
    "here — the fix hangs the parent on exit for reasons not yet isolated.",
    strict=False,
)
def test_interactive_shell_path_leaves_stdin_to_the_child(tmp_path):
    """The known-broken second path, kept as a failing marker so it is not
    rediscovered from scratch.

    Driven over a real pty, not a pipe: the guard is `sys.stdin.isatty()`, so a
    piped stdin never reaches the bug and a pipe-based test passes against
    broken code.
    """
    import pty as ptymod
    import time

    child = tmp_path / "child.py"
    child.write_text(CHILD)
    driver = tmp_path / "driver.py"
    driver.write_text(
        DRIVER.format(
            repo=str(REPO),
            tmp=str(tmp_path),
            body=(
                "from admin_lib.keybindings import run_with_keybindings\n"
                f"rc = run_with_keybindings({str(f'python3 {child}')!r}, {{}}, label='t')"
            ),
        )
    )

    master, slave = ptymod.openpty()
    proc = subprocess.Popen(
        [sys.executable, str(driver)],
        stdin=slave, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
        cwd=str(REPO), env={**os.environ, "PYTHONUNBUFFERED": "1"},
    )
    os.close(slave)
    try:
        time.sleep(0.5)  # let the child reach its prompt
        os.write(master, b"deploy\n")
        out = proc.communicate(timeout=30)[0].decode()
    except subprocess.TimeoutExpired:
        proc.kill()
        out = ""
    finally:
        os.close(master)
        if proc.poll() is None:
            proc.kill()
    assert "GOT:deploy" in out, out
    assert "rc= 0" in out, out


@pytest.mark.parametrize("stdin_source", ["closed", "devnull"])
def test_a_dead_stdin_does_not_hang_or_spin(tmp_path, stdin_source):
    """EOF on stdin must unregister it. A closed fd stays readable forever, so
    leaving it in the select set pegs a core instead of finishing."""
    child = tmp_path / "child.py"
    child.write_text("print('no prompt here')\n")
    driver = tmp_path / "driver.py"
    driver.write_text(
        DRIVER.format(
            repo=str(REPO),
            tmp=str(tmp_path),
            body=f"rc = core.run_cmd({str(f'python3 {child}')!r}, capture_log=True, pty=True)",
        )
    )
    stdin = subprocess.DEVNULL if stdin_source == "devnull" else subprocess.PIPE
    r = subprocess.run(
        [sys.executable, str(driver)],
        input=b"" if stdin is subprocess.PIPE else None,
        stdin=None if stdin is subprocess.PIPE else stdin,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        timeout=60,
        cwd=str(REPO),
    )
    out = r.stdout.decode()
    assert "no prompt here" in out, out
    assert "rc= 0" in out, out
