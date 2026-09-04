# ADR-0018: Dashboard key bindings are declared per row kind, and `enter` is an ordinary binding

`admin.toml` may carry a top-level `[keys.<row-kind>]` table per bindable Row kind (`bead`, `issue`, `branch`, `pr`, `commit`, `stash`), mapping a key to the name of a Command in the same manifest. The dashboard consults `keys[rowKind][key]` before its own key switch; an unbound (key, row kind) pair falls through to the built-in behavior unchanged. `enter` gets no special treatment — overriding what Enter does on a bead is the same mechanism as adding a new key on a bead.

A reserved set is never bindable: `q`, `ctrl+c`, `esc`, `tab`, `shift+tab`, the arrows, `j`, `k`, `1`–`4`, `/`. The Work tab's own row kinds (`command`, `commandTarget`, `worker`) are not bindable. Shadowing a built-in is reported by `admin check`, not blocked.

A non-workflow bound command receives its row through the environment — `ADMIN_ROW_KIND`, `ADMIN_ROW_ID`, `ADMIN_ROW_URL` — rather than as arguments. A bound command resolving to a single-step `kind="workflow"` action with `over = "selection"` acts on the tracker selection when nonempty, falling back to the cursor row alone otherwise; every other bound command always acts on the cursor row alone.

`[keys.<row-kind>]` merges per key, not per whole table, unlike every other raw archetype table (`[apple]`, `[deploy]`, ...) — the one exception in `internal/archetype/merge.go`'s `mergeRowKeys`. An archetype's `[keys.bead] enter = "implement"` and a project's `[keys.bead] o = "open-bead"` both survive; a project override does not silently drop the archetype's binding.
