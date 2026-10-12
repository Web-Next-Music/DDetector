#!/usr/bin/env python3
"""Run distribution builds using the existing Bash packaging pipeline."""

import argparse
import os
import shutil
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
TARGETS = ("all", "linux", "arch", "deb", "appimage", "windows")


def build_command(target):
    builder = ROOT / "packaging" / "ci" / "build.sh"
    env = os.environ.copy()
    if sys.platform == "win32":
        # Use MSYS2 Bash, not Git Bash or WSL: the build needs UCRT64 tools.
        msys_root = Path(env.get("MSYS2_ROOT", "C:/msys64"))
        bash = msys_root / "usr" / "bin" / "bash.exe"
        if not bash.is_file():
            raise RuntimeError(
                f"MSYS2 Bash not found: {bash}. Install MSYS2 or set MSYS2_ROOT."
            )
        env["MSYSTEM"] = "UCRT64"
        env["CHERE_INVOKING"] = "1"
        return [
            str(bash), "--login", "-c",
            'exec bash "$(cygpath -u "$1")" "$2"',
            "build-dist", str(builder), target,
        ], env
    bash = shutil.which("bash")
    if not bash:
        raise RuntimeError("Bash is required by the packaging pipeline.")
    return [bash, str(builder), target], env


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "target", nargs="?", choices=TARGETS,
        default="windows" if sys.platform == "win32" else "all",
    )
    args = parser.parse_args()
    if sys.platform == "win32" and args.target != "windows":
        parser.error("Only the windows target is supported on Windows; use Linux for Linux packages.")
    targets = ("linux", "windows") if args.target == "all" else (args.target,)
    try:
        commands = [build_command(target) for target in targets]
        if args.target == "all" and (ROOT / "dist").exists():
            shutil.rmtree(ROOT / "dist")
        for command, env in commands:
            subprocess.run(command, cwd=ROOT, env=env, check=True)
    except subprocess.CalledProcessError as error:
        return error.returncode if error.returncode > 0 else 1
    except (OSError, RuntimeError) as error:
        print(f"Build failed: {error}", file=sys.stderr)
        return 1
    except KeyboardInterrupt:
        return 130
    return 0


if __name__ == "__main__":
    sys.exit(main())
