"""Backwards-compat shim — admin_lib.server is now admin_lib.docker.

Existing projects importing from admin_lib.server will continue to work.
New projects should import from admin_lib.docker.
"""

from .docker import *  # noqa: F401,F403
