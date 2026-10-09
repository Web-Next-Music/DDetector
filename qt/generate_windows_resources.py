"""Regenerate the Go launcher's Windows icon resources using LLVM tools."""
from pathlib import Path
import subprocess
import tempfile


def main():
    qt = Path(__file__).resolve().parent
    launcher = qt.parent / "cmd" / "ddetector"
    with tempfile.TemporaryDirectory() as temporary:
        resource = Path(temporary) / "app-icon.res"
        subprocess.run(
            ["llvm-rc", "/I", str(qt), "/FO", str(resource), str(qt / "app-icon.rc")],
            cwd=qt,
            check=True,
        )
        for architecture, machine in (("amd64", "X64"), ("386", "X86"), ("arm64", "ARM64")):
            output = launcher / f"app_icon_windows_{architecture}.syso"
            subprocess.run(
                ["llvm-cvtres", f"/MACHINE:{machine}", f"/OUT:{output}", str(resource)],
                check=True,
            )


if __name__ == "__main__":
    main()
