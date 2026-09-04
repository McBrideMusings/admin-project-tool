# ADR-0010: Lua and Bash are the two archetype authoring languages

An archetype action body is written in Bash or Lua, and nothing else. `kind = "shell"` runs as Bash, with no host namespace. Lua runs in-process, in a Lua VM embedded in the Go binary, with host functions (`err`, `info`, `ok`, `pick_target`, `run_cmd`, `run_action`, and the rest of the host API) registered directly into the Lua state — no subprocess, no wire protocol, no stub preamble.

`kind = "python"` and `kind = "python-script"` are deleted. No Python is shipped, invoked, or authored anywhere in the tool or its archetypes.
