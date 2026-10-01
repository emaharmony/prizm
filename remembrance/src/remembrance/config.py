"""Configuration loader for Remembrance."""

from __future__ import annotations

import os
from pathlib import Path
from dataclasses import dataclass, field
from typing import Optional

import yaml


@dataclass
class ServerConfig:
    host: str = "127.0.0.1"
    port: int = 18790


@dataclass
class DatabaseConfig:
    metadata_path: str = "./remembrance-data/metadata.db"
    vector_path: str = "./remembrance-data/vectors"


@dataclass
class EmbeddingConfig:
    provider: str = "ollama"
    model: str = "nomic-embed-text"
    ollama_url: str = "http://localhost:11434"
    dimensions: int = 768


@dataclass
class HybridWeights:
    vector_similarity: float = 0.65
    keyword_score: float = 0.15
    importance_score: float = 0.10
    project_match: float = 0.05
    recency_score: float = 0.05


@dataclass
class SearchConfig:
    hybrid_weights: HybridWeights = field(default_factory=HybridWeights)
    default_limit: int = 8
    project_mismatch_penalty: float = 0.9


@dataclass
class ContextConfig:
    max_memories: int = 8
    default_token_budget: int = 2500
    max_token_budget: int = 8000
    output_format: str = "both"


@dataclass
class DefaultsConfig:
    scope: str = "project"
    importance_score: float = 0.5
    confidence_score: float = 1.0


@dataclass
class RemembranceConfig:
    server: ServerConfig = field(default_factory=ServerConfig)
    database: DatabaseConfig = field(default_factory=DatabaseConfig)
    embedding: EmbeddingConfig = field(default_factory=EmbeddingConfig)
    search: SearchConfig = field(default_factory=SearchConfig)
    context: ContextConfig = field(default_factory=ContextConfig)
    defaults: DefaultsConfig = field(default_factory=DefaultsConfig)


def load_config(path: Optional[str] = None) -> RemembranceConfig:
    """Load configuration from YAML file, with environment variable overrides."""
    if path is None:
        # Search in order: env var, current dir, configs dir
        candidates = [
            os.environ.get("REMEMBRANCE_CONFIG"),
            "remembrance.local.yaml",
            "remembrance.yaml",
            str(Path(__file__).parent.parent.parent / "configs" / "remembrance.yaml.example"),
        ]
        for candidate in candidates:
            if candidate and Path(candidate).exists():
                path = candidate
                break

    config = RemembranceConfig()

    if path and Path(path).exists():
        with open(path) as f:
            data = yaml.safe_load(f) or {}

        if "server" in data:
            for k, v in data["server"].items():
                setattr(config.server, k, v)
        if "database" in data:
            for k, v in data["database"].items():
                setattr(config.database, k, v)
        if "embedding" in data:
            for k, v in data["embedding"].items():
                setattr(config.embedding, k, v)
        if "search" in data:
            if "hybrid_weights" in data["search"]:
                for k, v in data["search"]["hybrid_weights"].items():
                    setattr(config.search.hybrid_weights, k, v)
            for k, v in data["search"].items():
                if k != "hybrid_weights":
                    setattr(config.search, k, v)
        if "context" in data:
            for k, v in data["context"].items():
                setattr(config.context, k, v)
        if "defaults" in data:
            for k, v in data["defaults"].items():
                setattr(config.defaults, k, v)

    # Environment variable overrides
    if os.environ.get("REMEMBRANCE_DB_PATH"):
        config.database.metadata_path = os.environ["REMEMBRANCE_DB_PATH"]
    if os.environ.get("REMEMBRANCE_OLLAMA_URL"):
        config.embedding.ollama_url = os.environ["REMEMBRANCE_OLLAMA_URL"]
    if os.environ.get("REMEMBRANCE_PORT"):
        config.server.port = int(os.environ["REMEMBRANCE_PORT"])

    return config