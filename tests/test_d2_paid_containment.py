"""D2 RED contract: cellular paid submits share the PCSCF/USIM fence boundary."""
import inspect
from unittest.mock import Mock, patch

import pytest

from control.app import main


class AsyncLock:
    async def acquire(self):
        return True

    def release(self):
        pass


class ManagerLock:
    def __enter__(self):
        return self

    def __exit__(self, *_args):
        return False


@pytest.mark.asyncio
async def test_cellular_submission_holds_pcscf_boundary_and_rechecks_usim_fence():
    order = []
    pcscf = object()
    with patch.object(main.hub, "recovery_lock", return_value=AsyncLock()), \
            patch.object(main.engine, "engine_maintenance_locked", return_value=ManagerLock()), \
            patch.object(main.engine, "acquire_pcscf_admission",
                         side_effect=lambda _iid: order.append("pcscf") or pcscf), \
            patch.object(main.engine, "release_pcscf_admission",
                         side_effect=lambda handle: order.append(("release", handle))), \
            patch.object(main.engine, "usim_recovery_blocks_paid_submission", return_value=False,
                         create=True), \
            patch.object(main, "_durable_maintenance_pending", return_value=False):
        async with main._maintenance_submission_boundary("6") as admitted:
            assert admitted is True
            assert order == ["pcscf"]
        assert order[-1] == ("release", pcscf)

    with patch.object(main.hub, "recovery_lock", return_value=AsyncLock()), \
            patch.object(main.engine, "engine_maintenance_locked", return_value=ManagerLock()), \
            patch.object(main.engine, "acquire_pcscf_admission", return_value=pcscf), \
            patch.object(main.engine, "release_pcscf_admission"), \
            patch.object(main.engine, "usim_recovery_blocks_paid_submission", return_value=True,
                         create=True), \
            patch.object(main, "_durable_maintenance_pending", return_value=False):
        async with main._maintenance_submission_boundary("6") as admitted:
            assert admitted is False


@pytest.mark.asyncio
async def test_termination_work_is_not_blocked_by_recovery_fence():
    """The new paid-submit boundary must not be added around the existing release path."""
    source = inspect.getsource(main._finalize_abandoned_cellular_media)
    assert "_maintenance_submission_boundary" not in source
