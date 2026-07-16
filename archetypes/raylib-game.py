"""'raylib-game' archetype — a CMake-built raylib game on the forge-engine cradle.

Seven games each hand-declared the same configure/build/run/smoke/clean set and
drifted apart on the details. Those details are [raylib] config:

    [raylib]
    binary      = "game-jerry-road"        # ./<build_dir>/<binary>
    build_dir   = "build"                  # default "build"
    cmake_flags = ["-DRF_HOTRELOAD=ON"]    # default none
    jobs        = 8                        # default: no -j flag
    smoke_env   = "RF_SMOKE"               # default "RF_SMOKE"
    clean_dirs  = ["build", "build-web"]   # default [build_dir]

This is the *consuming game* archetype; forge-engine is the engine repo itself.
"""

ARCHETYPE = {
    "name": "raylib-game",
    "modules": ["core"],
    "urls": {},
    "commands": {
        "build": {"desc": "Configure + build (CMake)",                 "steps": ["cmake-build"],          "group": 1, "priority": 10},
        "dev":   {"desc": "Build + run the game",                      "steps": ["cmake-build", "run"],   "group": 1, "priority": 20},
        "test":  {"desc": "Build + smoke-run the game (auto-exits)",   "steps": ["cmake-build", "smoke"], "group": 2, "priority": 10},
        "clean": {"desc": "Remove build artifacts",                    "steps": ["clean"],                "group": 2, "priority": 20},
    },
    "actions": {
        "cmake-build": {"kind": "raylib", "run": "build", "desc": "Configure + build (CMake)"},
        "run":         {"kind": "raylib", "run": "run",   "desc": "Run the built game"},
        "smoke":       {"kind": "raylib", "run": "smoke", "desc": "Smoke-run the game (auto-exits)"},
        "clean":       {"kind": "raylib", "run": "clean", "desc": "Remove build artifacts"},
    },
}
