#!/usr/bin/env python3
"""Offline gallery-dl contract fixture. Never used by the production server."""
import json
import shutil
import sys
from pathlib import Path
from urllib.parse import urlparse

args = sys.argv[1:]
if "--version" in args:
    print("gallery-dl contract fixture (offline)")
    sys.exit(0)
cookie = Path(args[args.index("--cookies") + 1])
config = cookie.parent.parent
assets = config / "fixtures"
phase = (config / "phase").read_text().strip() if (config / "phase").exists() else "1"
url = args[-1]
if "--directory" in args:
    target = Path(args[args.index("--directory") + 1])
    shutil.copyfile(assets / "photo.png", target / "asset.png")
    sys.exit(0)
if phase in ("4", "5"):
    print(json.dumps([[-1, {"error": "AuthenticationError", "message": "401 auth_token=FIXTURE_SECRET_SHOULD_NEVER_LEAK"}]]))
    sys.exit(1 if phase == "5" else 0)
if phase == "6":
    sys.exit(0)
if phase == "7":
    print("invalid fixture output")
    sys.exit(0)
if phase == "8":
    print("[warning] rate limit 429", file=sys.stderr)
    print("[]")
    sys.exit(0)
parts = urlparse(url).path.strip("/").split("/")
if parts == ["i", "bookmarks"]:
    print("[]")
elif parts[-1] == "info":
    print(json.dumps([[2, {"id": 100, "name": parts[0], "nick": "Archive Test", "description": "Offline integration fixture", "friends_count": 3 if phase == "3" else 2, "followers_count": 3 if phase == "3" else 2}]]))
elif parts[-1] in ("following", "followers"):
    ids = ["101"] if phase == "3" else ["101", "102" if phase == "1" else "103"]
    print(json.dumps([[6, f"https://x.com/member{uid}", {"rest_id": uid, "legacy": {"screen_name": f"member{uid}", "name": f"Member {uid}"}}] for uid in ids]))
else:
    tid = parts[-1] if "status" in parts else "1234567890123456780"
    author = "archive_test" if "status" in parts else parts[0]
    meta = {"tweet_id": tid, "user": {"name": author, "nick": "Archive Test"}, "content": "Offline media integration fixture", "date": "2026-11-10 12:00:00", "count": 3}
    messages = [[2, meta]]
    for num, kind, ext, media_url in [(1, "photo", "png", "https://pbs.twimg.com/media/xarchive_fixture.png"), (2, "video", "mp4", "https://video.twimg.com/ext_tw_video/xarchive_fixture.mp4"), (3, "animated_gif", "mp4", "https://video.twimg.com/tweet_video/xarchive_fixture.mp4")]:
        messages.append([3, media_url, dict(meta, num=num, type=kind, extension=ext)])
    print(json.dumps(messages))
