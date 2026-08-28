from pathlib import Path

import yaml


def test_production_compose_keeps_source_out_and_engines_control_managed():
    root = Path(__file__).resolve().parents[1]
    value = yaml.safe_load((root / "compose.production.yaml").read_text(encoding="utf-8"))
    assert set(value["services"]) == {"control"}
    control = value["services"]["control"]
    assert control["build"]["dockerfile"] == "control/Dockerfile"
    assert control["restart"] == "unless-stopped"
    assert control["environment"]["MDD_HOST_DATA"].startswith("${MDD_RUNTIME_ROOT")
    mounts = control["volumes"]
    assert all("source" not in item or item["source"] not in {".", "./", "/opt/mdd-gateway"}
               for item in mounts)
    assert any(item.get("target") == "/data" for item in mounts)
    assert "healthcheck" in control
