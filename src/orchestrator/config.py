"""Configuration via environment variables with Pydantic BaseSettings."""

from pathlib import Path

from pydantic_settings import BaseSettings


class Settings(BaseSettings):
    """Application settings loaded from environment variables."""

    model_config = {"env_file": ".env", "env_file_encoding": "utf-8"}

    DATABASE_URL: str = "sqlite+aiosqlite:///./orchestrator.db"
    REDIS_URL: str = "redis://localhost:6379"
    WT_BIN: Path = Path.home() / "git" / "godinj-dotfiles.git" / "wt" / "wt.sh"
    CLAUDE_BIN: str = "claude"
    MAX_CONCURRENT_AGENTS: int = 5
    CONTEXT_COMPACTION_THRESHOLD: float = 0.7


settings = Settings()
