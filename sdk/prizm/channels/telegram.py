"""Telegram channel adapter — translates Telegram messages to/from Prizm events."""

from __future__ import annotations

import logging
from typing import Any

from telegram import Update
from telegram.ext import Application, ContextTypes, MessageHandler, filters

from prizm.client import PrizmClient
from prizm.event import Event

logger = logging.getLogger("prizm.channels.telegram")


class TelegramAdapter:
    """
    Bridges Telegram messages to Prizm events and back.

    Telegram messages arrive → published as prizm.channel.received.telegram
    Prizm events on prizm.channel.sent.telegram → sent to Telegram

    Supports:
    - Text messages (private and group)
    - Voice messages (transcription handled by agent)
    - Inline keyboards (agent can send interactive buttons)
    - Secret chat awareness (routes E2E-flagged messages differently)

    Usage:
        adapter = TelegramAdapter(prizm_client, bot_token="...")
        await adapter.start()
    """

    def __init__(
        self,
        prizm_client: PrizmClient,
        bot_token: str,
        allowed_chats: list[int] | None = None,
        secret_chat_routing: bool = True,
    ):
        self.prizm = prizm_client
        self.bot_token = bot_token
        self.allowed_chats = allowed_chats  # None = all chats
        self.secret_chat_routing = secret_chat_routing

        # Set up Telegram bot
        self.app = Application.builder().token(bot_token).build()

        # Register handlers
        self.app.add_handler(
            MessageHandler(filters.TEXT & ~filters.COMMAND, self._handle_text_message)
        )
        self.app.add_handler(
            MessageHandler(filters.VOICE, self._handle_voice_message)
        )
        self.app.add_handler(
            MessageHandler(filters.PHOTO, self._handle_photo_message)
        )

    async def start(self) -> None:
        """Start the Telegram bot and subscribe to Prizm outbound events."""
        # Subscribe to outbound Prizm events (agent → Telegram)
        await self.prizm.subscribe(
            "prizm.channel.sent.telegram",
            self._handle_prizm_outbound,
            durable="telegram-outbound",
        )

        # Start Telegram bot
        logger.info("prizm/telegram: starting bot...")
        await self.app.initialize()
        await self.app.start()
        await self.app.updater.start_polling()

    async def stop(self) -> None:
        """Stop the Telegram bot."""
        await self.app.updater.stop()
        await self.app.stop()
        await self.app.shutdown()

    async def _handle_text_message(self, update: Update, context: ContextTypes.DEFAULT_TYPE) -> None:
        """Convert a Telegram text message into a Prizm event."""
        if not update.message or not update.message.text:
            return

        msg = update.message
        chat = msg.chat

        # Filter chats if configured
        if self.allowed_chats and chat.id not in self.allowed_chats:
            return

        # Determine chat type
        chat_type = chat.type  # "private", "group", "supergroup"

        # Secret chat routing flag
        # Note: Telegram Bot API doesn't have direct secret chat access,
        # but we flag messages from private chats for agents that want
        # to route sensitive conversations differently
        is_private = chat_type == "private"

        payload: dict[str, Any] = {
            "channel": "telegram",
            "chat_type": chat_type,
            "chat_id": str(chat.id),
            "chat_title": chat.title or "",
            "sender_id": str(msg.from_user.id) if msg.from_user else "",
            "sender_name": msg.from_user.full_name if msg.from_user else "",
            "sender_username": msg.from_user.username if msg.from_user else None,
            "text": msg.text,
            "message_id": str(msg.message_id),
            "timestamp": msg.date.isoformat() if msg.date else "",
            "is_bot": msg.from_user.is_bot if msg.from_user else False,
            "is_private": is_private,
            "encryption_flag": "e2e" if is_private and self.secret_chat_routing else "transport",
        }

        # Reply-to reference
        if msg.reply_to_message:
            payload["reply_to"] = str(msg.reply_to_message.message_id)

        # Emit to Prizm
        await self.prizm.emit(
            "prizm.channel.received.telegram",
            payload,
            source="telegram",
            metadata={"session_id": f"telegram-{chat.id}"},
        )
        logger.debug(f"prizm/telegram: received message from {payload['sender_name']}")

    async def _handle_voice_message(self, update: Update, context: ContextTypes.DEFAULT_TYPE) -> None:
        """Convert a Telegram voice message into a Prizm event."""
        if not update.message or not update.message.voice:
            return

        msg = update.message
        voice = msg.voice

        # Get the voice file
        voice_file = await context.bot.get_file(voice.file_id)

        payload: dict[str, Any] = {
            "channel": "telegram",
            "chat_type": msg.chat.type,
            "chat_id": str(msg.chat.id),
            "sender_id": str(msg.from_user.id) if msg.from_user else "",
            "sender_name": msg.from_user.full_name if msg.from_user else "",
            "message_type": "voice",
            "voice_file_id": voice.file_id,
            "voice_file_url": voice_file.file_path or "",
            "voice_duration": voice.duration,
            "voice_mime_type": voice.mime_type or "audio/ogg",
            "message_id": str(msg.message_id),
            "timestamp": msg.date.isoformat() if msg.date else "",
            "is_private": msg.chat.type == "private",
            "encryption_flag": "e2e" if msg.chat.type == "private" and self.secret_chat_routing else "transport",
        }

        # Include caption if present
        if msg.caption:
            payload["caption"] = msg.caption

        await self.prizm.emit(
            "prizm.channel.received.telegram",
            payload,
            source="telegram",
            metadata={"session_id": f"telegram-{msg.chat.id}"},
        )

    async def _handle_photo_message(self, update: Update, context: ContextTypes.DEFAULT_TYPE) -> None:
        """Convert a Telegram photo message into a Prizm event."""
        if not update.message or not update.message.photo:
            return

        msg = update.message
        # Get the largest photo size
        photo = msg.photo[-1]

        payload: dict[str, Any] = {
            "channel": "telegram",
            "chat_type": msg.chat.type,
            "chat_id": str(msg.chat.id),
            "sender_id": str(msg.from_user.id) if msg.from_user else "",
            "sender_name": msg.from_user.full_name if msg.from_user else "",
            "message_type": "photo",
            "photo_file_id": photo.file_id,
            "photo_width": photo.width,
            "photo_height": photo.height,
            "message_id": str(msg.message_id),
            "timestamp": msg.date.isoformat() if msg.date else "",
            "is_private": msg.chat.type == "private",
        }

        if msg.caption:
            payload["caption"] = msg.caption

        await self.prizm.emit(
            "prizm.channel.received.telegram",
            payload,
            source="telegram",
            metadata={"session_id": f"telegram-{msg.chat.id}"},
        )

    async def _handle_prizm_outbound(self, event: Event) -> None:
        """Send a Prizm outbound event to Telegram."""
        payload = event.payload
        chat_id = int(payload.get("chat_id", 0))
        text = payload.get("text", "")

        if not chat_id or not text:
            logger.warning("prizm/telegram: outbound event missing chat_id or text")
            return

        # Parse inline keyboard if present
        reply_markup = None
        keyboard_data = payload.get("keyboard")
        if keyboard_data:
            from telegram import InlineKeyboardButton, InlineKeyboardMarkup

            buttons = []
            for row in keyboard_data:
                row_buttons = [
                    InlineKeyboardButton(btn["text"], callback_data=btn["callback"])
                    for btn in row
                ]
                buttons.append(row_buttons)
            reply_markup = InlineKeyboardMarkup(buttons)

        # Send the message
        try:
            bot = self.app.bot
            sent = await bot.send_message(
                chat_id=chat_id,
                text=text,
                parse_mode=payload.get("parse_mode", "HTML"),
                reply_markup=reply_markup,
            )

            # Emit confirmation event
            await self.prizm.emit(
                "prizm.channel.sent.confirmation",
                {
                    "channel": "telegram",
                    "chat_id": str(chat_id),
                    "message_id": str(sent.message_id),
                    "correlation_id": event.correlation_id,
                },
                source="telegram",
                correlation_id=event.correlation_id,
                parent_id=event.id,
            )
            logger.debug(f"prizm/telegram: sent message to chat {chat_id}")

        except Exception as e:
            logger.error(f"prizm/telegram: failed to send message: {e}")