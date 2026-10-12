## Linux

### Requirements

- `Go` 1.23 or later;
- `CMake` 3.21+, `Ninja`, and a `C++17` compiler;
- `Qt` 6.9+ with the `Quick`, `QuickControls2`, `Network`, `Multimedia`, `Widgets`, and `Concurrent` modules, plus `qmllint`;
- `qtermwidget6` 2.2.0+, `KF6SyntaxHighlighting` 6.13.0+, and `Qt6Keychain`;
- `libgit2` 1.5+ and `pkg-config`.

Install development packages; see `packaging/deps.env` for versions. For a non-system Qt installation, set `-DCMAKE_PREFIX_PATH=...` in CMake.

### Build and Run

```sh
mkdir -p build
go build -o build/ddetector-backend ./cmd/ddetector
cmake -S qt --preset release
cmake --build qt/build-release --parallel
./build/ddetector-backend --qt ./qt/build-release/ddetector-qt
```

### Development

```sh
cmake -S qt --preset dev
cmake --build qt/build --parallel
./build/ddetector-backend --qt ./qt/build/ddetector-qt
```

## Windows

### Requirements

Install `Go` 1.23+ (available on `PATH`) and `MSYS2`. Use the **MSYS2 UCRT64** terminal, not Git Bash or WSL.

Update MSYS2 with `pacman -Syu`. If prompted to restart the terminal, reopen it and repeat the update. Then install dependencies:

```sh
pacman -S --needed \
  git \
  mingw-w64-ucrt-x86_64-gcc \
  mingw-w64-ucrt-x86_64-ccache \
  mingw-w64-ucrt-x86_64-cmake \
  mingw-w64-ucrt-x86_64-ninja \
  mingw-w64-ucrt-x86_64-pkgconf \
  mingw-w64-ucrt-x86_64-qt6-base \
  mingw-w64-ucrt-x86_64-qt6-declarative \
  mingw-w64-ucrt-x86_64-qt6-multimedia \
  mingw-w64-ucrt-x86_64-qt6-svg \
  mingw-w64-ucrt-x86_64-qt6-imageformats \
  mingw-w64-ucrt-x86_64-syntax-highlighting \
  mingw-w64-ucrt-x86_64-qtkeychain \
  mingw-w64-ucrt-x86_64-libgit2 \
  mingw-w64-ucrt-x86_64-ntldd \
  mingw-w64-ucrt-x86_64-nsis
```

### Build and Run

Run from the repository root in **MSYS2 UCRT64**. Adjust the prefix if MSYS2 is not installed at `C:\msys64`:

```sh
mkdir -p build
go build -o build/ddetector-backend.exe ./cmd/ddetector
cmake -S qt --preset release -DCMAKE_PREFIX_PATH=C:/msys64/ucrt64
cmake --build qt/build-release --parallel
./build/ddetector-backend.exe --qt ./qt/build-release/ddetector-qt.exe
```

Keep using the UCRT64 terminal to launch this build so its runtime DLLs are available on `PATH`.

### Development

After building the Go backend, run in **MSYS2 UCRT64**:

```sh
cmake -S qt --preset dev -DCMAKE_PREFIX_PATH=C:/msys64/ucrt64
cmake --build qt/build --parallel
./build/ddetector-backend.exe --qt ./qt/build/ddetector-qt.exe
```

### Build an Installer

Install `Python`, then run from the repository root in **PowerShell**:

```powershell
py scripts/build-dist.py windows
```
