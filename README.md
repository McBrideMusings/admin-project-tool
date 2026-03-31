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

- **core** — Universal: colors, output, logging, run_cmd, sync_deploy, command registry, TUI menu, pick_target, dispatch_target, URL status
- **logs** — Generic log formatter: strips existing timestamps, prepends HH:MM:SS, colorizes log levels (ERROR/WARN/INFO/DEBUG). Opt-in via `formatter=make_formatter()` on `run_cmd`, `run_with_reload`, `run_parallel`
- **icons** — Icon generation: SVG/text/solid color rendering at arbitrary sizes, optional DEV banner overlay (requires Pillow, cairosvg optional)
- **apple** — Xcode/Swift/iOS: dev loops (macOS/iOS with automatic log streaming), xcodebuild, simulator, dev icon swap via icons module
- **docker** — Docker/web: run_with_reload, run_parallel, Docker build/deploy, remote Docker build
- **remote** — SSH, rsync, scp, systemd service management
- **unraid** — Unraid plugin deployment (plugin_deploy, plugin_remove)

## Bundler

`bundle` produces a self-contained admin script by inlining admin_lib — no `~/.admin/` dependency needed:

```bash
./bundle path/to/project/admin          # Bundle a single project
./bundle path/to/project/admin --stdout # Preview without overwriting
./bundle --all                          # Re-bundle all projects in ~/Projects/
```

Bundled files include a `@bundled` marker and can be re-bundled when the library updates.

## Bootstrap & Audit

`init-admin` scaffolds new admin scripts and audits existing ones:

```bash
~/.admin/init-admin .              # Generate from detected project type
~/.admin/init-admin --audit .      # Audit existing admin script
~/.admin/init-admin --type docker  # Override detected type
~/.admin/init-admin --multi .      # Force multi-target generation
```

When multiple project patterns are detected (e.g., an Xcode project + a Docker subdirectory), init-admin generates a multi-target admin file with `dispatch_target` routing and `pick_target` menus.

## Templates

### Single-target
- `templates/basic.py` — Go, Rust, generic projects
- `templates/apple.py` — Xcode/Swift projects
- `templates/docker.py` — Docker/web server projects (includes URL status and `open` command)

### Multi-target
- `templates/multi_header.py` — Shared header with computed imports
- `templates/multi_router.py` — Command routing via `dispatch_target`
- `templates/multi_footer.py` — Clean, reload, boot
- `templates/fragments/apple.py` — Apple target (build/dev/deploy via xcodebuild + dev loops)
- `templates/fragments/docker.py` — Docker target (build/dev/deploy via docker_build + run_with_reload)
- `templates/fragments/sync.py` — Sync target (build/deploy via run_cmd + sync_deploy, no dev)
- `templates/fragments/basic.py` — Basic target (build/dev/deploy via run_cmd + run_with_reload)
