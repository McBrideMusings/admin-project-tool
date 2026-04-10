"""'firefox-extension' archetype — WebExtension built with Mozilla's web-ext tool."""

ARCHETYPE = {
    "name": "firefox-extension",
    "modules": ["core"],
    "urls": {},
    "commands": {
        "dev": {
            "run": "web-ext run --firefox=nightly",
            "desc": "Launch Firefox Nightly with the extension loaded",
        },
        "build": {
            "run": "web-ext build --overwrite-dest",
            "desc": "Package the extension as a .xpi",
        },
        "lint": {
            "run": "web-ext lint",
            "desc": "Lint manifest and sources",
        },
        "clean": {
            "run": "rm -rf web-ext-artifacts",
            "desc": "Remove build artifacts",
        },
    },
}
