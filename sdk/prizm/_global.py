"""Global convenience API — import prizm and go."""

from __future__ import annotations

from typing import Any

from prizm.client import PrizmClient
from prizm.event import Event

# Module-level default client
_default_client: PrizmClient | None = None


def _get_client() -> PrizmClient:
    if _default_client is None:
        raise RuntimeError("Not connected. Call prizm.connect() first.")
    return _default_client


async def connect(nats_url: str = "nats://localhost:4222", name: str = "prizm-sdk") -> PrizmClient:
    """Connect to the Prizm event bus. Creates a default global client."""
    global _default_client
    _default_client = PrizmClient(nats_url=nats_url, name=name)
    await _default_client.connect()
    return _default_client


async def emit(
    event_type: str,
    payload: dict[str, Any],
    *,
    source: str = "",
    correlation_id: str = "",
    parent_id: str = "",
    metadata: dict[str, Any] | None = None,
) -> Event:
    """Publish an event using the default global client."""
    return await _get_client().emit(
        event_type,
        payload,
        source=source,
        correlation_id=correlation_id,
        parent_id=parent_id,
        metadata=metadata,
    )


def on(subject: str, durable: str = "", queue: str = ""):
    """
    Decorator to subscribe to events on a subject.

    Usage:
        @prizm.on("prizm.channel.received.discord")
        async def handle_discord(event):
            ...
    """
    def decorator(func):
        # Register the handler — we'll subscribe in run()
        if not hasattr(on, "_handlers"):
            on._handlers = []  # type: ignore[attr-defined]
        on._handlers.append((subject, func, durable, queue))  # type: ignore[attr-defined]
        return func
    return decorator


def agent(name: str, subscribes: list[str]):
    """
    Decorator to define an agent that subscribes to specific event types.

    Usage:
        @prizm.agent(name="reviewer", subscribes=["prizm.agent.decision"])
        async def reviewer(event):
            ...
    """
    def decorator(func):
        if not hasattr(agent, "_agents"):
            agent._agents = []  # type: ignore[attr-defined]
        agent._agents.append((name, subscribes, func))  # type: ignore[attr-defined]
        return func
    return decorator


def tool(tool_name: str):
    """
    Decorator to register a tool handler.

    Usage:
        @prizm.tool("github.create_issue")
        async def create_issue(args):
            return {"url": "..."}
    """
    def decorator(func):
        if not hasattr(tool, "_tools"):
            tool._tools = []  # type: ignore[attr-defined]
        tool._tools.append((tool_name, func))  # type: ignore[attr-defined]
        return func
    return decorator


async def run() -> None:
    """Start the default client and subscribe all registered handlers."""
    client = _get_client()

    # Subscribe @on handlers
    if hasattr(on, "_handlers"):
        for subject, handler, durable, queue in on._handlers:  # type: ignore[attr-defined]
            await client.subscribe(subject, handler, durable=durable or "", queue=queue or "")

    # Subscribe @agent handlers
    if hasattr(agent, "_agents"):
        for agent_name, subjects, handler in agent._agents:  # type: ignore[attr-defined]
            for subject in subjects:
                await client.subscribe(
                    subject,
                    handler,
                    durable=f"agent-{agent_name}",
                    queue=f"agents-{agent_name}",
                )

    # Register @tool handlers
    if hasattr(tool, "_tools"):
        for tool_name, handler in tool._tools:  # type: ignore[attr-defined]
            await client.register_tool(tool_name, handler)

    await client.run()
