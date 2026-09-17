"""
Prizm — Event-Native AI Agent Platform SDK

One beam of light. One event. A spectrum of reactions.
"""

from prizm.client import PrizmClient
from prizm.event import Event, EventMetadata
from prizm._global import connect, emit, on, tool, agent, run

__all__ = [
    "PrizmClient",
    "Event",
    "EventMetadata",
    "connect",
    "emit",
    "on",
    "tool",
    "agent",
    "run",
]
__version__ = "0.2.0-preview.1"