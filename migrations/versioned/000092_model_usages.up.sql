-- Description: Add model_usages table for per-call LLM usage ledger (M2 cost observability).
DO $$ BEGIN RAISE NOTICE '[Migration 000092] Creating model_usages table'; END $$;

CREATE TABLE IF NOT EXISTS model_usages (
    id                 VARCHAR(64)  PRIMARY KEY,
    tenant_id          BIGINT       NOT NULL,
    model_id           VARCHAR(64)  NOT NULL DEFAULT '',
    model_type         VARCHAR(32)  NOT NULL DEFAULT 'chat',
    purpose            VARCHAR(64)  NOT NULL DEFAULT '',
    session_id         VARCHAR(64)  NOT NULL DEFAULT '',
    message_id         VARCHAR(64)  NOT NULL DEFAULT '',
    knowledge_id       VARCHAR(64)  NOT NULL DEFAULT '',
    prompt_tokens      BIGINT       NOT NULL DEFAULT 0,
    completion_tokens  BIGINT       NOT NULL DEFAULT 0,
    total_tokens       BIGINT       NOT NULL DEFAULT 0,
    cached_tokens      BIGINT       NOT NULL DEFAULT 0,
    cache_read_tokens  BIGINT       NOT NULL DEFAULT 0,
    cache_write_tokens BIGINT       NOT NULL DEFAULT 0,
    cache_miss_tokens  BIGINT       NOT NULL DEFAULT 0,
    cache_status       VARCHAR(32)  NOT NULL DEFAULT '',
    duration_ms        BIGINT       NOT NULL DEFAULT 0,
    created_at         TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_model_usages_query ON model_usages (tenant_id, model_id, created_at);
