use x_archive::{
    api,
    app::App,
    store::{ExtractedMedia, ExtractedTweet, Store},
};

#[tokio::test]
async fn application_stores_database_with_config_and_reports_its_size() {
    let directory = tempfile::tempdir().unwrap();
    let data = directory.path().join("data");
    let config = directory.path().join("config");
    let app = App::new(data.clone(), config.clone(), None).await.unwrap();
    assert!(config.join("archive.sqlite").is_file());
    assert!(config.join("settings.json").is_file());
    assert!(!data.join("archive.sqlite").exists());
    assert_eq!(std::fs::read_dir(&data).unwrap().count(), 0);
    let expected = tokio::fs::metadata(config.join("archive.sqlite"))
        .await
        .unwrap()
        .len();
    assert!(expected > 0);
    assert_eq!(api::storage(&app).await["database_bytes"], expected);
    let task = app.store.enqueue("tweet", "123", "saved").await.unwrap();
    app.store.pool.close().await;
    let reopened = App::new(data, config, None).await.unwrap();
    assert_eq!(
        reopened
            .store
            .enqueue("tweet", "123", "saved")
            .await
            .unwrap()
            .id,
        task.id
    );
}

#[tokio::test]
async fn application_migrates_legacy_database_including_uncheckpointed_wal() {
    let directory = tempfile::tempdir().unwrap();
    let data = directory.path().join("data");
    let config = directory.path().join("config");
    tokio::fs::create_dir_all(data.join("images"))
        .await
        .unwrap();
    tokio::fs::create_dir_all(&config).await.unwrap();
    tokio::fs::write(data.join("images/original.jpg"), b"original media")
        .await
        .unwrap();
    tokio::fs::write(config.join("settings.json"), br#"{"auto_download":false}"#)
        .await
        .unwrap();
    tokio::fs::write(config.join("cookies.txt"), b"saved session")
        .await
        .unwrap();

    // Copy an idle WAL database before closing it to simulate an interrupted process.
    let original = directory.path().join("original.sqlite");
    let store = Store::open(&original).await.unwrap();
    let task = store.enqueue("tweet", "123", "legacy").await.unwrap();
    for (source, target) in [
        (original.clone(), data.join("archive.sqlite")),
        (
            directory.path().join("original.sqlite-wal"),
            data.join("archive.sqlite-wal"),
        ),
    ] {
        tokio::fs::copy(source, target).await.unwrap();
    }
    store.pool.close().await;
    assert!(
        tokio::fs::metadata(data.join("archive.sqlite-wal"))
            .await
            .unwrap()
            .len()
            > 0
    );

    let app = App::new(data.clone(), config.clone(), None).await.unwrap();
    assert_eq!(
        app.store
            .enqueue("tweet", "123", "legacy")
            .await
            .unwrap()
            .id,
        task.id
    );
    assert!(!app.settings.read().await.auto_download);
    assert_eq!(
        tokio::fs::read(config.join("cookies.txt")).await.unwrap(),
        b"saved session"
    );
    assert_eq!(
        tokio::fs::read(data.join("images/original.jpg"))
            .await
            .unwrap(),
        b"original media"
    );
    for name in [
        "archive.sqlite",
        "archive.sqlite-wal",
        "archive.sqlite-shm",
        ".instance.lock",
    ] {
        assert!(!data.join(name).exists(), "legacy file remains: {name}");
    }
    app.store.pool.close().await;
    let reopened = App::new(data, config, None).await.unwrap();
    assert_eq!(
        reopened
            .store
            .enqueue("tweet", "123", "legacy")
            .await
            .unwrap()
            .id,
        task.id
    );
}

#[tokio::test]
async fn legacy_migration_refuses_to_overwrite_an_existing_config_database() {
    let directory = tempfile::tempdir().unwrap();
    let data = directory.path().join("data");
    let config = directory.path().join("config");
    for path in [&data, &config] {
        tokio::fs::create_dir_all(path).await.unwrap();
        tokio::fs::write(
            path.join("archive.sqlite"),
            path.to_string_lossy().as_bytes(),
        )
        .await
        .unwrap();
    }
    let error = App::new(data.clone(), config.clone(), None)
        .await
        .err()
        .unwrap();
    assert!(error.to_string().contains("均存在 archive.sqlite"));
    for path in [&data, &config] {
        assert_eq!(
            tokio::fs::read(path.join("archive.sqlite")).await.unwrap(),
            path.to_string_lossy().as_bytes()
        );
    }
}

#[tokio::test]
async fn legacy_migration_requires_the_old_instance_to_stop() {
    let directory = tempfile::tempdir().unwrap();
    let data = directory.path().join("data");
    let config = directory.path().join("config");
    tokio::fs::create_dir_all(&data).await.unwrap();
    let store = Store::open(&data.join("archive.sqlite")).await.unwrap();
    store.pool.close().await;
    let lock = std::fs::File::create(data.join(".instance.lock")).unwrap();
    fs2::FileExt::try_lock_exclusive(&lock).unwrap();
    let error = App::new(data.clone(), config.clone(), None)
        .await
        .err()
        .unwrap();
    assert!(error.to_string().contains("先停止"));
    assert!(data.join("archive.sqlite").exists());
    assert!(!config.join("archive.sqlite").exists());
}

#[tokio::test]
async fn startup_locks_the_config_directory_before_opening_the_database() {
    let directory = tempfile::tempdir().unwrap();
    let config = directory.path().join("config");
    let data = directory.path().join("data");
    std::fs::create_dir_all(&config).unwrap();
    let lock = std::fs::File::create(config.join(".instance.lock")).unwrap();
    fs2::FileExt::try_lock_exclusive(&lock).unwrap();
    let output = tokio::time::timeout(
        std::time::Duration::from_secs(5),
        tokio::process::Command::new(env!("CARGO_BIN_EXE_x-archive"))
            .args(["--bind", "127.0.0.1:0"])
            .arg("--config-dir")
            .arg(&config)
            .arg("--data-dir")
            .arg(&data)
            .kill_on_drop(true)
            .output(),
    )
    .await
    .unwrap()
    .unwrap();
    assert!(!output.status.success());
    assert!(!config.join("archive.sqlite").exists());
    assert!(!data.exists());
}

#[tokio::test]
async fn duplicate_requests_share_one_durable_task_and_only_one_worker_claims_it() {
    let store = Store::memory().await.unwrap();
    let (a, b) = tokio::join!(
        store.enqueue("tweet", "123", "test"),
        store.enqueue("tweet", "123", "test")
    );
    let a = a.unwrap();
    assert_eq!(a.id, b.unwrap().id);
    let (first, second) = tokio::join!(store.claim(&a.id), store.claim(&a.id));
    assert_eq!(first.unwrap() as u8 + second.unwrap() as u8, 1);
    store.recover().await.unwrap();
    let status: String = sqlx::query_scalar("SELECT status FROM tasks WHERE id=?")
        .bind(&a.id)
        .fetch_one(&store.pool)
        .await
        .unwrap();
    assert_eq!(status, "queued");
}

#[tokio::test]
async fn enqueue_returns_the_accepted_task_while_a_worker_completes_tasks() {
    let directory = tempfile::tempdir().unwrap();
    let store = Store::open(&directory.path().join("archive.sqlite"))
        .await
        .unwrap();
    let pool = store.pool.clone();
    let worker = tokio::spawn(async move {
        loop {
            sqlx::query("UPDATE tasks SET status='succeeded' WHERE status='queued'")
                .execute(&pool)
                .await
                .unwrap();
            tokio::task::yield_now().await;
        }
    });
    for index in 0..100 {
        let task = store.enqueue("tweet", &index.to_string(), "test").await;
        if task.is_err() {
            worker.abort();
        }
        assert_eq!(task.unwrap().target, index.to_string());
    }
    worker.abort();
    let _ = worker.await;
}

#[tokio::test]
async fn concurrent_redownloads_share_a_task_and_preserve_the_archived_file() {
    let directory = tempfile::tempdir().unwrap();
    let store = Store::open(&directory.path().join("archive.sqlite"))
        .await
        .unwrap();
    sqlx::query("INSERT INTO tweets(id,author,published_at,url) VALUES('123','jack','2026-11-10T00:00:00Z','https://x.com/jack/status/123')")
        .execute(&store.pool).await.unwrap();
    sqlx::query("INSERT INTO media(id,tweet_id,ordinal,kind,extension,remote_url,status,file_path,favorite) VALUES('media','123',1,'image','jpg','https://pbs.twimg.com/media/test.jpg','ready','images/original.jpg',1)")
        .execute(&store.pool).await.unwrap();
    let (a, b) = tokio::join!(
        store.queue_download("media", "test"),
        store.queue_download("media", "test")
    );
    let task = a.unwrap();
    assert_eq!(task.id, b.unwrap().id);
    assert!(store.claim(&task.id).await.unwrap());
    sqlx::query("UPDATE media SET status='downloading' WHERE id='media'")
        .execute(&store.pool)
        .await
        .unwrap();
    assert_eq!(
        store.queue_download("media", "test").await.unwrap().id,
        task.id
    );
    let media: (String, String, bool) =
        sqlx::query_as("SELECT status,file_path,favorite FROM media WHERE id='media'")
            .fetch_one(&store.pool)
            .await
            .unwrap();
    assert_eq!(
        media,
        ("downloading".into(), "images/original.jpg".into(), true)
    );
}

#[tokio::test]
async fn reopening_database_recovers_running_and_cancelled_media_without_losing_ready_files() {
    let directory = tempfile::tempdir().unwrap();
    let path = directory.path().join("archive.sqlite");
    let store = Store::open(&path).await.unwrap();
    sqlx::query("INSERT INTO tweets(id,author,published_at,url) VALUES('123','jack','2026-11-10T00:00:00Z','https://x.com/jack/status/123')")
        .execute(&store.pool).await.unwrap();
    for (index, status) in ["running", "cancelling", "succeeded"]
        .into_iter()
        .enumerate()
    {
        sqlx::query("INSERT INTO media(id,tweet_id,ordinal,kind,extension,remote_url,status,file_path,favorite) VALUES(?,'123',?,'image','jpg','https://pbs.twimg.com/media/test.jpg',?,'images/original.jpg',1)")
            .bind(status).bind(index as i64).bind(if status == "succeeded" { "ready" } else { "downloading" }).execute(&store.pool).await.unwrap();
        sqlx::query(
            "INSERT INTO tasks(id,kind,target,label,status) VALUES(?,'download',?,'test',?)",
        )
        .bind(status)
        .bind(status)
        .bind(status)
        .execute(&store.pool)
        .await
        .unwrap();
    }
    store.pool.close().await;
    let reopened = Store::open(&path).await.unwrap();
    reopened.recover().await.unwrap();
    for (id, task_status, media_status) in [
        ("running", "queued", "pending"),
        ("cancelling", "cancelled", "failed"),
        ("succeeded", "succeeded", "ready"),
    ] {
        let row: (String, String, String, bool) = sqlx::query_as("SELECT t.status,m.status,m.file_path,m.favorite FROM tasks t JOIN media m ON m.id=t.target WHERE t.id=?")
            .bind(id).fetch_one(&reopened.pool).await.unwrap();
        assert_eq!(
            row,
            (
                task_status.into(),
                media_status.into(),
                "images/original.jpg".into(),
                true
            )
        );
    }
}

#[tokio::test]
async fn repeated_sync_does_not_duplicate_tweets_media_or_reset_favorites() {
    let store = Store::memory().await.unwrap();
    let tweets = vec![ExtractedTweet {
        id: "123".into(),
        author: "jack".into(),
        author_name: "Jack".into(),
        text: "hello".into(),
        published_at: "2026-11-10T00:00:00Z".into(),
        media: vec![ExtractedMedia {
            ordinal: 1,
            kind: "image".into(),
            extension: "jpg".into(),
            url: "https://pbs.twimg.com/media/test.jpg".into(),
        }],
    }];
    assert_eq!(store.ingest(&tweets).await.unwrap(), 1);
    sqlx::query("UPDATE media SET favorite=1,status='ready',file_path='images/test.jpg'")
        .execute(&store.pool)
        .await
        .unwrap();
    assert_eq!(store.ingest(&tweets).await.unwrap(), 0);
    let counts: (i64, i64, i64) = sqlx::query_as("SELECT (SELECT COUNT(*) FROM tweets), (SELECT COUNT(*) FROM media), (SELECT favorite FROM media LIMIT 1)").fetch_one(&store.pool).await.unwrap();
    assert_eq!(counts, (1, 1, 1));
}
