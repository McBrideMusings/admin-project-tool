#!/bin/bash
# Demo dev server: prints the kind of log a real one does, on a fixed schedule.
. "$(dirname "$0")/lib.sh"
emit INFO vite "ready in 312 ms" 0.15
emit INFO api "listening on http://localhost:3000" 0.15
emit INFO db "connected to postgres, pool 10" 0.2
emit DEBUG auth "session store warmed, 214 keys" 0.2
emit INFO api "GET /v1/projects 200 14ms" 0.2
emit INFO api "GET /v1/projects/atlas/builds 200 22ms" 0.25
emit WARN cache "miss ratio 0.41, above 0.35 target" 0.25
emit INFO api "POST /v1/deploys 201 87ms" 0.2
emit INFO queue "job deploy:4812 picked up" 0.25
emit ERROR queue "job deploy:4811 retry 1/3 after timeout" 0.25
emit INFO api "GET /v1/deploys/4812 200 9ms" 0.3
n=0
while true; do
  n=$((n + 1))
  emit INFO api "GET /v1/health 200 3ms" 0.4
  emit INFO queue "job deploy:48$((12 + n)) done in 1.$((n % 9))s" 0.5
done
