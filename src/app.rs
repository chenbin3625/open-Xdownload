use crate::{
    config::{Settings, load_settings, private_dir, private_write},
    store::Store,
};
use anyhow::{Context, Result, ensure};
use serde::{Deserialize, Serialize};
use std::{path::PathBuf, sync::Arc, time::Duration};
use tokio::sync::{Mutex, RwLock};

#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct CookieStatus {
    pub status: String,
    pub reason: String,
    pub checked_at: Option<String>,
    #[serde(skip_serializing)]
    pub revision: String,
}

impl Default for CookieStatus {
    fn default() -> Self {
        Self {
            status: "missing".into(),
            reason: "请填写自己的 X 登录 Cookie".into(),
            checked_at: None,
            revision: String::new(),
        }
    }
}

#[derive(Clone)]
pub struct App {
    pub store: Store,
    pub data: PathBuf,
    pub config: PathBuf,
    pub settings: Arc<RwLock<Settings>>,
    pub cookie: Arc<RwLock<CookieStatus>>,
    pub config_gate: Arc<Mutex<()>>,
    pub rotation: Arc<Mutex<crate::sessions::Rotation>>,
    pub client: reqwest::Client,
    pub password: Option<String>,
    pub session_token: String,
    pub gallery_dl: String,
    pub yt_dlp: String,
}

impl App {
    pub async fn new(data: PathBuf, config: PathBuf, password: Option<String>) -> Result<Self> {
        private_dir(&data).await?;
        private_dir(&config).await?;
        let data = tokio::fs::canonicalize(data).await?;
        let config = tokio::fs::canonicalize(config).await?;
        ensure!(data != config, "数据与配置目录不能相同");
        migrate_legacy_database(&data, &config).await?;
        let settings = load_settings(&config).await?;
        let store = Store::open(&config.join("archive.sqlite")).await?;
        let cookie = if config.join("cookies.txt").exists() {
            #[cfg(unix)]
            {
                use std::os::unix::fs::PermissionsExt;
                tokio::fs::set_permissions(
                    config.join("cookies.txt"),
                    std::fs::Permissions::from_mode(0o600),
                )
                .await?;
            }
            CookieStatus {
                status: "unknown".into(),
                reason: "Cookie 已保存，请检测当前会话".into(),
                checked_at: None,
                revision: uuid::Uuid::new_v4().to_string(),
            }
        } else {
            CookieStatus::default()
        };
        let client = reqwest::Client::builder()
            .timeout(Duration::from_secs(30))
            .redirect(reqwest::redirect::Policy::none())
            .build()?;
        Ok(Self {
            store,
            data,
            config,
            settings: Arc::new(RwLock::new(settings)),
            cookie: Arc::new(RwLock::new(cookie)),
            config_gate: Arc::new(Mutex::new(())),
            rotation: Arc::new(Mutex::new(crate::sessions::Rotation::default())),
            client,
            password,
            session_token: format!(
                "{}{}",
                uuid::Uuid::new_v4().simple(),
                uuid::Uuid::new_v4().simple()
            ),
            gallery_dl: std::env::var("GALLERY_DL_BIN").unwrap_or_else(|_| "gallery-dl".into()),
            yt_dlp: std::env::var("YT_DLP_BIN").unwrap_or_else(|_| "yt-dlp".into()),
        })
    }
    pub async fn set_cookie_status(&self, status: CookieStatus) -> Result<()> {
        // Only the current saved session may change the visible session status.
        let mut current = self.cookie.write().await;
        if current.revision == status.revision {
            private_write(
                &self.config.join("cookie-status.json"),
                &serde_json::to_vec(&status)?,
            )
            .await?;
            *current = status;
        }
        Ok(())
    }
}

async fn migrate_legacy_database(data: &std::path::Path, config: &std::path::Path) -> Result<()> {
    use sqlx::{Connection, sqlite::SqliteConnectOptions};

    let source = data.join("archive.sqlite");
    if !source.try_exists()? {
        return Ok(());
    }
    let legacy_lock_path = data.join(".instance.lock");
    let legacy_lock = std::fs::OpenOptions::new()
        .create(true)
        .truncate(false)
        .write(true)
        .open(&legacy_lock_path)?;
    fs2::FileExt::try_lock_exclusive(&legacy_lock)
        .context("迁移数据库前请先停止使用此数据目录的旧实例")?;
    let destination = config.join("archive.sqlite");
    ensure!(
        !destination.try_exists()?,
        "数据目录和配置目录中均存在 archive.sqlite，请先备份并确认要使用的数据库，再移除另一份"
    );

    // SQLite copies a consistent snapshot, including WAL, across mounted volumes.
    let temporary = config.join(format!("archive-{}.sqlite.tmp", uuid::Uuid::new_v4()));
    let mut connection = sqlx::SqliteConnection::connect_with(
        &SqliteConnectOptions::new()
            .filename(&source)
            .read_only(true),
    )
    .await?;
    let result = sqlx::query("VACUUM INTO ?")
        .bind(temporary.to_string_lossy().as_ref())
        .execute(&mut connection)
        .await;
    connection.close().await?;
    if let Err(error) = result {
        let _ = tokio::fs::remove_file(&temporary).await;
        return Err(error).context("迁移旧数据库失败，原数据库已保留");
    }
    tokio::fs::File::open(&temporary).await?.sync_all().await?;
    tokio::fs::rename(&temporary, &destination).await?;
    #[cfg(unix)]
    tokio::fs::File::open(config).await?.sync_all().await?;
    tokio::fs::remove_file(&source).await?;
    for name in ["archive.sqlite-wal", "archive.sqlite-shm"] {
        let path = data.join(name);
        if path.try_exists()? {
            tokio::fs::remove_file(path).await?;
        }
    }
    drop(legacy_lock);
    tokio::fs::remove_file(legacy_lock_path).await?;
    tracing::info!(from = %source.display(), to = %destination.display(), "数据库已迁移至配置目录");
    Ok(())
}
