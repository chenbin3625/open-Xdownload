#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""生成 xtid 的 golden 期望值。

用法（要求使用 Python 3，且已安装参考实现
https://github.com/iSarabjitDhiman/XClientTransaction）：

    python -m venv /tmp/xtid-ref/venv
    /tmp/xtid-ref/venv/bin/pip install beautifulsoup4
    /tmp/xtid-ref/venv/bin/pip install -e /tmp/xtid-ref2/xct
    /tmp/xtid-ref/venv/bin/python -I testdata/gen_golden.py

参考实现的算法与 twscrape 的 twscrape/xclid.py 完全一致（twscrape 的代码
也标注来源于 XClientTransaction）。固定时间戳与随机字节后，输出可以逐字节
比对。X 改版时：重新匿名抓取 x.com 首页、首页引用的签名 JS（当前为
x-web 的签名 chunk）以及它依赖的入口 chunk，替换本目录下的 fixture，再运行
本脚本即可。

注意：fixture 属于不可信数据，本脚本只解析它们，不执行其中任何 JS。
"""

import base64
import json
import os
import random
import re
import sys

from bs4 import BeautifulSoup
from x_client_transaction.transaction import ClientTransaction

# 与 twscrape 的 XClIdGen.calc 一致
TIMESTAMP = 1763000000          # Python 的 time.time() 值（秒）
RANDOM_BYTE = 137               # 对应 random.randint(0, 255)
DEFAULT_KEYWORD = "obfiowerehiring"
ADDITIONAL_RANDOM_NUMBER = 3
CASES = [
    ("GET", "/i/api/graphql/abc123/UserByScreenName"),
    ("GET", "/i/api/graphql/abc123/UserMedia"),
    ("POST", "/i/api/graphql/xyz789/CreateTweet"),
    ("GET", "/2/timeline/profile/123.json"),
]

HERE = os.path.dirname(os.path.abspath(__file__))
HOME = os.path.join(HERE, "home.html")
SIGNING_JS = os.path.join(HERE, "DIQrwHty.js")
OUT = os.path.join(HERE, "golden.json")


def main():
    home = open(HOME, encoding="utf-8").read()
    signing_js = open(SIGNING_JS, encoding="utf-8").read()

    # 校验 fixture 与参考实现假设一致：签名 JS 里能解析出 key byte 索引，
    # 首页里能解析出 twitter-site-verification。
    soup = BeautifulSoup(home, "html.parser")
    if not soup.find("meta", {"name": "twitter-site-verification", "content": True}):
        sys.exit("home.html 缺少 twitter-site-verification")

    # 参考实现用 random.randint(0, 255) 生成首字节；固定它以保证可复现。
    random.randint = lambda a, b: RANDOM_BYTE

    tx = ClientTransaction(
        soup,
        signing_js,
        random_keyword=DEFAULT_KEYWORD,
        random_number=ADDITIONAL_RANDOM_NUMBER,
    )

    # time_now 参数就是 twscrape 里的 ts（自 1682924400 起的秒数）。
    ts = TIMESTAMP - 1682924400
    cases = []
    for method, path in CASES:
        tid = tx.generate_transaction_id(method=method, path=path, time_now=ts)
        cases.append({"method": method, "path": path, "transactionId": tid})

    golden = {
        "note": "由 testdata/gen_golden.py 调用 XClientTransaction 生成；不要手改。",
        "reference": "iSarabjitDhiman/XClientTransaction@f33670478bf799c2c10b33894c97347ccb9904a1",
        "timestamp": TIMESTAMP,
        "randomByte": RANDOM_BYTE,
        "keyword": DEFAULT_KEYWORD,
        "additionalRandomNumber": ADDITIONAL_RANDOM_NUMBER,
        "verificationKey": tx.key,
        "animationKey": tx.animation_key,
        "cases": cases,
    }
    with open(OUT, "w", encoding="utf-8") as f:
        json.dump(golden, f, ensure_ascii=False, indent=2)
        f.write("\n")
    print("wrote", OUT, "cases:", len(cases))
    print("animationKey:", tx.animation_key)
    print("vk bytes:", list(base64.b64decode(tx.key)))
    print("indices:", [tx.row_index, *tx.key_bytes_indices])


if __name__ == "__main__":
    main()
