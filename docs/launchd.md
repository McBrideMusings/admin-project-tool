# Launchd service deploys

`kind = "launchd"` installs and manages a project's binary as a background service — a
macOS launchd daemon today, with the same config table meant to cover systemd and a
remote-SSH target. It exists as its own doc because the install has invariants a reader
would get wrong by guessing at the obvious implementation.

**Call chain:** `[actions.X]` with `kind = "launchd"` and a `[launchd]` config table →
`admin_lib/interp.py` / `internal/kinds/service.go` (the kind's factory) → `internal/service`
(`local.go`, `remote.go`, `config.go`, `render.go`) for the install, swap and teardown
mechanics.

**What must stay true:**

- **Install is BTM-aware.** macOS Background Task Management treats registering an
  unchanged launchd plist as a new registration and can prompt the user again. The
  installer diffs the plist first and `kickstart`s the existing job instead of
  re-registering when nothing changed.
- **The binary swap under a live daemon is atomic.** The new binary is written beside the
  running one and renamed into place, never written over the path the daemon has open, so
  a daemon mid-request never reads a half-written file.
- **`env_file` / `env_file_keys` are read at generation time, not at service start.** A
  non-interactive `admin deploy` never sources a dotenv the way an interactive shell
  would, so the values named in `env_file_keys` are read out of `env_file` while the
  plist is being generated and baked into it directly.
- **`[launchd].args_cmd`** is a shell command whose stdout is appended to the service's
  `args` at install time, for values only knowable once the target machine is present
  (e.g. a tailnet address that exists only while the VPN is up) — `${VAR}` can't express
  this because it resolves against the shell that is generating the config, not the one
  that will run the service.
- **`nuke-dev`'s inline `[actions.X.config]` replaces `[dev_kill]`, it does not merge with
  it.** A worktree-scoped teardown that declares no ports ends up structurally unable to
  reach a standing service, by design — that's what makes it worktree-scoped.

**Where the code is:** `internal/service/` (install, swap, config resolution, rendering)
and `internal/kinds/service.go` (the `launchd` kind factory that wires it into dispatch).

**How to drive and observe it:** declare `kind = "launchd"` with a `[launchd]` table on an
action and run it through `admin <action>`; `internal/service/config_test.go` and
`internal/service/render_test.go` exercise the config resolution and plist-rendering
paths.
