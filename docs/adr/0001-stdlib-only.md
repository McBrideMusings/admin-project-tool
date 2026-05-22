# Stdlib only — generator and generated `./admin`

Both `admin-project-tool` (the generator) and every `./admin` script it produces depend on the Python 3.11+ standard library by default. No `requirements.txt`, no `pyproject.toml` runtime deps, no unconditional third-party imports — including obvious candidates like `click`, `typer`, `rich`, `jinja2`, or `tomli-w`.

**Why.** The entire point is that a generated `./admin` is portable: a contributor can `git clone` a project and run `./admin` immediately, with no relationship to the version of `admin-project-tool` that produced it (or whether it's installed at all). For that guarantee to hold, the generated script must be self-contained — which transitively forces the generator to be stdlib-only too, since anything it inlines into `admin_lib/` becomes part of the generated script.

**Considered and rejected.** `jinja2` for template rendering (would shrink `gen/fragments.py`), `click`/`typer` for the CLI (would shrink `admin-gen`), `rich` for prettier TUI output. All produce nicer code at the cost of breaking the zero-install guarantee. If a future contributor reaches for a third-party dep "just for the generator," the dep travels into `admin_lib/` the moment anyone uses it from there — there's no clean firewall. So the rule applies to both halves.

**Carve-out: try/except-guarded optional deps.** A module may import a third-party package inside a `try: import X / except ImportError` block, **provided** the module degrades gracefully when the import fails — never crashes, never silently produces wrong output. Failure mode must be "feature unavailable," not "script broken." Current carve-outs:

- `admin_lib/icons.py` — `Pillow` + `cairosvg` for icon generation; no-op with a warning when absent.
- `gen/manifest.py`, `admin_lib/apple.py` — `tomli` as a TOML parser backport for Python 3.9/3.10 hosts; fully replaced by stdlib `tomllib` on Python 3.11+.

Generated `./admin` scripts never unconditionally import third-party packages. The `tomli` fallback in `gen/manifest.py` and `admin_lib/apple.py` runs only on the **developer's** machine (where they control their Python install), not in the generated output. Archetypes that need TOML parsing at runtime (e.g., `forge-engine`) fall back to `None` and degrade gracefully rather than importing `tomli` into the generated script.

**Consequence.** On Python 3.11+: TOML parsing uses stdlib `tomllib` (read-only — writing TOML is hand-rolled in `gen/manifest.py`). On Python 3.9/3.10: `install.sh` checks for `tomli` and errors with `pip install tomli` if absent. Rendering is string concatenation in `gen/fragments.py`. The CLI is plain `argparse`. The TUI in `admin_lib/` uses raw `cbreak`/`termios`. This is verbose in places and a deliberate trade.
