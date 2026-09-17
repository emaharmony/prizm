"""Discord channel adapter — translates Discord messages to/from Prizm events."""

from __future__ import annotations

import logging
from typing import Any

import discord
from discord.ext import commands

from prizm.client import PrizmClient
from prizm.event import Event

logger = logging.getLogger("prizm.channels.discord")


class DiscordAdapter:
    """
    Bridges Discord messages to Prizm events and back.

    Discord messages arrive → published as prizm.channel.received.discord
    Prizm events on prizm.channel.sent.discord → sent to Discord

    Usage:
        adapter = DiscordAdapter(prizm_client, discord_token="...")
        await adapter.start()
    """

    def __init__(
        self,
        prizm_client: PrizmClient,
        discord_token: str,
        guild_id: int | None = None,
        allowed_channels: list[int] | None = None,
    ):
        self.prizm = prizm_client
        self.discord_token = discord_token
        self.guild_id = guild_id
        self.allowed_channels = allowed_channels  # None = all channels

        # Set up Discord bot
        intents = discord.Intents.default()
        intents.message_content = True
        intents.guild_messages = True
        intents.dm_messages = True

        self.bot = commands.Bot(command_prefix="!", intents=intents)

        @self.bot.event
        async def on_message(message: discord.Message):
            await self._handle_discord_message(message)

        @self.bot.event
        async def on_ready():
            logger.info(f"prizm/discord: connected as {self.bot.user}")

    async def start(self) -> None:
        """Start the Discord bot and subscribe to Prizm outbound events."""
        # Subscribe to outbound Prizm events (agent → Discord)
        await self.prizm.subscribe(
            "prizm.channel.sent.discord",
            self._handle_prizm_outbound,
            durable="discord-outbound",
        )

        # Start Discord bot (blocks)
        logger.info("prizm/discord: starting bot...")
        await self.bot.start(self.discord_token)

    async def _handle_discord_message(self, message: discord.Message) -> None:
        """Convert a Discord message into a Prizm event."""
        # Ignore own messages
        if message.author == self.bot.user:
            return

        # Filter channels if configured
        if self.allowed_channels and message.channel.id not in self.allowed_channels:
            return

        # Determine channel type
        channel_type = "dm" if isinstance(message.channel, discord.DMChannel) else "guild"

        # Build event payload
        payload: dict[str, Any] = {
            "channel": "discord",
            "channel_type": channel_type,
            "channel_id": str(message.channel.id),
            "guild_id": str(message.guild.id) if message.guild else None,
            "sender_id": str(message.author.id),
            "sender_name": message.author.display_name,
            "text": message.content,
            "message_id": str(message.id),
            "timestamp": message.created_at.isoformat(),
            "is_bot": message.author.bot,
            "attachments": [
                {"url": a.url, "filename": a.filename, "content_type": a.content_type}
                for a in message.attachments
            ],
        }

        # Reference message (replies)
        if message.reference and message.reference.message_id:
            payload["reply_to"] = str(message.reference.message_id)

        # Emit to Prizm
        await self.prizm.emit(
            "prizm.channel.received.discord",
            payload,
            source="discord",
            metadata={"session_id": f"discord-{message.channel.id}"},
        )
        logger.debug(f"prizm/discord: received message from {message.author.display_name}")

    async def _handle_prizm_outbound(self, event: Event) -> None:
        """Send a Prizm outbound event to Discord."""
        payload = event.payload
        channel_id = int(payload.get("channel_id", 0))
        text = payload.get("text", "")

        if not channel_id or not text:
            logger.warning("prizm/discord: outbound event missing channel_id or text")
            return

        # Find the Discord channel
        channel = self.bot.get_channel(channel_id)
        if not channel:
            try:
                channel = await self.bot.fetch_channel(channel_id)
            except discord.NotFound:
                logger.warning(f"prizm/discord: channel {channel_id} not found")
                return

        # Send the message
        try:
            sent = await channel.send(text)

            # Emit confirmation event
            await self.prizm.emit(
                "prizm.channel.sent.confirmation",
                {
                    "channel": "discord",
                    "channel_id": str(channel_id),
                    "message_id": str(sent.id),
                    "correlation_id": event.correlation_id,
                },
                source="discord",
                correlation_id=event.correlation_id,
                parent_id=event.id,
            )
            logger.debug(f"prizm/discord: sent message to channel {channel_id}")
        except discord.HTTPException as e:
            logger.error(f"prizm/discord: failed to send message: {e}")
