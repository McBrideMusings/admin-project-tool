# ADR-0021: Agent interaction is code, terminals are config

Placement (where an agent lives — a pane, a window) is config: `[terminal]`'s `spawn`/`attach`/`kill`/`status` shell strings (ADR-0020). Protocol (what is inside it and how to talk to it) is code: `agent.Driver`, a compiled Go interface, because every harness that vocabulary addresses speaks it the same way regardless of which project is asking.

```go
type Driver interface {
    Start(handle, kind string, args []string) error
    Prompt(handle, text string, until []Status) error
    Read(handle string, lines int) (string, error)
    Info(handle string) (Info, error)
}
```

A `Driver` never opens, closes, or focuses a pane — `attach`/`kill` stay `[terminal]`'s job, and the handle `[terminal].spawn` returns is the only reference it needs. A driver package registers itself from its own `init()`; `agent.Select()` returns the first registered driver whose availability check passes, with no `admin.toml` key naming which one to use. A `kind = "workflow"` action requires both a resolved agent and a `[terminal]` launcher to dispatch, or fails with a named error.
