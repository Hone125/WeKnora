-- Description: Drop embedding_cache table.
DO $$ BEGIN RAISE NOTICE '[Migration 000093] Dropping embedding_cache table'; END $$;

DROP TABLE IF EXISTS embedding_cache;
