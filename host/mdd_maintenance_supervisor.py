#!/usr/bin/env python3
"""Host authority for maintenance-proxy full-mode authorization.

The proxy can count and revoke its own requests, but it cannot prove which Docker processes or
host sockets it is forwarding to.  This root-only supervisor supplies that missing authority.
It never grants from polling: a local transaction owner must send one explicit ``recover`` over
the Unix socket, after which every grant and lease renewal is tied to exact host evidence.
"""
from __future__ import annotations

from dataclasses import dataclass
import argparse
import contextlib
import fcntl
import http.client
import ipaddress
import json
import os
from pathlib import Path
import re
import select
import socket
import ssl
import subprocess
import threading
import time
import uuid

try:  # package import in tests; direct script import on the installed host
    from .mdd_maintenance_proxy import (
        ProxyStateError, _BOOT_ID, _TXID, _atomic_json, _digest, _image_id,
        host_boot_id, manifest_locked, read_manifest,
    )
    from .mdd_upgrade_guard import UpgradeGuardError, pending_paid_work
except ImportError:  # pragma: no cover - exercised by direct host entrypoint
    from mdd_maintenance_proxy import (
        ProxyStateError, _BOOT_ID, _TXID, _atomic_json, _digest, _image_id,
        host_boot_id, manifest_locked, read_manifest,
    )
    from mdd_upgrade_guard import UpgradeGuardError, pending_paid_work


class SupervisorError(RuntimeError):
    """A fail-closed proof or lifecycle failure with a stable diagnostic code."""

    def __init__(self, code: str):
        super().__init__(code)
        self.code = code if re.fullmatch(r"[a-z0-9_]{3,64}", code) else "proof_failed"


class FenceReleasePending(SupervisorError):
    """Full is proven; a durable, idempotent admission-fence release remains."""


def _deadline_remaining(deadline: float, code: str) -> float:
    remaining = deadline - time.monotonic()
    if remaining <= 0:
        raise SupervisorError(code)
    return remaining


@contextlib.contextmanager
def _bounded_flock(path: Path, timeout: float, code: str, *,
                   deadline: float | None = None):
    """Take one local file lock with a monotonic total deadline."""
    path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    handle = path.open("a+")
    deadline = time.monotonic() + timeout if deadline is None else deadline
    try:
        while True:
            _deadline_remaining(deadline, code)
            try:
                fcntl.flock(handle.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
                break
            except BlockingIOError:
                time.sleep(min(0.02, _deadline_remaining(deadline, code)))
        yield handle
    finally:
        with contextlib.suppress(OSError):
            fcntl.flock(handle.fileno(), fcntl.LOCK_UN)
        handle.close()


@dataclass(frozen=True)
class ContainerFact:
    container_id: str
    image_id: str
    started_at: str
    pid: int
    restart_count: int
    network_mode: str
    networks: tuple[tuple[str, str], ...]
    labels: tuple[tuple[str, str], ...]
    port_bindings: tuple[tuple[str, str, str], ...]
    create_spec_hash: str


@dataclass(frozen=True)
class Proof:
    txid: str
    manifest_digest: str
    manifest: dict
    proxy: ContainerFact
    control: ContainerFact
    engines: tuple[ContainerFact, ...]
    engine_records_digest: str
    proxy_entry: tuple[tuple[str, object], ...]
    process_boot_id: str
    mode_epoch: int
    mode: dict


def _json_command(command: list[str], timeout: float) -> object:
    try:
        result = subprocess.run(command, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
                                timeout=timeout, check=False, text=True)
    except (OSError, subprocess.TimeoutExpired) as exc:
        raise SupervisorError("docker_unavailable") from exc
    if result.returncode != 0:
        raise SupervisorError("docker_object_unavailable")
    try:
        return json.loads(result.stdout)
    except Exception as exc:
        raise SupervisorError("docker_response_invalid") from exc


class DockerInspector:
    """Small bounded Docker-API facade; the CLI talks to the local Unix daemon socket."""

    def __init__(self, timeout: float = 2.0):
        self.timeout = float(timeout)
        if not 0.2 <= self.timeout <= 5.0:
            raise SupervisorError("docker_timeout_invalid")

    def container(self, container_id: str) -> ContainerFact:
        raw = _json_command(["docker", "inspect", "--type", "container", container_id],
                            self.timeout)
        if not isinstance(raw, list) or len(raw) != 1 or not isinstance(raw[0], dict):
            raise SupervisorError("docker_container_invalid")
        value = raw[0]
        state = value.get("State") or {}
        host = value.get("HostConfig") or {}
        settings = value.get("NetworkSettings") or {}
        config = value.get("Config") or {}
        actual_id = str(value.get("Id") or "")
        image = str(value.get("Image") or "")
        started = str(state.get("StartedAt") or "")
        pid = state.get("Pid")
        restart = value.get("RestartCount")
        mode = str(host.get("NetworkMode") or "")
        if mode == "default":
            mode = "bridge"
        if (actual_id != container_id or not re.fullmatch(r"[0-9a-f]{64}", actual_id)
                or not str(image).startswith("sha256:")
                or not re.fullmatch(r"[0-9a-f]{64}", str(image)[7:])
                or state.get("Status") != "running" or state.get("Running") is not True
                or not started or type(pid) is not int or pid < 1
                or type(restart) is not int or restart < 0 or not mode):
            raise SupervisorError("docker_generation_invalid")
        networks = settings.get("Networks")
        if not isinstance(networks, dict):
            raise SupervisorError("docker_network_invalid")
        normalized_networks = []
        for name, item in networks.items():
            if not isinstance(name, str) or not isinstance(item, dict):
                raise SupervisorError("docker_network_invalid")
            address = str(item.get("IPAddress") or item.get("GlobalIPv6Address") or "")
            normalized_networks.append((name, address))
        labels = config.get("Labels") or {}
        if not isinstance(labels, dict):
            raise SupervisorError("docker_labels_invalid")
        bindings = host.get("PortBindings") or {}
        if not isinstance(bindings, dict):
            raise SupervisorError("docker_port_bindings_invalid")
        normalized_bindings = []
        for container_port, items in bindings.items():
            if items is None:
                continue
            if not isinstance(container_port, str) or not isinstance(items, list):
                raise SupervisorError("docker_port_bindings_invalid")
            for item in items:
                if not isinstance(item, dict):
                    raise SupervisorError("docker_port_bindings_invalid")
                normalized_bindings.append((container_port, str(item.get("HostIp") or ""),
                                            str(item.get("HostPort") or "")))
        normalized_mounts = [
            {key: item.get(key) for key in
             ("Type", "Source", "Destination", "Mode", "RW", "Propagation")}
            for item in (value.get("Mounts") or []) if isinstance(item, dict)
        ]
        normalized_mounts.sort(key=lambda item: json.dumps(
            item, sort_keys=True, separators=(",", ":")))
        spec = {
            "config": {key: config.get(key) for key in
                       ("Image", "Entrypoint", "Cmd", "Env", "Labels")},
            "host": {key: host.get(key) for key in
                     ("NetworkMode", "PortBindings", "Binds", "RestartPolicy",
                      "Privileged", "ReadonlyRootfs")},
            "mounts": normalized_mounts,
        }
        return ContainerFact(
            actual_id, image, started, pid, restart, mode,
            tuple(sorted(normalized_networks)),
            tuple(sorted((str(key), str(value)) for key, value in labels.items())),
            tuple(sorted(normalized_bindings)), _digest(spec))

    def network(self, name: str) -> dict:
        raw = _json_command(["docker", "network", "inspect", name], self.timeout)
        if not isinstance(raw, list) or len(raw) != 1 or not isinstance(raw[0], dict):
            raise SupervisorError("docker_network_invalid")
        return raw[0]

    def engine_facts(self) -> tuple[ContainerFact, ...]:
        try:
            result = subprocess.run([
                "docker", "ps", "--no-trunc", "-q", "--filter",
                "label=io.mdd-sim-gateway.component=engine"],
                stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, timeout=self.timeout,
                check=False, text=True)
        except (OSError, subprocess.TimeoutExpired) as exc:
            raise SupervisorError("engine_list_unavailable") from exc
        ids = tuple(sorted(item for item in result.stdout.splitlines() if item))
        if result.returncode != 0 or any(not re.fullmatch(r"[0-9a-f]{64}", item)
                                         for item in ids):
            raise SupervisorError("engine_list_invalid")
        facts = tuple(self.container(item) for item in ids)
        verify = subprocess.run([
            "docker", "ps", "--no-trunc", "-q", "--filter",
            "label=io.mdd-sim-gateway.component=engine"],
            stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, timeout=self.timeout,
            check=False, text=True)
        if verify.returncode != 0 or tuple(sorted(verify.stdout.splitlines())) != ids:
            raise SupervisorError("engine_generation_changed")
        if tuple(self.container(item) for item in ids) != facts:
            raise SupervisorError("engine_generation_changed")
        return facts

    def engine_channels_zero(self) -> tuple[ContainerFact, ...]:
        facts = self.engine_facts()
        for fact in facts:
            try:
                channels = subprocess.run([
                    "docker", "exec", fact.container_id, "asterisk", "-rx",
                    "core show channels count"], stdout=subprocess.PIPE,
                    stderr=subprocess.DEVNULL, timeout=self.timeout, check=False, text=True)
            except (OSError, subprocess.TimeoutExpired) as exc:
                raise SupervisorError("asterisk_channels_unknown") from exc
            match = re.search(r"\b(\d+)\s+active channels?\b", channels.stdout, re.I)
            if channels.returncode != 0 or not match:
                raise SupervisorError("asterisk_channels_unknown")
            if int(match.group(1)) != 0:
                raise SupervisorError("asterisk_channels_active")
        if self.engine_facts() != facts:
            raise SupervisorError("engine_generation_changed")
        return facts


def _read_json_strict(path: Path) -> object:
    try:
        with Path(path).open(encoding="utf-8") as handle:
            return json.load(handle)
    except Exception as exc:
        raise SupervisorError("state_unreadable") from exc


def read_ready(path: Path) -> dict:
    value = _read_json_strict(path)
    required = {"version", "txid", "container_id", "image_id", "process_boot_id",
                "mode_epoch", "entry", "ready_at"}
    if (not isinstance(value, dict) or set(value) != required or value.get("version") != 1
            or not isinstance(value.get("txid"), str) or not _TXID.fullmatch(value["txid"])
            or not re.fullmatch(r"[0-9a-f]{64}", str(value.get("container_id") or ""))
            or not str(value.get("image_id") or "").startswith("sha256:")
            or not re.fullmatch(r"[0-9a-f]{64}", str(value["image_id"])[7:])
            or not isinstance(value.get("process_boot_id"), str)
            or not _BOOT_ID.fullmatch(value["process_boot_id"])
            or type(value.get("mode_epoch")) is not int or value["mode_epoch"] < 1
            or type(value.get("ready_at")) is not int or value["ready_at"] < 1):
        raise SupervisorError("ready_invalid")
    entry = value.get("entry")
    if not isinstance(entry, dict) or set(entry) != {
            "bind", "tls_port", "plain_port", "admin_bind", "admin_port"}:
        raise SupervisorError("ready_entry_invalid")
    try:
        __import__("ipaddress").ip_address(str(entry["bind"]))
        __import__("ipaddress").ip_address(str(entry["admin_bind"]))
    except ValueError as exc:
        raise SupervisorError("ready_entry_invalid") from exc
    if (entry["tls_port"] != 8443 or entry["plain_port"] != 8000
            or entry["admin_port"] != 19090
            or any(type(entry[key]) is not int or not 1 <= entry[key] <= 65535
                   for key in ("tls_port", "plain_port", "admin_port"))):
        raise SupervisorError("ready_entry_invalid")
    return value


def read_mode(path: Path, host_boot: str) -> dict:
    value = _read_json_strict(path)
    required = {"version", "txid", "container_id", "image_id", "process_boot_id",
                "host_boot_id", "supervisor_boot_id", "lease_seq", "epoch", "state",
                "active_full", "forwarding_full", "manifest_digest", "updated_at"}
    if not isinstance(value, dict) or set(value) != required or value.get("version") != 1:
        raise SupervisorError("mode_invalid")
    try:
        if (not _TXID.fullmatch(str(value.get("txid") or ""))
                or not re.fullmatch(r"[0-9a-f]{64}", str(value.get("container_id") or ""))
                or _image_id(value.get("image_id"), "proxy") != value["image_id"]
                or not _BOOT_ID.fullmatch(str(value.get("process_boot_id") or ""))
                or value.get("host_boot_id") != host_boot
                or (str(value.get("supervisor_boot_id") or "")
                    and not _BOOT_ID.fullmatch(value["supervisor_boot_id"]))
                or type(value.get("lease_seq")) is not int or value["lease_seq"] < 0
                or type(value.get("epoch")) is not int or value["epoch"] < 1
                or value.get("state") not in {"deny", "full", "revoking", "deny_applied"}
                or type(value.get("active_full")) is not int or value["active_full"] < 0
                or type(value.get("forwarding_full")) is not int
                or not 0 <= value["forwarding_full"] <= value["active_full"]
                or not isinstance(value.get("manifest_digest"), str)
                or (value["manifest_digest"] and
                    not re.fullmatch(r"[0-9a-f]{64}", value["manifest_digest"]))
                or type(value.get("updated_at")) is not int or value["updated_at"] < 1):
            raise SupervisorError("mode_invalid")
    except (ProxyStateError, KeyError, TypeError) as exc:
        raise SupervisorError("mode_invalid") from exc
    leased = value["state"] in {"full", "revoking", "deny_applied"}
    if leased != bool(value["supervisor_boot_id"]) or leased != (value["lease_seq"] > 0):
        raise SupervisorError("mode_lease_invalid")
    return value


def _line_generation(value: object, role: str) -> dict:
    required = {"container_id", "started_at", "restart_count", "pid", "run_id",
                "run_id_mode", "image_id"}
    if not isinstance(value, dict) or set(value) != required:
        raise SupervisorError("line_generation_invalid")
    if (not re.fullmatch(r"[0-9a-f]{64}", str(value.get("container_id") or ""))
            or not str(value.get("image_id") or "").startswith("sha256:")
            or not re.fullmatch(r"[0-9a-f]{64}", str(value["image_id"])[7:])
            or not isinstance(value.get("started_at"), str) or not value["started_at"]
            or type(value.get("restart_count")) is not int or value["restart_count"] < 0
            or type(value.get("pid")) is not int or value["pid"] < 1
            or value.get("run_id_mode") not in {"present", "legacy_absent"}
            or not isinstance(value.get("run_id"), str)
            or (value["run_id_mode"] == "present"
                and not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_.:-]{0,255}", value["run_id"]))
            or (value["run_id_mode"] == "legacy_absent" and value["run_id"] != "")
            or (role == "rollback" and value["run_id_mode"] != "present")):
        raise SupervisorError("line_generation_invalid")
    return value


def _line_records_from_markers(data: Path, manifest: dict) -> list[dict]:
    records = []
    for line in manifest["lines"]:
        path = Path(data) / "instances" / str(line["instance"]) / "run" / \
            "engine-maintenance.json"
        records.append(_read_json_strict(path))
    return records


def _read_line_proof(path: Path, manifest: dict) -> list[dict]:
    value = _read_json_strict(path)
    required = {"version", "txid", "manifest_digest", "supervisor_boot_id",
                "process_boot_id", "mode_epoch", "records"}
    if (not isinstance(value, dict) or set(value) != required or value.get("version") != 1
            or value.get("txid") != manifest["txid"]
            or value.get("manifest_digest") != _digest(manifest)
            or not _BOOT_ID.fullmatch(str(value.get("supervisor_boot_id") or ""))
            or not _BOOT_ID.fullmatch(str(value.get("process_boot_id") or ""))
            or type(value.get("mode_epoch")) is not int or value["mode_epoch"] < 1
            or not isinstance(value.get("records"), list)):
        raise SupervisorError("line_proof_invalid")
    return value["records"]


def prove_engine_topology(data: Path, manifest: dict, inspector: DockerInspector,
                          *, require_zero: bool,
                          records: list[dict] | None = None) \
        -> tuple[tuple[ContainerFact, ...], str]:
    expected: dict[str, tuple[str, dict]] = {}
    records = (_line_records_from_markers(data, manifest)
               if records is None else json.loads(json.dumps(records)))
    if len(records) != len(manifest["lines"]):
        raise SupervisorError("line_record_mismatch")
    records_by_instance = {}
    for record in records:
        if not isinstance(record, dict):
            raise SupervisorError("line_record_mismatch")
        iid = str(record.get("instance") or "")
        if not iid or iid in records_by_instance:
            raise SupervisorError("line_record_mismatch")
        records_by_instance[iid] = record
    if set(records_by_instance) != {str(line["instance"]) for line in manifest["lines"]}:
        raise SupervisorError("line_record_mismatch")
    ordered_records = []
    for line in manifest["lines"]:
        iid = str(line["instance"])
        record = records_by_instance[iid]
        required = {"version", "txid", "instance", "phase", "source",
                    "target_image_digest", "target", "rollback", "attempts",
                    "manual_required", "source_create_spec",
                    "source_create_spec_digest", "rollback_image_ref"}
        if (not isinstance(record, dict) or set(record) != required
                or record.get("version") != 1 or record.get("txid") != manifest["txid"]
                or str(record.get("instance")) != iid
                or record.get("phase") != line["phase"]
                or record.get("target_image_digest") != line["target_image_digest"]
                or type(record.get("attempts")) is not int or not 0 <= record["attempts"] <= 100
                or record.get("manual_required") is not False
                or not isinstance(record.get("source_create_spec"), dict)
                or record["source_create_spec"].get("version") != 1
                or str(record["source_create_spec"].get("instance")) != iid
                or not re.fullmatch(r"[0-9a-f]{64}", str(
                    record.get("source_create_spec_digest") or ""))
                or _digest(record["source_create_spec"]) !=
                    record["source_create_spec_digest"]
                or not re.fullmatch(
                    r"mdd-sim-gateway/engine-rollback:"
                    r"[A-Za-z0-9][A-Za-z0-9_.-]{0,127}",
                    str(record.get("rollback_image_ref") or ""))):
            raise SupervisorError("line_record_mismatch")
        source = _line_generation(record.get("source"), "source")
        if (source["container_id"] != line["source_container_id"]
                or source["image_id"] != line["source_image_id"]):
            raise SupervisorError("line_source_mismatch")
        if line["phase"] == "rollback_verified":
            facts = _line_generation(record.get("rollback"), "rollback")
            if facts["image_id"] != source["image_id"]:
                raise SupervisorError("line_rollback_image_mismatch")
        elif line["phase"] == "aborted":
            if record.get("rollback") is not None:
                raise SupervisorError("line_aborted_rollback_invalid")
            facts = source
        else:
            raise SupervisorError("line_not_terminal")
        if facts["container_id"] in expected:
            raise SupervisorError("line_generation_duplicate")
        expected[facts["container_id"]] = (iid, facts)
        ordered_records.append(record)
    actual = (inspector.engine_channels_zero() if require_zero else inspector.engine_facts())
    if {fact.container_id for fact in actual} != set(expected):
        raise SupervisorError("engine_topology_mismatch")
    for fact in actual:
        iid, wanted = expected[fact.container_id]
        if (fact.image_id != wanted["image_id"] or fact.started_at != wanted["started_at"]
                or fact.pid != wanted["pid"] or fact.restart_count != wanted["restart_count"]):
            raise SupervisorError("engine_generation_mismatch")
        run_path = Path(data) / "instances" / iid / "run" / "engine-run-id"
        if wanted["run_id_mode"] == "present":
            try:
                run_id = run_path.read_text(encoding="utf-8").strip()
            except OSError as exc:
                raise SupervisorError("engine_run_id_unavailable") from exc
            if run_id != wanted["run_id"]:
                raise SupervisorError("engine_run_id_mismatch")
        elif run_path.exists():
            raise SupervisorError("engine_legacy_run_id_mismatch")
    return tuple(sorted(actual, key=lambda item: item.container_id)), _digest(ordered_records)


def read_admin_health(host: str, port: int, timeout: float = 2.0) -> dict:
    try:
        connection = http.client.HTTPConnection(host, port, timeout=timeout)
        connection.request("GET", "/health", headers={"Connection": "close"})
        response = connection.getresponse()
        raw = response.read(65537)
        connection.close()
        if response.status != 200 or len(raw) > 65536:
            raise SupervisorError("proxy_health_unavailable")
        value = json.loads(raw)
    except SupervisorError:
        raise
    except Exception as exc:
        raise SupervisorError("proxy_health_unavailable") from exc
    required = {"ok", "txid", "container_id", "image_id", "process_boot_id",
                "host_boot_id", "supervisor_boot_id", "lease_seq", "epoch", "state",
                "active_full", "forwarding_full", "authorization_lost",
                "authorization_eligible"}
    if not isinstance(value, dict) or set(value) != required:
        raise SupervisorError("proxy_health_invalid")
    if (type(value.get("ok")) is not bool
            or not isinstance(value.get("txid"), str)
            or not isinstance(value.get("container_id"), str)
            or not isinstance(value.get("image_id"), str)
            or not isinstance(value.get("process_boot_id"), str)
            or not isinstance(value.get("host_boot_id"), str)
            or not isinstance(value.get("supervisor_boot_id"), str)
            or type(value.get("lease_seq")) is not int or value["lease_seq"] < 0
            or type(value.get("epoch")) is not int or value["epoch"] < 1
            or value.get("state") not in {"deny", "full", "revoking", "deny_applied"}
            or type(value.get("active_full")) is not int or value["active_full"] < 0
            or type(value.get("forwarding_full")) is not int
            or not 0 <= value["forwarding_full"] <= value["active_full"]
            or type(value.get("authorization_lost")) is not bool
            or type(value.get("authorization_eligible")) is not bool):
        raise SupervisorError("proxy_health_invalid")
    return value


def _proc_cgroup(pid: int) -> str:
    try:
        return Path(f"/proc/{pid}/cgroup").read_text(encoding="ascii")
    except OSError as exc:
        raise SupervisorError("control_process_unavailable") from exc


def _container_cgroup_pids(pid: int) -> set[int]:
    wanted = _proc_cgroup(pid)
    result = set()
    try:
        entries = list(Path("/proc").iterdir())
    except OSError as exc:
        raise SupervisorError("proc_unavailable") from exc
    for entry in entries:
        if not entry.name.isdigit():
            continue
        try:
            if (entry / "cgroup").read_text(encoding="ascii") == wanted:
                result.add(int(entry.name))
        except OSError:
            continue
    if pid not in result:
        raise SupervisorError("control_cgroup_unavailable")
    return result


def _listeners(port: int, pid: int) -> list[tuple[str, str]]:
    """Return (address, inode) for every host/netns TCP listener on one port."""
    result = []
    for name, ipv6 in (("tcp", False), ("tcp6", True)):
        path = Path(f"/proc/{pid}/net/{name}")
        try:
            lines = path.read_text(encoding="ascii").splitlines()[1:]
        except OSError as exc:
            raise SupervisorError("socket_table_unavailable") from exc
        for line in lines:
            fields = line.split()
            if len(fields) < 10 or fields[3] != "0A":
                continue
            encoded, raw_port = fields[1].split(":", 1)
            if int(raw_port, 16) != port:
                continue
            packed = bytes.fromhex(encoded)
            if ipv6:
                # /proc stores each 32-bit word little-endian.
                packed = b"".join(packed[index:index + 4][::-1]
                                  for index in range(0, 16, 4))
            else:
                packed = packed[::-1]
            result.append((str(socket.inet_ntop(socket.AF_INET6 if ipv6 else socket.AF_INET,
                                                packed)), fields[9]))
    return result


def _socket_inode_owners(inodes: set[str], allowed_pids: set[int]) -> set[int]:
    owners = set()
    wanted = {f"socket:[{inode}]" for inode in inodes}
    for pid in allowed_pids:
        try:
            entries = list(Path(f"/proc/{pid}/fd").iterdir())
        except OSError:
            continue
        for entry in entries:
            try:
                if os.readlink(entry) in wanted:
                    owners.add(pid)
            except OSError:
                continue
    return owners


def _all_pids() -> set[int]:
    try:
        return {int(item.name) for item in Path("/proc").iterdir() if item.name.isdigit()}
    except OSError as exc:
        raise SupervisorError("proc_unavailable") from exc


def _prove_owned_listeners(port: int, expected_addresses: set[str],
                           allowed_pids: set[int]) -> None:
    listeners = _listeners(port, 1)
    if not listeners or {address for address, _inode in listeners} != expected_addresses:
        raise SupervisorError("proxy_entry_listener_mismatch")
    inodes = {inode for _address, inode in listeners}
    allowed = _socket_inode_owners(inodes, allowed_pids)
    all_owners = _socket_inode_owners(inodes, _all_pids())
    if not allowed or allowed != all_owners:
        raise SupervisorError("proxy_entry_foreign_owner")


def prove_proxy_ingress(proxy: ContainerFact, entry: dict) -> None:
    """Prove standard external ports terminate at the exact proxy generation."""
    if proxy.network_mode == "host":
        bind = str(ipaddress.ip_address(entry["bind"]))
        admin_bind = str(ipaddress.ip_address(entry["admin_bind"]))
        if not ipaddress.ip_address(admin_bind).is_loopback:
            raise SupervisorError("proxy_admin_exposed")
        pids = _container_cgroup_pids(proxy.pid)
        _prove_owned_listeners(entry["tls_port"], {bind}, pids)
        _prove_owned_listeners(entry["plain_port"], {bind}, pids)
        _prove_owned_listeners(entry["admin_port"], {admin_bind}, pids)
        return
    if proxy.network_mode != "bridge":
        raise SupervisorError("proxy_entry_network_invalid")
    expected_bindings = {
        (f"{entry['tls_port']}/tcp", entry["tls_port"]),
        (f"{entry['plain_port']}/tcp", entry["plain_port"]),
    }
    actual = set()
    for container_port, host_ip, host_port in proxy.port_bindings:
        if container_port == f"{entry['admin_port']}/tcp":
            raise SupervisorError("proxy_admin_exposed")
        if container_port in {item[0] for item in expected_bindings}:
            if not host_port.isdigit():
                raise SupervisorError("proxy_entry_binding_invalid")
            actual.add((container_port, int(host_port)))
            if host_ip not in {"", "0.0.0.0", "::"}:
                raise SupervisorError("proxy_entry_binding_not_global")
    if actual != expected_bindings:
        raise SupervisorError("proxy_entry_binding_invalid")
    container_ips = {address for _name, address in proxy.networks if address}
    if not container_ips:
        raise SupervisorError("proxy_entry_network_invalid")
    admin_bind = str(ipaddress.ip_address(entry["admin_bind"]))
    if admin_bind not in container_ips:
        raise SupervisorError("proxy_admin_binding_invalid")
    admin_listeners = _listeners(entry["admin_port"], proxy.pid)
    if (not admin_listeners
            or {address for address, _inode in admin_listeners} != {admin_bind}):
        raise SupervisorError("proxy_admin_binding_invalid")
    proxy_pids = _container_cgroup_pids(proxy.pid)
    if not _socket_inode_owners(
            {inode for _address, inode in admin_listeners}, proxy_pids):
        raise SupervisorError("proxy_admin_owner_mismatch")
    for container_port, host_port in expected_bindings:
        listeners = _listeners(host_port, 1)
        if not listeners:
            raise SupervisorError("proxy_bridge_userland_proxy_required")
        owners = _socket_inode_owners({inode for _address, inode in listeners}, _all_pids())
        if not owners:
            raise SupervisorError("proxy_entry_listener_owner_unknown")
        wanted_port = container_port.split("/", 1)[0]
        for pid in owners:
            try:
                argv = Path(f"/proc/{pid}/cmdline").read_bytes().replace(b"\0", b" ").decode()
            except OSError as exc:
                raise SupervisorError("proxy_entry_listener_owner_unknown") from exc
            if "docker-proxy" not in argv:
                raise SupervisorError("proxy_bridge_userland_proxy_required")
            if (f"-container-port {wanted_port}" not in argv
                    or f"-host-port {host_port}" not in argv
                    or not any(f"-container-ip {address}" in argv for address in container_ips)):
                raise SupervisorError("proxy_entry_listener_owner_mismatch")


def prove_host_sockets(manifest: dict, control: ContainerFact) -> None:
    upstream = manifest["rollback_upstream"]
    control_pids = _container_cgroup_pids(control.pid)
    for host_key, port_key in (("tls_host", "tls_port"), ("plain_host", "plain_port")):
        try:
            address = socket.getaddrinfo(upstream[host_key], upstream[port_key],
                                         type=socket.SOCK_STREAM)[0][4][0]
            if not __import__("ipaddress").ip_address(address).is_loopback:
                raise SupervisorError("host_upstream_not_loopback")
        except SupervisorError:
            raise
        except Exception as exc:
            raise SupervisorError("host_upstream_invalid") from exc
        listeners = _listeners(int(upstream[port_key]), 1)
        if not listeners:
            raise SupervisorError("control_listener_missing")
        if any(not __import__("ipaddress").ip_address(item[0]).is_loopback
               for item in listeners):
            raise SupervisorError("control_listener_exposed")
        inodes = {item[1] for item in listeners}
        owners = _socket_inode_owners(inodes, control_pids)
        if not owners or len(owners) != len(_socket_inode_owners(inodes, set(
                int(item.name) for item in Path("/proc").iterdir() if item.name.isdigit()))):
            raise SupervisorError("control_listener_foreign_owner")


def prove_bridge(manifest: dict, proxy: ContainerFact, control: ContainerFact,
                 inspector: DockerInspector) -> None:
    proxy_networks = dict(proxy.networks)
    candidates = []
    for network_name, control_ip in control.networks:
        if network_name not in proxy_networks or not control_ip:
            continue
        network = inspector.network(network_name)
        labels = network.get("Labels") or {}
        containers = network.get("Containers") or {}
        if (isinstance(labels, dict)
                and labels.get("io.mdd-sim-gateway.maintenance") == "true"):
            if (not isinstance(containers, dict)
                    or set(containers) != {control.container_id, proxy.container_id}):
                raise SupervisorError("maintenance_bridge_not_dedicated")
            candidates.append((network_name, control_ip))
    if len(candidates) != 1:
        raise SupervisorError("maintenance_bridge_attachment_invalid")
    _network_name, control_ip = candidates[0]
    labels = dict(control.labels)
    proxy_labels = dict(proxy.labels)
    if (labels.get("io.mdd-sim-gateway.maintenance-upstream") != "true"
            or proxy_labels.get("io.mdd-sim-gateway.component") != "maintenance-proxy"):
        raise SupervisorError("maintenance_bridge_label_missing")
    upstream = manifest["rollback_upstream"]
    if upstream["tls_host"] != control_ip or upstream["plain_host"] != control_ip:
        raise SupervisorError("maintenance_bridge_upstream_mismatch")
    control_pids = _container_cgroup_pids(control.pid)
    for port in (upstream["tls_port"], upstream["plain_port"]):
        listeners = _listeners(int(port), control.pid)
        if not listeners or {address for address, _inode in listeners} != {control_ip}:
            raise SupervisorError("control_listener_missing")
        inodes = {item[1] for item in listeners}
        if not _socket_inode_owners(inodes, control_pids):
            raise SupervisorError("control_listener_foreign_owner")


def probe_upstreams(manifest: dict, timeout: float = 2.0) -> None:
    upstream = manifest["rollback_upstream"]
    for tls, host_key, port_key in ((True, "tls_host", "tls_port"),
                                    (False, "plain_host", "plain_port")):
        try:
            connection = socket.create_connection(
                (upstream[host_key], upstream[port_key]), timeout=timeout)
            if tls:
                context = ssl.create_default_context()
                context.check_hostname = False
                context.verify_mode = ssl.CERT_NONE
                connection = context.wrap_socket(connection, server_hostname=upstream[host_key])
            request = (b"GET /api/auth/status HTTP/1.1\r\nHost: maintenance-probe\r\n"
                       b"Connection: close\r\n\r\n")
            connection.sendall(request)
            response = b""
            while len(response) <= 65536:
                chunk = connection.recv(65537 - len(response))
                if not chunk:
                    break
                response += chunk
            connection.close()
            header, separator, body = response.partition(b"\r\n\r\n")
            if (not separator or not header.startswith(b"HTTP/1.1 200 ")
                    or len(response) > 65536):
                raise SupervisorError("control_application_unhealthy")
            value = json.loads(body)
            if (not isinstance(value, dict)
                    or set(value) != {"configured", "authenticated", "username",
                                      "token", "csrf"}
                    or type(value.get("configured")) is not bool
                    or value.get("authenticated") is not False
                    or not isinstance(value.get("username"), str)
                    or value.get("token") != ""
                    or value.get("csrf") not in {None, ""}):
                raise SupervisorError("control_application_unhealthy")
        except SupervisorError:
            raise
        except Exception as exc:
            raise SupervisorError("control_upstream_unavailable") from exc


class MaintenanceSupervisor:
    LEASE_INTERVAL = 1.0
    REVOKE_TIMEOUT = 20.0
    DRAIN_QUIET = 2.0
    ADMISSION_LOCK_TIMEOUT = 2.0

    def __init__(self, data: Path, *, socket_path: Path | None = None,
                 inspector: DockerInspector | None = None, require_root: bool = True):
        self.data = Path(data)
        self.root = self.data / "orchestrator"
        self.manifest_path = self.root / "control-upgrade.json"
        self.mode_path = self.root / "maintenance-proxy.json"
        self.ready_path = self.root / "maintenance-proxy-ready.json"
        self.status_path = self.root / "maintenance-supervisor-status.json"
        self.entry_fence_path = self.root / "maintenance-entry-fence.json"
        self.line_proof_path = self.root / "maintenance-line-proof.json"
        self.lock_path = self.root / "maintenance-supervisor.lock"
        self.socket_path = Path(socket_path or "/run/mdd-sim-gateway/maintenance-supervisor.sock")
        self.database_path = self.data / "mdd-sim-gateway.sqlite"
        self.inspector = inspector or DockerInspector()
        self.require_root = require_root
        self.host_boot = host_boot_id(required=require_root)
        self.supervisor_boot_id = uuid.uuid4().hex
        self.stop_event = threading.Event()
        self._lock_handle = None
        self._server = None
        self._proof: Proof | None = None
        self._lease_lock = threading.Lock()
        # Heartbeat may replace Proof to advance lease_seq without changing this token. Only
        # invalidation, explicit recovery and stop advance it, so fence commits can distinguish
        # a harmless renewal from loss of authority.
        self._lease_generation = 0
        self._pending_recovery_id: str | None = None
        self._active_generation: str | None = self.supervisor_boot_id
        self._attempt = 0
        self._state = "starting"
        self._error_code = ""

    def _invalidate_proof_locked(self) -> None:
        self._lease_generation += 1
        self._pending_recovery_id = None
        self._proof = None

    def _invalidate_proof(self) -> bool:
        """Invalidate only under the lease-generation lock; timeout stops all future renewal."""
        if not self._lease_lock.acquire(timeout=self.ADMISSION_LOCK_TIMEOUT):
            self.stop_event.set()
            return False
        try:
            self._invalidate_proof_locked()
            return True
        finally:
            self._lease_lock.release()

    def _publish(self, *, state: str | None = None, error: str = "",
                 proof: Proof | None = None) -> dict:
        if state is not None:
            self._state = state
        self._error_code = error
        current = proof or self._proof
        record = {
            "version": 1, "supervisor_boot_id": self.supervisor_boot_id,
            "host_boot_id": self.host_boot, "state": self._state,
            "txid": current.txid if current else "",
            "manifest_digest": current.manifest_digest if current else "",
            "proxy": ({"container_id": current.proxy.container_id,
                       "image_id": current.proxy.image_id,
                       "started_at": current.proxy.started_at,
                       "pid": current.proxy.pid,
                       "restart_count": current.proxy.restart_count}
                      if current else {}),
            "control": ({"container_id": current.control.container_id,
                         "image_id": current.control.image_id,
                         "started_at": current.control.started_at,
                         "pid": current.control.pid,
                         "restart_count": current.control.restart_count}
                        if current else {}),
            "process_boot_id": current.process_boot_id if current else "",
            "mode_epoch": current.mode_epoch if current else 0,
            "attempt": self._attempt, "error_code": error,
            "updated_at": int(time.time()),
        }
        self.root.mkdir(mode=0o700, parents=True, exist_ok=True)
        _atomic_json(self.status_path, record)
        return record

    def _admin_address(self, proxy: ContainerFact,
                       entry: dict | None = None) -> tuple[str, int]:
        if proxy.network_mode == "host":
            return str((entry or {}).get("admin_bind") or "127.0.0.1"), int(
                (entry or {}).get("admin_port") or 19090)
        if not entry:
            raise SupervisorError("proxy_admin_address_unknown")
        address = str(entry.get("admin_bind") or "")
        if address not in {item[1] for item in proxy.networks}:
            raise SupervisorError("proxy_admin_address_unknown")
        return address, int(entry.get("admin_port") or 0)

    @staticmethod
    def _terminal_manifest(manifest: dict) -> None:
        if manifest["phase"] != "rollback_committed":
            raise SupervisorError("manifest_not_rollback_committed")
        if any(line["phase"] not in {"rollback_verified", "aborted"}
               for line in manifest["lines"]):
            raise SupervisorError("line_not_terminal")

    @staticmethod
    def _expected_generations(manifest: dict, proxy: ContainerFact,
                              control: ContainerFact) -> None:
        expected_proxy = manifest["proxy"]
        current = manifest["rollback_control"]
        if (proxy.container_id != expected_proxy["container_id"]
                or proxy.image_id != expected_proxy["image_id"]
                or proxy.restart_count != 0):
            raise SupervisorError("proxy_generation_mismatch")
        checks = {
            "control_container_mismatch": control.container_id == current["container_id"],
            "control_image_mismatch": control.image_id == current["image_id"],
            "control_started_at_mismatch": control.started_at == current["started_at"],
            "control_pid_mismatch": control.pid == current["pid"],
            "control_restart_count_mismatch": (
                control.restart_count == current["restart_count"]),
            "control_network_mode_mismatch": control.network_mode == current["network_mode"],
            "control_create_spec_mismatch": (
                control.create_spec_hash == current["create_spec_hash"]),
        }
        for code, matches in checks.items():
            if not matches:
                raise SupervisorError(code)

    def _network_proof(self, manifest: dict, proxy: ContainerFact,
                       control: ContainerFact) -> None:
        if control.network_mode == "host":
            prove_host_sockets(manifest, control)
        elif control.network_mode == "bridge":
            prove_bridge(manifest, proxy, control, self.inspector)
        else:
            raise SupervisorError("control_network_mode_invalid")

    def _line_records(self, manifest: dict) -> list[dict] | None:
        if os.path.lexists(self.line_proof_path):
            return _read_line_proof(self.line_proof_path, manifest)
        return None

    def _partial_release_pending(self, manifest: dict) -> bool:
        return (os.path.lexists(self.line_proof_path)
                and os.path.lexists(self.entry_fence_path)
                and any(not os.path.lexists(
                    self.data / "instances" / str(line["instance"]) / "run" /
                    "engine-maintenance.json") for line in manifest["lines"]))

    def prove(self) -> Proof:
        manifest = read_manifest(self.manifest_path)
        self._terminal_manifest(manifest)
        digest = _digest(manifest)
        proxy_first = self.inspector.container(manifest["proxy"]["container_id"])
        control_first = self.inspector.container(manifest["rollback_control"]["container_id"])
        self._expected_generations(manifest, proxy_first, control_first)
        ready = read_ready(self.ready_path)
        entry = tuple(sorted(ready["entry"].items()))
        prove_proxy_ingress(proxy_first, ready["entry"])
        mode = read_mode(self.mode_path, self.host_boot)
        health = read_admin_health(*self._admin_address(proxy_first, ready["entry"]))
        expected_identity = {
            "txid": manifest["txid"], "container_id": proxy_first.container_id,
            "image_id": proxy_first.image_id,
            "process_boot_id": ready["process_boot_id"],
        }
        if any(ready[key] != value for key, value in expected_identity.items()):
            raise SupervisorError("ready_identity_mismatch")
        if ready["mode_epoch"] != mode["epoch"]:
            raise SupervisorError("ready_epoch_mismatch")
        if any(health.get(key) != value for key, value in expected_identity.items()):
            raise SupervisorError("health_identity_mismatch")
        for key in ("host_boot_id", "supervisor_boot_id", "lease_seq", "epoch", "state",
                    "active_full", "forwarding_full"):
            if health.get(key) != mode.get(key):
                raise SupervisorError("health_mode_mismatch")
        if (health.get("ok") is not True or health.get("authorization_lost") is not False
                or health.get("authorization_eligible") is not True
                or mode["state"] not in {"deny", "deny_applied"}
                or mode["active_full"] != 0 or mode["forwarding_full"] != 0
                or mode["process_boot_id"] != ready["process_boot_id"]
                or mode["container_id"] != proxy_first.container_id
                or mode["image_id"] != proxy_first.image_id
                or mode["txid"] != manifest["txid"]):
            raise SupervisorError("proxy_not_authorization_eligible")
        try:
            paid = pending_paid_work(self.database_path)
        except UpgradeGuardError as exc:
            raise SupervisorError("paid_work_unknown") from exc
        if any(value != 0 for value in paid.values()):
            raise SupervisorError("paid_work_active")
        self._network_proof(manifest, proxy_first, control_first)
        probe_upstreams(manifest)
        proxy_second = self.inspector.container(proxy_first.container_id)
        control_second = self.inspector.container(control_first.container_id)
        if proxy_second != proxy_first:
            raise SupervisorError("proxy_docker_generation_changed")
        if control_second != control_first:
            raise SupervisorError("control_docker_generation_changed")
        engines, engine_records_digest = prove_engine_topology(
            self.data, manifest, self.inspector, require_zero=True,
            records=self._line_records(manifest))
        return Proof(manifest["txid"], digest, manifest, proxy_first, control_first, engines,
                     engine_records_digest, entry, ready["process_boot_id"],
                     int(mode["epoch"]), mode)

    def _exact_recheck_locked(self, proof: Proof, *, require_zero: bool = True) \
            -> tuple[dict, dict]:
        manifest = read_manifest(self.manifest_path)
        if _digest(manifest) != proof.manifest_digest or manifest != proof.manifest:
            raise SupervisorError("manifest_changed")
        proxy = self.inspector.container(proof.proxy.container_id)
        control = self.inspector.container(proof.control.container_id)
        if proxy != proof.proxy:
            raise SupervisorError("proxy_docker_generation_changed")
        if control != proof.control:
            raise SupervisorError("control_docker_generation_changed")
        engines, records_digest = prove_engine_topology(
            self.data, manifest, self.inspector, require_zero=require_zero,
            records=self._line_records(manifest))
        if engines != proof.engines or records_digest != proof.engine_records_digest:
            raise SupervisorError("engine_generation_changed")
        self._network_proof(manifest, proxy, control)
        ready = read_ready(self.ready_path)
        if tuple(sorted(ready["entry"].items())) != proof.proxy_entry:
            raise SupervisorError("proxy_entry_changed")
        prove_proxy_ingress(proxy, ready["entry"])
        mode = read_mode(self.mode_path, self.host_boot)
        return manifest, mode

    @staticmethod
    def _same_lease_owner(mode: dict, proof: Proof) -> bool:
        stable = ("txid", "container_id", "image_id", "process_boot_id",
                  "host_boot_id", "supervisor_boot_id", "epoch", "state",
                  "manifest_digest")
        return (all(mode.get(key) == proof.mode.get(key) for key in stable)
                and mode.get("state") == "full"
                and type(mode.get("lease_seq")) is int
                and mode["lease_seq"] >= proof.mode.get("lease_seq", 0))

    def _assert_commit_authority(self, proof: Proof, lease_generation: int,
                                 recovery_id: str | None) -> None:
        if (self.stop_event.is_set()
                or self._active_generation != self.supervisor_boot_id
                or self._lease_generation != lease_generation):
            raise SupervisorError("commit_generation_changed")
        if recovery_id is not None:
            if (self._pending_recovery_id != recovery_id or self._proof is not None):
                raise SupervisorError("commit_generation_changed")
            return
        current = self._proof
        if (current is None or current.txid != proof.txid
                or current.manifest_digest != proof.manifest_digest
                or current.process_boot_id != proof.process_boot_id
                or current.mode_epoch != proof.mode_epoch
                or current.proxy != proof.proxy or current.control != proof.control
                or current.engines != proof.engines
                or current.engine_records_digest != proof.engine_records_digest):
            raise SupervisorError("commit_generation_changed")

    def recover(self) -> dict:
        """Explicitly prove and grant one new epoch. No background path calls this."""
        if not self._lease_lock.acquire(timeout=self.ADMISSION_LOCK_TIMEOUT):
            raise SupervisorError("lease_lock_timeout")
        try:
            if (self._active_generation != self.supervisor_boot_id
                    or self.stop_event.is_set()):
                raise SupervisorError("supervisor_stopping")
            # A proxy may have autonomously reached deny_applied after lease expiry while this
            # process still held an old in-memory proof. Explicit recovery starts a new proof
            # attempt and prevents that old heartbeat generation from racing the grant.
            self._invalidate_proof_locked()
            recovery_id = uuid.uuid4().hex
            self._pending_recovery_id = recovery_id
            lease_generation = self._lease_generation
        finally:
            self._lease_lock.release()
        self._attempt += 1
        self._publish(state="proving")
        try:
            initial_mode = read_mode(self.mode_path, self.host_boot)
            if (initial_mode["state"] not in {"deny", "deny_applied"}
                    or initial_mode["active_full"] or initial_mode["forwarding_full"]):
                raise SupervisorError("proxy_not_authorization_eligible")
            initial_manifest = read_manifest(self.manifest_path)
            with self._line_admission_locks(initial_manifest):
                fence = self._ensure_admission_fences(
                    initial_manifest, initial_mode, "recovery")
            proof = self.prove()
            mode_lock = self.mode_path.with_suffix(self.mode_path.suffix + ".lock")
            manifest_lock = self.manifest_path.with_suffix(
                self.manifest_path.suffix + ".lock")
            with _bounded_flock(
                    manifest_lock, self.ADMISSION_LOCK_TIMEOUT,
                    "recover_manifest_lock_timeout"):
                with _bounded_flock(
                        mode_lock, self.ADMISSION_LOCK_TIMEOUT,
                        "recover_mode_lock_timeout"):
                    _manifest, mode = self._exact_recheck_locked(proof)
                    if (mode != proof.mode or mode["state"] not in {"deny", "deny_applied"}
                            or mode["active_full"] or mode["forwarding_full"]):
                        raise SupervisorError("mode_changed")
                    granted = dict(mode)
                    granted.update({
                        "epoch": mode["epoch"] + 1, "state": "full",
                        "supervisor_boot_id": self.supervisor_boot_id, "lease_seq": 1,
                        "manifest_digest": proof.manifest_digest,
                        "updated_at": int(time.time()),
                    })
                    _atomic_json(self.mode_path, granted)
            granted_proof = Proof(
                proof.txid, proof.manifest_digest, proof.manifest, proof.proxy, proof.control,
                proof.engines, proof.engine_records_digest, proof.proxy_entry,
                proof.process_boot_id,
                granted["epoch"], granted)
            # Post-CAS generation checks happen before retaining the authority in memory.
            if (self.inspector.container(proof.proxy.container_id) != proof.proxy
                    or self.inspector.container(proof.control.container_id) != proof.control):
                raise SupervisorError("post_grant_generation_changed")
            probe_upstreams(proof.manifest)
            health = read_admin_health(*self._admin_address(
                proof.proxy, dict(proof.proxy_entry)))
            post_ready = read_ready(self.ready_path)
            if (health.get("ok") is not True or health.get("state") != "full"
                    or health.get("epoch") != granted["epoch"]
                    or health.get("supervisor_boot_id") != self.supervisor_boot_id
                    or health.get("lease_seq") != 1
                    or post_ready["process_boot_id"] != proof.process_boot_id
                    or post_ready["mode_epoch"] != granted["epoch"]):
                raise SupervisorError("post_grant_health_mismatch")
            for key in ("host_boot_id", "supervisor_boot_id", "lease_seq", "epoch", "state",
                        "active_full", "forwarding_full"):
                if health.get(key) != granted.get(key):
                    raise SupervisorError("post_grant_health_mismatch")
            self._commit_entry_fence(
                fence, granted_proof, lease_generation=lease_generation,
                recovery_id=recovery_id)
            if not self._lease_lock.acquire(timeout=self.ADMISSION_LOCK_TIMEOUT):
                raise SupervisorError("lease_lock_timeout")
            try:
                if (self._active_generation != self.supervisor_boot_id
                        or self.stop_event.is_set()
                        or self._lease_generation != lease_generation
                        or self._pending_recovery_id != recovery_id):
                    raise SupervisorError("supervisor_stopping")
                self._proof = granted_proof
                self._pending_recovery_id = None
                return self._publish(state="full", proof=granted_proof)
            finally:
                self._lease_lock.release()
        except FenceReleasePending as exc:
            if not self._lease_lock.acquire(timeout=self.ADMISSION_LOCK_TIMEOUT):
                self.stop_event.set()
                raise SupervisorError("lease_lock_timeout") from exc
            try:
                if (self._active_generation != self.supervisor_boot_id
                        or self.stop_event.is_set()
                        or self._lease_generation != lease_generation
                        or self._pending_recovery_id != recovery_id):
                    raise SupervisorError("supervisor_stopping") from exc
                self._proof = granted_proof
                self._pending_recovery_id = None
                return self._publish(
                    state="release_pending", error=exc.code, proof=granted_proof)
            finally:
                self._lease_lock.release()
        except Exception as exc:
            with contextlib.suppress(Exception):
                self.revoke(wait=False, urgent=True)
            code = exc.code if isinstance(exc, SupervisorError) else "proof_failed"
            self._invalidate_proof()
            self._publish(state="manual_required", error=code)
            raise SupervisorError(code) from exc

    def _heartbeat(self) -> None:
        if not self._lease_lock.acquire(timeout=self.ADMISSION_LOCK_TIMEOUT):
            raise SupervisorError("lease_lock_timeout")
        try:
            if (self._active_generation != self.supervisor_boot_id
                    or self.stop_event.is_set()):
                return
            proof = self._proof
            if proof is None:
                return
            mode_lock = self.mode_path.with_suffix(self.mode_path.suffix + ".lock")
            manifest_lock = self.manifest_path.with_suffix(
                self.manifest_path.suffix + ".lock")
            with _bounded_flock(manifest_lock, self.ADMISSION_LOCK_TIMEOUT,
                                "lease_manifest_lock_timeout"):
                with _bounded_flock(mode_lock, self.ADMISSION_LOCK_TIMEOUT,
                                    "lease_mode_lock_timeout"):
                    if (self._active_generation != self.supervisor_boot_id
                            or self.stop_event.is_set() or self._proof is not proof):
                        return
                    manifest = read_manifest(self.manifest_path)
                    mode = read_mode(self.mode_path, self.host_boot)
                    if (_digest(manifest) != proof.manifest_digest
                            or mode["state"] != "full"
                            or mode["epoch"] != proof.mode_epoch
                            or mode["process_boot_id"] != proof.process_boot_id
                            or mode["supervisor_boot_id"] != self.supervisor_boot_id):
                        raise SupervisorError("lease_owner_changed")
                    mode["lease_seq"] += 1
                    mode["updated_at"] = int(time.time())
                    _atomic_json(self.mode_path, mode)
            if (self._active_generation != self.supervisor_boot_id
                    or self.stop_event.is_set() or self._proof is not proof):
                return
            self._proof = Proof(
                proof.txid, proof.manifest_digest, proof.manifest, proof.proxy,
                proof.control, proof.engines, proof.engine_records_digest,
                proof.proxy_entry, proof.process_boot_id, proof.mode_epoch, mode)
            self._publish(state="full", proof=self._proof)
        except Exception:
            # Invalidate before releasing the generation lock. A fence commit cannot slip
            # between a failed heartbeat and the loop's diagnostic handler.
            self._invalidate_proof_locked()
            raise
        finally:
            self._lease_lock.release()

    def renew(self) -> None:
        proof = self._proof
        if proof is None:
            return
        try:
            if (self.inspector.container(proof.proxy.container_id) != proof.proxy
                    or self.inspector.container(proof.control.container_id) != proof.control):
                raise SupervisorError("lease_generation_changed")
            engines, records_digest = prove_engine_topology(
                self.data, proof.manifest, self.inspector, require_zero=False,
                records=self._line_records(proof.manifest))
            if engines != proof.engines or records_digest != proof.engine_records_digest:
                raise SupervisorError("lease_engine_generation_changed")
            self._network_proof(proof.manifest, proof.proxy, proof.control)
            ready = read_ready(self.ready_path)
            if (ready["process_boot_id"] != proof.process_boot_id
                    or ready["container_id"] != proof.proxy.container_id
                    or ready["image_id"] != proof.proxy.image_id
                    or ready["mode_epoch"] != proof.mode_epoch):
                raise SupervisorError("lease_ready_changed")
            if tuple(sorted(ready["entry"].items())) != proof.proxy_entry:
                raise SupervisorError("lease_entry_changed")
            prove_proxy_ingress(proof.proxy, ready["entry"])
            probe_upstreams(proof.manifest)
            health = read_admin_health(*self._admin_address(
                proof.proxy, dict(proof.proxy_entry)))
            if (health.get("ok") is not True
                    or health.get("authorization_lost") is not False
                    or health.get("state") != "full"
                    or health.get("epoch") != proof.mode_epoch
                    or health.get("process_boot_id") != proof.process_boot_id
                    or health.get("supervisor_boot_id") != self.supervisor_boot_id
                    or type(health.get("lease_seq")) is not int
                    or health.get("lease_seq") < proof.mode["lease_seq"]):
                raise SupervisorError("lease_health_changed")
            self._heartbeat()
            if not self._lease_lock.acquire(timeout=self.ADMISSION_LOCK_TIMEOUT):
                raise SupervisorError("lease_lock_timeout")
            try:
                current = self._proof
                lease_generation = self._lease_generation
            finally:
                self._lease_lock.release()
            if current is not None and os.path.lexists(self.entry_fence_path):
                fence = self._read_entry_fence()
                if (fence["state"] == "recovery" and fence["txid"] == current.txid
                        and fence["process_boot_id"] == current.process_boot_id
                        and fence["mode_epoch"] == current.mode_epoch):
                    try:
                        self._commit_entry_fence(
                            fence, current, lease_generation=lease_generation)
                    except FenceReleasePending as pending:
                        self._publish(
                            state="release_pending", error=pending.code)
                        return
                    except SupervisorError as cleanup_error:
                        if (self._partial_release_pending(current.manifest)
                                and cleanup_error.code in {
                                    "admission_lock_timeout",
                                    "commit_manifest_lock_timeout",
                                    "commit_mode_lock_timeout",
                                    "commit_deadline_timeout",
                                    "commit_generation_changed",
                                }):
                            self._publish(
                                state="release_pending", error=cleanup_error.code)
                            return
                        raise
                    if not self._lease_lock.acquire(timeout=self.ADMISSION_LOCK_TIMEOUT):
                        raise SupervisorError("lease_lock_timeout")
                    try:
                        self._assert_commit_authority(
                            current, lease_generation, recovery_id=None)
                        self._publish(state="full", proof=self._proof)
                    finally:
                        self._lease_lock.release()
        except Exception as exc:
            code = exc.code if isinstance(exc, SupervisorError) else "lease_failed"
            with contextlib.suppress(Exception):
                self.revoke(wait=False, urgent=True)
            self._invalidate_proof()
            self._publish(state="manual_required", error=code)

    def _read_entry_fence(self) -> dict:
        value = _read_json_strict(self.entry_fence_path)
        required = {"version", "fence_id", "txid", "state", "process_boot_id",
                    "mode_epoch", "line_records_digest", "created_at"}
        if (not isinstance(value, dict) or set(value) != required
                or value.get("version") != 1
                or not _BOOT_ID.fullmatch(str(value.get("fence_id") or ""))
                or not _TXID.fullmatch(str(value.get("txid") or ""))
                or value.get("state") not in {"draining", "recovery"}
                or not _BOOT_ID.fullmatch(str(value.get("process_boot_id") or ""))
                or type(value.get("mode_epoch")) is not int or value["mode_epoch"] < 1
                or not re.fullmatch(r"[0-9a-f]{64}",
                                    str(value.get("line_records_digest") or ""))
                or type(value.get("created_at")) is not int or value["created_at"] < 1):
            raise SupervisorError("entry_fence_invalid")
        return value

    def _normalize_entry_fence(self, value: dict, mode: dict, state: str) -> dict:
        if state not in {"draining", "recovery"}:
            raise SupervisorError("entry_fence_invalid")
        if value["txid"] != mode["txid"]:
            raise SupervisorError("entry_fence_owner_mismatch")
        owner_changed = (value["process_boot_id"] != mode["process_boot_id"]
                         or value["mode_epoch"] != mode["epoch"])
        if owner_changed:
            # Only explicit recovery from a fresh, zero-active deny generation may adopt a
            # retained same-transaction fence. Planned revoke can never steal another epoch.
            if (state != "recovery" or mode["state"] not in {"deny", "deny_applied"}
                    or mode["active_full"] or mode["forwarding_full"]):
                raise SupervisorError("entry_fence_owner_mismatch")
            value = {**value, "process_boot_id": mode["process_boot_id"],
                     "mode_epoch": mode["epoch"]}
        if value["state"] != state:
            value = {**value, "state": state}
        return value

    def _ensure_admission_fences(self, manifest: dict, mode: dict, state: str) -> dict:
        """Publish exact legacy-compatible Engine markers before the Control-global fence."""
        snapshot_records = self._line_records(manifest)
        records = (snapshot_records if snapshot_records is not None
                   else _line_records_from_markers(self.data, manifest))
        records_by_iid = {str(item.get("instance")): item for item in records}
        if set(records_by_iid) != {str(line["instance"]) for line in manifest["lines"]}:
            raise SupervisorError("line_record_mismatch")
        records_digest = _digest([records_by_iid[str(line["instance"])]
                                  for line in manifest["lines"]])
        if os.path.lexists(self.entry_fence_path):
            value = self._normalize_entry_fence(self._read_entry_fence(), mode, state)
        else:
            value = {
                "version": 1, "fence_id": uuid.uuid4().hex, "txid": mode["txid"],
                "state": state, "process_boot_id": mode["process_boot_id"],
                "mode_epoch": mode["epoch"], "line_records_digest": records_digest,
                "created_at": int(time.time()),
            }
        if value["line_records_digest"] != records_digest:
            raise SupervisorError("entry_fence_line_proof_mismatch")
        for line in manifest["lines"]:
            iid = str(line["instance"])
            path = self.data / "instances" / iid / "run" / "engine-maintenance.json"
            expected = records_by_iid[iid]
            if os.path.lexists(path):
                if _read_json_strict(path) != expected:
                    raise SupervisorError("line_record_changed")
            else:
                _atomic_json(path, expected)
        if not os.path.lexists(self.entry_fence_path) or self._read_entry_fence() != value:
            _atomic_json(self.entry_fence_path, value)
        return value

    def _verify_admission_fences(self, manifest: dict, fence: dict,
                                 *, allow_released: bool = False) -> None:
        if self._read_entry_fence() != fence:
            raise SupervisorError("entry_fence_changed")
        snapshot = self._line_records(manifest)
        if allow_released and snapshot is not None:
            expected = {str(item["instance"]): item for item in snapshot}
            for line in manifest["lines"]:
                iid = str(line["instance"])
                path = self.data / "instances" / iid / "run" / \
                    "engine-maintenance.json"
                if os.path.lexists(path) and _read_json_strict(path) != expected[iid]:
                    raise SupervisorError("line_record_changed")
            if _digest(snapshot) != fence["line_records_digest"]:
                raise SupervisorError("line_record_changed")
            return
        records = _line_records_from_markers(self.data, manifest)
        if _digest(records) != fence["line_records_digest"]:
            raise SupervisorError("line_record_changed")

    @contextlib.contextmanager
    def _line_admission_locks(self, manifest: dict, *,
                              deadline: float | None = None):
        handles = []
        deadline = (time.monotonic() + self.ADMISSION_LOCK_TIMEOUT
                    if deadline is None else deadline)
        try:
            for iid in sorted(str(line["instance"]) for line in manifest["lines"]):
                _deadline_remaining(deadline, "admission_lock_timeout")
                run_dir = self.data / "instances" / iid / "run"
                run_dir.mkdir(mode=0o700, parents=True, exist_ok=True)
                for name in (".engine-maintenance.lock", ".pcscf-rebind.lock"):
                    _deadline_remaining(deadline, "admission_lock_timeout")
                    handle = (run_dir / name).open("a+")
                    handles.append(handle)
                    while True:
                        _deadline_remaining(deadline, "admission_lock_timeout")
                        try:
                            fcntl.flock(handle.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
                            break
                        except BlockingIOError:
                            time.sleep(min(
                                0.02, _deadline_remaining(
                                    deadline, "admission_lock_timeout")))
            yield
        finally:
            for handle in reversed(handles):
                with contextlib.suppress(OSError):
                    fcntl.flock(handle.fileno(), fcntl.LOCK_UN)
                handle.close()

    def _drained_now(self, manifest: dict) -> None:
        try:
            paid = pending_paid_work(self.database_path)
        except Exception as exc:
            raise SupervisorError("paid_work_unknown") from exc
        if any(value != 0 for value in paid.values()):
            raise SupervisorError("paid_work_active")
        prove_engine_topology(
            self.data, manifest, self.inspector, require_zero=True,
            records=self._line_records(manifest))

    def _wait_drained(self, manifest: dict, fence: dict) -> None:
        deadline = time.monotonic() + self.REVOKE_TIMEOUT
        quiet_since = None
        next_renew = time.monotonic()
        while time.monotonic() < deadline:
            try:
                self._verify_admission_fences(manifest, fence)
                self._drained_now(manifest)
                quiet_since = quiet_since or time.monotonic()
                if time.monotonic() - quiet_since >= self.DRAIN_QUIET:
                    with self._line_admission_locks(manifest):
                        self._verify_admission_fences(manifest, fence)
                        self._drained_now(manifest)
                    return
            except Exception:
                quiet_since = None
            if time.monotonic() >= next_renew:
                self.renew()
                if self._proof is None:
                    raise SupervisorError("lease_failed_during_drain")
                next_renew = time.monotonic() + self.LEASE_INTERVAL
            time.sleep(0.1)
        raise SupervisorError("paid_drain_timeout")

    @staticmethod
    def _durable_unlink(path: Path) -> None:
        os.unlink(path)
        dirfd = os.open(path.parent, os.O_RDONLY | getattr(os, "O_DIRECTORY", 0))
        try:
            os.fsync(dirfd)
        finally:
            os.close(dirfd)

    def _commit_entry_fence(self, fence: dict, proof: Proof, *,
                            lease_generation: int,
                            recovery_id: str | None = None) -> None:
        # One shared deadline covers every lock tree and both exact-proof phases. It is shorter
        # than the proxy's five-second autonomous lease, so an overdue commit never starts an
        # irreversible unlink under authority the proxy may already have withdrawn.
        deadline = time.monotonic() + self.ADMISSION_LOCK_TIMEOUT
        mode_lock = self.mode_path.with_suffix(self.mode_path.suffix + ".lock")
        manifest_lock = self.manifest_path.with_suffix(
            self.manifest_path.suffix + ".lock")
        with self._line_admission_locks(proof.manifest, deadline=deadline):
            existing_snapshot = os.path.lexists(self.line_proof_path)
            partial_released = existing_snapshot and any(
                not os.path.lexists(
                    self.data / "instances" / str(line["instance"]) / "run" /
                    "engine-maintenance.json")
                for line in proof.manifest["lines"])

            # Phase one may do heavier proof work, but releases manifest/mode before taking the
            # lease lock. This avoids inversion with heartbeat's lease -> manifest -> mode order.
            with _bounded_flock(
                    manifest_lock, self.ADMISSION_LOCK_TIMEOUT,
                    "commit_manifest_lock_timeout", deadline=deadline):
                with _bounded_flock(
                        mode_lock, self.ADMISSION_LOCK_TIMEOUT,
                        "commit_mode_lock_timeout", deadline=deadline):
                    manifest, mode = self._exact_recheck_locked(
                        proof, require_zero=not partial_released)
                    if not self._same_lease_owner(mode, proof):
                        raise SupervisorError("entry_fence_commit_mismatch")
                    if (fence["state"] == "recovery"
                            and fence["process_boot_id"] == proof.process_boot_id
                            and fence["mode_epoch"] == proof.mode_epoch - 1):
                        fence = {**fence, "mode_epoch": proof.mode_epoch}
                        _deadline_remaining(deadline, "commit_deadline_timeout")
                        _atomic_json(self.entry_fence_path, fence)
                    self._verify_admission_fences(
                        manifest, fence, allow_released=existing_snapshot)
                    records = (self._line_records(manifest)
                               or _line_records_from_markers(self.data, manifest))
                    engines, records_digest = prove_engine_topology(
                        self.data, manifest, self.inspector,
                        require_zero=not partial_released, records=records)
                    if (engines != proof.engines
                            or records_digest != proof.engine_records_digest):
                        raise SupervisorError("line_proof_generation_changed")
                    _deadline_remaining(deadline, "commit_deadline_timeout")
                    try:
                        _atomic_json(self.line_proof_path, {
                            "version": 1, "txid": manifest["txid"],
                            "manifest_digest": proof.manifest_digest,
                            "supervisor_boot_id": self.supervisor_boot_id,
                            "process_boot_id": proof.process_boot_id,
                            "mode_epoch": proof.mode_epoch, "records": records,
                        })
                    except Exception as exc:
                        raise FenceReleasePending(
                            "entry_fence_release_pending") from exc

            # Final linearization: line -> lease -> manifest -> mode. Re-run all exact Docker,
            # Engine/run-id, ready/entry and network facts while the lease generation is held.
            remaining = _deadline_remaining(deadline, "commit_deadline_timeout")
            if not self._lease_lock.acquire(timeout=remaining):
                raise SupervisorError("commit_deadline_timeout")
            try:
                self._assert_commit_authority(
                    proof, lease_generation, recovery_id)
                with _bounded_flock(
                        manifest_lock, self.ADMISSION_LOCK_TIMEOUT,
                        "commit_manifest_lock_timeout", deadline=deadline):
                    with _bounded_flock(
                            mode_lock, self.ADMISSION_LOCK_TIMEOUT,
                            "commit_mode_lock_timeout", deadline=deadline):
                        manifest, mode = self._exact_recheck_locked(
                            proof, require_zero=not partial_released)
                        _deadline_remaining(deadline, "commit_deadline_timeout")
                        self._assert_commit_authority(
                            proof, lease_generation, recovery_id)
                        if not self._same_lease_owner(mode, proof):
                            raise SupervisorError("entry_fence_commit_mismatch")
                        self._verify_admission_fences(
                            manifest, fence, allow_released=existing_snapshot)
                        final_records = self._line_records(manifest)
                        if final_records != records:
                            raise SupervisorError("line_record_changed")
                        records_by_iid = {
                            str(item["instance"]): item for item in final_records}
                        released_any = partial_released
                        try:
                            for line in manifest["lines"]:
                                _deadline_remaining(
                                    deadline, "commit_deadline_timeout")
                                self._assert_commit_authority(
                                    proof, lease_generation, recovery_id)
                                iid = str(line["instance"])
                                marker = self.data / "instances" / iid / "run" / \
                                    "engine-maintenance.json"
                                if os.path.lexists(marker):
                                    if _read_json_strict(marker) != records_by_iid[iid]:
                                        raise SupervisorError("line_record_changed")
                                    try:
                                        self._durable_unlink(marker)
                                    except OSError:
                                        if os.path.lexists(marker):
                                            raise
                                    released_any = True
                                elif not existing_snapshot:
                                    raise SupervisorError("line_record_missing")
                            _deadline_remaining(deadline, "commit_deadline_timeout")
                            self._assert_commit_authority(
                                proof, lease_generation, recovery_id)
                            try:
                                self._durable_unlink(self.entry_fence_path)
                            except OSError:
                                if os.path.lexists(self.entry_fence_path):
                                    raise
                        except SupervisorError as exc:
                            if released_any:
                                raise FenceReleasePending(exc.code) from exc
                            raise
                        except Exception as exc:
                            raise FenceReleasePending(
                                "entry_fence_release_pending") from exc
            finally:
                self._lease_lock.release()

    def revoke(self, *, wait: bool = True, urgent: bool = False) -> dict:
        """CAS any full claim to revoking and optionally await the exact proxy ack."""
        expected = None
        manifest = None
        fence = None
        mode_lock = self.mode_path.with_suffix(self.mode_path.suffix + ".lock")
        initial = read_mode(self.mode_path, self.host_boot)
        if (initial["state"] in {"deny", "deny_applied"}
                and initial["active_full"] == 0 and initial["forwarding_full"] == 0):
            if not self._lease_lock.acquire(timeout=self.ADMISSION_LOCK_TIMEOUT):
                raise SupervisorError("lease_lock_timeout")
            try:
                self._invalidate_proof_locked()
            finally:
                self._lease_lock.release()
            return self._publish(state=initial["state"])
        if initial["state"] == "full" and not urgent:
            manifest = read_manifest(self.manifest_path)
            with self._line_admission_locks(manifest):
                fence = self._ensure_admission_fences(manifest, initial, "draining")
            self._publish(state="draining")
            try:
                self._wait_drained(manifest, fence)
            except SupervisorError as exc:
                self._publish(state="manual_required", error=exc.code)
                raise
        admission_guard = (self._line_admission_locks(manifest)
                           if fence is not None and manifest is not None
                           else contextlib.nullcontext())
        with admission_guard:
            if fence is not None and manifest is not None:
                self._verify_admission_fences(manifest, fence)
                self._drained_now(manifest)
            if not self._lease_lock.acquire(timeout=self.ADMISSION_LOCK_TIMEOUT):
                raise SupervisorError("lease_lock_timeout")
            try:
                try:
                    manifest_lock = self.manifest_path.with_suffix(
                        self.manifest_path.suffix + ".lock")
                    with _bounded_flock(
                            manifest_lock, self.ADMISSION_LOCK_TIMEOUT,
                            "revoke_manifest_lock_timeout"):
                        with _bounded_flock(
                                mode_lock, self.ADMISSION_LOCK_TIMEOUT,
                                "revoke_mode_lock_timeout"):
                            mode = read_mode(self.mode_path, self.host_boot)
                            if (mode["txid"] != initial["txid"]
                                    or mode["process_boot_id"] != initial["process_boot_id"]
                                    or mode["epoch"] != initial["epoch"]):
                                raise SupervisorError("revoke_owner_changed")
                            if mode["state"] == "full":
                                mode["state"] = "revoking"
                                mode["updated_at"] = int(time.time())
                                _atomic_json(self.mode_path, mode)
                            expected = {key: mode[key] for key in (
                                "txid", "container_id", "image_id", "process_boot_id",
                                "host_boot_id", "supervisor_boot_id", "epoch")}
                except Exception as exc:
                    # Invalidate while holding the same generation lock as heartbeat. No old
                    # proof can be published after an unknown revoke CAS.
                    self._invalidate_proof_locked()
                    code = (exc.code if isinstance(exc, SupervisorError)
                            else "revoke_write_failed")
                    self._publish(state="manual_required", error=code)
                    raise SupervisorError(code) from exc
                self._invalidate_proof_locked()
            finally:
                self._lease_lock.release()
        if (mode["state"] in {"deny", "deny_applied"}
                and mode["active_full"] == 0 and mode["forwarding_full"] == 0):
            return self._publish(state=mode["state"])
        self._publish(state="revoking")
        if not wait:
            return self._publish(state="revoking")
        deadline = time.monotonic() + self.REVOKE_TIMEOUT
        while time.monotonic() < deadline:
            mode = read_mode(self.mode_path, self.host_boot)
            if (all(mode.get(key) == value for key, value in expected.items())
                    and mode["state"] == "deny_applied"
                    and mode["active_full"] == 0 and mode["forwarding_full"] == 0):
                return self._publish(state="deny_applied")
            time.sleep(0.1)
        self._publish(state="manual_required", error="revoke_ack_timeout")
        raise SupervisorError("revoke_ack_timeout")

    def status(self) -> dict:
        try:
            value = _read_json_strict(self.status_path)
            return value if isinstance(value, dict) else self._publish(error="status_invalid")
        except SupervisorError:
            return self._publish()

    def _acquire_singleton(self) -> None:
        if self.require_root and os.geteuid() != 0:
            raise SupervisorError("root_required")
        self.root.mkdir(mode=0o700, parents=True, exist_ok=True)
        handle = self.lock_path.open("a+")
        try:
            fcntl.flock(handle.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
        except OSError as exc:
            handle.close()
            raise SupervisorError("supervisor_already_running") from exc
        self._lock_handle = handle

    def _startup_revoke(self) -> None:
        if not self.mode_path.exists():
            self._publish(state="idle")
            return
        try:
            mode = read_mode(self.mode_path, self.host_boot)
            if mode["state"] in {"full", "revoking"}:
                self.revoke(wait=False, urgent=True)
            else:
                self._publish(state=mode["state"])
        except Exception as exc:
            code = exc.code if isinstance(exc, SupervisorError) else "startup_state_invalid"
            self._publish(state="manual_required", error=code)

    @staticmethod
    def _peer_is_root(connection: socket.socket) -> bool:
        if not hasattr(socket, "SO_PEERCRED"):
            return False
        try:
            raw = connection.getsockopt(socket.SOL_SOCKET, socket.SO_PEERCRED, 12)
            _pid, uid, _gid = __import__("struct").unpack("3i", raw)
            return uid == 0
        except OSError:
            return False

    def _serve_command(self, connection: socket.socket) -> None:
        response = {"ok": False, "error_code": "command_invalid"}
        try:
            if self.require_root and not self._peer_is_root(connection):
                raise SupervisorError("command_forbidden")
            connection.settimeout(2.0)
            raw = connection.recv(4097)
            if len(raw) > 4096 or not raw.endswith(b"\n"):
                raise SupervisorError("command_invalid")
            value = json.loads(raw)
            if not isinstance(value, dict) or set(value) != {"version", "action"} \
                    or value.get("version") != 1 \
                    or value.get("action") not in {"status", "revoke", "recover"}:
                raise SupervisorError("command_invalid")
            if value["action"] == "status":
                result = self.status()
            elif value["action"] == "revoke":
                result = self.revoke()
            else:
                result = self.recover()
            response = {"ok": True, "status": result}
        except Exception as exc:
            response = {"ok": False, "error_code": (
                exc.code if isinstance(exc, SupervisorError) else "command_failed")}
        with contextlib.suppress(OSError):
            connection.sendall(json.dumps(response, sort_keys=True,
                                          separators=(",", ":")).encode() + b"\n")

    def run(self) -> None:
        self._acquire_singleton()
        self._startup_revoke()
        self.socket_path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
        with contextlib.suppress(FileNotFoundError):
            self.socket_path.unlink()
        server = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self._server = server
        def heartbeat_loop():
            while not self.stop_event.wait(self.LEASE_INTERVAL):
                try:
                    self._heartbeat()
                except Exception as exc:
                    if (self.stop_event.is_set()
                            or self._active_generation != self.supervisor_boot_id):
                        return
                    code = exc.code if isinstance(exc, SupervisorError) else "lease_failed"
                    # A heartbeat failure must not enter another potentially contended lock
                    # tree. Stop lease_seq progress and let the proxy's monotonic lease expire.
                    self._invalidate_proof()
                    with contextlib.suppress(Exception):
                        self._publish(state="manual_required", error=code)
        heartbeat = threading.Thread(
            target=heartbeat_loop, name="mdd-maintenance-lease", daemon=True)
        heartbeat.start()
        try:
            server.bind(str(self.socket_path))
            os.chmod(self.socket_path, 0o600)
            server.listen(8)
            server.setblocking(False)
            next_audit = time.monotonic() + 5.0
            while not self.stop_event.is_set():
                timeout = max(0.0, min(0.25, next_audit - time.monotonic()))
                ready, _, _ = select.select([server], [], [], timeout)
                if ready:
                    connection, _ = server.accept()
                    with connection:
                        self._serve_command(connection)
                if time.monotonic() >= next_audit:
                    self.renew()
                    next_audit = time.monotonic() + 5.0
        finally:
            self.stop_event.set()
            generation_stopped = self._lease_lock.acquire(
                timeout=self.ADMISSION_LOCK_TIMEOUT * 2 + 0.5)
            if generation_stopped:
                try:
                    self._active_generation = None
                    self._invalidate_proof_locked()
                finally:
                    self._lease_lock.release()
            heartbeat.join(timeout=self.ADMISSION_LOCK_TIMEOUT * 2 + 0.5)
            server.close()
            self._server = None
            with contextlib.suppress(FileNotFoundError):
                self.socket_path.unlink()
            # Never release the singleton while an old lease writer could still run. Keeping
            # the fd open fails closed and prevents a replacement generation from overlapping.
            heartbeat_stopped = generation_stopped and not heartbeat.is_alive()
            if heartbeat_stopped and self._lock_handle is not None:
                self._lock_handle.close()
                self._lock_handle = None
            if not heartbeat_stopped:
                raise SupervisorError("heartbeat_stop_timeout")

    def stop(self) -> None:
        # Publish intent first so an in-flight commit sees it before any further unlink. The
        # lease lock then supplies the bounded generation fence; failure still leaves heartbeat
        # stopped and lets the proxy's monotonic lease fail closed.
        self.stop_event.set()
        if self._server is not None:
            with contextlib.suppress(OSError):
                wake = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
                wake.connect(str(self.socket_path))
                wake.close()
        if self._lease_lock.acquire(timeout=self.ADMISSION_LOCK_TIMEOUT):
            try:
                self._active_generation = None
                self._invalidate_proof_locked()
            finally:
                self._lease_lock.release()


def command(socket_path: Path, action: str, timeout: float = 25.0) -> dict:
    if action not in {"status", "revoke", "recover"}:
        raise SupervisorError("command_invalid")
    client = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    client.settimeout(timeout)
    try:
        client.connect(str(socket_path))
        client.sendall(json.dumps({"version": 1, "action": action},
                                  separators=(",", ":")).encode() + b"\n")
        raw = b""
        while not raw.endswith(b"\n") and len(raw) <= 65536:
            chunk = client.recv(65537 - len(raw))
            if not chunk:
                break
            raw += chunk
    finally:
        client.close()
    try:
        value = json.loads(raw)
    except Exception as exc:
        raise SupervisorError("command_response_invalid") from exc
    if not isinstance(value, dict) or value.get("ok") is not True:
        raise SupervisorError(str((value or {}).get("error_code") or "command_failed"))
    return value["status"]


def main(argv=None) -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--data", required=True, type=Path)
    parser.add_argument("--socket", type=Path,
                        default=Path("/run/mdd-sim-gateway/maintenance-supervisor.sock"))
    parser.add_argument("action", choices=("watch", "status", "revoke", "recover"))
    args = parser.parse_args(argv)
    try:
        if args.action == "watch":
            MaintenanceSupervisor(args.data.resolve(), socket_path=args.socket).run()
        else:
            print(json.dumps(command(args.socket, args.action), sort_keys=True))
        return 0
    except (SupervisorError, OSError) as exc:
        print(f"maintenance supervisor failed closed: {exc}", flush=True)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
