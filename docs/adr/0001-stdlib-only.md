# ADR-0001: Stdlib only — generator and generated `./admin`

Both `admin-project-tool` and every `./admin` script it produces depend only on the Python 3.11+ standard library — no `requirements.txt`, no `pyproject.toml` runtime deps, no unconditional third-party imports.

A module may import a third-party package inside a `try: import X / except ImportError` block, provided it degrades gracefully when the import fails — never crashes, never silently produces wrong output.

- `admin_lib/icons.py` — `Pillow` + `cairosvg` for icon generation; no-op with a warning when absent.
- `admin_lib/manifest.py`, `admin_lib/apple.py` — `tomli` as a TOML parser backport for Python 3.9/3.10; replaced by stdlib `tomllib` on 3.11+.

Generated `./admin` scripts never unconditionally import third-party packages; the `tomli` fallback runs only on the developer's own machine.
