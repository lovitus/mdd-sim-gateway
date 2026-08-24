#!/usr/bin/env python3
import argparse

try:
    from config_store import ConfigStore
    from gui import run_gui
    from control_contract import EXIT_CONFLICT
    from agent_host import HostConflictError
except ModuleNotFoundError:
    from .config_store import ConfigStore
    from .gui import run_gui
    from .control_contract import EXIT_CONFLICT
    from .agent_host import HostConflictError


def main(argv=None, *, store=None):
    parser = argparse.ArgumentParser(prog="mdd-agent-gui")
    parser.parse_args(argv)
    store = store or ConfigStore()
    try:
        return int(run_gui(store=store) or 0)
    except HostConflictError as exc:
        print(str(exc))
        return EXIT_CONFLICT


if __name__ == "__main__":
    raise SystemExit(main())
