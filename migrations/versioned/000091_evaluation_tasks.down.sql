-- Description: Drop evaluation_tasks table.
DO $$ BEGIN RAISE NOTICE '[Migration 000036] Dropping evaluation_tasks table'; END $$;

DROP TABLE IF EXISTS evaluation_tasks;
