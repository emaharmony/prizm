"""
Prizm Minimal Example — SDK only, no channel adapters.

Starts the Prizm bus (Go binary), connects, publishes test events,
and demonstrates event chains.

Usage:
    # Terminal 1: Start the bus
    cd /path/to/prizm && ./prizm-bus

    # Terminal 2: Run this example
    cd sdk && python -m examples.minimal
"""

import asyncio
import logging

import prizm

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(name)s %(message)s")
logger = logging.getLogger("minimal")


async def main():
    # Connect to the Prizm bus
    await prizm.connect(name="minimal-example")
    logger.info("Connected to Prizm bus!")

    # Subscribe to all agent events
    @prizm.on("prizm.agent.*")
    async def log_agent_events(event):
        logger.info(f"🧠 Agent event: {event.type} from {event.source} "
                     f"| payload: {event.payload}")

    # Subscribe to memory events
    @prizm.on("prizm.memory.*")
    async def log_memory_events(event):
        logger.info(f"📝 Memory event: {event.type} | {event.payload}")

    # Define a simple agent
    @prizm.agent(name="greeter", subscribes=["prizm.channel.received.*"])
    async def greeter(event):
        """Simple agent that greets people."""
        sender = event.payload.get("sender_name", "unknown")
        text = event.payload.get("text", "")

        # Make a decision
        await prizm.emit(
            "prizm.agent.decision",
            {
                "action": "greet",
                "confidence": 0.95,
                "reasoning": f"User {sender} said: {text}",
            },
            source="greeter",
            correlation_id=event.correlation_id,
            parent_id=event.id,
        )

        # Store memory
        await prizm.emit(
            "prizm.memory.stored",
            {
                "category": "interaction",
                "tier": "session",
                "content": f"Greeted {sender}",
            },
            source="greeter",
            correlation_id=event.correlation_id,
        )

    # Publish some test events
    logger.info("Publishing test events...")

    correlation_id = "test-001"

    # Simulate a Discord message arriving
    await prizm.emit(
        "prizm.channel.received.discord",
        {
            "channel": "discord",
            "channel_id": "123456789",
            "sender_name": "Ema",
            "text": "Hello Prizm!",
        },
        source="test",
        correlation_id=correlation_id,
    )

    # Simulate a Telegram message arriving
    await prizm.emit(
        "prizm.channel.received.telegram",
        {
            "channel": "telegram",
            "chat_id": "987654321",
            "sender_name": "Ema",
            "text": "Testing from Telegram!",
            "is_private": True,
            "encryption_flag": "e2e",
        },
        source="test",
        correlation_id=correlation_id,
    )

    logger.info("Test events published! Running event loop...")
    logger.info("Press Ctrl+C to stop.")

    # Subscribe handlers and run
    await prizm.run()


if __name__ == "__main__":
    asyncio.run(main())