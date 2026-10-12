#!/usr/bin/env bash
set -Eeuo pipefail

readonly TARGET="${1:-linux}"
readonly SOURCE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
readonly DIST_DIR="$SOURCE_DIR/dist"
readonly BUILD_DIR="$SOURCE_DIR/build/native"
readonly VERSION="${VERSION:-$(git -C "$SOURCE_DIR" describe --tags --always --dirty 2>/dev/null | sed 's/^v//' || printf '0.0.0')}"
readonly GIT_TAG="${GIT_TAG:-$(git -C "$SOURCE_DIR" describe --tags --exact-match 2>/dev/null || printf 'dev')}"
readonly VERSION_NUM="$(printf '%s\n' "$VERSION" | awk -F. '
    /^[0-9]+\.[0-9]+\.[0-9]+([.+-]|$)/ {
        if ($1 <= 65535 && $2 <= 65535 && $3 + 0 <= 65535) {
            printf "%d.%d.%d.0", $1, $2, $3
            valid = 1
        }
    }
    END { if (!valid) printf "0.0.0.0" }
')"
readonly ARCH_VERSION="${VERSION//-/_}"
readonly JOBS="${JOBS:-$(nproc)}"
readonly LINSYS2_BIN="${LINSYS2_BIN:-$HOME/.local/share/linsys2/ucrt64/ucrt64/bin}"
set -a
source "$SOURCE_DIR/packaging/deps.env"
set +a
readonly TOOLS_DIR="${TOOLS_DIR:-$SOURCE_DIR/build/tools}"
readonly LINUXDEPLOY_DIR="$TOOLS_DIR/linuxdeploy-$LINUXDEPLOY_VERSION-qt-$LINUXDEPLOY_PLUGIN_QT_VERSION"
readonly LINUXDEPLOY="$LINUXDEPLOY_DIR/linuxdeploy-x86_64.AppImage"
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

fetch_tool() {
    local path="$1" url="$2" sha256="$3"
    mkdir -p "$(dirname "$path")"
    if [[ ! -f "$path" ]] || ! echo "$sha256  $path" | sha256sum --check --status; then
        curl --fail --location --retry 3 "$url" --output "$path"
        echo "$sha256  $path" | sha256sum --check --status || {
            echo "Checksum mismatch for $url; update packaging/deps.env" >&2
            rm -f "$path"
            exit 1
        }
    fi
    chmod +x "$path"
}

build_linux() {
    cmake -S "$SOURCE_DIR/qt" -B "$BUILD_DIR/linux" --preset release "-DDDETECTOR_GIT_TAG=$GIT_TAG"
    go build -C "$SOURCE_DIR" -trimpath -ldflags='-s -w' \
        -o "$BUILD_DIR/linux/ddetector-backend" ./cmd/ddetector &
    local backend_pid=$!
    cmake --build "$BUILD_DIR/linux" --parallel "$JOBS"
    wait "$backend_pid"
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

    fetch_tool "$LINUXDEPLOY" \
        "https://github.com/linuxdeploy/linuxdeploy/releases/download/$LINUXDEPLOY_VERSION/linuxdeploy-x86_64.AppImage" \
        "$LINUXDEPLOY_SHA256"
    fetch_tool "$LINUXDEPLOY_DIR/linuxdeploy-plugin-qt-x86_64.AppImage" \
        "https://github.com/linuxdeploy/linuxdeploy-plugin-qt/releases/download/$LINUXDEPLOY_PLUGIN_QT_VERSION/linuxdeploy-plugin-qt-x86_64.AppImage" \
        "$LINUXDEPLOY_PLUGIN_QT_SHA256"

    QMAKE="${QMAKE:-$(command -v qmake6)}" \
    QML_SOURCES_PATHS="$SOURCE_DIR/qt/qml" \
    EXTRA_QT_PLUGINS='svg;webp' \
    NO_STRIP=1 \
    APPIMAGE_EXTRACT_AND_RUN=1 \
        "$LINUXDEPLOY" --appimage-extract-and-run \
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
        "$LINUXDEPLOY" --appimage-extract-and-run \
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
    dpkg-deb -Zzstd --root-owner-group --build "$root" "$output"
    dpkg-deb --info "$output"
}

build_windows() {
    env VERSION="$VERSION" VERSION_NUM="$VERSION_NUM" GIT_TAG="$GIT_TAG" JOBS="$JOBS" \
        SOURCE_DIR="$SOURCE_DIR" BUILD_DIR="$BUILD_DIR" DIST_DIR="$DIST_DIR" \
        LINSYS2_BIN="$LINSYS2_BIN" bash "$SOURCE_DIR/packaging/windows/build.sh"
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
