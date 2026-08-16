import unittest
from unittest.mock import AsyncMock, Mock

from control.app.runtime import RuntimeRegistry


class RuntimeRegistryTests(unittest.IsolatedAsyncioTestCase):
    async def test_repeated_get_uses_cached_inspect(self):
        inspector = Mock(return_value={
            "running": True, "ip": "172.17.0.2", "container_id": "one"})
        registry = RuntimeRegistry(inspector)

        first = await registry.get("1")
        second = await registry.get("1")

        self.assertEqual(first["container_id"], "one")
        self.assertEqual(second["ip"], "172.17.0.2")
        inspector.assert_called_once_with("1")

    async def test_stop_event_updates_cache_without_inspect(self):
        inspector = Mock(return_value={
            "running": True, "ip": "172.17.0.2", "container_id": "old"})
        changed = AsyncMock()
        registry = RuntimeRegistry(inspector)
        registry._on_change = changed

        await registry.handle_event({
            "Action": "die", "id": "old",
            "Actor": {"Attributes": {"name": "mdd-sim-gateway-engine-7"}},
        })

        runtime = await registry.get("7")
        self.assertFalse(runtime["running"])
        inspector.assert_not_called()
        changed.assert_awaited_once()

    async def test_start_event_forces_fresh_generation(self):
        inspector = Mock(side_effect=[
            {"running": True, "ip": "172.17.0.2", "container_id": "old"},
            {"running": True, "ip": "172.17.0.2", "container_id": "new"},
        ])
        registry = RuntimeRegistry(inspector)
        await registry.get("2")

        await registry.handle_event({
            "Action": "start", "id": "new",
            "Actor": {"Attributes": {"name": "mdd-sim-gateway-engine-2"}},
        })

        self.assertEqual((await registry.get("2"))["container_id"], "new")
        self.assertEqual(inspector.call_count, 2)

    async def test_unrelated_container_event_is_ignored(self):
        inspector = Mock()
        changed = AsyncMock()
        registry = RuntimeRegistry(inspector)
        registry._on_change = changed

        await registry.handle_event({
            "Action": "stop", "Actor": {"Attributes": {"name": "other"}}})

        inspector.assert_not_called()
        changed.assert_not_awaited()


if __name__ == "__main__":
    unittest.main()
