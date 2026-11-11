use anyhow::{Context, Result, ensure};
use serde::{Deserialize, Serialize};
use std::path::{Component, Path, PathBuf};
use tokio::fs;

#[derive(Clone, Debug, Serialize, Deserialize)]
#[serde(default, deny_unknown_fields)]
pub struct Settings {
    pub sync_interval_minutes: u32,
    pub download_concurrency: u32,
    pub image_path: String,
    pub video_path: String,
    pub auto_download: bool,
    pub relationship_sync_enabled: bool,
    pub relationship_interval_minutes: u32,
    pub proxy_url: String,
}

impl Default for Settings {
    fn default() -> Self {
        Self {
            sync_interval_minutes: 60,
            download_concurrency: 3,
            image_path: "images".into(),
            video_path: "videos".into(),
            auto_download: true,
            relationship_sync_enabled: false,
            relationship_interval_minutes: 1440,
            proxy_url: String::new(),
        }
    }
}

impl Settings {
    pub fn redacted(&self) -> Self {
        let mut value = self.clone();
        if let Ok(mut proxy) = url::Url::parse(&value.proxy_url)
            && (!proxy.username().is_empty() || proxy.password().is_some())
        {
            let _ = proxy.set_username("********");
            let _ = proxy.set_password(None);
            value.proxy_url = proxy.to_string();
        }
        value
    }
    pub fn validate(&self) -> Result<()> {
        if !self.proxy_url.is_empty() {
            let proxy =
                url::Url::parse(&self.proxy_url).map_err(|_| anyhow::anyhow!("代理地址无效"))?;
            ensure!(
                self.proxy_url.len() <= 2048
                    && matches!(proxy.scheme(), "http" | "https" | "socks5" | "socks5h")
                    && proxy.host_str().is_some()
                    && proxy.query().is_none()
                    && proxy.fragment().is_none()
                    && matches!(proxy.path(), "" | "/"),
                "代理地址应为 HTTP、HTTPS 或 SOCKS5 地址"
            );
        }
        ensure!(
            (5..=43200).contains(&self.sync_interval_minutes),
            "同步间隔应在 5 至 43200 分钟之间"
        );
        ensure!(
            (1..=8).contains(&self.download_concurrency),
            "下载并发数应在 1 至 8 之间"
        );
        ensure!(
            (60..=43200).contains(&self.relationship_interval_minutes),
            "关系快照间隔应在 60 至 43200 分钟之间"
        );
        for value in [&self.image_path, &self.video_path] {
            let path = Path::new(value);
            ensure!(
                !value.is_empty()
                    && value.len() <= 200
                    && !path.is_absolute()
                    && path.components().all(|p| matches!(p, Component::Normal(_))),
                "保存路径必须为数据目录内的相对目录，不能包含 .."
            );
            ensure!(
                !value.starts_with('.') && !value.starts_with("thumbnails"),
                "保存目录不能使用保留名称"
            );
        }
        Ok(())
    }
}

pub async fn private_dir(path: &Path) -> Result<()> {
    fs::create_dir_all(path).await?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        fs::set_permissions(path, std::fs::Permissions::from_mode(0o700)).await?;
    }
    Ok(())
}

pub async fn private_write(path: &Path, content: &[u8]) -> Result<()> {
    use tokio::io::AsyncWriteExt;
    let temp = path.with_extension(format!("{}.tmp", uuid::Uuid::new_v4()));
    let mut options = fs::OpenOptions::new();
    options.write(true).create_new(true);
    #[cfg(unix)]
    options.mode(0o600);
    let mut file = options.open(&temp).await?;
    file.write_all(content).await?;
    file.sync_all().await?;
    drop(file);
    fs::rename(&temp, path).await?;
    Ok(())
}

pub async fn safe_path(root: &Path, relative: &str) -> Result<PathBuf> {
    let relative = Path::new(relative);
    ensure!(
        !relative.is_absolute()
            && relative
                .components()
                .all(|p| matches!(p, Component::Normal(_))),
        "路径不在数据目录中"
    );
    let root = fs::canonicalize(root).await?;
    let mut path = root.clone();
    for part in relative.components() {
        path.push(part);
        if let Ok(meta) = fs::symlink_metadata(&path).await {
            ensure!(!meta.file_type().is_symlink(), "数据目录中不能使用符号链接");
        }
    }
    ensure!(path.starts_with(root), "路径不在数据目录中");
    Ok(path)
}

pub async fn load_settings(config: &Path) -> Result<Settings> {
    let path = config.join("settings.json");
    if !path.exists() {
        let settings = Settings::default();
        private_write(&path, &serde_json::to_vec_pretty(&settings)?).await?;
        return Ok(settings);
    }
    let settings: Settings =
        serde_json::from_slice(&fs::read(path).await?).context("settings.json 格式错误")?;
    settings.validate()?;
    Ok(settings)
}
