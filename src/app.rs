use crate::{
    config::{Settings, load_settings, private_dir, private_write},
    store::Store,
};
use anyhow::{Result, ensure};
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
            reason: "请导入自己的 X 登录 Cookie".into(),
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
        let settings = load_settings(&config).await?;
        let store = Store::open(&data.join("archive.sqlite")).await?;
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
        // Only the current upload may change the visible session status.
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
