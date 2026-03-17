"""admin_lib — shared infrastructure for per-project admin task runners."""

__version__ = "0.1.0"

from .core import (
    C,
    info,
    ok,
    warn,
    err,
    run_cmd,
    core,
    command,
    boot,
    pick_target,
    register_cleanup,
)
