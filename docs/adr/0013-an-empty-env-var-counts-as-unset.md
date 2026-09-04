# ADR-0013: A `${VAR}` whose variable is empty counts as unset

`resolve_env` (`admin_lib/core.py`) and its Go port `manifest.ResolveEnv` (`internal/manifest/envresolve.go`) treat a variable set to the empty string exactly as one that is not set at all. `${VAR}` with no default errors (`required env var VAR is set but empty`); `${VAR:-default}` takes the default; `${VAR}` naming a `COMPUTED_VARS` entry computes normally, since an empty pin is not a pin; `${VAR:-}` — an explicitly written empty default — stays a legal, deliberate empty.

`admin check` resolves every `${VAR}` reference in the manifest's config tables (`manifest.CONFIG_TABLE_FIELDS`) against the loaded `.env` and names each one no environment value satisfies, by its dotted key. Command and action `run` strings are excluded — they resolve through `resolve_computed`, which passes an unresolvable `${VAR}` through to the shell so shell-locals keep working.

A literal `""` written directly in `admin.toml` still cannot produce a build setting and warns rather than skipping silently; a key genuinely absent from the table remains a legitimate, silent skip.
