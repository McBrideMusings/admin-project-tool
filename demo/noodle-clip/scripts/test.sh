#!/bin/bash
# Demo test run: suites pass one after another.
. "$(dirname "$0")/lib.sh"
emit INFO vitest "6 suites, 214 tests" 0.2
emit INFO vitest "auth ........ 38 passed" 0.3
emit INFO vitest "billing ..... 52 passed" 0.3
emit INFO vitest "projects .... 61 passed" 0.35
emit INFO vitest "deploys ..... 44 passed" 0.3
emit INFO vitest "queue ........ 19 passed" 0.3
emit INFO vitest "214 passed in 3.8s" 0
