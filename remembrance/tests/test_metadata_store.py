"""Tests for MetadataStore — ensure_project and FK-safe store_memory."""

from datetime import datetime, timezone

import pytest

from remembrance.stores.metadata_store import MetadataStore
from remembrance.models import Memory


@pytest.fixture
def store(tmp_path):
    """Create a MetadataStore with a fresh database."""
    db_path = tmp_path / "test_metadata.db"
    s = MetadataStore(str(db_path))
    return s


def _make_memory(project_id: str = "test-project", source_type: str = "agent") -> Memory:
    """Create a minimal Memory for testing."""
    now = datetime.now(timezone.utc).isoformat()
    return Memory(
        id="mem-001",
        project_id=project_id,
        user_id="system",
        scope="project",
        category="conversation",
        title="test memory",
        summary="test summary",
        content="test content",
        tags=[],
        importance_score=0.5,
        confidence_score=0.9,
        source_type=source_type,
        source_ref="",
        source_agent="prizm:lumi",
        status="active",
        created_at=now,
        updated_at=now,
        last_accessed_at=now,
        access_count=0,
        metadata=None,
    )


class TestEnsureProject:
    def test_creates_new_project(self, store):
        """ensure_project should create a project row when it doesn't exist."""
        store.ensure_project("new-project")
        row = store.conn.execute(
            "SELECT id, name FROM projects WHERE id = ?", ("new-project",)
        ).fetchone()
        assert row is not None
        assert row[0] == "new-project"
        assert row[1] == "new-project"

    def test_idempotent(self, store):
        """Calling ensure_project twice should not raise."""
        store.ensure_project("my-project")
        store.ensure_project("my-project")  # should not raise
        row = store.conn.execute(
            "SELECT id FROM projects WHERE id = ?", ("my-project",)
        ).fetchone()
        assert row is not None

    def test_does_not_overwrite_existing(self, store):
        """ensure_project should not overwrite an existing project's data."""
        now = datetime.now(timezone.utc).isoformat()
        store.conn.execute(
            """INSERT INTO projects (id, name, description, root_path, created_at, updated_at)
               VALUES (?, ?, ?, ?, ?, ?)""",
            ("existing", "Existing Project", "Original description", "/original/path", now, now),
        )
        store.conn.commit()

        store.ensure_project("existing")

        row = store.conn.execute(
            "SELECT name, description, root_path FROM projects WHERE id = ?",
            ("existing",),
        ).fetchone()
        # Should preserve original data
        assert row[0] == "Existing Project"
        assert row[1] == "Original description"
        assert row[2] == "/original/path"


class TestStoreMemoryFKSafe:
    def test_store_memory_auto_creates_project(self, store):
        """store_memory should auto-create the project if it doesn't exist."""
        memory = _make_memory(project_id="auto-created-project")
        result = store.store_memory(memory)
        assert result.id == "mem-001"

        # Verify project was auto-created
        row = store.conn.execute(
            "SELECT id FROM projects WHERE id = ?", ("auto-created-project",)
        ).fetchone()
        assert row is not None

    def test_store_memory_with_existing_project(self, store):
        """store_memory should work fine when the project already exists."""
        store.ensure_project("existing-project")
        memory = _make_memory(project_id="existing-project")
        result = store.store_memory(memory)
        assert result.id == "mem-001"

    def test_store_memory_valid_source_type(self, store):
        """store_memory should accept all valid source_type values."""
        for i, source_type in enumerate(("manual", "agent", "pipeline", "event")):
            memory = _make_memory(project_id=f"test-{source_type}", source_type=source_type)
            memory.id = f"mem-src-{i}"  # unique ID per iteration
            result = store.store_memory(memory)
            assert result.id == f"mem-src-{i}"