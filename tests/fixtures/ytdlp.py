#!/usr/bin/env python3
"""Offline yt-dlp contract fixture copying a real generated H.264 clip."""
import shutil
import sys
from pathlib import Path

args = sys.argv[1:]
if "--version" in args:
    print("yt-dlp contract fixture (offline)")
    sys.exit(0)
cookie = Path(args[args.index("--cookies") + 1])
source = cookie.parent.parent / "fixtures" / "video.mp4"
target = Path(args[args.index("-o") + 1].replace("%(ext)s", "mp4"))
shutil.copyfile(source, target)
