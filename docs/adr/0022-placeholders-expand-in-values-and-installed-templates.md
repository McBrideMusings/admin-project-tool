# ADR-0022: `${VAR}` placeholders expand in config values and in installed templates

A project keeps real addresses, hostnames and paths out of its tracked files by writing `${VAR}` or `${VAR:-default}` and supplying the value from the environment. The tool expands these in every `admin.toml` value that names a host, path, URL or run option, and in the text of a file that an action installs as a template. Both use one function, `dockerrun.ExpandEnvRefs`.

A reference with no value and no default fails the action and names the variable. A literal `${VAR}` never reaches a host.

The `install-template` action is the one place a file's contents are expanded. The docker archetype's deploy calls it and has no copy path of its own.
