"""'go-cli' archetype — Go command-line tool with standard go toolchain commands."""

ARCHETYPE = {
    "name": "go-cli",
    "modules": ["core"],
    "urls": {},
    "commands": {
        "build": {
            "run": "go build ./...",
            "desc": "Compile all packages",
        },
        "test": {
            "run": "go test ./...",
            "desc": "Run all tests",
        },
        "vet": {
            "run": "go vet ./...",
            "desc": "Run go vet",
        },
        "fmt": {
            "run": "gofmt -w .",
            "desc": "Format source files",
        },
        "deploy": {
            "run": "go install ./...",
            "desc": "Install binaries into GOBIN",
        },
        "clean": {
            "run": "go clean ./...",
            "desc": "Remove build artifacts",
        },
    },
}
