"""Regression coverage for selecting a relocatable distribution FFmpeg."""

import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest


@unittest.skipUnless(shutil.which("bash") and shutil.which("readelf"), "Linux build tools required")
class FFmpegSelectionTest(unittest.TestCase):
    def test_loader_validation(self):
        source = (Path(__file__).parent / "ci" / "build.sh").read_text()
        start = source.index('    ffmpeg="${FFMPEG:')
        end = source.index('    qtermwidget_data=', start)
        script = 'set -eu\ncheck() {\n' + source[start:end] + '\n}\ncheck\n'
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            ffmpeg = root / "ffmpeg"
            ffmpeg.write_text("#!/bin/sh\nexit 0\n")
            ffmpeg.chmod(0o755)
            readelf = root / "readelf"
            env = dict(os.environ, PATH=directory + os.pathsep + os.environ["PATH"], FFMPEG=str(ffmpeg))
            for loader, accepted in (
                ("/home/linuxbrew/.linuxbrew/lib/ld.so", False),
                ("/lib64/ld-linux-x86-64.so.2", True),
                ("/lib/x86_64-linux-gnu/ld-linux-x86-64.so.2", True),
            ):
                with self.subTest(loader=loader):
                    readelf.write_text("#!/bin/sh\nprintf '%s\\n' '[Requesting program interpreter: " + loader + "]'\n")
                    readelf.chmod(0o755)
                    result = subprocess.run(["bash", "-c", script], env=env, capture_output=True, text=True)
                    self.assertEqual(result.returncode == 0, accepted, result.stderr)
                    if not accepted:
                        self.assertIn("non-portable ELF loader", result.stderr)


if __name__ == "__main__":
    unittest.main()
