"""Real production ASGI routing, including nested StaticFiles under /mdd."""

import asyncio
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
from unittest.mock import patch


async def _probe_application():
    import httpx
    from starlette.responses import JSONResponse
    from starlette.routing import Route
    from control.app import main

    ui = Path(os.environ["MDD_WEBUI"])
    expected_index = (ui / "index.html").read_bytes()
    expected_files = [path for path in ui.rglob("*") if path.is_file()]
    assert any(path.suffix == ".js" for path in expected_files)
    assert any(path.suffix == ".css" for path in expected_files)
    verified = []

    async def protected_probe(request):
        return JSONResponse({"authenticated": bool(request.state.admin_session),
                             "root_path": request.scope.get("root_path", ""),
                             "path": request.scope["path"],
                             "raw_path": request.scope.get("raw_path", b"").decode(),
                             "query": request.scope.get("query_string", b"").decode(),
                             "asset_url": str(request.url_for("assets", path="probe.js"))})

    # Side-effect-free endpoints exercise the actual production authentication/CSRF/fence
    # middleware. The production StaticFiles mount and SPA handlers remain unchanged.
    for path in ("/api/context-probe", "/api/instances/5/context-probe",
                 "/api/instances/5/cellular-call/owned/release"):
        main.app.router.routes.insert(0, Route(path, protected_probe, methods=["GET", "POST"]))

    state = {"global": False, "line": False}
    audit_records = []
    session = lambda token: {"csrf": "test-csrf"} if token == "test-session" else None
    with patch.object(main.auth, "session", session), \
            patch.object(main.auth, "configured", return_value=True), \
            patch.object(main.auth, "username", return_value="test-user"), \
            patch.object(main, "_write_audit_record", lambda record, settings: audit_records.append(record)), \
            patch.object(main.engine, "global_maintenance_pending", lambda: state["global"]), \
            patch.object(main.engine, "engine_maintenance_pending", lambda iid: state["line"] and iid == "5"):
        async with httpx.AsyncClient(transport=httpx.ASGITransport(app=main.app),
                                    base_url="https://gateway.test", follow_redirects=False) as client:
            for prefix in ("", "/mdd"):
                audit_start = len(audit_records)
                index = await client.get(prefix + "/")
                assert index.status_code == 200 and index.content == expected_index
                for path in expected_files:
                    relative = path.relative_to(ui).as_posix()
                    # index.html is served by the production SPA; assets by real StaticFiles.
                    response = await client.get(prefix + "/" + relative)
                    expected_hash = hashlib.sha256(path.read_bytes()).hexdigest()
                    assert response.status_code == 200, (prefix, relative, response.status_code)
                    assert hashlib.sha256(response.content).hexdigest() == expected_hash, relative
                    verified.append({"path": prefix + "/" + relative, "sha256": expected_hash})
                for suffix in ("assets/not-a-real-file.js", "assets/%2e%2e/index.html"):
                    assert (await client.get(prefix + "/" + suffix)).status_code == 404

                public = await client.get(prefix + "/api/auth/status")
                assert public.status_code == 200 and public.json()["authenticated"] is False
                assert (await client.get(prefix + "/api/context-probe")).status_code == 401
                headers = {"authorization": "Bearer test-session"}
                assert (await client.post(prefix + "/api/context-probe", headers=headers)).status_code == 403
                headers["x-mdd-csrf-token"] = "test-csrf"
                success = await client.post(prefix + "/api/context-probe?q=%2F", headers=headers)
                assert success.status_code == 200, success.text
                assert success.json() == {
                    "authenticated": True, "root_path": prefix,
                    "path": prefix + "/api/context-probe", "raw_path": prefix + "/api/context-probe",
                    "query": "q=%2F", "asset_url": "https://gateway.test" + prefix + "/assets/probe.js"}
                assert (await client.get(prefix + "/api/not-a-real-api", headers=headers)).status_code == 404
                state["global"] = True
                blocked = await client.post(prefix + "/api/context-probe", headers=headers)
                assert blocked.status_code == 503 and blocked.json()["detail"]["code"] == "maintenance_in_progress"
                # Maintenance still allows authenticated Hangup/release, never bypassing CSRF.
                release = prefix + "/api/instances/5/cellular-call/owned/release"
                assert (await client.post(release, headers=headers)).status_code == 200
                assert (await client.post(release, headers={"authorization": "Bearer test-session"})).status_code == 403
                state["global"] = False
                state["line"] = True
                assert (await client.post(prefix + "/api/instances/5/context-probe", headers=headers)).status_code == 503
                state["line"] = False
                assert [{"path": record["path"], "status": record["status"]}
                        for record in audit_records[audit_start:]] == [
                    {"path": "/api/context-probe", "status": 403},
                    {"path": "/api/context-probe", "status": 200},
                    {"path": "/api/context-probe", "status": 503},
                    {"path": "/api/instances/5/cellular-call/owned/release", "status": 200},
                    {"path": "/api/instances/5/cellular-call/owned/release", "status": 403},
                    {"path": "/api/instances/5/context-probe", "status": 503},
                ], f"canonical mutation audit changed for {prefix or '/'}"

            bare = await client.get("/mdd?q=%2F")
            assert bare.status_code == 307
            assert bare.headers["location"] == "https://gateway.test/mdd/?q=%2F"
            assert (await client.get(bare.headers["location"])).content == expected_index
            # A lookalike prefix is an ordinary SPA route, not a second static mount.
            near = await client.get("/mdd-other/assets/not-a-real-file.js")
            assert near.content == expected_index

        websocket_results = []
        for prefix in ("", "/mdd"):
            for authenticated in (False, True):
                events = asyncio.Queue()
                await events.put({"type": "websocket.connect"})
                await events.put({"type": "websocket.disconnect", "code": 1000})
                sent = []
                path = prefix + "/ws"
                scope = {"type": "websocket", "asgi": {"version": "3.0", "spec_version": "2.4"},
                         "scheme": "wss", "path": path, "raw_path": path.encode(), "root_path": "",
                         "query_string": b"auth_close=1", "server": ("gateway.test", 443),
                         "client": ("127.0.0.1", 12345), "subprotocols": [],
                         "headers": [(b"host", b"gateway.test"),
                                     (b"cookie", f"{main.auth.SESSION_COOKIE}={('test-session' if authenticated else 'invalid')}".encode())]}

                async def send(message):
                    sent.append(message)

                await asyncio.wait_for(main.app(scope, events.get, send), 2)
                assert sent[0]["type"] == "websocket.accept"
                if not authenticated:
                    assert sent[-1]["type"] == "websocket.close" and sent[-1]["code"] == 4401
                else:
                    assert not any(item["type"] == "websocket.close" for item in sent)
                assert not main.hub.clients
                assert scope["path"] == path and scope["raw_path"] == path.encode()
                websocket_results.append({"path": path, "authenticated": authenticated})
        return {"files": verified, "websockets": websocket_results,
                "bare_prefix_redirect": 307, "canonical_audit_records": len(audit_records),
                "auth_csrf_and_maintenance": "passed"}


def test_real_application_context_prefix_preserves_static_api_and_websocket_semantics(tmp_path):
    # main mounts StaticFiles at import time. A fresh interpreter exercises the real app with
    # the frozen build without reloading or corrupting another test's module-global Hub.
    root = Path(__file__).resolve().parents[1]
    result = subprocess.run([sys.executable, str(Path(__file__).resolve()), "--probe"],
                            cwd=root, capture_output=True, text=True, timeout=25,
                            env={**os.environ, "MDD_WEBUI": str(root / "webui" / "dist"),
                                 "MDD_DATA": str(tmp_path), "PYTHONPATH": str(root),
                                 "PYTHONDONTWRITEBYTECODE": "1"})
    assert result.returncode == 0, result.stdout + result.stderr
    report = json.loads(result.stdout)
    assert len(report["files"]) >= 8 and len(report["websockets"]) == 4
    assert report["bare_prefix_redirect"] == 307
    assert report["canonical_audit_records"] == 12
    assert report["auth_csrf_and_maintenance"] == "passed"


if __name__ == "__main__":
    assert sys.argv[1:] == ["--probe"]
    print(json.dumps(asyncio.run(_probe_application())))
