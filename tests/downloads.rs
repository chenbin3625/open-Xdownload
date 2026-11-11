use std::{path::PathBuf, time::Duration};

use sqlx::Row;
use x_archive::{
    app::App,
    config::private_write,
    store::{ExtractedMedia, ExtractedTweet},
    worker,
};

#[tokio::test]
async fn downloads_separate_users_with_default_and_custom_media_roots() {
    let directory = tempfile::tempdir().unwrap();
    let mut app = App::new(
        directory.path().join("data"),
        directory.path().join("config"),
        None,
    )
    .await
    .unwrap();
    let root = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
    app.gallery_dl = root
        .join("tests/fixtures/gallery.py")
        .to_string_lossy()
        .into_owned();
    app.yt_dlp = root
        .join("tests/fixtures/ytdlp.py")
        .to_string_lossy()
        .into_owned();
    app.client = reqwest::Client::builder()
        .timeout(Duration::from_millis(20))
        .build()
        .unwrap();
    private_write(&app.config.join("cookies.txt"), b"OFFLINE_FIXTURE_COOKIE")
        .await
        .unwrap();
    let assets = app.config.join("fixtures");
    tokio::fs::create_dir(&assets).await.unwrap();
    image::RgbImage::from_pixel(64, 48, image::Rgb([40, 150, 90]))
        .save(assets.join("photo.png"))
        .unwrap();
    let status = tokio::process::Command::new("ffmpeg")
        .args([
            "-nostdin",
            "-v",
            "error",
            "-y",
            "-f",
            "lavfi",
            "-i",
            "testsrc2=size=64x48:rate=10",
            "-t",
            "0.1",
            "-c:v",
            "libx264",
            "-pix_fmt",
            "yuv420p",
        ])
        .arg(assets.join("video.mp4"))
        .status()
        .await
        .unwrap();
    assert!(status.success(), "fixture video generation failed");

    let external_images = directory
        .path()
        .join("external/images")
        .to_string_lossy()
        .into_owned();
    let external_videos = directory
        .path()
        .join("external/videos")
        .to_string_lossy()
        .into_owned();
    for (round, image_root, video_root) in [
        (0, "images".to_string(), "videos".to_string()),
        (
            1,
            "photos/originals".to_string(),
            "clips/originals".to_string(),
        ),
        (
            2,
            "templated/images".to_string(),
            "templated/videos".to_string(),
        ),
        (3, external_images, external_videos),
    ] {
        if round > 0 {
            let mut settings = app.settings.write().await;
            settings.image_path = image_root.clone();
            settings.video_path = video_root.clone();
            if round == 2 {
                settings.thumbnail_path = "templated/thumbnails".into();
                settings.image_path_template =
                    "{kind}/{author}/{tweet_id}_{ordinal}.{extension}".into();
                settings.video_path_template =
                    "{kind}/{author}/{tweet_id}_{ordinal}.{extension}".into();
                settings.thumbnail_path_template = "{kind}/{author}/{media_id}.{extension}".into();
            } else if round == 3 {
                let defaults = x_archive::config::Settings::default();
                settings.thumbnail_path = defaults.thumbnail_path;
                settings.image_path_template = defaults.image_path_template;
                settings.video_path_template = defaults.video_path_template;
                settings.thumbnail_path_template = defaults.thumbnail_path_template;
            }
            settings.validate().unwrap();
        }
        for (index, author) in ["Archive_One", "archive_two"].into_iter().enumerate() {
            let username = author.to_ascii_lowercase();
            let tweet_id = format!("12345678901234567{round}{index}");
            app.store
                .ingest(&[ExtractedTweet {
                    id: tweet_id.clone(),
                    author: author.into(),
                    author_name: author.into(),
                    text: "offline download fixture".into(),
                    published_at: "2026-11-10T12:00:00Z".into(),
                    media: vec![
                        ExtractedMedia {
                            ordinal: 1,
                            kind: "image".into(),
                            extension: "png".into(),
                            url: "https://pbs.twimg.com/media/fixture.png".into(),
                        },
                        ExtractedMedia {
                            ordinal: 2,
                            kind: "video".into(),
                            extension: "mp4".into(),
                            url: "https://video.twimg.com/ext_tw_video/fixture.mp4".into(),
                        },
                        ExtractedMedia {
                            ordinal: 3,
                            kind: "gif".into(),
                            extension: "mp4".into(),
                            url: "https://video.twimg.com/tweet_video/fixture.mp4".into(),
                        },
                    ],
                }])
                .await
                .unwrap();
            let media: Vec<(String, i64)> =
                sqlx::query_as("SELECT id,ordinal FROM media WHERE tweet_id=? ORDER BY ordinal")
                    .bind(&tweet_id)
                    .fetch_all(&app.store.pool)
                    .await
                    .unwrap();
            assert_eq!(media.len(), 3);
            for (id, ordinal) in media {
                let task = app
                    .store
                    .queue_download(&id, "fixture download")
                    .await
                    .unwrap();
                assert!(app.store.claim(&task.id).await.unwrap());
                worker::execute(&app, &task).await.unwrap();
                let task_status: (String, Option<String>) =
                    sqlx::query_as("SELECT status,error FROM tasks WHERE id=?")
                        .bind(&task.id)
                        .fetch_one(&app.store.pool)
                        .await
                        .unwrap();
                assert_eq!(task_status, ("succeeded".into(), None));

                let row = sqlx::query(
                    "SELECT status,file_path,thumbnail_path,size,width,height FROM media WHERE id=?",
                )
                .bind(&id)
                .fetch_one(&app.store.pool)
                .await
                .unwrap();
                let (media_root, extension, kind) = if ordinal == 1 {
                    (image_root.as_str(), "png", "image")
                } else if ordinal == 2 {
                    (video_root.as_str(), "mp4", "video")
                } else {
                    (video_root.as_str(), "mp4", "gif")
                };
                let expected_file = if round == 2 {
                    format!("{media_root}/{kind}/{username}/{tweet_id}_{ordinal}.{extension}")
                } else {
                    format!("{media_root}/{username}/{tweet_id}_{ordinal}.{extension}")
                };
                let expected_thumbnail = if round == 2 {
                    format!("templated/thumbnails/{kind}/{username}/{id}.jpg")
                } else {
                    format!("thumbnails/{username}/{id}.jpg")
                };
                assert_eq!(row.get::<String, _>("status"), "ready");
                assert_eq!(row.get::<String, _>("file_path"), expected_file);
                assert_eq!(row.get::<String, _>("thumbnail_path"), expected_thumbnail);
                assert_eq!(row.get::<i64, _>("width"), 64);
                assert_eq!(row.get::<i64, _>("height"), 48);
                let file = app.data.join(&expected_file);
                let metadata = tokio::fs::metadata(&file).await.unwrap();
                assert!(metadata.len() > 0);
                assert_eq!(row.get::<i64, _>("size") as u64, metadata.len());
                let (result, result_size): (String, i64) =
                    sqlx::query_as("SELECT result,result_size FROM tasks WHERE id=?")
                        .bind(&task.id)
                        .fetch_one(&app.store.pool)
                        .await
                        .unwrap();
                assert_eq!(result, "已下载并生成预览");
                assert_eq!(result_size as u64, metadata.len());
                let thumbnail = app.data.join(&expected_thumbnail);
                let (width, height) = image::image_dimensions(thumbnail).unwrap();
                assert!(width > 0 && height > 0);
                assert!(width <= 640 && height <= 640);
                assert!(
                    !app.data
                        .join(media_root)
                        .join(format!("{tweet_id}_{ordinal}.{extension}"))
                        .exists()
                );
                assert!(!app.data.join(format!("thumbnails/{id}.jpg")).exists());
                if ordinal == 1 {
                    assert_eq!(image::image_dimensions(file).unwrap(), (64, 48));
                }
            }
        }
    }
    let ready: i64 = sqlx::query_scalar("SELECT COUNT(*) FROM media WHERE status='ready'")
        .fetch_one(&app.store.pool)
        .await
        .unwrap();
    assert_eq!(ready, 24);
}

#[tokio::test]
async fn failed_and_cancelled_downloads_remove_partial_files() {
    let directory = tempfile::tempdir().unwrap();
    let mut app = App::new(
        directory.path().join("data"),
        directory.path().join("config"),
        None,
    )
    .await
    .unwrap();
    app.gallery_dl = format!("{}/tests/fixtures/gallery.py", env!("CARGO_MANIFEST_DIR"));
    app.client = reqwest::Client::builder()
        .timeout(Duration::from_millis(20))
        .build()
        .unwrap();
    private_write(&app.config.join("cookies.txt"), b"OFFLINE_FIXTURE_COOKIE")
        .await
        .unwrap();
    for (index, mode) in ["fail", "invalid-image", "cancel"].into_iter().enumerate() {
        app.store
            .ingest(&[ExtractedTweet {
                id: index.to_string(),
                author: "archive_test".into(),
                author_name: "Archive Test".into(),
                text: "cleanup fixture".into(),
                published_at: "2026-11-10T12:00:00Z".into(),
                media: vec![ExtractedMedia {
                    ordinal: 1,
                    kind: "image".into(),
                    extension: "png".into(),
                    url: "https://pbs.twimg.com/media/fixture.png".into(),
                }],
            }])
            .await
            .unwrap();
        let media: String = sqlx::query_scalar("SELECT id FROM media WHERE tweet_id=?")
            .bind(index.to_string())
            .fetch_one(&app.store.pool)
            .await
            .unwrap();
        let task = app
            .store
            .queue_download(&media, "cleanup fixture")
            .await
            .unwrap();
        sqlx::query("UPDATE tasks SET max_attempts=1 WHERE id=?")
            .bind(&task.id)
            .execute(&app.store.pool)
            .await
            .unwrap();
        assert!(app.store.claim(&task.id).await.unwrap());
        let task = sqlx::query_as("SELECT * FROM tasks WHERE id=?")
            .bind(&task.id)
            .fetch_one(&app.store.pool)
            .await
            .unwrap();
        tokio::fs::write(app.config.join("download-mode"), mode)
            .await
            .unwrap();
        let worker_app = app.clone();
        let execution = tokio::spawn(async move { worker::execute(&worker_app, &task).await });
        if mode == "cancel" {
            tokio::time::timeout(Duration::from_secs(5), async {
                while !app.config.join("download-started").exists() {
                    tokio::time::sleep(Duration::from_millis(10)).await;
                }
            })
            .await
            .unwrap();
            sqlx::query("UPDATE tasks SET status='cancelling' WHERE kind='download' AND target=? AND status='running'")
                .bind(&media).execute(&app.store.pool).await.unwrap();
        }
        execution.await.unwrap().unwrap();
        let (id, status): (String, String) =
            sqlx::query_as("SELECT id,status FROM tasks WHERE kind='download' AND target=?")
                .bind(&media)
                .fetch_one(&app.store.pool)
                .await
                .unwrap();
        assert_eq!(
            status,
            if mode == "cancel" {
                "cancelled"
            } else {
                "failed"
            }
        );
        assert!(
            !app.data.join(".staging").join(&id).exists(),
            "{mode} left staging files"
        );
        let retry = app
            .store
            .queue_download(&media, "manual retry")
            .await
            .unwrap();
        assert_ne!(retry.id, id);
        assert!(!app.data.join(".staging").join(id).exists());
    }
}
