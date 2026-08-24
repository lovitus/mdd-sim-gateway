#!/usr/bin/env python3
"""Unified Windows service, SSH CLI and GUI entrypoint."""

try:
    from cli import run_cli
except ModuleNotFoundError:
    from .cli import run_cli


if __name__ == "__main__":
    raise SystemExit(run_cli())

