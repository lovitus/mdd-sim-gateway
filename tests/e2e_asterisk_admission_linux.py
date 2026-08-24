#!/usr/bin/env python3
"""Disposable, no-SIM validation for the patched Asterisk admission boundary.

Run only in a throwaway Linux build container after ``make install``.  It proves the actual
loaded module, dialplan function, incoming MESSAGE pre-202 behavior and AMI MessageSend final
submit without placing a carrier call or sending a real SMS.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import signal
import socket
import subprocess
import sys
import time
from pathlib import Path


RUN_ID = "11111111-2222-4333-8444-555555555555"


def wait_for(predicate, reason: str, timeout: float = 10.0):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        value = predicate()
        if value:
            return value
        time.sleep(0.05)
    raise RuntimeError(f"timeout waiting for {reason}")


def authority(seq: int) -> dict:
    engine = {
        "container_id": "b" * 64,
        "image_id": "sha256:" + "c" * 64,
        "started_at": "2026-08-23T00:00:00.000000000Z",
        "restart_count": 0,
        "run_id": RUN_ID,
    }
    return {
        "version": 1,
        "protocol": "mdd-admission-v1",
        "mode": "normal_committed",
        "iid": "7",
        "issuer_boot_id": "a" * 32,
        "authority_epoch": 1,
        "lease_seq": seq,
        "engine": engine,
        "engine_generation_digest": hashlib.sha256(json.dumps(
            engine, sort_keys=True, separators=(",", ":")).encode()).hexdigest(),
        "maintenance": None,
        "normal": {"commit_id": "d" * 32, "state_digest": "e" * 64},
    }


def atomic_authority(path: Path, seq: int) -> None:
    temporary = path.with_suffix(".tmp")
    temporary.write_text(json.dumps(authority(seq), sort_keys=True), encoding="utf-8")
    temporary.chmod(0o600)
    temporary.replace(path)


def cli(config: Path, *command: str) -> subprocess.CompletedProcess:
    return subprocess.run(["asterisk", "-C", str(config), "-rx", " ".join(command)],
                          text=True, capture_output=True, timeout=5)


def sip_message(port: int, call_id: str, *, body: bytes = b"admission-test",
                content_type: str = "text/plain") -> str:
    headers = (
        "MESSAGE sip:100@127.0.0.1 SIP/2.0\r\n"
        f"Via: SIP/2.0/UDP 127.0.0.1:{port};branch=z9hG4bK-{call_id}\r\n"
        "Max-Forwards: 70\r\n"
        f"From: <sip:sender@127.0.0.1:{port}>;tag=sender-{call_id}\r\n"
        "To: <sip:100@127.0.0.1>\r\n"
        f"Call-ID: {call_id}\r\n"
        "CSeq: 1 MESSAGE\r\n"
        f"Content-Type: {content_type}\r\n"
        f"Content-Length: {len(body)}\r\n\r\n"
    ).encode("ascii")
    request = headers + body
    with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as client:
        client.bind(("127.0.0.1", port))
        client.settimeout(3)
        client.sendto(request, ("127.0.0.1", 5060))
        deadline = time.monotonic() + 3
        while time.monotonic() < deadline:
            data, _ = client.recvfrom(8192)
            decoded = data.decode("ascii", errors="replace")
            # RP-DATA can also cause a new outbound RP-ACK MESSAGE to this source port. The
            # admission assertion concerns the stateful response to our original request.
            if decoded.startswith("SIP/2.0 "):
                return decoded
    raise RuntimeError(f"no SIP response for {call_id}")


def ami_action(fields: dict[str, str]) -> str:
    with socket.create_connection(("127.0.0.1", 5038), timeout=3) as client:
        client.settimeout(3)
        client.recv(4096)
        login = "Action: Login\r\nUsername: gate\r\nSecret: gate\r\nEvents: off\r\n\r\n"
        client.sendall(login.encode("ascii"))
        response = client.recv(4096).decode("utf-8", errors="replace")
        if "Response: Success" not in response:
            raise RuntimeError(f"AMI login failed: {response!r}")
        frame = "".join(f"{key}: {value}\r\n" for key, value in fields.items()) + "\r\n"
        client.sendall(frame.encode("utf-8"))
        return client.recv(8192).decode("utf-8", errors="replace")


def _sip_header(decoded: str, name: str) -> str:
    prefix = name.lower() + ":"
    for line in decoded.splitlines():
        if line.lower().startswith(prefix):
            return line
    raise RuntimeError(f"missing SIP header {name}")


def expect_one_message_packet(sink: socket.socket, body: bytes, reason: str) -> None:
    previous_timeout = sink.gettimeout()
    sink.settimeout(2.0)
    try:
        packet, address = sink.recvfrom(8192)
        if b"MESSAGE " not in packet:
            raise RuntimeError(f"{reason} did not emit a SIP MESSAGE packet")
        if b"\r\n\r\n" in packet:
            packet_body = packet.split(b"\r\n\r\n", 1)[1]
        elif b"\n\n" in packet:
            packet_body = packet.split(b"\n\n", 1)[1]
        else:
            raise RuntimeError(f"{reason} emitted a SIP MESSAGE without a body separator")
        if packet_body != body:
            raise RuntimeError(f"{reason} emitted unexpected body: {packet_body[:120]!r}")

        decoded = packet.decode("utf-8", errors="replace")
        response = (
            "SIP/2.0 200 OK\r\n"
            f"{_sip_header(decoded, 'Via')}\r\n"
            f"{_sip_header(decoded, 'From')}\r\n"
            f"{_sip_header(decoded, 'To')}\r\n"
            f"{_sip_header(decoded, 'Call-ID')}\r\n"
            f"{_sip_header(decoded, 'CSeq')}\r\n"
            "Content-Length: 0\r\n\r\n"
        )
        sink.sendto(response.encode("utf-8"), address)

        sink.settimeout(1.0)
        try:
            extra, _ = sink.recvfrom(8192)
            raise RuntimeError(f"{reason} emitted an extra SIP packet: {extra[:120]!r}")
        except socket.timeout:
            pass
    finally:
        sink.settimeout(previous_timeout)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--root", required=True)
    parser.add_argument("--admission-gate", required=True)
    parser.add_argument("--module-dir", required=True)
    parser.add_argument("--data-dir", default="/var/lib/asterisk")
    args = parser.parse_args()
    root = Path(args.root)
    etc = root / "etc"
    run = root / "run"
    logs = root / "logs"
    var = root / "var"
    for path in (etc, run, logs, var):
        path.mkdir(parents=True, exist_ok=True)
    hits = root / "message-hits"
    asterisk_log = root / "asterisk.log"
    gate_log = root / "gate.log"
    config = etc / "asterisk.conf"
    config.write_text(f"""[directories]
astetcdir => {etc}
astmoddir => {args.module_dir}
astvarlibdir => {var}
astdbdir => {var}
astkeydir => {var}
astdatadir => {args.data_dir}
astagidir => {var}
astspooldir => {var}
astrundir => {run}
astlogdir => {logs}
astsbindir => /usr/sbin
[options]
verbose = 3
""")
    (etc / "modules.conf").write_text("[modules]\nautoload=yes\n")
    (etc / "logger.conf").write_text(
        "[general]\ndateformat=%Y-%m-%d %H:%M:%S\n"
        "[logfiles]\nconsole => debug,notice,warning,error\n")
    (etc / "manager.conf").write_text("""[general]
enabled=yes
webenabled=no
port=5038
bindaddr=127.0.0.1
[gate]
secret=gate
read=all
write=all
""")
    (etc / "pjsip.conf").write_text("""[transport-udp]
type=transport
protocol=udp
bind=127.0.0.1:5060
[test]
type=endpoint
transport=transport-udp
context=test
message_context=test_msg
disallow=all
allow=ulaw
aors=test
[test]
type=aor
contact=sip:test@127.0.0.1:5099
[local-identify]
type=identify
endpoint=test
match=127.0.0.1
[volte_ims]
type=endpoint
transport=transport-udp
context=test
message_context=test_msg
disallow=all
allow=ulaw
aors=volte_ims
[volte_ims]
type=aor
contact=sip:carrier@127.0.0.1:5099
""")
    (etc / "extensions.conf").write_text(f"""[test]
exten => 100,1,GotoIf($[\"${{MDD_ADMISSION(call_in)}}\" != \"ALLOW\"]?blocked)
same => n,Progress()
same => n,Ringing()
same => n,Hangup()
same => n(blocked),Hangup(41)
[test_msg]
; Delay consumption beyond the three-second authority TTL. Once PJSIP has committed the
; request with 202, removing authority must not cause a second dialplan rejection.
exten => _.,1,Wait(3.6)
same => n,Set(FILE({hits},,,a)=hit)
same => n,Hangup()
""")

    environment = dict(os.environ, MDD_ID="7", MDD_ENGINE_RUN_ID=RUN_ID,
                       MDD_ADMISSION_SOCKET=str(run / "admission-gate.sock"))
    gate_output = gate_log.open("w")
    asterisk_output = asterisk_log.open("w")
    gate = subprocess.Popen([
        sys.executable, args.admission_gate, "--rundir", str(run), "--iid", "7",
        "--engine-run-id", RUN_ID, "serve",
    ], env=environment, stdout=gate_output, stderr=subprocess.STDOUT)
    asterisk = None
    try:
        wait_for((run / "admission-gate.sock").exists, "gate socket")
        asterisk = subprocess.Popen(["asterisk", "-f", "-C", str(config)],
                                    env=environment, stdout=asterisk_output,
                                    stderr=subprocess.STDOUT)
        wait_for(lambda: cli(config, "core", "show", "uptime").returncode == 0,
                 "Asterisk control socket", timeout=20)
        modules = wait_for(
            lambda: (result if (result := cli(
                config, "module", "show", "like", "res_mdd_admission")).returncode == 0
                and "res_mdd_admission.so" in result.stdout else None),
            "admission module loaded", timeout=20)
        cli(config, "core", "set", "debug", "5")
        denied_function = cli(config, "dialplan", "eval", "function",
                              "MDD_ADMISSION(call_in)")
        if "DENY" not in denied_function.stdout:
            raise RuntimeError(f"cold function did not deny: {denied_function.stdout!r}")

        denied = sip_message(5098, "deny-1")
        if "SIP/2.0 503" not in denied or hits.exists():
            raise RuntimeError(f"cold MESSAGE was not rejected pre-queue: {denied!r}")

        # RP transaction completions are not new billable/persistent work. They must finish
        # under an expired authority, preserve one exact RPDU evidence line and queue nothing.
        rp_ack = bytes.fromhex("03 11")
        rp_error = bytes.fromhex("05 12 01 16")
        for call_id, body in (("ack-cold", rp_ack), ("error-cold", rp_error)):
            response = sip_message(
                5098, call_id, body=body,
                content_type="application/vnd.3gpp.sms")
            if "SIP/2.0 200" not in response:
                raise RuntimeError(f"completion was not accepted: {call_id} {response!r}")
        for call_id, body in (
                ("ack-truncated", bytes.fromhex("03")),
                ("ack-trailing", bytes.fromhex("03 11 00")),
                ("rp-unknown", bytes.fromhex("02 11")),
                ("rp-oversize", b"\x03\x11" + b"x" * 247)):
            response = sip_message(
                5098, call_id, body=body,
                content_type="application/vnd.3gpp.sms")
            if "SIP/2.0 400" not in response:
                raise RuntimeError(f"invalid RPDU was not rejected: {call_id} {response!r}")
        if hits.exists():
            raise RuntimeError("completion or invalid RPDU reached the user message queue")
        for body in (rp_ack, rp_error):
            encoded = body.hex()
            wait_for(lambda encoded=encoded: asterisk_log.read_text(
                encoding="utf-8", errors="replace").count(
                    f"SMS RP-DATA '{encoded}'.") == 1,
                f"one exact RPDU evidence line for {encoded}")

        authority_path = run / "admission-authority.json"
        atomic_authority(authority_path, 1)
        time.sleep(0.2)
        atomic_authority(authority_path, 2)
        wait_for(lambda: "ALLOW" in cli(config, "dialplan", "eval", "function",
                                         "MDD_ADMISSION(call_in)").stdout,
                 "Asterisk ALLOW")
        accepted = sip_message(5098, "allow-1")
        if "SIP/2.0 202" not in accepted:
            raise RuntimeError(f"authorized MESSAGE was not accepted: {accepted!r}")
        authority_path.unlink()
        wait_for(lambda: hits.exists() and hits.read_text().count("hit") == 1,
                 "one committed MESSAGE after authority removal")

        # Missing authority after the committed MT request remains fail-closed for new work.
        expired = cli(config, "dialplan", "eval", "function", "MDD_ADMISSION(sms_out)")
        if "DENY" not in expired.stdout:
            raise RuntimeError("same sequence refreshed an expired gate")
        with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as sink:
            sink.bind(("127.0.0.1", 5099))
            sink.settimeout(0.4)
            denied_ami = ami_action({
                "Action": "MessageSend",
                "To": "pjsip:volte_ims/123@127.0.0.1:5099",
                "From": "sip:test@localhost", "Body": "denied",
            })
            try:
                sink.recvfrom(8192)
                raise RuntimeError("expired AMI MessageSend emitted a SIP packet")
            except socket.timeout:
                pass
            if "Response: Error" not in denied_ami:
                raise RuntimeError(f"expired AMI action was not rejected: {denied_ami!r}")

            denied_domain_ami = ami_action({
                "Action": "MessageSend",
                "To": "pjsip:volte_ims@example.invalid",
                "From": "sip:test@localhost", "Body": "denied-domain",
            })
            try:
                sink.recvfrom(8192)
                raise RuntimeError(
                    "expired endpoint@domain AMI MessageSend emitted a SIP packet")
            except socket.timeout:
                pass
            if "Response: Error" not in denied_domain_ami:
                raise RuntimeError(
                    f"expired endpoint@domain AMI action was not rejected: {denied_domain_ami!r}")

            non_ims_ami = ami_action({
                "Action": "MessageSend",
                "To": "pjsip:test@example.invalid",
                "From": "sip:test@localhost", "Body": "non-ims",
            })
            if "Response: Success" not in non_ims_ami:
                raise RuntimeError(f"non-IMS endpoint was blocked: {non_ims_ami!r}")
            expect_one_message_packet(sink, b"non-ims", "non-IMS endpoint")

            atomic_authority(authority_path, 3)
            time.sleep(0.2)
            atomic_authority(authority_path, 4)
            wait_for(lambda: "ALLOW" in cli(config, "dialplan", "eval", "function",
                                             "MDD_ADMISSION(sms_out)").stdout,
                     "renewed Asterisk ALLOW")
            allowed_ami = ami_action({
                "Action": "MessageSend",
                "To": "pjsip:volte_ims/123@127.0.0.1:5099",
                "From": "sip:test@localhost", "Body": "allowed",
            })
            if "Response: Success" not in allowed_ami:
                raise RuntimeError(f"authorized AMI action was not accepted: {allowed_ami!r}")
            expect_one_message_packet(sink, b"allowed", "authorized AMI MessageSend")

            allowed_domain_ami = ami_action({
                "Action": "MessageSend",
                "To": "pjsip:volte_ims@example.invalid",
                "From": "sip:test@localhost", "Body": "allowed-domain",
            })
            if "Response: Success" not in allowed_domain_ami:
                raise RuntimeError(
                    f"authorized endpoint@domain AMI action was not accepted: {allowed_domain_ami!r}")
            expect_one_message_packet(
                sink, b"allowed-domain", "authorized endpoint@domain AMI MessageSend")

        print(json.dumps({
            "ok": True, "module_loaded": True, "cold_message": 503,
            "warm_message": 202, "queued_messages": 1,
            "completion_status": 200, "completion_evidence_lines": 2,
            "invalid_rpdu_status": 400, "completion_queue_side_effects": 0,
            "expired_ami_packets": 0, "expired_domain_ami_packets": 0,
            "non_ims_ami_packets": 1, "warm_ami_packets": 1,
            "warm_domain_ami_packets": 1,
        }, sort_keys=True))
        return 0
    finally:
        if asterisk is not None and asterisk.poll() is None:
            asterisk.send_signal(signal.SIGTERM)
            try:
                asterisk.wait(timeout=8)
            except subprocess.TimeoutExpired:
                asterisk.kill()
                asterisk.wait()
        if gate.poll() is None:
            gate.send_signal(signal.SIGTERM)
            try:
                gate.wait(timeout=3)
            except subprocess.TimeoutExpired:
                gate.kill()
                gate.wait()
        gate_output.close()
        asterisk_output.close()


if __name__ == "__main__":
    raise SystemExit(main())
