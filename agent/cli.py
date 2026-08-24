"""Non-interactive, SSH-safe management CLI for the unified MDD Agent."""

from __future__ import annotations

import argparse
import json
import os
import sys

try:
    from config_store import (CONFIG_KEYS, DEFAULT_CONFIG, ConfigError, ConfigStore,
                              validate_config)
    from control_contract import (
        EXIT_ACTION_FAILED, EXIT_CONFIG, EXIT_ELEVATION_REQUIRED, EXIT_PERMISSION,
        EXIT_CONFLICT, EXIT_UNAVAILABLE, EXIT_UNHEALTHY, ProtocolError,
    )
    from local_control import ControlClient
    from service_manager import ServiceManager, is_windows_admin
except ModuleNotFoundError:
    from .config_store import (CONFIG_KEYS, DEFAULT_CONFIG, ConfigError, ConfigStore,
                               validate_config)
    from .control_contract import (
        EXIT_ACTION_FAILED, EXIT_CONFIG, EXIT_ELEVATION_REQUIRED, EXIT_PERMISSION,
        EXIT_CONFLICT, EXIT_UNAVAILABLE, EXIT_UNHEALTHY, ProtocolError,
    )
    from .local_control import ControlClient
    from .service_manager import ServiceManager, is_windows_admin


def _coerce(key: str, value: str):
    if key == "token":
        return value
    default = DEFAULT_CONFIG[key]
    if isinstance(default, bool):
        normalized = value.strip().lower()
        if normalized not in {"true", "false", "1", "0", "yes", "no", "on", "off"}:
            raise ConfigError(f"{key} expects true or false")
        return normalized in {"true", "1", "yes", "on"}
    if isinstance(default, int):
        return int(value)
    if isinstance(default, float):
        return float(value)
    return value


def _emit(value, as_json: bool) -> None:
    if as_json:
        # JSON is an automation boundary used heavily over Windows OpenSSH.  Escaping
        # non-ASCII keeps it lossless on legacy GBK and other non-UTF-8 console code pages.
        print(json.dumps(value, ensure_ascii=True, separators=(",", ":")))
        return
    text = (json.dumps(value, ensure_ascii=False, indent=2)
            if isinstance(value, dict) else str(value))
    try:
        print(text)
    except UnicodeEncodeError:
        # Windows OpenSSH commonly exposes a legacy GBK stdout even when Agent status
        # contains Unicode device names.  Degrade only the unencodable characters; do
        # not reconfigure the process-wide stream or mask pipe/filesystem failures.
        encoding = getattr(sys.stdout, "encoding", None) or "utf-8"
        safe_text = text.encode(encoding, errors="backslashreplace").decode(encoding)
        print(safe_text)


def _request_macos_cli_audio_permission(host) -> None:
    """Request TCC from a local interactive CLI, then refresh only audio capability.

    macOS decides which executable owns the permission and whether it may present consent.
    A Terminal-launched packaged CLI can therefore ask directly; an SSH session without an
    active desktop may remain unpromptable, which is reported without stopping other devices.
    """
    try:
        try:
            from call_audio import _mac_microphone_permission
        except ModuleNotFoundError:
            from .call_audio import _mac_microphone_permission
        state = _mac_microphone_permission(request=True, timeout=30.0)
    except Exception as exc:
        print(f"MDD Agent microphone permission check failed: {exc}", file=sys.stderr)
        return
    if state == "authorized":
        try:
            result = host.runtime.reprobe_audio()
            ready = bool((result or {}).get("ready"))
            print("MDD Agent microphone permission is authorized; "
                  + ("call audio is ready." if ready else
                     "call audio was rechecked but is not ready."), file=sys.stderr)
        except Exception as exc:
            print(f"MDD Agent microphone is authorized, but audio recheck failed: {exc}",
                  file=sys.stderr)
        return
    guidance = {
        "permission_denied": "microphone permission was denied",
        "permission_restricted": "microphone permission is restricted",
        "permission_required": "macOS could not present the microphone consent dialog",
    }.get(state, f"microphone permission state is {state}")
    suffix = ("; an SSH session without a logged-in desktop cannot force macOS to display "
              "the consent UI" if os.environ.get("SSH_CONNECTION") else "")
    print(f"MDD Agent {guidance}{suffix}. Other Agent functions remain available.",
          file=sys.stderr)


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(prog="mdd-agent", description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    run = sub.add_parser("run", help="run the Agent hardware host in the foreground")
    run.add_argument("--json", action="store_true", help=argparse.SUPPRESS)
    run.add_argument(
        "--token",
        help="fallback Agent token (visible in process argv); saved configuration takes priority",
    )
    run.add_argument("--token-stdin", action="store_true",
                     help="read a fallback Agent token from stdin without persisting it")
    for name in ("status", "devices", "doctor", "self-test", "reconnect"):
        command = sub.add_parser(name)
        command.add_argument("--json", action="store_true")
    logs = sub.add_parser("logs")
    logs.add_argument("--lines", type=int, default=200)
    logs.add_argument("--json", action="store_true")

    maintenance = sub.add_parser("maintenance", help=argparse.SUPPRESS)
    maintenance_sub = maintenance.add_subparsers(
        dest="maintenance_command", required=True)
    prepare_install = maintenance_sub.add_parser("prepare-install", help=argparse.SUPPRESS)
    prepare_install.add_argument("--json", action="store_true")
    cancel_install = maintenance_sub.add_parser("cancel-install", help=argparse.SUPPRESS)
    cancel_install.add_argument("--nonce", required=True)
    cancel_install.add_argument("--json", action="store_true")

    config = sub.add_parser("config")
    config_sub = config.add_subparsers(dest="config_command", required=True)
    show = config_sub.add_parser("show")
    show.add_argument("--json", action="store_true")
    for name in ("set", "validate"):
        item = config_sub.add_parser(name)
        item.add_argument("key", choices=sorted(CONFIG_KEYS | {"token"}))
        item.add_argument("value", nargs="?")
        item.add_argument("--stdin", action="store_true",
                          help="read the value from stdin; required for secrets in automation")
        item.add_argument("--json", action="store_true")

    service = sub.add_parser("service")
    service_sub = service.add_subparsers(dest="service_command", required=True)
    for name in ("status", "start", "stop", "restart", "run"):
        item = service_sub.add_parser(name)
        item.add_argument("--json", action="store_true")
    install = service_sub.add_parser("install")
    install.add_argument("--server")
    install.add_argument("--token-stdin", action="store_true")
    install.add_argument("--reader-only", action="store_true",
                         help="allow installation readiness without a connected modem")
    install.add_argument(
        "--supervised-legacy-idle-migration", action="store_true",
        help="one-time migration of an old Agent after the operator has confirmed it is idle",
    )
    install.add_argument("--json", action="store_true")
    uninstall = service_sub.add_parser("uninstall")
    uninstall.add_argument("--purge", action="store_true")
    uninstall.add_argument("--json", action="store_true")

    gui = sub.add_parser("gui")
    gui.add_argument("--json", action="store_true", help=argparse.SUPPRESS)
    return parser


def run_cli(argv=None, *, store=None, client=None, services=None, host_factory=None) -> int:
    args = build_parser().parse_args(argv)
    store = store or ConfigStore()
    client = client or ControlClient(store.root)
    services = services or ServiceManager(data_dir=str(store.root))
    as_json = bool(getattr(args, "json", False))
    try:
        if args.command == "run":
            if os.name == "nt":
                raise RuntimeError("use the installed Windows service to host Agent hardware")
            if args.token and args.token_stdin:
                raise ConfigError("--token and --token-stdin are mutually exclusive")
            # Do not even consume stdin or inspect environment fallback when persistent config
            # is complete. This makes the documented precedence observable and non-blocking.
            if not store.has_persisted_token():
                fallback_token = str(args.token or "")
                if args.token_stdin:
                    token = sys.stdin.readline().rstrip("\r\n")
                    if not token:
                        raise ConfigError("--token-stdin requires a non-empty token")
                    fallback_token = token
                if not fallback_token:
                    fallback_token = str(os.environ.get("MDD_AGENT_TOKEN") or "")
                if fallback_token:
                    store.set_session_token(fallback_token)
            try:
                from agent_host import AgentHost, HostConflictError
            except ModuleNotFoundError:
                from .agent_host import AgentHost, HostConflictError
            host = host_factory(store) if host_factory else AgentHost(store)
            try:
                after_start = (_request_macos_cli_audio_permission
                               if sys.platform == "darwin" else None)
                try:
                    host.run_forever(after_start=after_start)
                except TypeError as exc:
                    # Third-party/test host factories written for the older no-argument
                    # contract remain usable. The production AgentHost always accepts the
                    # callback, so do not hide a TypeError raised from inside it.
                    if host_factory and "after_start" in str(exc):
                        host.run_forever()
                    else:
                        raise
                return 0
            except HostConflictError:
                raise

        if args.command == "service":
            action = args.service_command
            if action == "run":
                if os.name != "nt":
                    raise RuntimeError("SCM service host is only available on Windows")
                try:
                    from windows.service_host import run_service_dispatcher
                except ModuleNotFoundError:
                    from .windows.service_host import run_service_dispatcher
                run_service_dispatcher()
                return 0
            if action == "status":
                value = services.status()
                _emit(value, as_json)
                return 0 if value.get("installed") else 4
            if action == "install":
                if not is_windows_admin():
                    raise PermissionError("elevation_required")
                services.prepare()
                changes = {}
                if args.server:
                    changes["server"] = args.server
                if args.token_stdin:
                    changes["token"] = sys.stdin.readline().rstrip("\r\n")
                if changes:
                    store.save(changes)
                if not store.load(include_secrets=True).get("token"):
                    raise ConfigError("Agent token is not configured; use --token-stdin")
                _emit(services.install(
                    reader_only=args.reader_only,
                    supervised_legacy_idle_migration=
                    args.supervised_legacy_idle_migration), as_json)
                return 0
            if action == "uninstall":
                _emit(services.uninstall(args.purge), as_json)
                return 0
            _emit(services.action(action), as_json)
            return 0

        if args.command == "gui":
            try:
                from gui import run_gui
            except ModuleNotFoundError:
                from .gui import run_gui
            return int(run_gui(store=store, client=client, services=services) or 0)

        if args.command == "status":
            service_state = services.status()
            try:
                runtime = client.call("status")
            except Exception:
                runtime = None
            value = {"version": 1, "service": service_state, "runtime": runtime}
            _emit(value, as_json)
            return 0 if runtime or service_state.get("state") == "stopped" else EXIT_UNAVAILABLE

        if args.command == "config":
            if args.config_command == "show":
                _emit(client.call("config.show"), as_json)
                return 0
            raw = sys.stdin.readline().rstrip("\r\n") if args.stdin else args.value
            if raw is None:
                raise ConfigError("a value or --stdin is required")
            if args.key == "token" and not args.stdin:
                raise ConfigError("token must be supplied with --stdin so it is not exposed in argv")
            changes = {args.key: _coerce(args.key, raw)}
            method = "config.set" if args.config_command == "set" else "config.validate"
            try:
                result = client.call(method, {"changes": changes})
            except (ConnectionError, FileNotFoundError, TimeoutError, OSError):
                if os.name == "nt":
                    raise
                if args.config_command == "validate":
                    candidate = dict(store.load(include_secrets=False))
                    candidate.update(changes)
                    result = {"valid": True, "config": validate_config(candidate)}
                else:
                    result = {"config": store.save(changes), "restart_required": True,
                              "offline": True}
            _emit(result, as_json)
            return 0

        if args.command == "maintenance":
            method = f"maintenance.{args.maintenance_command}"
            params = ({"nonce": args.nonce}
                      if args.maintenance_command == "cancel-install" else {})
            result = client.call(method, params)
            _emit(result, as_json)
            ready = (result.get("cancelled") if args.maintenance_command == "cancel-install"
                     else result.get("ready"))
            return 0 if ready else EXIT_CONFLICT

        params = {"lines": args.lines} if args.command == "logs" else {}
        value = client.call(args.command, params)
        _emit(value, as_json)
        if args.command in {"doctor", "self-test"} and not value.get("healthy"):
            return EXIT_UNHEALTHY
        return 0
    except Exception as exc:
        try:
            from agent_host import HostConflictError
        except ModuleNotFoundError:
            try:
                from .agent_host import HostConflictError
            except ModuleNotFoundError:
                HostConflictError = ()
        if HostConflictError and isinstance(exc, HostConflictError):
            print(str(exc), file=sys.stderr)
            return EXIT_CONFLICT
        if isinstance(exc, PermissionError):
            if str(exc) == "elevation_required":
                _emit({"ok": False, "error": "elevation_required"}, as_json)
                print("Administrator approval is required. Re-run this command from an elevated "
                      "PowerShell; SSH sessions will not open a UAC dialog.", file=sys.stderr)
                return EXIT_ELEVATION_REQUIRED
            print(str(exc), file=sys.stderr)
            return EXIT_PERMISSION
        if isinstance(exc, ConfigError):
            print(str(exc), file=sys.stderr)
            return EXIT_CONFIG
        if isinstance(exc, ProtocolError):
            print(f"{exc.code}: {exc}", file=sys.stderr)
            return EXIT_PERMISSION if exc.code == "permission_denied" else EXIT_ACTION_FAILED
        if isinstance(exc, (ConnectionError, FileNotFoundError, TimeoutError, OSError)):
            print(f"Agent service is unavailable: {exc}", file=sys.stderr)
            return EXIT_UNAVAILABLE
        print(str(exc) or type(exc).__name__, file=sys.stderr)
        return EXIT_ACTION_FAILED
