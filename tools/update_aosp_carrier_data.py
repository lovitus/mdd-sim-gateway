#!/usr/bin/env python3
"""Refresh the vendored AOSP Carrier ID textproto from a reviewed commit."""
from __future__ import annotations

import argparse
import base64
from pathlib import Path
import re
from urllib.request import urlopen


ROOT = Path(__file__).resolve().parents[1]
TARGET = ROOT / "control" / "app" / "resources" / "carrier_list.textpb"


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("commit", help="full TelephonyProvider Git commit to vendor")
    args = parser.parse_args()
    if not re.fullmatch(r"[0-9a-f]{40}", args.commit):
        parser.error("commit must be a full 40-character lowercase Git hash")
    url = ("https://android.googlesource.com/platform/packages/providers/TelephonyProvider/+/{}/"
           "assets/latest_carrier_id/carrier_list.textpb?format=TEXT").format(args.commit)
    with urlopen(url, timeout=30) as response:  # noqa: S310 - fixed HTTPS origin
        content = base64.b64decode(response.read()).decode("utf-8")
    if content.count("carrier_id {") < 1000 or not re.search(r"^version: \d+$", content, re.M):
        raise SystemExit("downloaded Carrier ID table failed structural validation")
    TARGET.parent.mkdir(parents=True, exist_ok=True)
    TARGET.write_text(content, encoding="utf-8")
    version = re.search(r"^version: (\d+)$", content, re.M).group(1)
    print(f"wrote {TARGET.relative_to(ROOT)}: {content.count('carrier_id {')} carriers, version {version}")


if __name__ == "__main__":
    main()
