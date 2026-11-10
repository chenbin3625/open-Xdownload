use axum::{
    Router,
    body::Body,
    http::{Request, StatusCode},
};
use http_body_util::BodyExt;
use serde_json::Value;
use tempfile::TempDir;
use tower::ServiceExt;
use x_archive::{
    api,
    app::App,
    store::{ExtractedMedia, ExtractedTweet, Store},
};

async fn fixture() -> (TempDir, App, Router) {
    let directory = tempfile::tempdir().unwrap();
    let app = App::new(
        directory.path().join("data"),
        directory.path().join("config"),
        None,
    )
    .await
    .unwrap();
    sqlx::query("INSERT INTO tweets(id,author,published_at,url) VALUES('123','archive_test','2026-11-10T00:00:00Z','https://x.com/archive_test/status/123')")
        .execute(&app.store.pool).await.unwrap();
    for (ordinal, id, status) in [
        (1, "one", "ready"),
        (2, "two", "failed"),
        (3, "three", "pending"),
    ] {
        sqlx::query("INSERT INTO media(id,tweet_id,ordinal,kind,extension,remote_url,status) VALUES(?,'123',?,'image','jpg','https://pbs.twimg.com/media/test.jpg',?)")
            .bind(id).bind(ordinal).bind(status).execute(&app.store.pool).await.unwrap();
    }
    sqlx::query("INSERT INTO tasks(id,kind,target,label,status,progress,attempts,result) VALUES('job','tweet','123','归档推文 123','succeeded',100,1,'已识别 3 个媒体')")
        .execute(&app.store.pool).await.unwrap();
    for (id, target, status, progress, size) in [
        ("first", "one", "succeeded", 100, 8213655),
        ("second", "two", "failed", 70, 0),
        ("third", "three", "queued", 0, 0),
    ] {
        sqlx::query("INSERT INTO tasks(id,kind,target,label,status,progress,result_size,parent_id,error) VALUES(?,'download',?,'自动下载媒体',?,?,?,'job',?)")
            .bind(id).bind(target).bind(status).bind(progress).bind(size)
            .bind((status == "failed").then_some("连接失败"))
            .execute(&app.store.pool).await.unwrap();
    }
    let router = api::router(app.clone());
    (directory, app, router)
}

async fn call(router: &Router, method: &str, path: &str) -> (StatusCode, Value) {
    let response = router
        .clone()
        .oneshot(
            Request::builder()
                .method(method)
                .uri(path)
                .header("Host", "127.0.0.1:3000")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    let status = response.status();
    let body = response.into_body().collect().await.unwrap().to_bytes();
    (status, serde_json::from_slice(&body).unwrap())
}

#[tokio::test]
async fn groups_are_filtered_and_paginated_after_aggregating_downloads() {
    let (_directory, app, router) = fixture().await;
    sqlx::query("INSERT INTO tasks(id,kind,target,label,status,progress) VALUES('repeat','tweet','123','重复导入','succeeded',100)")
        .execute(&app.store.pool).await.unwrap();
    let (status, page) = call(
        &router,
        "GET",
        "/api/tasks?grouped=true&status=running&limit=1",
    )
    .await;
    assert_eq!(status, StatusCode::OK);
    assert_eq!(page["total"], 1);
    let job = &page["items"][0];
    assert_eq!(job["id"], "job");
    assert_eq!(job["status"], "running");
    assert_eq!(job["progress"], 56);
    assert_eq!(job["download_total"], 3);
    assert_eq!(job["download_completed"], 1);
    assert_eq!(job["download_failed"], 1);
    assert_eq!(job["download_size"], 8213655);
    assert_eq!(job["download_error"], "连接失败");
    let (_, page) = call(
        &router,
        "GET",
        "/api/tasks?grouped=true&status=succeeded&limit=1",
    )
    .await;
    assert_eq!(page["total"], 1);
    assert_eq!(page["items"][0]["id"], "repeat");
    let (_, page) = call(
        &router,
        "GET",
        "/api/tasks?grouped=true&status=running&limit=1&page=2",
    )
    .await;
    assert_eq!(page["total"], 1);
    assert_eq!(page["items"].as_array().unwrap().len(), 0);
    let (_, page) = call(&router, "GET", "/api/tasks?grouped=true").await;
    assert_eq!(page["total"], 2);
    let (_, page) = call(&router, "GET", "/api/tasks").await;
    assert_eq!(page["total"], 5);
    let (_, dashboard) = call(&router, "GET", "/api/dashboard").await;
    assert_eq!(dashboard["active_tasks"], 1);
    assert_eq!(dashboard["failed_tasks"], 0);
}

#[tokio::test]
async fn retry_keeps_completed_downloads_and_counts_only_the_latest_attempt() {
    let (_directory, app, router) = fixture().await;
    sqlx::query(
        "UPDATE tasks SET status='succeeded',progress=100,result_size=1024 WHERE id='third'",
    )
    .execute(&app.store.pool)
    .await
    .unwrap();
    let (_, page) = call(&router, "GET", "/api/tasks?grouped=true&status=failed").await;
    assert_eq!(page["total"], 1);
    assert_eq!(
        call(&router, "POST", "/api/tasks/job/retry").await.0,
        StatusCode::OK
    );
    let (_, page) = call(&router, "GET", "/api/tasks?grouped=true&status=running").await;
    assert_eq!(page["total"], 1);
    assert_eq!(page["items"][0]["download_total"], 3);
    assert_eq!(page["items"][0]["download_completed"], 2);
    assert_eq!(page["items"][0]["download_failed"], 0);
    let attempts: i64 = sqlx::query_scalar("SELECT COUNT(*) FROM tasks WHERE parent_id='job'")
        .fetch_one(&app.store.pool)
        .await
        .unwrap();
    assert_eq!(attempts, 4);
    sqlx::query("UPDATE tasks SET created_at='2026-11-10T00:00:00Z' WHERE parent_id='job'")
        .execute(&app.store.pool)
        .await
        .unwrap();
    sqlx::query("UPDATE tasks SET status='succeeded',progress=100,result_size=2048 WHERE parent_id='job' AND status='queued'")
        .execute(&app.store.pool).await.unwrap();
    let (_, page) = call(&router, "GET", "/api/tasks?grouped=true&status=succeeded").await;
    assert_eq!(page["total"], 1);
    assert_eq!(page["items"][0]["download_completed"], 3);
    assert_eq!(page["items"][0]["download_size"], 8213655 + 1024 + 2048);
    assert_eq!(page["items"][0]["progress"], 100);
}

#[tokio::test]
async fn cancel_reaches_downloads_after_the_import_has_finished() {
    let (_directory, app, router) = fixture().await;
    sqlx::query("UPDATE tasks SET status='running' WHERE id='second'")
        .execute(&app.store.pool)
        .await
        .unwrap();
    assert_eq!(
        call(&router, "POST", "/api/tasks/job/cancel").await.0,
        StatusCode::OK
    );
    let states: Vec<(String, String)> = sqlx::query_as("SELECT id,status FROM tasks ORDER BY id")
        .fetch_all(&app.store.pool)
        .await
        .unwrap();
    assert_eq!(
        states,
        vec![
            ("first".into(), "succeeded".into()),
            ("job".into(), "succeeded".into()),
            ("second".into(), "cancelling".into()),
            ("third".into(), "cancelled".into()),
        ]
    );
    let media_status: String = sqlx::query_scalar("SELECT status FROM media WHERE id='three'")
        .fetch_one(&app.store.pool)
        .await
        .unwrap();
    assert_eq!(media_status, "failed");
    let (_, page) = call(&router, "GET", "/api/tasks?grouped=true&status=cancelling").await;
    assert_eq!(page["total"], 1);
    assert_eq!(page["items"][0]["download_completed"], 1);
}

#[tokio::test]
async fn ingestion_links_downloads_atomically_and_rejects_cancelled_parents() {
    let store = Store::memory().await.unwrap();
    let job = store.enqueue("tweet", "123", "import").await.unwrap();
    assert!(store.claim(&job.id).await.unwrap());
    let tweets = [ExtractedTweet {
        id: "123".into(),
        author: "archive_test".into(),
        author_name: "Archive".into(),
        text: "sample".into(),
        published_at: "2026-11-10T00:00:00Z".into(),
        media: vec![ExtractedMedia {
            ordinal: 1,
            kind: "image".into(),
            extension: "jpg".into(),
            url: "https://pbs.twimg.com/media/test.jpg".into(),
        }],
    }];
    store.ingest_for_task(&tweets, Some(&job.id)).await.unwrap();
    store.ingest_for_task(&tweets, Some(&job.id)).await.unwrap();
    let count: i64 = sqlx::query_scalar("SELECT COUNT(*) FROM tasks WHERE parent_id=?")
        .bind(&job.id)
        .fetch_one(&store.pool)
        .await
        .unwrap();
    assert_eq!(count, 1);
    sqlx::query("UPDATE tasks SET status='cancelled' WHERE id=?")
        .bind(&job.id)
        .execute(&store.pool)
        .await
        .unwrap();
    assert!(
        store
            .ingest_for_task(&tweets, Some(&job.id))
            .await
            .unwrap_err()
            .to_string()
            .contains("取消")
    );
    let count: i64 = sqlx::query_scalar("SELECT COUNT(*) FROM tasks WHERE parent_id=?")
        .bind(&job.id)
        .fetch_one(&store.pool)
        .await
        .unwrap();
    assert_eq!(count, 1);
}

#[tokio::test]
async fn enabling_automatic_downloads_creates_one_visible_batch() {
    let (_directory, app, router) = fixture().await;
    sqlx::query("DELETE FROM tasks")
        .execute(&app.store.pool)
        .await
        .unwrap();
    sqlx::query("UPDATE media SET status='pending'")
        .execute(&app.store.pool)
        .await
        .unwrap();
    app.store.queue_automatic_downloads().await.unwrap();
    app.store.queue_automatic_downloads().await.unwrap();
    let (_, page) = call(&router, "GET", "/api/tasks?grouped=true").await;
    assert_eq!(page["total"], 1);
    assert_eq!(page["items"][0]["label"], "自动下载任务");
    assert_eq!(page["items"][0]["status"], "running");
    assert_eq!(page["items"][0]["download_total"], 3);
    let (_, raw) = call(&router, "GET", "/api/tasks").await;
    assert_eq!(raw["total"], 4);
}

#[tokio::test]
async fn migration_retains_old_downloads_in_an_explicit_history_batch() {
    let pool = sqlx::SqlitePool::connect("sqlite::memory:").await.unwrap();
    sqlx::raw_sql(include_str!("../migrations/0001_initial.sql"))
        .execute(&pool)
        .await
        .unwrap();
    sqlx::query("INSERT INTO tasks(id,kind,target,label,status,result) VALUES('old','download','one','自动下载媒体','succeeded','已下载并生成预览，8213655 字节')")
        .execute(&pool).await.unwrap();
    sqlx::raw_sql(include_str!("../migrations/0002_task_groups.sql"))
        .execute(&pool)
        .await
        .unwrap();
    let row: (String, i64) =
        sqlx::query_as("SELECT parent_id,result_size FROM tasks WHERE id='old'")
            .fetch_one(&pool)
            .await
            .unwrap();
    assert_eq!(row, ("legacy-auto-downloads".into(), 8213655));
    let label: String =
        sqlx::query_scalar("SELECT label FROM tasks WHERE id='legacy-auto-downloads'")
            .fetch_one(&pool)
            .await
            .unwrap();
    assert_eq!(label, "历史自动下载");
}
