# ADR-0015: Pty allocation uses `github.com/creack/pty`

The Go binary allocates pseudo-terminals with `github.com/creack/pty`, pinned at v1.1.24, as a direct dependency in `go.mod`. Only `pty.Start` (run a child on a new pty) and `pty.Setsize` (size that pty to the dashboard's render region) are used.

The dashboard hosts a Command in its own pty rather than surrendering the terminal to it: from the child's point of view the pty is its terminal, so `internal/frame` writes its scroll region and footer into the pty unchanged, and both the dashboard and the child render at once with no second footer implementation.

[ADR-0001](0001-stdlib-only.md)'s stdlib-only rule scopes to Python — the generator and the generated `./admin` script — and says nothing about Go; the Go binary already carries thirteen other direct dependencies. `creack/pty` covers darwin and linux, which is every platform in `internal/platform.Supported` (ADR-0012).
