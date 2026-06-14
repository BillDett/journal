#!/usr/bin/env sh
set -eu

cd "$(dirname "$0")/.."
VERSION=$(scripts/version.sh)
CLEAN_FLAG="-clean"

if [ "${CLEAN:-1}" = "0" ]; then
  CLEAN_FLAG=""
fi

if [ "${NSIS:-0}" = "1" ]; then
  wails build $CLEAN_FLAG -platform windows/amd64 -nsis -ldflags "-X main.appVersion=$VERSION"
else
  wails build $CLEAN_FLAG -platform windows/amd64 -ldflags "-X main.appVersion=$VERSION"
fi
