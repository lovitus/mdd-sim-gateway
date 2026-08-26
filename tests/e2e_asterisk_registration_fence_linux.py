#!/usr/bin/env python3
"""No-charge Linux E2E for the patched Asterisk REGISTER dispatch fence."""
from __future__ import annotations

import fcntl
import json
import os
from pathlib import Path
import queue
import re
import shutil
import signal
import socket
import subprocess
import sys
import tempfile
import threading
import time
import uuid

try:
    from engine import admission_gate
except ImportError:
    import importlib.util
    spec = importlib.util.spec_from_file_location(
        "mdd_e2e_admission_gate", "/usr/local/bin/admission_gate.py")
    require_spec = spec is not None and spec.loader is not None
    if not require_spec:
        raise
    admission_gate = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = admission_gate
    spec.loader.exec_module(admission_gate)


def require(value, reason):
    if not value:
        raise AssertionError(reason)


class Registrar:
    def __init__(self):
        self.socket = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        self.socket.bind(("127.0.0.1", 0))
        self.port = self.socket.getsockname()[1]
        self.messages: queue.Queue[bytes] = queue.Queue()
        self.stop = threading.Event()
        self.thread = threading.Thread(target=self._run, name="mdd-test-registrar", daemon=True)

    @staticmethod
    def _header(text: str, name: str) -> str:
        match = re.search(rf"(?im)^{re.escape(name)}:\s*(.+?)\r?$", text)
        return match.group(1).strip() if match else ""

    def _run(self):
        self.socket.settimeout(0.1)
        while not self.stop.is_set():
            try:
                payload, peer = self.socket.recvfrom(65535)
            except socket.timeout:
                continue
            except OSError:
                return
            if not payload.startswith(b"REGISTER "):
                continue
            self.messages.put(payload)
            text = payload.decode("utf-8", errors="replace")
            via = self._header(text, "Via")
            source = self._header(text, "From")
            target = self._header(text, "To")
            if ";tag=" not in target:
                target += ";tag=mdd-e2e"
            response = (
                "SIP/2.0 403 Forbidden\r\n"
                f"Via: {via}\r\nFrom: {source}\r\nTo: {target}\r\n"
                f"Call-ID: {self._header(text, 'Call-ID')}\r\n"
                f"CSeq: {self._header(text, 'CSeq')}\r\n"
                "Server: mdd-loopback-e2e\r\nContent-Length: 0\r\n\r\n"
            ).encode("ascii", errors="strict")
            self.socket.sendto(response, peer)

    def start(self):
        self.thread.start()

    def count(self) -> int:
        return self.messages.qsize()

    def close(self):
        self.stop.set()
        self.socket.close()
        self.thread.join(1)


def module_directory() -> Path:
    matches = [path for root in (Path("/usr/lib64"), Path("/usr/lib"))
               for path in root.glob("asterisk/modules")
               if (path / "res_pjsip_outbound_registration.so").is_file()]
    require(len(matches) == 1, "Asterisk module directory is missing or ambiguous")
    for module in ("res_mdd_admission.so", "res_pjsip_outbound_registration.so"):
        require((matches[0] / module).is_file(), f"candidate lacks {module}")
    return matches[0]


def write_configuration(root: Path, registrar_port: int, manager_port: int) -> Path:
    etc = root / "etc"
    etc.mkdir()
    modules = module_directory()
    directories = {
        "astetcdir": etc, "astmoddir": modules, "astvarlibdir": root / "lib",
        "astdbdir": root / "lib", "astkeydir": root / "keys",
        "astdatadir": Path("/var/lib/asterisk"),
        "astagidir": root / "agi", "astspooldir": root / "spool",
        "astrundir": root / "run", "astlogdir": root / "log", "astsbindir": "/usr/sbin",
    }
    for path in directories.values():
        if isinstance(path, Path):
            path.mkdir(parents=True, exist_ok=True)
    asterisk_conf = root / "asterisk.conf"
    asterisk_conf.write_text(
        "[directories]\n" + "".join(f"{name} => {value}\n" for name, value in directories.items())
        + "[options]\nverbose = 3\n",
        encoding="utf-8")
    (etc / "modules.conf").write_text("[modules]\nautoload=yes\n", encoding="utf-8")
    (etc / "logger.conf").write_text(
        "[general]\ndateformat=%F %T.%3q\n[logfiles]\nconsole => notice,warning,error\n",
        encoding="utf-8")
    (etc / "manager.conf").write_text(
        f"[general]\nenabled=yes\nbindaddr=127.0.0.1\nport={manager_port}\n"
        "[mdd-e2e]\nsecret=mdd-e2e-secret\nread=all\nwrite=all\n",
        encoding="utf-8")
    (etc / "pjsip.conf").write_text(
        "[global]\ntype=global\nuser_agent=MDD-registration-fence-E2E\n"
        "[transport-test]\ntype=transport\nprotocol=udp\nbind=127.0.0.1:5060\n"
        "[volte_ims]\ntype=registration\ntransport=transport-test\n"
        f"server_uri=sip:127.0.0.1:{registrar_port}\n"
        "client_uri=sip:mdd-e2e@127.0.0.1\ncontact_user=mdd-e2e\n"
        "retry_interval=1\nforbidden_retry_interval=0\nfatal_retry_interval=0\n"
        "max_retries=0\nexpiration=60\nmax_random_initial_delay=0\n",
        encoding="utf-8")
    for name in ("extensions.conf", "queues.conf", "voicemail.conf"):
        (etc / name).write_text("[general]\n", encoding="utf-8")
    return asterisk_conf


def free_tcp_port() -> int:
    listener = socket.socket()
    listener.bind(("127.0.0.1", 0))
    port = listener.getsockname()[1]
    listener.close()
    return port


def ami_action(port: int, fields: dict[str, str], timeout: float = 3.0) -> dict[str, str]:
    sock = socket.create_connection(("127.0.0.1", port), timeout=timeout)
    stream = sock.makefile("rwb", buffering=0)
    try:
        stream.readline()
        def exchange(values):
            payload = "".join(f"{key}: {value}\r\n" for key, value in values.items()) + "\r\n"
            stream.write(payload.encode("ascii"))
            response = {}
            while True:
                line = stream.readline().decode("utf-8", errors="replace").rstrip("\r\n")
                if not line:
                    return response
                key, separator, value = line.partition(":")
                if separator:
                    response[key] = value.strip()
        login = exchange({"Action": "Login", "Username": "mdd-e2e",
                          "Secret": "mdd-e2e-secret", "Events": "off"})
        require(login.get("Response") == "Success", "AMI login failed")
        return exchange(fields)
    finally:
        try:
            stream.close()
        finally:
            sock.close()


def wait_until(predicate, timeout=5.0, *, process=None, stage="condition", log_path=None):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if process is not None and process.poll() is not None:
            log = Path(log_path).read_text(errors="replace") if log_path else ""
            raise AssertionError(
                f"Asterisk exited rc={process.returncode} during {stage}:\n{log[-12000:]}")
        try:
            value = predicate()
        except (OSError, ConnectionError):
            value = None
        if value:
            return value
        time.sleep(0.02)
    raise AssertionError(f"bounded E2E wait timed out during {stage}")


def write_permit(rundir: Path, nonce: str, run_id: str):
    now = time.time()
    value = {"version": 2, "phase": "permit_issued", "permit_nonce": nonce,
             "campaign_epoch": "b" * 64, "engine_run_id": run_id,
             "auth_seq_baseline": 7, "issued_at": now, "deadline": now + 10}
    (rundir / admission_gate.REGISTRATION_PERMIT_NAME).write_text(
        json.dumps(value), encoding="utf-8")


def main() -> int:
    require(sys.platform.startswith("linux"), "Linux validation image is required")
    require(os.geteuid() == 0, "isolated validation container must run as root")
    require(shutil.which("asterisk"), "Asterisk executable is missing")
    registrar = Registrar()
    manager_port = free_tcp_port()
    process = None
    service = None
    with tempfile.TemporaryDirectory(prefix="mdd-registration-e2e.") as directory:
        root = Path(directory)
        rundir = root / "run"
        rundir.mkdir()
        run_id = str(uuid.uuid4())
        fence = rundir / "usim-auth-recovery.fence"
        fence.write_text("fenced", encoding="utf-8")
        state = admission_gate.GateState("1", run_id)
        service = admission_gate.GateService(
            state, rundir / "admission-authority.json",
            rundir / "admission-gate.sock", rundir / "admission-status.json",
            fence_paths=((fence, "local_fence_usim_auth_recovery"),))
        registrar.start()
        service.start()
        config = write_configuration(root, registrar.port, manager_port)
        asterisk_log = root / "asterisk.log"
        asterisk_output = asterisk_log.open("w")
        env = dict(os.environ, MDD_RUNDIR=str(rundir),
                   MDD_ADMISSION_SOCKET=str(rundir / "admission-gate.sock"),
                   MDD_ENGINE_RUN_ID=run_id)
        process = subprocess.Popen(
            ["asterisk", "-f", "-C", str(config)],
            stdout=asterisk_output, stderr=subprocess.STDOUT, text=True, env=env)
        try:
            wait_until(lambda: ami_action(manager_port, {"Action": "Ping"})
                       .get("Response") == "Success", timeout=20, process=process,
                       stage="AMI startup", log_path=asterisk_log)
            # Initial apply and its ordinary timer are both fenced.
            time.sleep(1.5)
            require(registrar.count() == 0, "ordinary timer emitted REGISTER while fenced")

            nonce = "a" * 32
            write_permit(rundir, nonce, run_id)
            queued = ami_action(manager_port, {
                "Action": "PJSIPRegister", "Registration": "volte_ims",
                "MDDPermitNonce": nonce})
            require(queued.get("Response") == "Success", "permit AMI request failed")
            receipt = wait_until(
                lambda: (rundir / admission_gate.REGISTRATION_DISPATCH_NAME).is_file(),
                process=process, stage="permit receipt", log_path=asterisk_log)
            require(receipt and wait_until(lambda: registrar.count() == 1, process=process,
                                           stage="permitted REGISTER", log_path=asterisk_log),
                    "exact permit did not produce one loopback REGISTER")
            time.sleep(0.5)
            require(registrar.count() == 1, "one permitted dispatch produced repeated REGISTER")
            ami_action(manager_port, {"Action": "PJSIPRegister", "Registration": "volte_ims",
                                      "MDDPermitNonce": nonce})
            time.sleep(0.5)
            require(registrar.count() == 1, "permit replay produced a second REGISTER")

            # A new campaign can consume its receipt, but a busy P-CSCF lock forbids send.
            (rundir / admission_gate.REGISTRATION_DISPATCH_NAME).unlink()
            nonce2 = "c" * 32
            write_permit(rundir, nonce2, run_id)
            pcscf = (rundir / ".pcscf-rebind.lock").open("a+")
            fcntl.flock(pcscf.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
            try:
                ami_action(manager_port, {"Action": "PJSIPRegister", "Registration": "volte_ims",
                                          "MDDPermitNonce": nonce2})
                wait_until(lambda: (rundir / admission_gate.REGISTRATION_DISPATCH_NAME).is_file(),
                           process=process, stage="contended permit receipt",
                           log_path=asterisk_log)
                time.sleep(0.3)
                require(registrar.count() == 1,
                        "P-CSCF contention allowed REGISTER after receipt consumption")
            finally:
                fcntl.flock(pcscf.fileno(), fcntl.LOCK_UN)
                pcscf.close()

            rearmed = ami_action(manager_port, {
                "Action": "PJSIPRegister", "Registration": "volte_ims",
                "MDDRearmOnly": "true"})
            require(rearmed.get("Response") == "Success"
                    and rearmed.get("MDDTimerId")
                    and rearmed.get("SentRegister", "").casefold() == "false",
                    "timer-only rearm receipt is incomplete")
            # Let at least two one-second callbacks collide with the still-durable fence. Each
            # must consume zero network side effects while retaining exactly one successor.
            time.sleep(2.3)
            require(registrar.count() == 1, "timer-only rearm sent REGISTER while fenced")
            (rundir / admission_gate.REGISTRATION_DISPATCH_NAME).unlink()
            fence.unlink()
            wait_until(lambda: registrar.count() == 2, timeout=2.5, process=process,
                       stage="post-fence deferred REGISTER", log_path=asterisk_log)
            require(registrar.count() == 2,
                    "repeated fenced timer callbacks lost or duplicated the deferred REGISTER")
        finally:
            active_exception = sys.exc_info()[0] is not None
            if process is not None and process.poll() is None:
                process.send_signal(signal.SIGTERM)
                try:
                    process.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait(timeout=3)
            asterisk_output.close()
            if (process is not None and process.returncode not in (0, -signal.SIGTERM)
                    and not active_exception):
                output = asterisk_log.read_text(errors="replace")
                raise AssertionError(
                    f"Asterisk exited rc={process.returncode} during cleanup:\n{output[-12000:]}")
            if service is not None:
                service.stop()
            registrar.close()
    print("ASTERISK_REGISTRATION_FENCE_LINUX_E2E_PASS")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
