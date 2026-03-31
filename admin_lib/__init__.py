"""admin_lib — shared infrastructure for per-project admin task runners."""

__version__ = "0.1.0"

from .core import (
    C,
    info,
    ok,
    warn,
    err,
    run_cmd,
    sync_deploy,
    command,
    boot,
    pick_target,
    dispatch_target,
    register_cleanup,
)
from .logs import LogFormatter, make_formatter
