# ADR-0013: A `${VAR}` whose variable is empty counts as unset

**Status:** Accepted
**Date:** 2026-08-31
**Context:** oa-jsy.2

## Decision

`resolve_env` (`admin_lib/core.py`) and its Go port `manifest.ResolveEnv`
(`internal/manifest/envresolve.go`) treat a variable set to the empty string exactly
as they treat one that is not set at all:

- `${VAR}` with no default is an error — `required env var VAR is set but empty`,
  exit 2 in Python, a returned error in Go.
- `${VAR:-default}` takes the default.
- `${VAR}` naming a `COMPUTED_VARS` entry is computed. An empty pin is not a pin.
- `${VAR:-}` — an explicitly written empty default — stays a legal, deliberate empty.

`admin check` grew an env pass that reports the same condition ahead of any build:
it loads the project `.env` the way boot does, then resolves every `${VAR}` in the
manifest's **config tables** and names each reference no environment value satisfies,
by its dotted key (`apple.development_team = ${IOS_DEVELOPMENT_TEAM}`).

## Why

`resolve_env` has aborted on a genuinely unset variable since the v2 scaffolding
(cb3c9f7). A variable set to `""` fell through that check and resolved to `""`, which
consumers cannot distinguish from an absent key — so they drop it.

`_compose_sign_settings` did exactly that:

```python
val = cfg.get(manifest_key)
if val is None:
    continue
val = resolve_env(str(val))
if val:                       # empty string falls through, key never emitted
    parts.append(f'{xcode_key}="{val}"')
```

`admin.toml` carried `development_team = "${HERMES_DEV_TEAM}"`. No `DEVELOPMENT_TEAM`
reached `xcodebuild`, macOS fell back to `CODE_SIGN_IDENTITY = -`, and ad-hoc signing
strips every entitlement that needs a provisioning profile. `admin deploy mac` printed
`** BUILD SUCCEEDED **` and installed an app whose entitlements were only:

```
[Key] com.apple.security.get-task-allow
```

with no `com.apple.developer.ubiquity-kvstore-identifier` — so iCloud key-value sync
could not work on the Mac, and nothing in the output said why. It went unnoticed until
2026-08-23.

Two properties of the failure decided where the fix goes.

**It has to be in the tool.** `admin.toml` is untracked in every project
(`~/.config/git/ignore`), so a fresh clone writes one from scratch with no history and
no review. Documentation guards the condition; it does not remove it. The next
mistyped variable name gets the identical silent downgrade.

**Guarding the call site would have been the wrong shape.** `resolve_env` has ~40
callers and the empty string is indistinguishable from a legitimately absent value at
every one of them. Making the empty string unrepresentable removes the condition; a
check in `_compose_sign_settings` would have to be repeated in each of the others, and
in the next one written.

The new semantics are also the ones the syntax already advertised: POSIX `${VAR:-word}`
substitutes `word` when the parameter is unset **or null**. The old behavior ignored a
default whenever the variable was exported empty.

## Scope of the check in `admin check`

Config tables only — the values handed to `resolve_env` unconditionally. Command and
action `run` strings are excluded on purpose: a `kind = "interactive-shell"` body goes
through `resolve_computed` (`interp.py:746`), which passes an unresolvable `${VAR}`
to the shell so shell-locals keep working. Failing on one would break a legal manifest.

`manifest.CONFIG_TABLE_FIELDS` is the single list of which tables those are, read by
both `admin check` and the runtime registrar (`interp._register_env_vars`), so a new
table becomes visible to both at once instead of only to whichever one was remembered.

## Consequence

A project whose `.env` carries a stale `FOO=` line, against a bare `${FOO}` reference,
now fails where it previously ran with the key silently missing. That is the intended
change: the failure was always there, and only its visibility moved. The fix in each
case is to give the reference a default, export a real value, or delete the key.

`_compose_sign_settings` keeps one guard, now loud: a **literal** `""` written in
`admin.toml` still cannot produce a build setting, and warns rather than skipping in
silence. A key genuinely absent from the table remains a legitimate, silent skip — that
distinction is the whole point and must not be collapsed.
