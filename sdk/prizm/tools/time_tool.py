"""Time tool — returns current UTC time. Simplest possible tool for testing."""

from __future__ import annotations

from datetime import datetime, timezone

from prizm.client import PrizmClient


class TimeTool:
    """
    Time tool — responds to prizm.tool.called.time with the current UTC timestamp.

    Usage:
        client = PrizmClient()
        await client.connect()
        time_tool = TimeTool(client)
        await time_tool.register()
    """

    def __init__(self, client: PrizmClient):
        self.client = client

    async def register(self) -> None:
        """Register the time tool with Prizm."""
        await self.client.register_tool("time", self._handle)

    @staticmethod
    async def _handle(args: dict) -> dict:
        """Return the current UTC time."""
        now = datetime.now(timezone.utc)
        return {
            "utc_time": now.isoformat(),
            "unix_seconds": int(now.timestamp()),
            "formatted": now.strftime("%Y-%m-%d %H:%M:%S UTC"),
        }
