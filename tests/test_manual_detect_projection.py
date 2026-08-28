from unittest.mock import AsyncMock, patch

import pytest

from control.app import main


@pytest.mark.asyncio
async def test_manual_detect_projects_identity_without_scheduling_engine_start():
    name = "Virtual PCD 00 0A"
    previous_cards = dict(main.hub.cards)
    try:
        async def observed(reader_name, reader_index, *, schedule_start=True, **_kwargs):
            assert reader_name == name
            assert reader_index == 10
            assert schedule_start is False
            main.hub.cards[name] = {
                "name": name, "index": 10, "present": True,
                "iccid": "8944110069499811522", "matched": "1",
                "identity_current": True,
            }

        with patch.object(main.sim, "list_readers", return_value=["x"] * 10 + [name]), \
                patch.object(main, "_on_card_insert", new=AsyncMock(side_effect=observed)), \
                patch.object(main.hub, "broadcast", new=AsyncMock()) as broadcast:
            result = await main.api_sim_detect(10)

        assert result["iccid"].endswith("1522")
        assert result["matched"] == "1"
        assert broadcast.await_count == 1
        assert broadcast.await_args.args[0]["type"] == "cards"
    finally:
        main.hub.cards.clear()
        main.hub.cards.update(previous_cards)
