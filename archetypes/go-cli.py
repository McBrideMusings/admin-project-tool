"""'go-cli' archetype — Go command-line tool with standard go toolchain commands.

Build, install, and clean read their paths from [go] rather than the run string:

    [go]
    binary = "bin/wtree"   # build -o here; clean removes it. Omit -> go build ./...
    main   = "."           # package to build/install. Omit -> ./...

Without [go] the archetype behaves exactly as it always did (go build ./...),
so projects that never set it are unaffected.
"""

ARCHETYPE = {
    "name": "go-cli",
    "modules": ["core"],
    "urls": {},
    "commands": {
        "build":  {"desc": "Compile all packages",           "steps": ["build"]},
        "test":   {"desc": "Run all tests",                  "steps": ["test"]},
        "vet":    {"desc": "Run go vet",                     "steps": ["vet"]},
        "fmt":    {"desc": "Format source files",            "steps": ["fmt"]},
        "deploy": {"desc": "Install binaries into GOBIN",    "steps": ["deploy"]},
        "clean":  {"desc": "Remove build artifacts",         "steps": ["clean"]},
    },
    "actions": {
        "build":  {"kind": "go",    "run": "build",           "desc": "Compile all packages"},
        "test":   {"kind": "shell", "run": "go test ./...",   "desc": "Run all tests"},
        "vet":    {"kind": "shell", "run": "go vet ./...",    "desc": "Run go vet"},
        "fmt":    {"kind": "shell", "run": "gofmt -w .",      "desc": "Format source files"},
        "deploy": {"kind": "go",    "run": "install",         "desc": "Install binaries into GOBIN"},
        "clean":  {"kind": "go",    "run": "clean",           "desc": "Remove build artifacts"},
    },
}
