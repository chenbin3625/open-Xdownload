CREATE TABLE users (
    id TEXT PRIMARY KEY,
    username TEXT NOT NULL COLLATE NOCASE UNIQUE,
    name TEXT NOT NULL DEFAULT '',
    bio TEXT NOT NULL DEFAULT '',
    avatar TEXT,
    enabled INTEGER NOT NULL DEFAULT 1,
    sync_interval INTEGER,
    last_sync TEXT,
    last_error TEXT,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE TABLE tweets (
    id TEXT PRIMARY KEY,
    user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
    author TEXT NOT NULL,
    author_name TEXT NOT NULL DEFAULT '',
    text TEXT NOT NULL DEFAULT '',
    published_at TEXT NOT NULL,
    url TEXT NOT NULL,
    archived_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX tweets_user_date ON tweets(user_id, published_at DESC);

CREATE TABLE media (
    id TEXT PRIMARY KEY,
    tweet_id TEXT NOT NULL REFERENCES tweets(id) ON DELETE CASCADE,
    ordinal INTEGER NOT NULL,
    kind TEXT NOT NULL CHECK(kind IN ('image','video','gif')),
    extension TEXT NOT NULL,
    remote_url TEXT NOT NULL,
    file_path TEXT,
    thumbnail_path TEXT,
    status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','downloading','ready','failed')),
    error TEXT,
    size INTEGER NOT NULL DEFAULT 0,
    width INTEGER NOT NULL DEFAULT 0,
    height INTEGER NOT NULL DEFAULT 0,
    duration REAL,
    favorite INTEGER NOT NULL DEFAULT 0,
    UNIQUE(tweet_id, ordinal)
);
CREATE INDEX media_status ON media(status);

CREATE TABLE tags (id INTEGER PRIMARY KEY, name TEXT NOT NULL COLLATE NOCASE UNIQUE);
CREATE TABLE media_tags (
    media_id TEXT NOT NULL REFERENCES media(id) ON DELETE CASCADE,
    tag_id INTEGER NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
    PRIMARY KEY(media_id, tag_id)
);

CREATE TABLE tasks (
    id TEXT PRIMARY KEY,
    kind TEXT NOT NULL CHECK(kind IN ('tweet','sync','download','relationships','cookie_check')),
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
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE UNIQUE INDEX task_active ON tasks(kind,target) WHERE status IN ('queued','running','cancelling');
CREATE INDEX task_dispatch ON tasks(status, available_at);

CREATE TABLE snapshots (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    direction TEXT NOT NULL CHECK(direction IN ('following','followers')),
    status TEXT NOT NULL CHECK(status IN ('complete','partial','failed')),
    expected_count INTEGER,
    fetched_count INTEGER NOT NULL DEFAULT 0,
    error TEXT,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX snapshots_user ON snapshots(user_id,direction,created_at DESC);
CREATE TABLE relationships (
    snapshot_id TEXT NOT NULL REFERENCES snapshots(id) ON DELETE CASCADE,
    user_id TEXT NOT NULL,
    username TEXT NOT NULL,
    name TEXT NOT NULL DEFAULT '',
    PRIMARY KEY(snapshot_id,user_id)
);
