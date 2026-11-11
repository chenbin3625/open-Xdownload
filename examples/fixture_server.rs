//! A separate, disposable offline server for browser integration tests.
use anyhow::Result;
use axum::{Json, Router, extract::State, routing::post};
use serde_json::{Value, json};
use std::{path::PathBuf, time::Duration};
use x_archive::{api, app::App, worker};

#[tokio::main]
async fn main() -> Result<()> {
    let temp = tempfile::tempdir()?;
    let mut app = App::new(temp.path().join("data"), temp.path().join("config"), None).await?;
    let root = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
    app.gallery_dl = root
        .join("tests/fixtures/gallery.py")
        .to_string_lossy()
        .into();
    app.yt_dlp = root
        .join("tests/fixtures/ytdlp.py")
        .to_string_lossy()
        .into();
    app.client = reqwest::Client::builder()
        .timeout(Duration::from_millis(20))
        .build()?;
    let assets = app.config.join("fixtures");
    std::fs::create_dir(&assets)?;
    let image = image::RgbImage::from_fn(900, 600, |x, y| {
        if x > 160 && x < 740 && y > 150 && y < 450 {
            image::Rgb([112, 181, 189])
        } else if x % 90 < 3 || y % 75 < 3 {
            image::Rgb([61, 70, 78])
        } else {
            image::Rgb([29 + (x / 20) as u8, 34 + (y / 12) as u8, 44])
        }
    });
    image.save(assets.join("photo.png"))?;
    let status = std::process::Command::new("ffmpeg")
        .args([
            "-nostdin",
            "-v",
            "error",
            "-y",
            "-f",
            "lavfi",
            "-i",
            "testsrc2=size=640x360:rate=24",
            "-t",
            "2",
            "-c:v",
            "libx264",
            "-pix_fmt",
            "yuv420p",
            "-movflags",
            "+faststart",
        ])
        .arg(assets.join("video.mp4"))
        .status()?;
    anyhow::ensure!(status.success(), "fixture video generation failed");
    let scheduler = worker::start(app.clone()).await?;
    let fixture_routes = Router::new()
        .route("/__fixture/phase", post(phase))
        .route("/__fixture/reset", post(reset))
        .with_state(app.clone());
    let router = api::router(app).merge(fixture_routes);
    let port = std::env::var("X_ARCHIVE_TEST_PORT")
        .unwrap_or_else(|_| "3101".into())
        .parse::<u16>()?;
    let listener = tokio::net::TcpListener::bind(("127.0.0.1", port)).await?;
    println!(
        "Offline browser fixture server on http://{}",
        listener.local_addr()?
    );
    axum::serve(listener, router).await?;
    drop(scheduler);
    drop(temp);
    Ok(())
}
async fn phase(State(app): State<App>, Json(value): Json<Value>) -> Json<Value> {
    let value = value["phase"].as_u64().unwrap_or(1).clamp(1, 4);
    tokio::fs::write(app.config.join("phase"), value.to_string())
        .await
        .unwrap();
    Json(json!({"ok":true}))
}
async fn reset(State(app): State<App>) -> Json<Value> {
    sqlx::query("DELETE FROM schedules")
        .execute(&app.store.pool)
        .await
        .unwrap();
    let _ = tokio::fs::remove_file(app.config.join("cookie-pool.json")).await;
    *app.rotation.lock().await = Default::default();
    sqlx::query("DELETE FROM tasks")
        .execute(&app.store.pool)
        .await
        .unwrap();
    sqlx::query("DELETE FROM tweets")
        .execute(&app.store.pool)
        .await
        .unwrap();
    sqlx::query("DELETE FROM users")
        .execute(&app.store.pool)
        .await
        .unwrap();
    tokio::fs::write(app.config.join("phase"), "1")
        .await
        .unwrap();
    Json(json!({"ok":true}))
}
