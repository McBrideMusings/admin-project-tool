#!/usr/bin/env python3
"""admin -- project task runner for {{PROJECT_NAME}}."""

import sys, os
from collections import OrderedDict
sys.path.insert(0, os.path.expanduser("~/.admin"))
from admin_lib.core import (
    core, command, run_cmd, boot, dispatch_target,
    C, info, ok, warn, err, sync_deploy,
)
{{EXTRA_IMPORTS}}

PROJECT_NAME = "{{PROJECT_NAME}}"
