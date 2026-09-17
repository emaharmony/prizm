"""Echo agent — simplest possible agent. Subscribes to all events and logs them."""

from __future__ import annotations

import logging

from prizm.client import PrizmClient
from prizm.event import Event

logger = logging.getLogger("prizm.agents.echo")


class EchoAgent:
    """
    Echo agent — subscribes to prizm.> and logs every event.

    Useful for debugging, monitoring, and learning the Prizm event flow.

    Usage:
        client = PrizmClient()
        await client.connect()
        echo = EchoAgent(client)
        await echo.start()
    """

    def __init__(self, client: PrizmClient, name: str = "echo"):
        self.client = client
        self.name = name

    async def start(self) -> None:
        """Subscribe to all Prizm events and log them."""
        await self.client.subscribe("prizm.>", self._handle_event, durable=f"agent-{self.name}")
        logger.info(f"prizm/echo: agent '{self.name}' started, listening to prizm.>")

    async def _handle_event(self, event: Event) -> None:
        """Log every event that passes through."""
        logger.info(
            f"💎 [{event.type}] id={event.id[:20]} source={event.source} "
            f"correlation={event.correlation_id[:12] if event.correlation_id else '-'}"
        )
        # Optionally echo back a confirmation event
        # await self.client.emit(
        #     "prizm.agent.output",
        #     {"echo": True, "original_type": event.type},
        #     source=self.name,
        #     correlation_id=event.correlation_id,
        #     parent_id=event.id,
        # )