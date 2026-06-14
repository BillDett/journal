#!/usr/bin/env sh
set -eu

cd "$(dirname "$0")/.."

scripts/build-macos.sh
CLEAN=0 scripts/build-windows.sh
