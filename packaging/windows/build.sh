#!/usr/bin/env bash
set -Eeuo pipefail

readonly SOURCE_DIR="${SOURCE_DIR:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)}"
readonly BUILD_DIR="${BUILD_DIR:-$SOURCE_DIR/build/native}"
readonly DIST_DIR="${DIST_DIR:-$SOURCE_DIR/dist}"
: "${VERSION:?Run packaging/ci/build.sh windows or provide VERSION}"
: "${VERSION_NUM:?Provide VERSION_NUM (four numeric components)}"
readonly GIT_TAG="${GIT_TAG:-dev}"
readonly JOBS="${JOBS:-$(nproc)}"

TOOL=()
case "$(uname -s)" in
    MSYS*|MINGW*)
        readonly TOOL_BIN="${MSYS2_BIN:-/ucrt64/bin}"
        export PATH="$TOOL_BIN:$PATH"

        windows_path() { cygpath -m "$1"; }
        ;;
    Linux*)
        command -v linsys2 >/dev/null || {
            echo 'linsys2 is required: install it with yay -S linsys2' >&2
            exit 1
        }
        readonly TOOL_BIN="${LINSYS2_BIN:-$HOME/.local/share/linsys2/ucrt64/ucrt64/bin}"
        [[ -d "$TOOL_BIN" ]] || {
            echo "LinSYS2 UCRT64 environment not found at $TOOL_BIN; run: linsys2-pacman init" >&2
            exit 1
        }
        TOOL=(linsys2 run --)
        windows_path() { printf 'Z:%s\n' "$1"; }
        ;;
    *) echo 'Windows builds require Linux/LinSYS2 or native MSYS2.' >&2; exit 2 ;;
esac
for tool in cmake.exe windeployqt6.exe makensis.exe ntldd.exe; do
    [[ -f "$TOOL_BIN/$tool" ]] || {
        echo "Missing Windows build tool: $TOOL_BIN/$tool" >&2
        echo 'Install the Windows build dependencies listed in qt/README.md.' >&2
        exit 1
    }
done
command -v go >/dev/null || { echo 'Go must be available on PATH.' >&2; exit 1; }

readonly winbuild="$BUILD_DIR/windows"
readonly windist="$winbuild/dist"
readonly output="$DIST_DIR/DDetector-$VERSION-Setup.exe"
readonly source_win="$(windows_path "$SOURCE_DIR")"
readonly build_win="$(windows_path "$winbuild")"
readonly prefix_win="$(windows_path "${TOOL_BIN%/bin}")"
readonly output_win="$(windows_path "$output")"
rm -rf "${windist:?}" "$output"
mkdir -p "$windist" "$DIST_DIR"

GOOS=windows GOARCH=amd64 go build -C "$SOURCE_DIR" -trimpath -ldflags='-s -w -H=windowsgui' \
    -o "$windist/ddetector.exe" ./cmd/ddetector

# Go above needs MSYS path conversion; the Qt/NSIS commands use explicit Windows paths.
export MSYS2_ARG_CONV_EXCL='*'

"${TOOL[@]}" cmake.exe -S "$source_win/qt" -B "$build_win/qt" --preset release \
    "-DCMAKE_PREFIX_PATH=$prefix_win" \
    "-DDDETECTOR_GIT_TAG=$GIT_TAG"
"${TOOL[@]}" cmake.exe --build "$build_win/qt" --parallel "$JOBS"
cp "$winbuild/qt/ddetector-qt.exe" "$windist/"

"${TOOL[@]}" windeployqt6.exe --release --qmldir "$source_win/qt/qml" \
    --compiler-runtime "$build_win/dist/ddetector-qt.exe"

# Keep Qt's lookup paths relative to the installed executable, not the build prefix.
printf '[Paths]\nPrefix=.\nPlugins=.\nQmlImports=qml\n' > "$windist/qt.conf"

# Plugins loaded at runtime (including FFmpeg and QML) are not EXE imports.
while IFS= read -r -d '' executable; do
    report="$("${TOOL[@]}" ntldd.exe -R "$(windows_path "$executable")" 2>/dev/null)"
    if grep -q 'not found' <<<"$report"; then
        printf '%s\n' "$report" >&2
        echo "Unresolved Windows runtime dependency in $executable" >&2
        exit 1
    fi
    while IFS= read -r dependency; do
        [[ -n "$dependency" ]] || continue
        if [[ -f "$TOOL_BIN/$dependency" && ! -f "$windist/$dependency" ]]; then
            cp "$TOOL_BIN/$dependency" "$windist/"
        fi
    done < <(awk '/=>/ {print $1}' <<<"$report" | sort -u)
done < <(find "$windist" -type f \( -iname '*.exe' -o -iname '*.dll' \) -print0)

"${TOOL[@]}" makensis.exe \
    "-DVERSION=$VERSION" \
    "-DVERSION_NUM=$VERSION_NUM" \
    "-DDIST_DIR=$build_win/dist" \
    "-DOUTPUT_FILE=$output_win" \
    "$source_win/packaging/windows/installer.nsi"
test -s "$output"
file "$output"
