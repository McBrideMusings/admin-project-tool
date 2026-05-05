"""'rust-tauri' archetype — Rust workspace with Tauri desktop frontend.

Self-contained: includes all standard cargo commands plus Tauri-specific
build/dev overrides. Can be used alone or composed with the 'rust' archetype
(rust-tauri wins on build/dev when merged in order).
"""

ARCHETYPE = {
    "name": "rust-tauri",
    "modules": ["core"],
    "urls": {},
    "commands": {
        "build": {"desc": "Build Tauri app bundle",            "steps": ["build"]},
        "dev":   {"desc": "Run Tauri app in development mode", "steps": ["dev"]},
        "test":  {"desc": "Run all tests",                     "steps": ["test"]},
        "vet":   {"desc": "Run Clippy linter",                 "steps": ["vet"]},
        "fmt":   {"desc": "Format all source files",           "steps": ["fmt"]},
        "clean": {"desc": "Remove build artifacts",            "steps": ["clean"]},
    },
    "actions": {
        "build": {"kind": "shell", "run": "cargo tauri build",                       "desc": "Build Tauri app bundle"},
        "dev":   {"kind": "shell", "run": "cargo tauri dev",                         "desc": "Run Tauri app in development mode"},
        "test":  {"kind": "shell", "run": "cargo test --workspace",                  "desc": "Run all tests"},
        "vet":   {"kind": "shell", "run": "cargo clippy --workspace -- -D warnings", "desc": "Run Clippy linter"},
        "fmt":   {"kind": "shell", "run": "cargo fmt --all",                         "desc": "Format all source files"},
        "clean": {"kind": "shell", "run": "cargo clean",                             "desc": "Remove build artifacts"},
    },
}
