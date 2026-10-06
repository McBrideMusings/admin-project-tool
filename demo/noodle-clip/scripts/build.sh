#!/bin/bash
# Demo build: a short, steady stream that ends successfully.
. "$(dirname "$0")/lib.sh"
emit INFO tsc "checking 148 files" 0.2
emit INFO vite "transforming 612 modules" 0.3
emit INFO vite "rendering chunks" 0.15
emit WARN vite "chunk vendor-charts is 612 kB, above the 500 kB limit" 0.2
emit INFO vite "dist/assets/index-4f1c.js  218.4 kB" 0.15
emit INFO build "done in 1.4s" 0
