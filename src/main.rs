use anyhow::{Result, ensure};
use clap::Parser;
use std::{net::SocketAddr, path::PathBuf};
use tracing_subscriber::EnvFilter;
use x_archive::{api, app::App, worker};

#[derive(Parser)]
#[command(version, about = "X Archive")]
struct Args {
    #[arg(long, env = "X_ARCHIVE_BIND", default_value = "127.0.0.1:3000")]
    bind: SocketAddr,
    #[arg(long, env = "X_ARCHIVE_PASSWORD", hide_env_values = true)]
    password: Option<String>,
    #[arg(long, env = "X_ARCHIVE_DATA", default_value = "data")]
    data_dir: PathBuf,
    #[arg(long, env = "X_ARCHIVE_CONFIG", default_value = "config")]
    config_dir: PathBuf,
}

#[tokio::main]
async fn main() -> Result<()> {
    let args = Args::parse();
    tracing_subscriber::fmt()
        .with_env_filter(
            EnvFilter::try_from_default_env()
                .unwrap_or_else(|_| "x_archive=info,tower_http=warn".into()),
        )
        .init();
    let bind = args.bind;
    let password = args.password.filter(|p| !p.is_empty());
    ensure!(
        bind.ip().is_loopback() || password.is_some(),
        "对外监听需要通过 --password 或 X_ARCHIVE_PASSWORD 设置访问密码"
    );
    // One process owns the persistent queue and its recovery operation.
    x_archive::config::private_dir(&args.config_dir).await?;
    let lock = std::fs::OpenOptions::new()
        .create(true)
        .truncate(false)
        .write(true)
        .open(args.config_dir.join(".instance.lock"))?;
    fs2::FileExt::try_lock_exclusive(&lock)?;
    let app = App::new(args.data_dir, args.config_dir, password).await?;
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
