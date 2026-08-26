"""
engine.py - Per-SIM engine container lifecycle via the Docker SDK.

Each instance runs one `mdd-sim-gateway/engine` container that owns its ePDG tunnel + Asterisk.
The manager renders instance.json, starts/stops/recreates the container with the right
mounts/caps/ports, and reads the engine's runtime status files (bind-mounted run dir):
the swu_ike daemon publishes swu_status.json {state: CONNECTED} for tunnel state.

PC/SC: engine containers are pcscd CLIENTS — they mount the HOST pcscd socket (/run/pcscd).
The pcsc-lite client library in the engine image is pinned to the SAME version as the host
pcscd (Dockerfile PCSC_VERSION == install.sh PCSC_VERSION) so client/server protocol matches.
"""
from __future__ import annotations

from datetime import datetime
from contextlib import contextmanager
import fcntl
import glob
import hashlib
import ipaddress
import json
import logging
import math
import os
import re
import shutil
import stat
import threading
import time

import docker

from . import (config as cfg, egress, sysinfo, engine_replacement_contract,
               engine_start_quarantine_contract as start_quarantine_contract)

log = logging.getLogger("mdd.engine")

# Bounded so a line that rebuilds every two minutes cannot fill a Pi's SD card. Only the
# recent tail is diagnostically useful.
DIAGNOSTIC_RECORDS = 200
# Asterisk writes colour escapes even when captured to a file; strip them so the stored
# record stays greppable.
_ANSI = re.compile(r"\x1b\[[0-9;]*m")
# Lines worth keeping from a container that is about to be destroyed: the IMS registration
# exchange and the reasons Asterisk gives for not completing it. Matching on words such as
# "registration" or the endpoint name pulls in DEBUG chatter instead — nearly every debug
# line names res_pjsip_outbound_registration.c — which then crowds the real evidence out of
# the bounded tail. Match protocol lines and operator-visible failures only.
_SIP_EVIDENCE = re.compile(
    r"SIP/2\.0 \d{3}"                       # response status line
    r"|^(?:REGISTER|INVITE|MESSAGE|SUBSCRIBE) sip:"   # request line
    r"|No response received"                # the registration timed out with no answer
    r"|Temporal response '\d{3}'.*retrying in '\d+'"  # carrier scheduled an in-place retry
    r"|(?:Fatal response '\d{3}'|'\d{3}' fatal response received)"  # fatal retry evidence
    r"|transport '[^']+' failed"            # the tunnel died under an established transport
    r"|Status: \w+"                         # Asterisk's own registration verdict
    r"|Failed to authenticate"
    r"|[Uu]nable to register", re.I)
# A DEBUG line only earns its place when it carries an actual SIP status line.
_DEBUG_LINE = re.compile(r"\bDEBUG\b")
_DISPLAY_TIMESTAMP = re.compile(
    r"^\[\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}[+-]\d{4}\] ")
_DOCKER_TIMESTAMP = re.compile(
    r"^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})(?:\.\d+)?(Z|[+-]\d{2}:\d{2}) (.*)$")
_ASTERISK_TIMESTAMP = re.compile(r"^\[[A-Z][a-z]{2}\s+\d{1,2}\s+\d{2}:\d{2}:\d{2}\]\s*")
# Enough to cover a full REGISTER exchange plus the failures around it.
SIP_EVIDENCE_LINES = 40

DATA_DIR = cfg.DATA_DIR
IMAGE = os.environ.get("MDD_ENGINE_IMAGE", "mdd-sim-gateway/engine")
PCSCD_SOCK = os.environ.get("MDD_PCSCD_DIR", "/run/pcscd")
def _default_host_data_dir() -> str:
    if os.environ.get("MDD_HOST_DATA"):
        return os.environ["MDD_HOST_DATA"]
    if os.path.exists("/data") and os.path.abspath(DATA_DIR) == "/data":
        return "/opt/mdd-gateway/data"
    return DATA_DIR

HOST_DATA_DIR = _default_host_data_dir()
MANAGED_LABEL = "io.mdd-sim-gateway.managed"
ENGINE_ADMISSION_ABI_LABEL = "io.mdd-sim-gateway.admission-abi"
ENGINE_ADMISSION_ABI = "mdd-admission-v1"
ENGINE_MEDIA_WEBSOCKET_LABEL = "io.mdd-sim-gateway.media-websocket"
ENGINE_MEDIA_WEBSOCKET_ABI = "mdd-media-ws-v1"
ENGINE_BROWSER_OUTBOUND_LABEL = "io.mdd-sim-gateway.browser-outbound"
ENGINE_BROWSER_OUTBOUND_ABI = "mdd-browser-outbound-v1"
ENGINE_BROWSER_INBOUND_LABEL = "io.mdd-sim-gateway.browser-inbound"
ENGINE_BROWSER_INBOUND_ABI = "mdd-browser-inbound-v1"
ENGINE_MAINTENANCE_NAME = "engine-maintenance.json"
ENGINE_MAINTENANCE_LOCK = ".engine-maintenance.lock"
CONTROL_UPGRADE_NAME = "control-upgrade.json"
CONTROL_MAINTENANCE_FENCE_NAME = "maintenance-entry-fence.json"
ENGINE_REPLACEMENT_NAME = "engine-replacement.json"
ENGINE_DEFAULT_PROMOTION_NAME = "engine-default-promotion.json"
ENGINE_REPLACEMENT_UNSCOPED_REMOVALS_DIR = "engine-replacement-unscoped-removals"
ENGINE_REPLACEMENT_SCOPED_CARD_LOSS_DIR = "engine-replacement-scoped-card-loss"
ENGINE_REPLACEMENT_SCOPED_CARD_LOSS_UNCERTAIN_DIR = (
    "engine-replacement-scoped-card-loss-uncertain")
ENGINE_REPLACEMENT_EVENT_LOCK = ".engine-replacement-events.lock"
ENGINE_START_RECEIPTS_DIR = "engine-start-receipts"
ENGINE_REPLACEMENT_TX_LABEL = "io.mdd-sim-gateway.replacement-txid"
ENGINE_REPLACEMENT_INTENT_LABEL = "io.mdd-sim-gateway.replacement-intent"
ENGINE_REPLACEMENT_SOURCE_SPEC_LABEL = (
    "io.mdd-sim-gateway.replacement-source-spec-digest")
ENGINE_START_RECEIPT_MAX_ATTEMPTS = 3
_MAINTENANCE_PHASES = {
    "prepared", "source_quiescing", "source_removed", "target_starting",
    "target_started", "verified", "rollback_required", "rollback_starting",
    "rollback_started", "rollback_verified", "aborted", "manual_required",
}
_HEX64 = re.compile(r"^[0-9a-f]{64}$")
_TXID = re.compile(r"^[A-Za-z0-9][A-Za-z0-9_.:-]{7,127}$")
_RUN_ID = re.compile(r"^[A-Za-z0-9][A-Za-z0-9_.:-]{0,255}$")
_STARTED_AT = re.compile(
    r"^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?(?:Z|[+-]\d{2}:\d{2})$")
_ROLLBACK_IMAGE_REF = re.compile(
    r"^mdd-sim-gateway/engine-rollback:[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$")
_CREATE_SPEC_ENV = {"MDD_ID", "SWU_LIVENESS_PERIOD", "SWU_OUTER_MTU"}
_CREATE_SPEC_BIND_TARGETS = {
    "/config/instance.json", "/logs", "/run/mdd-sim-gateway", "/run/pcscd",
    "/etc/localtime", "/etc/asterisk/certificate.crt",
    "/etc/asterisk/certificate.key", "/opt/mdd-sim-gateway/templates",
}
_CREATE_SPEC_SYSCTLS = {
    "net.ipv6.conf.all.disable_ipv6": "0",
    "net.ipv6.conf.default.disable_ipv6": "0",
    "net.ipv6.conf.all.accept_ra": "0",
    "net.ipv6.conf.default.accept_ra": "0",
    "net.ipv6.conf.all.autoconf": "0",
    "net.ipv6.conf.default.autoconf": "0",
    "net.ipv6.conf.all.use_tempaddr": "0",
    "net.ipv6.conf.default.use_tempaddr": "0",
}


class MaintenanceStateError(RuntimeError):
    """A durable maintenance record exists but cannot be trusted."""


class EngineRunIdUnavailable(MaintenanceStateError):
    """A newly-created Engine has not published its run ID yet."""


class EngineStartReceiptError(MaintenanceStateError):
    """A target create receipt or its Docker attestation cannot be trusted."""


class EnginePortConflict(RuntimeError):
    """Docker could not publish one of the line's host ports."""


class EngineAlreadyExists(RuntimeError):
    """An absent-only maintenance start found any existing container at the line name."""


class EngineLifecycleFenced(RuntimeError):
    """Normal lifecycle work lost the race to a global replacement owner."""


class EngineStartQuarantined(EngineLifecycleFenced):
    """An absent Engine is intentionally fenced from every create path."""

    def __init__(self, message: str, *, blocked_iids=None, state_unknown: bool = False):
        super().__init__(message)
        self.blocked_iids = tuple(str(iid) for iid in (blocked_iids or ()))
        self.state_unknown = bool(state_unknown)


class EngineAdmissionABIError(RuntimeError):
    """The selected Engine image cannot enforce the compiled admission contract."""


def _owned(container) -> bool:
    labels = (container.attrs.get("Config") or {}).get("Labels") or {}
    image = str((container.attrs.get("Config") or {}).get("Image") or "")
    return labels.get(MANAGED_LABEL) == "true" or image.startswith("mdd-sim-gateway/")


def _require_engine_admission_abi(client, image: str) -> str:
    """Resolve one image once and return its verified immutable ID.

    Callers must create the container from the returned ID, never from the original tag: a tag
    can be retargeted after inspect and would otherwise reopen a pre-202/pre-send gate bypass.
    """
    try:
        inspected = client.images.get(image)
    except Exception as exc:
        raise EngineAdmissionABIError(
            f"cannot inspect Engine image {image!r} for admission ABI") from exc
    canonical = str(getattr(inspected, "id", "") or "")
    if not canonical.startswith("sha256:") or not _HEX64.fullmatch(canonical[7:]):
        raise EngineAdmissionABIError("Engine image inspect returned a non-canonical image ID")
    labels = ((getattr(inspected, "attrs", {}) or {}).get("Config") or {}).get("Labels") or {}
    if labels.get(ENGINE_ADMISSION_ABI_LABEL) != ENGINE_ADMISSION_ABI:
        raise EngineAdmissionABIError(
            f"Engine image lacks exact admission ABI {ENGINE_ADMISSION_ABI}")
    if labels.get(ENGINE_MEDIA_WEBSOCKET_LABEL) != ENGINE_MEDIA_WEBSOCKET_ABI:
        raise EngineAdmissionABIError(
            f"Engine image lacks exact media WebSocket ABI {ENGINE_MEDIA_WEBSOCKET_ABI}")
    if labels.get(ENGINE_BROWSER_OUTBOUND_LABEL) != ENGINE_BROWSER_OUTBOUND_ABI:
        raise EngineAdmissionABIError(
            f"Engine image lacks exact browser outbound ABI {ENGINE_BROWSER_OUTBOUND_ABI}")
    if labels.get(ENGINE_BROWSER_INBOUND_LABEL) != ENGINE_BROWSER_INBOUND_ABI:
        raise EngineAdmissionABIError(
            f"Engine image lacks exact browser inbound ABI {ENGINE_BROWSER_INBOUND_ABI}")
    requested = str(image)
    if requested.startswith("sha256:") and requested != canonical:
        raise EngineAdmissionABIError("immutable Engine request resolved to a different image ID")
    return canonical


def _require_engine_rollback_admission_abi(client, image: str) -> str:
    """Validate an exact retained predecessor without claiming it supports new media."""
    try:
        inspected = client.images.get(image)
    except Exception as exc:
        raise EngineAdmissionABIError(
            f"cannot inspect retained rollback image {image!r}") from exc
    canonical = str(getattr(inspected, "id", "") or "")
    labels = ((getattr(inspected, "attrs", {}) or {}).get("Config") or {}).get(
        "Labels") or {}
    if (not canonical.startswith("sha256:") or not _HEX64.fullmatch(canonical[7:]) or
            labels.get(ENGINE_ADMISSION_ABI_LABEL) != ENGINE_ADMISSION_ABI or
            str(image) != canonical):
        raise EngineAdmissionABIError(
            "retained rollback image lacks its exact admission ABI or immutable identity")
    return canonical


def _canonical_digest(value: object) -> str:
    payload = json.dumps(value, sort_keys=True, separators=(",", ":")).encode("utf-8")
    return hashlib.sha256(payload).hexdigest()


def _validate_engine_create_spec(value: object, iid: str) -> dict:
    """Validate the small Docker create-spec subset owned by this product.

    Docker inspect contains daemon defaults, runtime state and fields that must never be
    replayed.  Maintenance snapshots retain only the arguments that ``_start_container``
    supplies.  A source with an unexpected mount, environment override or port is rejected
    before the old container is touched.
    """
    if not isinstance(value, dict) or set(value) != {
            "version", "instance", "environment", "binds", "ports", "devices",
            "privileged", "restart_policy", "network_mode", "extra_hosts", "sysctls",
            "labels"}:
        raise MaintenanceStateError("invalid Engine create spec schema")
    if type(value.get("version")) is not int or value["version"] != 1 \
            or str(value.get("instance")) != str(iid):
        raise MaintenanceStateError("Engine create spec identity mismatch")

    environment = value.get("environment")
    if not isinstance(environment, dict) or not environment \
            or set(environment) - _CREATE_SPEC_ENV:
        raise MaintenanceStateError("invalid Engine create spec environment")
    if environment.get("MDD_ID") != str(iid):
        raise MaintenanceStateError("Engine create spec MDD_ID mismatch")
    liveness = environment.get("SWU_LIVENESS_PERIOD")
    if not isinstance(liveness, str) or not re.fullmatch(r"\d+", liveness):
        raise MaintenanceStateError("invalid Engine create spec liveness")
    mtu = environment.get("SWU_OUTER_MTU")
    if mtu is not None and (not isinstance(mtu, str) or not mtu.isdigit()
                            or not 1280 <= int(mtu) <= 9000):
        raise MaintenanceStateError("invalid Engine create spec outer MTU")

    binds = value.get("binds")
    if not isinstance(binds, list) or not binds:
        raise MaintenanceStateError("invalid Engine create spec binds")
    checked_binds, targets = [], set()
    for item in binds:
        if not isinstance(item, dict) or set(item) != {"host", "container", "mode"}:
            raise MaintenanceStateError("invalid Engine create spec bind")
        host, target, mode = item.get("host"), item.get("container"), item.get("mode")
        if (not isinstance(host, str) or not os.path.isabs(host)
                or not isinstance(target, str) or target not in _CREATE_SPEC_BIND_TARGETS
                or mode not in {"ro", "rw"} or target in targets):
            raise MaintenanceStateError("unsafe Engine create spec bind")
        targets.add(target)
        checked_binds.append({"host": host, "container": target, "mode": mode})
    required_targets = {"/config/instance.json", "/logs", "/run/mdd-sim-gateway",
                        "/run/pcscd"}
    if not required_targets.issubset(targets):
        raise MaintenanceStateError("Engine create spec is missing a required bind")
    expected_hosts = {
        "/config/instance.json": os.path.join(
            HOST_DATA_DIR, "instances", str(iid), "instance.json"),
        "/logs": os.path.join(HOST_DATA_DIR, "instances", str(iid), "logs"),
        "/run/mdd-sim-gateway": os.path.join(
            HOST_DATA_DIR, "instances", str(iid), "run"),
        "/run/pcscd": PCSCD_SOCK,
    }
    for item in checked_binds:
        expected = expected_hosts.get(item["container"])
        if expected is not None and os.path.normpath(item["host"]) != os.path.normpath(expected):
            raise MaintenanceStateError("Engine create spec bind does not match line ownership")

    ports = value.get("ports")
    if not isinstance(ports, list) or not ports:
        raise MaintenanceStateError("invalid Engine create spec ports")
    checked_ports, container_ports = [], set()
    udp_count = 0
    for item in ports:
        if not isinstance(item, dict) or set(item) != {
                "container_port", "host_ip", "host_port"}:
            raise MaintenanceStateError("invalid Engine create spec port")
        container_port = item.get("container_port")
        host_ip = item.get("host_ip")
        host_port = item.get("host_port")
        match = re.fullmatch(r"(\d{1,5})/(tcp|udp)", str(container_port or ""))
        if (not match or container_port in container_ports
                or host_ip not in {"", "127.0.0.1"}
                or type(host_port) is not int or not 1 <= host_port <= 65535):
            raise MaintenanceStateError("unsafe Engine create spec port")
        port, protocol = int(match.group(1)), match.group(2)
        if not 1 <= port <= 65535:
            raise MaintenanceStateError("invalid Engine create spec container port")
        if protocol == "tcp":
            if container_port not in {"8089/tcp", "5038/tcp"} or host_ip != "127.0.0.1":
                raise MaintenanceStateError("unexpected Engine TCP publication")
        else:
            if host_ip != "" or port != host_port:
                raise MaintenanceStateError("unexpected Engine RTP publication")
            udp_count += 1
        container_ports.add(container_port)
        checked_ports.append({"container_port": container_port, "host_ip": host_ip,
                              "host_port": host_port})
    if "8089/tcp" not in container_ports or not 1 <= udp_count <= 128:
        raise MaintenanceStateError("Engine create spec media publications are incomplete")

    devices = value.get("devices")
    if devices != [{"host": "/dev/net/tun", "container": "/dev/net/tun",
                    "permissions": "rwm"}]:
        raise MaintenanceStateError("invalid Engine create spec devices")
    if value.get("privileged") is not True:
        raise MaintenanceStateError("Engine create spec lost privileged TUN ownership")
    if value.get("restart_policy") != {"Name": "unless-stopped", "MaximumRetryCount": 0}:
        raise MaintenanceStateError("invalid Engine create spec restart policy")
    if value.get("network_mode") != "bridge":
        raise MaintenanceStateError("invalid Engine create spec network mode")
    if value.get("extra_hosts") != ["host.docker.internal:host-gateway"]:
        raise MaintenanceStateError("invalid Engine create spec extra hosts")
    if value.get("sysctls") != _CREATE_SPEC_SYSCTLS:
        raise MaintenanceStateError("invalid Engine create spec sysctls")
    if value.get("labels") != {
            MANAGED_LABEL: "true", "io.mdd-sim-gateway.component": "engine"}:
        raise MaintenanceStateError("invalid Engine create spec labels")
    return json.loads(json.dumps({**value, "binds": checked_binds,
                                  "ports": checked_ports}))


def capture_engine_create_spec(iid: str, expected_container_id: str) -> dict:
    """Capture and verify the exact allowlisted create arguments of one source Engine."""
    iid = str(iid)
    client = _client()
    container = client.containers.get(container_name(iid))
    if not _owned(container) or str(container.id) != str(expected_container_id):
        raise MaintenanceStateError("Engine changed before create-spec capture")
    container.reload()
    attrs = container.attrs or {}
    state = attrs.get("State") or {}
    if state.get("Status") != "running":
        raise MaintenanceStateError("source Engine is not running")
    image_id = str(attrs.get("Image") or "")
    image = client.images.get(image_id)
    image_config = (getattr(image, "attrs", {}) or {}).get("Config") or {}
    config = attrs.get("Config") or {}
    if config.get("Entrypoint") != image_config.get("Entrypoint") \
            or config.get("Cmd") != image_config.get("Cmd"):
        raise MaintenanceStateError("source Engine overrides image entrypoint or command")

    def env_map(raw: object) -> dict[str, str]:
        result = {}
        if not isinstance(raw, list):
            raise MaintenanceStateError("invalid Engine environment inspection")
        for item in raw:
            if not isinstance(item, str) or "=" not in item:
                raise MaintenanceStateError("invalid Engine environment entry")
            key, item_value = item.split("=", 1)
            if key in result:
                raise MaintenanceStateError("duplicate Engine environment entry")
            result[key] = item_value
        return result

    actual_env, image_env = env_map(config.get("Env") or []), env_map(
        image_config.get("Env") or [])
    if {key: val for key, val in actual_env.items() if key not in _CREATE_SPEC_ENV} != image_env:
        raise MaintenanceStateError("source Engine has unexpected environment overrides")
    environment = {key: actual_env[key] for key in sorted(_CREATE_SPEC_ENV)
                   if key in actual_env}

    host_config = attrs.get("HostConfig") or {}
    binds = []
    for raw in host_config.get("Binds") or []:
        if not isinstance(raw, str):
            raise MaintenanceStateError("invalid Engine bind inspection")
        parts = raw.rsplit(":", 2)
        if len(parts) != 3:
            raise MaintenanceStateError("invalid Engine bind inspection")
        binds.append({"host": parts[0], "container": parts[1], "mode": parts[2]})

    ports = []
    for container_port, bindings in sorted((host_config.get("PortBindings") or {}).items()):
        if not isinstance(bindings, list) or len(bindings) != 1 \
                or not isinstance(bindings[0], dict):
            raise MaintenanceStateError("ambiguous Engine port binding")
        host_port = str(bindings[0].get("HostPort") or "")
        if not host_port.isdigit():
            raise MaintenanceStateError("invalid Engine host port")
        ports.append({"container_port": str(container_port),
                      "host_ip": str(bindings[0].get("HostIp") or ""),
                      "host_port": int(host_port)})

    devices = []
    for item in host_config.get("Devices") or []:
        if not isinstance(item, dict):
            raise MaintenanceStateError("invalid Engine device inspection")
        devices.append({"host": str(item.get("PathOnHost") or ""),
                        "container": str(item.get("PathInContainer") or ""),
                        "permissions": str(item.get("CgroupPermissions") or "")})
    labels = config.get("Labels") or {}
    spec = {
        "version": 1, "instance": iid, "environment": environment,
        "binds": sorted(binds, key=lambda item: item["container"]),
        "ports": sorted(ports, key=lambda item: item["container_port"]),
        "devices": devices, "privileged": host_config.get("Privileged"),
        "restart_policy": dict(host_config.get("RestartPolicy") or {}),
        "network_mode": str(host_config.get("NetworkMode") or ""),
        "extra_hosts": sorted(host_config.get("ExtraHosts") or []),
        "sysctls": dict(host_config.get("Sysctls") or {}),
        "labels": {MANAGED_LABEL: labels.get(MANAGED_LABEL),
                   "io.mdd-sim-gateway.component": labels.get(
                       "io.mdd-sim-gateway.component")},
    }
    checked = _validate_engine_create_spec(spec, iid)
    container.reload()
    if str(container.id) != str(expected_container_id) \
            or str((container.attrs or {}).get("Image") or "") != image_id:
        raise MaintenanceStateError("Engine changed during create-spec capture")
    return checked


def _host_data_path(path: str) -> str:
    """Translate a control-container path under MDD_DATA to the same file on the host.

    Explicit TLS files are configured from the WebUI as /data/... in Docker mode. Sibling
    engine containers cannot bind-mount that container-only path; Docker needs the host's
    /opt/... data path instead. Paths outside DATA_DIR are already host/native paths.
    """
    absolute = os.path.abspath(path)
    data_root = os.path.abspath(DATA_DIR)
    try:
        if os.path.commonpath([absolute, data_root]) == data_root:
            return os.path.join(os.path.abspath(HOST_DATA_DIR), os.path.relpath(absolute, data_root))
    except ValueError:
        pass
    return absolute


def _runtime_data_path(path: str) -> str:
    """Translate a TLS path persisted while the manager used Docker's /data mount.

    The native control plane and sibling engine both use the host data directory.  Without this
    migration, the WebUI can have the public certificate while Asterisk silently falls back to
    an old self-signed certificate, causing browsers to reject the softphone WSS connection.
    """
    value = str(path or "")
    if value.startswith("/data/") and os.path.abspath(DATA_DIR) != "/data":
        translated = os.path.join(DATA_DIR, os.path.relpath(value, "/data"))
        if os.path.exists(translated):
            return translated
    return value


_docker_client = None
_docker_client_lock = threading.Lock()
_registration_evidence_lock = threading.Lock()


def _client():
    """Reuse the Docker HTTP connection pool instead of rebuilding it on every status sample."""
    global _docker_client
    if _docker_client is None:
        with _docker_client_lock:
            if _docker_client is None:
                _docker_client = docker.from_env(timeout=30)
    return _docker_client


def close_client():
    """Release the shared Docker client during control-plane shutdown."""
    global _docker_client
    with _docker_client_lock:
        client, _docker_client = _docker_client, None
    if client is not None:
        try:
            client.close()
        except Exception:
            pass


def container_name(iid: str) -> str:
    return f"mdd-sim-gateway-engine-{iid}"


def _instance_paths(iid: str):
    base = os.path.join(DATA_DIR, "instances", str(iid))
    host_base = os.path.join(HOST_DATA_DIR, "instances", str(iid))
    os.makedirs(os.path.join(base, "run"), exist_ok=True)
    os.makedirs(os.path.join(base, "logs"), exist_ok=True)
    return base, host_base


def _maintenance_path(iid: str) -> str:
    return os.path.join(DATA_DIR, "instances", str(iid), "run",
                        ENGINE_MAINTENANCE_NAME)


def _engine_start_receipt_path(iid: str) -> str:
    return os.path.join(DATA_DIR, "orchestrator", ENGINE_START_RECEIPTS_DIR,
                        f"{str(iid)}.json")


def global_maintenance_pending() -> bool:
    """Corrupt/in-flight manifests fence; strict committed topology restores normal work."""
    # The host supervisor publishes this before a planned full-proxy revoke. It remains until
    # the external transaction owner completes recovery and explicitly releases its fence.
    # Existence is deliberately fail-closed even when the JSON itself is damaged.
    fence = os.path.join(DATA_DIR, "orchestrator", CONTROL_MAINTENANCE_FENCE_NAME)
    if os.path.lexists(fence):
        return True
    replacement = os.path.join(DATA_DIR, "orchestrator", ENGINE_REPLACEMENT_NAME)
    if os.path.lexists(replacement):
        try:
            with open(replacement, encoding="utf-8") as handle:
                value = engine_replacement_contract.validate_manifest(json.load(handle))
            if value["phase"] not in {"committed", "aborted"}:
                return True
        except Exception:
            return True
    path = os.path.join(DATA_DIR, "orchestrator", CONTROL_UPGRADE_NAME)
    if not os.path.lexists(path):
        return False
    try:
        with open(path, encoding="utf-8") as handle:
            value = json.load(handle)
        if not isinstance(value, dict) or set(value) != {
                "version", "txid", "phase", "owner", "source_control", "rollback_control", "proxy",
                "rollback_upstream", "lines"}:
            return True
        if (type(value.get("version")) is not int or value["version"] != 1
                or not isinstance(value.get("txid"), str)
                or not _TXID.fullmatch(value["txid"])):
            return True
        phase = value.get("phase")
        if phase not in {"rollback_committed", "committed"}:
            return True
        proxy = value.get("proxy")
        if (not isinstance(proxy, dict) or set(proxy) != {"container_id", "image_id"}
                or not isinstance(proxy.get("container_id"), str)
                or not _HEX64.fullmatch(proxy["container_id"])
                or not isinstance(proxy.get("image_id"), str)
                or not proxy["image_id"].startswith("sha256:")
                or not _HEX64.fullmatch(proxy["image_id"][7:])):
            return True
        lines = value.get("lines")
        if not isinstance(lines, list):
            return True
        allowed = ({"rollback_verified", "aborted"}
                   if phase == "rollback_committed" else {"verified"})
        if any(not isinstance(line, dict) or line.get("phase") not in allowed
               for line in lines):
            return True
        return False
    except Exception:
        return True


def engine_default_promotion_pending() -> bool:
    """Block normal creates while promotion is active or its committed tag has drifted."""
    path = os.path.join(DATA_DIR, "orchestrator", ENGINE_DEFAULT_PROMOTION_NAME)
    if not os.path.lexists(path):
        return False
    try:
        with open(path, encoding="utf-8") as handle:
            value = engine_replacement_contract.validate_default_promotion(json.load(handle))
        if value["phase"] == "aborted":
            return False
        if value["phase"] != "committed":
            return True
        if value["default_ref"] != IMAGE:
            return True
        return _require_engine_admission_abi(
            _client(), IMAGE) != value["candidate_image"]
    except Exception:
        return True


def _active_replacement_manifest() -> dict | None:
    """Return one strict active replacement manifest; malformed state stays unknowable."""
    path = os.path.join(DATA_DIR, "orchestrator", ENGINE_REPLACEMENT_NAME)
    try:
        with open(path, encoding="utf-8") as handle:
            value = engine_replacement_contract.validate_manifest(json.load(handle))
    except FileNotFoundError:
        return None
    except Exception as exc:
        raise MaintenanceStateError("active Engine replacement manifest is unreadable") from exc
    return value if value["phase"] not in {"committed", "aborted"} else None


def _unscoped_removal_receipt_path(txid: str, iid: str) -> str:
    return os.path.join(DATA_DIR, "orchestrator",
                        ENGINE_REPLACEMENT_UNSCOPED_REMOVALS_DIR,
                        f"{txid}.{iid}.json")


def _write_unscoped_removal_receipt(manifest: dict, receipt: dict) -> dict:
    checked = engine_replacement_contract.validate_unscoped_removal_receipt(
        receipt, manifest)
    _atomic_json(_unscoped_removal_receipt_path(
        checked["txid"], checked["iid"]), checked)
    return checked


def _scoped_card_loss_intent_path(txid: str, iid: str) -> str:
    return os.path.join(DATA_DIR, "orchestrator",
                        ENGINE_REPLACEMENT_SCOPED_CARD_LOSS_DIR,
                        f"{txid}.{iid}.json")


def _write_scoped_card_loss_intent(manifest: dict, intent: dict) -> dict:
    checked = engine_replacement_contract.validate_scoped_card_loss_intent(
        intent, manifest)
    path = _scoped_card_loss_intent_path(checked["txid"], checked["iid"])
    try:
        with open(path, encoding="utf-8") as handle:
            existing = engine_replacement_contract.validate_scoped_card_loss_intent(
                json.load(handle), manifest)
        return existing
    except FileNotFoundError:
        pass
    _atomic_json(path, checked)
    return checked


def _write_scoped_card_loss_uncertainty(iid: str, event: dict,
                                        code: str) -> dict:
    """Publish an existence-fenced card event when strict ownership is unknowable."""
    iid = str(iid)
    if (not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_.-]{0,63}", iid)
            or code not in {
                "manifest_unreadable", "card_identity_incomplete",
                "intent_write_failed"}):
        raise ValueError("invalid scoped card-loss uncertainty identity")
    value = {
        "version": 1, "iid": iid, "code": code,
        "reason": str(event.get("reason") or "")[:64],
        "reader_name": str(event.get("reader_name") or "")[:255],
        "reader_index": (event.get("reader_index")
                         if type(event.get("reader_index")) is int else -1),
        "iccid": str(event.get("iccid") or "")[:32],
        "observed_at": time.time(),
    }
    path = os.path.join(
        DATA_DIR, "orchestrator",
        ENGINE_REPLACEMENT_SCOPED_CARD_LOSS_UNCERTAIN_DIR, f"{iid}.json")
    _atomic_json(path, value)
    return value


@contextmanager
def replacement_lifecycle_shared_locked(blocking: bool = False):
    """Serialize normal starts before the host snapshots replacement topology."""
    try:
        with start_quarantine_contract.global_lifecycle_locked(
                DATA_DIR, exclusive=False, blocking=blocking) as handle:
            yield handle
    except start_quarantine_contract.QuarantineContractError as exc:
        raise EngineLifecycleFenced(
            "global Engine replacement owns the lifecycle lock") from exc


_START_PERMIT_SECRET = object()


class _EngineStartPermit:
    """Unforgeable process-local proof that the canonical lifecycle locks are held."""

    __slots__ = ("_secret", "iid", "lifecycle_handle", "line_handle", "mode", "active")

    def __init__(self, secret, iid: str, lifecycle_handle, line_handle, mode: str):
        if secret is not _START_PERMIT_SECRET:
            raise TypeError("Engine start permits are private")
        self._secret = secret
        self.iid = start_quarantine_contract.canonical_iid(iid)
        self.lifecycle_handle = lifecycle_handle
        self.line_handle = line_handle
        self.mode = mode
        self.active = True


class _CardProbePermit:
    """One global probe transaction with a single authoritative post-identity bind."""

    __slots__ = ("_secret", "lifecycle_handle", "active", "bound_iids", "exclusive",
                 "_line_context", "line_handles", "bind_attempted")

    def __init__(self, secret, lifecycle_handle):
        if secret is not _START_PERMIT_SECRET:
            raise TypeError("card probe permits are private")
        self._secret = secret
        self.lifecycle_handle = lifecycle_handle
        self.active = True
        self.bound_iids = ()
        self.exclusive = False
        self._line_context = None
        self.line_handles = ()
        self.bind_attempted = False

    def bind_actual(self, iids, *, exclusive: bool = False) -> tuple[str, ...]:
        if (self._secret is not _START_PERMIT_SECRET or self.active is not True
                or self.lifecycle_handle is None or self.lifecycle_handle.closed
                or self.bind_attempted):
            raise EngineLifecycleFenced("invalid, expired or already-bound card probe permit")
        self.bind_attempted = True
        canonical = tuple(sorted({
            start_quarantine_contract.canonical_iid(iid) for iid in iids}))
        if not canonical:
            raise EngineLifecycleFenced("actual card identity has no Engine instance binding")
        context = start_quarantine_contract.locked_lines(
            DATA_DIR, canonical, exclusive=exclusive, blocking=False)
        entered = False
        try:
            handles = context.__enter__()
            entered = True
            blockers = _active_probe_quarantines_locked()
            if blockers:
                raise EngineStartQuarantined(
                    "SIM identity probing is blocked by an active Engine quarantine",
                    blocked_iids=blockers)
        except start_quarantine_contract.QuarantineContractError as exc:
            if entered:
                context.__exit__(type(exc), exc, exc.__traceback__)
            raise EngineLifecycleFenced(str(exc)) from exc
        except Exception as exc:
            if entered:
                context.__exit__(type(exc), exc, exc.__traceback__)
            raise
        self._line_context = context
        self.line_handles = handles
        self.bound_iids = canonical
        self.exclusive = bool(exclusive)
        return canonical

    def close(self) -> None:
        self.active = False
        if self._line_context is not None:
            context, self._line_context = self._line_context, None
            context.__exit__(None, None, None)
        self.line_handles = ()


def _require_start_permit(permit: object, iid: str, *, maintenance: bool = False) \
        -> _EngineStartPermit:
    expected_mode = "maintenance" if maintenance else "normal"
    if (type(permit) is not _EngineStartPermit
            or getattr(permit, "_secret", None) is not _START_PERMIT_SECRET
            or permit.iid != start_quarantine_contract.canonical_iid(iid)
            or permit.mode != expected_mode or permit.active is not True
            or permit.line_handle is None or permit.line_handle.closed
            or (not maintenance and (permit.lifecycle_handle is None
                                     or permit.lifecycle_handle.closed))):
        raise EngineLifecycleFenced("invalid or expired Engine start permit")
    if start_quarantine_contract.is_pending(DATA_DIR, permit.iid):
        raise EngineStartQuarantined(
            "Engine start is blocked by a durable absent-line quarantine")
    return permit


def _require_rollback_start_permit(permit: object, iid: str) -> _EngineStartPermit:
    if (type(permit) is not _EngineStartPermit
            or getattr(permit, "_secret", None) is not _START_PERMIT_SECRET
            or permit.iid != start_quarantine_contract.canonical_iid(iid)
            or permit.mode != "rollback" or permit.active is not True
            or permit.line_handle is None or permit.line_handle.closed):
        raise EngineLifecycleFenced("invalid or expired Engine rollback permit")
    if start_quarantine_contract.is_pending(DATA_DIR, permit.iid):
        raise EngineStartQuarantined(
            "Engine rollback is blocked by a durable absent-line quarantine")
    return permit


def _require_delete_permit(permit: object, iid: str) -> _EngineStartPermit:
    if (type(permit) is not _EngineStartPermit
            or getattr(permit, "_secret", None) is not _START_PERMIT_SECRET
            or permit.iid != start_quarantine_contract.canonical_iid(iid)
            or permit.mode != "delete" or permit.active is not True
            or permit.lifecycle_handle is None or permit.lifecycle_handle.closed
            or permit.line_handle is None or permit.line_handle.closed):
        raise EngineLifecycleFenced("invalid or expired Engine delete permit")
    if start_quarantine_contract.is_pending(DATA_DIR, permit.iid):
        raise EngineStartQuarantined(
            "hard delete is blocked by a durable absent-line quarantine")
    return permit


@contextmanager
def normal_start_permits(iids, *, blocking: bool = False):
    """Hold one global SH plus stable line SH locks across preflight and Docker create."""
    canonical = sorted({start_quarantine_contract.canonical_iid(iid) for iid in iids})
    try:
        with replacement_lifecycle_shared_locked(blocking=blocking) as lifecycle_handle:
            with start_quarantine_contract.locked_lines(
                    DATA_DIR, canonical, exclusive=False, blocking=blocking) as handles:
                pending = [iid for iid in canonical
                           if start_quarantine_contract.is_pending(DATA_DIR, iid)]
                if pending:
                    # Parse valid records for diagnostics, while any malformed object remains
                    # an equally strong existence fence.
                    for iid in pending:
                        try:
                            start_quarantine_contract.read_active(DATA_DIR, iid)
                        except start_quarantine_contract.QuarantineContractError:
                            pass
                    raise EngineStartQuarantined(
                        "Engine start is blocked by a durable absent-line quarantine")
                permits = {
                    iid: _EngineStartPermit(
                        _START_PERMIT_SECRET, iid, lifecycle_handle, handle, "normal")
                    for iid, handle in zip(canonical, handles)
                }
                try:
                    yield permits
                finally:
                    for permit in permits.values():
                        permit.active = False
    except start_quarantine_contract.QuarantineContractError as exc:
        raise EngineLifecycleFenced(str(exc)) from exc


@contextmanager
def normal_start_permit(iid: str, *, blocking: bool = False):
    canonical = start_quarantine_contract.canonical_iid(iid)
    with normal_start_permits([canonical], blocking=blocking) as permits:
        yield permits[canonical]


def _active_probe_quarantines_locked() -> list[str]:
    """Strict presence scan; caller must already own the global lifecycle lock."""
    try:
        active = start_quarantine_contract.active_iids(DATA_DIR)
        for iid in active:
            start_quarantine_contract.read_active(DATA_DIR, iid)
        return active
    except start_quarantine_contract.QuarantineContractError as exc:
        raise EngineStartQuarantined(
            "SIM identity probing is blocked by untrusted quarantine state",
            state_unknown=True) from exc


@contextmanager
def card_probe_permits(_history_iids=(), *, blocking: bool = False):
    """Fence APDU and publication globally, then bind the one actual identity exactly once.

    History is deliberately ignored for locking: it is only a display hint and cannot prove
    which SIM is currently in a reader. The actual iid(s) are bound after the APDU, or before
    publishing a running-engine inference.
    """
    try:
        with replacement_lifecycle_shared_locked(blocking=blocking) as lifecycle_handle:
            blockers = _active_probe_quarantines_locked()
            if blockers:
                raise EngineStartQuarantined(
                    "SIM identity probing is blocked by an active Engine quarantine",
                    blocked_iids=blockers)
            permit = _CardProbePermit(_START_PERMIT_SECRET, lifecycle_handle)
            try:
                yield permit
            finally:
                permit.close()
    except start_quarantine_contract.QuarantineContractError as exc:
        raise EngineLifecycleFenced(str(exc)) from exc


def active_engine_start_quarantines() -> list[str]:
    """Return a trustworthy marker-presence snapshot or fail closed while Host mutates it."""
    try:
        with replacement_lifecycle_shared_locked(blocking=False):
            return _active_probe_quarantines_locked()
    except start_quarantine_contract.QuarantineContractError as exc:
        raise EngineLifecycleFenced(str(exc)) from exc


@contextmanager
def normal_delete_permit(iid: str, *, blocking: bool = False):
    """Serialize hard delete with acquire and keep the stable lock across rmtree."""
    canonical = start_quarantine_contract.canonical_iid(iid)
    try:
        with replacement_lifecycle_shared_locked(blocking=blocking) as lifecycle_handle:
            with start_quarantine_contract.locked_lines(
                    DATA_DIR, [canonical], exclusive=True, blocking=blocking) as handles:
                if start_quarantine_contract.is_pending(DATA_DIR, canonical):
                    raise EngineStartQuarantined(
                        "hard delete is blocked by a durable absent-line quarantine")
                permit = _EngineStartPermit(
                    _START_PERMIT_SECRET, canonical, lifecycle_handle, handles[0], "delete")
                try:
                    yield permit
                finally:
                    permit.active = False
    except start_quarantine_contract.QuarantineContractError as exc:
        raise EngineLifecycleFenced(str(exc)) from exc


@contextmanager
def maintenance_start_permit(iid: str, *, blocking: bool = False):
    """Take only the stable line SH; the Host replacement already owns global EX."""
    canonical = start_quarantine_contract.canonical_iid(iid)
    try:
        with start_quarantine_contract.locked_lines(
                DATA_DIR, [canonical], exclusive=False, blocking=blocking) as handles:
            if start_quarantine_contract.is_pending(DATA_DIR, canonical):
                try:
                    start_quarantine_contract.read_active(DATA_DIR, canonical)
                except start_quarantine_contract.QuarantineContractError:
                    pass
                raise EngineStartQuarantined(
                    "maintenance Engine start is blocked by an absent-line quarantine")
            permit = _EngineStartPermit(
                _START_PERMIT_SECRET, canonical, None, handles[0], "maintenance")
            try:
                yield permit
            finally:
                permit.active = False
    except start_quarantine_contract.QuarantineContractError as exc:
        raise EngineLifecycleFenced(str(exc)) from exc


@contextmanager
def _rollback_start_permit(iid: str, txid: str, image_digest: str,
                           *, blocking: bool = False):
    """Mint rollback authority only after revalidating the exact durable transaction."""
    canonical = start_quarantine_contract.canonical_iid(iid)
    try:
        with start_quarantine_contract.locked_lines(
                DATA_DIR, [canonical], exclusive=False, blocking=blocking) as handles:
            marker = read_engine_maintenance(canonical)
            if (marker is None or marker.get("txid") != str(txid)
                    or marker.get("phase") != "rollback_starting"
                    or (marker.get("source") or {}).get("image_id") != str(image_digest)):
                raise MaintenanceStateError(
                    "rollback permit does not match the exact durable transaction")
            if start_quarantine_contract.is_pending(DATA_DIR, canonical):
                raise EngineStartQuarantined(
                    "maintenance Engine rollback is blocked by absent-line quarantine")
            permit = _EngineStartPermit(
                _START_PERMIT_SECRET, canonical, None, handles[0], "rollback")
            try:
                yield permit
            finally:
                permit.active = False
    except start_quarantine_contract.QuarantineContractError as exc:
        raise EngineLifecycleFenced(str(exc)) from exc


def engine_start_quarantine_pending(iid: str) -> bool:
    return start_quarantine_contract.is_pending(DATA_DIR, iid)


def engine_start_quarantine_status(iid: str) -> dict | None:
    """Return a bounded client-safe view; malformed presence stays manual/fail-closed."""
    canonical = start_quarantine_contract.canonical_iid(iid)
    if not start_quarantine_contract.is_pending(DATA_DIR, canonical):
        return None
    try:
        current = start_quarantine_contract.read_active(DATA_DIR, canonical)
        if current is None:
            return None
        record, _digest = current
        return {"valid": True, "reason": record["reason"]}
    except start_quarantine_contract.QuarantineContractError:
        return {"valid": False,
                "reason": "The durable Engine start quarantine is invalid."}


@contextmanager
def replacement_event_locked():
    """Linearize physical lifecycle evidence with the wrapper's final commit."""
    directory = os.path.join(DATA_DIR, "orchestrator")
    os.makedirs(directory, mode=0o700, exist_ok=True)
    path = os.path.join(directory, ENGINE_REPLACEMENT_EVENT_LOCK)
    fd = os.open(path, os.O_RDWR | os.O_CREAT, 0o600)
    handle = os.fdopen(fd, "r+")
    try:
        fcntl.flock(handle.fileno(), fcntl.LOCK_EX)
        yield
    finally:
        try:
            fcntl.flock(handle.fileno(), fcntl.LOCK_UN)
        finally:
            handle.close()


def engine_maintenance_pending(iid: str) -> bool:
    """Existence is the fail-closed per-line fence, even when JSON is corrupt."""
    return os.path.lexists(_maintenance_path(str(iid)))


def _validate_generation_facts(facts: object, role: str) -> None:
    if not isinstance(facts, dict) or set(facts) != {
            "container_id", "started_at", "restart_count", "pid", "run_id",
            "run_id_mode", "image_id"}:
        raise MaintenanceStateError(f"invalid engine maintenance {role} schema")
    if not isinstance(facts.get("container_id"), str) or not _HEX64.fullmatch(
            facts["container_id"]):
        raise MaintenanceStateError(f"invalid {role} container id")
    if (not isinstance(facts.get("image_id"), str)
            or not facts["image_id"].startswith("sha256:")
            or not _HEX64.fullmatch(facts["image_id"][7:])):
        raise MaintenanceStateError(f"invalid {role} image id")
    if (not isinstance(facts.get("started_at"), str)
            or not _STARTED_AT.fullmatch(facts["started_at"])):
        raise MaintenanceStateError(f"invalid {role} process generation")
    mode = facts.get("run_id_mode")
    run_id = facts.get("run_id")
    if mode == "present":
        if not isinstance(run_id, str) or not _RUN_ID.fullmatch(run_id):
            raise MaintenanceStateError(f"invalid {role} Engine run id")
    elif mode == "legacy_absent" and role == "source":
        if run_id != "":
            raise MaintenanceStateError("legacy absent run id must be empty")
    else:
        raise MaintenanceStateError(f"invalid {role} run id mode")
    if (type(facts.get("restart_count")) is not int or facts["restart_count"] < 0
            or type(facts.get("pid")) is not int or facts["pid"] <= 0):
        raise MaintenanceStateError(f"invalid {role} Docker incarnation")


def _validate_engine_maintenance(value: object, iid: str) -> dict:
    if not isinstance(value, dict) or set(value) != {
            "version", "txid", "instance", "phase", "source",
            "target_image_digest", "target", "rollback", "attempts", "manual_required",
            "source_create_spec", "source_create_spec_digest", "rollback_image_ref"}:
        raise MaintenanceStateError("invalid engine maintenance schema")
    if (type(value.get("version")) is not int or value.get("version") != 1
            or str(value.get("instance")) != str(iid)):
        raise MaintenanceStateError("engine maintenance identity mismatch")
    txid = value.get("txid")
    phase = value.get("phase")
    attempts = value.get("attempts")
    manual_required = value.get("manual_required")
    target = value.get("target_image_digest")
    source_create_spec_digest = value.get("source_create_spec_digest")
    rollback_image_ref = value.get("rollback_image_ref")
    if not isinstance(txid, str) or not _TXID.fullmatch(txid):
        raise MaintenanceStateError("invalid engine maintenance transaction")
    if phase not in _MAINTENANCE_PHASES:
        raise MaintenanceStateError("invalid engine maintenance phase")
    if type(attempts) is not int or not 0 <= attempts <= 100:
        raise MaintenanceStateError("invalid engine maintenance attempt count")
    if type(manual_required) is not bool:
        raise MaintenanceStateError("invalid engine maintenance manual flag")
    if (not isinstance(target, str) or not target.startswith("sha256:")
            or not _HEX64.fullmatch(target[7:])):
        raise MaintenanceStateError("invalid immutable target image digest")
    source_create_spec = _validate_engine_create_spec(
        value.get("source_create_spec"), str(iid))
    if (not isinstance(source_create_spec_digest, str)
            or not _HEX64.fullmatch(source_create_spec_digest)
            or _canonical_digest(source_create_spec) != source_create_spec_digest):
        raise MaintenanceStateError("invalid Engine create spec digest")
    if not isinstance(rollback_image_ref, str) \
            or not _ROLLBACK_IMAGE_REF.fullmatch(rollback_image_ref):
        raise MaintenanceStateError("invalid Engine rollback image reference")
    source = value.get("source")
    target_facts = value.get("target")
    rollback_facts = value.get("rollback")

    _validate_generation_facts(source, "source")
    if target_facts is not None:
        _validate_generation_facts(target_facts, "target")
        if target_facts["image_id"] != target:
            raise MaintenanceStateError("target facts do not match immutable image")
        if (target_facts["container_id"] == source["container_id"]
                or (source["run_id_mode"] == "present"
                    and target_facts["run_id"] == source["run_id"])):
            raise MaintenanceStateError("target did not create a new Engine generation")
    if phase in {"target_started", "verified"} and target_facts is None:
        raise MaintenanceStateError("target generation is required in this phase")
    if rollback_facts is not None:
        _validate_generation_facts(rollback_facts, "rollback")
        if rollback_facts["image_id"] != source["image_id"]:
            raise MaintenanceStateError("rollback facts do not match source image")
        occupied = {source["container_id"]}
        if target_facts is not None:
            occupied.add(target_facts["container_id"])
        if rollback_facts["container_id"] in occupied:
            raise MaintenanceStateError("rollback did not create a new container generation")
    if phase in {"rollback_started", "rollback_verified"} and rollback_facts is None:
        raise MaintenanceStateError("rollback generation is required in this phase")
    if manual_required != (phase == "manual_required"):
        raise MaintenanceStateError("manual-required phase is inconsistent")
    return json.loads(json.dumps(value))


def read_engine_maintenance(iid: str) -> dict | None:
    """Read one strict durable record; malformed content never means 'no maintenance'."""
    path = _maintenance_path(str(iid))
    try:
        with open(path, encoding="utf-8") as handle:
            return _validate_engine_maintenance(json.load(handle), str(iid))
    except FileNotFoundError:
        return None
    except MaintenanceStateError:
        raise
    except Exception as exc:
        raise MaintenanceStateError("unreadable engine maintenance record") from exc


def _atomic_json(path: str, value: dict) -> None:
    directory = os.path.dirname(path)
    os.makedirs(directory, mode=0o700, exist_ok=True)
    tmp = f"{path}.tmp.{os.getpid()}.{threading.get_ident()}"
    payload = json.dumps(value, ensure_ascii=False, sort_keys=True,
                         separators=(",", ":")).encode("utf-8") + b"\n"
    fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    try:
        with os.fdopen(fd, "wb", closefd=True) as handle:
            handle.write(payload)
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(tmp, path)
        dirfd = os.open(directory, os.O_RDONLY | getattr(os, "O_DIRECTORY", 0))
        try:
            os.fsync(dirfd)
        finally:
            os.close(dirfd)
    finally:
        try:
            os.unlink(tmp)
        except FileNotFoundError:
            pass


def _validate_engine_start_receipt(value: object, iid: str) -> dict:
    if not isinstance(value, dict) or set(value) != {
            "version", "instance", "txid", "intent", "phase", "image_id",
            "source_create_spec_digest", "attestation", "container_id", "attempts",
            "generation", "created_at", "updated_at"}:
        raise EngineStartReceiptError("invalid Engine start receipt schema")
    if (type(value.get("version")) is not int or value["version"] != 1
            or str(value.get("instance")) != str(iid)
            or not _TXID.fullmatch(str(value.get("txid") or ""))
            or value.get("intent") != "target"
            or value.get("phase") not in {"prepared", "created", "clearing"}
            or not isinstance(value.get("image_id"), str)
            or not value["image_id"].startswith("sha256:")
            or not _HEX64.fullmatch(value["image_id"][7:])
            or not _HEX64.fullmatch(str(value.get("source_create_spec_digest") or ""))
            or value.get("attestation") != "tx_label"
            or type(value.get("attempts")) is not int
            or not 0 <= value["attempts"] <= ENGINE_START_RECEIPT_MAX_ATTEMPTS):
        raise EngineStartReceiptError("invalid Engine start receipt identity")
    for field in ("created_at", "updated_at"):
        number = value.get(field)
        if (not isinstance(number, (int, float)) or isinstance(number, bool)
                or not math.isfinite(float(number)) or float(number) <= 0):
            raise EngineStartReceiptError("invalid Engine start receipt timestamp")
    if value["updated_at"] < value["created_at"]:
        raise EngineStartReceiptError("Engine start receipt timestamps are reversed")
    container_id = value.get("container_id")
    generation = value.get("generation")
    if value["phase"] == "prepared":
        if container_id is not None or generation is not None:
            raise EngineStartReceiptError("prepared Engine start receipt has a container")
    elif (not isinstance(container_id, str) or not _HEX64.fullmatch(container_id)
          or (value["phase"] == "created" and generation is not None)):
        raise EngineStartReceiptError("created Engine start receipt identity is invalid")
    elif value["phase"] == "clearing":
        try:
            _validate_generation_facts(generation, "target")
        except MaintenanceStateError as exc:
            raise EngineStartReceiptError(
                "clearing Engine start receipt generation is invalid") from exc
        if (generation["container_id"] != container_id
                or generation["image_id"] != value["image_id"]):
            raise EngineStartReceiptError(
                "clearing Engine start receipt generation changed")
    return json.loads(json.dumps(value))


def read_engine_start_receipt(iid: str) -> dict | None:
    """Strictly read the transaction WAL; malformed filesystem state remains manual."""
    path = _engine_start_receipt_path(str(iid))
    try:
        metadata = os.lstat(path)
        if (not stat.S_ISREG(metadata.st_mode) or stat.S_IMODE(metadata.st_mode) != 0o600
                or metadata.st_uid != os.geteuid()):
            raise EngineStartReceiptError("unsafe Engine start receipt file")
        with open(path, encoding="utf-8") as handle:
            return _validate_engine_start_receipt(json.load(handle), str(iid))
    except FileNotFoundError:
        return None
    except EngineStartReceiptError:
        raise
    except Exception as exc:
        raise EngineStartReceiptError("unreadable Engine start receipt") from exc


def _write_engine_start_receipt(iid: str, value: dict) -> dict:
    checked = _validate_engine_start_receipt(value, str(iid))
    path = _engine_start_receipt_path(str(iid))
    directory = os.path.dirname(path)
    os.makedirs(directory, mode=0o700, exist_ok=True)
    metadata = os.lstat(directory)
    if (not stat.S_ISDIR(metadata.st_mode) or stat.S_IMODE(metadata.st_mode) != 0o700
            or metadata.st_uid != os.geteuid()):
        raise EngineStartReceiptError("unsafe Engine start receipt directory")
    _atomic_json(path, checked)
    reread = read_engine_start_receipt(str(iid))
    if reread != checked:
        raise EngineStartReceiptError("Engine start receipt readback changed")
    return checked


def _require_start_receipt_scope(receipt: dict, marker: dict, iid: str) -> None:
    if (receipt.get("instance") != str(iid)
            or receipt.get("txid") != marker.get("txid")
            or receipt.get("intent") != "target"
            or receipt.get("image_id") != marker.get("target_image_digest")
            or receipt.get("source_create_spec_digest") !=
                marker.get("source_create_spec_digest")
            or receipt.get("attestation") != "tx_label"):
        raise EngineStartReceiptError("Engine start receipt scope changed")


def _attest_replacement_target_container(
        iid: str, marker: dict, container, expected_container_id: str | None = None) -> str:
    try:
        container.reload()
    except Exception as exc:
        raise EngineStartReceiptError("target container inspect failed") from exc
    attrs = getattr(container, "attrs", {}) or {}
    labels = (attrs.get("Config") or {}).get("Labels") or {}
    container_id = str(getattr(container, "id", "") or "")
    if (not _HEX64.fullmatch(container_id)
            or str(getattr(container, "name", "") or "") != container_name(iid)
            or (expected_container_id is not None
                and container_id != expected_container_id)
            or str(attrs.get("Image") or "") != marker["target_image_digest"]
            or labels.get(ENGINE_REPLACEMENT_TX_LABEL) != marker["txid"]
            or labels.get(ENGINE_REPLACEMENT_INTENT_LABEL) != "target"
            or labels.get(ENGINE_REPLACEMENT_SOURCE_SPEC_LABEL) !=
                marker["source_create_spec_digest"]):
        raise EngineStartReceiptError("target container attestation changed")
    return container_id


def attest_engine_start_receipt_container(iid: str, receipt: dict) -> str:
    marker = read_engine_maintenance(str(iid))
    if marker is None:
        raise EngineStartReceiptError("target receipt has no maintenance owner")
    checked = _validate_engine_start_receipt(receipt, str(iid))
    _require_start_receipt_scope(checked, marker, str(iid))
    if checked["phase"] not in {"created", "clearing"}:
        raise EngineStartReceiptError("target receipt is not created")
    try:
        container = _client().containers.get(container_name(str(iid)))
    except Exception as exc:
        raise EngineStartReceiptError("created target container is unavailable") from exc
    return _attest_replacement_target_container(
        str(iid), marker, container, checked["container_id"])


def prepared_engine_start_retryable(iid: str, txid: str) -> bool:
    """Whether another explicit wrapper run can safely resume the prepared target WAL."""
    receipt = read_engine_start_receipt(str(iid))
    marker = read_engine_maintenance(str(iid))
    if (receipt is None or marker is None or receipt.get("phase") != "prepared"
            or receipt.get("txid") != str(txid)
            or marker.get("txid") != str(txid)
            or marker.get("phase") != "target_starting"
            or receipt.get("attempts") >= ENGINE_START_RECEIPT_MAX_ATTEMPTS):
        return False
    _require_start_receipt_scope(receipt, marker, str(iid))
    try:
        container = _client().containers.get(container_name(str(iid)))
    except docker.errors.NotFound:
        return True
    _attest_replacement_target_container(str(iid), marker, container)
    return True


def _attest_clearing_receipt_without_marker(iid: str, receipt: dict) -> str:
    checked = _validate_engine_start_receipt(receipt, str(iid))
    if checked["phase"] != "clearing":
        raise EngineStartReceiptError("orphan target receipt is not clearing")
    synthetic_marker = {
        "txid": checked["txid"], "target_image_digest": checked["image_id"],
        "source_create_spec_digest": checked["source_create_spec_digest"],
    }
    try:
        container = _client().containers.get(container_name(str(iid)))
    except Exception as exc:
        raise EngineStartReceiptError("clearing target container is unavailable") from exc
    return _attest_replacement_target_container(
        str(iid), synthetic_marker, container, checked["container_id"])


def clear_engine_start_receipt(iid: str, txid: str, *, require_absent: bool) -> None:
    path = _engine_start_receipt_path(str(iid))
    receipt = read_engine_start_receipt(str(iid))
    if receipt is None:
        return
    if receipt["txid"] != str(txid):
        raise EngineStartReceiptError("another transaction owns the Engine start receipt")
    if require_absent:
        try:
            _client().containers.get(container_name(str(iid)))
        except docker.errors.NotFound:
            pass
        else:
            raise EngineStartReceiptError(
                "target receipt cannot clear while its Docker name exists")
    os.unlink(path)
    directory = os.path.dirname(path)
    dirfd = os.open(directory, os.O_RDONLY | getattr(os, "O_DIRECTORY", 0))
    try:
        os.fsync(dirfd)
    finally:
        os.close(dirfd)


def write_engine_maintenance(iid: str, value: dict) -> dict:
    checked = _validate_engine_maintenance(value, str(iid))
    _atomic_json(_maintenance_path(str(iid)), checked)
    return checked


@contextmanager
def engine_maintenance_locked(iid: str, blocking: bool = True):
    """Serialize a line transaction before taking the narrower P-CSCF flock."""
    base, _ = _instance_paths(str(iid))
    path = os.path.join(base, "run", ENGINE_MAINTENANCE_LOCK)
    fd = os.open(path, os.O_RDWR | os.O_CREAT, 0o600)
    handle = os.fdopen(fd, "r+")
    operation = fcntl.LOCK_EX | (0 if blocking else fcntl.LOCK_NB)
    try:
        fcntl.flock(handle.fileno(), operation)
        yield handle
    finally:
        try:
            fcntl.flock(handle.fileno(), fcntl.LOCK_UN)
        finally:
            handle.close()


def engine_generation_facts(iid: str, expected_container_id: str | None = None,
                            *, allow_legacy_run_id: bool = False) -> dict:
    """Return the exact running Docker+Asterisk incarnation used by maintenance CAS."""
    container = _client().containers.get(container_name(str(iid)))
    if not _owned(container):
        raise MaintenanceStateError("refusing foreign Engine generation")
    container.reload()
    if expected_container_id and str(container.id) != str(expected_container_id):
        raise MaintenanceStateError("Engine container generation changed")
    state = (container.attrs or {}).get("State") or {}
    if str(state.get("Status") or getattr(container, "status", "")) != "running":
        raise MaintenanceStateError("Engine generation is not running")
    image_id = str((container.attrs or {}).get("Image") or "")
    started_at = state.get("StartedAt")
    restart_count = (container.attrs or {}).get("RestartCount")
    pid = state.get("Pid")
    run_path = os.path.join(DATA_DIR, "instances", str(iid), "run", "engine-run-id")
    try:
        with open(run_path, encoding="utf-8") as handle:
            run_id = handle.read(257).strip()
        run_id_mode = "present"
    except FileNotFoundError as exc:
        if not allow_legacy_run_id:
            raise EngineRunIdUnavailable("Engine run id is unavailable") from exc
        run_id, run_id_mode = "", "legacy_absent"
    except OSError as exc:
        raise MaintenanceStateError("Engine run id is unavailable") from exc
    facts = {
        "container_id": str(container.id), "started_at": started_at,
        "restart_count": restart_count, "pid": pid, "run_id": run_id,
        "run_id_mode": run_id_mode, "image_id": image_id,
    }
    _validate_generation_facts(facts, "source" if allow_legacy_run_id else "target")
    return facts


def _acquire_engine_maintenance_admission(iid: str, txid: str,
                                        expected_container_id: str, target_image_digest: str):
    """A proven idle replacement may repair a USIM outage without permitting paid work."""
    ordinary = acquire_pcscf_admission(iid)
    if ordinary is not None or not usim_recovery_fence_pending(iid):
        return ordinary, None
    handle = _acquire_pcscf_flock(iid)
    if handle is None:
        return None, None
    try:
        if os.path.lexists(_run_path(iid, _PCSCF_REBIND_NAME)):
            raise MaintenanceStateError("P-CSCF rebind is pending")
        source = engine_generation_facts(iid, expected_container_id)
        with open(os.path.join(DATA_DIR, "orchestrator", ENGINE_REPLACEMENT_NAME),
                  encoding="utf-8") as stream:
            manifest = engine_replacement_contract.validate_manifest(json.load(stream))
        line = next((item for item in manifest["lines"] if item["iid"] == iid), None)
        if (manifest["txid"] != txid or manifest["phase"] not in {"prepared", "running"}
                or manifest["candidate_image"] != target_image_digest
                or not line or line["phase"] != "pending" or line["error"] or line["source"] != source
                or line["terminal"] is not None):
            raise MaintenanceStateError("USIM-fenced maintenance has no matching replacement owner")
        fence = _read_usim_recovery_fence(iid)
        status = read_run_json(iid, "usim_status.json") or {}
        if (not fence or fence["engine_run_id"] != source["run_id"]
                or status.get("engine_run_id") != source["run_id"]
                or type(status.get("auth_seq")) is not int or status["auth_seq"] != fence["auth_seq"]
                or type(status.get("latest_auth_seq")) is not int
                or status["latest_auth_seq"] != fence["auth_seq"]
                or status.get("state") != "AUTH_UNAVAILABLE"
                or status.get("cause_class") != fence["cause_class"]):
            raise MaintenanceStateError("USIM-fenced maintenance evidence changed")
        return handle, source
    except Exception as exc:
        release_pcscf_admission(handle)
        if isinstance(exc, MaintenanceStateError):
            raise
        raise MaintenanceStateError("USIM-fenced maintenance evidence is unavailable") from exc


def begin_engine_maintenance(iid: str, txid: str, expected_container_id: str,
                             target_image_digest: str,
                             rollback_image_ref: str) -> dict:
    """Publish the durable line fence while new Control submissions are totally ordered.

    This is not the paid-work drain by itself. The deployment owner must already hold the
    global admission fence and prove the SMS watcher/paid-lease gates before calling it.
    """
    iid = str(iid)
    with engine_maintenance_locked(iid):
        existing = read_engine_maintenance(iid)
        if existing is not None:
            if (existing["txid"] == txid
                    and existing["source"]["container_id"] == expected_container_id
                    and existing["target_image_digest"] == target_image_digest
                    and existing["rollback_image_ref"] == rollback_image_ref):
                return existing
            raise MaintenanceStateError("another Engine maintenance transaction exists")
        admission, admitted_source = _acquire_engine_maintenance_admission(
            iid, txid, expected_container_id, target_image_digest)
        if admission is None:
            raise MaintenanceStateError("P-CSCF admission is busy")
        try:
            source = engine_generation_facts(
                iid, expected_container_id, allow_legacy_run_id=True)
            if admitted_source is not None and source != admitted_source:
                raise MaintenanceStateError("Engine changed after maintenance admission")
            source_create_spec = capture_engine_create_spec(iid, expected_container_id)
            # Validate zero channels on the retained exact object, then reinspect the complete
            # Docker+run-id token before publishing. A later inbound call is handled by the
            # subsequent graceful barrier; unknown here always aborts.
            container = _client().containers.get(container_name(iid))
            if str(container.id) != expected_container_id:
                raise MaintenanceStateError("Engine changed before idle proof")
            rc, raw = container.exec_run(
                ["asterisk", "-rx", "core show channels count"])
            output = raw.decode(errors="replace") if isinstance(raw, bytes) else str(raw)
            match = re.search(r"\b(\d+)\s+active channels?\b", output, re.I)
            if rc != 0 or not match:
                raise MaintenanceStateError("active call state is unknown")
            if int(match.group(1)) != 0:
                raise MaintenanceStateError("an active call blocks maintenance")
            if engine_generation_facts(
                    iid, expected_container_id,
                    allow_legacy_run_id=(source["run_id_mode"] == "legacy_absent")) != source:
                raise MaintenanceStateError("Engine changed during idle proof")
            record = {
                "version": 1, "txid": txid, "instance": iid,
                "phase": "prepared", "source": source,
                "target_image_digest": target_image_digest, "target": None,
                "rollback": None,
                "source_create_spec": source_create_spec,
                "source_create_spec_digest": _canonical_digest(source_create_spec),
                "rollback_image_ref": rollback_image_ref,
                "attempts": 0, "manual_required": False,
            }
            return write_engine_maintenance(iid, record)
        finally:
            release_pcscf_admission(admission)


_MAINTENANCE_TRANSITIONS = {
    "prepared": {"source_quiescing", "aborted", "manual_required"},
    "source_quiescing": {"source_removed", "rollback_required", "aborted",
                          "manual_required"},
    "source_removed": {"target_starting", "rollback_starting", "manual_required"},
    "target_starting": {"target_started", "rollback_starting", "manual_required"},
    "target_started": {"verified", "rollback_starting", "manual_required"},
    "rollback_required": {"aborted", "rollback_starting", "manual_required"},
    "rollback_starting": {"rollback_started", "manual_required"},
    "rollback_started": {"rollback_verified", "manual_required"},
    "verified": set(), "rollback_verified": set(), "aborted": set(),
    "manual_required": set(),
}


def transition_engine_maintenance(iid: str, txid: str, expected_phase: str,
                                  phase: str, *, target: dict | None = None,
                                  rollback: dict | None = None,
                                  increment_attempts: bool = False) -> dict:
    """Strict durable compare-and-swap for one reviewed maintenance phase."""
    iid = str(iid)
    with engine_maintenance_locked(iid):
        current = read_engine_maintenance(iid)
        if current is None:
            raise MaintenanceStateError("Engine maintenance record disappeared")
        if current["txid"] != txid or current["phase"] != expected_phase:
            raise MaintenanceStateError("Engine maintenance compare-and-swap failed")
        if phase not in _MAINTENANCE_TRANSITIONS.get(expected_phase, set()):
            raise MaintenanceStateError("invalid Engine maintenance transition")
        if phase == "source_removed":
            try:
                _client().containers.get(container_name(iid))
            except docker.errors.NotFound:
                pass
            else:
                raise MaintenanceStateError("source_removed requires an absent Docker name")
        if phase == "aborted":
            actual = engine_generation_facts(
                iid, current["source"]["container_id"],
                allow_legacy_run_id=(
                    current["source"]["run_id_mode"] == "legacy_absent"))
            if actual != current["source"]:
                raise MaintenanceStateError("abort requires the exact preserved source")
        if phase in {"target_started", "verified"}:
            actual = engine_generation_facts(iid)
            if target is not None and actual != target:
                raise MaintenanceStateError("target generation evidence changed")
            target = actual
            if target["image_id"] != current["target_image_digest"]:
                raise MaintenanceStateError("target image digest mismatch")
        if phase in {"rollback_started", "rollback_verified"}:
            actual = engine_generation_facts(iid)
            if rollback is not None and actual != rollback:
                raise MaintenanceStateError("rollback generation evidence changed")
            rollback = actual
            if rollback["image_id"] != current["source"]["image_id"]:
                raise MaintenanceStateError("rollback image digest mismatch")
        updated = dict(current)
        updated["phase"] = phase
        if target is not None:
            updated["target"] = target
        if rollback is not None:
            updated["rollback"] = rollback
        if increment_attempts:
            updated["attempts"] += 1
        updated["manual_required"] = phase == "manual_required"
        return write_engine_maintenance(iid, updated)


def recover_precreate_missing_target_to_rollback(
        iid: str, txid: str, expected_source_container_id: str,
        expected_target_image: str) -> dict:
    """Recover only the proven pre-create receipt-API failure into rollback.

    This is deliberately not part of the normal transition graph. The operator wrapper must
    first abort every still-pending source line. Here we accept only the incident in which the
    source was removed, target creation never began, no receipt exists, and the retained source
    image is still exact. Any Docker object or changed field remains manual-required.
    """
    iid, txid = str(iid), str(txid)
    with engine_maintenance_locked(iid):
        current = read_engine_maintenance(iid)
        if (current is None or current.get("txid") != txid
                or current.get("phase") != "manual_required"
                or current.get("manual_required") is not True
                or current.get("attempts") != 0
                or current.get("target") is not None
                or current.get("rollback") is not None
                or current.get("target_image_digest") != expected_target_image
                or (current.get("source") or {}).get("container_id") !=
                    expected_source_container_id
                or _canonical_digest(current.get("source_create_spec")) !=
                    current.get("source_create_spec_digest")):
            raise MaintenanceStateError(
                "pre-create recovery does not match the exact manual transaction")
        if os.path.lexists(_engine_start_receipt_path(iid)):
            raise MaintenanceStateError(
                "pre-create recovery found an Engine start receipt")
        client = _client()
        for identity in (container_name(iid), expected_source_container_id):
            try:
                client.containers.get(identity)
            except docker.errors.NotFound:
                pass
            else:
                raise MaintenanceStateError(
                    "pre-create recovery requires source and target absence")
        try:
            scoped = client.containers.list(
                all=True, filters={"label": f"{ENGINE_REPLACEMENT_TX_LABEL}={txid}"})
        except Exception as exc:
            raise MaintenanceStateError(
                "pre-create recovery could not attest transaction containers") from exc
        if scoped:
            raise MaintenanceStateError(
                "pre-create recovery found a transaction-owned container")
        try:
            retained = client.images.get(current["rollback_image_ref"])
        except Exception as exc:
            raise MaintenanceStateError(
                "pre-create recovery rollback image is unavailable") from exc
        if str(getattr(retained, "id", "") or "") != current["source"]["image_id"]:
            raise MaintenanceStateError(
                "pre-create recovery rollback image changed")
        updated = dict(current)
        updated["phase"] = "rollback_starting"
        updated["manual_required"] = False
        return write_engine_maintenance(iid, updated)


def prepare_usim_fenced_source_rollback(
        iid: str, txid: str, expected_container_id: str,
        expected_target_image: str, rollback_image_ref: str,
        expected_engine_run_id: str, expected_auth_seq: int) -> dict:
    """Prepare a same-image rollback when the local PC/SC outage blocks normal begin.

    The USIM fence intentionally makes ``acquire_pcscf_admission`` return ``None``. This
    incident-only path creates no target and sends no REGISTER: it snapshots one exact idle
    source directly into rollback_starting so the Host can stop it and replay the retained old
    image. Every unknown observation remains fenced.
    """
    iid, txid = str(iid), str(txid)
    with engine_maintenance_locked(iid):
        existing = read_engine_maintenance(iid)
        if existing is not None:
            if (existing.get("txid") == txid
                    and existing.get("phase") in {
                        "rollback_starting", "rollback_started", "rollback_verified"}
                    and existing.get("source", {}).get("container_id") ==
                        expected_container_id):
                return existing
            raise MaintenanceStateError(
                "USIM-fenced rollback found another maintenance owner")
        fence = _read_usim_recovery_fence(iid)
        if (fence is None or fence.get("engine_run_id") != expected_engine_run_id
                or fence.get("auth_seq") != expected_auth_seq
                or fence.get("cause_class") != "pcsc_service_unavailable"):
            raise MaintenanceStateError("USIM-fenced rollback outage identity changed")
        failure = read_run_json(iid, "usim_status.json")
        if (not isinstance(failure, dict) or failure.get("state") != "AUTH_UNAVAILABLE"
                or failure.get("engine_run_id") != expected_engine_run_id
                or failure.get("auth_seq") != expected_auth_seq
                    or failure.get("cause_class") != "pcsc_service_unavailable"):
            raise MaintenanceStateError("USIM-fenced rollback failure evidence changed")
        source = engine_generation_facts(iid, expected_container_id)
        if source["run_id"] != expected_engine_run_id:
            raise MaintenanceStateError("USIM-fenced rollback source run changed")
        with _usim_recovery_locked(iid):
            recovery = _read_usim_recovery_unlocked(iid)
        if recovery is not None and (
                recovery.get("container_id") != expected_container_id
                or recovery.get("started_at") != source["started_at"]
                or recovery.get("engine_run_id") != expected_engine_run_id
                or recovery.get("auth_seq") != expected_auth_seq
                or recovery.get("cause_class") != "pcsc_service_unavailable"
                or recovery.get("phase") != "pending"
                or float(recovery.get("submitted_at") or 0) != 0.0
                or recovery.get("result_class") != ""):
            raise MaintenanceStateError(
                "USIM-fenced rollback recovery record is not unsubmitted and exact")
        container = _client().containers.get(container_name(iid))
        rc, raw = container.exec_run(["asterisk", "-rx", "core show channels count"])
        output = raw.decode(errors="replace") if isinstance(raw, bytes) else str(raw)
        match = re.search(r"\b(\d+)\s+active channels?\b", output, re.I)
        if rc != 0 or not match or int(match.group(1)) != 0:
            raise MaintenanceStateError("USIM-fenced rollback call state is not zero")
        if registration_state(iid) != "Rejected":
            raise MaintenanceStateError("USIM-fenced rollback registration is not Rejected")
        if os.path.lexists(_engine_start_receipt_path(iid)):
            raise MaintenanceStateError("USIM-fenced rollback found a target receipt")
        try:
            scoped = _client().containers.list(
                all=True, filters={"label": f"{ENGINE_REPLACEMENT_TX_LABEL}={txid}"})
        except Exception as exc:
            raise MaintenanceStateError(
                "USIM-fenced rollback could not attest transaction containers") from exc
        if scoped:
            raise MaintenanceStateError(
                "USIM-fenced rollback found a transaction-owned target")
        try:
            retained = _client().images.get(rollback_image_ref)
        except Exception as exc:
            raise MaintenanceStateError(
                "USIM-fenced rollback retained image is unavailable") from exc
        if str(getattr(retained, "id", "") or "") != source["image_id"]:
            raise MaintenanceStateError("USIM-fenced rollback retained image changed")
        source_create_spec = capture_engine_create_spec(iid, expected_container_id)
        record = {
            "version": 1, "txid": txid, "instance": iid,
            "phase": "rollback_starting", "source": source,
            "target_image_digest": expected_target_image,
            "target": None, "rollback": None,
            "source_create_spec": source_create_spec,
            "source_create_spec_digest": _canonical_digest(source_create_spec),
            "rollback_image_ref": rollback_image_ref,
            "attempts": 0, "manual_required": False,
        }
        return write_engine_maintenance(iid, record)


def recover_usim_rollback_started_after_missing_runid_exception(
        iid: str, txid: str, expected_container_id: str,
        manifest_started_at: float, failure_updated_at: float) -> dict:
    """Attest the already-created old-image rollback after the missing exception incident."""
    iid, txid = str(iid), str(txid)
    with engine_maintenance_locked(iid):
        current = read_engine_maintenance(iid)
        if (current is None or current.get("txid") != txid
                or current.get("phase") != "manual_required"
                or current.get("manual_required") is not True
                or current.get("attempts") != 0
                or current.get("target") is not None
                or current.get("rollback") is not None):
            raise MaintenanceStateError(
                "run-id exception recovery does not match the manual transaction")
        facts = engine_generation_facts(iid, expected_container_id)
        source = current["source"]
        if (facts["image_id"] != source["image_id"]
                or facts["image_id"] == current["target_image_digest"]
                or facts["container_id"] == source["container_id"]
                or facts["run_id"] == source["run_id"]
                or facts["restart_count"] != 0):
            raise MaintenanceStateError(
                "run-id exception recovery generation is not a fresh old-image rollback")
        try:
            started_text = re.sub(
                r"(\.\d{6})\d+(?=Z|[+-]\d{2}:\d{2}$)", r"\1",
                facts["started_at"])
            started_epoch = datetime.fromisoformat(
                started_text.replace("Z", "+00:00")).timestamp()
        except (TypeError, ValueError) as exc:
            raise MaintenanceStateError(
                "run-id exception recovery StartedAt is invalid") from exc
        if (not isinstance(manifest_started_at, (int, float))
                or not isinstance(failure_updated_at, (int, float))
                or not float(manifest_started_at) <= started_epoch <=
                    float(failure_updated_at)):
            raise MaintenanceStateError(
                "run-id exception recovery generation is outside the incident window")
        client = _client()
        try:
            client.containers.get(source["container_id"])
        except docker.errors.NotFound:
            pass
        else:
            raise MaintenanceStateError(
                "run-id exception recovery source container still exists")
        if os.path.lexists(_engine_start_receipt_path(iid)):
            raise MaintenanceStateError(
                "run-id exception recovery found a target receipt")
        if client.containers.list(
                all=True, filters={"label": f"{ENGINE_REPLACEMENT_TX_LABEL}={txid}"}):
            raise MaintenanceStateError(
                "run-id exception recovery found a transaction target")
        if client.containers.list(
                all=True, filters={"ancestor": current["target_image_digest"]}):
            raise MaintenanceStateError(
                "run-id exception recovery found a candidate container")
        try:
            retained = client.images.get(current["rollback_image_ref"])
        except Exception as exc:
            raise MaintenanceStateError(
                "run-id exception recovery rollback image is unavailable") from exc
        if str(getattr(retained, "id", "") or "") != source["image_id"]:
            raise MaintenanceStateError(
                "run-id exception recovery rollback image changed")
        if capture_engine_create_spec(iid, facts["container_id"]) != \
                current["source_create_spec"]:
            raise MaintenanceStateError(
                "run-id exception recovery create spec changed")
        updated = dict(current)
        updated["phase"] = "rollback_started"
        updated["rollback"] = facts
        updated["manual_required"] = False
        return write_engine_maintenance(iid, updated)


def clear_engine_maintenance(iid: str, txid: str, terminal_phase: str) -> None:
    """Clear only an explicit safe terminal after the global owner commits the same outcome."""
    iid = str(iid)
    path = _maintenance_path(iid)
    with engine_maintenance_locked(iid):
        current = read_engine_maintenance(iid)
        if current is None:
            receipt = read_engine_start_receipt(iid)
            if receipt is None:
                return
            if (terminal_phase != "verified" or receipt.get("txid") != txid
                    or receipt.get("phase") != "clearing"
                    or _attest_clearing_receipt_without_marker(iid, receipt) !=
                        receipt["container_id"]
                    or engine_generation_facts(
                        iid, receipt["container_id"]) != receipt["generation"]):
                raise EngineStartReceiptError(
                    "orphan Engine start receipt cannot complete terminal cleanup")
            clear_engine_start_receipt(iid, txid, require_absent=False)
            return
        if (terminal_phase not in {"verified", "rollback_verified", "aborted"}
                or current["txid"] != txid or current["phase"] != terminal_phase):
            raise MaintenanceStateError("only the terminal transaction owner may clear maintenance")
        receipt = read_engine_start_receipt(iid)
        if terminal_phase == "verified":
            if (receipt is None or receipt.get("phase") not in {"created", "clearing"}
                    or receipt.get("txid") != txid
                    or receipt.get("container_id") != current["target"]["container_id"]
                    or attest_engine_start_receipt_container(iid, receipt) !=
                        current["target"]["container_id"]):
                raise EngineStartReceiptError(
                    "verified target lacks its exact Engine start receipt")
            if engine_generation_facts(
                    iid, current["target"]["container_id"]) != current["target"]:
                raise EngineStartReceiptError(
                    "verified target generation changed before terminal cleanup")
            if receipt["phase"] == "clearing" and receipt.get(
                    "generation") != current["target"]:
                raise EngineStartReceiptError(
                    "clearing receipt generation disagrees with verified target")
            if receipt["phase"] == "created":
                receipt = _write_engine_start_receipt(iid, {
                    **receipt, "phase": "clearing", "generation": current["target"],
                    "updated_at": time.time(),
                })
        elif receipt is not None:
            raise EngineStartReceiptError(
                "non-target terminal still has an Engine start receipt")
        os.unlink(path)
        directory = os.path.dirname(path)
        dirfd = os.open(directory, os.O_RDONLY | getattr(os, "O_DIRECTORY", 0))
        try:
            os.fsync(dirfd)
        finally:
            os.close(dirfd)
        if terminal_phase == "verified":
            clear_engine_start_receipt(iid, txid, require_absent=False)


def _clear_runtime_state(base: str):
    """Remove observations owned by the previous engine process.

    Runtime files are bind-mounted outside the container and therefore survive a container
    recreation.  Keeping an old CONNECTED marker makes the new process look online before it
    has completed IKE and IMS registration.
    """
    run_dir = os.path.join(base, "run")
    for name in ("swu_status.json", "pcscf", "pcscf.applied", "pcscf-discovery.json",
                 "pcscf-rebind.json", "engine-run-id", "pin_status.json", "usim_status.json",
                 "usim-auth-recovery.json", "usim-auth-recovery.fence",
                 "registration_evidence.json", "engine.env",
                 "swu.ctl"):
        try:
            os.unlink(os.path.join(run_dir, name))
        except FileNotFoundError:
            pass
    for path in glob.glob(os.path.join(run_dir, "registration_evidence.*.json")):
        try:
            os.unlink(path)
        except FileNotFoundError:
            pass


def _tail_lines(path: str, limit: int) -> list[str]:
    try:
        with open(path, errors="replace") as handle:
            return handle.read().splitlines()[-limit:]
    except OSError:
        return []


def _charon_evidence(base: str) -> dict:
    """Summarise IKE health from the tunnel log.

    Retransmits and outright IKE timeouts are the signature of a lossy country exit, which
    looks identical to a carrier problem from the status machine's point of view.
    """
    lines = _tail_lines(os.path.join(base, "run", "charon.log"), 400)
    last_state = ""
    for line in reversed(lines):
        bare = _DISPLAY_TIMESTAMP.sub("", line, count=1)
        if bare.startswith("STATE ") or "tunnel CONNECTED" in bare:
            last_state = line.strip()
            break
    return {"retransmits": sum(1 for line in lines if "retransmit" in line),
            "timeouts": sum(1 for line in lines if "TIMEOUT" in line),
            "last_state": last_state, "tail": lines[-40:]}


def ike_evidence(iid: str) -> dict:
    """Retransmit/timeout counts for one line, without building a whole diagnostic snapshot.

    The failover policy needs to know whether the tunnel's own signalling was answered; it
    runs on the freeze path, where reading the container's logs would be far too heavy.
    """
    base, _host_base = _instance_paths(str(iid))
    return _charon_evidence(base)


def _sip_evidence(raw: str) -> list[str]:
    """Keep the SIP protocol lines and registration failures from a container log."""
    kept = []
    for line in raw.splitlines():
        line = _ANSI.sub("", line).rstrip()
        bare = _DISPLAY_TIMESTAMP.sub("", line, count=1)
        if not _SIP_EVIDENCE.search(bare):
            continue
        if _DEBUG_LINE.search(bare) and "SIP/2.0" not in bare:
            continue
        kept.append(line)
    return kept[-SIP_EVIDENCE_LINES:]


def _egress_evidence(inst: dict) -> dict:
    """Which exit node this line was using when it failed."""
    try:
        country = egress.line_country(inst)
        current = (egress.status().get("exits") or {}).get(country) or {}
        return {"country": country, "node": current.get("node", ""),
                "selection": current.get("selection", ""),
                "candidate_count": current.get("candidate_count"),
                "ready": current.get("ready")}
    except Exception:
        return {}


def _host_evidence() -> dict:
    """The host conditions that can take every line down at once, plus what they mean."""
    try:
        snapshot = sysinfo.collect(DATA_DIR)
        return {"alerts": [item["code"] for item in sysinfo.alerts(snapshot)],
                "throttling": snapshot.get("throttling") or {},
                "undervoltage": snapshot.get("undervoltage") or {},
                "temperature_c": snapshot.get("temperature_c"),
                "load": (snapshot.get("load") or {}).get("per_core"),
                "memory": snapshot.get("memory") or {},
                "network": snapshot.get("network") or {}}
    except Exception:
        return {}


def _append_diagnostic(base: str, record: dict):
    path = os.path.join(base, "logs", "diagnostics.jsonl")
    lines = _tail_lines(path, DIAGNOSTIC_RECORDS - 1)
    lines.append(json.dumps(record, ensure_ascii=False, sort_keys=True))
    tmp = path + ".tmp"
    with open(tmp, "w", encoding="utf-8") as handle:
        handle.write("\n".join(lines) + "\n")
    os.replace(tmp, path)


def capture_diagnostics(iid: str, inst: dict, base: str, reason: str):
    """Persist the evidence that recreating the container is about to destroy.

    Container logs and Asterisk's live registration view disappear with ``remove()``. A line
    stuck in the health policy's rebuild loop destroys its own evidence every couple of
    minutes, which is precisely when that evidence is needed. Never raises: a failed capture
    must not block the rebuild it is documenting.
    """
    try:
        record = {"ts": int(time.time()), "instance": str(iid), "reason": reason,
                  "registration": registration_state(iid),
                  "pcscf": read_pcscf(iid) or "",
                  "charon": _charon_evidence(base),
                  "egress": _egress_evidence(inst),
                  # A brown-out takes out the USB-attached NIC, the modem and the reader at
                  # once, and reaches the status machine as a tunnel that simply stopped
                  # passing traffic. Recorded here so that cause is visible beside the effect
                  # instead of being reconstructed from a shell session days later.
                  "host": _host_evidence()}
        for name in ("swu_status.json", "usim_status.json", "pin_status.json"):
            record[name[:-5]] = read_run_json(iid, name) or {}
        record["sip"] = _sip_evidence(logs(iid, 600))
        _append_diagnostic(base, record)
    except Exception as exc:  # noqa
        log.warning("diagnostic capture failed for instance %s: %s", iid, exc)


def _start_container(inst: dict, settings: dict, dev_mounts: bool = False,
                     reason: str = "rebuild", *, image: str = IMAGE,
                     replace_existing: bool = True, permit=None,
                     maintenance: bool = False):
    """Shared Engine create specification for normal replacement and maintenance start."""
    iid = str(inst["id"])
    _require_start_permit(permit, iid, maintenance=maintenance)
    client = _client()
    # This is deliberately the first externally mutating boundary. Missing/wrong ABI must leave
    # country routing, rendered config, the old container and every runtime observation intact.
    resolved_image = _require_engine_admission_abi(client, image)
    # Fail closed before creating the container when country routing is enabled. The host-side
    # orchestrator confirms that this carrier's outer ePDG address is routed through the selected
    # country TUN; inner IMS/SIP/RTP then stays inside the resulting IPsec tunnel.
    egress_state = egress.ensure_line(inst, settings) or {"ready": True, "mode": "legacy"}
    engine_environment = {
        "MDD_ID": iid,
        "SWU_LIVENESS_PERIOD": str(inst.get("liveness_period", 0)),
    }
    proxy_enabled = bool((settings.get("proxy") or {}).get("enabled", False))
    egress_mode = str(egress_state.get("mode") or "")
    if proxy_enabled and egress_mode not in {"direct", "disabled"}:
        outer_mtu = egress_state.get("outer_mtu")
        interface = str(egress_state.get("interface") or "")
        if type(outer_mtu) is not int or not 1280 <= outer_mtu <= 9000 or not interface:
            raise egress.EgressError(
                "country TUN is ready but its authoritative outer MTU state is missing")
        engine_environment["SWU_OUTER_MTU"] = str(outer_mtu)
    cfg.write_instance_json(inst, settings)
    base, host_base = _instance_paths(iid)
    ports = inst.get("ports", {})
    # Resolve the name before clearing any runtime state. Maintenance recovery is strictly
    # start-absent and must never force/remove an object it did not create.
    try:
        old = client.containers.get(container_name(iid))
        if not _owned(old):
            raise RuntimeError(f"refusing to replace foreign container {old.name}")
        if not replace_existing:
            raise EngineAlreadyExists(
                f"refusing absent-only start while {old.name} still exists")
        # Only a replacement destroys evidence; a first start has none to keep.
        capture_diagnostics(iid, inst, base, reason)
        old.remove(force=True)
    except docker.errors.NotFound:
        pass

    _clear_runtime_state(base)

    volumes = {
        os.path.join(host_base, "instance.json"): {"bind": "/config/instance.json", "mode": "ro"},
        os.path.join(host_base, "logs"): {"bind": "/logs", "mode": "rw"},
        os.path.join(host_base, "run"): {"bind": "/run/mdd-sim-gateway", "mode": "rw"},
        PCSCD_SOCK: {"bind": "/run/pcscd", "mode": "rw"},
    }
    # The image has no timezone, so every engine log (IKE, Asterisk) was stamped in UTC while
    # the timeline, the WebUI and the operator's shell read local time. Correlating a rekey or
    # a teardown with an outage meant doing the offset in your head. Give the container the
    # host's zone. Registration evidence parses this prefix back to epoch time so stale
    # failures can be ordered behind a later Registered tombstone.
    if os.path.exists("/etc/localtime"):
        volumes["/etc/localtime"] = {"bind": "/etc/localtime", "mode": "ro"}
    # TLS cert for the local SIP-TLS / WebRTC (WSS 8089) transport. An explicit cert in
    # settings.tls wins; otherwise fall back to the control plane's own self-signed cert
    # (generated by run.py under $MDD_DATA/certs) so the engine's WSS listener always has
    # a cert — without it Asterisk fails to bind 8089 and the browser softphone can't connect.
    # Bind-mounts must use the HOST path (engine is a sibling container); check existence via
    # the in-container DATA_DIR but mount the HOST_DATA_DIR path.
    tls = settings.get("tls", {})
    configured_cert = _runtime_data_path(tls.get("cert_path"))
    configured_key = _runtime_data_path(tls.get("key_path"))
    cert_host = key_host = None
    if configured_cert and os.path.exists(configured_cert) and \
            configured_key and os.path.exists(configured_key):
        cert_host = _host_data_path(configured_cert)
        key_host = _host_data_path(configured_key)
    else:
        # self-signed pair written by run.py: $MDD_DATA/certs/self-signed.{crt,key}
        ss_crt = os.path.join(DATA_DIR, "certs", "self-signed.crt")
        ss_key = os.path.join(DATA_DIR, "certs", "self-signed.key")
        if os.path.exists(ss_crt) and os.path.exists(ss_key):
            cert_host = os.path.join(HOST_DATA_DIR, "certs", "self-signed.crt")
            key_host = os.path.join(HOST_DATA_DIR, "certs", "self-signed.key")
        else:
            log.warning("no TLS cert available for engine %s WSS/8089 — browser softphone will "
                        "not connect until a cert exists (control plane cert at %s missing)", iid, ss_crt)
    if cert_host and key_host:
        volumes[cert_host] = {"bind": "/etc/asterisk/certificate.crt", "mode": "ro"}
        volumes[key_host] = {"bind": "/etc/asterisk/certificate.key", "mode": "ro"}

    eng_templates = "/opt/mdd-gateway/engine/templates"
    if os.path.isdir(eng_templates):
        volumes[eng_templates] = {"bind": "/opt/mdd-sim-gateway/templates", "mode": "ro"}

    if dev_mounts:
        eng = os.path.join(os.path.dirname(os.path.dirname(os.path.dirname(__file__))), "engine")
        for f in ["pin_keeper.py", "ami_usim.py", "render.py", "notify.py", "swu_ike.py",
                  "pcscf_state.py", "admission_gate.py", "log_capture.py"]:
            volumes[os.path.join(eng, f)] = {"bind": f"/usr/local/bin/{f}", "mode": "ro"}
        volumes[os.path.join(eng, "entrypoint.sh")] = {"bind": "/entrypoint.sh", "mode": "ro"}
        volumes[os.path.join(eng, "engine-runtime.sh")] = {
            "bind": "/engine-runtime.sh", "mode": "ro"}
        volumes[os.path.join(eng, "templates")] = {"bind": "/opt/mdd-sim-gateway/templates", "mode": "ro"}

    # The public browser reaches this transport only through Control's authenticated and
    # generation-fenced WS bridge.  Binding Asterisk WSS to the LAN would let a cached UI or
    # arbitrary SIP client bypass the one-shot browser-media admission before a carrier call.
    port_bindings = {f"{8089}/tcp": ("127.0.0.1", ports.get("webrtc", 8089))}
    # AMI grants system/command/originate. The manager dials the container bridge directly, so a
    # host mapping is unnecessary in normal operation and costs another docker-proxy. Keep the
    # loopback-only mapping as an explicit diagnostic option.
    if (settings.get("debug") or {}).get("ami", False):
        port_bindings[f"{5038}/tcp"] = ("127.0.0.1", ports.get("ami", 5038))
    # RTP range
    rtp_start = ports.get("rtp_start", 10000)
    for p in range(rtp_start, rtp_start + cfg.rtp_span(ports)):
        port_bindings[f"{p}/udp"] = p

    try:
        # Manual filesystem mutation does not honor the flock.  Re-read at the last possible
        # boundary even though a legitimate acquire cannot pass the held permit locks.
        _require_start_permit(permit, iid, maintenance=maintenance)
        c = client.containers.run(
            resolved_image,
            name=container_name(iid),
            detach=True,
            privileged=True,
            devices=["/dev/net/tun:/dev/net/tun:rwm"],
            volumes=volumes,
            ports=port_bindings,
            restart_policy={"Name": "unless-stopped"},
            labels={MANAGED_LABEL: "true", "io.mdd-sim-gateway.component": "engine"},
            environment=engine_environment,
            sysctls={
                "net.ipv6.conf.all.disable_ipv6": "0",
                "net.ipv6.conf.default.disable_ipv6": "0",
                "net.ipv6.conf.all.accept_ra": "0",
                "net.ipv6.conf.default.accept_ra": "0",
                "net.ipv6.conf.all.autoconf": "0",
                "net.ipv6.conf.default.autoconf": "0",
                "net.ipv6.conf.all.use_tempaddr": "0",
                "net.ipv6.conf.default.use_tempaddr": "0",
            },
            extra_hosts={"host.docker.internal": "host-gateway"},
        )
    except docker.errors.APIError as exc:
        explanation = str(getattr(exc, "explanation", "") or exc)
        if "address already in use" in explanation.lower() or "port is already allocated" in explanation.lower():
            raise EnginePortConflict(explanation) from exc
        raise
    log.info("started engine container %s", c.name)
    return c.id


def start(inst: dict, settings: dict, dev_mounts: bool = False,
          reason: str = "rebuild", *, _permit=None):
    """(Re)create and start the configured Engine, preserving legacy management semantics."""
    iid = str(inst.get("id") or "")
    if _permit is None:
        with normal_start_permit(iid) as permit:
            return start(inst, settings, dev_mounts=dev_mounts, reason=reason,
                         _permit=permit)
    permit = _require_start_permit(_permit, iid)
    if (global_maintenance_pending() or engine_default_promotion_pending()
            or engine_maintenance_pending(iid)):
        raise EngineLifecycleFenced(
            "normal Engine start is fenced by durable maintenance")
    return _start_container(inst, settings, dev_mounts=dev_mounts, reason=reason,
                            image=IMAGE, replace_existing=True, permit=permit)


def start_if_absent(inst: dict, settings: dict, dev_mounts: bool = False,
                    reason: str = "automatic-recovery", *, _permit=None):
    """Start a normal Engine only when no container currently owns its name.

    Background recovery must never inherit ``start()``'s replacement semantics: a manual
    start or the host orchestrator can win the race after the caller's last inspect. Docker's
    name reservation is the final atomic fence, so an existing generation is reported instead
    of being force-removed.
    """
    iid = str(inst.get("id") or "")
    if _permit is None:
        with normal_start_permit(iid) as permit:
            return start_if_absent(
                inst, settings, dev_mounts=dev_mounts, reason=reason,
                _permit=permit)
    permit = _require_start_permit(_permit, iid)
    if (global_maintenance_pending() or engine_default_promotion_pending()
            or engine_maintenance_pending(iid)):
        raise EngineLifecycleFenced(
            "normal Engine start is fenced by durable maintenance")
    return _start_container(inst, settings, dev_mounts=dev_mounts, reason=reason,
                            image=IMAGE, replace_existing=False, permit=permit)


def start_absent(inst: dict, settings: dict, target_image_digest: str, txid: str,
                 dev_mounts: bool = False, reason: str = "maintenance",
                 intent: str = "target"):
    """Start one immutable Engine image only when the Docker name is truly absent."""
    if (not isinstance(target_image_digest, str)
            or not target_image_digest.startswith("sha256:")
            or not _HEX64.fullmatch(target_image_digest[7:])):
        raise MaintenanceStateError("maintenance start requires an immutable image digest")
    iid = str(inst.get("id") or "")
    if intent != "target":
        raise MaintenanceStateError(
            "rollback requires the frozen create-spec and retained-image path")
    required_phase = "target_starting"
    with engine_maintenance_locked(iid):
        marker = read_engine_maintenance(iid)
        if marker is None:
            raise MaintenanceStateError("maintenance start requires a durable line fence")
        expected_digest = (marker["target_image_digest"] if intent == "target"
                           else marker["source"]["image_id"])
        if (marker["txid"] != txid or marker["phase"] != required_phase
                or expected_digest != target_image_digest):
            raise MaintenanceStateError("maintenance start ownership or phase mismatch")
        # Keep the maintenance lock over name resolution and create. Unlike the P-CSCF flock,
        # this lock is not used by Engine entrypoint initialization, so there is no init-run
        # deadlock; it prevents another transaction from changing phase/digest mid-create.
        with maintenance_start_permit(iid) as permit:
            return _start_container(
                inst, settings, dev_mounts=dev_mounts, reason=reason,
                image=target_image_digest, replace_existing=False, permit=permit,
                maintenance=True)


def _start_container_from_create_spec(
        spec: dict, image_digest: str, *, permit=None,
        txid: str = "", intent: str = "rollback") -> str:
    """Create an absent Engine from a previously verified allowlisted source spec."""
    iid = str(spec.get("instance") or "")
    rollback = getattr(permit, "mode", "") == "rollback"
    if rollback:
        _require_rollback_start_permit(permit, iid)
    else:
        _require_start_permit(permit, iid, maintenance=True)
    checked = _validate_engine_create_spec(spec, iid)
    client = _client()
    resolved_image = (_require_engine_rollback_admission_abi(client, image_digest)
                      if rollback else _require_engine_admission_abi(client, image_digest))
    try:
        existing = client.containers.get(container_name(iid))
    except docker.errors.NotFound:
        existing = None
    if existing is not None:
        raise EngineAlreadyExists(
            f"refusing create-spec replay while {container_name(iid)} exists")
    for binding in checked["binds"]:
        if not os.path.lexists(binding["host"]):
            raise MaintenanceStateError(
                f"Engine create-spec bind is unavailable: {binding['container']}")
    base, _ = _instance_paths(iid)
    _clear_runtime_state(base)
    volumes = {item["host"]: {"bind": item["container"], "mode": item["mode"]}
               for item in checked["binds"]}
    ports = {}
    for item in checked["ports"]:
        ports[item["container_port"]] = (
            (item["host_ip"], item["host_port"])
            if item["host_ip"] else item["host_port"])
    devices = [f"{item['host']}:{item['container']}:{item['permissions']}"
               for item in checked["devices"]]
    extra_hosts = dict(item.split(":", 1) for item in checked["extra_hosts"])
    labels = dict(checked["labels"])
    if not rollback:
        if not _TXID.fullmatch(str(txid or "")) or intent != "target":
            raise MaintenanceStateError("target create lacks transaction attestation")
        labels.update({
            ENGINE_REPLACEMENT_TX_LABEL: str(txid),
            ENGINE_REPLACEMENT_INTENT_LABEL: "target",
            ENGINE_REPLACEMENT_SOURCE_SPEC_LABEL:
                _canonical_digest(checked),
        })
    try:
        if rollback:
            _require_rollback_start_permit(permit, iid)
        else:
            _require_start_permit(permit, iid, maintenance=True)
        container = client.containers.run(
            resolved_image, name=container_name(iid), detach=True,
            privileged=True, devices=devices, volumes=volumes, ports=ports,
            restart_policy=checked["restart_policy"], labels=labels,
            environment=checked["environment"], sysctls=checked["sysctls"],
            extra_hosts=extra_hosts, network_mode=checked["network_mode"])
    except docker.errors.APIError as exc:
        explanation = str(getattr(exc, "explanation", "") or exc)
        if "address already in use" in explanation.lower() \
                or "port is already allocated" in explanation.lower():
            raise EnginePortConflict(explanation) from exc
        raise
    log.info("started engine container %s from frozen create spec", container.name)
    return str(container.id)


def start_absent_from_snapshot(iid: str, image_digest: str, txid: str,
                               *, intent: str = "target") -> str:
    """Replay the transaction's frozen create spec with one immutable image digest."""
    iid = str(iid)
    if intent not in {"target", "rollback"}:
        raise MaintenanceStateError("invalid maintenance snapshot start intent")
    required_phase = "target_starting" if intent == "target" else "rollback_starting"
    with engine_maintenance_locked(iid):
        marker = read_engine_maintenance(iid)
        if marker is None:
            raise MaintenanceStateError("snapshot start requires a durable line fence")
        expected_digest = (marker["target_image_digest"] if intent == "target"
                           else marker["source"]["image_id"])
        if (marker["txid"] != txid or marker["phase"] != required_phase
                or image_digest != expected_digest):
            raise MaintenanceStateError("snapshot start ownership or phase mismatch")
        if _canonical_digest(marker["source_create_spec"]) != \
                marker["source_create_spec_digest"]:
            raise MaintenanceStateError("snapshot create spec changed")
        if intent == "rollback":
            try:
                retained = _client().images.get(marker["rollback_image_ref"])
            except Exception as exc:
                raise MaintenanceStateError("rollback image retention tag is unavailable") from exc
            if str(getattr(retained, "id", "") or "") != image_digest:
                raise MaintenanceStateError("rollback image retention tag changed")
            with _rollback_start_permit(iid, txid, image_digest) as permit:
                return _start_container_from_create_spec(
                    marker["source_create_spec"], image_digest, permit=permit,
                    txid=txid, intent="rollback")

        receipt = read_engine_start_receipt(iid)
        now = time.time()
        if receipt is None:
            receipt = _write_engine_start_receipt(iid, {
                "version": 1, "instance": iid, "txid": txid,
                "intent": "target", "phase": "prepared", "image_id": image_digest,
                "source_create_spec_digest": marker["source_create_spec_digest"],
                "attestation": "tx_label", "container_id": None,
                "generation": None, "attempts": 0,
                "created_at": now, "updated_at": now,
            })
        _require_start_receipt_scope(receipt, marker, iid)
        client = _client()
        if receipt["phase"] == "created":
            attest_engine_start_receipt_container(iid, receipt)
            return receipt["container_id"]
        try:
            existing = client.containers.get(container_name(iid))
        except docker.errors.NotFound:
            existing = None
        if existing is not None:
            container_id = _attest_replacement_target_container(iid, marker, existing)
        else:
            if receipt["attempts"] >= ENGINE_START_RECEIPT_MAX_ATTEMPTS:
                raise EngineStartReceiptError("Engine target create attempts are exhausted")
            receipt = _write_engine_start_receipt(iid, {
                **receipt, "attempts": receipt["attempts"] + 1,
                "updated_at": time.time(),
            })
            try:
                with maintenance_start_permit(iid) as permit:
                    returned_id = _start_container_from_create_spec(
                        marker["source_create_spec"], image_digest, permit=permit,
                        txid=txid, intent="target")
            except Exception as exc:
                try:
                    existing = client.containers.get(container_name(iid))
                    container_id = _attest_replacement_target_container(
                        iid, marker, existing)
                except docker.errors.NotFound:
                    raise EngineStartReceiptError(
                        "Engine target create failed with exact name absent") from exc
                except EngineStartReceiptError:
                    raise
                except Exception as inspect_exc:
                    raise EngineStartReceiptError(
                        "Engine target create outcome is unknown") from inspect_exc
            else:
                try:
                    existing = client.containers.get(container_name(iid))
                    container_id = _attest_replacement_target_container(
                        iid, marker, existing, returned_id)
                except Exception as exc:
                    raise EngineStartReceiptError(
                        "Engine target create returned without exact attestation") from exc
        created = _write_engine_start_receipt(iid, {
            **receipt, "phase": "created", "container_id": container_id,
            "generation": None, "updated_at": time.time(),
        })
        attest_engine_start_receipt_container(iid, created)
        return created["container_id"]


def stop(iid: str, expected_container_id: str | None = None):
    try:
        c = _client().containers.get(container_name(iid))
        if not _owned(c):
            raise RuntimeError(f"refusing to remove foreign container {c.name}")
        if expected_container_id and str(c.id) != str(expected_container_id):
            log.info("not stopping replacement engine %s (expected generation %s, found %s)",
                     iid, expected_container_id, c.id)
            return False
        c.remove(force=True)
        return True
    except docker.errors.NotFound:
        return False


def capture_and_stop(iid: str, inst: dict, reason: str,
                     expected_container_id: str | None = None) -> bool:
    """Snapshot a failing line, then remove its container.

    The health policy gives up by stopping the container and only rebuilds it after a
    cooldown, so by the time ``start()`` runs there is nothing left to read. This is the
    path that destroys the evidence in practice, and it is also the exact moment worth
    recording: the policy has just concluded the line cannot register.

    Blocking (Docker exec + log read); callers on the event loop must use a worker thread.
    """
    if expected_container_id:
        try:
            current = _client().containers.get(container_name(iid))
            if not _owned(current):
                raise RuntimeError(f"refusing to inspect foreign container {current.name}")
            if str(current.id) != str(expected_container_id):
                return False
        except docker.errors.NotFound:
            return False
    base, _ = _instance_paths(iid)
    capture_diagnostics(iid, inst, base, reason)
    return (stop(iid, expected_container_id=expected_container_id)
            if expected_container_id else stop(iid))


def capture_and_stop_if_idle(iid: str, inst: dict, reason: str,
                             expected_container_id: str) -> dict:
    """Capture evidence and safely stop one idle Engine generation.

    Docker keeps the same container ID across an ``unless-stopped`` restart, so the ID alone
    is not an Asterisk-lifetime fence.  Every zero-channel sample and graceful-shutdown command
    below is bound to ``State.Pid + State.StartedAt + RestartCount``.  One changed incarnation
    may retry the whole transaction once; no sample is ever carried across that boundary.
    """
    terminal_statuses = {"exited", "dead"}
    client = None
    container = None
    removed = False
    restart_disabled = False
    original_restart_policy = {"Name": "no"}
    graceful_token = None

    def result(status: str, **fields) -> dict:
        return {"status": status, "stopped": status == "stopped", **fields}

    def reload_view() -> dict:
        try:
            container.reload()
        except docker.errors.NotFound:
            return {"kind": "error", "result": result("missing")}
        except Exception as exc:
            return {"kind": "error", "result": result(
                "quiesce_state_unknown", error=str(exc))}
        if str(container.id) != str(expected_container_id):
            return {"kind": "error", "result": result("generation_changed")}
        status = str(getattr(container, "status", "unknown"))
        if status in terminal_statuses:
            return {"kind": "terminal", "status": status}
        if status != "running":
            return {"kind": "error", "result": result(
                "quiesce_state_unknown", container_status=status)}
        state = (container.attrs or {}).get("State") or {}
        pid = state.get("Pid")
        started_at = state.get("StartedAt")
        restart_count = (container.attrs or {}).get("RestartCount")
        if (type(pid) is not int or pid <= 0 or
                not isinstance(started_at, str) or not started_at or
                type(restart_count) is not int or restart_count < 0):
            return {"kind": "error", "result": result(
                "quiesce_state_unknown", container_status=status,
                error="Docker did not provide a stable Asterisk incarnation")}
        return {"kind": "running", "token": (pid, started_at, restart_count)}

    def active_channels() -> int | None:
        try:
            rc, raw = container.exec_run(
                ["asterisk", "-rx", "core show channels count"])
        except Exception:
            return None
        output = raw.decode(errors="replace") if isinstance(raw, bytes) else str(raw)
        match = re.search(r"\b(\d+)\s+active channels?\b", output, re.I)
        return int(match.group(1)) if rc == 0 and match else None

    def finalize_terminal() -> tuple[dict, bool]:
        """Remove only this exact stopped object; report a same-ID restart as retryable."""
        nonlocal removed
        view = reload_view()
        if view["kind"] != "terminal":
            if view["kind"] == "running":
                return result("quiesce_restart_race"), True
            return view["result"], False
        try:
            container.remove(force=False)
            removed = True
            return result("stopped", active_channels=0), False
        except Exception as exc:
            raced = reload_view()
            if raced["kind"] == "running":
                return result("quiesce_restart_race", error=str(exc)), True
            if raced["kind"] == "error":
                return raced["result"], False
            return result("quiesce_finalize_failed", error=str(exc)), False

    def abort_and_observe(base: dict, token: tuple) -> dict:
        """Abort one graceful barrier and observe delayed exit without touching a new PID."""
        view = reload_view()
        if (view["kind"] == "error"
                and view["result"].get("status") == "quiesce_state_unknown"):
            # One transient inspect failure must not strand a graceful barrier. This is the
            # only retry and remains bound to the same retained container handle.
            time.sleep(0.1)
            view = reload_view()
        if view["kind"] == "terminal":
            return finalize_terminal()[0]
        if view["kind"] == "error":
            return view["result"]
        if view["token"] != token:
            return base
        try:
            container.exec_run(["asterisk", "-rx", "core abort shutdown"])
        except Exception:
            pass
        for _ in range(20):
            time.sleep(0.5)
            view = reload_view()
            if view["kind"] == "terminal":
                return finalize_terminal()[0]
            if view["kind"] == "error":
                return view["result"]
            if view["token"] != token:
                return base
        return base

    def restore_original_policy(base: dict) -> dict:
        """Only call this for a deliberately preserved, currently running Engine."""
        if not restart_disabled or removed:
            return base
        restore_error = ""
        original_name = str(original_restart_policy.get("Name") or "no")

        def policy_matches_original() -> bool:
            actual = (((container.attrs or {}).get("HostConfig") or {}).get(
                "RestartPolicy") or {})
            if str(actual.get("Name") or "no") != original_name:
                return False
            if original_name == "on-failure":
                return int(actual.get("MaximumRetryCount") or 0) == int(
                    original_restart_policy.get("MaximumRetryCount") or 0)
            return True

        for restore_attempt in range(2):
            view = reload_view()
            if view["kind"] == "terminal":
                final, retry = finalize_terminal()
                if not retry:
                    return final
            elif view["kind"] == "error":
                return view["result"]
            if policy_matches_original():
                return base
            actual_name = str(((((container.attrs or {}).get("HostConfig") or {}).get(
                "RestartPolicy") or {}).get("Name") or "no"))
            if actual_name != "no":
                return result("restart_policy_restore_failed",
                              error="restart policy changed concurrently")
            try:
                container.update(restart_policy=original_restart_policy)
                return base
            except Exception as exc:
                restore_error = str(exc)
                raced = reload_view()
                if raced["kind"] == "terminal":
                    final, retry = finalize_terminal()
                    if not retry:
                        return final
                elif raced["kind"] == "error":
                    return raced["result"]
                if policy_matches_original():
                    return base
                if restore_attempt == 1:
                    return result("restart_policy_restore_failed", error=restore_error)
        return result("restart_policy_restore_failed", error=restore_error)

    def finish_preserved(base: dict, token: tuple | None = None) -> dict:
        """Single cleanup exit for every transaction that did not remove the Engine."""
        if base.get("stopped") or removed:
            return base
        if token is not None:
            base = abort_and_observe(base, token)
            if base.get("stopped") or removed:
                return base
        return restore_original_policy(base)

    try:
        client = docker.from_env(timeout=5)
        container = client.containers.get(container_name(iid))
        if not _owned(container):
            return result("foreign")
        if str(container.id) != str(expected_container_id):
            return result("generation_changed")
        base, _ = _instance_paths(iid)
        capture_diagnostics(iid, inst, base, reason)

        # Capture is intentionally outside the critical samples. Resolve the name once more,
        # then retain this exact handle for every later inspect/exec/stop/remove operation.
        container = client.containers.get(container_name(iid))
        if not _owned(container):
            return result("foreign")
        if str(container.id) != str(expected_container_id):
            return result("generation_changed")
        container.reload()
        policy = dict(((container.attrs.get("HostConfig") or {}).get(
            "RestartPolicy") or {}))
        policy_name = str(policy.get("Name") or "no")
        original_restart_policy = {
            "Name": policy_name,
            **({"MaximumRetryCount": int(policy.get("MaximumRetryCount") or 0)}
               if policy_name == "on-failure" else {}),
        }
        if policy_name != "no":
            disable_error = ""
            for disable_attempt in range(2):
                # A terminal observation can lose a race to the already-armed restart
                # manager.  Consume finalize_terminal's retry flag and re-run this bounded
                # policy transition on the same object instead of entering a long backoff
                # while the original policy is still active.
                view = reload_view()
                if view["kind"] == "terminal":
                    final, retry = finalize_terminal()
                    if not retry:
                        return final
                elif view["kind"] == "error":
                    return view["result"]
                actual_policy = str((((container.attrs or {}).get("HostConfig") or {}).get(
                    "RestartPolicy") or {}).get("Name") or "no")
                if actual_policy == "no":
                    restart_disabled = True
                    break
                if actual_policy != policy_name:
                    return result("restart_policy_disable_failed",
                                  error="restart policy changed concurrently")
                try:
                    container.update(restart_policy={"Name": "no"})
                    restart_disabled = True
                    break
                except Exception as exc:
                    disable_error = str(exc)
                    # Docker may have committed the update but lost the response, or PID 1
                    # may have exited while it was in flight. Reinspect this exact handle once.
                    raced = reload_view()
                    if raced["kind"] == "terminal":
                        final, retry = finalize_terminal()
                        if not retry:
                            return final
                    elif raced["kind"] == "error":
                        return raced["result"]
                    actual_policy = str((((container.attrs or {}).get(
                        "HostConfig") or {}).get("RestartPolicy") or {}).get(
                            "Name") or "no")
                    if actual_policy == "no":
                        restart_disabled = True
                        break
                    if disable_attempt == 1:
                        return result("restart_policy_disable_failed",
                                      error=disable_error)
            if not restart_disabled:
                return result("restart_policy_disable_failed", error=disable_error)

        final = result("quiesce_restart_race")
        for attempt in range(2):
            graceful_token = None
            view = reload_view()
            if view["kind"] == "terminal":
                final, retry = finalize_terminal()
                if retry and attempt == 0:
                    continue
                return finish_preserved(final)
            if view["kind"] == "error":
                return finish_preserved(view["result"])
            token = view["token"]
            first = active_channels()
            if first is None:
                return finish_preserved(result("call_state_unknown"))
            if first > 0:
                return finish_preserved(result(
                    "active_call", active_channels=first))

            graceful_token = token
            try:
                rc, _raw = container.exec_run(
                    ["asterisk", "-rx", "core stop gracefully"])
            except Exception as exc:
                view = reload_view()
                if view["kind"] == "terminal":
                    final, retry = finalize_terminal()
                    if retry and attempt == 0:
                        graceful_token = None
                        continue
                    return finish_preserved(final, graceful_token)
                return finish_preserved(result(
                    "quiesce_failed", error=str(exc)), graceful_token)
            if rc != 0:
                view = reload_view()
                if view["kind"] == "terminal":
                    final, retry = finalize_terminal()
                    if retry and attempt == 0:
                        graceful_token = None
                        continue
                    return finish_preserved(final, graceful_token)
                return finish_preserved(result("quiesce_failed"), graceful_token)

            # Bind the post-barrier zero sample to the same Asterisk process. If Docker has
            # already restarted PID 1, discard both samples and use the single retry budget.
            view = reload_view()
            if view["kind"] == "terminal":
                final, retry = finalize_terminal()
                if retry and attempt == 0:
                    graceful_token = None
                    continue
                return finish_preserved(final, graceful_token)
            if view["kind"] == "error":
                return finish_preserved(view["result"], graceful_token)
            if view["token"] != token:
                if attempt == 0:
                    graceful_token = None
                    continue
                return finish_preserved(final)
            second = active_channels()
            if second is None:
                return finish_preserved(result("call_state_unknown"), graceful_token)
            if second > 0:
                return finish_preserved(
                    result("active_call", active_channels=second), graceful_token)
            view = reload_view()
            if view["kind"] == "terminal":
                final, retry = finalize_terminal()
                if retry and attempt == 0:
                    graceful_token = None
                    continue
                return finish_preserved(final, graceful_token)
            if view["kind"] == "error":
                return finish_preserved(view["result"], graceful_token)
            if view["token"] != token:
                if attempt == 0:
                    graceful_token = None
                    continue
                return finish_preserved(final)

            stop_error = None
            try:
                # This is the Docker manual-stop fence. ``restart=no`` alone does not cancel
                # an already armed unless-stopped restart manager on every daemon version.
                container.stop(timeout=2)
            except Exception as exc:
                stop_error = str(exc)
            retry_incarnation = False
            for _ in range(10):
                time.sleep(0.2)
                view = reload_view()
                if view["kind"] == "terminal":
                    final, retry = finalize_terminal()
                    if retry and attempt == 0:
                        retry_incarnation = True
                        graceful_token = None
                        break
                    return finish_preserved(final, graceful_token)
                if view["kind"] == "error":
                    return finish_preserved(view["result"], graceful_token)
                if view["token"] != token:
                    if attempt == 0:
                        retry_incarnation = True
                        graceful_token = None
                        break
                    return finish_preserved(final)
            if retry_incarnation:
                continue
            final = result("quiesce_restart_race",
                           **({"error": stop_error} if stop_error else {}))
            if attempt == 0:
                continue
            # The second bounded transaction exhausted while the same Asterisk incarnation
            # is still running. Cancel its graceful barrier once and restore the immutable
            # policy only if it remains a deliberately preserved running process.
            return finish_preserved(final, graceful_token)
        return finish_preserved(final, graceful_token)
    except docker.errors.NotFound:
        return result("missing")
    except Exception as exc:  # noqa: broad fail-closed boundary around Docker/CLI
        log.warning("idle recovery check failed for engine %s: %s", iid, exc)
        return finish_preserved(
            result("error", error=str(exc)), graceful_token) if restart_disabled else result(
                "error", error=str(exc))
    finally:
        if client is not None:
            try:
                client.close()
            except Exception:
                pass


def stop_for_card_loss(iid: str, inst: dict, event: dict) -> dict:
    try:
        with replacement_event_locked():
            return _stop_for_card_loss_locked(iid, inst, event)
    except Exception as exc:
        # The audit/interlock plane must never become a reason to leave a physically absent
        # SIM's Engine running. Resolve the named generation once and contain that exact ID;
        # without the lock/receipt the replacement transaction remains manual.
        stopped = False
        try:
            current = _client().containers.get(container_name(str(iid)))
            stopped = bool(stop(str(iid), expected_container_id=str(current.id)))
        except docker.errors.NotFound:
            stopped = False
        except Exception:
            try:
                stopped = bool(stop(str(iid)))
            except Exception:
                stopped = False
        log.error("card-loss transaction failed for Engine %s: %s", iid, exc)
        return {"status": "forced_manual", "stopped": stopped,
                "receipt": False, "reason": "card_loss_transaction_failed"}


def _stop_for_card_loss_locked(iid: str, inst: dict, event: dict) -> dict:
    """Contain one physically absent SIM, preserving replacement ownership evidence.

    Physical removal is a billing-safety boundary, so an active replacement never defers the
    stop.  Only a transaction-bound zero-channel receipt is allowed to authorize the resulting
    unscoped snapshot subtraction; every unknown/non-zero path still force-contains the exact
    Docker generation but leaves replacement recovery manual.
    """
    iid = str(iid)
    reason = str(event.get("reason") or "")
    card_event = {
        "reader_name": str(event.get("reader_name") or ""),
        "reader_index": int(event.get("reader_index", -1)),
        "iccid": str(event.get("iccid") or ""), "matched": iid,
    }

    def contain_named(expected: str | None = None) -> bool:
        """Resolve the currently named generation once, then stop that exact ID."""
        observed = None
        try:
            observed = str(_client().containers.get(container_name(iid)).id)
        except docker.errors.NotFound:
            return False
        except Exception:
            pass
        if observed:
            return bool(stop(iid, expected_container_id=observed))
        # Docker inspect itself is unavailable. Physical safety keeps the historical named
        # containment fallback, while the event lock guarantees the wrapper observes drift
        # before commit; this path can never produce an accepted receipt.
        return bool(stop(iid, expected_container_id=expected)) if expected else bool(stop(iid))

    def fence_uncertain(code: str) -> bool:
        """Best-effort two durable manual-only fences; neither authorizes topology drift."""
        durable = False
        try:
            _write_scoped_card_loss_uncertainty(iid, event, code)
            durable = True
        except Exception as exc:
            log.error("scoped card-loss uncertainty write failed for Engine %s: %s",
                      iid, exc)
        try:
            marker = read_engine_maintenance(iid)
            if (marker is not None and marker.get("phase") != "manual_required"):
                transition_engine_maintenance(
                    iid, marker["txid"], marker["phase"], "manual_required")
                durable = True
        except Exception as exc:
            log.error("scoped card-loss line fence failed for Engine %s: %s", iid, exc)
        return durable

    try:
        manifest = _active_replacement_manifest()
    except MaintenanceStateError:
        # The physical safety action still happens, but corrupt ownership can never receive an
        # authorizing receipt.
        fenced = fence_uncertain("manifest_unreadable")
        stopped = contain_named()
        return {"status": "forced_manual", "stopped": bool(stopped),
                "receipt": False, "intent": fenced,
                "reason": "replacement_manifest_unreadable"}
    if manifest is None:
        stopped = contain_named()
        return {"status": "stopped" if stopped else "missing", "stopped": bool(stopped),
                "receipt": False, "reason": reason}

    scoped = next((line for line in manifest["lines"] if line["iid"] == iid), None)
    if scoped is not None and scoped["phase"] != "skipped_absent":
        if (reason not in engine_replacement_contract.SCOPED_CARD_LOSS_REASONS
                or not card_event["reader_name"] or card_event["reader_index"] < 0
                or not card_event["iccid"]):
            fenced = fence_uncertain("card_identity_incomplete")
            stopped = contain_named()
            return {"status": "forced_manual", "stopped": bool(stopped),
                    "receipt": False, "intent": fenced,
                    "reason": "card_event_identity_incomplete"}
        evidence = {
            "iid": iid, "source": scoped["source"], "reason": reason, "card": card_event,
        }
        intent = {
            "version": 1, "txid": manifest["txid"],
            "scope_digest": engine_replacement_contract.replacement_scope_digest(manifest),
            "iid": iid, "source": scoped["source"], "reason": reason,
            "attestation": engine_replacement_contract.SCOPED_CARD_LOSS_ATTESTATION,
            "card": card_event,
            "evidence_digest": hashlib.sha256(json.dumps(
                evidence, sort_keys=True, separators=(",", ":")).encode()).hexdigest(),
            "created_at": time.time(),
        }
        try:
            _write_scoped_card_loss_intent(manifest, intent)
        except Exception as exc:
            # The uncertainty path and existing line marker are independent manual-only
            # fallbacks when the strict transaction-bound tombstone cannot be committed.
            fenced = fence_uncertain("intent_write_failed")
            stopped = contain_named()
            log.error("scoped card-loss intent write failed for Engine %s: %s", iid, exc)
            return {"status": "forced_manual", "stopped": bool(stopped),
                    "receipt": False, "intent": fenced,
                    "reason": "scoped_intent_write_failed"}
        stopped = contain_named()
        return {"status": "forced_manual", "stopped": bool(stopped),
                "receipt": False, "intent": True,
                "reason": "scoped_card_loss"}

    original = next((item for item in manifest["unscoped"] if item["iid"] == iid), None)
    try:
        current_values = engine_replacement_contract.snapshot_unscoped_engines(
            _client(), set(manifest["iids"]))
        current = next((item for item in current_values if item["iid"] == iid), None)
    except Exception:
        current = None
    if original is None or current != original:
        # Scoped or already-drifted generations are not owned by this sidecar contract. Stop
        # the currently named Engine for physical safety and force the host transaction manual.
        stopped = contain_named((current or original or {}).get("container_id"))
        return {"status": "forced_manual", "stopped": bool(stopped),
                "receipt": False, "reason": "replacement_generation_unowned"}

    if (reason not in engine_replacement_contract.UNSCOPED_REMOVAL_REASONS
            or not card_event["reader_name"] or card_event["reader_index"] < 0
            or not card_event["iccid"]):
        stopped = contain_named(original["container_id"])
        return {"status": "forced_manual", "stopped": bool(stopped),
                "receipt": False, "reason": "card_event_identity_incomplete"}

    channels = active_channel_count(iid)
    now = time.time()
    event_digest = hashlib.sha256(json.dumps({
        "iid": iid, "original": original, "reason": reason, "card": card_event,
    }, sort_keys=True, separators=(",", ":")).encode()).hexdigest()
    phase = "planned" if channels == 0 else "forced_unknown"
    initial_receipt = {
        "version": 1, "txid": manifest["txid"],
        "scope_digest": engine_replacement_contract.replacement_scope_digest(manifest),
        "iid": iid, "original": original, "phase": phase, "reason": reason,
        "attestation": "control_card_monitor", "card": card_event,
        "channels": channels if type(channels) is int else -1,
        "evidence_digest": event_digest, "created_at": now, "updated_at": now,
    }
    try:
        receipt = _write_unscoped_removal_receipt(manifest, initial_receipt)
    except Exception as exc:
        # Evidence durability controls replacement recovery, never physical containment.
        stopped = False
        if channels == 0:
            result = capture_and_stop_if_idle(
                iid, inst, f"replacement-{reason}", original["container_id"])
            stopped = result.get("status") == "stopped"
        if not stopped:
            stopped = contain_named(original["container_id"])
        log.error("card-loss receipt write failed for Engine %s: %s", iid, exc)
        return {"status": "forced_manual", "stopped": bool(stopped),
                "receipt": False, "reason": "receipt_write_failed"}

    stopped = False
    if channels == 0:
        result = capture_and_stop_if_idle(
            iid, inst, f"replacement-{reason}", original["container_id"])
        stopped = result.get("status") == "stopped"
    if not stopped:
        # A call appeared after the first zero sample, the CLI became unknown, or graceful
        # containment failed. Immediate exact-ID removal is safer for billing, but it is not
        # evidence the replacement wrapper may accept.
        stopped = contain_named(original["container_id"])
        try:
            receipt = _write_unscoped_removal_receipt(manifest, {
                **receipt, "phase": "forced_unknown", "updated_at": time.time(),
            })
        except Exception as exc:
            log.error("forced card-loss receipt update failed for Engine %s: %s", iid, exc)
        return {"status": "forced_manual", "stopped": bool(stopped),
                "receipt": False, "reason": "forced_unknown"}

    client = _client()
    try:
        try:
            client.containers.get(original["container_id"])
            absent_by_id = False
        except docker.errors.NotFound:
            absent_by_id = True
        try:
            client.containers.get(container_name(iid))
            absent_by_name = False
        except docker.errors.NotFound:
            absent_by_name = True
    except Exception:
        absent_by_id = absent_by_name = False
    if not (absent_by_id and absent_by_name):
        stopped = contain_named(original["container_id"])
        try:
            _write_unscoped_removal_receipt(manifest, {
                **receipt, "phase": "forced_unknown", "updated_at": time.time(),
            })
        except Exception as exc:
            log.error("unproven card-loss receipt update failed for Engine %s: %s", iid, exc)
        return {"status": "forced_manual", "stopped": bool(stopped),
                "receipt": False, "reason": "removal_not_proven"}
    try:
        receipt = _write_unscoped_removal_receipt(manifest, {
            **receipt, "phase": "removed", "updated_at": time.time(),
        })
    except Exception as exc:
        log.error("removed card-loss receipt commit failed for Engine %s: %s", iid, exc)
        return {"status": "forced_manual", "stopped": True,
                "receipt": False, "reason": "receipt_write_failed"}
    return {"status": "stopped", "stopped": True, "receipt": True,
            "reason": receipt["reason"]}


def delete_instance_data(iid: str, *, _permit=None) -> bool:
    """Remove one deleted line's rendered config, runtime markers and bounded logs."""
    if _permit is None:
        with normal_delete_permit(iid) as permit:
            return delete_instance_data(iid, _permit=permit)
    _require_delete_permit(_permit, iid)
    root = os.path.realpath(os.path.join(DATA_DIR, "instances"))
    target = os.path.realpath(os.path.join(root, str(iid)))
    if os.path.dirname(target) != root:
        raise ValueError("invalid instance id")
    if not os.path.isdir(target):
        return False
    _require_delete_permit(_permit, iid)
    shutil.rmtree(target)
    return True


def is_running(iid: str) -> bool:
    return container_runtime(iid)["running"]


def container_runtime(iid: str) -> dict:
    """Return running state and bridge address from one Docker inspect operation."""
    try:
        c = _client().containers.get(container_name(iid))
        running = c.status == "running"
        ip = None
        webrtc_host_port = None
        rtp_mapping_exact = False
        started_at_epoch = None
        started_at = ""
        restart_policy = ""
        engine_run_id = ""
        media_websocket = False
        browser_outbound = False
        browser_inbound = False
        labels = ((c.attrs.get("Config") or {}).get("Labels") or {})
        media_websocket = labels.get(
            ENGINE_MEDIA_WEBSOCKET_LABEL) == ENGINE_MEDIA_WEBSOCKET_ABI
        browser_outbound = labels.get(
            ENGINE_BROWSER_OUTBOUND_LABEL) == ENGINE_BROWSER_OUTBOUND_ABI
        browser_inbound = labels.get(
            ENGINE_BROWSER_INBOUND_LABEL) == ENGINE_BROWSER_INBOUND_ABI
        if running:
            started_at = str((c.attrs.get("State") or {}).get("StartedAt") or "")
            restart_policy = str((((c.attrs.get("HostConfig") or {}).get(
                "RestartPolicy") or {}).get("Name") or "no"))
            try:
                started_at_epoch = datetime.fromisoformat(
                    started_at.replace("Z", "+00:00")).timestamp()
            except (TypeError, ValueError):
                started_at_epoch = None
            for network in c.attrs.get("NetworkSettings", {}).get("Networks", {}).values():
                if network.get("IPAddress"):
                    ip = network["IPAddress"]
                    break
            bindings = (c.attrs.get("NetworkSettings", {}).get("Ports", {}) or {}).get(
                "8089/tcp") or []
            if bindings and str(bindings[0].get("HostPort") or "").isdigit():
                webrtc_host_port = int(bindings[0]["HostPort"])
            inst = cfg.get_instance(str(iid)) or {}
            ports_cfg = inst.get("ports") or {}
            rtp_start = ports_cfg.get("rtp_start")
            if type(rtp_start) is int and 1 <= rtp_start <= 65535:
                span = cfg.rtp_span(ports_cfg)
                published = c.attrs.get("NetworkSettings", {}).get("Ports", {}) or {}
                rtp_mapping_exact = bool(span and all(
                    any(str(binding.get("HostPort") or "") == str(port)
                        and str(binding.get("HostIp") or "") in {"", "0.0.0.0"}
                        for binding in published.get(f"{port}/udp") or [])
                    for port in range(rtp_start, rtp_start + span)))
            engine_run_id = read_engine_run_id(iid) or ""
        return {"running": running, "ip": ip, "container_id": getattr(c, "id", None),
                "container_status": str((c.attrs.get("State") or {}).get("Status") or ""),
                "webrtc_host_port": webrtc_host_port,
                "rtp_mapping_exact": rtp_mapping_exact,
                "started_at_epoch": started_at_epoch,
                "started_at": started_at,
                "restart_policy": restart_policy,
                "engine_run_id": engine_run_id,
                "media_websocket": media_websocket,
                "browser_outbound": browser_outbound,
                "browser_inbound": browser_inbound}
    except docker.errors.NotFound:
        return {"running": False, "ip": None, "container_id": None,
                "container_status": "missing",
                "webrtc_host_port": None, "rtp_mapping_exact": False,
                "started_at_epoch": None, "started_at": "",
                "restart_policy": "", "engine_run_id": "",
                "media_websocket": False, "browser_outbound": False,
                "browser_inbound": False}


def running_managed_engine_inventory() -> dict:
    """Inspect every running managed Engine, including retained or renamed containers."""
    script = (
        "import json\n"
        "cfg=json.load(open('/config/instance.json',encoding='utf-8'))\n"
        "env={}\n"
        "for item in open('/proc/1/environ','rb').read().split(b'\\0'):\n"
        "    if b'=' in item:\n"
        "        k,v=item.split(b'=',1); env[k.decode(errors='ignore')]=v.decode(errors='ignore')\n"
        "print(json.dumps({'iid':str(cfg.get('id','')),'run_id':env.get('MDD_ENGINE_RUN_ID','')}))\n"
    )
    try:
        containers = _client().containers.list(all=True)
        entries = []
        for container in containers:
            container.reload()
            attrs = container.attrs or {}
            labels = (attrs.get("Config") or {}).get("Labels") or {}
            state = attrs.get("State") or {}
            component = str(labels.get("io.mdd-sim-gateway.component") or "")
            is_engine = bool(_owned(container) and (
                component == "engine" or str(getattr(container, "name", "")).startswith(
                    "mdd-sim-gateway-engine-")))
            if not is_engine:
                continue
            status = str(state.get("Status") or "unknown").casefold()
            if status in {"exited", "dead"}:
                continue
            if status != "running":
                return {"ok": False,
                        "error": f"managed Engine state is not inspectable: {status}"}
            result = container.exec_run(
                ["python3", "-c", script], stdout=True, stderr=False)
            exit_code = getattr(result, "exit_code", None)
            output = getattr(result, "output", b"")
            if exit_code is None and isinstance(result, tuple) and len(result) == 2:
                exit_code, output = result
            if exit_code != 0:
                return {"ok": False, "error": "managed Engine identity exec failed"}
            if isinstance(output, bytes):
                output = output.decode("utf-8", errors="strict")
            value = json.loads(str(output).strip())
            iid = str(value.get("iid") or "")
            run_id = str(value.get("run_id") or "")
            if (not re.fullmatch(r"[A-Za-z0-9_.:-]{1,128}", iid)
                    or not _ENGINE_RUN_ID_RE.fullmatch(run_id)):
                return {"ok": False, "error": "managed Engine identity is invalid"}
            entries.append({
                "container_id": str(getattr(container, "id", "") or ""),
                "iid": iid, "engine_run_id": run_id,
            })
        return {"ok": True, "entries": entries}
    except Exception as exc:  # noqa
        return {"ok": False, "error": type(exc).__name__}


def media_websocket_runtime_ready(iid: str,
                                  expected_container_id: str | None = None) -> bool:
    """Prove that one running Engine can accept the per-call media WebSocket.

    The image label is only an admission contract; it does not prove that Asterisk loaded the
    two modules or that the run-scoped credential was rendered with private permissions.  This
    probe is used only by the bounded Engine replacement health gate, never by normal polling.
    It deliberately returns no command output because ``websocket_client.conf`` contains the
    derived per-run secret.
    """
    try:
        container = _client().containers.get(container_name(iid))
        container.reload()
        labels = ((container.attrs.get("Config") or {}).get("Labels") or {})
        if (str(container.id) != str(expected_container_id or container.id)
                or container.status != "running"
                or labels.get(ENGINE_MEDIA_WEBSOCKET_LABEL) !=
                ENGINE_MEDIA_WEBSOCKET_ABI):
            return False

        for module in ("chan_websocket.so", "res_websocket_client.so"):
            rc, raw = container.exec_run(
                ["asterisk", "-rx", f"module show like {module}"])
            output = raw.decode(errors="replace") if isinstance(raw, bytes) else str(raw)
            running = any(
                len(fields) >= 4 and fields[0] == module
                and fields[-2] == "Running" and fields[-3] != "Not"
                for fields in (line.split() for line in output.splitlines()))
            if rc != 0 or not running:
                return False

        for path, private in (
                ("/etc/asterisk/websocket_client.conf", True),
                ("/etc/asterisk/chan_websocket.conf", False)):
            rc, raw = container.exec_run(["stat", "-c", "%a %s", path])
            output = raw.decode(errors="replace") if isinstance(raw, bytes) else str(raw)
            match = re.fullmatch(r"([0-7]{3,4})\s+([1-9][0-9]*)\s*", output)
            if rc != 0 or not match or (private and match.group(1) != "600"):
                return False

        if labels.get(ENGINE_BROWSER_OUTBOUND_LABEL) == ENGINE_BROWSER_OUTBOUND_ABI:
            for module in ("func_groupcount.so", "func_strings.so"):
                rc, raw = container.exec_run(
                    ["asterisk", "-rx", f"module show like {module}"])
                output = raw.decode(errors="replace") if isinstance(raw, bytes) else str(raw)
                if (rc != 0 or not any(
                        len(fields) >= 4 and fields[0] == module
                        and fields[-2] == "Running" and fields[-3] != "Not"
                        for fields in (line.split() for line in output.splitlines()))):
                    return False
            expected = {
                "browser-media-outbound-warmup": (
                    "GROUP(mdd_line_call)", "GROUP_COUNT(active@mdd_line_call)",
                    "Echo()", "TIMEOUT(absolute)=10"),
                "browser-media-outbound": (
                    "MDD_NATIVE_CALL", "MDD_MEDIA_TOKEN", "MDD_MEDIA_EPOCH",
                    "MDD_OPERATION_ID", "MDD_DESTINATION", "Goto(from-local,"),
                "from-local": ("MDD_NATIVE_CALL", "native-required", "Dial(PJSIP/"),
                "volte_ims": ("GROUP(mdd_line_call)",
                              "GROUP_COUNT(active@mdd_line_call)", "line-busy"),
            }
            for context, needles in expected.items():
                rc, raw = container.exec_run(
                    ["asterisk", "-rx", f"dialplan show {context}"])
                output = raw.decode(errors="replace") if isinstance(raw, bytes) else str(raw)
                if rc != 0 or not all(needle in output for needle in needles):
                    return False

        if labels.get(ENGINE_BROWSER_INBOUND_LABEL) == ENGINE_BROWSER_INBOUND_ABI:
            rc, raw = container.exec_run(
                ["asterisk", "-rx", "module show like app_mdd_answer_bridged.so"])
            output = raw.decode(errors="replace") if isinstance(raw, bytes) else str(raw)
            if (rc != 0 or not any(
                    len(fields) >= 4 and fields[0] == "app_mdd_answer_bridged.so"
                    and fields[-2] == "Running" and fields[-3] != "Not"
                    for fields in (line.split() for line in output.splitlines()))):
                return False
            rc, raw = container.exec_run(
                ["asterisk", "-rx", "manager show command MddAnswerBridged"])
            output = raw.decode(errors="replace") if isinstance(raw, bytes) else str(raw)
            if rc != 0 or "MddAnswerBridged" not in output:
                return False
            inbound_contexts = {
                "browser-media-inbound-warmup": (
                    "MDD_ADMISSION(media_check)", "TIMEOUT(absolute)=10", "Echo()"),
                "browser-media-inbound-attach": (
                    "MDD_INBOUND_ATTACH", "MDD_INBOUND_SOURCE_ID",
                    "MDD_INBOUND_WINNER_CHANNEL",
                    "MDD_ADMISSION(media_check)", "TIMEOUT(absolute)=10",
                    "Bridge(${MDD_INBOUND_WINNER_CHANNEL},n)"),
                "mdd-inbound-result": ("notify.py call_result", "DIALSTATUS"),
                "volte_ims": ("hangup_handler_push", "TIMEOUT(absolute)=65", "Wait(60)"),
            }
            for context, needles in inbound_contexts.items():
                rc, raw = container.exec_run(
                    ["asterisk", "-rx", f"dialplan show {context}"])
                output = raw.decode(errors="replace") if isinstance(raw, bytes) else str(raw)
                if rc != 0 or not all(needle in output for needle in needles):
                    return False
                if context == "volte_ims" and "Dial(PJSIP/webrtc" in output:
                    return False

        container.reload()
        return (str(container.id) == str(expected_container_id or container.id)
                and container.status == "running")
    except Exception:
        return False


def container_ip(iid: str) -> str | None:
    return container_runtime(iid)["ip"]


def read_run_json(iid: str, name: str) -> dict | None:
    path = os.path.join(DATA_DIR, "instances", str(iid), "run", name)
    try:
        with open(path) as f:
            return json.load(f)
    except Exception:
        return None


def _registration_evidence_path(iid: str, generation: str, incarnation: str) -> str:
    digest = hashlib.sha256(
        f"{generation}\0{incarnation}".encode()).hexdigest()
    return os.path.join(
        DATA_DIR, "instances", str(iid), "run", f"registration_evidence.{digest}.json")


def read_registration_evidence(iid: str, generation: str, incarnation: str) -> dict | None:
    if not generation or not incarnation:
        return None
    try:
        with open(
                _registration_evidence_path(iid, generation, incarnation),
                encoding="utf-8") as handle:
            value = json.load(handle)
        return value if isinstance(value, dict) else None
    except (OSError, ValueError, TypeError):
        return None


def write_registration_evidence(iid: str, value: dict) -> bool:
    """Atomically retain ordered evidence for one exact Asterisk incarnation.

    Status polling and pushed status updates can finish out of order.  The lock and source
    timestamp comparison prevent an older failure sample from replacing a later Registered
    tombstone.  The filename also includes Docker's StartedAt incarnation because an
    ``unless-stopped`` restart keeps the same container id while replacing Asterisk PID 1.
    """
    generation = str(value.get("generation") or "")
    incarnation = str(value.get("incarnation") or "")
    observed_at = value.get("observed_at")
    if (not generation or not incarnation or type(observed_at) not in (int, float)
            or not math.isfinite(observed_at) or observed_at <= 0):
        raise ValueError(
            "registration evidence requires an Engine generation, incarnation, and timestamp")
    run_dir = os.path.join(DATA_DIR, "instances", str(iid), "run")
    os.makedirs(run_dir, exist_ok=True)
    path = _registration_evidence_path(iid, generation, incarnation)
    temporary = f"{path}.tmp.{os.getpid()}.{threading.get_ident()}"
    with _registration_evidence_lock:
        try:
            with open(path, encoding="utf-8") as handle:
                existing = json.load(handle)
        except (OSError, ValueError, TypeError):
            existing = None
        if isinstance(existing, dict):
            previous_at = existing.get("observed_at")
            if type(previous_at) in (int, float) and math.isfinite(previous_at):
                if observed_at < previous_at:
                    return False
                if (observed_at == previous_at
                        and existing.get("kind") == "registered"
                        and value.get("kind") != "registered"):
                    return False
        try:
            with open(temporary, "w", encoding="utf-8") as handle:
                json.dump(value, handle, separators=(",", ":"), sort_keys=True)
                handle.flush()
                os.fsync(handle.fileno())
            os.chmod(temporary, 0o600)
            os.replace(temporary, path)
            return True
        finally:
            try:
                os.unlink(temporary)
            except FileNotFoundError:
                pass


_PCSCF_REBIND_NAME = "pcscf-rebind.json"
_PCSCF_REBIND_LOCK = ".pcscf-rebind.lock"
_ENGINE_RUN_ID_RE = re.compile(r"^[A-Za-z0-9_.:-]{1,128}$")
_PCSCF_REBIND_PHASES = {"pending", "submitted", "cancel_requested", "abort_submitted"}
_PCSCF_REBIND_RETRY_LIMIT = 3
_PCSCF_REBIND_RETRY_BASE_SECONDS = 5.0
IMS_REGISTER_COMMAND = "pjsip send register volte_ims"
_USIM_RECOVERY_NAME = "usim-auth-recovery.json"
_USIM_RECOVERY_LOCK = ".usim-auth-recovery.lock"
_USIM_RECOVERY_FENCE_NAME = "usim-auth-recovery.fence"
_USIM_RECOVERY_CAUSES = frozenset({"pcsc_service_unavailable", "pcsc_card_reset"})
_USIM_RECOVERY_PHASES = {"pending", "submitted_unknown", "submitted", "exhausted"}
_USIM_RECOVERY_DELAYS = (1.0, 2.0, 4.0, 8.0, 16.0)


def _run_path(iid: str, name: str) -> str:
    return os.path.join(DATA_DIR, "instances", str(iid), "run", name)


class UsimRecoveryStateError(RuntimeError):
    """A durable local-auth recovery record exists but cannot be trusted."""


def usim_recovery_fence_pending(iid: str) -> bool:
    """Existence alone is the fail-closed Engine-side local-auth admission fence."""
    return os.path.lexists(_run_path(str(iid), _USIM_RECOVERY_FENCE_NAME))


def _read_usim_recovery_fence(iid: str) -> dict | None:
    path = _run_path(str(iid), _USIM_RECOVERY_FENCE_NAME)
    try:
        with open(path, encoding="utf-8") as handle:
            value = json.load(handle)
    except FileNotFoundError:
        return None
    except Exception as exc:
        raise UsimRecoveryStateError("unreadable USIM recovery fence") from exc
    if (not isinstance(value, dict) or set(value) != {
            "version", "engine_run_id", "auth_seq", "cause_class", "created_at"}
            or type(value.get("version")) is not int or value["version"] != 1
            or not _ENGINE_RUN_ID_RE.fullmatch(str(value.get("engine_run_id") or ""))
            or type(value.get("auth_seq")) is not int or value["auth_seq"] <= 0
            or value.get("cause_class") not in _USIM_RECOVERY_CAUSES
            or not isinstance(value.get("created_at"), (int, float))
            or isinstance(value.get("created_at"), bool)
            or not math.isfinite(float(value["created_at"]))
            or float(value["created_at"]) <= 0):
        raise UsimRecoveryStateError("invalid USIM recovery fence")
    return value


@contextmanager
def _usim_recovery_locked(iid: str):
    run_dir = os.path.dirname(_run_path(iid, _USIM_RECOVERY_LOCK))
    os.makedirs(run_dir, exist_ok=True)
    with open(_run_path(iid, _USIM_RECOVERY_LOCK), "a+", encoding="utf-8") as handle:
        os.chmod(handle.name, 0o600)
        fcntl.flock(handle.fileno(), fcntl.LOCK_EX)
        try:
            yield
        finally:
            fcntl.flock(handle.fileno(), fcntl.LOCK_UN)


def _usim_recovery_key(iid: str, container_id: str, started_at: str,
                       engine_run_id: str, auth_seq: int) -> dict:
    if (not re.fullmatch(r"[0-9a-f]{64}", str(container_id or ""))
            or not _STARTED_AT.fullmatch(str(started_at or ""))
            or not _ENGINE_RUN_ID_RE.fullmatch(str(engine_run_id or ""))
            or type(auth_seq) is not int or auth_seq <= 0):
        raise UsimRecoveryStateError("invalid USIM recovery identity")
    return {"instance": str(iid), "container_id": str(container_id),
            "started_at": str(started_at), "engine_run_id": str(engine_run_id),
            "auth_seq": auth_seq}


def _validate_usim_recovery(value: object, iid: str) -> dict:
    if not isinstance(value, dict) or set(value) != {
            "version", "instance", "container_id", "started_at", "engine_run_id",
            "auth_seq", "cause_class", "topology_digest", "phase", "attempts",
            "next_attempt_at", "updated_at", "submitted_at", "result_class"}:
        raise UsimRecoveryStateError("invalid USIM recovery schema")
    key = _usim_recovery_key(
        str(iid), value.get("container_id"), value.get("started_at"),
        value.get("engine_run_id"), value.get("auth_seq"))
    if (type(value.get("version")) is not int or value["version"] != 1
            or str(value.get("instance")) != str(iid)
            or value.get("cause_class") not in _USIM_RECOVERY_CAUSES
            or not _HEX64.fullmatch(str(value.get("topology_digest") or ""))
            or value.get("phase") not in _USIM_RECOVERY_PHASES
            or type(value.get("attempts")) is not int
            or not 0 <= value["attempts"] <= len(_USIM_RECOVERY_DELAYS)):
        raise UsimRecoveryStateError("invalid USIM recovery state")
    for field in ("next_attempt_at", "updated_at", "submitted_at"):
        number = value.get(field)
        if (not isinstance(number, (int, float)) or isinstance(number, bool)
                or not math.isfinite(float(number)) or float(number) < 0):
            raise UsimRecoveryStateError(f"invalid USIM recovery {field}")
    if value["phase"] in {"submitted_unknown", "submitted"} and value["submitted_at"] <= 0:
        raise UsimRecoveryStateError("submitted USIM recovery lacks timestamp")
    if not isinstance(value.get("result_class"), str):
        raise UsimRecoveryStateError("invalid USIM recovery result")
    return {**value, **key}


def _read_usim_recovery_unlocked(iid: str) -> dict | None:
    path = _run_path(str(iid), _USIM_RECOVERY_NAME)
    try:
        with open(path, encoding="utf-8") as handle:
            return _validate_usim_recovery(json.load(handle), str(iid))
    except FileNotFoundError:
        return None
    except UsimRecoveryStateError:
        raise
    except Exception as exc:
        raise UsimRecoveryStateError("unreadable USIM recovery record") from exc


def read_usim_recovery(iid: str) -> dict | None:
    with _usim_recovery_locked(str(iid)):
        return _read_usim_recovery_unlocked(str(iid))


def _current_usim_failure(iid: str, key: dict, cause_class: str) -> dict | None:
    value = read_run_json(str(iid), "usim_status.json")
    if (not isinstance(value, dict) or value.get("state") != "AUTH_UNAVAILABLE"
            or cause_class not in _USIM_RECOVERY_CAUSES
            or value.get("cause_class") != cause_class
            or value.get("engine_run_id") != key["engine_run_id"]
            or type(value.get("auth_seq")) is not int
            or value.get("auth_seq") != key["auth_seq"]):
        return None
    observed = value.get("ts")
    if (not isinstance(observed, (int, float)) or isinstance(observed, bool)
            or not math.isfinite(float(observed)) or float(observed) <= 0):
        return None
    return value


def reserve_usim_recovery_attempt(iid: str, *, container_id: str, started_at: str,
                                  engine_run_id: str, auth_seq: int,
                                  topology_digest: str, now: float | None = None) -> dict:
    """Persist the bounded probe cadence; passive call/AMI waits never exhaust recovery.

    ``attempts`` saturates at the last 1/2/4/8/16-second delay instead of becoming a retry
    budget. The exact AUTH_UNAVAILABLE observation is independently limited to 3700 seconds by
    the status layer, so automation remains time-bounded while a legitimate long call can finish.
    A successful submission still transitions durably to submitted_unknown and cannot replay.
    """
    iid = str(iid)
    key = _usim_recovery_key(iid, container_id, started_at, engine_run_id, auth_seq)
    if not _HEX64.fullmatch(str(topology_digest or "")):
        raise UsimRecoveryStateError("invalid USIM recovery topology")
    current_time = time.time() if now is None else float(now)
    if not math.isfinite(current_time) or current_time <= 0:
        raise UsimRecoveryStateError("invalid USIM recovery clock")
    with _usim_recovery_locked(iid):
        fence = _read_usim_recovery_fence(iid)
        if (fence is None or fence.get("engine_run_id") != engine_run_id
                or fence.get("auth_seq") != auth_seq):
            return {"status": "outage_epoch_changed"}
        cause_class = str(fence.get("cause_class") or "")
        if _current_usim_failure(iid, key, cause_class) is None:
            return {"status": "stale_failure"}
        record = _read_usim_recovery_unlocked(iid)
        same = record is not None and all(record.get(k) == v for k, v in key.items())
        if record is not None and not same:
            if (record.get("engine_run_id") == engine_run_id
                    and int(record.get("auth_seq") or 0) >= auth_seq):
                return {"status": "newer_or_invalid_owner"}
            record = None
        if record is not None and record.get("cause_class") != cause_class:
            return {"status": "cause_changed", "record": record}
        if record is None:
            record = {"version": 1, **key, "cause_class": cause_class,
                      "topology_digest": topology_digest, "phase": "pending",
                      "attempts": 0, "next_attempt_at": current_time + _USIM_RECOVERY_DELAYS[0],
                      "updated_at": current_time, "submitted_at": 0.0, "result_class": ""}
            _atomic_json(_run_path(iid, _USIM_RECOVERY_NAME), record)
            return {"status": "waiting", "record": record}
        if record["phase"] != "pending":
            return {"status": record["phase"], "record": record}
        if record["topology_digest"] != topology_digest:
            return {"status": "topology_changed", "record": record}
        if current_time < float(record["next_attempt_at"]):
            return {"status": "waiting", "record": record}
        attempt = min(record["attempts"] + 1, len(_USIM_RECOVERY_DELAYS))
        next_at = current_time + _USIM_RECOVERY_DELAYS[
            min(attempt, len(_USIM_RECOVERY_DELAYS) - 1)]
        record.update(attempts=attempt, next_attempt_at=next_at, updated_at=current_time)
        _atomic_json(_run_path(iid, _USIM_RECOVERY_NAME), record)
        return {"status": "reserved", "attempt": attempt, "record": record}


@contextmanager
def _pcscf_rebind_locked(iid: str):
    run_dir = os.path.dirname(_run_path(iid, _PCSCF_REBIND_LOCK))
    os.makedirs(run_dir, exist_ok=True)
    with open(_run_path(iid, _PCSCF_REBIND_LOCK), "a+", encoding="utf-8") as handle:
        os.chmod(handle.name, 0o600)
        fcntl.flock(handle.fileno(), fcntl.LOCK_EX)
        try:
            yield
        finally:
            fcntl.flock(handle.fileno(), fcntl.LOCK_UN)


def _validate_pcscf_rebind(value: object, iid: str) -> dict | None:
    if (not isinstance(value, dict) or type(value.get("version")) is not int
            or value.get("version") != 1):
        return None
    instance = str(value.get("instance") or "")
    run_id = str(value.get("engine_run_id") or "")
    phase = str(value.get("phase") or "")
    observed_at = value.get("observed_at")
    raw_applied = str(value.get("applied") or "").strip()
    try:
        applied = str(ipaddress.ip_address(raw_applied)) if raw_applied else ""
        desired = str(ipaddress.ip_address(str(value.get("desired") or "")))
    except ValueError:
        return None
    if (instance != str(iid) or not _ENGINE_RUN_ID_RE.fullmatch(run_id)
            or phase not in _PCSCF_REBIND_PHASES
            or not isinstance(observed_at, (int, float)) or isinstance(observed_at, bool)
            or not math.isfinite(float(observed_at)) or float(observed_at) <= 0
            or type(value.get("shutdown_reserved")) is not bool):
        return None
    result = dict(value)
    result.update({"instance": instance, "engine_run_id": run_id, "phase": phase,
                   "applied": applied, "desired": desired,
                   "observed_at": float(observed_at)})
    return result


def _read_pcscf_rebind_unlocked(iid: str) -> dict | None:
    try:
        with open(_run_path(iid, _PCSCF_REBIND_NAME), encoding="utf-8") as handle:
            return _validate_pcscf_rebind(json.load(handle), str(iid))
    except (OSError, ValueError, TypeError):
        return None


def _write_pcscf_rebind_unlocked(iid: str, value: dict) -> None:
    path = _run_path(iid, _PCSCF_REBIND_NAME)
    temporary = f"{path}.tmp.control.{os.getpid()}.{threading.get_ident()}"
    try:
        with open(temporary, "w", encoding="utf-8") as handle:
            json.dump(value, handle, ensure_ascii=True, sort_keys=True)
            handle.write("\n")
            handle.flush()
            os.fsync(handle.fileno())
        os.chmod(temporary, 0o600)
        os.replace(temporary, path)
    finally:
        try:
            os.unlink(temporary)
        except FileNotFoundError:
            pass


def read_engine_run_id(iid: str) -> str | None:
    try:
        with open(_run_path(iid, "engine-run-id"), encoding="utf-8") as handle:
            value = handle.read(256).strip()
        return value if _ENGINE_RUN_ID_RE.fullmatch(value) else None
    except OSError:
        return None


def read_pcscf_rebind(iid: str) -> dict | None:
    """Read a strict durable marker; any valid owner remains an admission fence.

    A marker from the immediately previous Engine run deliberately survives an
    ``unless-stopped`` restart until the new entrypoint has freshly rendered its P-CSCF.
    Therefore callers gate on validity, while mutation helpers additionally require the exact
    current container id, StartedAt and Engine run id.
    """
    with _pcscf_rebind_locked(str(iid)):
        return _read_pcscf_rebind_unlocked(str(iid))


def pcscf_rebind_pending(iid: str) -> bool:
    # A corrupt/partial marker cannot authorize new paid work.  Strict parsing controls whether
    # Control may mutate the Engine; file presence itself is the fail-closed admission fence and
    # the next successful entrypoint generation will clear it after rendering.
    return os.path.exists(_run_path(str(iid), _PCSCF_REBIND_NAME))


def _acquire_pcscf_flock(iid: str):
    """Acquire only the shared transition flock; callers must validate their own fences."""
    run_dir = os.path.dirname(_run_path(str(iid), _PCSCF_REBIND_LOCK))
    os.makedirs(run_dir, exist_ok=True)
    handle = open(_run_path(str(iid), _PCSCF_REBIND_LOCK), "a+", encoding="utf-8")
    try:
        os.chmod(handle.name, 0o600)
        try:
            fcntl.flock(handle.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            handle.close()
            return None
        return handle
    except Exception:
        handle.close()
        raise


def acquire_pcscf_admission(iid: str):
    """Acquire the cross-process no-marker boundary for one immediate submission.

    The returned file object keeps ``flock`` held across the caller's AMI/WebSocket submit.
    SWu cannot publish a new marker until release, so the operation is totally ordered either
    before the transition (allowed once) or after it (rejected).  Acquisition is deliberately
    non-blocking: lock contention means a transition or another admitted submission may be in
    flight, so new work fails closed instead of leaving an uncancellable worker waiting on flock.
    ``None`` means fenced or busy.
    """
    handle = _acquire_pcscf_flock(str(iid))
    if handle is None:
        return None
    try:
        if (os.path.lexists(_run_path(str(iid), _PCSCF_REBIND_NAME))
                or usim_recovery_fence_pending(str(iid))):
            fcntl.flock(handle.fileno(), fcntl.LOCK_UN)
            handle.close()
            return None
        return handle
    except Exception:
        handle.close()
        raise


def _acquire_usim_recovery_admission(iid: str):
    """Acquire the shared transition flock while requiring the recovery fence to exist.

    Ordinary submissions reject this fence in ``acquire_pcscf_admission``. Only the bounded
    recovery worker may bypass it, and its strict owner/auth sequence is revalidated before the
    durable REGISTER linearization point.
    """
    handle = _acquire_pcscf_flock(str(iid))
    if handle is None:
        return None
    try:
        if (os.path.lexists(_run_path(str(iid), _PCSCF_REBIND_NAME))
                or not usim_recovery_fence_pending(str(iid))):
            fcntl.flock(handle.fileno(), fcntl.LOCK_UN)
            handle.close()
            return None
        return handle
    except Exception:
        handle.close()
        raise


def release_pcscf_admission(handle) -> None:
    if handle is None:
        return
    try:
        fcntl.flock(handle.fileno(), fcntl.LOCK_UN)
    finally:
        handle.close()


def _pcscf_retry_delay(rejections: int) -> float:
    """Small bounded backoff for a command that Asterisk explicitly rejected."""
    count = max(1, min(int(rejections), _PCSCF_REBIND_RETRY_LIMIT))
    return _PCSCF_REBIND_RETRY_BASE_SECONDS * (2 ** (count - 1))


def _pcscf_retry_status(marker: dict, prefix: str, now: float) -> dict | None:
    """Return an admission-safe wait/exhausted result for one command family."""
    count = marker.get(f"{prefix}_rejections", 0)
    if type(count) is not int or count < 0:
        # Optional retry metadata is control-owned.  Corruption cannot authorize another command.
        return {"status": f"{prefix}_retry_state_invalid"}
    if count >= _PCSCF_REBIND_RETRY_LIMIT:
        return {"status": f"{prefix}_retry_exhausted", "rejections": count,
                "manual_required": True}
    next_at = marker.get(f"next_{prefix}_at", 0)
    if type(next_at) not in (int, float) or isinstance(next_at, bool) \
            or not math.isfinite(float(next_at)) or float(next_at) < 0:
        return {"status": f"{prefix}_retry_state_invalid"}
    if float(next_at) > now:
        return {"status": f"{prefix}_retry_wait", "rejections": count,
                "retry_after": max(0.0, float(next_at) - now)}
    return None


def _exact_pcscf_container(container, expected_container_id: str,
                           expected_started_at: str, expected_run_id: str,
                           iid: str) -> str | None:
    container.reload()
    if str(getattr(container, "id", "")) != str(expected_container_id):
        return "generation_changed"
    if str(getattr(container, "status", "")) != "running":
        return "not_running"
    attrs = container.attrs or {}
    if str((attrs.get("State") or {}).get("StartedAt") or "") != str(expected_started_at):
        return "incarnation_changed"
    policy = str((((attrs.get("HostConfig") or {}).get("RestartPolicy") or {}).get(
        "Name") or "no"))
    if policy != "unless-stopped":
        return "restart_policy_not_managed"
    if read_engine_run_id(str(iid)) != str(expected_run_id):
        return "run_changed"
    return None


def request_pcscf_rebind(iid: str, expected_container_id: str,
                          expected_started_at: str, expected_run_id: str) -> dict:
    """Reserve and submit exactly one graceful stop for an exact Engine incarnation.

    Reservation is durable *before* the Docker exec.  A Control crash or lost response can
    therefore reduce liveness (the UI remains safely fenced) but can never issue an unbounded
    or duplicate shutdown command.  There is intentionally no reload/kill fallback.
    """
    client = None
    try:
        client = docker.from_env(timeout=5)
        container = client.containers.get(container_name(str(iid)))
        if not _owned(container):
            return {"status": "foreign"}
        mismatch = _exact_pcscf_container(
            container, expected_container_id, expected_started_at, expected_run_id, str(iid))
        if mismatch:
            return {"status": mismatch}
        # Avoid probing Asterisk forever after a terminal retry result.  Repeat this eligibility
        # check after the readiness probe because SWu may coalesce/cancel the marker meanwhile.
        with _pcscf_rebind_locked(str(iid)):
            marker = _read_pcscf_rebind_unlocked(str(iid))
            if not marker or marker.get("engine_run_id") != expected_run_id:
                return {"status": "marker_changed"}
            if marker.get("phase") == "cancel_requested":
                return {"status": "cancel_requested"}
            if marker.get("shutdown_reserved"):
                return {"status": "already_submitted"}
            retry_status = _pcscf_retry_status(marker, "submit", time.time())
            if retry_status:
                return retry_status
        try:
            rc, _raw = container.exec_run(
                ["asterisk", "-rx", "core waitfullybooted"])
        except Exception:
            return {"status": "asterisk_not_ready"}
        if rc != 0:
            return {"status": "asterisk_not_ready"}
        mismatch = _exact_pcscf_container(
            container, expected_container_id, expected_started_at, expected_run_id, str(iid))
        if mismatch:
            return {"status": mismatch}

        with _pcscf_rebind_locked(str(iid)):
            marker = _read_pcscf_rebind_unlocked(str(iid))
            if not marker or marker.get("engine_run_id") != expected_run_id:
                return {"status": "marker_changed"}
            if marker.get("phase") == "cancel_requested":
                return {"status": "cancel_requested"}
            if marker.get("shutdown_reserved"):
                return {"status": "already_submitted"}
            now = time.time()
            retry_status = _pcscf_retry_status(marker, "submit", now)
            if retry_status:
                return retry_status
            mismatch = _exact_pcscf_container(
                container, expected_container_id, expected_started_at,
                expected_run_id, str(iid))
            if mismatch:
                return {"status": mismatch}
            marker.update({"shutdown_reserved": True, "phase": "submitted",
                           "shutdown_reserved_at": now,
                           "submit_result": "reserved"})
            _write_pcscf_rebind_unlocked(str(iid), marker)

        # Reinspect after releasing the cross-process marker lock.  If PID 1 changed in the
        # small handoff window, leave the old-run marker fenced for the new entrypoint to clear.
        mismatch = _exact_pcscf_container(
            container, expected_container_id, expected_started_at, expected_run_id, str(iid))
        if mismatch:
            return {"status": mismatch, "reserved": True}
        try:
            rc, _raw = container.exec_run(["asterisk", "-rx", "core stop gracefully"])
            submit_result = "accepted" if rc == 0 else "rejected"
        except Exception as exc:  # response may be lost after Asterisk accepted the command
            submit_result = "unknown"
            log.warning("P-CSCF graceful rebind submission uncertain for line %s: %s", iid, exc)
        with _pcscf_rebind_locked(str(iid)):
            current = _read_pcscf_rebind_unlocked(str(iid))
            if (current and current.get("engine_run_id") == expected_run_id
                    and current.get("shutdown_reserved")):
                if submit_result != "rejected":
                    # Accepted and response-lost commands remain reserved: replay could terminate
                    # a replacement generation or duplicate a command Asterisk already accepted.
                    current["submit_result"] = submit_result
                    _write_pcscf_rebind_unlocked(str(iid), current)
                else:
                    mismatch = _exact_pcscf_container(
                        container, expected_container_id, expected_started_at,
                        expected_run_id, str(iid))
                    if mismatch:
                        return {"status": f"submission_rejected_{mismatch}",
                                "reserved": True}
                    count = int(current.get("submit_rejections") or 0) + 1
                    current["submit_rejections"] = count
                    current["submit_result"] = "rejected"
                    current["shutdown_reserved"] = False
                    current.pop("shutdown_reserved_at", None)
                    if current.get("phase") != "cancel_requested":
                        current["phase"] = "pending"
                    if count >= _PCSCF_REBIND_RETRY_LIMIT:
                        current["manual_required"] = "graceful_stop_rejected"
                        current.pop("next_submit_at", None)
                    else:
                        current.pop("manual_required", None)
                        current["next_submit_at"] = time.time() + _pcscf_retry_delay(count)
                    _write_pcscf_rebind_unlocked(str(iid), current)
                    return {"status": ("submit_retry_exhausted"
                                       if count >= _PCSCF_REBIND_RETRY_LIMIT
                                       else "submit_rejected_retrying"),
                            "rejections": count,
                            "manual_required": count >= _PCSCF_REBIND_RETRY_LIMIT,
                            "reserved": False}
        return {"status": "submitted" if submit_result == "accepted" else
                f"submission_{submit_result}", "reserved": True}
    except docker.errors.NotFound:
        return {"status": "missing"}
    except Exception as exc:
        return {"status": "error", "error": str(exc)}
    finally:
        if client is not None:
            try:
                client.close()
            except Exception:
                pass


def cancel_pcscf_rebind(iid: str, expected_container_id: str,
                         expected_started_at: str, expected_run_id: str) -> dict:
    """Cancel a return-to-applied request, aborting one reserved graceful stop at most once."""
    client = None
    try:
        client = docker.from_env(timeout=5)
        container = client.containers.get(container_name(str(iid)))
        if not _owned(container):
            return {"status": "foreign"}
        mismatch = _exact_pcscf_container(
            container, expected_container_id, expected_started_at, expected_run_id, str(iid))
        if mismatch:
            return {"status": mismatch}
        with _pcscf_rebind_locked(str(iid)):
            marker = _read_pcscf_rebind_unlocked(str(iid))
            if (not marker or marker.get("engine_run_id") != expected_run_id
                    or marker.get("phase") != "cancel_requested"):
                return {"status": "marker_changed"}
            if not marker.get("shutdown_reserved"):
                os.unlink(_run_path(str(iid), _PCSCF_REBIND_NAME))
                return {"status": "cancelled"}
            if marker.get("abort_reserved_at"):
                return {"status": "abort_already_submitted"}
            now = time.time()
            retry_status = _pcscf_retry_status(marker, "abort", now)
            if retry_status:
                return retry_status
            mismatch = _exact_pcscf_container(
                container, expected_container_id, expected_started_at,
                expected_run_id, str(iid))
            if mismatch:
                return {"status": mismatch}
            marker.update({"phase": "abort_submitted", "abort_reserved_at": now})
            _write_pcscf_rebind_unlocked(str(iid), marker)
        mismatch = _exact_pcscf_container(
            container, expected_container_id, expected_started_at, expected_run_id, str(iid))
        if mismatch:
            return {"status": mismatch, "abort_reserved": True}
        try:
            rc, _raw = container.exec_run(["asterisk", "-rx", "core abort shutdown"])
        except Exception as exc:
            log.warning("P-CSCF graceful rebind abort uncertain for line %s: %s", iid, exc)
            return {"status": "abort_unknown", "abort_reserved": True}
        if rc != 0:
            with _pcscf_rebind_locked(str(iid)):
                current = _read_pcscf_rebind_unlocked(str(iid))
                if current and current.get("engine_run_id") == expected_run_id:
                    mismatch = _exact_pcscf_container(
                        container, expected_container_id, expected_started_at,
                        expected_run_id, str(iid))
                    if mismatch:
                        return {"status": f"abort_rejected_{mismatch}",
                                "abort_reserved": True}
                    # If discovery changed away from the applied address during the abort, the
                    # original graceful stop is useful again.  Do not schedule another abort.
                    if current.get("desired") != current.get("applied"):
                        current.pop("abort_reserved_at", None)
                        current["phase"] = "submitted"
                        _write_pcscf_rebind_unlocked(str(iid), current)
                        return {"status": "abort_rejected_target_changed",
                                "abort_reserved": False}
                    count = int(current.get("abort_rejections") or 0) + 1
                    current["abort_rejections"] = count
                    current["phase"] = "cancel_requested"
                    current.pop("abort_reserved_at", None)
                    if count >= _PCSCF_REBIND_RETRY_LIMIT:
                        current["manual_required"] = "abort_shutdown_rejected"
                        current.pop("next_abort_at", None)
                    else:
                        current.pop("manual_required", None)
                        current["next_abort_at"] = time.time() + _pcscf_retry_delay(count)
                    _write_pcscf_rebind_unlocked(str(iid), current)
                    return {"status": ("abort_retry_exhausted"
                                       if count >= _PCSCF_REBIND_RETRY_LIMIT
                                       else "abort_rejected_retrying"),
                            "rejections": count,
                            "manual_required": count >= _PCSCF_REBIND_RETRY_LIMIT,
                            "abort_reserved": False}
            return {"status": "abort_rejected_owner_changed", "abort_reserved": True}
        with _pcscf_rebind_locked(str(iid)):
            current = _read_pcscf_rebind_unlocked(str(iid))
            if current and current.get("engine_run_id") == expected_run_id:
                mismatch = _exact_pcscf_container(
                    container, expected_container_id, expected_started_at,
                    expected_run_id, str(iid))
                if mismatch:
                    # The successful CLI response belongs to the old process.  Never release
                    # the durable fence across a same-container restart; fresh SWu discovery in
                    # the replacement generation owns that transition.
                    return {"status": f"aborted_{mismatch}", "abort_reserved": True}
                if current.get("desired") == current.get("applied"):
                    os.unlink(_run_path(str(iid), _PCSCF_REBIND_NAME))
                else:
                    # The target changed away again while abort was in flight.  It is a new,
                    # bounded transaction and may reserve one fresh graceful stop.
                    for key in ("shutdown_reserved_at", "submit_result", "abort_reserved_at",
                                "submit_rejections", "next_submit_at", "abort_rejections",
                                "next_abort_at", "manual_required"):
                        current.pop(key, None)
                    current.update({"phase": "pending", "shutdown_reserved": False})
                    _write_pcscf_rebind_unlocked(str(iid), current)
        return {"status": "aborted"}
    except (docker.errors.NotFound, FileNotFoundError):
        return {"status": "missing"}
    except Exception as exc:
        return {"status": "error", "error": str(exc)}
    finally:
        if client is not None:
            try:
                client.close()
            except Exception:
                pass


def read_pcscf(iid: str) -> str | None:
    path = os.path.join(DATA_DIR, "instances", str(iid), "run", "pcscf")
    try:
        with open(path) as f:
            v = f.read().strip()
            return v or None
    except Exception:
        return None


def tunnel_installed(iid: str) -> bool:
    """True if the ims tunnel is up: the swu_ike daemon writes run/swu_status.json
    {state: CONNECTED} once the SWu (ePDG) IPsec tunnel is established."""
    st = read_run_json(iid, "swu_status.json")
    return st is not None and st.get("state") == "CONNECTED"


def usim_recovery_transport_ready(iid: str, expected_run_id: str) -> bool:
    """Fail closed unless the current SWu/P-CSCF generation can accept one REGISTER.

    The caller already owns ``.pcscf-rebind.lock``. This helper deliberately performs only
    lock-free reads so recovery cannot recursively acquire the same process flock.
    """
    run_id = str(expected_run_id or "")
    if not _ENGINE_RUN_ID_RE.fullmatch(run_id) or pcscf_rebind_pending(str(iid)):
        return False
    swu = read_run_json(str(iid), "swu_status.json")
    discovery = read_run_json(str(iid), "pcscf-discovery.json")
    if (not isinstance(swu, dict) or swu.get("state") != "CONNECTED"
            or not isinstance(discovery, dict)
            or str(discovery.get("engine_run_id") or "") != run_id):
        return False
    swu_observed = swu.get("ts")
    discovered_at = discovery.get("observed_at")
    if any(not isinstance(value, (int, float)) or isinstance(value, bool)
           or not math.isfinite(float(value)) or float(value) <= 0
           for value in (swu_observed, discovered_at)):
        return False
    try:
        discovered = str(ipaddress.ip_address(str(discovery.get("address") or "")))
    except ValueError:
        return False
    current = read_pcscf(str(iid))
    try:
        with open(_run_path(str(iid), "pcscf.applied"), encoding="utf-8") as handle:
            applied = handle.read(256).strip()
        current = str(ipaddress.ip_address(str(current or "")))
        applied = str(ipaddress.ip_address(applied))
    except (OSError, ValueError):
        return False
    return current == discovered == applied


def exec_cli(iid: str, command: str) -> str:
    client = None
    try:
        # Bound the Docker HTTP exchange.  Some callers hold a short cross-process admission lock
        # across this exact submission point; an unbounded daemon/CLI response would prevent SWu
        # from publishing a route transition.
        client = docker.from_env(timeout=5)
        c = client.containers.get(container_name(iid))
        rc, out = c.exec_run(["asterisk", "-rx", command])
        return out.decode(errors="replace") if isinstance(out, bytes) else str(out)
    except Exception as e:  # noqa
        return f"error: {e}"
    finally:
        if client is not None:
            try:
                client.close()
            except Exception:
                pass


def exec_cli_with_pcscf_admission(iid: str, command: str, *, before_exec=None) -> dict:
    """Own the admission flock and bounded CLI exchange in one synchronous worker.

    Async request cancellation cannot stop a Python executor thread.  Keeping both acquisition
    and release here prevents even repeated cancellation from releasing the cross-process fence
    while that worker can still submit REGISTER to the old Asterisk generation.
    """
    handle = acquire_pcscf_admission(str(iid))
    if handle is None:
        return {"admitted": False, "submitted": False, "output": ""}
    try:
        if before_exec is not None and before_exec() is not True:
            return {"admitted": True, "submitted": False, "output": ""}
        return {"admitted": True, "submitted": True,
                "output": exec_cli(str(iid), command)}
    finally:
        release_pcscf_admission(handle)


def submit_usim_recovery_register(iid: str, *, container_id: str, started_at: str,
                                  engine_run_id: str, auth_seq: int, attempt: int,
                                  topology_digest: str, zero_channels, before_exec) -> dict:
    """Submit one exact local-PC/SC recovery REGISTER with durable at-most-once ordering.

    One worker owns the P-CSCF flock across the complete zero-channel snapshot, exact identity
    recheck, ``submitted_unknown`` commit and CLI exchange. Once that record exists, neither a
    Control restart nor an ambiguous Docker response may resubmit this key. ``zero_channels``
    and ``before_exec`` are bounded synchronous callbacks owned by Control.
    """
    iid = str(iid)
    key = _usim_recovery_key(iid, container_id, started_at, engine_run_id, auth_seq)
    if (type(attempt) is not int or not 1 <= attempt <= len(_USIM_RECOVERY_DELAYS)
            or not _HEX64.fullmatch(str(topology_digest or ""))
            or not callable(zero_channels) or not callable(before_exec)):
        raise UsimRecoveryStateError("invalid USIM recovery submission")
    with _usim_recovery_locked(iid):
        record = _read_usim_recovery_unlocked(iid)
        if record is None:
            return {"status": "missing_recovery", "submitted": False}
        if not all(record.get(k) == v for k, v in key.items()):
            return {"status": "stale_recovery", "submitted": False}
        if record["phase"] != "pending":
            return {"status": record["phase"], "submitted": False}
        if (record["attempts"] != attempt
                or record["topology_digest"] != topology_digest):
            return {"status": "reservation_changed", "submitted": False}

        admission = _acquire_usim_recovery_admission(iid)
        if admission is None:
            return {"status": "admission_denied", "submitted": False}
        committed = False

        def commit_unknown():
            nonlocal committed, record
            runtime = container_runtime(iid)
            try:
                fence = _read_usim_recovery_fence(iid)
            except UsimRecoveryStateError:
                fence = None
            if (not runtime.get("running")
                    or runtime.get("container_id") != container_id
                    or runtime.get("started_at") != started_at
                    or runtime.get("engine_run_id") != engine_run_id
                    or fence is None
                    or fence.get("engine_run_id") != engine_run_id
                    or fence.get("auth_seq") != auth_seq
                    or fence.get("cause_class") != record["cause_class"]
                    or _current_usim_failure(iid, key, record["cause_class"]) is None
                    or global_maintenance_pending()
                    or engine_maintenance_pending(iid)
                    or os.path.lexists(_run_path(iid, _PCSCF_REBIND_NAME))
                    or os.path.lexists(_run_path(iid, "admission-deny"))
                    or before_exec() is not True):
                return False
            now = time.time()
            record = {**record, "phase": "submitted_unknown", "submitted_at": now,
                      "updated_at": now, "next_attempt_at": 0.0,
                      "result_class": "submission_outcome_unknown"}
            _atomic_json(_run_path(iid, _USIM_RECOVERY_NAME), record)
            committed = True
            return True

        try:
            try:
                idle = zero_channels() is True
            except Exception:
                idle = False
            if not idle:
                return {"status": "channel_state_unknown", "submitted": False}
            if commit_unknown() is not True:
                return {"status": "pre_submit_rejected", "submitted": False}
            # A Docker/CLI error after the linearization point is ambiguous: the command may have
            # reached Asterisk. Preserve submitted_unknown and never replay it.
            output = str(exec_cli(iid, IMS_REGISTER_COMMAND) or "")
            if committed and not output.casefold().startswith("error:"):
                now = time.time()
                record = {**record, "phase": "submitted", "updated_at": now,
                          "result_class": "cli_returned"}
                _atomic_json(_run_path(iid, _USIM_RECOVERY_NAME), record)
                return {"status": "submitted", "submitted": True, "output": output}
            return {"status": "submitted_unknown", "submitted": bool(committed),
                    "output": output}
        finally:
            release_pcscf_admission(admission)


def registration_state(iid: str) -> str:
    """Read IMS registration through the local Asterisk CLI.

    Some IMS-patched Asterisk builds accept AMI's PJSIPShowRegistrationsDetailed action but
    never complete it. Treating that management timeout as a carrier failure eventually stops
    an otherwise healthy line. The CLI is the authoritative local view and has proven reliable
    on those same builds.
    """
    client = None
    try:
        # Use a short-lived client with an HTTP read timeout. Asterisk's remote CLI can block
        # behind an IMS TCP connect; the normal shared helper intentionally has no global Docker
        # timeout, so using it here would leave one worker thread behind on every status poll.
        client = docker.from_env(timeout=5)
        container = client.containers.get(container_name(iid))
        rc, raw = container.exec_run(["asterisk", "-rx", "pjsip show registrations"])
        output = raw.decode(errors="replace") if isinstance(raw, bytes) else str(raw)
    except Exception:
        return "unknown"
    finally:
        if client is not None:
            try:
                client.close()
            except Exception:
                pass
    if re.search(r"\bRejected\b", output, re.I):
        return "Rejected"
    if re.search(r"\bUnregistered\b", output, re.I):
        return "Unregistered"
    if re.search(r"\bRegistered\b", output):
        return "Registered"
    return "unknown"


def active_channel_count(iid: str) -> int | None:
    """Return Asterisk's live channel count through a bounded local CLI query.

    This is the fail-closed fallback used immediately before automatic recovery removes an
    Engine.  A missing container, a wedged CLI or unparsable output is deliberately ``None``:
    none of those observations proves that a potentially billable call has ended.
    """
    client = None
    try:
        client = docker.from_env(timeout=5)
        container = client.containers.get(container_name(iid))
        _rc, raw = container.exec_run(["asterisk", "-rx", "core show channels count"])
        output = raw.decode(errors="replace") if isinstance(raw, bytes) else str(raw)
        match = re.search(r"\b(\d+)\s+active channels?\b", output, re.I)
        return int(match.group(1)) if match else None
    except Exception:
        return None
    finally:
        if client is not None:
            try:
                client.close()
            except Exception:
                pass


def _format_docker_logs(raw: str, local_tz=None) -> str:
    """Render Docker's per-record UTC time in the same local format as the IKE log."""
    rendered = []
    for line in raw.splitlines(keepends=True):
        content = line.rstrip("\r\n")
        ending = line[len(content):]
        match = _DOCKER_TIMESTAMP.match(content)
        if not match:
            rendered.append(line)
            continue
        zone = "+00:00" if match.group(2) == "Z" else match.group(2)
        event_time = datetime.fromisoformat(match.group(1) + zone)
        event_time = event_time.astimezone(local_tz) if local_tz else event_time.astimezone()
        message = _ASTERISK_TIMESTAMP.sub("", match.group(3), count=1)
        rendered.append("[%s] %s%s" % (
            event_time.strftime("%Y-%m-%d %H:%M:%S%z"), message, ending))
    return "".join(rendered)


def logs(iid: str, tail: int = 200, since=None) -> str:
    try:
        c = _client().containers.get(container_name(iid))
        # Docker records the emission time for every physical stdout/stderr line. Request that
        # source timestamp rather than stamping at page-refresh time, then normalize it to the
        # same local display format as charon.log.
        kwargs = {"tail": tail, "timestamps": True}
        if since is not None:
            # docker SDK accepts an int (unix ts) or datetime; used by the SMS delivery
            # watcher to read only the lines emitted after a send.
            kwargs["since"] = since
        raw = c.logs(**kwargs).decode(errors="replace")
        return _format_docker_logs(raw)
    except Exception as e:  # noqa
        return f"error: {e}"


def charon_log(iid: str, tail: int = 200) -> str:
    """Recent SWu tunnel (IKE) log lines from the instance run dir. The file is named
    charon.log for control-plane/WebUI compatibility (the log-view key is 'charon')."""
    path = os.path.join(DATA_DIR, "instances", str(iid), "run", "charon.log")
    try:
        with open(path, errors="replace") as f:
            return "".join(f.readlines()[-tail:])
    except Exception:
        return ""


def usim_status(iid: str) -> dict:
    return read_run_json(iid, "usim_status.json") or {}
