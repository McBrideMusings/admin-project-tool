#!/usr/bin/env python3
"""Validation: power-hour-generator admin migrated to admin_lib.

This is what the migrated admin file looks like.
Run with: python3 tests/test_phg_migration.py --help
"""

import glob
import sys
import os

sys.path.insert(0, os.path.expanduser("~/.admin"))
from admin_lib.core import core, command, run_cmd, boot, C, info, ok, warn, err

PROJECT_NAME = "Power Hour Generator"


@core("build", "Compile all Go packages")
def cmd_build(args):
    info("Building...")
    rc = run_cmd("go build ./...")
    if rc != 0:
        err("Build failed")
        sys.exit(rc)
    ok("Build complete")


@core("deploy", "Build and install CLI to PATH")
def cmd_deploy(args):
    info("Building and installing powerhour...")
    rc = run_cmd("go build ./...")
    if rc != 0:
        err("Build failed")
        sys.exit(rc)
    rc = run_cmd("go install ./cmd/powerhour")
    if rc != 0:
        err("Install failed")
        sys.exit(rc)
    ok("Build and install complete")


@command("test", "Run all tests")
def cmd_test(args):
    info("Running tests...")
    rc = run_cmd("go test ./...")
    if rc != 0:
        err("Tests failed")
        sys.exit(rc)
    ok("All tests passed")


@command("vet", "Run go vet on all packages")
def cmd_vet(args):
    info("Running go vet...")
    rc = run_cmd("go vet ./...")
    if rc != 0:
        err("Vet found issues")
        sys.exit(rc)
    ok("Vet passed")


@command("fmt", "Format Go source files")
def cmd_fmt(args):
    info("Formatting Go source files...")
    go_files = []
    for d in ("cmd", "internal", "pkg"):
        go_files.extend(glob.glob(os.path.join(d, "**", "*.go"), recursive=True))
    if not go_files:
        warn("No Go files found")
        return
    rc = run_cmd("gofmt -w " + " ".join(go_files))
    if rc != 0:
        err("Format failed")
        sys.exit(rc)
    ok("Format complete")


@command("clean", "Remove build artifacts")
def cmd_clean(args):
    info("Cleaning build artifacts...")
    run_cmd("go clean ./...")
    if os.path.exists("powerhour"):
        os.remove("powerhour")
        ok("Removed ./powerhour")
    ok("Clean complete")


@command("docs", "Start the VitePress dev server")
def cmd_docs(args):
    import subprocess
    import threading
    from admin_lib.core import _children, LOG_DIR, LOG_FILE

    info("Starting VitePress dev server...")
    proc = subprocess.Popen(
        "npm run docs:dev", shell=True, cwd="docs",
        stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
        bufsize=1, universal_newlines=True,
    )
    _children.append(proc)

    os.makedirs(LOG_DIR, exist_ok=True)
    log_fh = open(LOG_FILE, "a")

    def stream():
        try:
            for line in proc.stdout:
                sys.stdout.write(line)
                sys.stdout.flush()
                log_fh.write(line)
                log_fh.flush()
        except (IOError, ValueError):
            pass

    threading.Thread(target=stream, daemon=True).start()

    try:
        proc.wait()
    except KeyboardInterrupt:
        proc.terminate()
        proc.wait()
    finally:
        if proc in _children:
            _children.remove(proc)
        log_fh.close()
    ok("Dev server stopped")


if __name__ == "__main__":
    boot(PROJECT_NAME)
