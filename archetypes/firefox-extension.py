"""'firefox-extension' archetype — WebExtension built with Mozilla's web-ext tool."""

ARCHETYPE = {
    "name": "firefox-extension",
    "modules": ["core"],
    "urls": {},
    "commands": {
        "dev":   {"desc": "Launch Firefox Nightly with the extension loaded", "steps": ["dev"]},
        "build": {"desc": "Package the extension as a .xpi",                 "steps": ["build"]},
        "lint":  {"desc": "Lint manifest and sources",                        "steps": ["lint"]},
        "clean": {"desc": "Remove build artifacts",                           "steps": ["clean"]},
    },
    "actions": {
        "dev":   {"kind": "shell", "run": "web-ext run --firefox=nightly",    "desc": "Launch Firefox Nightly with the extension loaded"},
        "build": {"kind": "shell", "run": "web-ext build --overwrite-dest",   "desc": "Package the extension as a .xpi"},
        "lint":  {"kind": "shell", "run": "web-ext lint",                     "desc": "Lint manifest and sources"},
        "clean": {"kind": "shell", "run": "rm -rf web-ext-artifacts",         "desc": "Remove build artifacts"},
    },
}
