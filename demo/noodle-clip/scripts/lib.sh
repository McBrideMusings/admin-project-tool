#!/bin/bash
# Colours a log line the way a real dev server does: a coloured level and a
# coloured scope tag, then the message. Usage: emit LEVEL scope message [sleep-seconds]
emit() {
  local level="$1" scope="$2" msg="$3" pause="${4:-0.2}" lc sc
  case "$level" in
    INFO) lc=36 ;;
    WARN) lc=33 ;;
    ERROR) lc=31 ;;
    DEBUG) lc=90 ;;
  esac
  case "$scope" in
    api) sc=35 ;;
    db) sc=34 ;;
    vite) sc=32 ;;
    queue) sc=95 ;;
    cache) sc=33 ;;
    auth) sc=94 ;;
    tsc) sc=34 ;;
    vitest) sc=92 ;;
    build) sc=96 ;;
    *) sc=37 ;;
  esac
  printf '\033[%sm[%s]\033[0m \033[%sm[%s]\033[0m %s\n' "$lc" "$level" "$sc" "$scope" "$msg"
  sleep "$pause"
}
