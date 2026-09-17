"""Canonical Prizm event schema — the core data structure that flows through the bus."""

from __future__ import annotations

import time
from typing import Any

from pydantic import BaseModel, ConfigDict, Field


def _event_id() -> str:
    """Generate a sortable, unique event ID: evt_<ms>_<counter>."""
    _counter = 0
    while True:
        _counter += 1
        yield f"evt_{int(time.time() * 1000)}_{_counter:06d}"


_id_gen = _event_id()


class EventMetadata(BaseModel):
    """LLM provenance, cost tracking, and session context."""

    model: str = ""
    prompt_hash: str = ""
    token_cost: int = 0
    session_id: str = ""
    latency_ms: int = 0


class Event(BaseModel):
    """
    The canonical Prizm event.

    Every action, decision, and state change in Prizm flows as one of these.
    """

    model_config = ConfigDict(extra="allow")

    id: str = Field(default_factory=lambda: next(_id_gen))
    type: str = ""                          # e.g. "prizm.agent.decision"
    source: str = ""                        # e.g. "lumi", "telegram", "discord"
    timestamp: str = Field(default_factory=lambda: time.strftime("%Y-%m-%dT%H:%M:%S.000Z", time.gmtime()))
    correlation_id: str = ""                # Links events in the same workflow
    parent_id: str = ""                     # Direct causal parent
    payload: dict[str, Any] = Field(default_factory=dict)
    metadata: EventMetadata = Field(default_factory=EventMetadata)
