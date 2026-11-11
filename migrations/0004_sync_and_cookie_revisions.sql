-- Existing archives need one complete pass before using an incremental cursor.
ALTER TABLE users ADD COLUMN sync_cursor TEXT;
ALTER TABLE tasks ADD COLUMN cookie_revision TEXT;
