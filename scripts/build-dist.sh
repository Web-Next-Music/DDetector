#!/usr/bin/env bash
set -Eeuo pipefail

readonly TARGET="${1:-all}"
readonly ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly BUILDER="$ROOT/packaging/ci/build.sh"

case "$TARGET" in
    all)
        rm -rf "$ROOT/dist"
        "$BUILDER" linux
        "$BUILDER" windows
        ;;
    linux|arch|deb|appimage|windows)
        "$BUILDER" "$TARGET"
        ;;
    *)
        echo "Usage: $0 {all|linux|arch|deb|appimage|windows}" >&2
        exit 2
        ;;
esac
