#!/usr/bin/env python3
# SPDX-License-Identifier: GPL-2.0-only
"""Install the isolated MDD AMI primitive that answers an already-bridged IMS leg."""

from pathlib import Path
import os
import shutil


ROOT = Path(os.environ.get("ASTERISK_SOURCE_ROOT", "/home/asterisk-build/asterisk"))
SOURCE = Path(__file__).with_name("mdd_answer_bridged") / "app_mdd_answer_bridged.c"
TARGET = ROOT / "apps" / "app_mdd_answer_bridged.c"


def main() -> int:
    if not (ROOT / "apps" / "Makefile").is_file():
        raise RuntimeError(f"Asterisk source tree is unavailable: {ROOT}")
    shutil.copyfile(SOURCE, TARGET)
    print("installed MDD bridged-answer AMI module")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
