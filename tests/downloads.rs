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

    for (round, image_root, video_root) in [
        (0, "images", "videos"),
        (1, "photos/originals", "clips/originals"),
    ] {
        if round == 1 {
            let mut settings = app.settings.write().await;
            settings.image_path = image_root.into();
            settings.video_path = video_root.into();
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
                let (media_root, extension) = if ordinal == 1 {
                    (image_root, "png")
                } else {
                    (video_root, "mp4")
                };
                let expected_file =
                    format!("{media_root}/{username}/{tweet_id}_{ordinal}.{extension}");
                let expected_thumbnail = format!("thumbnails/{username}/{id}.jpg");
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
    assert_eq!(ready, 12);
}
