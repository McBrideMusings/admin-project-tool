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

[logs.app]                                                                            # optional — adds `./admin logs`
dev  = "./tmp/app.log"                                                                # string = local path
prod = { path = "/var/log/app.log", host = "${APP_HOST}", user = "${APP_USER:-root}" } # table = remote (ssh)
default_env = "dev"
```

### `./admin logs`

If `admin.toml` declares a `[logs.<target>]` section, the generated `./admin` exposes a file-tailing command:

```bash
./admin logs                     # TUI picker over log targets
./admin logs app                 # follow from end, default env
./admin logs app --env prod      # explicit env
./admin logs app --tail 500      # print last 500 lines, then follow
./admin logs app --all           # print whole file, then follow
./admin logs app --no-follow     # one-shot; combine with --tail or --all
```

Follow mode reopens the file on inode change or size shrink, so it handles both rotation and truncation-style circular buffers. Remote targets tail over SSH with `tail -F`.

Intended for text logs. Binary files pass through unfiltered and may garble the terminal; a warning is printed if the first 512 bytes contain NULs.

**Non-interactive usage.** In scripts or CI (non-TTY stdout), `./admin logs` skips the picker: target and env must be explicit unless there is only one of each. If a picker would have been required, the command prints an error and exits with status 1 instead. Recommended scripting patterns:

```bash
./admin logs app --env prod --no-follow --tail 100
./admin logs app --env prod --all --no-follow
```

## Layers

- **core** — Universal: colors, output, logging, run_cmd, sync_deploy, command registry, TUI menu, pick_target, dispatch_target, URL status
- **log_format** — Generic log formatter: strips existing timestamps, prepends HH:MM:SS, colorizes log levels (ERROR/WARN/INFO/DEBUG). Opt-in via `formatter=make_formatter()` on `run_cmd`, `run_with_reload`, `run_parallel`
- **logtail** — File-based log tailing (`./admin logs`): local or SSH, per-env paths, `--tail N` / `--all` / `--no-follow`. Auto-included when `admin.toml` has a `[logs]` section; handles rotation and truncation-style circular buffers
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
