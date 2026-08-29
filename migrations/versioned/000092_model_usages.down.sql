-- Description: Drop model_usages table.
DO $$ BEGIN RAISE NOTICE '[Migration 000092] Dropping model_usages table'; END $$;

DROP TABLE IF EXISTS model_usages;
