from pathlib import Path

import yaml


def test_production_compose_keeps_source_out_and_engines_control_managed():
    root = Path(__file__).resolve().parents[1]
    value = yaml.safe_load((root / "compose.production.yaml").read_text(encoding="utf-8"))
    assert set(value["services"]) == {"control"}
    control = value["services"]["control"]
    assert control["build"]["dockerfile"] == "control/Dockerfile"
    assert control["restart"] == "unless-stopped"
    assert control["network_mode"] == "bridge"
    assert control["labels"]["io.mdd-sim-gateway.managed"] == "true"
    assert control["labels"]["io.mdd-sim-gateway.component"] == "control"
    assert control["environment"]["MDD_HOST_STATE"].startswith("${MDD_STATE_ROOT")
    assert control["environment"]["MDD_HOST_CONFIG"].startswith("${MDD_CONFIG_ROOT")
    assert control["environment"]["MDD_CONFIG_DIR"] == "/var/lib/mdd/config"
    assert control["environment"]["MDD_STATE_DIR"] == "/var/lib/mdd/state"
    assert "MDD_DATA" not in control["environment"]
    assert control["environment"]["MDD_ARTIFACT_DIR"] == "/var/lib/mdd/artifacts"
    assert control["environment"]["MDD_MANAGER_URL"].startswith("${MDD_MANAGER_URL")
    assert "MDD_ALLOWED_AGENT_PACKAGE_DIGESTS" in control["environment"]
    mounts = control["volumes"]
    assert all("source" not in item or item["source"] not in {".", "./", "/opt/mdd-gateway"}
               for item in mounts)
    persistent_targets = {item.get("target") for item in mounts}
    assert {"/var/lib/mdd/config", "/var/lib/mdd/state",
            "/var/lib/mdd/artifacts"}.issubset(persistent_targets)
    persistent_sources = {item.get("source") for item in mounts
                          if item.get("target") in persistent_targets}
    assert len(persistent_sources) == len(persistent_targets)
    assert any(item.get("target") == "/run/mdd" and
               str(item.get("source", "")).startswith("${MDD_RUNTIME_ROOT")
               for item in mounts)
    assert "healthcheck" in control


def test_legacy_compose_entrypoint_cannot_drift_from_production_contract():
    root = Path(__file__).resolve().parents[1]
    assert (root / "docker-compose.yml").read_text(encoding="utf-8") == (
        root / "compose.production.yaml"
    ).read_text(encoding="utf-8")
