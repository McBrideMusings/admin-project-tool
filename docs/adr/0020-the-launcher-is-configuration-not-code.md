# ADR-0020: The launcher is configuration, not code

A project names the terminal it runs in with a `[terminal]` table of four shell strings: `spawn`, `attach`, `kill`, and optional `status`. `spawn` runs with `ADMIN_CMD`, `ADMIN_LABEL` and `ADMIN_CWD` set, plus `ADMIN_ROW_*` on a row-bound dispatch (ADR-0018); its trimmed stdout is an opaque handle the tool stores and replays as `ADMIN_HANDLE`, never parses. `status` reports whether the pane still exists.

`detach = true` sends an action to that launcher from one surface: a dashboard row-enter. `admin <command>` and `admin cmd` typed at a shell run inline whatever the manifest declares, as does anything reached from inside an already-launched run. With no `[terminal]` declared, the dashboard runs the action in its own hosted pty.
