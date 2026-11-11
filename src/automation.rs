use crate::{
    domain,
    store::{Store, Task},
};
use anyhow::{Context, Result, ensure};
use serde::{Deserialize, Serialize};
use sqlx::SqliteConnection;

#[derive(Clone, Debug, Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
pub struct ArchiveTarget {
    pub kind: String,
    pub target: String,
}

pub fn normalize(items: &[ArchiveTarget]) -> Result<Vec<ArchiveTarget>> {
    ensure!(
        !items.is_empty() && items.len() <= 200,
        "一次应提交 1 至 200 个目标"
    );
    let mut seen = std::collections::HashSet::new();
    let mut result = Vec::new();
    for (index, item) in items.iter().enumerate() {
        let parsed = match item.kind.as_str() {
            "user" | "following" => domain::username(&item.target),
            "list" => domain::list_id(&item.target),
            "tweet" => {
                let target = item.target.trim();
                if !target.is_empty() && target.bytes().all(|c| c.is_ascii_digit()) {
                    domain::tweet_id(&format!("https://x.com/i/web/status/{target}"))
                } else {
                    domain::tweet_id(target)
                }
            }
            _ => anyhow::bail!("第 {} 个目标的任务类型无效", index + 1),
        }
        .with_context(|| format!("第 {} 个目标无效", index + 1))?;
        if seen.insert((item.kind.clone(), parsed.clone())) {
            result.push(ArchiveTarget {
                kind: item.kind.clone(),
                target: parsed,
            });
        }
    }
    Ok(result)
}

pub async fn queue_targets(
    conn: &mut SqliteConnection,
    items: &[ArchiveTarget],
) -> Result<Vec<Task>> {
    let mut tasks = Vec::new();
    for item in items {
        let mut kind = item.kind.as_str();
        let mut target = item.target.clone();
        let label = match kind {
            "user" => {
                sqlx::query("INSERT INTO users(id,username) VALUES(?,?) ON CONFLICT(username) DO UPDATE SET enabled=1")
                    .bind(uuid::Uuid::new_v4().to_string()).bind(&target).execute(&mut *conn).await?;
                target = sqlx::query_scalar("SELECT id FROM users WHERE username=?")
                    .bind(&target)
                    .fetch_one(&mut *conn)
                    .await?;
                sqlx::query("UPDATE tweets SET user_id=? WHERE author=? COLLATE NOCASE")
                    .bind(&target)
                    .bind(&item.target)
                    .execute(&mut *conn)
                    .await?;
                kind = "sync";
                format!("同步 @{}", item.target)
            }
            "following" => format!("归档 @{} 关注的用户", item.target),
            "list" => format!("列表 {} 成员归档", item.target),
            _ => format!("归档推文 {}", item.target),
        };
        sqlx::query(
            "INSERT INTO tasks(id,kind,target,label) VALUES(?,?,?,?) ON CONFLICT DO NOTHING",
        )
        .bind(uuid::Uuid::new_v4().to_string())
        .bind(kind)
        .bind(&target)
        .bind(label)
        .execute(&mut *conn)
        .await?;
        tasks.push(sqlx::query_as("SELECT * FROM tasks WHERE kind=? AND target=? AND status IN ('queued','running','cancelling')")
            .bind(kind).bind(target).fetch_one(&mut *conn).await?);
    }
    Ok(tasks)
}

pub async fn enqueue(store: &Store, items: &[ArchiveTarget]) -> Result<Vec<Task>> {
    let items = normalize(items)?;
    let mut tx = store.pool.begin_with("BEGIN IMMEDIATE").await?;
    let tasks = queue_targets(&mut tx, &items).await?;
    tx.commit().await?;
    Ok(tasks)
}

#[derive(Deserialize, Serialize, sqlx::FromRow)]
pub struct Schedule {
    pub id: String,
    pub name: String,
    pub enabled: bool,
    pub interval_minutes: i64,
    #[serde(skip_serializing)]
    pub items_json: String,
    pub last_run_at: Option<String>,
    pub next_run_at: String,
    #[serde(skip_serializing)]
    pub last_task_ids_json: String,
    pub created_at: String,
    pub updated_at: String,
}

impl Schedule {
    pub fn value(&self) -> Result<serde_json::Value> {
        let mut value = serde_json::to_value(self)?;
        value["items"] = serde_json::from_str(&self.items_json)?;
        value["last_task_ids"] = serde_json::from_str(&self.last_task_ids_json)?;
        Ok(value)
    }
}

pub async fn run_schedule(store: &Store, id: &str, due_only: bool) -> Result<Vec<Task>> {
    let mut tx = store.pool.begin_with("BEGIN IMMEDIATE").await?;
    let schedule: Option<Schedule> = sqlx::query_as("SELECT * FROM schedules WHERE id=? AND (?=0 OR (enabled=1 AND next_run_at<=strftime('%Y-%m-%dT%H:%M:%fZ','now')))")
        .bind(id).bind(due_only).fetch_optional(&mut *tx).await?;
    let Some(schedule) = schedule else {
        ensure!(due_only, "计划不存在");
        return Ok(Vec::new());
    };
    let items: Vec<ArchiveTarget> = serde_json::from_str(&schedule.items_json)?;
    let tasks = queue_targets(&mut tx, &normalize(&items)?).await?;
    let ids: Vec<_> = tasks.iter().map(|t| &t.id).collect();
    sqlx::query("UPDATE schedules SET last_run_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),next_run_at=?,last_task_ids_json=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?")
        .bind((chrono::Utc::now() + chrono::Duration::minutes(schedule.interval_minutes)).to_rfc3339_opts(chrono::SecondsFormat::Millis, true))
        .bind(serde_json::to_string(&ids)?).bind(id).execute(&mut *tx).await?;
    tx.commit().await?;
    Ok(tasks)
}
