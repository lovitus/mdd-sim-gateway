"""Small adapters from existing line semantics to a remote modem attachment."""
from __future__ import annotations

from .modem_registry import ModemUnavailable, registry


def instance_iccid(instances: list[dict], instance_id) -> str:
    iid = str(instance_id)
    inst = next((item for item in instances if str(item.get("id")) == iid), None)
    return str((inst or {}).get("iccid") or "").strip()


def attached_iccid(instances: list[dict], instance_id) -> str:
    iccid = instance_iccid(instances, instance_id)
    return iccid if iccid and registry.resolve(iccid) else ""


async def invoke(instances: list[dict], instance_id, method: str, params: dict | None = None,
                 operation_id: str = "", timeout: float = 20.0) -> dict:
    iccid = instance_iccid(instances, instance_id)
    if not iccid:
        raise ModemUnavailable("line has no ICCID")
    return await registry.rpc(iccid, method, params, operation_id=operation_id,
                              timeout=timeout)
