#!/usr/bin/env sh
set -eu

VERSION=$(node -e "const fs=require('fs'); const data=JSON.parse(fs.readFileSync('wails.json', 'utf8')); const version=data.info && data.info.productVersion; if (!version) process.exit(1); process.stdout.write(version)")

case "$VERSION" in
  *[!0-9.]* | "" | *..* | .* | *.)
    echo "Invalid wails.json info.productVersion: $VERSION" >&2
    exit 1
    ;;
esac

printf '%s' "$VERSION"
