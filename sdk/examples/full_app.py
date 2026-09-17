"""
Prizm Example App — demonstrates the full SDK with Discord and Telegram adapters.

Usage:
    export DISCORD_TOKEN="your-bot-token"
    export TELEGRAM_TOKEN="your-bot-token"
    export NATS_URL="nats://localhost:4222"

    python -m examples.full_app
"""

import asyncio
import logging
import os

import prizm
from prizm.agents.echo import EchoAgent
from prizm.channels.discord import DiscordAdapter
from prizm.channels.telegram import TelegramAdapter
from prizm.tools.time_tool import TimeTool

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(name)s %(message)s")
logger = logging.getLogger("prizm-example")


async def main():
    # ── Connect to Prizm bus ─────────────────────────────────────
    client = await prizm.connect(
        nats_url=os.getenv("NATS_URL", "nats://localhost:4222"),
        name="prizm-example",
    )

    # ── Register agents ──────────────────────────────────────────
    # Echo agent logs all events
    echo = EchoAgent(client)
    await echo.start()

    # ── Register tools ───────────────────────────────────────────
    time_tool = TimeTool(client)
    await time_tool.register()

    # ── Register custom handler ──────────────────────────────────
    @prizm.on("prizm.channel.received.discord")
    async def handle_discord(event):
        """Handle incoming Discord messages."""
        logger.info(f"📣 Discord message from {event.payload.get('sender_name', 'unknown')}: "
                     f"{event.payload.get('text', '')[:80]}")

        # Simple echo response (replace with LLM call)
        text = event.payload.get("text", "")
        if text.strip():
            await prizm.emit(
                "prizm.channel.sent.discord",
                {
                    "channel_id": event.payload.get("channel_id"),
                    "text": f"💎 Prizm received: {text[:200]}",
                },
                source="prizm-example",
                correlation_id=event.correlation_id,
                parent_id=event.id,
            )

    @prizm.on("prizm.channel.received.telegram")
    async def handle_telegram(event):
        """Handle incoming Telegram messages."""
        logger.info(f"📨 Telegram message from {event.payload.get('sender_name', 'unknown')}: "
                     f"{event.payload.get('text', '')[:80]}")

        # Check encryption flag
        encryption = event.payload.get("encryption_flag", "transport")
        text = event.payload.get("text", "")
        if text.strip():
            privacy_note = "🔒 E2E" if encryption == "e2e" else "🔓 Transport"
            await prizm.emit(
                "prizm.channel.sent.telegram",
                {
                    "chat_id": event.payload.get("chat_id"),
                    "text": f"💎 Prizm received [{privacy_note}]: {text[:200]}",
                },
                source="prizm-example",
                correlation_id=event.correlation_id,
                parent_id=event.id,
            )

    # ── Start channel adapters ───────────────────────────────────
    adapters = []

    discord_token = os.getenv("DISCORD_TOKEN")
    if discord_token:
        discord_adapter = DiscordAdapter(client, discord_token=discord_token)
        adapters.append(("discord", discord_adapter))
        logger.info("prizm: Discord adapter configured")
    else:
        logger.info("prizm: No DISCORD_TOKEN, skipping Discord adapter")

    telegram_token = os.getenv("TELEGRAM_TOKEN")
    if telegram_token:
        telegram_adapter = TelegramAdapter(client, bot_token=telegram_token)
        adapters.append(("telegram", telegram_adapter))
        logger.info("prizm: Telegram adapter configured")
    else:
        logger.info("prizm: No TELEGRAM_TOKEN, skipping Telegram adapter")

    # ── Run everything ────────────────────────────────────────────
    logger.info("prizm: all adapters ready, starting event loop...")

    # Start channel adapters as concurrent tasks
    adapter_tasks = []
    for name, adapter in adapters:
        task = asyncio.create_task(adapter.start(), name=f"adapter-{name}")
        adapter_tasks.append(task)

    # Also start the Prizm event loop
    try:
        await prizm.run()
    except KeyboardInterrupt:
        logger.info("prizm: shutting down...")
        for name, adapter in adapters:
            if hasattr(adapter, "stop"):
                await adapter.stop()
        await client.close()


if __name__ == "__main__":
    asyncio.run(main())
