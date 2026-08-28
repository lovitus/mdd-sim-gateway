"""One-click self-update: control-plane request publishing + host updater file handling."""
import importlib.util
import hashlib
import json
import os
import shutil
import tarfile
import tempfile
import threading
import time
import unittest
from types import SimpleNamespace
import requests
from pathlib import Path
from unittest.mock import patch

from control.app import config, update_check
from host import mdd_orchestrator

_SPEC = importlib.util.spec_from_file_location(
    "mdd_update", Path(__file__).resolve().parent.parent / "host" / "mdd_update.py")
mdd_update = importlib.util.module_from_spec(_SPEC)
_SPEC.loader.exec_module(mdd_update)

_AVAILABLE = {"ok": True, "update_available": True, "latest": "9.9.9",
              "release_url": "https://example.invalid/release"}


class RequestApplyTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        patcher = patch.object(config, "DATA_DIR", self.tmp.name)
        patcher.start()
        self.addCleanup(patcher.stop)
        self.request_path = os.path.join(self.tmp.name, "orchestrator", "update-request.json")
        self.status_path = os.path.join(self.tmp.name, "orchestrator", "update-status.json")

    def test_request_is_published_with_version_and_repository(self):
        available = {**_AVAILABLE, "network": {
            "proxy_mode": "library", "proxy_profile_id": "primary"}}
        with patch.object(update_check, "check", return_value=available):
            result = update_check.request_apply()
        self.assertTrue(result["ok"])
        with open(self.request_path, encoding="utf-8") as handle:
            request = json.load(handle)
        self.assertEqual(request["version"], "9.9.9")
        self.assertEqual(request["repository"], update_check.repository())
        self.assertEqual(request["network"]["proxy_profile_id"], "primary")
        with open(self.status_path, encoding="utf-8") as handle:
            status = json.load(handle)
        self.assertEqual(status["state"], "running")
        self.assertEqual(status["phase"], "requested")

    def test_no_available_update_publishes_nothing(self):
        with patch.object(update_check, "check",
                          return_value={"ok": True, "update_available": False}):
            result = update_check.request_apply()
        self.assertFalse(result["ok"])
        self.assertFalse(os.path.exists(self.request_path))

    def test_running_update_is_not_requested_twice(self):
        os.makedirs(os.path.dirname(self.status_path))
        with open(self.status_path, "w", encoding="utf-8") as handle:
            json.dump({"state": "running", "phase": "reloading",
                       "updated_at": int(time.time())}, handle)
        with patch.object(update_check, "check", return_value=dict(_AVAILABLE)):
            result = update_check.request_apply()
        self.assertEqual(result["error_code"], "update.error.in_progress")
        self.assertFalse(os.path.exists(self.request_path))

    def test_unconsumed_request_is_reported_as_stalled(self):
        os.makedirs(os.path.dirname(self.request_path))
        with open(self.request_path, "w", encoding="utf-8") as handle:
            json.dump({"version": "9.9.9", "requested_at": int(time.time()) - 300}, handle)
        status = update_check.apply_status()
        self.assertTrue(status["requested"])
        self.assertEqual(status["state"], "stalled")


class UpdaterTests(unittest.TestCase):
    def test_browser_outbound_abi_is_required_for_default_and_running_engines(self):
        with tempfile.TemporaryDirectory() as tmp:
            source = Path(tmp)
            (source / "engine").mkdir()
            (source / "install.sh").write_text(
                'ENGINE_BROWSER_OUTBOUND_ABI="mdd-browser-outbound-v1"\n',
                encoding="utf-8")
            (source / "engine/Dockerfile").write_text(
                'LABEL io.mdd-sim-gateway.browser-outbound="mdd-browser-outbound-v1"\n',
                encoding="utf-8")
            with patch.object(mdd_update, "_docker_image_label", return_value=""), \
                    patch.object(mdd_update, "running_engine_names", return_value=[]):
                self.assertTrue(mdd_update.engine_media_migration_required(source))
            with patch.object(mdd_update, "_docker_image_label",
                              return_value=mdd_update.ENGINE_BROWSER_OUTBOUND_ABI), \
                    patch.object(mdd_update, "running_engine_names", return_value=[]):
                self.assertFalse(mdd_update.engine_media_migration_required(source))

    def test_browser_inbound_abi_is_required_for_default_and_running_engines(self):
        with tempfile.TemporaryDirectory() as tmp:
            source = Path(tmp)
            (source / "engine").mkdir()
            (source / "install.sh").write_text(
                'ENGINE_BROWSER_INBOUND_ABI="mdd-browser-inbound-v1"\n',
                encoding="utf-8")
            (source / "engine/Dockerfile").write_text(
                'LABEL io.mdd-sim-gateway.browser-inbound="mdd-browser-inbound-v1"\n',
                encoding="utf-8")
            with patch.object(mdd_update, "_docker_image_label", return_value=""), \
                    patch.object(mdd_update, "running_engine_names", return_value=[]):
                self.assertTrue(mdd_update.engine_media_migration_required(source))
            with patch.object(mdd_update, "_docker_image_label",
                              return_value=mdd_update.ENGINE_BROWSER_INBOUND_ABI), \
                    patch.object(mdd_update, "running_engine_names", return_value=[]):
                self.assertFalse(mdd_update.engine_media_migration_required(source))

    def test_new_update_running_status_wins_old_migration_completion_race(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            data, repo = root / "data", root / "repo"
            path = data / "orchestrator" / "update-status.json"
            old = {
                "state": "action_required",
                "phase": "engine_media_migration_required",
                "target": "1.0.0",
                "engine_media_migration_required": True,
                "engine_media_websocket_abi": mdd_update.ENGINE_MEDIA_WEBSOCKET_ABI,
                "engine_browser_outbound_abi": mdd_update.ENGINE_BROWSER_OUTBOUND_ABI,
                "engine_browser_inbound_abi": mdd_update.ENGINE_BROWSER_INBOUND_ABI,
                "updated_at": 1,
            }
            mdd_update.atomic_json(path, old)
            entered, release = threading.Event(), threading.Event()

            def migration_required(_source):
                entered.set()
                self.assertTrue(release.wait(2.0))
                return False

            result = []
            with patch.object(
                    mdd_update, "engine_media_migration_required",
                    side_effect=migration_required):
                completer = threading.Thread(target=lambda: result.append(
                    mdd_update.complete_engine_media_migration_status(repo, data)))
                completer.start()
                self.assertTrue(entered.wait(1.0))
                writer = threading.Thread(target=lambda: update_check._write_update_status(
                    str(path), {"state": "running", "phase": "requested",
                                "target": "2.0.0", "updated_at": 2}))
                writer.start()
                time.sleep(0.05)
                self.assertTrue(writer.is_alive(), "new update must wait on the shared CAS lock")
                release.set()
                completer.join(2.0)
                writer.join(2.0)

            self.assertEqual(result, [True])
            current = json.loads(path.read_text(encoding="utf-8"))
            self.assertEqual(current["state"], "running")
            self.assertEqual(current["target"], "2.0.0")

    def test_reload_reuses_satisfied_python_requirements_offline(self):
        installer = (Path(__file__).resolve().parent.parent / "install.sh").read_text(
            encoding="utf-8")
        offline = 'pip" install --quiet --no-index'
        online = 'pip" install --quiet wheel -r'
        self.assertIn(offline, installer)
        self.assertIn(online, installer)
        self.assertLess(installer.index(offline), installer.index(online))
        self.assertNotIn('pip" install --quiet --upgrade pip wheel', installer)

    def test_release_archive_checksum_is_required_and_verified(self):
        with tempfile.TemporaryDirectory() as tmp:
            archive = Path(tmp, "mdd-sim-gateway-v9.9.9.tar.gz")
            archive.write_bytes(b"release")
            digest = hashlib.sha256(b"release").hexdigest()
            sums = Path(tmp, "SHA256SUMS")
            sums.write_text(f"{digest}  {archive.name}\n", encoding="utf-8")
            mdd_update.verify_release_archive(archive, sums)
            archive.write_bytes(b"changed")
            with self.assertRaises(mdd_update.UpdateError):
                mdd_update.verify_release_archive(archive, sums)

    def test_proxy_environment_is_explicit_and_not_added_to_curl_arguments(self):
        proxy = "socks5h://user:secret@127.0.0.1:1080"
        env = mdd_update.network_environment(proxy)
        self.assertEqual(env["ALL_PROXY"], proxy)
        completed = type("Completed", (), {"returncode": 0, "stderr": ""})()
        with tempfile.TemporaryDirectory() as tmp, patch.object(
                mdd_update.subprocess, "run", return_value=completed) as run:
            mdd_update.download("https://example.invalid/release.tar.gz",
                                Path(tmp, "release.tar.gz"), env, proxy)
        args = run.call_args.args[0]
        self.assertNotIn(proxy, args)
        self.assertEqual(run.call_args.kwargs["env"]["HTTPS_PROXY"], proxy)

    def test_verified_control_image_is_loaded_and_identity_checked(self):
        completed = lambda code=0, out="", err="": type(
            "Completed", (), {"returncode": code, "stdout": out, "stderr": err})()
        calls = [completed(0, "sha256:old\n"), completed(), completed(0, "Loaded image\n"),
                 completed(0, "arm64|9.9.9\n")]
        with tempfile.TemporaryDirectory() as tmp, \
                patch.object(mdd_update.platform, "machine", return_value="aarch64"), \
                patch.object(mdd_update.subprocess, "run", side_effect=calls) as run:
            artifact = Path(tmp, "control.tar.gz")
            artifact.write_bytes(b"image")
            mdd_update.load_control_image(artifact, "9.9.9")
        self.assertEqual(run.call_args_list[2].args[0][:3], ["docker", "load", "--input"])

    def test_control_image_mismatch_restores_previous_tag(self):
        completed = lambda code=0, out="", err="": type(
            "Completed", (), {"returncode": code, "stdout": out, "stderr": err})()
        calls = [completed(0, "sha256:old\n"), completed(), completed(),
                 completed(0, "amd64|9.9.9\n"), completed()]
        with tempfile.TemporaryDirectory() as tmp, \
                patch.object(mdd_update.platform, "machine", return_value="aarch64"), \
                patch.object(mdd_update.subprocess, "run", side_effect=calls) as run:
            with self.assertRaises(mdd_update.UpdateError):
                mdd_update.load_control_image(Path(tmp, "control.tar.gz"), "9.9.9")
        self.assertEqual(run.call_args_list[-1].args[0], [
            "docker", "tag", "mdd-sim-gateway/control:previous",
            "mdd-sim-gateway/control"])

    def test_apply_tree_preserves_installation_state(self):
        with tempfile.TemporaryDirectory() as tmp:
            repo, source = Path(tmp, "repo"), Path(tmp, "source")
            for relative in ["data/auth.json", ".git/config", "control/.venv/bin/python",
                            "control/app/stale.py", "webui/node_modules/pkg/index.js",
                            "webui/dist/index.html", "README.md"]:
                path = repo / relative
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text("old", encoding="utf-8")
            (repo / ".env").write_text("MDD_PORT=9999", encoding="utf-8")
            for relative in ["control/app/new.py", "webui/src/App.jsx", "README.md", "VERSION"]:
                path = source / relative
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text("new", encoding="utf-8")

            mdd_update.apply_tree(source, repo)

            self.assertEqual((repo / "data/auth.json").read_text(encoding="utf-8"), "old")
            self.assertEqual((repo / ".env").read_text(encoding="utf-8"), "MDD_PORT=9999")
            self.assertEqual((repo / ".git/config").read_text(encoding="utf-8"), "old")
            self.assertEqual((repo / "control/.venv/bin/python").read_text(encoding="utf-8"), "old")
            self.assertTrue((repo / "webui/node_modules/pkg/index.js").exists())
            self.assertEqual((repo / "webui/dist/index.html").read_text(encoding="utf-8"), "old",
                             "the served dist must survive so a failed rebuild keeps the UI up")
            self.assertEqual((repo / "README.md").read_text(encoding="utf-8"), "new")
            self.assertEqual((repo / "control/app/new.py").read_text(encoding="utf-8"), "new")
            self.assertFalse((repo / "control/app/stale.py").exists(),
                             "files removed upstream must not linger in managed directories")

    def test_apply_tree_replaces_only_marked_release_dist(self):
        with tempfile.TemporaryDirectory() as tmp:
            repo, source = Path(tmp, "repo"), Path(tmp, "source")
            (repo / "webui/dist").mkdir(parents=True)
            (repo / "webui/dist/index.html").write_text("old", encoding="utf-8")
            (source / "webui/dist").mkdir(parents=True)
            (source / "webui/dist/index.html").write_text("new", encoding="utf-8")
            (source / "webui/dist/.mdd-release-version").write_text(
                "9.9.9\n", encoding="utf-8")
            mdd_update.apply_tree(source, repo)
            self.assertEqual((repo / "webui/dist/index.html").read_text(), "new")

    def test_perform_accepts_release_without_distribution_metadata(self):
        with tempfile.TemporaryDirectory() as tmp:
            base = Path(tmp)
            repo, data, artifacts, payload = (base / "repo", base / "data",
                                              base / "artifacts", base / "payload")
            source = payload / "mdd-sim-gateway-v9.9.9"
            (source / "webui/dist").mkdir(parents=True)
            (source / "engine").mkdir()
            (source / "install.sh").write_text(
                '#!/bin/sh\nENGINE_MEDIA_WEBSOCKET_ABI="mdd-media-ws-v1"\n'
                'ENGINE_BROWSER_OUTBOUND_ABI="mdd-browser-outbound-v1"\n'
                'ENGINE_BROWSER_INBOUND_ABI="mdd-browser-inbound-v1"\n',
                encoding="utf-8")
            (source / "engine/Dockerfile").write_text(
                'LABEL io.mdd-sim-gateway.media-websocket="mdd-media-ws-v1" '
                'io.mdd-sim-gateway.browser-outbound="mdd-browser-outbound-v1" '
                'io.mdd-sim-gateway.browser-inbound="mdd-browser-inbound-v1"\n',
                encoding="utf-8")
            (source / "VERSION").write_text("9.9.9\n", encoding="utf-8")
            (source / "webui/dist/index.html").write_text("new", encoding="utf-8")
            (source / "webui/dist/.mdd-release-version").write_text(
                "9.9.9\n", encoding="utf-8")
            archive = base / "release.tar.gz"
            with tarfile.open(archive, "w:gz") as handle:
                handle.add(source, arcname=source.name)
            sums = base / "SHA256SUMS"
            sums.write_text(
                f"{hashlib.sha256(archive.read_bytes()).hexdigest()}  "
                "mdd-sim-gateway-v9.9.9.tar.gz\n", encoding="utf-8")
            repo.mkdir()
            data.mkdir()
            artifacts.mkdir()
            (repo / "VERSION").write_text("1.3.4\n", encoding="utf-8")
            status = mdd_update.Status(
                data / "orchestrator/update-status.json", "9.9.9")

            def fake_download(_url, destination, _env, _proxy=""):
                shutil.copy2(sums if destination.name == "SHA256SUMS" else archive,
                             destination)

            completed = type("Completed", (), {"returncode": 0})()
            with patch.object(mdd_update, "download", side_effect=fake_download), \
                    patch.object(mdd_update.subprocess, "run", return_value=completed), \
                    patch.object(mdd_update, "_docker_image_label", return_value=""):
                mdd_update.perform(repo, data, artifacts, "9.9.9",
                                   "MddIdd/mdd-sim-gateway", status)

            self.assertEqual((repo / "VERSION").read_text().strip(), "9.9.9")
            self.assertFalse((repo / "EDITION").exists())
            self.assertEqual(len(list((artifacts / "backups").glob("pre-update-*.tar.gz"))), 1)
            reload_log = artifacts / "update" / "reload.log"
            self.assertTrue(reload_log.is_file())
            self.assertEqual(reload_log.stat().st_mode & 0o777, 0o600)
            self.assertFalse((data / "update").exists())
            self.assertFalse((data / "backups").exists())
            completion = json.loads(status.path.read_text(encoding="utf-8"))
            self.assertEqual(completion["state"], "action_required")
            self.assertEqual(completion["phase"], "engine_media_migration_required")
            self.assertTrue(completion["engine_media_migration_required"])

            with patch.object(
                    mdd_update, "engine_media_migration_required", return_value=False):
                damaged = {**completion, "engine_media_websocket_abi": "other-abi"}
                mdd_update.atomic_json(status.path, damaged)
                self.assertFalse(
                    mdd_update.complete_engine_media_migration_status(repo, data))
                damaged = {**completion, "engine_browser_outbound_abi": "other-abi"}
                mdd_update.atomic_json(status.path, damaged)
                self.assertFalse(
                    mdd_update.complete_engine_media_migration_status(repo, data))
                damaged = {**completion, "engine_browser_inbound_abi": "other-abi"}
                mdd_update.atomic_json(status.path, damaged)
                self.assertFalse(
                    mdd_update.complete_engine_media_migration_status(repo, data))
                mdd_update.atomic_json(status.path, completion)
                self.assertTrue(mdd_update.complete_engine_media_migration_status(repo, data))
            completion = json.loads(status.path.read_text(encoding="utf-8"))
            self.assertEqual(completion["state"], "success")
            self.assertEqual(completion["phase"], "done")
            self.assertFalse(completion["engine_media_migration_required"])

    def test_perform_rejects_legacy_running_engine_before_replacing_tree(self):
        with tempfile.TemporaryDirectory() as tmp:
            base = Path(tmp)
            repo, data, artifacts, payload = (base / "repo", base / "data",
                                              base / "artifacts", base / "payload")
            source = payload / "mdd-sim-gateway-v9.9.9"
            (source / "webui/dist").mkdir(parents=True)
            (source / "engine").mkdir()
            (source / "install.sh").write_text(
                'ENGINE_ADMISSION_ABI="mdd-admission-v1"\n', encoding="utf-8")
            (source / "engine/Dockerfile").write_text(
                'LABEL io.mdd-sim-gateway.admission-abi="mdd-admission-v1"\n',
                encoding="utf-8")
            (source / "VERSION").write_text("9.9.9\n", encoding="utf-8")
            (source / "webui/dist/index.html").write_text("new", encoding="utf-8")
            (source / "webui/dist/.mdd-release-version").write_text(
                "9.9.9\n", encoding="utf-8")
            archive = base / "release.tar.gz"
            with tarfile.open(archive, "w:gz") as handle:
                handle.add(source, arcname=source.name)
            sums = base / "SHA256SUMS"
            sums.write_text(
                f"{hashlib.sha256(archive.read_bytes()).hexdigest()}  "
                "mdd-sim-gateway-v9.9.9.tar.gz\n", encoding="utf-8")
            repo.mkdir()
            data.mkdir()
            artifacts.mkdir()
            (repo / "VERSION").write_text("1.3.4\n", encoding="utf-8")
            status = mdd_update.Status(data / "orchestrator/status.json", "9.9.9")

            def fake_download(_url, destination, _env, _proxy=""):
                shutil.copy2(sums if destination.name == "SHA256SUMS" else archive,
                             destination)

            def docker_run(args, **_kwargs):
                cmd = " ".join(args)
                if "image inspect mdd-sim-gateway/engine" in cmd:
                    return SimpleNamespace(returncode=0, stdout="mdd-admission-v1\n", stderr="")
                if "ps --format" in cmd:
                    return SimpleNamespace(returncode=0,
                                           stdout="mdd-sim-gateway-engine-7\n", stderr="")
                if "index .Config.Labels" in cmd:
                    return SimpleNamespace(returncode=0, stdout="true\n", stderr="")
                if "{{.Config.Image}}" in cmd:
                    return SimpleNamespace(returncode=0,
                                           stdout="mdd-sim-gateway/engine\n", stderr="")
                if "inspect -f {{.Image}} mdd-sim-gateway-engine-7" in cmd:
                    return SimpleNamespace(returncode=0, stdout="sha256:" + "b" * 64 + "\n",
                                           stderr="")
                if "image inspect sha256:" in cmd:
                    return SimpleNamespace(returncode=0, stdout="\n", stderr="")
                return SimpleNamespace(returncode=0, stdout="", stderr="")

            with patch.object(mdd_update, "download", side_effect=fake_download), \
                    patch.object(mdd_update.subprocess, "run", side_effect=docker_run), \
                    self.assertRaises(mdd_update.UpdateError):
                mdd_update.perform(repo, data, artifacts, "9.9.9",
                                   "MddIdd/mdd-sim-gateway", status)

            self.assertEqual((repo / "VERSION").read_text().strip(), "1.3.4")

    def test_admission_health_requires_current_matching_status(self):
        with tempfile.TemporaryDirectory() as tmp:
            run = Path(tmp)
            start_ns = time.time_ns()
            identity = "a" * 64
            state_digest = "b" * 64
            auth = {
                "healthy": True, "state": "allow", "updated_at_ns": start_ns,
                "authority_identity_digest": identity,
                "normal_state_digest": state_digest,
                "authority_epoch": 3, "lease_seq": 10,
            }
            gate = {
                "state": "allow", "updated_at_ns": start_ns,
                "authority_identity_digest": identity,
                "normal_state_digest": state_digest,
                "authority_epoch": 3, "lease_seq": 11,
            }
            (run / "admission-authority-status.json").write_text(json.dumps(auth))
            (run / "admission-gate-status.json").write_text(json.dumps(gate))

            self.assertTrue(mdd_update.admission_status_current(
                run, min_updated_ns=start_ns))
            gate["normal_state_digest"] = "c" * 64
            (run / "admission-gate-status.json").write_text(json.dumps(gate))
            self.assertFalse(mdd_update.admission_status_current(
                run, min_updated_ns=start_ns))
            gate["normal_state_digest"] = state_digest
            gate["updated_at_ns"] = start_ns - 1
            (run / "admission-gate-status.json").write_text(json.dumps(gate))
            self.assertFalse(mdd_update.admission_status_current(
                run, min_updated_ns=start_ns))

    def test_media_websocket_release_allows_source_first_no_engine_update(self):
        with tempfile.TemporaryDirectory() as tmp:
            base = Path(tmp)
            source, data = base / "source", base / "data"
            (source / "engine").mkdir(parents=True)
            (source / "install.sh").write_text(
                'ENGINE_ADMISSION_ABI="mdd-admission-v1"\n'
                'ENGINE_MEDIA_WEBSOCKET_ABI="mdd-media-ws-v1"\n', encoding="utf-8")
            (source / "engine/Dockerfile").write_text(
                'LABEL io.mdd-sim-gateway.admission-abi="mdd-admission-v1" \\\n'
                ' io.mdd-sim-gateway.media-websocket="mdd-media-ws-v1"\n',
                encoding="utf-8")

            def label(_image, name):
                if name == mdd_update.ENGINE_ADMISSION_ABI_LABEL:
                    return mdd_update.ENGINE_ADMISSION_ABI
                return ""

            with patch.object(mdd_update, "_docker_image_label", side_effect=label), \
                    patch.object(mdd_update, "running_engine_names", return_value=[]):
                mdd_update.preflight_no_engine_replacement(source, data)

    def test_preflight_rejects_stale_admission_status(self):
        with tempfile.TemporaryDirectory() as tmp:
            base = Path(tmp)
            source, data = base / "source", base / "data"
            (source / "engine").mkdir(parents=True)
            (source / "install.sh").write_text(
                'ENGINE_ADMISSION_ABI="mdd-admission-v1"\n', encoding="utf-8")
            (source / "engine/Dockerfile").write_text(
                'LABEL io.mdd-sim-gateway.admission-abi="mdd-admission-v1"\n',
                encoding="utf-8")
            run = data / "instances" / "7" / "run"
            run.mkdir(parents=True)
            stale_ns = time.time_ns() - 10_000
            payload = {
                "healthy": True, "state": "allow", "updated_at_ns": stale_ns,
                "authority_identity_digest": "a" * 64,
                "normal_state_digest": "b" * 64,
                "authority_epoch": 1, "lease_seq": 2,
            }
            (run / "admission-authority-status.json").write_text(json.dumps(payload))
            (run / "admission-gate-status.json").write_text(json.dumps(payload))

            def docker_run(args, **_kwargs):
                cmd = " ".join(args)
                if "image inspect mdd-sim-gateway/engine" in cmd:
                    return SimpleNamespace(returncode=0, stdout="mdd-admission-v1\n", stderr="")
                if "ps --format" in cmd:
                    return SimpleNamespace(returncode=0,
                                           stdout="mdd-sim-gateway-engine-7\n", stderr="")
                if "index .Config.Labels" in cmd:
                    return SimpleNamespace(returncode=0, stdout="true\n", stderr="")
                if "{{.Config.Image}}" in cmd:
                    return SimpleNamespace(returncode=0,
                                           stdout="mdd-sim-gateway/engine\n", stderr="")
                if "inspect -f {{.Image}} mdd-sim-gateway-engine-7" in cmd:
                    return SimpleNamespace(returncode=0, stdout="sha256:" + "b" * 64 + "\n",
                                           stderr="")
                if "image inspect sha256:" in cmd:
                    return SimpleNamespace(returncode=0, stdout="mdd-admission-v1\n", stderr="")
                return SimpleNamespace(returncode=0, stdout="", stderr="")

            with patch.object(mdd_update.subprocess, "run", side_effect=docker_run), \
                    self.assertRaises(mdd_update.UpdateError):
                mdd_update.preflight_no_engine_replacement(
                    source, data, health_timeout=0.0)

    def test_perform_rejects_malformed_version_and_repository(self):
        with tempfile.TemporaryDirectory() as tmp:
            status = mdd_update.Status(Path(tmp, "status.json"), "x")
            with self.assertRaises(mdd_update.UpdateError):
                mdd_update.perform(Path(tmp), Path(tmp), Path(tmp), "../evil",
                                   "MddIdd/mdd-sim-gateway", status)
            with self.assertRaises(mdd_update.UpdateError):
                mdd_update.perform(Path(tmp), Path(tmp), Path(tmp), "1.0.2",
                                   "MddIdd/x/../y", status)


class OrchestratorUpdateTests(unittest.TestCase):
    def test_library_proxy_is_resolved_into_private_file_not_command_line(self):
        with tempfile.TemporaryDirectory() as tmp:
            data = Path(tmp)
            artifacts = data / "artifacts"
            root = data / "orchestrator"
            root.mkdir()
            (root / "update-request.json").write_text(json.dumps({
                "version": "9.9.9", "repository": "MddIdd/mdd-sim-gateway",
                "network": {"proxy_mode": "library", "proxy_profile_id": "primary"},
            }), encoding="utf-8")
            (root / "desired.json").write_text(json.dumps({"proxy": {
                "profiles": {"primary": {"name": "Primary", "type": "node"}},
                "exits": {"us": {"enabled": True, "profile_id": "primary"}},
            }}), encoding="utf-8")
            (root / "proxy-status.json").write_text(json.dumps({"exits": {"us": {
                "ready": True, "proxy_host": mdd_orchestrator.COUNTRY_PROXY_LISTEN,
                "proxy_port": 22538,
            }}}), encoding="utf-8")
            with patch.dict(os.environ, {"MDD_ARTIFACT_DIR": str(artifacts)}):
                app = mdd_orchestrator.Orchestrator(
                    data, Path(__file__).resolve().parent.parent)
            completed = type("Completed", (), {"returncode": 0, "stdout": "", "stderr": ""})()
            with patch.object(app, "service_active", return_value=False), \
                    patch.object(mdd_orchestrator, "run", return_value=completed) as run:
                app.process_update_request()
            network_path = data / "orchestrator" / "update-network.json"
            self.assertEqual(json.loads(network_path.read_text())["proxy_url"],
                             f"socks5h://{mdd_orchestrator.COUNTRY_PROXY_LISTEN}:22538")
            command = run.call_args_list[-1].args[0]
            self.assertNotIn("socks5h://", " ".join(command))
            self.assertEqual(network_path.stat().st_mode & 0o777, 0o600)
            self.assertTrue((artifacts / "update" / "runner.py").is_file())
            self.assertIn(str(artifacts), command)


if __name__ == "__main__":
    unittest.main()


class StarCountTests(unittest.TestCase):
    """The count decorates a link and keeps its last successful value across outages."""

    def setUp(self):
        update_check._stars_cache = None

    def test_a_failed_star_lookup_leaves_the_release_check_intact(self):
        session = SimpleNamespace(get=lambda *a, **k: (_ for _ in ()).throw(
            requests.RequestException("offline")))
        self.assertIsNone(update_check._stargazers(session, {}, "owner/repo"))

    def test_a_star_count_is_read_from_the_repository_endpoint(self):
        calls = []

        class Response:
            @staticmethod
            def raise_for_status():
                return None

            @staticmethod
            def json():
                return {"stargazers_count": 13300}

        def get(url, **kwargs):
            calls.append(url)
            return Response()

        value = update_check._stargazers(SimpleNamespace(get=get), {}, "owner/repo")
        self.assertEqual(value, 13300)
        self.assertEqual(calls, ["https://api.github.com/repos/owner/repo"])

        offline = SimpleNamespace(get=lambda *a, **k: (_ for _ in ()).throw(
            requests.RequestException("offline")))
        self.assertEqual(update_check._stargazers(offline, {}, "owner/repo"), 13300)

    def test_a_malformed_star_count_is_absent_rather_than_zero(self):
        class Response:
            @staticmethod
            def raise_for_status():
                return None

            @staticmethod
            def json():
                return {"stargazers_count": None}

        self.assertIsNone(update_check._stargazers(
            SimpleNamespace(get=lambda *a, **k: Response()), {}, "owner/repo"))
