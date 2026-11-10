use crate::{
    app::App,
    config::{private_dir, private_write},
    domain::safe_tool_error,
    store::{ExtractedMedia, ExtractedTweet},
};
use anyhow::{Context, Result, bail, ensure};
use serde_json::Value;
use std::{collections::BTreeMap, process::Stdio, time::Duration};
use tokio::{
    io::{AsyncRead, AsyncReadExt},
    process::Command,
};

pub struct CookieLease(pub std::path::PathBuf);
impl Drop for CookieLease {
    fn drop(&mut self) {
        let _ = std::fs::remove_file(&self.0);
    }
}
pub async fn session_cookie(app: &App) -> Result<CookieLease> {
    let _guard = app.config_gate.lock().await;
    let bytes = tokio::fs::read(app.config.join("cookies.txt"))
        .await
        .context("尚未导入 Cookie，请先在设置中导入自己的 X 登录 Cookie")?;
    let directory = app.config.join(".sessions");
    private_dir(&directory).await?;
    let path = directory.join(format!("{}.txt", uuid::Uuid::new_v4()));
    private_write(&path, &bytes).await?;
    Ok(CookieLease(path))
}

pub struct ToolOutput {
    pub success: bool,
    pub stdout: Vec<u8>,
    pub stderr: String,
}

impl ToolOutput {
    pub fn failure(&self) -> Option<&'static str> {
        if let Ok(messages) = serde_json::from_slice::<Vec<Value>>(&self.stdout) {
            for message in messages {
                if message[0].as_i64() == Some(-1) {
                    return Some(safe_tool_error(&message[1].to_string()));
                }
            }
        }
        if !self.success {
            return Some(safe_tool_error(&self.stderr));
        }
        let stderr = self.stderr.to_ascii_lowercase();
        if stderr.contains("[error]") || stderr.contains("[warning]") {
            return Some(safe_tool_error(&self.stderr));
        }
        None
    }
}

async fn bounded_read(mut reader: impl AsyncRead + Unpin, cap: u64) -> Result<Vec<u8>> {
    let mut output = Vec::new();
    (&mut reader).take(cap + 1).read_to_end(&mut output).await?;
    ensure!(
        output.len() as u64 <= cap,
        "工具输出超出安全上限，请缩小归档范围后重试"
    );
    Ok(output)
}

pub async fn run(
    app: &App,
    task: Option<&str>,
    binary: &str,
    args: &[String],
    timeout: Duration,
) -> Result<ToolOutput> {
    let mut child = Command::new(binary)
        .args(args)
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .kill_on_drop(true)
        .spawn()
        .map_err(|_| anyhow::anyhow!("无法启动下载或媒体工具，请检查工具是否已安装"))?;
    let stdout = child.stdout.take().context("工具输出不可用")?;
    let stderr = child.stderr.take().context("工具错误输出不可用")?;
    let cancelled = async {
        loop {
            tokio::time::sleep(Duration::from_millis(500)).await;
            if let Some(id) = task {
                let status: Option<String> =
                    sqlx::query_scalar("SELECT status FROM tasks WHERE id=?")
                        .bind(id)
                        .fetch_optional(&app.store.pool)
                        .await?;
                ensure!(status.as_deref() == Some("running"), "任务已取消");
            }
        }
        #[allow(unreachable_code)]
        Ok::<(), anyhow::Error>(())
    };
    let result = {
        let work = async {
            let (stdout, stderr, status) = tokio::try_join!(
                bounded_read(stdout, 96 * 1024 * 1024),
                bounded_read(stderr, 256 * 1024),
                async { Ok::<_, anyhow::Error>(child.wait().await?) }
            )?;
            Ok(ToolOutput {
                success: status.success(),
                stdout,
                stderr: String::from_utf8_lossy(&stderr).into_owned(),
            })
        };
        tokio::select! {
            result = work => result,
            _ = tokio::time::sleep(timeout) => Err(anyhow::anyhow!("操作超时，已停止工具；已保存的归档可以继续保留，请稍后重试")),
            result = cancelled => match result { Err(error) => Err(error), Ok(_) => Err(anyhow::anyhow!("任务已取消")) },
        }
    };
    if result.is_err() {
        let _ = child.kill().await;
    }
    result
}

pub fn gallery_args(cookie: &std::path::Path) -> Vec<String> {
    [
        "--config-ignore",
        "--no-input",
        "--no-postprocessors",
        "--no-colors",
        "--cache-file",
        ":memory:",
        "--cookies",
        cookie.to_str().unwrap_or(""),
        "--retries",
        "2",
        "--http-timeout",
        "30",
        "-o",
        "extractor.twitter.text-tweets=true",
        "-o",
        "extractor.twitter.retweets=false",
        "-o",
        "extractor.twitter.pinned=false",
        "-o",
        "extractor.twitter.replies=true",
        "-o",
        "extractor.twitter.quoted=false",
        "-o",
        "extractor.twitter.rate-limit=abort",
        "-o",
        "extractor.twitter.cookies-update=false",
    ]
    .iter()
    .map(|s| (*s).to_string())
    .collect()
}

pub async fn extract(
    app: &App,
    task: &str,
    url: &str,
    since: Option<&str>,
    limit: bool,
) -> Result<ToolOutput> {
    ensure!(
        app.config.join("cookies.txt").exists(),
        "尚未导入 Cookie，请先在设置中导入自己的 X 登录 Cookie"
    );
    let cookie = session_cookie(app).await?;
    let mut args = gallery_args(&cookie.0);
    args.push("--dump-json".into());
    if let Some(date) = since {
        args.extend(["--date-after".into(), date.into()]);
    }
    if limit {
        args.extend(["--post-range".into(), "1".into()]);
    }
    args.extend(["--".into(), url.into()]);
    let output = run(
        app,
        Some(task),
        &app.gallery_dl,
        &args,
        Duration::from_secs(if limit { 120 } else { 1800 }),
    )
    .await?;
    if !output.success {
        ensure_success(&output)?;
    }
    let _: Vec<Value> = serde_json::from_slice(&output.stdout)
        .context("下载工具返回了无效数据，请检查工具版本和网络后重试")?;
    Ok(output)
}

fn string(value: &Value) -> String {
    value
        .as_str()
        .map(str::to_string)
        .or_else(|| value.as_u64().map(|n| n.to_string()))
        .unwrap_or_default()
}

pub fn media_url(input: &str) -> Result<()> {
    let url = url::Url::parse(input)?;
    ensure!(
        url.scheme() == "https"
            && url.username().is_empty()
            && url.password().is_none()
            && url.port().is_none()
            && matches!(url.host_str(), Some("pbs.twimg.com" | "video.twimg.com")),
        "提取工具返回了不支持的媒体地址"
    );
    Ok(())
}

pub fn parse_tweets(bytes: &[u8]) -> Result<Vec<ExtractedTweet>> {
    let messages: Vec<Value> =
        serde_json::from_slice(bytes).context("下载工具返回了无法识别的数据，请检查版本")?;
    let mut tweets: BTreeMap<u64, ExtractedTweet> = BTreeMap::new();
    for message in &messages {
        let typ = message[0].as_i64().unwrap_or(0);
        if typ != 2 && typ != 3 {
            continue;
        }
        let metadata = message
            .as_array()
            .and_then(|v| v.last())
            .context("推文数据格式错误")?;
        let id = string(&metadata["tweet_id"]);
        if id.is_empty() {
            continue;
        }
        let numeric: u64 = id.parse().context("推文 ID 格式错误")?;
        let author = crate::domain::username(&string(&metadata["user"]["name"]))?;
        let author_name = string(&metadata["user"]["nick"]);
        let date = string(&metadata["date"]);
        let published = if let Ok(date) = chrono::DateTime::parse_from_rfc3339(&date) {
            date.to_utc().to_rfc3339()
        } else if let Ok(date) = chrono::NaiveDateTime::parse_from_str(&date, "%Y-%m-%d %H:%M:%S") {
            date.and_utc().to_rfc3339()
        } else {
            chrono::DateTime::from_timestamp_millis(((numeric >> 22) + 1_288_834_974_657) as i64)
                .context("发布时间无效")?
                .to_rfc3339()
        };
        let tweet = tweets.entry(numeric).or_insert_with(|| ExtractedTweet {
            id,
            author,
            author_name,
            text: string(&metadata["content"]),
            published_at: published,
            media: Vec::new(),
        });
        if typ == 3 {
            let url = string(&message[1]);
            media_url(&url)?;
            let extension = string(&metadata["extension"]).to_ascii_lowercase();
            ensure!(
                [
                    "jpg", "jpeg", "png", "webp", "gif", "mp4", "webm", "mov", "m4v"
                ]
                .contains(&extension.as_str()),
                "媒体格式暂不支持"
            );
            let typ = string(&metadata["type"]);
            let kind = if typ == "animated_gif" || typ == "gif" || extension == "gif" {
                "gif"
            } else if ["mp4", "webm", "mov", "m4v"].contains(&extension.as_str()) {
                "video"
            } else {
                "image"
            };
            let ordinal = metadata["num"]
                .as_i64()
                .unwrap_or(tweet.media.len() as i64 + 1);
            if !tweet.media.iter().any(|m| m.ordinal == ordinal) {
                tweet.media.push(ExtractedMedia {
                    ordinal,
                    kind: kind.into(),
                    extension,
                    url,
                });
            }
        }
    }
    Ok(tweets.into_values().collect())
}

pub fn profile(bytes: &[u8]) -> Result<Value> {
    let messages: Vec<Value> = serde_json::from_slice(bytes).context("用户资料格式错误")?;
    messages
        .iter()
        .find(|m| m[0] == 2)
        .and_then(|m| m.as_array()?.last())
        .cloned()
        .filter(|p| p["name"].is_string())
        .context("无法读取该用户资料，请检查账号权限")
}

pub fn relationship_members(bytes: &[u8]) -> Result<Vec<(String, String, String)>> {
    let messages: Vec<Value> = serde_json::from_slice(bytes).context("关系列表格式错误")?;
    let mut members = BTreeMap::new();
    for message in messages {
        if message[0] != 6 {
            continue;
        }
        let raw = &message[2];
        let id = string(&raw["rest_id"]);
        let id = if id.is_empty() {
            string(&raw["id_str"])
        } else {
            id
        };
        ensure!(
            !id.is_empty() && id.bytes().all(|c| c.is_ascii_digit()),
            "关系列表中存在无法识别的用户"
        );
        let core = if raw["core"].is_object() {
            &raw["core"]
        } else if raw["legacy"].is_object() {
            &raw["legacy"]
        } else {
            raw
        };
        let username = crate::domain::username(&string(&core["screen_name"]))?;
        members.insert(id.clone(), (id, username, string(&core["name"])));
    }
    Ok(members.into_values().collect())
}

pub async fn versions(app: &App) -> Value {
    let mut versions = serde_json::Map::new();
    for (name, binary, flag) in [
        ("gallery-dl", app.gallery_dl.as_str(), "--version"),
        ("yt-dlp", app.yt_dlp.as_str(), "--version"),
        ("ffmpeg", "ffmpeg", "-version"),
        ("ffprobe", "ffprobe", "-version"),
    ] {
        let result = run(app, None, binary, &[flag.into()], Duration::from_secs(5)).await;
        let version = result.ok().filter(|r| r.success).map(|r| {
            String::from_utf8_lossy(&r.stdout)
                .lines()
                .next()
                .unwrap_or("")
                .chars()
                .take(100)
                .collect::<String>()
        });
        versions.insert(
            name.into(),
            serde_json::json!({"available": version.is_some(), "version": version}),
        );
    }
    Value::Object(versions)
}

pub fn ensure_success(output: &ToolOutput) -> Result<()> {
    if let Some(message) = output.failure() {
        bail!(message);
    }
    Ok(())
}
