from datetime import datetime

from prizm.event import Event, EventMetadata


def test_event_defaults_and_unique_ids():
    first = Event(type="prizm.test", source="pytest")
    second = Event(type="prizm.test", source="pytest")

    assert first.id.startswith("evt_")
    assert first.id != second.id
    assert first.payload == {}
    assert first.metadata == EventMetadata()
    assert datetime.fromisoformat(first.timestamp.replace("Z", "+00:00"))


def test_event_round_trip_preserves_extra_fields():
    event = Event.model_validate(
        {
            "type": "prizm.test",
            "source": "pytest",
            "payload": {"ok": True},
            "future_schema_field": "preserved",
        }
    )

    restored = Event.model_validate_json(event.model_dump_json())
    assert restored.payload == {"ok": True}
    assert restored.future_schema_field == "preserved"
