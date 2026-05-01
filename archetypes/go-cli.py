"""'go-cli' archetype — Go command-line tool with standard go toolchain commands."""

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
        "build":  {"kind": "shell", "run": "go build ./...",   "desc": "Compile all packages"},
        "test":   {"kind": "shell", "run": "go test ./...",    "desc": "Run all tests"},
        "vet":    {"kind": "shell", "run": "go vet ./...",     "desc": "Run go vet"},
        "fmt":    {"kind": "shell", "run": "gofmt -w .",       "desc": "Format source files"},
        "deploy": {"kind": "shell", "run": "go install ./...", "desc": "Install binaries into GOBIN"},
        "clean":  {"kind": "shell", "run": "go clean ./...",   "desc": "Remove build artifacts"},
    },
}
