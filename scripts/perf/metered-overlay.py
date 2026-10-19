"""Prepare an opt-in Go overlay; production Open and go.mod remain unchanged."""

import argparse
import json
import os
from pathlib import Path

os.umask(0o077)
parser = argparse.ArgumentParser()
parser.add_argument("directory", type=Path)
args = parser.parse_args()
directory = args.directory.resolve()
directory.mkdir(mode=0o700, parents=True, exist_ok=True)
if directory.stat().st_mode & 0o077:
    raise RuntimeError("Private directory required")
source = Path(__file__).resolve().parents[2] / "internal/infrastructure/sqlite/open.go"
original = source.read_text()
needle = 'sql.Open("sqlite", dsn)'
if original.count(needle) != 1:
    raise RuntimeError("Source differs; review instrumentation before benchmarking")
replacement = directory / "open-metered.go"
with replacement.open("x") as handle:
    handle.write(original.replace(needle, 'sql.Open("deskperf_sqlite", dsn)'))
overlay = directory / "overlay.json"
with overlay.open("x") as handle:
    json.dump({"Replace": {str(source): str(replacement)}}, handle)
print(overlay)
