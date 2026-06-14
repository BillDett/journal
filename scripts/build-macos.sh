#!/usr/bin/env sh
set -eu

cd "$(dirname "$0")/.."
VERSION=$(scripts/version.sh)
CLEAN_FLAG="-clean"

if [ "${CLEAN:-1}" = "0" ]; then
  CLEAN_FLAG=""
fi

wails build $CLEAN_FLAG -platform darwin/arm64 -ldflags "-X main.appVersion=$VERSION"
