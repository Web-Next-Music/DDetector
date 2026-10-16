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
for tool in cmake.exe windeployqt6.exe makensis.exe ntldd.exe ffmpeg.exe; do
    [[ -f "$TOOL_BIN/$tool" ]] || {
        echo "Missing Windows build tool: $TOOL_BIN/$tool" >&2
        echo 'Install the Windows build dependencies listed in qt/README.md.' >&2
        exit 1
    }
done
command -v go >/dev/null || { echo 'Go must be available on PATH.' >&2; exit 1; }
command -v python3 >/dev/null || { echo 'Python 3 must be available on PATH.' >&2; exit 1; }

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
    -DBUILD_TESTING=OFF \
    "-DCMAKE_PREFIX_PATH=$prefix_win" \
    "-DDDETECTOR_GIT_TAG=$GIT_TAG"
"${TOOL[@]}" cmake.exe --build "$build_win/qt" --parallel "$JOBS"
cp "$winbuild/qt/ddetector-qt.exe" "$windist/"
cp "$TOOL_BIN/ffmpeg.exe" "$windist/"

"${TOOL[@]}" windeployqt6.exe --release --qmldir "$source_win/qt/qml" \
    --compiler-runtime "$build_win/dist/ddetector-qt.exe"

# Keep Qt's lookup paths relative to the installed executable, not the build prefix.
printf '[Paths]\nPrefix=.\nPlugins=.\nQmlImports=qml\n' > "$windist/qt.conf"

# Allow MSYS2 path conversion when python3 is a native Windows executable.
MSYS2_ARG_CONV_EXCL= python3 "$SOURCE_DIR/packaging/prune-qt-runtime.py" windows "$windist"

# Seed every runtime plugin as well as the EXEs; traverse direct imports only.
readonly dependency_root="$(cd "$windist" && pwd)"
declare -a dependency_queue=()
declare -A dependency_seen=()
while IFS= read -r -d '' executable; do
    dependency_queue+=("$executable")
done < <(find "$dependency_root" -type f \( -iname '*.exe' -o -iname '*.dll' \) -print0)
deployment_started=$SECONDS
scanned=0
for ((next=0; next<${#dependency_queue[@]}; next++)); do
    executable="${dependency_queue[next]}"
    key="${executable//\\//}"
    key="${key,,}"
    [[ -z "${dependency_seen[$key]:-}" ]] || continue
    dependency_seen[$key]=1
    if ! report="$("${TOOL[@]}" ntldd.exe "$(windows_path "$executable")")"; then
        printf '%s\n' "$report" >&2
        echo "Windows dependency scan failed for $executable" >&2
        exit 1
    fi
    if grep -qi 'not[[:space:]]\+found' <<<"$report"; then
        printf '%s\n' "$report" >&2
        echo "Unresolved Windows runtime dependency in $executable" >&2
        exit 1
    fi
    scanned=$((scanned + 1))
    while IFS= read -r import; do
        [[ "$import" == *'=>'* ]] || continue
        read -r dependency _ <<<"$import"
        resolved="${import#*=>}"
        resolved="${resolved//\\//}"
        resolved="${resolved,,}"
        # Windows supplies these DLLs; never ship them from the toolchain.
        [[ "$resolved" =~ /windows/(system32|syswow64|winsxs)/ ]] && continue
        if [[ -f "$TOOL_BIN/$dependency" ]]; then
            if [[ ! -f "$windist/$dependency" ]]; then
                cp "$TOOL_BIN/$dependency" "$windist/"
            fi
            # Already deployed does not mean already scanned.
            dependency_queue+=("$dependency_root/$dependency")
        fi
    done <<<"$report"
done
printf 'Windows dependencies: scanned %d binaries in %ds\n' "$scanned" "$((SECONDS - deployment_started))"
"${TOOL[@]}" "$(windows_path "$windist/ffmpeg.exe")" -version >/dev/null

"${TOOL[@]}" makensis.exe \
    "-DVERSION=$VERSION" \
    "-DVERSION_NUM=$VERSION_NUM" \
    "-DDIST_DIR=$build_win/dist" \
    "-DOUTPUT_FILE=$output_win" \
    "$source_win/packaging/windows/installer.nsi"
test -s "$output"
file "$output"
