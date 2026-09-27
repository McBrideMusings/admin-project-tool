# ADR-0001: One static Go binary

`admin` is a single Go binary that needs nothing installed on the machine it runs on — no Python, no interpreter, no package manager. Third-party Go modules are allowed because they compile into the binary. Archetypes are TOML manifests with Lua or shell bodies, installed to `~/.admin/archetypes/` separately from the binary, and `admin compile` emits a copy of the same binary with one project's resolved manifest embedded.
