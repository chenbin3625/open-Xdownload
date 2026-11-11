use axum::{
    Router,
    body::Body,
    http::{Request, StatusCode},
};
use http_body_util::BodyExt;
use serde_json::{Value, json};
use tempfile::TempDir;
use tower::ServiceExt;
use x_archive::{
    api,
    app::App,
    config::private_write,
    store::{ExtractedMedia, ExtractedTweet},
    tools, worker,
};

async fn app(password: Option<String>) -> (TempDir, App, Router) {
    let directory = tempfile::tempdir().unwrap();
    let app = App::new(
        directory.path().join("data"),
        directory.path().join("config"),
        password,
    )
    .await
    .unwrap();
    let router = api::router(app.clone());
    (directory, app, router)
}
async fn call(router: &Router, method: &str, path: &str, body: Value) -> (StatusCode, Value) {
    let response = router
        .clone()
        .oneshot(
            Request::builder()
                .method(method)
                .uri(path)
                .header("Host", "127.0.0.1:3000")
                .header("Content-Type", "application/json")
                .body(Body::from(body.to_string()))
                .unwrap(),
        )
        .await
        .unwrap();
    let status = response.status();
    let body = response.into_body().collect().await.unwrap().to_bytes();
    (status, serde_json::from_slice(&body).unwrap())
}

#[tokio::test]
async fn settings_persist_and_invalid_paths_are_rejected() {
    let (_dir, app, router) = app(None).await;
    let (_, data) = call(&router, "GET", "/api/settings", json!(null)).await;
    let mut settings = data["settings"].clone();
    settings["download_concurrency"] = json!(2);
    settings["image_path"] = json!("photos/originals");
    settings["image_path_template"] = json!("{kind}/{author}/{tweet_id}_{ordinal}.{extension}");
    assert_eq!(
        call(&router, "PUT", "/api/settings", settings.clone())
            .await
            .0,
        StatusCode::OK
    );
    let reopened = App::new(app.data.clone(), app.config.clone(), None)
        .await
        .unwrap();
    let saved = reopened.settings.read().await;
    assert_eq!(saved.image_path, "photos/originals");
    assert_eq!(
        saved.image_path_template,
        "{kind}/{author}/{tweet_id}_{ordinal}.{extension}"
    );
    assert_eq!(saved.thumbnail_path, "thumbnails");
    drop(saved);
    settings["image_path"] = json!("../config");
    assert_eq!(
        call(&router, "PUT", "/api/settings", settings.clone())
            .await
            .0,
        StatusCode::BAD_REQUEST
    );
    settings["image_path"] = json!("images");
    settings["image_path_template"] = json!("{author}/{unknown}.{extension}");
    assert_eq!(
        call(&router, "PUT", "/api/settings", settings).await.0,
        StatusCode::BAD_REQUEST
    );
    #[cfg(unix)]
    {
        std::os::unix::fs::symlink(&app.config, app.data.join("escape")).unwrap();
        assert!(
            x_archive::config::safe_path(&app.data, "escape/cookies.txt")
                .await
                .is_err()
        );
    }
}

#[tokio::test]
async fn authentication_and_request_origin_protect_all_private_routes() {
    let (_dir, _app, router) = app(Some("a-test-password-123".into())).await;
    assert_eq!(
        call(&router, "GET", "/api/media", json!(null)).await.0,
        StatusCode::UNAUTHORIZED
    );
    let response = router
        .clone()
        .oneshot(
            Request::builder()
                .method("POST")
                .uri("/api/session")
                .header("Host", "127.0.0.1:3000")
                .header("Origin", "https://evil.test")
                .header("Content-Type", "application/json")
                .body(Body::from(r#"{"password":"a-test-password-123"}"#))
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::FORBIDDEN);
    let response = router
        .clone()
        .oneshot(
            Request::builder()
                .method("POST")
                .uri("/api/session")
                .header("Host", "127.0.0.1:3000")
                .header("Content-Type", "application/json")
                .body(Body::from(r#"{"password":"a-test-password-123"}"#))
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::OK);
    let cookie = response.headers()["set-cookie"]
        .to_str()
        .unwrap()
        .split(';')
        .next()
        .unwrap()
        .to_owned();
    let response = router
        .clone()
        .oneshot(
            Request::builder()
                .uri("/api/media")
                .header("Host", "127.0.0.1:3000")
                .header("Cookie", cookie)
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::OK);
    let (_dir, _app, local) = app(None).await;
    let response = local
        .oneshot(
            Request::builder()
                .uri("/api/settings")
                .header("Host", "evil.test")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::FORBIDDEN);
}

#[tokio::test]
async fn entered_cookie_never_returns_credentials_and_tools_cannot_overwrite_original() {
    let (_dir, app, router) = app(None).await;
    let (status, response) = call(
        &router,
        "POST",
        "/api/cookies",
        json!({"cookie":"Cookie: auth_token=TEST_SECRET_AUTH; ct0=TEST_SECRET_CSRF; session=OTHER_SECRET"}),
    )
    .await;
    assert_eq!(status, StatusCode::OK);
    assert_eq!(response["cookie"]["status"], "unknown");
    assert_eq!(response["task"]["kind"], "cookie_check");
    assert_eq!(response["task"]["status"], "queued");
    assert_eq!(response["task"]["target"], app.cookie.read().await.revision);
    assert!(response["cookie"].get("revision").is_none());
    assert!(!response.to_string().contains("SECRET"));
    let (_, status_response) = call(&router, "GET", "/api/cookies", json!(null)).await;
    assert!(!status_response.to_string().contains("SECRET"));
    let saved = tokio::fs::read_to_string(app.config.join("cookies.txt"))
        .await
        .unwrap();
    assert!(x_archive::domain::validate_cookies(&saved).is_ok());
    assert!(saved.contains("\tauth_token\tTEST_SECRET_AUTH\n"));
    assert!(saved.contains("\tct0\tTEST_SECRET_CSRF\n"));
    assert!(!saved.contains("OTHER_SECRET"));
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        assert_eq!(
            std::fs::metadata(app.config.join("cookies.txt"))
                .unwrap()
                .permissions()
                .mode()
                & 0o777,
            0o600
        );
    }
    let lease = tools::session_cookie(&app).await.unwrap();
    private_write(&app.config.join("cookies.txt"), b"new-cookie")
        .await
        .unwrap();
    tokio::fs::write(&lease.0, b"old-tool-cookie")
        .await
        .unwrap();
    assert_eq!(
        tokio::fs::read_to_string(app.config.join("cookies.txt"))
            .await
            .unwrap(),
        "new-cookie"
    );
    let path = lease.0.clone();
    drop(lease);
    assert!(!path.exists());
    assert_eq!(
        call(&router, "DELETE", "/api/cookies", json!(null)).await.0,
        StatusCode::OK
    );
    assert!(!app.config.join("cookies.txt").exists());
}

#[tokio::test]
async fn invalid_cookie_input_preserves_the_session_and_does_not_queue_checks() {
    let (_dir, app, router) = app(None).await;
    let (status, response) = call(
        &router,
        "POST",
        "/api/cookies",
        json!({"cookie":"auth_token=SECRET_ONLY"}),
    )
    .await;
    assert_eq!(status, StatusCode::BAD_REQUEST);
    assert!(!response.to_string().contains("SECRET"));
    assert!(!app.config.join("cookies.txt").exists());
    assert_eq!(app.cookie.read().await.status, "missing");
    let task_count = || async {
        sqlx::query_scalar::<_, i64>("SELECT COUNT(*) FROM tasks")
            .fetch_one(&app.store.pool)
            .await
            .unwrap()
    };
    assert_eq!(task_count().await, 0);
    assert_eq!(
        call(
            &router,
            "POST",
            "/api/cookies",
            json!({"cookie":"auth_token=SECRET_AUTH; ct0=SECRET_CSRF"}),
        )
        .await
        .0,
        StatusCode::OK
    );
    let original = tokio::fs::read(app.config.join("cookies.txt"))
        .await
        .unwrap();
    let revision = app.cookie.read().await.revision.clone();
    for input in [
        json!({"cookie":""}),
        json!({"cookie":"auth_token=SECRET_AUTH; ct0="}),
        json!({"cookie":"auth_token=SECRET_AUTH; ct0=SECRET_CSRF; ct0=SECRET_OTHER"}),
        json!({"cookie":"auth_token=SECRET_AUTH; ct0=SECRET_CSRF\n"}),
        json!({"cookie":"A".repeat(1024 * 1024 + 1)}),
        json!("auth_token=SECRET_AUTH; ct0=SECRET_CSRF"),
        json!({"cookie": ["SECRET_AUTH", "SECRET_CSRF"]}),
        json!({"missing":"SECRET_AUTH"}),
    ] {
        let (status, response) = call(&router, "POST", "/api/cookies", input).await;
        assert_eq!(status, StatusCode::BAD_REQUEST);
        assert!(!response.to_string().contains("SECRET"));
        assert_eq!(
            tokio::fs::read(app.config.join("cookies.txt"))
                .await
                .unwrap(),
            original
        );
        assert_eq!(app.cookie.read().await.revision, revision);
        assert_eq!(task_count().await, 1);
    }
}

#[tokio::test]
async fn cookie_checks_distinguish_authentication_restrictions_and_invalid_tool_output() {
    let (_dir, mut app, router) = app(None).await;
    app.gallery_dl = format!("{}/tests/fixtures/gallery.py", env!("CARGO_MANIFEST_DIR"));
    private_write(&app.config.join("cookies.txt"), b"OFFLINE_COOKIE_SECRET")
        .await
        .unwrap();
    app.cookie.write().await.revision = "test-session".into();
    for (phase, expected) in [
        ("1", "valid"),
        ("4", "invalid"),
        ("5", "invalid"),
        ("6", "unknown"),
        ("7", "unknown"),
        ("8", "limited"),
    ] {
        tokio::fs::write(app.config.join("phase"), phase)
            .await
            .unwrap();
        let task = app
            .store
            .enqueue("cookie_check", "test-session", "test")
            .await
            .unwrap();
        assert!(app.store.claim(&task.id).await.unwrap());
        worker::execute(&app, &task).await.unwrap();
        let (_, cookie) = call(&router, "GET", "/api/cookies", json!(null)).await;
        assert_eq!(cookie["status"], expected, "phase {phase}");
        assert!(!cookie.to_string().contains("SECRET"));
        let (status, error): (String, Option<String>) =
            sqlx::query_as("SELECT status,error FROM tasks WHERE id=?")
                .bind(task.id)
                .fetch_one(&app.store.pool)
                .await
                .unwrap();
        assert_eq!(
            status,
            if expected == "valid" {
                "succeeded"
            } else {
                "failed"
            }
        );
        assert!(!error.unwrap_or_default().contains("SECRET"));
    }
}

#[tokio::test]
async fn cancellation_holds_deduplication_lock_until_execution_has_exited() {
    let (_dir, app, router) = app(None).await;
    let task = app.store.enqueue("tweet", "123", "test").await.unwrap();
    app.store.claim(&task.id).await.unwrap();
    assert_eq!(
        call(
            &router,
            "POST",
            &format!("/api/tasks/{}/cancel", task.id),
            json!(null)
        )
        .await
        .0,
        StatusCode::OK
    );
    assert_eq!(
        app.store.enqueue("tweet", "123", "test").await.unwrap().id,
        task.id
    );
    assert_eq!(
        call(
            &router,
            "POST",
            &format!("/api/tasks/{}/retry", task.id),
            json!(null)
        )
        .await
        .0,
        StatusCode::BAD_REQUEST
    );
    worker::execute(&app, &task).await.unwrap();
    let (_, new) = call(
        &router,
        "POST",
        &format!("/api/tasks/{}/retry", task.id),
        json!(null),
    )
    .await;
    assert_ne!(new["id"], task.id);
    let old: String = sqlx::query_scalar("SELECT status FROM tasks WHERE id=?")
        .bind(task.id)
        .fetch_one(&app.store.pool)
        .await
        .unwrap();
    assert_eq!(old, "cancelled");
}

#[tokio::test]
async fn cancelling_queued_downloads_leaves_a_retryable_media_state() {
    let (_dir, app, router) = app(None).await;
    sqlx::query("INSERT INTO tweets(id,author,author_name,text,published_at,url) VALUES('123','jack','Jack','sample','2026-11-10T00:00:00Z','https://x.com/jack/status/123')")
        .execute(&app.store.pool).await.unwrap();
    for id in ["cancel", "cookie-delete"] {
        sqlx::query("INSERT INTO media(id,tweet_id,ordinal,kind,extension,remote_url) VALUES(?,'123',?,'image','jpg','https://pbs.twimg.com/media/test.jpg')")
            .bind(id).bind(if id == "cancel" { 1 } else { 2 }).execute(&app.store.pool).await.unwrap();
        let task = app.store.queue_download(id, "test").await.unwrap();
        let (status, _) = if id == "cancel" {
            call(
                &router,
                "POST",
                &format!("/api/tasks/{}/cancel", task.id),
                json!(null),
            )
            .await
        } else {
            call(&router, "DELETE", "/api/cookies", json!(null)).await
        };
        assert_eq!(status, StatusCode::OK);
        let (status, error): (String, Option<String>) =
            sqlx::query_as("SELECT status,error FROM media WHERE id=?")
                .bind(id)
                .fetch_one(&app.store.pool)
                .await
                .unwrap();
        assert_eq!(status, "failed");
        assert!(error.is_some());
        let replacement = app.store.queue_download(id, "retry").await.unwrap();
        assert_ne!(replacement.id, task.id);
        assert_eq!(replacement.status, "queued");
    }
}

#[tokio::test]
async fn tweet_previews_group_media_in_order_and_respect_pagination_and_filters() {
    let (_dir, app, router) = app(None).await;
    sqlx::query("INSERT INTO tweets(id,author,author_name,text,published_at,url) VALUES('1','alice','Alice','with media','2026-11-10T12:00:00Z','https://x.com/alice/status/1'),('2','bob','Bob','another tweet','2026-11-09T12:00:00Z','https://x.com/bob/status/2'),('3','alice','Alice','text only','2026-11-08T12:00:00Z','https://x.com/alice/status/3')")
        .execute(&app.store.pool).await.unwrap();
    sqlx::query("INSERT INTO media(id,tweet_id,ordinal,kind,extension,remote_url) VALUES('first-2','1',2,'video','mp4','https://video.twimg.com/test.mp4'),('first-1','1',1,'image','jpg','https://pbs.twimg.com/media/test.jpg'),('second-1','2',1,'gif','mp4','https://video.twimg.com/test.mp4')")
        .execute(&app.store.pool).await.unwrap();
    let (status, page) = call(&router, "GET", "/api/tweets?limit=2", json!(null)).await;
    assert_eq!(status, StatusCode::OK);
    assert_eq!(page["total"], 3);
    assert_eq!(page["items"].as_array().unwrap().len(), 2);
    let first = &page["items"][0];
    assert_eq!(first["id"], "1");
    assert_eq!(first["media_count"], 2);
    assert_eq!(first["ready_count"], 0);
    assert_eq!(first["media"][0]["id"], "first-1");
    assert_eq!(first["media"][1]["id"], "first-2");
    assert_eq!(first["media"][1]["kind"], "video");
    assert_eq!(first["media"][1]["has_file"], false);
    assert_eq!(first["media"][1]["status"], "pending");
    assert!(first["media"][0].get("remote_url").is_none());
    let second = &page["items"][1];
    assert_eq!(second["id"], "2");
    assert_eq!(second["media"].as_array().unwrap().len(), 1);
    assert_eq!(second["media"][0]["id"], "second-1");
    let (_, page) = call(&router, "GET", "/api/tweets?limit=2&page=2", json!(null)).await;
    assert_eq!(page["items"][0]["id"], "3");
    assert_eq!(page["items"][0]["media"], json!([]));
    let (_, page) = call(&router, "GET", "/api/tweets?search=bob", json!(null)).await;
    assert_eq!(page["total"], 1);
    assert_eq!(page["items"][0]["media"][0]["id"], "second-1");
    let (_, page) = call(&router, "GET", "/api/tweets?search=absent", json!(null)).await;
    assert_eq!(page["items"], json!([]));
}

#[tokio::test]
async fn media_filters_tags_favorites_and_http_ranges_use_real_persisted_files() {
    let (_dir, app, router) = app(None).await;
    app.store
        .ingest(&[ExtractedTweet {
            id: "123".into(),
            author: "jack".into(),
            author_name: "Jack".into(),
            text: "sample".into(),
            published_at: "2026-11-10T12:00:00Z".into(),
            media: vec![ExtractedMedia {
                ordinal: 1,
                kind: "video".into(),
                extension: "mp4".into(),
                url: "https://video.twimg.com/test.mp4".into(),
            }],
        }])
        .await
        .unwrap();
    let id: String = sqlx::query_scalar("SELECT id FROM media")
        .fetch_one(&app.store.pool)
        .await
        .unwrap();
    tokio::fs::create_dir(app.data.join("videos"))
        .await
        .unwrap();
    tokio::fs::write(app.data.join("videos/test.mp4"), b"0123456789")
        .await
        .unwrap();
    tokio::fs::create_dir(app.data.join("thumbnails"))
        .await
        .unwrap();
    let thumbnail_path = format!("thumbnails/{id}.jpg");
    tokio::fs::write(app.data.join(&thumbnail_path), b"legacy-thumbnail")
        .await
        .unwrap();
    sqlx::query(
        "UPDATE media SET status='ready',file_path='videos/test.mp4',thumbnail_path=?,size=10",
    )
    .bind(&thumbnail_path)
    .execute(&app.store.pool)
    .await
    .unwrap();
    assert_eq!(
        call(
            &router,
            "PATCH",
            &format!("/api/media/{id}"),
            json!({"favorite":true,"tags":["reference"]})
        )
        .await
        .0,
        StatusCode::OK
    );
    let (_, page) = call(
        &router,
        "GET",
        "/api/media?kind=video&favorite=true&tag=reference&from=2026-11-10&to=2026-11-10",
        json!(null),
    )
    .await;
    assert_eq!(page["total"], 1);
    assert!(page["items"][0].get("remote_url").is_none());
    let (_, tweets) = call(&router, "GET", "/api/tweets", json!(null)).await;
    assert_eq!(tweets["items"][0]["media"][0], page["items"][0]);
    assert_eq!(tweets["items"][0]["ready_count"], 1);
    let response = router
        .clone()
        .oneshot(
            Request::builder()
                .uri(format!("/api/media/{id}/file"))
                .header("Host", "127.0.0.1:3000")
                .header("Range", "bytes=2-5")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::PARTIAL_CONTENT);
    assert_eq!(response.headers()["content-type"], "video/mp4");
    assert_eq!(
        response.into_body().collect().await.unwrap().to_bytes(),
        "2345"
    );
    let response = router
        .oneshot(
            Request::builder()
                .uri(format!("/api/media/{id}/thumbnail"))
                .header("Host", "127.0.0.1:3000")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::OK);
    assert_eq!(response.headers()["content-type"], "image/jpeg");
    assert_eq!(
        response.into_body().collect().await.unwrap().to_bytes(),
        "legacy-thumbnail"
    );
}

#[tokio::test]
async fn only_complete_snapshots_can_report_relationship_changes() {
    let (_dir, app, router) = app(None).await;
    sqlx::query("INSERT INTO users(id,username) VALUES('user','jack')")
        .execute(&app.store.pool)
        .await
        .unwrap();
    for (id, status) in [
        ("base", "complete"),
        ("new", "complete"),
        ("partial", "partial"),
    ] {
        sqlx::query("INSERT INTO snapshots(id,user_id,direction,status,fetched_count) VALUES(?,'user','following',?,2)").bind(id).bind(status).execute(&app.store.pool).await.unwrap();
    }
    for (snapshot, id) in [
        ("base", "1"),
        ("base", "2"),
        ("new", "2"),
        ("new", "3"),
        ("partial", "2"),
    ] {
        sqlx::query("INSERT INTO relationships(snapshot_id,user_id,username) VALUES(?,?,?)")
            .bind(snapshot)
            .bind(id)
            .bind(format!("user{id}"))
            .execute(&app.store.pool)
            .await
            .unwrap();
    }
    let (status, diff) = call(
        &router,
        "GET",
        "/api/snapshots/new/diff?base=base",
        json!(null),
    )
    .await;
    assert_eq!(status, StatusCode::OK);
    assert_eq!(diff["added"][0]["id"], "3");
    assert_eq!(diff["removed"][0]["id"], "1");
    assert_eq!(
        call(
            &router,
            "GET",
            "/api/snapshots/partial/diff?base=base",
            json!(null)
        )
        .await
        .0,
        StatusCode::BAD_REQUEST
    );
}
