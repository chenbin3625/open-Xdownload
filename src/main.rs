use anyhow::{Result, ensure};
use std::{net::SocketAddr, path::PathBuf};
use tracing_subscriber::EnvFilter;
use x_archive::{api, app::App, worker};

#[tokio::main]
async fn main() -> Result<()> {
    tracing_subscriber::fmt()
        .with_env_filter(
            EnvFilter::try_from_default_env()
                .unwrap_or_else(|_| "x_archive=info,tower_http=warn".into()),
        )
        .init();
    let bind: SocketAddr = std::env::var("X_ARCHIVE_BIND")
        .unwrap_or_else(|_| "127.0.0.1:3000".into())
        .parse()?;
    let password = std::env::var("X_ARCHIVE_PASSWORD")
        .ok()
        .filter(|p| !p.is_empty());
    ensure!(
        bind.ip().is_loopback() || password.as_ref().is_some_and(|p| p.len() >= 12),
        "对外监听需要设置至少 12 位的 X_ARCHIVE_PASSWORD"
    );
    let app = App::new(
        PathBuf::from(std::env::var("X_ARCHIVE_DATA").unwrap_or_else(|_| "data".into())),
        PathBuf::from(std::env::var("X_ARCHIVE_CONFIG").unwrap_or_else(|_| "config".into())),
        password,
    )
    .await?;
    // One process owns the persistent queue and its recovery operation.
    let lock = std::fs::OpenOptions::new()
        .create(true)
        .truncate(false)
        .write(true)
        .open(app.data.join(".instance.lock"))?;
    fs2::FileExt::try_lock_exclusive(&lock)?;
    let sessions = app.config.join(".sessions");
    if let Ok(meta) = tokio::fs::symlink_metadata(&sessions).await {
        ensure!(
            !meta.file_type().is_symlink(),
            "临时会话目录不能使用符号链接"
        );
        tokio::fs::remove_dir_all(sessions).await?;
    }
    let staging = x_archive::config::safe_path(&app.data, ".staging").await?;
    if staging.exists() {
        tokio::fs::remove_dir_all(staging).await?;
    }
    app.store.recover().await?;
    let scheduler = worker::start(app.clone()).await?;
    let listener = tokio::net::TcpListener::bind(bind).await?;
    tracing::info!(%bind, "X Archive ready");
    axum::serve(listener, api::router(app))
        .with_graceful_shutdown(shutdown())
        .await?;
    drop(scheduler);
    drop(lock);
    Ok(())
}

async fn shutdown() {
    #[cfg(unix)]
    {
        let mut terminate =
            tokio::signal::unix::signal(tokio::signal::unix::SignalKind::terminate())
                .expect("signal handler");
        tokio::select! { _=tokio::signal::ctrl_c()=>{}, _=terminate.recv()=>{} }
    }
    #[cfg(not(unix))]
    {
        let _ = tokio::signal::ctrl_c().await;
    }
}
