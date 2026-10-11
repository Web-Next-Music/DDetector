#!/usr/bin/env bash
set -Eeuo pipefail

readonly TARGET="${1:-linux}"
readonly SOURCE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
readonly DIST_DIR="$SOURCE_DIR/dist"
readonly BUILD_DIR="$SOURCE_DIR/build/native"
readonly VERSION="${VERSION:-$(git -C "$SOURCE_DIR" describe --tags --always --dirty 2>/dev/null | sed 's/^v//' || printf '0.0.0')}"
readonly VERSION_NUM="$(printf '%s' "$VERSION" | sed -E 's/-.*//; s/[^0-9.].*$//' | awk -F. '{ printf "%d.%d.%d", $1, $2, $3 }')"
readonly ARCH_VERSION="${VERSION//-/_}"
readonly JOBS="${JOBS:-$(nproc)}"
readonly MSYS2_DIR="${MSYS2_DIR:-/opt/msys64}"
APPDIR_READY=0

case "$TARGET" in
    linux|arch|deb|appimage|windows) ;;
    *) echo "Usage: $0 {linux|arch|deb|appimage|windows}" >&2; exit 2 ;;
esac
if [[ ! "$VERSION" =~ ^[0-9A-Za-z.+_-]+$ ]]; then
    echo "Invalid VERSION: $VERSION" >&2
    exit 2
fi

mkdir -p "$DIST_DIR" "$BUILD_DIR"

build_linux() {
    if [[ -x "$BUILD_DIR/linux/ddetector-qt" && -x "$BUILD_DIR/linux/ddetector-backend" ]]; then
        return
    fi
    local cmake_args=(
        -S "$SOURCE_DIR/qt" -B "$BUILD_DIR/linux" -G Ninja
        -DCMAKE_BUILD_TYPE=Release -DBUILD_TESTING=OFF
    )
    if command -v ccache >/dev/null; then
        cmake_args+=(-DCMAKE_CXX_COMPILER_LAUNCHER=ccache)
    fi
    cmake "${cmake_args[@]}"
    cmake --build "$BUILD_DIR/linux" --parallel "$JOBS"
    go build -C "$SOURCE_DIR" -trimpath -ldflags='-s -w' \
        -o "$BUILD_DIR/linux/ddetector-backend" ./cmd/ddetector
}

build_appdir() {
    if [[ "$APPDIR_READY" == 1 ]]; then
        return
    fi
    build_linux
    local appdir="$BUILD_DIR/AppDir"
    local qtermwidget_data syntax_data
    qtermwidget_data="${QTERMWIDGET_DATA_DIR:-/usr/share/qtermwidget6}"
    syntax_data="${SYNTAX_HIGHLIGHTING_DATA_DIR:-/usr/share/org.kde.syntax-highlighting}"
    rm -rf "$appdir"
    install -Dm755 "$BUILD_DIR/linux/ddetector-backend" "$appdir/usr/lib/ddetector/ddetector-backend"
    install -Dm755 "$BUILD_DIR/linux/ddetector-qt" "$appdir/usr/lib/ddetector/ddetector-qt"
    install -Dm644 "$SOURCE_DIR/qt/ddetector-qt.desktop" "$appdir/usr/share/applications/ddetector-qt.desktop"
    sed -i 's/^Exec=.*/Exec=ddetector/' "$appdir/usr/share/applications/ddetector-qt.desktop"
    install -Dm644 "$SOURCE_DIR/qt/assets/icon-256.png" "$appdir/usr/share/icons/hicolor/256x256/apps/ddetector-qt.png"
    install -Dm755 "$SOURCE_DIR/packaging/linux/AppRun" "$appdir/AppRun"
    cp -a "$qtermwidget_data" "$appdir/usr/share/"
    cp -a "$syntax_data" "$appdir/usr/share/"
    ln -s usr/share/applications/ddetector-qt.desktop "$appdir/ddetector-qt.desktop"
    ln -s usr/share/icons/hicolor/256x256/apps/ddetector-qt.png "$appdir/ddetector-qt.png"

    local linuxdeploy="$BUILD_DIR/linuxdeploy-x86_64.AppImage"
    local qt_plugin="$BUILD_DIR/linuxdeploy-plugin-qt-x86_64.AppImage"
    [[ -x "$linuxdeploy" ]] || curl --fail --location --retry 3 \
        https://github.com/linuxdeploy/linuxdeploy/releases/download/continuous/linuxdeploy-x86_64.AppImage \
        --output "$linuxdeploy"
    [[ -x "$qt_plugin" ]] || curl --fail --location --retry 3 \
        https://github.com/linuxdeploy/linuxdeploy-plugin-qt/releases/download/continuous/linuxdeploy-plugin-qt-x86_64.AppImage \
        --output "$qt_plugin"
    chmod +x "$linuxdeploy" "$qt_plugin"

    QMAKE="${QMAKE:-$(command -v qmake6)}" \
    QML_SOURCES_PATHS="$SOURCE_DIR/qt/qml" \
    EXTRA_QT_PLUGINS='svg;webp' \
    NO_STRIP=1 \
    APPIMAGE_EXTRACT_AND_RUN=1 \
        "$linuxdeploy" --appimage-extract-and-run \
        --appdir "$appdir" \
        --executable "$appdir/usr/lib/ddetector/ddetector-qt" \
        --desktop-file "$appdir/usr/share/applications/ddetector-qt.desktop" \
        --icon-file "$SOURCE_DIR/qt/assets/icon-256.png" \
        --plugin qt
    install -Dm755 "$SOURCE_DIR/packaging/linux/AppRun" "$appdir/AppRun"
    APPDIR_READY=1
}

build_appimage() {
    build_appdir
    local output="$DIST_DIR/DDetector-$VERSION-x86_64.AppImage"
    rm -f "$output"
    OUTPUT="$output" APPIMAGE_EXTRACT_AND_RUN=1 \
        "$BUILD_DIR/linuxdeploy-x86_64.AppImage" --appimage-extract-and-run \
        --appdir "$BUILD_DIR/AppDir" --output appimage
    file "$output"
}

build_arch() {
    build_appdir
    local root="$BUILD_DIR/arch/root"
    local output="$DIST_DIR/ddetector-$ARCH_VERSION-1-x86_64.pkg.tar.zst"
    rm -rf "$root" "$output"
    mkdir -p "$root/opt" "$root/usr/bin"
    cp -a "$BUILD_DIR/AppDir" "$root/opt/ddetector"
    ln -s /opt/ddetector/AppRun "$root/usr/bin/ddetector"
    cat > "$root/.PKGINFO" <<EOF
pkgname = ddetector
pkgbase = ddetector
pkgver = $ARCH_VERSION-1
pkgdesc = Music library and duplicate detector
url = https://github.com/web-next-music/ddetector-go
builddate = $(date +%s)
packager = DDetector native build
size = $(du -sb "$root" | cut -f1)
arch = x86_64
depend = glibc
EOF
    bsdtar --zstd -cf "$output" -C "$root" .PKGINFO opt usr
    file "$output"
}

build_deb() {
    build_appdir
    local root="$BUILD_DIR/deb/root"
    local output="$DIST_DIR/ddetector_${VERSION}_amd64.deb"
    rm -rf "$root" "$output"
    mkdir -p "$root/DEBIAN" "$root/opt" "$root/usr/bin"
    cp -a "$BUILD_DIR/AppDir" "$root/opt/ddetector"
    ln -s /opt/ddetector/AppRun "$root/usr/bin/ddetector"
    cat > "$root/DEBIAN/control" <<EOF
Package: ddetector
Version: $VERSION
Section: sound
Priority: optional
Architecture: amd64
Installed-Size: $(du -sk "$root/opt/ddetector" | cut -f1)
Maintainer: DDetector contributors
Depends: libc6
Description: Music library and duplicate detector
 Native Qt desktop client with its local Go backend.
EOF
    dpkg-deb --root-owner-group --build "$root" "$output"
    dpkg-deb --info "$output"
}

build_windows() {
    command -v wine >/dev/null || { echo 'wine is required for the local Windows build' >&2; exit 1; }
    [[ -x "$MSYS2_DIR/usr/bin/bash.exe" ]] || {
        echo "MSYS2 bash not found at $MSYS2_DIR/usr/bin/bash.exe; set MSYS2_DIR" >&2
        exit 1
    }

    local winbuild="$BUILD_DIR/windows"
    local windist="$winbuild/dist"
    local output="$DIST_DIR/DDetector-$VERSION-Setup.exe"
    local source_win="Z:$SOURCE_DIR"
    local build_win="Z:$winbuild"
    local dist_win="Z:$DIST_DIR"
    rm -rf "$winbuild" "$output"
    mkdir -p "$windist"
    GOOS=windows GOARCH=amd64 go build -C "$SOURCE_DIR" -trimpath -ldflags='-s -w' \
        -o "$windist/ddetector.exe" ./cmd/ddetector

    cat > /tmp/ddetector-windows-build.sh <<'MSYS'
set -Eeuo pipefail
SOURCE_DIR="$1"
BUILD_DIR="$2"
DIST_DIR="$3"
VERSION_NUM="$4"
VERSION="$5"
export PATH=/ucrt64/bin:/usr/bin
cmake -S "$SOURCE_DIR/qt" -B "$BUILD_DIR/qt" -G Ninja \
    -DCMAKE_BUILD_TYPE=Release -DBUILD_TESTING=OFF -DCMAKE_PREFIX_PATH=/ucrt64
cmake --build "$BUILD_DIR/qt" --parallel
cp "$BUILD_DIR/qt/ddetector-qt.exe" "$BUILD_DIR/dist/"
windeployqt6.exe --release --qmldir "$SOURCE_DIR/qt/qml" --compiler-runtime \
    "$BUILD_DIR/dist/ddetector-qt.exe"
for pass in 1 2 3 4 5 6; do
    copied=0
    while IFS= read -r dependency; do
        name=$(basename "$dependency")
        if [[ ! -e "$BUILD_DIR/dist/$name" ]]; then
            cp "$dependency" "$BUILD_DIR/dist/"
            copied=1
        fi
    done < <(find "$BUILD_DIR/dist" -maxdepth 1 -type f \( -iname '*.exe' -o -iname '*.dll' \) -print0 \
        | xargs -0 -r ldd 2>/dev/null | awk '/=> \/ucrt64\/bin\// {print $3}' | sort -u)
    [[ $copied -eq 0 ]] && break
done
if find "$BUILD_DIR/dist" -maxdepth 1 -type f \( -iname '*.exe' -o -iname '*.dll' \) -print0 \
    | xargs -0 -r ldd 2>/dev/null | grep -q 'not found'; then
    echo 'Unresolved Windows runtime dependency' >&2
    exit 1
fi
makensis \
    -DVERSION="$VERSION_NUM" \
    -DDIST_DIR="$BUILD_DIR/dist" \
    -DOUTPUT_FILE="$DIST_DIR/DDetector-$VERSION-Setup.exe" \
    "$SOURCE_DIR/packaging/windows/installer.nsi"
MSYS
    wine "$MSYS2_DIR/usr/bin/bash.exe" -lc \
        "bash Z:/tmp/ddetector-windows-build.sh '$source_win' '$build_win' '$dist_win' '$VERSION_NUM' '$VERSION'"
    test -s "$output"
    file "$output"
}

case "$TARGET" in
    linux)
        rm -f "$DIST_DIR"/*.AppImage "$DIST_DIR"/*.deb "$DIST_DIR"/*.pkg.tar.zst
        build_appimage
        build_arch
        build_deb
        ;;
    arch) build_arch ;;
    deb) build_deb ;;
    appimage) build_appimage ;;
    windows) build_windows ;;
esac

printf '\nArtifacts in %s:\n' "$DIST_DIR"
find "$DIST_DIR" -maxdepth 1 -type f -printf '  %f\n' | sort
