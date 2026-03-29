# admin-project-tool

Shared library for per-project `admin` task runners. Extracts duplicated infrastructure (TUI menu, process management, logging, signal handling, command registry) into a reusable library at `~/.admin/`.

## Install

```bash
./install.sh
```

This copies `admin_lib/` to `~/.admin/admin_lib/`.

## Usage

Per-project admin files import from `admin_lib`:

```python
#!/usr/bin/env python3
import sys, os
sys.path.insert(0, os.path.expanduser("~/.admin"))
from admin_lib.core import command, run_cmd, boot, C, info, ok, warn, err

PROJECT_NAME = "My Project"

@command("build", "Build the project")
def cmd_build(args):
    info("Building...")
    rc = run_cmd("make")
    if rc != 0:
        err("Build failed")
        sys.exit(rc)
    ok("Build complete")

if __name__ == "__main__":
    boot(PROJECT_NAME)
```

## Layers

- **core** — Universal: colors, output, logging, run_cmd, command registry, TUI menu, pick_target, URL status
- **apple** — Xcode/Swift/iOS: reload, xcodebuild, simulator, log predicates
- **server** — Docker/web: run_with_reload, run_parallel, Docker deploy
- **unraid** — SSH/Unraid plugins (future)

## URL Status

Projects with a web component can register URLs with `set_urls()`. The TUI home screen and `--help` output show live/down status for each URL (checked via `/health` endpoint). The server template includes this by default along with an `open` command for browser launching.

## Bootstrap & Audit

`init-admin` scaffolds new admin scripts and audits existing ones:

```bash
~/.admin/init-admin .              # Generate from detected project type
~/.admin/init-admin --audit .      # Audit existing admin script
~/.admin/init-admin --type server  # Override detected type
```

Audit checks: admin_lib importability, layer usage, decorator correctness, reload command, boot() call, and (for web projects) URL status setup.

## Templates

- `templates/basic.py` — Go, Rust, generic projects
- `templates/apple.py` — Xcode/Swift projects
- `templates/server.py` — Docker/web server projects (includes URL status and `open` command)
