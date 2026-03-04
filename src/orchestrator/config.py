"""Configuration via environment variables with Pydantic BaseSettings."""

from pydantic_settings import BaseSettings


class Settings(BaseSettings):
    DATABASE_URL: str = "sqlite+aiosqlite:///./orchestrator.db"
    REDIS_URL: str = "redis://localhost:6379"
    WT_BIN: str = "~/git/godinj-dotfiles.git/wt/wt.sh"
    CLAUDE_BIN: str = "claude"
    MAX_CONCURRENT_AGENTS: int = 5
    CONTEXT_COMPACTION_THRESHOLD: float = 0.7

    model_config = {"env_prefix": "", "env_file": ".env"}


settings = Settings()
