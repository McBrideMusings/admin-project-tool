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
    pick_target_or_none,
    dispatch_target,
    register_cleanup,
)
from .log_format import LogFormatter, make_formatter
