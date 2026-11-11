use anyhow::{Result, ensure};
use serde::{Deserialize, Serialize};
use sqlx::{
    SqlitePool,
    sqlite::{SqliteConnectOptions, SqliteJournalMode, SqlitePoolOptions},
};
use std::{path::Path, time::Duration};
use uuid::Uuid;

#[derive(Clone)]
pub struct Store {
    pub pool: SqlitePool,
}

#[derive(Debug, Clone, Serialize, Deserialize, sqlx::FromRow)]
pub struct Task {
    pub id: String,
    pub kind: String,
    pub target: String,
    pub label: String,
    pub status: String,
    pub progress: i64,
    pub attempts: i64,
    pub max_attempts: i64,
    pub error: Option<String>,
    pub result: Option<String>,
    pub available_at: String,
    pub created_at: String,
    pub updated_at: String,
    pub parent_id: Option<String>,
    pub result_size: i64,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ExtractedTweet {
    pub id: String,
    pub author: String,
    pub author_name: String,
    pub text: String,
    pub published_at: String,
    pub media: Vec<ExtractedMedia>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ExtractedMedia {
    pub ordinal: i64,
    pub kind: String,
    pub extension: String,
    pub url: String,
}

impl Store {
    pub async fn open(path: &Path) -> Result<Self> {
        let options = SqliteConnectOptions::new()
            .filename(path)
            .create_if_missing(true)
            .foreign_keys(true)
            .journal_mode(SqliteJournalMode::Wal)
            .busy_timeout(Duration::from_secs(10));
        let pool = SqlitePoolOptions::new()
            .max_connections(5)
            .connect_with(options)
            .await?;
        sqlx::migrate!("./migrations").run(&pool).await?;
        Ok(Self { pool })
    }
    pub async fn memory() -> Result<Self> {
        let pool = SqlitePoolOptions::new()
            .max_connections(1)
            .connect("sqlite::memory:")
            .await?;
        sqlx::migrate!("./migrations").run(&pool).await?;
        Ok(Self { pool })
    }
    pub async fn enqueue(&self, kind: &str, target: &str, label: &str) -> Result<Task> {
        let mut tx = self.pool.begin_with("BEGIN IMMEDIATE").await?;
        let id = Uuid::new_v4().to_string();
        sqlx::query(
            "INSERT INTO tasks(id,kind,target,label) VALUES(?,?,?,?) ON CONFLICT DO NOTHING",
        )
        .bind(&id)
        .bind(kind)
        .bind(target)
        .bind(label)
        .execute(&mut *tx)
        .await?;
        let task = sqlx::query_as("SELECT * FROM tasks WHERE kind=? AND target=? AND status IN ('queued','running','cancelling') ORDER BY created_at LIMIT 1")
            .bind(kind).bind(target).fetch_one(&mut *tx).await?;
        tx.commit().await?;
        Ok(task)
    }
    pub async fn claim(&self, id: &str) -> Result<bool> {
        Ok(sqlx::query("UPDATE tasks SET status='running',attempts=attempts+1,error=NULL,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=? AND status='queued'")
            .bind(id).execute(&self.pool).await?.rows_affected() == 1)
    }
    pub async fn queue_download(&self, target: &str, label: &str) -> Result<Task> {
        self.queue_child_download(target, label, None).await
    }
    pub async fn queue_child_download(
        &self,
        target: &str,
        label: &str,
        parent_id: Option<&str>,
    ) -> Result<Task> {
        let mut tx = self.pool.begin_with("BEGIN IMMEDIATE").await?;
        let exists: i64 = sqlx::query_scalar("SELECT COUNT(*) FROM media WHERE id=?")
            .bind(target)
            .fetch_one(&mut *tx)
            .await?;
        ensure!(exists == 1, "媒体记录不存在");
        let active:Option<Task>=sqlx::query_as("SELECT * FROM tasks WHERE kind='download' AND target=? AND status IN ('queued','running','cancelling')").bind(target).fetch_optional(&mut *tx).await?;
        if let Some(active) = active {
            tx.commit().await?;
            return Ok(active);
        }
        let id = Uuid::new_v4().to_string();
        sqlx::query("UPDATE media SET status='pending',error=NULL WHERE id=?")
            .bind(target)
            .execute(&mut *tx)
            .await?;
        sqlx::query("INSERT INTO tasks(id,kind,target,label,parent_id) VALUES(?,'download',?,?,?)")
            .bind(&id)
            .bind(target)
            .bind(label)
            .bind(parent_id)
            .execute(&mut *tx)
            .await?;
        let task = sqlx::query_as("SELECT * FROM tasks WHERE id=?")
            .bind(id)
            .fetch_one(&mut *tx)
            .await?;
        tx.commit().await?;
        Ok(task)
    }
    pub async fn recover(&self) -> Result<()> {
        let mut tx = self.pool.begin_with("BEGIN IMMEDIATE").await?;
        sqlx::query("UPDATE tasks SET status='cancelled',error='取消已在服务重启时确认' WHERE status='cancelling'").execute(&mut *tx).await?;
        sqlx::query("UPDATE tasks SET status='queued',progress=0,error='服务重启，任务已重新排队',available_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE status='running'").execute(&mut *tx).await?;
        sqlx::query("UPDATE media SET status=CASE WHEN EXISTS(SELECT 1 FROM tasks WHERE kind='download' AND target=media.id AND status='queued') THEN 'pending' ELSE 'failed' END,error='服务重启，待恢复或手动重试' WHERE status='downloading'").execute(&mut *tx).await?;
        tx.commit().await?;
        Ok(())
    }
    pub async fn queue_automatic_downloads(&self) -> Result<()> {
        let mut tx = self.pool.begin_with("BEGIN IMMEDIATE").await?;
        let missing: Vec<String> = sqlx::query_scalar("SELECT id FROM media m WHERE status='pending' AND NOT EXISTS (SELECT 1 FROM tasks WHERE kind='download' AND target=m.id) LIMIT 50")
            .fetch_all(&mut *tx).await?;
        if !missing.is_empty() {
            let parent_id = Uuid::new_v4().to_string();
            // The batch is a summary row; workers execute its child downloads.
            sqlx::query("INSERT INTO tasks(id,kind,target,label,status,progress,attempts) VALUES(?,'download',?,'自动下载任务','succeeded',100,1)")
                .bind(&parent_id).bind(&parent_id).execute(&mut *tx).await?;
            for id in missing {
                sqlx::query("INSERT INTO tasks(id,kind,target,label,parent_id) VALUES(?,'download',?,'自动下载媒体',?)")
                    .bind(Uuid::new_v4().to_string()).bind(id).bind(&parent_id)
                    .execute(&mut *tx).await?;
            }
        }
        tx.commit().await?;
        Ok(())
    }
    pub async fn ingest(&self, tweets: &[ExtractedTweet]) -> Result<u64> {
        self.ingest_for_task(tweets, None).await
    }
    pub async fn ingest_for_task(
        &self,
        tweets: &[ExtractedTweet],
        parent_id: Option<&str>,
    ) -> Result<u64> {
        let mut tx = self.pool.begin_with("BEGIN IMMEDIATE").await?;
        if let Some(parent_id) = parent_id {
            let running: bool = sqlx::query_scalar("SELECT status='running' FROM tasks WHERE id=?")
                .bind(parent_id)
                .fetch_one(&mut *tx)
                .await?;
            ensure!(running, "任务已取消");
        }
        let mut inserted = 0;
        for tweet in tweets {
            let user_id: Option<String> =
                sqlx::query_scalar("SELECT id FROM users WHERE username=? COLLATE NOCASE")
                    .bind(&tweet.author)
                    .fetch_optional(&mut *tx)
                    .await?;
            inserted += sqlx::query("INSERT INTO tweets(id,user_id,author,author_name,text,published_at,url) VALUES(?,?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING")
                .bind(&tweet.id).bind(&user_id).bind(&tweet.author).bind(&tweet.author_name).bind(&tweet.text)
                .bind(&tweet.published_at).bind(format!("https://x.com/{}/status/{}", tweet.author, tweet.id))
                .execute(&mut *tx).await?.rows_affected();
            sqlx::query(
                "UPDATE tweets SET user_id=COALESCE(?,user_id),author_name=?,text=? WHERE id=?",
            )
            .bind(&user_id)
            .bind(&tweet.author_name)
            .bind(&tweet.text)
            .bind(&tweet.id)
            .execute(&mut *tx)
            .await?;
            for media in &tweet.media {
                sqlx::query("INSERT INTO media(id,tweet_id,ordinal,kind,extension,remote_url) VALUES(?,?,?,?,?,?) ON CONFLICT(tweet_id,ordinal) DO UPDATE SET remote_url=excluded.remote_url WHERE media.status!='ready'")
                    .bind(Uuid::new_v4().to_string()).bind(&tweet.id).bind(media.ordinal).bind(&media.kind).bind(&media.extension).bind(&media.url)
                    .execute(&mut *tx).await?;
                if let Some(parent_id) = parent_id {
                    let queued = sqlx::query("INSERT INTO tasks(id,kind,target,label,parent_id) SELECT ?,'download',m.id,'自动下载媒体',? FROM media m WHERE m.tweet_id=? AND m.ordinal=? AND m.status!='ready' AND NOT EXISTS (SELECT 1 FROM tasks d WHERE d.kind='download' AND d.target=m.id AND d.status IN ('queued','running','cancelling'))")
                        .bind(Uuid::new_v4().to_string()).bind(parent_id).bind(&tweet.id).bind(media.ordinal)
                        .execute(&mut *tx).await?;
                    if queued.rows_affected() > 0 {
                        sqlx::query("UPDATE media SET status='pending',error=NULL WHERE tweet_id=? AND ordinal=?")
                            .bind(&tweet.id).bind(media.ordinal).execute(&mut *tx).await?;
                    }
                }
            }
        }
        tx.commit().await?;
        Ok(inserted)
    }
}
