# admin-project-tool

Shared library and generator for per-project `admin` task runners. Extracts duplicated infrastructure (TUI menu, process management, logging, signal handling, command registry) into a reusable library at `~/.admin/`, and generates each project's `./admin` from a short `admin.toml` manifest.

## Install

```bash
./install.sh
```

Copies `admin_lib/` and the generator (`gen/`, `archetypes/`, `detectors/`, `init-admin`) to `~/.admin/`. Requires Python 3.11+.

## Usage

### Bootstrap a new project

```bash
~/.admin/init-admin .              # detect stack, write admin.toml, generate ./admin
~/.admin/init-admin --regenerate . # reload admin.toml and regenerate ./admin
~/.admin/init-admin --audit .      # diff ./admin against what admin.toml would produce
```

### admin.toml manifest

```toml
project_name = "My Project"
python_min   = "3.11"
archetypes   = ["simple"]

[urls]
dev  = "http://localhost:8080"

[commands]
# Override archetype commands or add project-specific ones
```

## Layers

- **core** — Universal: colors, output, logging, run_cmd, sync_deploy, command registry, TUI menu, pick_target, dispatch_target, URL status
- **logs** — Generic log formatter: strips existing timestamps, prepends HH:MM:SS, colorizes log levels (ERROR/WARN/INFO/DEBUG). Opt-in via `formatter=make_formatter()` on `run_cmd`, `run_with_reload`, `run_parallel`
- **icons** — Icon generation: SVG/text/solid color rendering at arbitrary sizes, optional DEV banner overlay (requires Pillow, cairosvg optional)
- **apple** — Xcode/Swift/iOS: dev loops (macOS/iOS with automatic log streaming), xcodebuild, simulator, dev icon swap via icons module
- **docker** — Docker/web: run_with_reload, run_parallel, Docker build/deploy, remote Docker build
- **remote** — SSH, rsync, scp, systemd service management
- **unraid** — Unraid plugin deployment (plugin_deploy, plugin_remove)

## Archetypes

Composable mixins under `archetypes/`. A manifest lists one or more archetypes; their command sets are merged in order and emitted by the generator. See `docs/design.md` for details.

- **`hugo`** — Hugo static site builds and dev server
- **`go-cli`** — Go build, test, vet, fmt, deploy
- **`docker-unraid`** — Docker build on remote, deploy to Unraid
- **`stash-plugin`** — Rollup build + Stash plugin sync
- **`apple`** — Xcode/Swift/iOS builds, dev loops, device install
- **`unraid-plugin`** — Push source to Unraid USB, register/uninstall
- **`firefox-extension`** — web-ext dev, package, lint
- **`simple`** — Minimal build/test/clean shell strings (fallback)
