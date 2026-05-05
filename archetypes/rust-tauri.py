"""'rust-tauri' archetype — Tauri desktop frontend additions for a Rust workspace."""

ARCHETYPE = {
    "name": "rust-tauri",
    "modules": ["core"],
    "urls": {},
    "commands": {
        "build": {"desc": "Build Tauri app bundle",            "steps": ["build"]},
        "dev":   {"desc": "Run Tauri app in development mode", "steps": ["dev"]},
    },
    "actions": {
        "build": {"kind": "shell", "run": "cargo tauri build", "desc": "Build Tauri app bundle"},
        "dev":   {"kind": "shell", "run": "cargo tauri dev",   "desc": "Run Tauri app in development mode"},
    },
}
