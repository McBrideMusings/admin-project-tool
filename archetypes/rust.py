"""'rust' archetype — Rust workspace with standard cargo toolchain commands."""

ARCHETYPE = {
    "name": "rust",
    "modules": ["core"],
    "urls": {},
    "commands": {
        "build": {"desc": "Compile Rust workspace",  "steps": ["build"]},
        "test":  {"desc": "Run all tests",           "steps": ["test"]},
        "vet":   {"desc": "Run Clippy linter",       "steps": ["vet"]},
        "fmt":   {"desc": "Format all source files", "steps": ["fmt"]},
        "clean": {"desc": "Remove build artifacts",  "steps": ["clean"]},
    },
    "actions": {
        "build": {"kind": "shell", "run": "cargo build --workspace",                 "desc": "Compile Rust workspace"},
        "test":  {"kind": "shell", "run": "cargo test --workspace",                  "desc": "Run all tests"},
        "vet":   {"kind": "shell", "run": "cargo clippy --workspace -- -D warnings", "desc": "Run Clippy linter"},
        "fmt":   {"kind": "shell", "run": "cargo fmt --all",                         "desc": "Format all source files"},
        "clean": {"kind": "shell", "run": "cargo clean",                             "desc": "Remove build artifacts"},
    },
}
