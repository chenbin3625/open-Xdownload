use x_archive::store::{ExtractedMedia, ExtractedTweet, Store};

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
