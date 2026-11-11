#!/usr/bin/env python3
"""Offline gallery-dl contract fixture. Never used by the production server."""
import json
import os
import shutil
import sys
import time
from datetime import datetime
from pathlib import Path
from urllib.parse import urlparse

args = sys.argv[1:]
if "--version" in args:
    print("gallery-dl contract fixture (offline)")
    sys.exit(0)
assert "extractor.twitter.ratelimit=abort" in args
cookie = Path(args[args.index("--cookies") + 1])
config = cookie.parent.parent
assets = config / "fixtures"
if (config / "expected-proxy").exists():
    expected = (config / "expected-proxy").read_text()
    assert args[args.index("--proxy") + 1] == expected
    assert all(os.environ.get(key) == expected for key in ("HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY"))
phase = (config / "phase").read_text().strip() if (config / "phase").exists() else "1"
url = args[-1]
if "--directory" in args:
    target = Path(args[args.index("--directory") + 1])
    mode = (config / "download-mode").read_text().strip() if (config / "download-mode").exists() else ""
    if mode:
        (target / "asset.part").write_bytes(b"partial download")
        if mode == "cancel":
            (config / "download-started").touch()
            time.sleep(30)
        if mode == "invalid-image":
            (target / "asset.png").write_bytes(b"invalid image")
            sys.exit(0)
        print("[error] network failure", file=sys.stderr)
        sys.exit(1)
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
elif parts[-1] in ("following", "followers", "members"):
    if phase == "11":
        (config / "members-started").touch()
        for _ in range(1000):
            if (config / "members-release").exists():
                break
            time.sleep(0.01)
        print(json.dumps([[-1, {"error": "AuthenticationError", "message": "401"}]]))
        sys.exit(0)
    ids = ["101"] if phase == "3" else ["101", "102" if phase == "1" else "103"]
    print(json.dumps([[6, f"https://x.com/member{uid}", {"rest_id": uid, "legacy": {"screen_name": f"member{uid}", "name": f"Member {uid}"}}] for uid in ids]))
elif parts[-1] == "with_replies" and phase in ("9", "10"):
    (config / "timeline-args.json").write_text(json.dumps(args))
    tweets = [("1111", "2020-01-01 12:00:00"), ("2222", "2026-11-10 12:00:00")]
    if phase == "10":
        tweets = tweets[1:]
        print("[error] network failure after partial timeline", file=sys.stderr)
    if "--date-after" in args:
        after = datetime.fromisoformat(args[args.index("--date-after") + 1]).replace(tzinfo=None)
        tweets = [(tid, date) for tid, date in tweets if datetime.fromisoformat(date) >= after]
    print(json.dumps([[2, {"tweet_id": tid, "user": {"name": parts[0], "nick": "Archive Test"}, "content": "Timeline history fixture", "date": date}] for tid, date in tweets]))
else:
    tid = parts[-1] if "status" in parts else "1234567890123456780"
    author = "archive_test" if "status" in parts else parts[0]
    meta = {"tweet_id": tid, "user": {"name": author, "nick": "Archive Test"}, "content": "Offline media integration fixture", "date": "2026-11-10 12:00:00", "count": 3}
    messages = [[2, meta]]
    for num, kind, ext, media_url in [(1, "photo", "png", "https://pbs.twimg.com/media/xarchive_fixture.png"), (2, "video", "mp4", "https://video.twimg.com/ext_tw_video/xarchive_fixture.mp4"), (3, "animated_gif", "mp4", "https://video.twimg.com/tweet_video/xarchive_fixture.mp4")]:
        messages.append([3, media_url, dict(meta, num=num, type=kind, extension=ext)])
    print(json.dumps(messages))
