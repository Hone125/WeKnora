-- Description: Add evaluation_tasks table for persisting evaluation runs (config snapshot + metrics + cost/latency).
DO $$ BEGIN RAISE NOTICE '[Migration 000091] Creating evaluation_tasks table'; END $$;

CREATE TABLE IF NOT EXISTS evaluation_tasks (
    id                VARCHAR(255) PRIMARY KEY,
    tenant_id         BIGINT       NOT NULL,
    dataset_id        VARCHAR(255) NOT NULL DEFAULT '',
    status            INT          NOT NULL DEFAULT 0,
    err_msg           TEXT         NOT NULL DEFAULT '',
    total             INT          NOT NULL DEFAULT 0,
    finished          INT          NOT NULL DEFAULT 0,
    start_time        TIMESTAMPTZ  NOT NULL DEFAULT now(),
    end_time          TIMESTAMPTZ,
    params            JSONB,
    metric            JSONB,
    prompt_tokens     BIGINT       NOT NULL DEFAULT 0,
    completion_tokens BIGINT       NOT NULL DEFAULT 0,
    total_tokens      BIGINT       NOT NULL DEFAULT 0,
    latency_ms        BIGINT       NOT NULL DEFAULT 0,
    created_at        TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_evaluation_tasks_tenant ON evaluation_tasks (tenant_id);
