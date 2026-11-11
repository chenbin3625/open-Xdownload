use axum::{
    Router,
    body::Body,
    http::{Request, StatusCode},
};
use http_body_util::BodyExt;
use serde_json::{Value, json};
use tower::ServiceExt;
use x_archive::{
    api, app::App, automation, config::private_write, sessions, store::Task, tools, worker,
};

async fn setup() -> (tempfile::TempDir, App, Router) {
    let dir = tempfile::tempdir().unwrap();
    let mut app = App::new(dir.path().join("data"), dir.path().join("config"), None)
        .await
        .unwrap();
    app.gallery_dl = format!("{}/tests/fixtures/gallery.py", env!("CARGO_MANIFEST_DIR"));
    private_write(&app.config.join("cookies.txt"), b"# Netscape HTTP Cookie File\n.x.com\tTRUE\t/\tTRUE\t0\tauth_token\tPRIMARY_SECRET\n.x.com\tTRUE\t/\tTRUE\t0\tct0\tcsrf\n").await.unwrap();
    let router = api::router(app.clone());
    (dir, app, router)
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
    let bytes = response.into_body().collect().await.unwrap().to_bytes();
    (status, serde_json::from_slice(&bytes).unwrap())
}
async fn execute(app: &App, id: &str) -> Task {
    assert!(app.store.claim(id).await.unwrap());
    let task = sqlx::query_as("SELECT * FROM tasks WHERE id=?")
        .bind(id)
        .fetch_one(&app.store.pool)
        .await
        .unwrap();
    worker::execute(app, &task).await.unwrap();
    sqlx::query_as("SELECT * FROM tasks WHERE id=?")
        .bind(id)
        .fetch_one(&app.store.pool)
        .await
        .unwrap()
}

#[tokio::test]
async fn batch_normalizes_deduplicates_and_validates_before_writing() {
    let (_dir, app, router) = setup().await;
    let items = json!([
        {"kind":"user","target":"@Example"}, {"kind":"user","target":"https://x.com/example"},
        {"kind":"list","target":"1234"}, {"kind":"list","target":"https://twitter.com/i/lists/1234"},
        {"kind":"following","target":"Example"}, {"kind":"tweet","target":"https://x.com/example/status/1234"}
    ]);
    let (status, result) = call(
        &router,
        "POST",
        "/api/archives/batch",
        json!({"items":items}),
    )
    .await;
    assert_eq!(status, StatusCode::OK);
    assert_eq!(result["count"], 4);
    assert_eq!(result["duplicates"], 2);
    let task_ids: Vec<_> = result["tasks"]
        .as_array()
        .unwrap()
        .iter()
        .map(|t| t["id"].clone())
        .collect();
    let (_, again) = call(
        &router,
        "POST",
        "/api/archives/batch",
        json!({"items":items}),
    )
    .await;
    assert_eq!(
        task_ids,
        again["tasks"]
            .as_array()
            .unwrap()
            .iter()
            .map(|t| t["id"].clone())
            .collect::<Vec<_>>()
    );
    let invalid = json!({"items":[{"kind":"user","target":"new_user"},{"kind":"list","target":"https://evil.test/123"}]});
    assert_eq!(
        call(&router, "POST", "/api/archives/batch", invalid)
            .await
            .0,
        StatusCode::BAD_REQUEST
    );
    let count: i64 = sqlx::query_scalar("SELECT COUNT(*) FROM users")
        .fetch_one(&app.store.pool)
        .await
        .unwrap();
    assert_eq!(count, 1);
    let big: Vec<_> = (0..201)
        .map(|_| json!({"kind":"list","target":"123"}))
        .collect();
    assert_eq!(
        call(&router, "POST", "/api/archives/batch", json!({"items":big}))
            .await
            .0,
        StatusCode::BAD_REQUEST
    );
    let max: Vec<_> = (0..200)
        .map(|n| json!({"kind":"list","target":(n+10000).to_string()}))
        .collect();
    assert_eq!(
        call(&router, "POST", "/api/archives/batch", json!({"items":max}))
            .await
            .1["count"],
        200
    );
}

#[tokio::test]
async fn list_and_following_expand_members_into_deduplicated_sync_jobs() {
    let (_dir, app, router) = setup().await;
    app.settings.write().await.auto_download = false;
    for (kind, target) in [("list", "1234"), ("following", "example")] {
        let (_, result) = call(
            &router,
            "POST",
            "/api/archives/batch",
            json!({"items":[{"kind":kind,"target":target}]}),
        )
        .await;
        let task = execute(&app, result["tasks"][0]["id"].as_str().unwrap()).await;
        assert_eq!(task.status, "succeeded", "{:?}", task.error);
        assert!(task.result.unwrap().contains("2 名成员"));
    }
    let syncs: Vec<Task> = sqlx::query_as("SELECT * FROM tasks WHERE kind='sync'")
        .fetch_all(&app.store.pool)
        .await
        .unwrap();
    assert_eq!(syncs.len(), 2);
    for task in syncs {
        assert_eq!(execute(&app, &task.id).await.status, "succeeded");
    }
    let media: i64 = sqlx::query_scalar("SELECT COUNT(*) FROM media")
        .fetch_one(&app.store.pool)
        .await
        .unwrap();
    assert_eq!(media, 3); // Fixture uses one shared tweet ID, so media are deduplicated.
}

#[tokio::test]
async fn schedules_crud_claims_due_work_once_and_survives_restart() {
    let (_dir, app, router) = setup().await;
    let body = json!({"name":"nightly","enabled":true,"interval_minutes":60,"items":[{"kind":"list","target":"1234"}]});
    let (status, value) = call(&router, "POST", "/api/schedules", body.clone()).await;
    assert_eq!(status, StatusCode::OK);
    let id = value["id"].as_str().unwrap();
    sqlx::query("UPDATE schedules SET next_run_at='2000-01-01T00:00:00.000Z' WHERE id=?")
        .bind(id)
        .execute(&app.store.pool)
        .await
        .unwrap();
    let (a, b) = tokio::join!(
        automation::run_schedule(&app.store, id, true),
        automation::run_schedule(&app.store, id, true)
    );
    assert_eq!(a.unwrap().len() + b.unwrap().len(), 1);
    let reopened = App::new(app.data.clone(), app.config.clone(), None)
        .await
        .unwrap();
    let (_, rows) = call(&api::router(reopened), "GET", "/api/schedules", json!(null)).await;
    assert_eq!(rows[0]["last_task_ids"].as_array().unwrap().len(), 1);
    assert!(!rows[0]["last_run_at"].is_null());
    assert_eq!(
        call(
            &router,
            "PATCH",
            &format!("/api/schedules/{id}"),
            json!({"enabled":false})
        )
        .await
        .0,
        StatusCode::OK
    );
    assert!(
        automation::run_schedule(&app.store, id, true)
            .await
            .unwrap()
            .is_empty()
    );
    assert_eq!(
        call(
            &router,
            "POST",
            &format!("/api/schedules/{id}/run"),
            json!(null)
        )
        .await
        .0,
        StatusCode::OK
    );
    let mut edit = body;
    edit["name"] = json!("updated");
    edit["interval_minutes"] = json!(5);
    assert_eq!(
        call(&router, "PUT", &format!("/api/schedules/{id}"), edit)
            .await
            .0,
        StatusCode::OK
    );
    assert_eq!(
        call(
            &router,
            "DELETE",
            &format!("/api/schedules/{id}"),
            json!(null)
        )
        .await
        .0,
        StatusCode::OK
    );
    assert_eq!(
        call(&router, "GET", "/api/schedules", json!(null)).await.1,
        json!([])
    );
}

#[tokio::test]
async fn mixed_tweet_schedules_run_edit_and_reuse_persisted_targets() {
    let (_dir, app, router) = setup().await;
    let body = json!({"name":"mixed","enabled":true,"interval_minutes":5,"items":[
        {"kind":"tweet","target":"https://x.com/example/status/1234"},
        {"kind":"list","target":"5678"}, {"kind":"user","target":"example"}
    ]});
    let (status, created) = call(&router, "POST", "/api/schedules", body).await;
    assert_eq!(status, StatusCode::OK);
    let id = created["id"].as_str().unwrap();
    let (_, schedules) = call(&router, "GET", "/api/schedules", json!(null)).await;
    assert_eq!(schedules[0]["items"][0]["target"], "1234");
    let (status, run) = call(
        &router,
        "POST",
        &format!("/api/schedules/{id}/run"),
        json!(null),
    )
    .await;
    assert_eq!(status, StatusCode::OK, "{run}");
    assert_eq!(run["tasks"].as_array().unwrap().len(), 3);
    let mut edit = schedules[0].clone();
    edit.as_object_mut()
        .unwrap()
        .retain(|key, _| ["name", "enabled", "interval_minutes", "items"].contains(&key.as_str()));
    assert_eq!(
        call(&router, "PUT", &format!("/api/schedules/{id}"), edit)
            .await
            .0,
        StatusCode::OK
    );
    sqlx::query("UPDATE schedules SET next_run_at='2000-01-01T00:00:00.000Z' WHERE id=?")
        .bind(id)
        .execute(&app.store.pool)
        .await
        .unwrap();
    let tasks = automation::run_schedule(&app.store, id, true)
        .await
        .unwrap();
    assert_eq!(tasks.len(), 3);
    assert_eq!(tasks[0].target, "1234");
    for (old, new) in run["tasks"].as_array().unwrap().iter().zip(tasks) {
        assert_eq!(old["id"], new.id);
    }
}

#[tokio::test]
async fn a_broken_schedule_does_not_block_other_schedules_or_periodic_syncs() {
    let (_dir, app, _) = setup().await;
    sqlx::query("INSERT INTO schedules(id,name,interval_minutes,items_json,next_run_at) VALUES('broken','broken',5,'invalid json','2000-01-01'),('valid','valid',5,'[{\"kind\":\"tweet\",\"target\":\"1234\"}]','2000-01-01')")
        .execute(&app.store.pool).await.unwrap();
    sqlx::query(
        "INSERT INTO users(id,username,created_at) VALUES('periodic','periodic','2000-01-01')",
    )
    .execute(&app.store.pool)
    .await
    .unwrap();
    app.settings.write().await.relationship_sync_enabled = true;
    worker::schedule(&app).await.unwrap();
    let kinds: Vec<String> = sqlx::query_scalar("SELECT kind FROM tasks ORDER BY kind")
        .fetch_all(&app.store.pool)
        .await
        .unwrap();
    assert_eq!(kinds, ["relationships", "relationships", "sync", "tweet"]);
}

#[tokio::test]
async fn imports_and_partial_timelines_do_not_advance_the_sync_cursor() {
    let (_dir, app, router) = setup().await;
    app.settings.write().await.auto_download = false;
    let (_, imported) = call(
        &router,
        "POST",
        "/api/tweets",
        json!({"target":"https://x.com/archive_test/status/3333"}),
    )
    .await;
    assert_eq!(
        execute(&app, imported["id"].as_str().unwrap()).await.status,
        "succeeded"
    );
    let (_, user) = call(
        &router,
        "POST",
        "/api/users",
        json!({"target":"archive_test"}),
    )
    .await;
    let user_id = user["id"].as_str().unwrap();
    let task_id = user["task"]["id"].as_str().unwrap();
    tokio::fs::write(app.config.join("phase"), "10")
        .await
        .unwrap();
    assert_eq!(execute(&app, task_id).await.status, "queued");
    let cursor: Option<String> = sqlx::query_scalar("SELECT sync_cursor FROM users WHERE id=?")
        .bind(user_id)
        .fetch_one(&app.store.pool)
        .await
        .unwrap();
    assert!(cursor.is_none());
    tokio::fs::write(app.config.join("phase"), "9")
        .await
        .unwrap();
    assert_eq!(execute(&app, task_id).await.status, "succeeded");
    let args: Vec<String> = serde_json::from_slice(
        &tokio::fs::read(app.config.join("timeline-args.json"))
            .await
            .unwrap(),
    )
    .unwrap();
    assert!(!args.iter().any(|a| a == "--date-after"));
    let historical: i64 =
        sqlx::query_scalar("SELECT COUNT(*) FROM tweets WHERE id='1111' AND user_id=?")
            .bind(user_id)
            .fetch_one(&app.store.pool)
            .await
            .unwrap();
    assert_eq!(historical, 1);

    let cursor = "2020-06-01T12:00:00+00:00";
    sqlx::query("UPDATE users SET sync_cursor=? WHERE id=?")
        .bind(cursor)
        .bind(user_id)
        .execute(&app.store.pool)
        .await
        .unwrap();
    let (_, imported) = call(
        &router,
        "POST",
        "/api/tweets",
        json!({"target":"https://x.com/archive_test/status/4444"}),
    )
    .await;
    assert_eq!(
        execute(&app, imported["id"].as_str().unwrap()).await.status,
        "succeeded"
    );
    let (_, sync) = call(
        &router,
        "POST",
        &format!("/api/users/{user_id}/sync"),
        json!(null),
    )
    .await;
    let sync_id = sync["id"].as_str().unwrap();
    tokio::fs::write(app.config.join("phase"), "10")
        .await
        .unwrap();
    assert_eq!(execute(&app, sync_id).await.status, "queued");
    let saved: String = sqlx::query_scalar("SELECT sync_cursor FROM users WHERE id=?")
        .bind(user_id)
        .fetch_one(&app.store.pool)
        .await
        .unwrap();
    assert_eq!(saved, cursor);
    let args: Vec<String> = serde_json::from_slice(
        &tokio::fs::read(app.config.join("timeline-args.json"))
            .await
            .unwrap(),
    )
    .unwrap();
    let after = args.iter().position(|a| a == "--date-after").unwrap();
    assert_eq!(args[after + 1], "2020-05-30T12:00:00+00:00");
    tokio::fs::write(app.config.join("phase"), "9")
        .await
        .unwrap();
    assert_eq!(execute(&app, sync_id).await.status, "succeeded");
    let advanced: String = sqlx::query_scalar("SELECT sync_cursor FROM users WHERE id=?")
        .bind(user_id)
        .fetch_one(&app.store.pool)
        .await
        .unwrap();
    assert!(advanced > saved);
}

#[tokio::test]
async fn stale_backup_cookie_failures_cannot_cool_down_replacement_credentials() {
    let (_dir, app, _) = setup().await;
    tokio::fs::remove_file(app.config.join("cookies.txt"))
        .await
        .unwrap();
    let input = |cookie: &str| sessions::CookieInput {
        name: "backup".into(),
        enabled: true,
        kinds: vec!["tweet".into()],
        cookie: cookie.into(),
    };
    let id = sessions::save(&app, None, input("auth_token=OLD; ct0=OLD"))
        .await
        .unwrap();
    let task = app
        .store
        .enqueue("tweet", "1234", "backup task")
        .await
        .unwrap();
    sessions::assign(&app, &task).await.unwrap();
    let old = tools::session_cookie_for(&app, Some(&task.id))
        .await
        .unwrap();
    sessions::save(&app, Some(&id), input("auth_token=NEW; ct0=NEW"))
        .await
        .unwrap();
    sessions::cool_down(&app, &task, "Cookie 无效", None)
        .await
        .unwrap();
    assert!(sessions::list(&app).await.unwrap()[0]["cooldown_until"].is_null());
    assert!(
        tokio::fs::read_to_string(old.0.clone())
            .await
            .unwrap()
            .contains("OLD")
    );

    sessions::assign(&app, &task).await.unwrap();
    sessions::save(&app, Some(&id), input("auth_token=LATEST; ct0=LATEST"))
        .await
        .unwrap();
    let current = tools::session_cookie_for(&app, Some(&task.id))
        .await
        .unwrap();
    assert!(
        tokio::fs::read_to_string(current.0.clone())
            .await
            .unwrap()
            .contains("LATEST")
    );
    sessions::cool_down(&app, &task, "Cookie 无效", None)
        .await
        .unwrap();
    assert!(!sessions::list(&app).await.unwrap()[0]["cooldown_until"].is_null());
}

#[tokio::test]
async fn relationship_failures_keep_the_revision_of_the_failed_member_fetch() {
    let (_dir, app, router) = setup().await;
    tokio::fs::remove_file(app.config.join("cookies.txt"))
        .await
        .unwrap();
    let input = |cookie: &str| sessions::CookieInput {
        name: "relationships".into(),
        enabled: true,
        kinds: vec!["relationships".into()],
        cookie: cookie.into(),
    };
    let id = sessions::save(&app, None, input("auth_token=OLD; ct0=OLD"))
        .await
        .unwrap();
    let (_, user) = call(
        &router,
        "POST",
        "/api/users",
        json!({"target":"archive_test"}),
    )
    .await;
    let (_, task) = call(
        &router,
        "POST",
        &format!("/api/users/{}/snapshots", user["id"].as_str().unwrap()),
        json!({"direction":"following"}),
    )
    .await;
    let task_id = task["id"].as_str().unwrap().to_string();
    tokio::fs::write(app.config.join("phase"), "11")
        .await
        .unwrap();
    let worker_app = app.clone();
    let execution = tokio::spawn(async move { execute(&worker_app, &task_id).await });
    tokio::time::timeout(std::time::Duration::from_secs(5), async {
        while !app.config.join("members-started").exists() {
            tokio::time::sleep(std::time::Duration::from_millis(10)).await;
        }
    })
    .await
    .unwrap();
    sessions::save(
        &app,
        Some(&id),
        input("auth_token=REPLACEMENT; ct0=REPLACEMENT"),
    )
    .await
    .unwrap();
    tokio::fs::write(app.config.join("members-release"), "")
        .await
        .unwrap();
    let task = execution.await.unwrap();
    assert_eq!(task.status, "queued");
    assert!(task.error.unwrap().contains("Cookie 无效"));
    assert!(sessions::list(&app).await.unwrap()[0]["cooldown_until"].is_null());
}

#[tokio::test]
async fn cookie_pool_is_private_rotates_by_kind_and_skips_cooling_sessions() {
    let (_dir, app, router) = setup().await;
    let body = json!({"name":"backup","enabled":true,"kinds":["tweet"],"cookie":"auth_token=BACKUP_SECRET; ct0=BACKUP_CSRF"});
    let (status, value) = call(&router, "POST", "/api/cookie-pool", body).await;
    assert_eq!(status, StatusCode::OK);
    let id = value["id"].as_str().unwrap();
    let (_, rows) = call(&router, "GET", "/api/cookie-pool", json!(null)).await;
    assert!(!rows.to_string().contains("BACKUP_SECRET"));
    let a = app.store.enqueue("tweet", "1", "a").await.unwrap();
    let b = app.store.enqueue("tweet", "2", "b").await.unwrap();
    sessions::assign(&app, &a).await.unwrap();
    sessions::assign(&app, &b).await.unwrap();
    let lease = tools::session_cookie_for(&app, Some(&b.id)).await.unwrap();
    assert!(
        tokio::fs::read_to_string(&lease.0)
            .await
            .unwrap()
            .contains("BACKUP_SECRET")
    );
    assert!(
        sessions::cool_down(&app, &a, "Cookie 无效", None)
            .await
            .unwrap()
    );
    sessions::assign(&app, &a).await.unwrap();
    assert!(
        String::from_utf8(sessions::content(&app, Some(&a.id)).await.unwrap())
            .unwrap()
            .contains("BACKUP_SECRET")
    );
    let sync = app.store.enqueue("sync", "not-real", "sync").await.unwrap();
    assert!(sessions::assign(&app, &sync).await.is_err());
    assert_eq!(
        call(
            &router,
            "PUT",
            &format!("/api/cookie-pool/{id}"),
            json!({"name":"renamed","enabled":false,"kinds":["tweet"]})
        )
        .await
        .0,
        StatusCode::OK
    );
    let reopened = App::new(app.data.clone(), app.config.clone(), None)
        .await
        .unwrap();
    assert_eq!(sessions::load(&reopened).await.unwrap()[0].name, "renamed");
    assert_eq!(
        call(
            &router,
            "DELETE",
            &format!("/api/cookie-pool/{id}"),
            json!(null)
        )
        .await
        .0,
        StatusCode::OK
    );
    assert_eq!(
        call(&router, "GET", "/api/cookie-pool", json!(null))
            .await
            .1,
        json!([])
    );
}

#[tokio::test]
async fn failed_queue_uses_exponential_backoff_manual_retry_and_clears_on_success() {
    let (_dir, app, router) = setup().await;
    app.settings.write().await.auto_download = false;
    tokio::fs::write(app.config.join("phase"), "7")
        .await
        .unwrap();
    let task = app
        .store
        .enqueue("tweet", "1234", "bad tweet")
        .await
        .unwrap();
    let first = execute(&app, &task.id).await;
    assert_eq!(first.status, "queued");
    let at = chrono::DateTime::parse_from_rfc3339(&first.available_at).unwrap();
    assert!((at.to_utc() - chrono::Utc::now()).num_seconds() >= 28);
    let second = execute(&app, &task.id).await;
    let at = chrono::DateTime::parse_from_rfc3339(&second.available_at).unwrap();
    assert!((at.to_utc() - chrono::Utc::now()).num_seconds() >= 58);
    assert_eq!(execute(&app, &task.id).await.status, "failed");
    let (_, rows) = call(&router, "GET", "/api/failed-tweets", json!(null)).await;
    assert_eq!(rows["total"], 1);
    let id = rows["items"][0]["id"].as_str().unwrap();
    assert_eq!(
        call(
            &router,
            "POST",
            &format!("/api/failed-tweets/{id}/retry"),
            json!(null)
        )
        .await
        .0,
        StatusCode::OK
    );
    tokio::fs::write(app.config.join("phase"), "1")
        .await
        .unwrap();
    assert_eq!(execute(&app, &task.id).await.status, "succeeded");
    assert_eq!(
        call(&router, "GET", "/api/failed-tweets", json!(null))
            .await
            .1["total"],
        0
    );
    assert_eq!(worker::retry_delay(1, false), 30);
    assert_eq!(worker::retry_delay(2, false), 60);
    assert_eq!(worker::retry_delay(3, true), 3600);
}

#[tokio::test]
async fn global_proxy_is_validated_persisted_and_credentials_are_redacted() {
    let (_dir, app, router) = setup().await;
    let (_, value) = call(&router, "GET", "/api/settings", json!(null)).await;
    let mut settings = value["settings"].clone();
    settings["proxy_url"] = json!("socks5h://proxyuser:PROXY_SECRET@127.0.0.1:7890");
    let (status, saved) = call(&router, "PUT", "/api/settings", settings).await;
    assert_eq!(status, StatusCode::OK);
    assert!(!saved.to_string().contains("PROXY_SECRET"));
    assert_eq!(
        call(&router, "PUT", "/api/settings", saved.clone()).await.0,
        StatusCode::OK
    );
    assert!(app.settings.read().await.proxy_url.contains("PROXY_SECRET"));
    app.settings.write().await.auto_download = false;
    tokio::fs::write(
        app.config.join("expected-proxy"),
        &app.settings.read().await.proxy_url,
    )
    .await
    .unwrap();
    let task = app
        .store
        .enqueue("tweet", "1234", "proxy contract")
        .await
        .unwrap();
    assert_eq!(execute(&app, &task.id).await.status, "succeeded");
    let mut changed = saved.clone();
    changed["proxy_url"] = json!("socks5h://********@evil.test:7890");
    assert_eq!(
        call(&router, "PUT", "/api/settings", changed).await.0,
        StatusCode::BAD_REQUEST
    );
    let mut bad = saved;
    bad["proxy_url"] = json!("file:///etc/passwd");
    assert_eq!(
        call(&router, "PUT", "/api/settings", bad).await.0,
        StatusCode::BAD_REQUEST
    );
    let reopened = App::new(app.data.clone(), app.config.clone(), None)
        .await
        .unwrap();
    assert!(
        reopened
            .settings
            .read()
            .await
            .proxy_url
            .contains("PROXY_SECRET")
    );
}

#[tokio::test]
async fn cooling_pool_waits_without_consuming_attempts_and_updated_cookie_recovers() {
    let (_dir, app, router) = setup().await;
    let original_revision = app.cookie.read().await.revision.clone();
    let task = app.store.enqueue("tweet", "1234", "cooling").await.unwrap();
    sessions::assign(&app, &task).await.unwrap();
    sessions::cool_down(&app, &task, "频率限制", None)
        .await
        .unwrap();
    for _ in 0..4 {
        let waiting = execute(&app, &task.id).await;
        assert_eq!(waiting.status, "queued");
        assert_eq!(waiting.attempts, 0);
    }
    assert_eq!(
        call(
            &router,
            "POST",
            "/api/cookies",
            json!({"cookie":"auth_token=UPDATED; ct0=NEW_CSRF"})
        )
        .await
        .0,
        StatusCode::OK
    );
    assert!(
        !sessions::cool_down(&app, &task, "Cookie 无效", Some(&original_revision))
            .await
            .unwrap()
    );
    assert_eq!(execute(&app, &task.id).await.status, "succeeded");
    tokio::fs::remove_file(app.config.join("cookies.txt"))
        .await
        .unwrap();
    sessions::save(
        &app,
        None,
        sessions::CookieInput {
            name: "lists only".into(),
            enabled: true,
            kinds: vec!["list".into()],
            cookie: "auth_token=LIST_ONLY; ct0=CSRF".into(),
        },
    )
    .await
    .unwrap();
    let incompatible = app
        .store
        .enqueue("tweet", "5678", "needs tweet cookie")
        .await
        .unwrap();
    let waiting = execute(&app, &incompatible.id).await;
    assert_eq!(waiting.status, "queued");
    assert_eq!(waiting.attempts, 0);
}
