-- Mirrors versioned migration 000093_embedding_cache:
-- persistent embedding vectors (M3 two-level cache).

CREATE TABLE IF NOT EXISTS embedding_cache (
    cache_key  VARCHAR(64)  PRIMARY KEY,
    vector     BLOB         NOT NULL,
    created_at DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP
);
