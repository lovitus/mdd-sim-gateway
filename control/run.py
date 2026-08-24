#!/usr/bin/env python3
"""
run.py - Launch the control surface over HTTPS.

Uses the configured TLS cert/key if present, otherwise auto-generates a self-signed
cert (stored under $MDD_DATA/certs). Runs the FastAPI app with uvicorn.

Env:
  MDD_DATA      data dir (default ./data)
  MDD_HTTP_PORT override listen port (default from config.settings.http_port)
  MDD_BIND      override bind address (default from config.settings.bind)
"""
import asyncio
import datetime
import ipaddress
import os
import signal
import socket
from contextlib import contextmanager

try:
    from app import config as cfg
    from app import control_lifecycle
except ModuleNotFoundError:  # Imported as control.run by tests and tooling.
    from .app import config as cfg
    from .app import control_lifecycle


UVICORN_GRACEFUL_SHUTDOWN_SECONDS = 2.0


def _runtime_path(path):
    """Translate persisted Docker-mode /data paths when the control plane runs natively."""
    path = str(path or "")
    if path.startswith("/data/") and os.path.abspath(cfg.DATA_DIR) != "/data":
        translated = os.path.join(cfg.DATA_DIR, os.path.relpath(path, "/data"))
        if os.path.exists(translated):
            return translated
    return path


def _self_signed(cert_path, key_path):
    from cryptography import x509
    from cryptography.hazmat.primitives import hashes, serialization
    from cryptography.hazmat.primitives.asymmetric import rsa
    from cryptography.x509.oid import NameOID

    key = rsa.generate_private_key(public_exponent=65537, key_size=2048)
    name = x509.Name([x509.NameAttribute(NameOID.COMMON_NAME, u"mdd-sim-gateway")])
    san = [x509.DNSName(u"localhost")]
    try:
        host_ip = socket.gethostbyname(socket.gethostname())
        san.append(x509.IPAddress(ipaddress.ip_address(host_ip)))
    except Exception:
        pass
    # Optional *plural* TLS SAN hints only. WebRTC media selection never consumes these values:
    # each authenticated browser session is bound to current host inventory independently.
    for adv in os.environ.get("MDD_TLS_SAN_IPS", "").replace(",", " ").split():
        try:
            san.append(x509.IPAddress(ipaddress.ip_address(adv)))
        except Exception:
            pass
    san.append(x509.IPAddress(ipaddress.ip_address(u"127.0.0.1")))
    cert = (
        x509.CertificateBuilder()
        .subject_name(name).issuer_name(name)
        .public_key(key.public_key())
        .serial_number(x509.random_serial_number())
        .not_valid_before(datetime.datetime.utcnow() - datetime.timedelta(days=1))
        .not_valid_after(datetime.datetime.utcnow() + datetime.timedelta(days=3650))
        .add_extension(x509.SubjectAlternativeName(san), critical=False)
        .sign(key, hashes.SHA256())
    )
    os.makedirs(os.path.dirname(cert_path), exist_ok=True)
    with open(key_path, "wb") as f:
        f.write(key.private_bytes(serialization.Encoding.PEM,
                                  serialization.PrivateFormat.TraditionalOpenSSL,
                                  serialization.NoEncryption()))
    with open(cert_path, "wb") as f:
        f.write(cert.public_bytes(serialization.Encoding.PEM))


def _coordinated_exit(servers, sig, _frame) -> None:
    # This must be the first operation: Uvicorn closes WebSockets before ASGI lifespan shutdown.
    control_lifecycle.begin_shutdown()
    force_exit = sig == signal.SIGINT and any(server.should_exit for server in servers)
    for server in servers:
        server.should_exit = True
        if force_exit:
            server.force_exit = True


async def _run_dual(bind, https_port, http_port, cert_path, key_path, run_https=True, run_http=True):
    import uvicorn

    class CoordinatedServer(uvicorn.Server):
        @contextmanager
        def capture_signals(self):
            # One process-level handler below coordinates both HTTP and HTTPS servers.
            yield

    servers = []
    if run_https and https_port:
        cfg_https = uvicorn.Config("app.main:app", host=bind, port=https_port,
                                   ssl_certfile=cert_path, ssl_keyfile=key_path,
                                   log_level="info",
                                   timeout_graceful_shutdown=(
                                       UVICORN_GRACEFUL_SHUTDOWN_SECONDS))
        srv_https = CoordinatedServer(cfg_https)
        servers.append(srv_https)
        print(f"[run] serving https://{bind}:{https_port} (HTTPS + WSS)")

    if run_http and http_port and (not run_https or http_port != https_port):
        cfg_http = uvicorn.Config("app.main:app", host=bind, port=http_port,
                                  log_level="info",
                                  timeout_graceful_shutdown=(
                                      UVICORN_GRACEFUL_SHUTDOWN_SECONDS))
        srv_http = CoordinatedServer(cfg_http)
        servers.append(srv_http)
        print(f"[run] serving http://{bind}:{http_port} (plain HTTP + WS for Nginx upstream / debug)")

    if servers:
        control_lifecycle.reset_for_startup()
        handled_signals = [signal.SIGINT, signal.SIGTERM]
        if hasattr(signal, "SIGBREAK"):
            handled_signals.append(signal.SIGBREAK)
        original_handlers = {
            sig: signal.signal(sig, lambda caught, frame: _coordinated_exit(
                servers, caught, frame))
            for sig in handled_signals
        }
        try:
            await asyncio.gather(*(server.serve() for server in servers))
        finally:
            for sig, handler in original_handlers.items():
                signal.signal(sig, handler)
            print("[run] coordinated servers returned", flush=True)


def main():
    import asyncio
    import uvicorn

    # Runtime state contains SIM identities, PINs and service credentials. Keep every file
    # created by the control process private even if the host/container has a permissive umask.
    os.umask(0o077)

    settings = cfg.get_settings()
    tls = settings.get("tls", {})
    https_port = int(os.environ.get("MDD_HTTPS_PORT", os.environ.get("MDD_HTTP_PORT", settings.get("http_port", 8443))))
    http_port = int(os.environ.get("MDD_PLAIN_PORT", os.environ.get("MDD_HTTP_PLAIN_PORT", settings.get("http_plain_port", 8000))))
    bind = os.environ.get("MDD_BIND", settings.get("bind", "0.0.0.0"))

    tls_enabled = tls.get("enable", True)
    if os.environ.get("MDD_TLS", "").lower() in ("0", "false", "no", "off") or \
            os.environ.get("MDD_HTTP_ONLY", "").lower() in ("1", "true", "yes"):
        tls_enabled = False

    if not tls_enabled:
        asyncio.run(_run_dual(bind, 0, https_port, "", "",
                              run_https=False, run_http=True))
        print("[run] asyncio loop closed", flush=True)
        return

    configured_cert = _runtime_path(tls.get("cert_path"))
    configured_key = _runtime_path(tls.get("key_path"))
    if configured_cert and os.path.exists(configured_cert) and \
            configured_key and os.path.exists(configured_key):
        cert_path, key_path = configured_cert, configured_key
    else:
        cert_path = os.path.join(cfg.DATA_DIR, "certs", "self-signed.crt")
        key_path = os.path.join(cfg.DATA_DIR, "certs", "self-signed.key")
        if not (os.path.exists(cert_path) and os.path.exists(key_path)):
            print("[run] generating self-signed certificate...")
            _self_signed(cert_path, key_path)

    asyncio.run(_run_dual(bind, https_port, http_port, cert_path, key_path,
                          run_https=True, run_http=True))
    print("[run] asyncio loop closed", flush=True)


if __name__ == "__main__":
    main()
