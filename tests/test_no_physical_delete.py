import pytest
from fastapi.testclient import TestClient
from unittest.mock import patch

from control.app.main import app
from control.app import lpa


@pytest.mark.asyncio
async def test_lpa_profile_delete_raises_disabled():
    """LPA core layer must hard-reject physical profile deletion."""
    with pytest.raises(lpa.LpaError) as exc_info:
        await lpa.profile_delete("reader0", "89860000000000000001")
    assert "physical profile deletion is disabled" in str(exc_info.value)
    assert exc_info.value.code == -1


def test_api_esim_delete_returns_403_forbidden():
    """API layer must return HTTP 403 with physical_delete_prohibited code."""
    with patch("control.app.auth.session", return_value={"user": "admin", "csrf": "test-csrf"}):
        client = TestClient(app, cookies={"mdd_session": "test-session"})
        resp = client.delete(
            "/api/esim/profiles/89860000000000000001",
            headers={"x-mdd-csrf-token": "test-csrf"}
        )
        assert resp.status_code == 403
        detail = resp.json().get("detail", {})
        assert detail.get("code") == "physical_delete_prohibited"
        assert "disabled by policy" in detail.get("message", "")
