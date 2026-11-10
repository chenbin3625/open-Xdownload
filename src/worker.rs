use crate::app::App;
use crate::{app::CookieStatus, config::safe_path, domain, store::Task, tools};
use anyhow::{Context, Result, bail, ensure};
use serde_json::Value;
use std::time::Duration;
use tokio::fs;
use tokio_cron_scheduler::{Job, JobScheduler};

pub async fn start(app: App) -> Result<JobScheduler> {
    let scheduler = JobScheduler::new().await?;
    let scheduled = app.clone();
    scheduler
        .add(Job::new_async("0/30 * * * * *", move |_, _| {
            let app = scheduled.clone();
            Box::pin(async move {
                if let Err(error) = schedule(&app).await {
                    tracing::error!(%error, "scheduler failed");
                }
            })
        })?)
        .await?;
    scheduler.start().await?;
    tokio::spawn(async move {
        loop {
            if let Err(error) = dispatch(&app).await {
                tracing::error!(%error, "queue dispatch failed");
            }
            tokio::time::sleep(Duration::from_millis(800)).await;
        }
    });
    Ok(scheduler)
}

pub async fn schedule(app: &App) -> Result<()> {
    if !app.config.join("cookies.txt").exists() {
        return Ok(());
    }
    let settings = app.settings.read().await.clone();
    let users: Vec<(String,String)> = sqlx::query_as("SELECT id,username FROM users u WHERE enabled=1 AND (julianday('now')-julianday(COALESCE((SELECT MAX(created_at) FROM tasks WHERE kind='sync' AND target=u.id),u.last_sync,u.created_at)))*1440>=COALESCE(u.sync_interval,?)")
        .bind(settings.sync_interval_minutes).fetch_all(&app.store.pool).await?;
    for (id, username) in users {
        app.store
            .enqueue("sync", &id, &format!("定时同步 @{username}"))
            .await?;
    }
    if settings.relationship_sync_enabled {
        let users: Vec<(String, String)> =
            sqlx::query_as("SELECT id,username FROM users WHERE enabled=1")
                .fetch_all(&app.store.pool)
                .await?;
        for (id, username) in users {
            for direction in ["following", "followers"] {
                let target = format!("{id}:{direction}");
                let due:bool = sqlx::query_scalar("SELECT (julianday('now')-julianday(COALESCE(MAX(created_at),'2000-01-01')))*1440>=? FROM tasks WHERE kind='relationships' AND target=?")
                    .bind(settings.relationship_interval_minutes).bind(&target).fetch_one(&app.store.pool).await?;
                if due {
                    app.store
                        .enqueue(
                            "relationships",
                            &target,
                            &format!("@{username} {direction} 定时快照"),
                        )
                        .await?;
                }
            }
        }
    }
    Ok(())
}

async fn dispatch(app: &App) -> Result<()> {
    let settings = app.settings.read().await.clone();
    if settings.auto_download && app.config.join("cookies.txt").exists() {
        let missing:Vec<String> = sqlx::query_scalar("SELECT id FROM media m WHERE status='pending' AND NOT EXISTS (SELECT 1 FROM tasks WHERE kind='download' AND target=m.id) LIMIT 50").fetch_all(&app.store.pool).await?;
        for id in missing {
            app.store.enqueue("download", &id, "自动下载媒体").await?;
        }
    }
    let running:Vec<(String,i64)> = sqlx::query_as("SELECT CASE WHEN kind='download' THEN 'download' ELSE 'extract' END,COUNT(*) FROM tasks WHERE status IN ('running','cancelling') GROUP BY 1").fetch_all(&app.store.pool).await?;
    let mut downloads = running
        .iter()
        .find(|r| r.0 == "download")
        .map(|r| r.1)
        .unwrap_or(0);
    let mut extracts = running
        .iter()
        .find(|r| r.0 == "extract")
        .map(|r| r.1)
        .unwrap_or(0);
    let queued:Vec<Task> = sqlx::query_as("SELECT * FROM tasks WHERE status='queued' AND available_at<=strftime('%Y-%m-%dT%H:%M:%fZ','now') ORDER BY created_at LIMIT 64").fetch_all(&app.store.pool).await?;
    for mut task in queued {
        if task.kind == "download" {
            if downloads >= i64::from(settings.download_concurrency) {
                continue;
            }
        } else if extracts >= 1 {
            continue;
        }
        if !app.store.claim(&task.id).await? {
            continue;
        }
        if task.kind == "download" {
            downloads += 1;
        } else {
            extracts += 1;
        }
        task.attempts += 1;
        let app = app.clone();
        tokio::spawn(async move {
            if let Err(error) = execute(&app, &task).await {
                tracing::error!(task=%task.id,%error,"task status update failed");
            }
        });
    }
    Ok(())
}

async fn progress(app: &App, task: &Task, percent: i64) -> Result<()> {
    let result=sqlx::query("UPDATE tasks SET progress=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=? AND status='running'").bind(percent).bind(&task.id).execute(&app.store.pool).await?;
    ensure!(result.rows_affected() == 1, "任务已取消");
    Ok(())
}

pub async fn execute(app: &App, task: &Task) -> Result<()> {
    let session_revision = app.cookie.read().await.revision.clone();
    let result = match task.kind.as_str() {
        "tweet" => archive_tweet(app, task).await,
        "sync" => sync(app, task).await,
        "download" => download(app, task).await,
        "relationships" => relationships(app, task).await,
        "cookie_check" => cookie_check(app, task).await,
        _ => Err(anyhow::anyhow!("未知任务类型")),
    };
    match result {
        Ok(result) => {
            sqlx::query("UPDATE tasks SET status='succeeded',progress=100,result=?,error=NULL,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=? AND status='running'")
                .bind(result).bind(&task.id).execute(&app.store.pool).await?;
        }
        Err(error) => {
            // Every tool error has already been converted to a safe, user-facing reason.
            let reason = error.to_string();
            if reason.contains("Cookie 无效") {
                app.set_cookie_status(CookieStatus {
                    status: "invalid".into(),
                    reason: reason.clone(),
                    checked_at: Some(chrono::Utc::now().to_rfc3339()),
                    revision: session_revision,
                })
                .await?;
            }
            let auth_error =
                reason.contains("Cookie") || reason.contains("无权") || reason.contains("不可访问");
            let cancelled: bool = sqlx::query_scalar(
                "SELECT status IN ('cancelled','cancelling') FROM tasks WHERE id=?",
            )
            .bind(&task.id)
            .fetch_one(&app.store.pool)
            .await?;
            let retry = !cancelled
                && !auth_error
                && task.kind != "cookie_check"
                && task.attempts < task.max_attempts;
            let delay = if reason.contains("频率") {
                900
            } else {
                30_i64 * task.attempts.max(1)
            };
            let available = (chrono::Utc::now() + chrono::Duration::seconds(delay))
                .to_rfc3339_opts(chrono::SecondsFormat::Millis, true);
            let mut tx = app.store.pool.begin_with("BEGIN IMMEDIATE").await?;
            sqlx::query("UPDATE tasks SET status=?,error=?,available_at=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=? AND status='running'")
                .bind(if retry {"queued"} else {"failed"}).bind(&reason).bind(available).bind(&task.id).execute(&mut *tx).await?;
            if task.kind == "download" {
                sqlx::query(
                    "UPDATE media SET status='failed',error=? WHERE id=? AND status!='ready'",
                )
                .bind(&reason)
                .bind(&task.target)
                .execute(&mut *tx)
                .await?;
            }
            if task.kind == "sync" {
                sqlx::query("UPDATE users SET last_error=? WHERE id=?")
                    .bind(&reason)
                    .bind(&task.target)
                    .execute(&mut *tx)
                    .await?;
            }
            tx.commit().await?;
        }
    }
    sqlx::query("UPDATE tasks SET status='cancelled',error='任务已取消',updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=? AND status='cancelling'").bind(&task.id).execute(&app.store.pool).await?;
    Ok(())
}

async fn archive_tweet(app: &App, task: &Task) -> Result<String> {
    progress(app, task, 5).await?;
    let output = tools::extract(
        app,
        &task.id,
        &format!("https://x.com/i/web/status/{}", task.target),
        None,
        false,
    )
    .await?;
    let tweets = tools::parse_tweets(&output.stdout)?;
    progress(app, task, 80).await?;
    let inserted = app.store.ingest(&tweets).await?;
    if let Some(reason) = output.failure() {
        bail!(
            "已保存 {} 条可读取的推文，抓取未完整结束：{reason}",
            tweets.len()
        );
    }
    ensure!(
        tweets.iter().any(|t| t.id == task.target),
        "未获取到该推文，可能已删除或当前会话无权访问"
    );
    Ok(format!(
        "新增 {inserted} 条推文，已识别 {} 个媒体；已有内容自动去重",
        tweets.iter().map(|t| t.media.len()).sum::<usize>()
    ))
}

async fn sync(app: &App, task: &Task) -> Result<String> {
    let username: Option<String> =
        sqlx::query_scalar("SELECT username FROM users WHERE id=? AND enabled=1")
            .bind(&task.target)
            .fetch_optional(&app.store.pool)
            .await?;
    let username = username.context("用户不存在或已暂停")?;
    progress(app, task, 5).await?;
    let info = tools::extract(
        app,
        &task.id,
        &format!("https://x.com/{username}/info"),
        None,
        false,
    )
    .await?;
    tools::ensure_success(&info)?;
    let profile = tools::profile(&info.stdout)?;
    sqlx::query("UPDATE users SET name=?,bio=?,avatar=? WHERE id=?")
        .bind(profile["nick"].as_str().unwrap_or(&username))
        .bind(profile["description"].as_str().unwrap_or(""))
        .bind(profile["profile_image"].as_str())
        .bind(&task.target)
        .execute(&app.store.pool)
        .await?;
    let newest: Option<String> =
        sqlx::query_scalar("SELECT MAX(published_at) FROM tweets WHERE user_id=?")
            .bind(&task.target)
            .fetch_one(&app.store.pool)
            .await?;
    // A two-day overlap catches pinned posts, pagination changes, and delayed media.
    let since = newest
        .as_deref()
        .and_then(|s| chrono::DateTime::parse_from_rfc3339(s).ok())
        .map(|d| (d - chrono::Duration::days(2)).to_rfc3339());
    progress(app, task, 15).await?;
    let output = tools::extract(
        app,
        &task.id,
        &format!("https://x.com/{username}/with_replies"),
        since.as_deref(),
        false,
    )
    .await?;
    let tweets = tools::parse_tweets(&output.stdout)?;
    progress(app, task, 85).await?;
    let own: Vec<_> = tweets
        .into_iter()
        .filter(|t| t.author.eq_ignore_ascii_case(&username))
        .collect();
    let inserted = app.store.ingest(&own).await?;
    if let Some(reason) = output.failure() {
        bail!(
            "已保存 {} 条可读取的推文，抓取未完整结束：{reason}",
            own.len()
        );
    }
    sqlx::query("UPDATE users SET last_sync=strftime('%Y-%m-%dT%H:%M:%fZ','now'),last_error=NULL WHERE id=?").bind(&task.target).execute(&app.store.pool).await?;
    Ok(format!(
        "读取 {} 条当前会话可访问的推文，新增 {inserted} 条，重复内容已跳过",
        own.len()
    ))
}

async fn cookie_check(app: &App, task: &Task) -> Result<String> {
    if app.cookie.read().await.revision != task.target {
        bail!("Cookie 已更新，本次检测已过期");
    }
    progress(app, task, 10).await?;
    let result = tools::extract(app, &task.id, "https://x.com/i/bookmarks", None, true).await;
    let (status, reason) = match result {
        Ok(output) => match output.failure() {
            None => ("valid", "已通过需要登录的时间线读取检测".to_string()),
            Some(reason) if reason.contains("Cookie") => ("invalid", reason.to_string()),
            Some(reason) => ("limited", reason.to_string()),
        },
        Err(error) => ("unknown", error.to_string()),
    };
    app.set_cookie_status(CookieStatus {
        status: status.into(),
        reason: reason.clone(),
        checked_at: Some(chrono::Utc::now().to_rfc3339()),
        revision: task.target.clone(),
    })
    .await?;
    if status != "valid" {
        bail!(reason);
    }
    Ok(reason)
}

struct RelationshipFetch {
    members: Vec<(String, String, String)>,
    expected: Option<u64>,
    complete: bool,
    error: Option<String>,
}

async fn relationships(app: &App, task: &Task) -> Result<String> {
    let (user_id, direction) = task.target.split_once(':').context("快照任务参数无效")?;
    ensure!(
        ["following", "followers"].contains(&direction),
        "关系方向无效"
    );
    let username: Option<String> =
        sqlx::query_scalar("SELECT username FROM users WHERE id=? AND enabled=1")
            .bind(user_id)
            .fetch_optional(&app.store.pool)
            .await?;
    let username = username.context("用户不存在或已暂停")?;
    progress(app, task, 5).await?;
    let result: Result<RelationshipFetch> = async {
        let before = tools::extract(
            app,
            &task.id,
            &format!("https://x.com/{username}/info"),
            None,
            false,
        )
        .await?;
        tools::ensure_success(&before)?;
        let before = tools::profile(&before.stdout)?;
        let field = if direction == "following" {
            "friends_count"
        } else {
            "followers_count"
        };
        let expected = before[field].as_u64();
        progress(app, task, 20).await?;
        let output = tools::extract(
            app,
            &task.id,
            &format!("https://x.com/{username}/{direction}"),
            None,
            false,
        )
        .await?;
        let members = tools::relationship_members(&output.stdout)?;
        let error = output.failure().map(str::to_string);
        let after = tools::extract(
            app,
            &task.id,
            &format!("https://x.com/{username}/info"),
            None,
            false,
        )
        .await;
        let after_profile = after
            .as_ref()
            .ok()
            .filter(|o| o.failure().is_none())
            .and_then(|o| tools::profile(&o.stdout).ok());
        let after_count = after_profile.as_ref().and_then(|p| p[field].as_u64());
        let identity_matches = after_profile
            .as_ref()
            .is_some_and(|p| p["id"] == before["id"] && !p["id"].is_null());
        let complete = domain::complete_snapshot(
            error.is_none(),
            expected,
            members.len() as u64,
            expected != after_count || !identity_matches,
        );
        Ok(RelationshipFetch {
            members,
            expected,
            complete,
            error,
        })
    }
    .await;
    progress(app, task, 90).await?;
    let (members, expected, status, error) = match result {
        Ok(RelationshipFetch {
            members,
            expected,
            complete: true,
            ..
        }) => (members, expected, "complete", None),
        Ok(RelationshipFetch {
            members,
            expected,
            complete: false,
            error,
        }) => {
            let status = if members.is_empty() && error.is_some() {
                "failed"
            } else {
                "partial"
            };
            (
                members,
                expected,
                status,
                Some(error.unwrap_or_else(|| {
                    "抓取数量与资料计数不一致、计数未知或抓取期间发生变化，不能据此判断取消关系"
                        .into()
                })),
            )
        }
        Err(error) => (Vec::new(), None, "failed", Some(error.to_string())),
    };
    let id = uuid::Uuid::new_v4().to_string();
    let mut tx = app.store.pool.begin().await?;
    sqlx::query("INSERT INTO snapshots(id,user_id,direction,status,expected_count,fetched_count,error) VALUES(?,?,?,?,?,?,?)")
        .bind(&id).bind(user_id).bind(direction).bind(status).bind(expected.map(|v|v as i64)).bind(members.len() as i64).bind(&error).execute(&mut *tx).await?;
    // The snapshot and its members become visible together.
    for (member_id, username, name) in &members {
        sqlx::query("INSERT INTO relationships(snapshot_id,user_id,username,name) VALUES(?,?,?,?) ON CONFLICT DO NOTHING").bind(&id).bind(member_id).bind(username).bind(name).execute(&mut *tx).await?;
    }
    tx.commit().await?;
    if status == "failed" {
        bail!(error.unwrap_or_else(|| "关系抓取失败".into()));
    }
    Ok(format!(
        "{}快照：{} 条关系{}",
        if status == "complete" {
            "完整"
        } else {
            "部分"
        },
        members.len(),
        if status == "partial" {
            "；不参与新增或取消关系判断"
        } else {
            ""
        }
    ))
}

async fn download(app: &App, task: &Task) -> Result<String> {
    let row:Option<(String,String,String,String,String,i64,String)>=sqlx::query_as("SELECT m.kind,m.remote_url,m.extension,m.tweet_id,t.author,m.ordinal,m.status FROM media m JOIN tweets t ON t.id=m.tweet_id WHERE m.id=?").bind(&task.target).fetch_optional(&app.store.pool).await?;
    let (kind, url, extension, tweet_id, author, ordinal, status) =
        row.context("媒体记录不存在")?;
    if status == "ready" {
        return Ok("媒体已下载，跳过重复任务".into());
    }
    tools::media_url(&url)?;
    let free = fs2::available_space(&app.data).unwrap_or(0);
    ensure!(
        free > 128 * 1024 * 1024,
        "数据目录可用空间不足 128 MB，请清理磁盘后重试"
    );
    if let Ok(response) = app.client.head(&url).send().await
        && let Some(size) = response.content_length()
    {
        ensure!(
            free > size.saturating_add(128 * 1024 * 1024),
            "媒体大小超过当前可用空间，请清理磁盘后重试"
        );
    }
    progress(app, task, 5).await?;
    sqlx::query("UPDATE media SET status='downloading',error=NULL WHERE id=?")
        .bind(&task.target)
        .execute(&app.store.pool)
        .await?;
    let relative = format!(".staging/{}", task.id);
    let staging = safe_path(&app.data, &relative).await?;
    if staging.exists() {
        fs::remove_dir_all(&staging).await?;
    }
    fs::create_dir_all(&staging).await?;
    let is_image = kind == "image" || extension == "gif";
    let cookie = tools::session_cookie(app).await?;
    let output = if is_image {
        let mut args = tools::gallery_args(&cookie.0);
        args.extend([
            "--directory".into(),
            staging.to_string_lossy().into(),
            "--filename".into(),
            "asset.{extension}".into(),
            "--".into(),
            url,
        ]);
        tools::run(
            app,
            Some(&task.id),
            &app.gallery_dl,
            &args,
            Duration::from_secs(900),
        )
        .await?
    } else {
        let args = vec![
            "--ignore-config".into(),
            "--no-playlist".into(),
            "--no-cache-dir".into(),
            "--no-progress".into(),
            "--cookies".into(),
            cookie.0.to_string_lossy().into(),
            "--retries".into(),
            "2".into(),
            "--socket-timeout".into(),
            "30".into(),
            "-o".into(),
            staging.join("asset.%(ext)s").to_string_lossy().into(),
            "--".into(),
            url,
        ];
        tools::run(
            app,
            Some(&task.id),
            &app.yt_dlp,
            &args,
            Duration::from_secs(1800),
        )
        .await?
    };
    tools::ensure_success(&output)?;
    progress(app, task, 70).await?;
    let mut entries = fs::read_dir(&staging).await?;
    let mut source = None;
    while let Some(entry) = entries.next_entry().await? {
        let name = entry.file_name().to_string_lossy().into_owned();
        if entry.file_type().await?.is_file()
            && name.starts_with("asset.")
            && !name.ends_with(".part")
            && !name.ends_with(".ytdl")
        {
            source = Some(entry.path());
            break;
        }
    }
    let source = source.context("下载工具没有生成媒体文件，请检查工具版本后重试")?;
    ensure!(fs::metadata(&source).await?.len() > 0, "下载文件为空");
    let settings = app.settings.read().await.clone();
    let target_dir = if is_image {
        &settings.image_path
    } else {
        &settings.video_path
    };
    let ext = if is_image {
        source
            .extension()
            .and_then(|v| v.to_str())
            .unwrap_or(&extension)
            .to_string()
    } else {
        "mp4".to_string()
    };
    ensure!(
        ["jpg", "jpeg", "png", "webp", "gif", "mp4"].contains(&ext.as_str()),
        "下载后的媒体格式暂不支持"
    );
    let final_relative = format!("{target_dir}/{author}/{tweet_id}_{ordinal}.{ext}");
    let final_path = safe_path(&app.data, &final_relative).await?;
    fs::create_dir_all(final_path.parent().unwrap()).await?;
    let thumbnail_relative = format!("thumbnails/{}.jpg", task.target);
    let thumbnail = safe_path(&app.data, &thumbnail_relative).await?;
    fs::create_dir_all(thumbnail.parent().unwrap()).await?;
    let thumb_temp = staging.join("thumbnail.jpg");
    let (width, height, duration, processed) = if is_image {
        let source_copy = source.clone();
        let thumb_copy = thumb_temp.clone();
        let (width, height) = tokio::task::spawn_blocking(move || -> Result<_> {
            let mut reader = image::ImageReader::open(source_copy)?.with_guessed_format()?;
            let mut limits = image::Limits::default();
            limits.max_alloc = Some(256 * 1024 * 1024);
            limits.max_image_width = Some(32768);
            limits.max_image_height = Some(32768);
            reader.limits(limits);
            let image = reader.decode().context("图片解码失败，文件可能损坏")?;
            let dimensions = (image.width(), image.height());
            image.thumbnail(640, 640).to_rgb8().save(thumb_copy)?;
            Ok(dimensions)
        })
        .await??;
        (width, height, None, source)
    } else {
        let probe = tools::run(
            app,
            Some(&task.id),
            "ffprobe",
            &[
                "-v".into(),
                "error".into(),
                "-show_streams".into(),
                "-show_format".into(),
                "-of".into(),
                "json".into(),
                source.to_string_lossy().into(),
            ],
            Duration::from_secs(30),
        )
        .await?;
        tools::ensure_success(&probe)?;
        let probe: Value = serde_json::from_slice(&probe.stdout).context("视频信息解析失败")?;
        let video = probe["streams"]
            .as_array()
            .and_then(|s| s.iter().find(|v| v["codec_type"] == "video"))
            .context("未找到视频轨道")?;
        let width = video["width"].as_u64().unwrap_or(0) as u32;
        let height = video["height"].as_u64().unwrap_or(0) as u32;
        let duration = probe["format"]["duration"]
            .as_str()
            .and_then(|s| s.parse::<f64>().ok());
        let processed = staging.join("processed.mp4");
        let codec = if video["codec_name"] == "h264" {
            "copy"
        } else {
            "libx264"
        };
        let ffmpeg = tools::run(
            app,
            Some(&task.id),
            "ffmpeg",
            &[
                "-nostdin".into(),
                "-v".into(),
                "error".into(),
                "-y".into(),
                "-i".into(),
                source.to_string_lossy().into(),
                "-map".into(),
                "0:v:0".into(),
                "-map".into(),
                "0:a?".into(),
                "-c:v".into(),
                codec.into(),
                "-c:a".into(),
                "aac".into(),
                "-movflags".into(),
                "+faststart".into(),
                processed.to_string_lossy().into(),
            ],
            Duration::from_secs(1200),
        )
        .await?;
        tools::ensure_success(&ffmpeg)?;
        let cover = tools::run(
            app,
            Some(&task.id),
            "ffmpeg",
            &[
                "-nostdin".into(),
                "-v".into(),
                "error".into(),
                "-y".into(),
                "-i".into(),
                processed.to_string_lossy().into(),
                "-frames:v".into(),
                "1".into(),
                "-vf".into(),
                "scale=640:640:force_original_aspect_ratio=decrease".into(),
                thumb_temp.to_string_lossy().into(),
            ],
            Duration::from_secs(60),
        )
        .await?;
        tools::ensure_success(&cover)?;
        (width, height, duration, processed)
    };
    progress(app, task, 95).await?;
    let size = fs::metadata(&processed).await?.len() as i64;
    // Same-filesystem renames leave no partially written public media files.
    fs::rename(processed, &final_path).await?;
    fs::rename(thumb_temp, thumbnail).await?;
    sqlx::query("UPDATE media SET status='ready',error=NULL,file_path=?,thumbnail_path=?,extension=?,size=?,width=?,height=?,duration=? WHERE id=?")
        .bind(final_relative).bind(thumbnail_relative).bind(ext).bind(size).bind(width).bind(height).bind(duration).bind(&task.target).execute(&app.store.pool).await?;
    let _ = fs::remove_dir_all(staging).await;
    Ok(format!("已下载并生成预览，{} 字节", size))
}
