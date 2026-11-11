use crate::{app::App, config::private_write, domain, store::Task};
use anyhow::{Result, ensure};
use serde::{Deserialize, Serialize};
use std::collections::HashMap;

pub const KINDS: &[&str] = &[
    "tweet",
    "sync",
    "download",
    "relationships",
    "list",
    "following",
];

#[derive(Default)]
pub struct Rotation {
    pub cursors: HashMap<String, usize>,
    pub cooldowns: HashMap<String, (chrono::DateTime<chrono::Utc>, String)>,
}

#[derive(Clone, Serialize, Deserialize)]
pub struct PoolCookie {
    pub id: String,
    pub name: String,
    pub enabled: bool,
    pub kinds: Vec<String>,
    pub content: String,
    #[serde(default)]
    pub revision: String,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
pub struct CookieInput {
    pub name: String,
    pub enabled: bool,
    pub kinds: Vec<String>,
    #[serde(default)]
    pub cookie: String,
}

pub async fn load(app: &App) -> Result<Vec<PoolCookie>> {
    let path = app.config.join("cookie-pool.json");
    match tokio::fs::read(path).await {
        Ok(bytes) => Ok(serde_json::from_slice(&bytes)?),
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => Ok(Vec::new()),
        Err(e) => Err(e.into()),
    }
}

pub async fn available(app: &App) -> bool {
    app.config.join("cookies.txt").exists()
        || load(app)
            .await
            .is_ok_and(|pool| pool.iter().any(|c| c.enabled))
}

pub async fn list(app: &App) -> Result<serde_json::Value> {
    let pool = load(app).await?;
    let rotation = app.rotation.lock().await;
    Ok(serde_json::Value::Array(pool.iter().map(|c| {
        let cooldown = rotation.cooldowns.get(&c.id).filter(|(at, _)| *at > chrono::Utc::now());
        serde_json::json!({"id":c.id,"name":c.name,"enabled":c.enabled,"kinds":c.kinds,
            "cooldown_until":cooldown.map(|(at, _)| at.to_rfc3339()),"error":cooldown.map(|(_, e)| e)})
    }).collect()))
}

pub async fn save(app: &App, id: Option<&str>, input: CookieInput) -> Result<String> {
    ensure!(
        !input.name.trim().is_empty() && input.name.len() <= 100,
        "Cookie 名称应为 1 至 100 个字符"
    );
    ensure!(
        !input.kinds.is_empty() && input.kinds.iter().all(|k| KINDS.contains(&k.as_str())),
        "请选择有效的任务类型"
    );
    let content = if input.cookie.trim().is_empty() {
        None
    } else {
        Some(domain::cookies_from_header(&input.cookie)?)
    };
    let _guard = app.config_gate.lock().await;
    let mut pool = load(app).await?;
    let id = if let Some(id) = id {
        let entry = pool
            .iter_mut()
            .find(|c| c.id == id)
            .ok_or_else(|| anyhow::anyhow!("Cookie 不存在"))?;
        entry.name = input.name.trim().to_string();
        entry.enabled = input.enabled;
        entry.kinds = input.kinds;
        if let Some(content) = content {
            entry.content = content;
            entry.revision = uuid::Uuid::new_v4().to_string();
        }
        id.to_string()
    } else {
        ensure!(pool.len() < 20, "最多保存 20 组备用 Cookie");
        let content = content.ok_or_else(|| anyhow::anyhow!("请输入 Cookie"))?;
        ensure!(
            !pool.iter().any(|c| c.content == content),
            "该 Cookie 已在池中"
        );
        let id = uuid::Uuid::new_v4().to_string();
        pool.push(PoolCookie {
            id: id.clone(),
            name: input.name.trim().to_string(),
            enabled: input.enabled,
            kinds: input.kinds,
            content,
            revision: uuid::Uuid::new_v4().to_string(),
        });
        id
    };
    private_write(
        &app.config.join("cookie-pool.json"),
        &serde_json::to_vec_pretty(&pool)?,
    )
    .await?;
    app.rotation.lock().await.cooldowns.remove(&id);
    Ok(id)
}

pub async fn delete(app: &App, id: &str) -> Result<()> {
    let _guard = app.config_gate.lock().await;
    let mut pool = load(app).await?;
    let count = pool.len();
    pool.retain(|c| c.id != id);
    ensure!(pool.len() != count, "Cookie 不存在");
    private_write(
        &app.config.join("cookie-pool.json"),
        &serde_json::to_vec(&pool)?,
    )
    .await?;
    app.rotation.lock().await.cooldowns.remove(id);
    Ok(())
}

pub async fn assign(app: &App, task: &Task) -> Result<()> {
    let _guard = app.config_gate.lock().await;
    let pool = load(app).await?;
    let mut ids = Vec::new();
    if app.config.join("cookies.txt").exists() {
        ids.push("primary".to_string());
    }
    if task.kind != "cookie_check" {
        ids.extend(
            pool.iter()
                .filter(|c| c.enabled && c.kinds.contains(&task.kind))
                .map(|c| c.id.clone()),
        );
    }
    ensure!(
        !ids.is_empty(),
        "Cookie 池暂不可用：尚未设置适用于此任务的 Cookie"
    );
    let mut rotation = app.rotation.lock().await;
    let now = chrono::Utc::now();
    let cursor = *rotation.cursors.get(&task.kind).unwrap_or(&0);
    let choice = (0..ids.len()).map(|n| (cursor + n) % ids.len()).find(|i| {
        task.kind == "cookie_check"
            || rotation
                .cooldowns
                .get(&ids[*i])
                .is_none_or(|(at, _)| *at <= now)
    });
    let index =
        choice.ok_or_else(|| anyhow::anyhow!("Cookie 池触发频率限制或会话冷却，请稍后重试"))?;
    rotation.cursors.insert(task.kind.clone(), index + 1);
    let revision = pool
        .iter()
        .find(|c| c.id == ids[index])
        .map(|c| &c.revision);
    sqlx::query("UPDATE tasks SET cookie_id=?,cookie_revision=? WHERE id=?")
        .bind(&ids[index])
        .bind(revision)
        .bind(&task.id)
        .execute(&app.store.pool)
        .await?;
    Ok(())
}

pub async fn content(app: &App, task: Option<&str>) -> Result<Vec<u8>> {
    let id: Option<String> = if let Some(task) = task {
        sqlx::query_scalar("SELECT cookie_id FROM tasks WHERE id=?")
            .bind(task)
            .fetch_optional(&app.store.pool)
            .await?
            .flatten()
    } else {
        None
    };
    if let Some(id) = id.filter(|id| id != "primary") {
        let pool = load(app).await?;
        let entry = pool
            .iter()
            .find(|c| c.id == id)
            .ok_or_else(|| anyhow::anyhow!("任务使用的 Cookie 已删除"))?;
        if let Some(task) = task {
            sqlx::query("UPDATE tasks SET cookie_revision=? WHERE id=?")
                .bind(&entry.revision)
                .bind(task)
                .execute(&app.store.pool)
                .await?;
        }
        return Ok(entry.content.as_bytes().to_vec());
    }
    tokio::fs::read(app.config.join("cookies.txt"))
        .await
        .map_err(|_| anyhow::anyhow!("尚未设置 Cookie，请先在设置中填写自己的 X 登录 Cookie"))
}

pub async fn cool_down(
    app: &App,
    task: &Task,
    reason: &str,
    revision: Option<&str>,
) -> Result<bool> {
    let _guard = app.config_gate.lock().await;
    let (id, used_revision): (Option<String>, Option<String>) =
        sqlx::query_as("SELECT cookie_id,cookie_revision FROM tasks WHERE id=?")
            .bind(&task.id)
            .fetch_one(&app.store.pool)
            .await?;
    let primary = id.as_deref().is_none_or(|id| id == "primary");
    if primary
        && let Some(revision) = revision
        && app.cookie.read().await.revision != revision
    {
        return Ok(false);
    }
    if !primary {
        let pool = load(app).await?;
        if !pool
            .iter()
            .any(|c| Some(&c.id) == id.as_ref() && Some(&c.revision) == used_revision.as_ref())
        {
            return Ok(false);
        }
    }
    if let Some(id) = id {
        let seconds = if reason.contains("Cookie 无效") {
            3600
        } else {
            900
        };
        app.rotation.lock().await.cooldowns.insert(
            id,
            (
                chrono::Utc::now() + chrono::Duration::seconds(seconds),
                reason.to_string(),
            ),
        );
    }
    Ok(primary)
}
