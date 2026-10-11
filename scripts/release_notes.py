#!/usr/bin/env python3
"""Generate grouped Markdown release notes from Conventional Commit subjects."""

from __future__ import annotations

import argparse
import re
import subprocess
from collections import OrderedDict
from pathlib import Path

SECTIONS = OrderedDict(
    [
        ("feat", "✨ Features"),
        ("fix", "🐛 Bug fixes"),
        ("perf", "⚡ Performance"),
        ("refactor", "♻️ Refactoring"),
        ("docs", "📚 Documentation"),
        ("build", "📦 Build and dependencies"),
        ("ci", "👷 CI/CD"),
        ("other", "🔧 Other changes"),
    ]
)
CONVENTIONAL = re.compile(r"^(?P<type>[a-z]+)(?:\((?P<scope>[^)]+)\))?(?P<breaking>!)?:\s*(?P<title>.+)$")


def git(*args: str) -> str:
    return subprocess.check_output(["git", *args], text=True).strip()


def previous_tag(tag: str) -> str | None:
    try:
        value = subprocess.check_output(
            ["git", "describe", "--tags", "--abbrev=0", f"{tag}^"],
            text=True,
            stderr=subprocess.DEVNULL,
        ).strip()
    except subprocess.CalledProcessError:
        return None
    return value or None


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--tag", required=True)
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()

    previous = previous_tag(args.tag)
    revision = f"{previous}..{args.tag}" if previous else args.tag
    repository = git("config", "--get", "remote.origin.url")
    repository = re.sub(r"\.git$", "", repository)
    repository = re.sub(r"^git@github\.com:", "https://github.com/", repository)

    records = git("log", "--no-merges", "--format=%H%x09%s", revision).splitlines()
    grouped: dict[str, list[str]] = {key: [] for key in SECTIONS}
    breaking: list[str] = []

    for record in records:
        if not record:
            continue
        commit, subject = record.split("\t", 1)
        subject = subject.translate({0x2013: "-", 0x2014: "-", 0x2212: "-"})
        match = CONVENTIONAL.match(subject)
        kind = match.group("type") if match and match.group("type") in SECTIONS else "other"
        if match:
            title = match.group("title")
            scope = match.group("scope")
            if scope:
                title = f"**{scope}:** {title}"
            if match.group("breaking"):
                breaking.append(f"- {title} ([`{commit[:7]}`]({repository}/commit/{commit}))")
        else:
            title = subject
        grouped[kind].append(f"- {title} ([`{commit[:7]}`]({repository}/commit/{commit}))")

    lines = [f"# DDetector {args.tag}", ""]
    if breaking:
        lines.extend(["## ⚠️ Breaking changes", "", *breaking, ""])
    for kind, heading in SECTIONS.items():
        if grouped[kind]:
            lines.extend([f"## {heading}", "", *grouped[kind], ""])

    if previous:
        lines.extend([f"**Full changelog:** [{previous}...{args.tag}]({repository}/compare/{previous}...{args.tag})", ""])
    else:
        lines.extend(["First published release.", ""])

    args.output.write_text("\n".join(lines), encoding="utf-8")


if __name__ == "__main__":
    main()
