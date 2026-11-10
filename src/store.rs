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
        sqlx::query("INSERT INTO tasks(id,kind,target,label) VALUES(?,'download',?,?)")
            .bind(&id)
            .bind(target)
            .bind(label)
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
    pub async fn ingest(&self, tweets: &[ExtractedTweet]) -> Result<u64> {
        let mut tx = self.pool.begin_with("BEGIN IMMEDIATE").await?;
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
            }
        }
        tx.commit().await?;
        Ok(inserted)
    }
}
