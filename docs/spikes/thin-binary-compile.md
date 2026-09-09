# Spike: can the Go binary emit a thinner self carrying only what a manifest references?

## The question

A manifest names a handful of kinds — a project using `kind = "shell"` and `kind =
"multi-target"` has no use for the apple, docker, raylib, electron or launchd machinery, yet
the installed `admin` binary carries all of it. `admin-tlhw.6` has to decide what `admin
compile` emits, and this spike answers a prerequisite question first: can the Go binary
compile a thinner version of itself, containing only the code a given `admin.toml` actually
reaches, and is that even the right question given how `compile` is used today?

## The measurement

All builds ran from this worktree on `go version go1.24.2 darwin/arm64`, against base commit
`3590fc1a17e0681ce500ac0647c87b254f2e681f`.

**Full binary** — `go build -o /tmp/admin-full .` against the unmodified tree:

```
17251538 bytes  (16.5 MiB)
```

**One-kind binary** — I edited `internal/kinds/catalog.go`'s `Kinds` map down to a single
entry (`"shell"`) and `internal/kinds/factories.go`'s `registerBuiltins` down to a single
`r.MustRegister("shell", makeShell)` call, rebuilt with the same `go build -o
/tmp/admin-onekind .`, then reverted both files to their original content (`git diff --stat`
against `3590fc1a` shows nothing — the working tree is byte-identical to base):

```
13600946 bytes  (13.0 MiB)
```

Delta between full and one-kind: **3650592 bytes, 21.16% of the full binary.**

I checked that the trim actually removed code, not just data, with `go tool nm`:

```
$ go tool nm /tmp/admin-full    | grep -c 'internal/electron\.'
20
$ go tool nm /tmp/admin-onekind | grep -c 'internal/electron\.'
0
```

`internal/electron`'s package is present in the full binary's symbol table and completely
absent from the one-kind binary's. The same holds for `kinds.makeApple` and
`kinds.makeElectron` themselves: present (2 and 3 symbol hits respectively) in the full
binary, absent (0 and 0) in the one-kind binary.

**Empty-main floor** — a separate one-file module, `package main; func main() {}`, built the
same way:

```
1540818 bytes  (1.5 MiB)
```

I attribute this floor, in my own reading of what a Go binary carries even with no user code,
to the goroutine scheduler, the garbage collector, and the runtime's own type and symbol
tables — I did not read the runtime source to confirm which of those dominates, so treat the
attribution as inference, not a measured breakdown. What the measurement does show directly:
this floor is 8.93% of the full binary and 11.33% of the one-kind binary above, and no
kind-trimming mechanism reclaims it — it is present in both the full and one-kind builds
already measured, unchanged by which kinds are registered.

## Feasibility without a Go toolchain

No, not through recompilation. Building a thinner binary this way means running `go build`
again with a different `internal/kinds` and getting back a different `.go`-compiled artifact
— there is no way to strip an already-linked Go binary down to only the packages one kind
reaches. My own understanding of how `go build` works, not something I read or measured for
this spike, is that the dead-code elimination the measurement above exercises happens inside
the compiler and linker as they produce the binary, not as a step that could run afterward
against an already-linked executable.

That said, a Go toolchain is not a new dependency this repo would be taking on. `install.sh`
already refuses to run without one — `error: admin-project-tool requires the Go toolchain to
build admin` (`install.sh:92`) — because installing `admin` itself means `go build -o
"$DEST_DIR/admin" .` (`install.sh:140`). Every machine that can run `admin` at all already
has Go on it. `admin compile` today, though, is not that machine's Go binary at all: it is
the outgoing Python interpreter's `admin_lib/compile.py`, which packages `{admin_lib +
embedded resolved manifest}` into a `.pyz` zipapp needing nothing but `/usr/bin/env python3`
at the target (`admin_lib/compile.py:1-12`, ADR-0006: "for a project that must run where the
tool isn't installed"). That is the compile path's actual reason to exist — a machine without
`~/.admin` on `PATH`, which by definition may not have Go either. A Go-toolchain-gated thin
build would only ever run on the machine doing the compiling, before the artifact travels
anywhere, so the toolchain requirement itself is not the blocker — but it does mean the
resulting artifact can't be built by `compile` running *on* the destination machine, only
shipped there pre-built.

## Mechanisms

- **Build tags per kind** (`//go:build kind_shell`) — the cleanest source-level way to make
  `registerBuiltins` register only a subset, but every archetype/kind combination compile
  might need becomes its own tag set the build has to be told about ahead of time; nothing
  discovers "this manifest uses these three kinds" from the tag mechanism itself.
- **Code generation of a trimmed `main`** — write a small generated `registerBuiltins`
  listing only the kinds a resolved manifest references, drop it in a temp module alongside
  the rest of `internal/`, and `go build` that. This is exactly what I did by hand for the
  one-kind measurement above; automating it is a generator plus a `go build` invocation, nothing more exotic.
- **A plugin architecture** (Go's `plugin` package) — lets a base binary load kind
  implementations at runtime instead of compiling them in. `go doc plugin`, read on this
  machine, says plugins are "currently supported only on Linux, FreeBSD, and macOS" — this
  repo targets darwin and linux (ADR-0012), so the OS list does not rule the mechanism out.
  What does: the same doc page says a plugin must be built with the exact same Go toolchain
  version and dependency set as the host binary, which is a worse deployment constraint than
  the toolchain requirement this spike is otherwise trying to avoid. I did not build this to
  measure it; I am ruling it out on that toolchain-matching constraint alone, not a
  measurement.
- **Linker dead-code elimination on a generated registry** — this is the mechanism the
  measurement above actually exercises, and the `go tool nm` counts show it working directly:
  `internal/electron` and `kinds.makeApple`/`kinds.makeElectron` are present in the full
  binary's symbol table and completely absent from the one-kind binary's, with no change
  anywhere else in the source. The registry's init-time `MustRegister` calls do defeat this for the kinds that
  stay registered — every `MustRegister("apple", makeApple)` line takes `makeApple`'s address
  and stores it in the registry's map, which makes it reachable from `main` and keeps it in
  the link — but a kind whose `MustRegister` line is never reached (never written, in the
  generated-`main` variant) is invisible to the linker and gets dropped for free. So the
  registry itself is not an obstacle to trimming; it is the trim mechanism, once something
  upstream decides which lines to emit.

## Recommendation to admin-tlhw.6

Emit a full binary with the resolved manifest embedded, not a trimmed one — the saving does
not justify the mechanism. 3650592 bytes (21.16% of 17251538) is a real number, but 8.93% of
the full binary is a Go-runtime floor no mechanism reclaims, and the remaining reachable
savings require running `go build` again per project, on a machine that already needs a Go
toolchain to install `admin` in the first place — the same toolchain a plain `go build` of
the untrimmed binary needs. Trimming buys back roughly a fifth of a 16.5 MiB binary at the
cost of a code-generation step, a temp build directory, and a `go build` invocation for every
`admin compile` — for an artifact whose whole reason to exist (ADR-0006: a project that must
run where the tool isn't installed) has never once been about binary size. `compile` should
stay a single `go build`-shaped step against the full `internal/kinds` registry, with the
project's resolved `admin.toml` embedded the way the Python `compile.py` already embeds it as
a pickle — same shape, ported to Go, no per-manifest trimming.
