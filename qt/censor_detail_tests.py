#!/usr/bin/env python3
"""Compile/run detail regressions using an already-built native-ui target."""
import argparse
import os
import shlex
import subprocess
from pathlib import Path

parser = argparse.ArgumentParser()
parser.add_argument("--build-dir", type=Path, required=True)
args = parser.parse_args()
build = args.build_dir.resolve()
source = Path(__file__).resolve().with_suffix(".cpp")
target = build / "CMakeFiles/ddetector-qt-tests.dir"
flags = {}
for line in (target / "flags.make").read_text().splitlines():
    if " = " in line:
        key, value = line.split(" = ", 1)
        flags[key] = shlex.split(value)
link = shlex.split((target / "link.txt").read_text())
obj = build / "censor_detail_tests.o"
subprocess.run(
    [link[0], *flags["CXX_DEFINES"], *flags["CXX_INCLUDES"], *flags["CXX_FLAGS"],
     "-c", str(source), "-o", str(obj)], cwd=build, check=True, timeout=120)
old = "CMakeFiles/ddetector-qt-tests.dir/tests.cpp.o"
if old not in link:
    raise RuntimeError("Native-ui link command has no test entry-point object")
link[link.index(old)] = str(obj)
executable = build / "censor_detail_tests"
link[link.index("-o") + 1] = str(executable)
subprocess.run(link, cwd=build, check=True, timeout=120)
env = dict(os.environ, QT_QPA_PLATFORM="offscreen")
subprocess.run([str(executable)], env=env, check=True, timeout=30)
