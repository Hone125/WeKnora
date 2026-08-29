-- Description: Add embedding_cache table for persistent embedding vectors (M3 two-level cache).
DO $$ BEGIN RAISE NOTICE '[Migration 000093] Creating embedding_cache table'; END $$;

CREATE TABLE IF NOT EXISTS embedding_cache (
    cache_key  VARCHAR(64)  PRIMARY KEY,
    vector     BYTEA        NOT NULL,
    created_at TIMESTAMPTZ  NOT NULL DEFAULT now()
);
