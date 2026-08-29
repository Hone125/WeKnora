-- Mirrors versioned migration 000091_evaluation_tasks:
-- evaluation task persistence (config snapshot + metrics + cost/latency).

CREATE TABLE IF NOT EXISTS evaluation_tasks (
    id                VARCHAR(255) PRIMARY KEY,
    tenant_id         BIGINT        NOT NULL,
    dataset_id        VARCHAR(255)  NOT NULL DEFAULT '',
    status            INTEGER       NOT NULL DEFAULT 0,
    err_msg           TEXT          NOT NULL DEFAULT '',
    total             INTEGER       NOT NULL DEFAULT 0,
    finished          INTEGER       NOT NULL DEFAULT 0,
    start_time        DATETIME      NOT NULL DEFAULT CURRENT_TIMESTAMP,
    end_time          DATETIME,
    params            TEXT,
    metric            TEXT,
    prompt_tokens     BIGINT        NOT NULL DEFAULT 0,
    completion_tokens BIGINT        NOT NULL DEFAULT 0,
    total_tokens      BIGINT        NOT NULL DEFAULT 0,
    latency_ms        BIGINT        NOT NULL DEFAULT 0,
    created_at        DATETIME      NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at        DATETIME      NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_evaluation_tasks_tenant ON evaluation_tasks (tenant_id);
