use anyhow::{Context, Result, bail, ensure};
use serde::{Deserialize, Serialize};
use std::path::{Component, Path, PathBuf};
use tokio::fs;

const TEMPLATE_PLACEHOLDERS: &[&str] = &[
    "data",
    "kind",
    "author",
    "tweet_id",
    "ordinal",
    "extension",
    "media_id",
];

const RESERVED_COMPONENTS: &[&str] = &[".staging", ".sessions", ".instance.lock", "archive.sqlite"];

#[derive(Clone, Debug, Serialize, Deserialize)]
#[serde(default, deny_unknown_fields)]
pub struct Settings {
    pub sync_interval_minutes: u32,
    pub download_concurrency: u32,
    pub image_path: String,
    pub video_path: String,
    pub thumbnail_path: String,
    pub image_path_template: String,
    pub video_path_template: String,
    pub thumbnail_path_template: String,
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
            thumbnail_path: "thumbnails".into(),
            image_path_template: "{author}/{tweet_id}_{ordinal}.{extension}".into(),
            video_path_template: "{author}/{tweet_id}_{ordinal}.{extension}".into(),
            thumbnail_path_template: "{author}/{media_id}.{extension}".into(),
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
        for (value, label) in [
            (&self.image_path, "图片根目录"),
            (&self.video_path, "视频根目录"),
            (&self.thumbnail_path, "缩略图根目录"),
        ] {
            validate_root(value, label)?;
        }
        for (value, label, thumbnail) in [
            (&self.image_path_template, "图片路径模板", false),
            (&self.video_path_template, "视频路径模板", false),
            (&self.thumbnail_path_template, "缩略图路径模板", true),
        ] {
            validate_template(value, label, thumbnail)?;
        }
        Ok(())
    }
}

fn check_placeholders(value: &str, allowed: &[&str], label: &str) -> Result<()> {
    let mut rest = value;
    while let Some(open) = rest.find('{') {
        ensure!(!rest[..open].contains('}'), "{label}包含未配对的 }}");
        let after = &rest[open + 1..];
        let close = after
            .find('}')
            .ok_or_else(|| anyhow::anyhow!("{label}包含未闭合的占位符"))?;
        ensure!(!after[..close].contains('{'), "{label}包含嵌套占位符");
        let token = &after[..close];
        ensure!(
            allowed.contains(&token),
            "{label}包含不支持的占位符 {{{token}}}"
        );
        rest = &after[close + 1..];
    }
    ensure!(!rest.contains('}'), "{label}包含未配对的 }}");
    Ok(())
}

fn check_components(value: &str, label: &str) -> Result<()> {
    for component in Path::new(value).components() {
        match component {
            Component::Normal(name) => {
                let name = name.to_string_lossy();
                ensure!(
                    !RESERVED_COMPONENTS.contains(&name.as_ref()),
                    "{label}不能使用保留名称 {name}"
                );
            }
            Component::RootDir | Component::Prefix(_) => {}
            _ => bail!("{label}不能包含 . 或 .."),
        }
    }
    Ok(())
}

fn validate_root(value: &str, label: &str) -> Result<()> {
    ensure!(
        !value.is_empty() && value.len() <= 300,
        "{label}不能为空且不能超过 300 个字符"
    );
    ensure!(
        !value.chars().any(char::is_control),
        "{label}不能包含控制字符"
    );
    check_placeholders(value, &["data"], label)?;
    ensure!(
        !value.contains('{') || value.starts_with("{data}"),
        "{label}中的 {{data}} 必须位于开头"
    );
    let expanded = value.replace("{data}", "/data");
    check_components(&expanded, label)?;
    if !Path::new(&expanded).is_absolute() {
        ensure!(!expanded.starts_with('.'), "{label}不能位于隐藏目录");
    }
    Ok(())
}

fn validate_template(value: &str, label: &str, thumbnail: bool) -> Result<()> {
    ensure!(
        !value.is_empty() && value.len() <= 300,
        "{label}不能为空且不能超过 300 个字符"
    );
    ensure!(
        !value.chars().any(char::is_control),
        "{label}不能包含控制字符"
    );
    ensure!(!value.contains('\\'), "{label}请使用 / 作为路径分隔符");
    ensure!(!value.ends_with('/'), "{label}不能以 / 结尾");
    check_placeholders(value, TEMPLATE_PLACEHOLDERS, label)?;
    ensure!(
        !value.contains("{data}") || value.starts_with("{data}"),
        "{label}中的 {{data}} 必须位于开头"
    );
    check_components(&expand_samples(value), label)?;
    ensure!(
        value.contains("{extension}"),
        "{label}必须包含 {{extension}} 占位符"
    );
    if thumbnail {
        ensure!(
            value.contains("{media_id}"),
            "{label}必须包含 {{media_id}} 占位符"
        );
    } else {
        ensure!(
            value.contains("{media_id}")
                || (value.contains("{tweet_id}") && value.contains("{ordinal}")),
            "{label}必须包含 {{media_id}}，或同时包含 {{tweet_id}} 与 {{ordinal}}，以免文件互相覆盖"
        );
    }
    Ok(())
}

fn expand_samples(value: &str) -> String {
    value
        .replace("{data}", "/data")
        .replace("{kind}", "image")
        .replace("{author}", "alice")
        .replace("{tweet_id}", "1234567890")
        .replace("{ordinal}", "1")
        .replace("{extension}", "jpg")
        .replace("{media_id}", "00000000-0000-0000-0000-000000000000")
}

pub struct MediaTokens<'a> {
    pub data: &'a Path,
    pub kind: &'a str,
    pub author: &'a str,
    pub tweet_id: &'a str,
    pub ordinal: i64,
    pub extension: &'a str,
    pub media_id: &'a str,
}

impl MediaTokens<'_> {
    pub fn expand(&self, template: &str) -> Result<String> {
        let data = self.data.to_string_lossy();
        let ordinal = self.ordinal.to_string();
        let mut value = template.to_string();
        for (name, replacement) in [
            ("data", data.as_ref()),
            ("kind", self.kind),
            ("author", self.author),
            ("tweet_id", self.tweet_id),
            ("ordinal", ordinal.as_str()),
            ("extension", self.extension),
            ("media_id", self.media_id),
        ] {
            value = value.replace(&format!("{{{name}}}"), replacement);
        }
        ensure!(
            !value.contains('{') && !value.contains('}'),
            "路径模板包含未知占位符"
        );
        Ok(value)
    }
}

pub fn resolve_root(data: &Path, root: &str) -> PathBuf {
    let expanded = root.replace("{data}", &data.to_string_lossy());
    let path = PathBuf::from(expanded);
    if path.is_absolute() {
        path
    } else {
        data.join(path)
    }
}

pub fn media_path(
    data: &Path,
    root: &str,
    template: &str,
    tokens: &MediaTokens<'_>,
) -> Result<PathBuf> {
    let expanded = tokens.expand(template)?;
    let relative = Path::new(&expanded);
    ensure!(
        relative.components().all(|p| matches!(
            p,
            Component::Normal(_) | Component::RootDir | Component::Prefix(_)
        )),
        "路径模板不能包含 . 或 .."
    );
    Ok(if relative.is_absolute() {
        relative.to_path_buf()
    } else {
        resolve_root(data, root).join(relative)
    })
}

/// Store paths inside the data directory as relative paths for portability, but
/// keep paths configured outside the data directory absolute.
pub fn stored_path(data: &Path, path: &Path) -> String {
    path.strip_prefix(data)
        .unwrap_or(path)
        .to_string_lossy()
        .replace('\\', "/")
}

pub async fn stored_path_from_record(data: &Path, stored: &str) -> Result<PathBuf> {
    let path = Path::new(stored);
    if path.is_absolute() {
        Ok(path.to_path_buf())
    } else {
        safe_path(data, stored).await
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

#[cfg(test)]
mod tests {
    use super::*;

    fn tokens<'a>(data: &'a Path, media_id: &'a str) -> MediaTokens<'a> {
        MediaTokens {
            data,
            kind: "image",
            author: "alice",
            tweet_id: "123",
            ordinal: 1,
            extension: "jpg",
            media_id,
        }
    }

    #[test]
    fn older_settings_files_receive_template_defaults() {
        let settings: Settings = serde_json::from_str(
            r#"{"image_path":"photos","video_path":"clips","auto_download":false}"#,
        )
        .unwrap();
        settings.validate().unwrap();
        assert_eq!(
            settings.image_path_template,
            "{author}/{tweet_id}_{ordinal}.{extension}"
        );
        assert_eq!(settings.thumbnail_path, "thumbnails");
        assert_eq!(
            settings.thumbnail_path_template,
            "{author}/{media_id}.{extension}"
        );
    }

    #[test]
    fn defaults_keep_the_existing_layout() {
        let settings = Settings::default();
        settings.validate().unwrap();
        let data = Path::new("data");
        let tokens = tokens(data, "media-id");
        assert_eq!(
            media_path(
                data,
                &settings.image_path,
                &settings.image_path_template,
                &tokens
            )
            .unwrap(),
            PathBuf::from("data/images/alice/123_1.jpg")
        );
        assert_eq!(
            media_path(
                data,
                &settings.thumbnail_path,
                &settings.thumbnail_path_template,
                &tokens
            )
            .unwrap(),
            PathBuf::from("data/thumbnails/alice/media-id.jpg")
        );
    }

    #[test]
    fn roots_and_templates_are_configurable() {
        let data = Path::new("/srv/archive");
        let tokens = tokens(data, "media-id");
        let mut settings = Settings {
            image_path: "{data}/originals".into(),
            image_path_template: "{kind}/{author}/{tweet_id}-{ordinal}.{extension}".into(),
            ..Settings::default()
        };
        settings.validate().unwrap();
        assert_eq!(
            media_path(
                data,
                &settings.image_path,
                &settings.image_path_template,
                &tokens
            )
            .unwrap(),
            PathBuf::from("/srv/archive/originals/image/alice/123-1.jpg")
        );

        settings.image_path_template = "{data}/outside/{media_id}.{extension}".into();
        settings.validate().unwrap();
        assert_eq!(
            media_path(
                data,
                &settings.image_path,
                &settings.image_path_template,
                &tokens
            )
            .unwrap(),
            PathBuf::from("/srv/archive/outside/media-id.jpg")
        );

        settings.thumbnail_path = "/mnt/thumbnails".into();
        settings.thumbnail_path_template = "{author}/{media_id}.{extension}".into();
        settings.validate().unwrap();
        assert_eq!(
            media_path(
                data,
                &settings.thumbnail_path,
                &settings.thumbnail_path_template,
                &tokens
            )
            .unwrap(),
            PathBuf::from("/mnt/thumbnails/alice/media-id.jpg")
        );
    }

    #[test]
    fn invalid_roots_and_templates_are_rejected() {
        let mut settings = Settings {
            image_path: "../config".into(),
            ..Settings::default()
        };
        assert!(settings.validate().is_err());

        settings.image_path = ".media".into();
        assert!(settings.validate().is_err());

        settings.image_path = "images".into();
        settings.image_path_template = "{author}/{tweet_id}_{ordinal}".into();
        assert!(settings.validate().is_err());

        settings.image_path_template = "{author}/{unknown}_{ordinal}.{extension}".into();
        assert!(settings.validate().is_err());

        settings.image_path_template = "a/{author}/../{tweet_id}_{ordinal}.{extension}".into();
        assert!(settings.validate().is_err());

        settings.image_path_template =
            "photos/{data}/{author}/{tweet_id}_{ordinal}.{extension}".into();
        assert!(settings.validate().is_err());

        settings.image_path_template = "{author}/{tweet_id}_{ordinal}.{extension}".into();
        settings.thumbnail_path_template = "{author}/{tweet_id}_{ordinal}.{extension}".into();
        assert!(settings.validate().is_err());
    }

    #[test]
    fn stored_paths_stay_relative_only_inside_the_data_directory() {
        let data = Path::new("/srv/archive");
        assert_eq!(
            stored_path(data, &data.join("images/alice/1.jpg")),
            "images/alice/1.jpg"
        );
        assert_eq!(
            stored_path(data, Path::new("/mnt/media/alice/1.jpg")),
            "/mnt/media/alice/1.jpg"
        );
    }
}
