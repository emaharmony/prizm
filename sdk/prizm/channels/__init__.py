"""Channel adapters — translate between messaging platforms and Prizm events."""

from prizm.channels.discord import DiscordAdapter
from prizm.channels.telegram import TelegramAdapter

__all__ = ["DiscordAdapter", "TelegramAdapter"]