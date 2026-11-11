CREATE TABLE tasks_new (
    id TEXT PRIMARY KEY,
    kind TEXT NOT NULL CHECK(kind IN ('tweet','sync','download','relationships','cookie_check','list','following')),
    target TEXT NOT NULL,
    label TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'queued' CHECK(status IN ('queued','running','cancelling','succeeded','failed','cancelled')),
    progress INTEGER NOT NULL DEFAULT 0,
    attempts INTEGER NOT NULL DEFAULT 0,
    max_attempts INTEGER NOT NULL DEFAULT 3,
    error TEXT,
    result TEXT,
    available_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    parent_id TEXT REFERENCES tasks_new(id) ON DELETE SET NULL,
    result_size INTEGER NOT NULL DEFAULT 0
);
INSERT INTO tasks_new SELECT * FROM tasks;
DROP TABLE tasks;
ALTER TABLE tasks_new RENAME TO tasks;
CREATE UNIQUE INDEX task_active ON tasks(kind,target) WHERE status IN ('queued','running','cancelling');
CREATE INDEX task_dispatch ON tasks(status,available_at);
CREATE INDEX tasks_parent ON tasks(parent_id,target);
ALTER TABLE tasks ADD COLUMN cookie_id TEXT;

CREATE TABLE schedules (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 1,
    interval_minutes INTEGER NOT NULL CHECK(interval_minutes BETWEEN 5 AND 43200),
    items_json TEXT NOT NULL,
    last_run_at TEXT,
    next_run_at TEXT NOT NULL,
    last_task_ids_json TEXT NOT NULL DEFAULT '[]',
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX schedules_due ON schedules(enabled,next_run_at);

CREATE TABLE failed_tweets (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    kind TEXT NOT NULL CHECK(kind IN ('tweet','download')),
    target TEXT NOT NULL,
    tweet_id TEXT NOT NULL,
    error TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    UNIQUE(kind,target)
);
INSERT INTO failed_tweets(id,task_id,kind,target,tweet_id,error)
SELECT t.id,t.id,t.kind,t.target,CASE WHEN t.kind='tweet' THEN t.target ELSE m.tweet_id END,t.error
FROM tasks t LEFT JOIN media m ON t.kind='download' AND m.id=t.target
WHERE t.kind IN ('tweet','download') AND t.status IN ('failed','queued') AND t.error IS NOT NULL
AND (t.kind='tweet' OR m.id IS NOT NULL)
AND NOT EXISTS (SELECT 1 FROM tasks n WHERE n.kind=t.kind AND n.target=t.target
    AND (n.created_at>t.created_at OR (n.created_at=t.created_at AND n.rowid>t.rowid)));
