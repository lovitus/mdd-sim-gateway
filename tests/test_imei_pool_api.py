import asyncio
from unittest.mock import AsyncMock, patch

import pytest
from fastapi import HTTPException

from control.app import main


def test_bound_pool_entry_cannot_change_imei_digits():
    existing = {"id": "imei-one", "imei": "356839119784073"}
    bindings = {"894411": {"imei_id": "imei-one"}}
    with (
        patch.object(main.cfg, "get_imei_pool_entry", return_value=existing),
        patch.object(main.cfg, "list_iccid_imei_bindings", return_value=bindings),
        patch.object(main.cfg, "upsert_imei_pool_entry") as save,
    ):
        with pytest.raises(HTTPException) as caught:
            asyncio.run(main.api_save_imei_pool_entry({
                "id": "imei-one", "name": "Phone", "imei": "358742000000001",
            }))
    assert caught.value.status_code == 409
    save.assert_not_called()


def test_bound_pool_entry_cannot_be_deleted():
    bindings = {"894411": {"imei_id": "imei-one"}}
    with (
        patch.object(main.cfg, "list_iccid_imei_bindings", return_value=bindings),
        patch.object(main.cfg, "delete_imei_pool_entry") as delete,
    ):
        with pytest.raises(HTTPException) as caught:
            asyncio.run(main.api_delete_imei_pool_entry("imei-one"))
    assert caught.value.status_code == 409
    delete.assert_not_called()


def test_unbound_pool_entry_can_be_deleted():
    with (
        patch.object(main.cfg, "list_iccid_imei_bindings", return_value={}),
        patch.object(main.cfg, "delete_imei_pool_entry", return_value=True),
        patch.object(main.hub, "broadcast", new=AsyncMock()),
    ):
        result = asyncio.run(main.api_delete_imei_pool_entry("imei-unused"))
    assert result["ok"] is True
