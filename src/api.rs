use crate::{
    app::{App, CookieStatus},
    automation::{self, ArchiveTarget, Schedule},
    config::{Settings, private_write, safe_path},
    domain, sessions,
    store::Task,
    tools,
};
use axum::{
    Json, Router,
    body::Body,
    extract::{DefaultBodyLimit, Path, Query, State, rejection::JsonRejection},
    http::{HeaderMap, StatusCode, header},
    middleware::{self, Next},
    response::{IntoResponse, Response},
    routing::{get, patch, post},
};
use serde::{Deserialize, Serialize};
use serde_json::{Value, json};
use sha2::{Digest, Sha256};
use sqlx::{QueryBuilder, Row, Sqlite};
use subtle::ConstantTimeEq;
use tower::ServiceExt;
use tower_http::services::ServeFile;

pub struct ApiError(pub StatusCode, pub String);
impl IntoResponse for ApiError {
    fn into_response(self) -> Response {
        (self.0, Json(json!({"error": self.1}))).into_response()
    }
}
impl From<anyhow::Error> for ApiError {
    fn from(value: anyhow::Error) -> Self {
        Self(StatusCode::BAD_REQUEST, value.to_string())
    }
}
impl From<sqlx::Error> for ApiError {
    fn from(value: sqlx::Error) -> Self {
        tracing::error!(error=%value, "database operation failed");
        Self(
            StatusCode::INTERNAL_SERVER_ERROR,
            "数据库操作失败，请检查存储空间和服务日志".into(),
        )
    }
}
type Result<T> = std::result::Result<T, ApiError>;
fn bad(message: &str) -> ApiError {
    ApiError(StatusCode::BAD_REQUEST, message.into())
}
fn missing() -> ApiError {
    ApiError(StatusCode::NOT_FOUND, "记录不存在".into())
}
fn same_secret(a: &str, b: &str) -> bool {
    Sha256::digest(a.as_bytes())
        .ct_eq(&Sha256::digest(b.as_bytes()))
        .into()
}
fn authenticated(app: &App, headers: &HeaderMap) -> bool {
    if app.password.is_none() {
        return true;
    }
    headers
        .get(header::COOKIE)
        .and_then(|v| v.to_str().ok())
        .is_some_and(|v| {
            v.split(';').any(|s| {
                s.trim()
                    .strip_prefix("x_archive_session=")
                    .is_some_and(|v| same_secret(v, &app.session_token))
            })
        })
}

async fn guard(State(app): State<App>, request: axum::extract::Request, next: Next) -> Response {
    if app.password.is_none() {
        let host = request
            .headers()
            .get(header::HOST)
            .and_then(|v| v.to_str().ok())
            .and_then(|v| v.parse::<axum::http::uri::Authority>().ok());
        if !host.is_some_and(|h| matches!(h.host(), "localhost" | "127.0.0.1" | "[::1]")) {
            return ApiError(
                StatusCode::FORBIDDEN,
                "本机模式仅接受 localhost 或回环地址".into(),
            )
            .into_response();
        }
    }
    let mutating = !matches!(
        *request.method(),
        axum::http::Method::GET | axum::http::Method::HEAD | axum::http::Method::OPTIONS
    );
    if mutating {
        if request
            .headers()
            .get("sec-fetch-site")
            .is_some_and(|v| v == "cross-site")
        {
            return ApiError(StatusCode::FORBIDDEN, "请求来源不被允许".into()).into_response();
        }
        if let Some(origin) = request.headers().get(header::ORIGIN) {
            let origin = origin.to_str().ok().and_then(|v| url::Url::parse(v).ok());
            let host = request
                .headers()
                .get(header::HOST)
                .and_then(|v| v.to_str().ok());
            let authority = origin.as_ref().map(|u| match u.port() {
                Some(port) => format!("{}:{port}", u.host_str().unwrap_or("")),
                None => u.host_str().unwrap_or("").to_string(),
            });
            if authority.as_deref() != host {
                return ApiError(StatusCode::FORBIDDEN, "请求来源不被允许".into()).into_response();
            }
        }
    }
    if request.uri().path().starts_with("/api/")
        && !["/api/health", "/api/session"].contains(&request.uri().path())
        && !authenticated(&app, request.headers())
    {
        return ApiError(StatusCode::UNAUTHORIZED, "请先登录 X Archive".into()).into_response();
    }
    let mut response = next.run(request).await;
    response
        .headers_mut()
        .insert("x-content-type-options", "nosniff".parse().unwrap());
    response
        .headers_mut()
        .insert("referrer-policy", "no-referrer".parse().unwrap());
    response
        .headers_mut()
        .insert("x-frame-options", "DENY".parse().unwrap());
    if response
        .headers()
        .get(header::CONTENT_TYPE)
        .is_some_and(|v| v.as_bytes().starts_with(b"application/json"))
    {
        response
            .headers_mut()
            .insert(header::CACHE_CONTROL, "no-store".parse().unwrap());
    }
    response
}

pub fn router(app: App) -> Router {
    Router::new()
        .route(
            "/api/health",
            get(|| async { Json(json!({"status":"ok", "version":env!("CARGO_PKG_VERSION")})) }),
        )
        .route("/api/session", get(session).post(login).delete(logout))
        .route("/api/dashboard", get(dashboard))
        .route("/api/settings", get(settings).put(save_settings))
        .route(
            "/api/cookies",
            get(cookies).post(save_cookies).delete(delete_cookies),
        )
        .route("/api/cookies/check", post(check_cookies))
        .route("/api/cookie-pool", get(cookie_pool).post(add_pool_cookie))
        .route(
            "/api/cookie-pool/{id}",
            axum::routing::put(update_pool_cookie).delete(delete_pool_cookie),
        )
        .route("/api/archives/batch", post(batch_archive))
        .route("/api/schedules", get(list_schedules).post(create_schedule))
        .route(
            "/api/schedules/{id}",
            axum::routing::put(edit_schedule)
                .patch(toggle_schedule)
                .delete(delete_schedule),
        )
        .route("/api/schedules/{id}/run", post(run_schedule))
        .route("/api/failed-tweets", get(failed_tweets))
        .route("/api/failed-tweets/{id}/retry", post(retry_failed_tweet))
        .route(
            "/api/failed-tweets/{id}",
            axum::routing::delete(dismiss_failed_tweet),
        )
        .route("/api/users", get(users).post(add_user))
        .route(
            "/api/users/{id}",
            get(user).patch(update_user).delete(delete_user),
        )
        .route("/api/users/{id}/sync", post(sync_user))
        .route(
            "/api/users/{id}/snapshots",
            get(snapshots).post(create_snapshot),
        )
        .route("/api/snapshots/{id}", get(snapshot_members))
        .route("/api/snapshots/{id}/diff", get(snapshot_diff))
        .route("/api/tweets", get(tweets).post(import_tweet))
        .route("/api/media", get(media))
        .route("/api/media/{id}", patch(update_media))
        .route("/api/media/{id}/download", post(redownload))
        .route("/api/media/{id}/file", get(media_file))
        .route("/api/media/{id}/thumbnail", get(thumbnail))
        .route("/api/tags", get(tags))
        .route("/api/tasks", get(tasks))
        .route("/api/tasks/{id}/retry", post(retry_task))
        .route("/api/tasks/{id}/cancel", post(cancel_task))
        .fallback(static_file)
        .layer(DefaultBodyLimit::max(1024 * 1024 + 8192))
        .layer(middleware::from_fn_with_state(app.clone(), guard))
        .with_state(app)
}

async fn session(State(app): State<App>, headers: HeaderMap) -> Json<Value> {
    Json(json!({"authenticated":authenticated(&app,&headers),"required":app.password.is_some()}))
}
#[derive(Deserialize)]
struct Login {
    password: String,
}
async fn login(State(app): State<App>, Json(input): Json<Login>) -> Result<Response> {
    if app
        .password
        .as_ref()
        .is_some_and(|p| !same_secret(p, &input.password))
    {
        tokio::time::sleep(std::time::Duration::from_millis(700)).await;
        return Err(ApiError(StatusCode::UNAUTHORIZED, "访问密码不正确".into()));
    }
    Ok((
        [(
            header::SET_COOKIE,
            format!(
                "x_archive_session={}; HttpOnly; SameSite=Strict; Path=/; Max-Age=86400",
                app.session_token
            ),
        )],
        Json(json!({"ok":true})),
    )
        .into_response())
}
async fn logout() -> Response {
    (
        [(
            header::SET_COOKIE,
            "x_archive_session=; HttpOnly; SameSite=Strict; Path=/; Max-Age=0",
        )],
        Json(json!({"ok":true})),
    )
        .into_response()
}

pub async fn storage(app: &App) -> Value {
    let used: i64 = sqlx::query_scalar("SELECT COALESCE(SUM(size),0) FROM media")
        .fetch_one(&app.store.pool)
        .await
        .unwrap_or(0);
    json!({"media_bytes":used,"database_bytes":tokio::fs::metadata(app.data.join("archive.sqlite")).await.map(|m|m.len()).unwrap_or(0),"free_bytes":fs2::available_space(&app.data).unwrap_or(0),"total_bytes":fs2::total_space(&app.data).unwrap_or(0)})
}
async fn dashboard(State(app): State<App>) -> Result<Json<Value>> {
    let row = sqlx::query(&format!("{TASK_GROUPS} SELECT (SELECT COUNT(*) FROM users) users,(SELECT COUNT(*) FROM tweets) tweets,(SELECT COUNT(*) FROM media WHERE status='ready') media,(SELECT COUNT(*) FROM task_groups WHERE group_status IN ('queued','running','cancelling')) active_tasks,(SELECT COUNT(*) FROM task_groups WHERE group_status='failed') failed_tasks")).fetch_one(&app.store.pool).await?;
    Ok(Json(
        json!({"users":row.get::<i64,_>("users"),"tweets":row.get::<i64,_>("tweets"),"media":row.get::<i64,_>("media"),"active_tasks":row.get::<i64,_>("active_tasks"),"failed_tasks":row.get::<i64,_>("failed_tasks"),"storage":storage(&app).await,"cookie":app.cookie.read().await.clone()}),
    ))
}
async fn settings(State(app): State<App>) -> Json<Value> {
    Json(
        json!({"settings":app.settings.read().await.redacted(),"cookie":app.cookie.read().await.clone(),"tools":tools::versions(&app).await,"storage":storage(&app).await}),
    )
}
async fn save_settings(
    State(app): State<App>,
    Json(mut value): Json<Settings>,
) -> Result<Json<Settings>> {
    let _guard = app.config_gate.lock().await;
    if let Ok(mut proxy) = url::Url::parse(&value.proxy_url)
        && proxy.username() == "********"
    {
        let stored = app.settings.read().await.proxy_url.clone();
        let old = url::Url::parse(&stored).map_err(|_| bad("请重新填写代理认证信息"))?;
        if proxy.scheme() != old.scheme()
            || proxy.host_str() != old.host_str()
            || proxy.port_or_known_default() != old.port_or_known_default()
        {
            return Err(bad("代理地址已变化，请重新填写代理认证信息"));
        }
        proxy
            .set_username(old.username())
            .map_err(|_| bad("代理地址无效"))?;
        proxy
            .set_password(old.password())
            .map_err(|_| bad("代理地址无效"))?;
        value.proxy_url = proxy.to_string();
    }
    value.validate()?;
    for dir in [&value.image_path, &value.video_path] {
        let path = safe_path(&app.data, dir).await?;
        tokio::fs::create_dir_all(path)
            .await
            .map_err(anyhow::Error::from)?;
    }
    private_write(
        &app.config.join("settings.json"),
        &serde_json::to_vec_pretty(&value).map_err(anyhow::Error::from)?,
    )
    .await?;
    *app.settings.write().await = value.clone();
    Ok(Json(value.redacted()))
}
async fn cookies(State(app): State<App>) -> Json<CookieStatus> {
    Json(app.cookie.read().await.clone())
}
#[derive(Deserialize)]
struct CookieInput {
    cookie: String,
}
async fn save_cookies(
    State(app): State<App>,
    input: std::result::Result<Json<CookieInput>, JsonRejection>,
) -> Result<Json<Value>> {
    let Json(input) = input.map_err(|_| bad("请输入有效的 Cookie 内容"))?;
    let content = domain::cookies_from_header(&input.cookie)?;
    let _guard = app.config_gate.lock().await;
    private_write(&app.config.join("cookies.txt"), content.as_bytes()).await?;
    let revision = uuid::Uuid::new_v4().to_string();
    *app.cookie.write().await = CookieStatus {
        status: "unknown".into(),
        reason: "已保存，等待检测当前会话".into(),
        checked_at: None,
        revision: revision.clone(),
    };
    app.rotation.lock().await.cooldowns.remove("primary");
    let task = app
        .store
        .enqueue("cookie_check", &revision, "检测 Cookie")
        .await?;
    Ok(Json(
        json!({"cookie":app.cookie.read().await.clone(),"task":task}),
    ))
}
async fn delete_cookies(State(app): State<App>) -> Result<Json<Value>> {
    let _guard = app.config_gate.lock().await;
    if app.config.join("cookies.txt").exists() {
        tokio::fs::remove_file(app.config.join("cookies.txt"))
            .await
            .map_err(anyhow::Error::from)?;
    }
    *app.cookie.write().await = CookieStatus::default();
    app.rotation.lock().await.cooldowns.remove("primary");
    if sessions::available(&app).await {
        return Ok(Json(json!({"ok":true})));
    }
    let mut tx = app.store.pool.begin_with("BEGIN IMMEDIATE").await?;
    sqlx::query("UPDATE media SET status='failed',error='Cookie 已删除，下载已取消' WHERE status!='ready' AND id IN (SELECT target FROM tasks WHERE kind='download' AND status='queued')").execute(&mut *tx).await?;
    sqlx::query("UPDATE tasks SET status=CASE WHEN status='running' THEN 'cancelling' ELSE 'cancelled' END,error='Cookie 已删除',updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE status IN ('queued','running')").execute(&mut *tx).await?;
    tx.commit().await?;
    Ok(Json(json!({"ok":true})))
}
async fn require_cookie(app: &App) -> Result<()> {
    if !sessions::available(app).await {
        return Err(bad("请先在设置中填写 X 登录 Cookie"));
    }
    Ok(())
}
async fn check_cookies(State(app): State<App>) -> Result<Json<Task>> {
    if !app.config.join("cookies.txt").exists() {
        return Err(bad("请先设置主 Cookie"));
    }
    Ok(Json(
        app.store
            .enqueue(
                "cookie_check",
                &app.cookie.read().await.revision,
                "检测 Cookie",
            )
            .await?,
    ))
}

async fn cookie_pool(State(app): State<App>) -> Result<Json<Value>> {
    Ok(Json(sessions::list(&app).await?))
}
async fn add_pool_cookie(
    State(app): State<App>,
    Json(input): Json<sessions::CookieInput>,
) -> Result<Json<Value>> {
    Ok(Json(
        json!({"id": sessions::save(&app, None, input).await?}),
    ))
}
async fn update_pool_cookie(
    State(app): State<App>,
    Path(id): Path<String>,
    Json(input): Json<sessions::CookieInput>,
) -> Result<Json<Value>> {
    Ok(Json(
        json!({"id": sessions::save(&app, Some(&id), input).await?}),
    ))
}
async fn delete_pool_cookie(State(app): State<App>, Path(id): Path<String>) -> Result<Json<Value>> {
    sessions::delete(&app, &id).await?;
    Ok(Json(json!({"ok": true})))
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct BatchInput {
    items: Vec<ArchiveTarget>,
}
async fn batch_archive(
    State(app): State<App>,
    Json(input): Json<BatchInput>,
) -> Result<Json<Value>> {
    require_cookie(&app).await?;
    let tasks = automation::enqueue(&app.store, &input.items).await?;
    Ok(Json(
        json!({"tasks":tasks,"count":tasks.len(),"duplicates":input.items.len()-tasks.len()}),
    ))
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct ScheduleInput {
    name: String,
    enabled: bool,
    interval_minutes: i64,
    items: Vec<ArchiveTarget>,
}
impl ScheduleInput {
    fn validate(&self) -> Result<Vec<ArchiveTarget>> {
        if self.name.trim().is_empty() || self.name.len() > 100 {
            return Err(bad("计划名称应为 1 至 100 个字符"));
        }
        if !(5..=43200).contains(&self.interval_minutes) {
            return Err(bad("计划间隔应在 5 至 43200 分钟之间"));
        }
        Ok(automation::normalize(&self.items)?)
    }
}
async fn list_schedules(State(app): State<App>) -> Result<Json<Value>> {
    let rows: Vec<Schedule> =
        sqlx::query_as("SELECT * FROM schedules ORDER BY created_at DESC,id DESC")
            .fetch_all(&app.store.pool)
            .await?;
    Ok(Json(Value::Array(
        rows.iter()
            .map(Schedule::value)
            .collect::<anyhow::Result<_>>()?,
    )))
}
async fn create_schedule(
    State(app): State<App>,
    Json(input): Json<ScheduleInput>,
) -> Result<Json<Value>> {
    let items = input.validate()?;
    let id = uuid::Uuid::new_v4().to_string();
    let next = (chrono::Utc::now() + chrono::Duration::minutes(input.interval_minutes))
        .to_rfc3339_opts(chrono::SecondsFormat::Millis, true);
    sqlx::query("INSERT INTO schedules(id,name,enabled,interval_minutes,items_json,next_run_at) VALUES(?,?,?,?,?,?)")
        .bind(&id).bind(input.name.trim()).bind(input.enabled).bind(input.interval_minutes)
        .bind(serde_json::to_string(&items).map_err(anyhow::Error::from)?).bind(next).execute(&app.store.pool).await?;
    Ok(Json(json!({"id":id})))
}
async fn edit_schedule(
    State(app): State<App>,
    Path(id): Path<String>,
    Json(input): Json<ScheduleInput>,
) -> Result<Json<Value>> {
    let items = input.validate()?;
    let result = sqlx::query("UPDATE schedules SET name=?,enabled=?,interval_minutes=?,items_json=?,next_run_at=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?")
        .bind(input.name.trim()).bind(input.enabled).bind(input.interval_minutes)
        .bind(serde_json::to_string(&items).map_err(anyhow::Error::from)?)
        .bind((chrono::Utc::now() + chrono::Duration::minutes(input.interval_minutes)).to_rfc3339_opts(chrono::SecondsFormat::Millis, true))
        .bind(id).execute(&app.store.pool).await?;
    if result.rows_affected() == 0 {
        return Err(missing());
    }
    Ok(Json(json!({"ok":true})))
}
#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct ScheduleToggle {
    enabled: bool,
}
async fn toggle_schedule(
    State(app): State<App>,
    Path(id): Path<String>,
    Json(input): Json<ScheduleToggle>,
) -> Result<Json<Value>> {
    let result = sqlx::query("UPDATE schedules SET enabled=?,next_run_at=CASE WHEN ?=1 THEN strftime('%Y-%m-%dT%H:%M:%fZ','now','+'||interval_minutes||' minutes') ELSE next_run_at END,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?")
        .bind(input.enabled).bind(input.enabled).bind(id).execute(&app.store.pool).await?;
    if result.rows_affected() == 0 {
        return Err(missing());
    }
    Ok(Json(json!({"ok":true})))
}
async fn delete_schedule(State(app): State<App>, Path(id): Path<String>) -> Result<Json<Value>> {
    let result = sqlx::query("DELETE FROM schedules WHERE id=?")
        .bind(id)
        .execute(&app.store.pool)
        .await?;
    if result.rows_affected() == 0 {
        return Err(missing());
    }
    Ok(Json(json!({"ok":true})))
}
async fn run_schedule(State(app): State<App>, Path(id): Path<String>) -> Result<Json<Value>> {
    require_cookie(&app).await?;
    let tasks = automation::run_schedule(&app.store, &id, false).await?;
    Ok(Json(json!({"tasks":tasks})))
}

async fn failed_tweets(State(app): State<App>, Query(f): Query<Filters>) -> Result<Json<Value>> {
    let (limit, offset) = pagination(&f);
    let total: i64 = sqlx::query_scalar("SELECT COUNT(*) FROM failed_tweets")
        .fetch_one(&app.store.pool)
        .await?;
    let rows = sqlx::query("SELECT f.*,t.label,t.status,t.attempts,t.max_attempts,t.available_at,t.cookie_id FROM failed_tweets f JOIN tasks t ON t.id=f.task_id ORDER BY f.updated_at DESC,f.id LIMIT ? OFFSET ?")
        .bind(limit).bind(offset).fetch_all(&app.store.pool).await?;
    let items: Vec<_> = rows.iter().map(|r| json!({
        "id":r.get::<String,_>("id"),"task_id":r.get::<String,_>("task_id"),"kind":r.get::<String,_>("kind"),
        "target":r.get::<String,_>("target"),"tweet_id":r.get::<String,_>("tweet_id"),"label":r.get::<String,_>("label"),
        "error":r.get::<String,_>("error"),"status":r.get::<String,_>("status"),"attempts":r.get::<i64,_>("attempts"),
        "max_attempts":r.get::<i64,_>("max_attempts"),"available_at":r.get::<String,_>("available_at"),
        "updated_at":r.get::<String,_>("updated_at")
    })).collect();
    Ok(Json(
        json!({"items":items,"total":total,"page":offset/limit+1,"limit":limit}),
    ))
}
async fn retry_failed_tweet(State(app): State<App>, Path(id): Path<String>) -> Result<Json<Value>> {
    require_cookie(&app).await?;
    let mut tx = app.store.pool.begin_with("BEGIN IMMEDIATE").await?;
    let task: Task = sqlx::query_as(
        "SELECT t.* FROM tasks t JOIN failed_tweets f ON f.task_id=t.id WHERE f.id=?",
    )
    .bind(&id)
    .fetch_optional(&mut *tx)
    .await?
    .ok_or_else(missing)?;
    if !["queued", "failed", "cancelled"].contains(&task.status.as_str()) {
        return Err(bad("任务正在运行或已完成"));
    }
    let active: bool = sqlx::query_scalar("SELECT EXISTS(SELECT 1 FROM tasks WHERE kind=? AND target=? AND id!=? AND status IN ('queued','running','cancelling'))")
        .bind(&task.kind).bind(&task.target).bind(&task.id).fetch_one(&mut *tx).await?;
    if active {
        return Err(bad("此目标已有任务正在执行"));
    }
    sqlx::query("UPDATE tasks SET status='queued',attempts=0,progress=0,error=NULL,available_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?")
        .bind(&task.id).execute(&mut *tx).await?;
    if task.kind == "download" {
        sqlx::query("UPDATE media SET status='pending',error=NULL WHERE id=? AND status!='ready'")
            .bind(&task.target)
            .execute(&mut *tx)
            .await?;
    }
    tx.commit().await?;
    Ok(Json(json!({"task_id":task.id})))
}
async fn dismiss_failed_tweet(
    State(app): State<App>,
    Path(id): Path<String>,
) -> Result<Json<Value>> {
    let result = sqlx::query("DELETE FROM failed_tweets WHERE id=? AND task_id IN (SELECT id FROM tasks WHERE status IN ('failed','cancelled','succeeded'))")
        .bind(id).execute(&app.store.pool).await?;
    if result.rows_affected() == 0 {
        return Err(bad("记录不存在或仍在重试中"));
    }
    Ok(Json(json!({"ok":true})))
}

#[derive(Serialize, sqlx::FromRow)]
pub struct User {
    pub id: String,
    pub username: String,
    pub name: String,
    pub bio: String,
    pub avatar: Option<String>,
    pub enabled: bool,
    pub sync_interval: Option<i64>,
    pub last_sync: Option<String>,
    pub last_error: Option<String>,
    pub created_at: String,
    pub tweet_count: i64,
    pub media_count: i64,
}
const USERS: &str = "SELECT u.*, (SELECT COUNT(*) FROM tweets t WHERE t.user_id=u.id) tweet_count,(SELECT COUNT(*) FROM media m JOIN tweets t ON t.id=m.tweet_id WHERE t.user_id=u.id) media_count FROM users u";
async fn users(State(app): State<App>) -> Result<Json<Vec<User>>> {
    Ok(Json(
        sqlx::query_as(&format!("{USERS} ORDER BY u.created_at DESC"))
            .fetch_all(&app.store.pool)
            .await?,
    ))
}
async fn user(State(app): State<App>, Path(id): Path<String>) -> Result<Json<User>> {
    Ok(Json(
        sqlx::query_as(&format!("{USERS} WHERE u.id=?"))
            .bind(id)
            .fetch_optional(&app.store.pool)
            .await?
            .ok_or_else(missing)?,
    ))
}
#[derive(Deserialize)]
struct Target {
    target: String,
}
async fn add_user(State(app): State<App>, Json(input): Json<Target>) -> Result<Json<Value>> {
    require_cookie(&app).await?;
    let username = domain::username(&input.target)?;
    let id = uuid::Uuid::new_v4().to_string();
    sqlx::query(
        "INSERT INTO users(id,username) VALUES(?,?) ON CONFLICT(username) DO UPDATE SET enabled=1",
    )
    .bind(id)
    .bind(&username)
    .execute(&app.store.pool)
    .await?;
    let id: String = sqlx::query_scalar("SELECT id FROM users WHERE username=?")
        .bind(&username)
        .fetch_one(&app.store.pool)
        .await?;
    sqlx::query("UPDATE tweets SET user_id=? WHERE author=? COLLATE NOCASE")
        .bind(&id)
        .bind(&username)
        .execute(&app.store.pool)
        .await?;
    let task = app
        .store
        .enqueue("sync", &id, &format!("同步 @{username}"))
        .await?;
    Ok(Json(json!({"id":id,"task":task})))
}
#[derive(Deserialize)]
struct UserUpdate {
    enabled: bool,
    sync_interval: Option<i64>,
}
async fn update_user(
    State(app): State<App>,
    Path(id): Path<String>,
    Json(input): Json<UserUpdate>,
) -> Result<Json<Value>> {
    if input
        .sync_interval
        .is_some_and(|n| !(5..=43200).contains(&n))
    {
        return Err(bad("同步间隔应在 5 至 43200 分钟之间"));
    }
    let result = sqlx::query("UPDATE users SET enabled=?,sync_interval=? WHERE id=?")
        .bind(input.enabled)
        .bind(input.sync_interval)
        .bind(&id)
        .execute(&app.store.pool)
        .await?;
    if result.rows_affected() == 0 {
        return Err(missing());
    }
    if !input.enabled {
        sqlx::query("UPDATE tasks SET status=CASE WHEN status='running' THEN 'cancelling' ELSE 'cancelled' END,error='用户归档已暂停' WHERE (target=? OR target LIKE ?) AND status IN ('queued','running') AND kind IN ('sync','relationships')").bind(&id).bind(format!("{id}:%")).execute(&app.store.pool).await?;
    }
    Ok(Json(json!({"ok":true})))
}
async fn delete_user(State(app): State<App>, Path(id): Path<String>) -> Result<Json<Value>> {
    let mut tx = app.store.pool.begin().await?;
    sqlx::query("UPDATE tasks SET status=CASE WHEN status='running' THEN 'cancelling' ELSE 'cancelled' END,error='用户已移除' WHERE (target=? OR target LIKE ?) AND status IN ('queued','running') AND kind IN ('sync','relationships')").bind(&id).bind(format!("{id}:%")).execute(&mut *tx).await?;
    let result = sqlx::query("DELETE FROM users WHERE id=?")
        .bind(&id)
        .execute(&mut *tx)
        .await?;
    if result.rows_affected() == 0 {
        return Err(missing());
    }
    tx.commit().await?;
    Ok(Json(json!({"ok":true})))
}
async fn sync_user(State(app): State<App>, Path(id): Path<String>) -> Result<Json<Task>> {
    require_cookie(&app).await?;
    let username: Option<String> =
        sqlx::query_scalar("SELECT username FROM users WHERE id=? AND enabled=1")
            .bind(&id)
            .fetch_optional(&app.store.pool)
            .await?;
    let username = username.ok_or_else(|| bad("用户不存在或已暂停，请先启用归档"))?;
    Ok(Json(
        app.store
            .enqueue("sync", &id, &format!("同步 @{username}"))
            .await?,
    ))
}
async fn import_tweet(State(app): State<App>, Json(input): Json<Target>) -> Result<Json<Task>> {
    require_cookie(&app).await?;
    let id = domain::tweet_id(&input.target)?;
    Ok(Json(
        app.store
            .enqueue("tweet", &id, &format!("归档推文 {id}"))
            .await?,
    ))
}

#[derive(Default, Deserialize)]
pub struct Filters {
    pub page: Option<u32>,
    pub limit: Option<u32>,
    pub user: Option<String>,
    pub tweet: Option<String>,
    pub author: Option<String>,
    pub kind: Option<String>,
    pub from: Option<String>,
    pub to: Option<String>,
    pub favorite: Option<bool>,
    pub tag: Option<String>,
    pub search: Option<String>,
    pub status: Option<String>,
    pub grouped: Option<bool>,
}
fn pagination(f: &Filters) -> (i64, i64) {
    let limit = f.limit.unwrap_or(48).clamp(1, 100) as i64;
    (
        limit,
        (f.page.unwrap_or(1).clamp(1, 100000) as i64 - 1) * limit,
    )
}
fn filters<'a>(q: &mut QueryBuilder<'a, Sqlite>, f: &Filters, is_media: bool) -> Result<()> {
    if let Some(user) = &f.user {
        q.push(" AND t.user_id=").push_bind(user.clone());
    }
    if let Some(tweet) = &f.tweet {
        q.push(" AND t.id=").push_bind(tweet.clone());
    }
    if let Some(author) = &f.author {
        q.push(" AND t.author=").push_bind(author.clone());
    }
    if let Some(search) = &f.search {
        q.push(" AND (t.text LIKE ")
            .push_bind(format!(
                "%{}%",
                search.chars().take(200).collect::<String>()
            ))
            .push(" OR t.author LIKE ")
            .push_bind(format!(
                "%{}%",
                search.chars().take(200).collect::<String>()
            ))
            .push(")");
    }
    for (field, op, next) in [(&f.from, ">=", false), (&f.to, "<", true)] {
        if let Some(value) = field {
            let date = chrono::NaiveDate::parse_from_str(value, "%Y-%m-%d")
                .map_err(|_| bad("日期格式应为 YYYY-MM-DD"))?;
            let date = if next {
                date.succ_opt().ok_or_else(|| bad("日期超出范围"))?
            } else {
                date
            };
            q.push(format!(" AND t.published_at {op} "))
                .push_bind(format!("{date}T00:00:00"));
        }
    }
    if is_media {
        if let Some(kind) = &f.kind {
            if !["image", "video", "gif"].contains(&kind.as_str()) {
                return Err(bad("媒体类型无效"));
            }
            q.push(" AND m.kind=").push_bind(kind.clone());
        }
        if f.favorite == Some(true) {
            q.push(" AND m.favorite=1");
        }
        if let Some(tag) = &f.tag {
            q.push(" AND EXISTS (SELECT 1 FROM media_tags mt JOIN tags g ON g.id=mt.tag_id WHERE mt.media_id=m.id AND g.name=").push_bind(tag.clone()).push(")");
        }
    }
    Ok(())
}
#[derive(Serialize, sqlx::FromRow)]
struct Tweet {
    id: String,
    user_id: Option<String>,
    author: String,
    author_name: String,
    text: String,
    published_at: String,
    url: String,
    archived_at: String,
    media_count: i64,
    ready_count: i64,
    #[sqlx(skip)]
    media: Vec<Media>,
}
async fn tweets(State(app): State<App>, Query(f): Query<Filters>) -> Result<Json<Value>> {
    let (limit, offset) = pagination(&f);
    let mut count = QueryBuilder::new("SELECT COUNT(*) FROM tweets t WHERE 1=1");
    filters(&mut count, &f, false)?;
    let total: i64 = count
        .build_query_scalar()
        .fetch_one(&app.store.pool)
        .await?;
    let mut q = QueryBuilder::new(
        "SELECT t.*,(SELECT COUNT(*) FROM media WHERE tweet_id=t.id) media_count,(SELECT COUNT(*) FROM media WHERE tweet_id=t.id AND status='ready') ready_count FROM tweets t WHERE 1=1",
    );
    filters(&mut q, &f, false)?;
    q.push(" ORDER BY t.published_at DESC,t.id DESC LIMIT ")
        .push_bind(limit)
        .push(" OFFSET ")
        .push_bind(offset);
    let mut items = q
        .build_query_as::<Tweet>()
        .fetch_all(&app.store.pool)
        .await?;
    if !items.is_empty() {
        let mut q = QueryBuilder::new(MEDIA_SELECT);
        q.push(" AND m.tweet_id IN (");
        let mut ids = q.separated(",");
        for item in &items {
            ids.push_bind(&item.id);
        }
        q.push(") ORDER BY m.tweet_id,m.ordinal");
        let media = q
            .build_query_as::<Media>()
            .fetch_all(&app.store.pool)
            .await?;
        let mut grouped = std::collections::HashMap::<String, Vec<Media>>::new();
        for item in media {
            grouped.entry(item.tweet_id.clone()).or_default().push(item);
        }
        for item in &mut items {
            item.media = grouped.remove(&item.id).unwrap_or_default();
        }
    }
    Ok(Json(
        json!({"items":items,"total":total,"page":offset/limit+1,"limit":limit}),
    ))
}
#[derive(Serialize, sqlx::FromRow)]
struct Media {
    id: String,
    tweet_id: String,
    ordinal: i64,
    kind: String,
    extension: String,
    status: String,
    error: Option<String>,
    size: i64,
    width: i64,
    height: i64,
    duration: Option<f64>,
    favorite: bool,
    author: String,
    text: String,
    published_at: String,
    url: String,
    tags: String,
    has_file: bool,
    has_thumbnail: bool,
}
const MEDIA_SELECT: &str = "SELECT m.id,m.tweet_id,m.ordinal,m.kind,m.extension,m.status,m.error,m.size,m.width,m.height,m.duration,m.favorite,t.author,t.text,t.published_at,t.url,(SELECT COALESCE(group_concat(g.name,','),'') FROM media_tags mt JOIN tags g ON mt.tag_id=g.id WHERE mt.media_id=m.id) tags,m.file_path IS NOT NULL has_file,m.thumbnail_path IS NOT NULL has_thumbnail FROM media m JOIN tweets t ON t.id=m.tweet_id WHERE 1=1";
async fn media(State(app): State<App>, Query(f): Query<Filters>) -> Result<Json<Value>> {
    let (limit, offset) = pagination(&f);
    let mut count = QueryBuilder::new(
        "SELECT COUNT(*) FROM media m JOIN tweets t ON t.id=m.tweet_id WHERE 1=1",
    );
    filters(&mut count, &f, true)?;
    let total: i64 = count
        .build_query_scalar()
        .fetch_one(&app.store.pool)
        .await?;
    let mut q = QueryBuilder::new(MEDIA_SELECT);
    filters(&mut q, &f, true)?;
    q.push(" ORDER BY t.published_at DESC,m.ordinal LIMIT ")
        .push_bind(limit)
        .push(" OFFSET ")
        .push_bind(offset);
    let items = q
        .build_query_as::<Media>()
        .fetch_all(&app.store.pool)
        .await?;
    Ok(Json(
        json!({"items":items,"total":total,"page":offset/limit+1,"limit":limit}),
    ))
}
#[derive(Deserialize)]
struct MediaUpdate {
    favorite: Option<bool>,
    tags: Option<Vec<String>>,
}
async fn update_media(
    State(app): State<App>,
    Path(id): Path<String>,
    Json(input): Json<MediaUpdate>,
) -> Result<Json<Value>> {
    let exists: i64 = sqlx::query_scalar("SELECT COUNT(*) FROM media WHERE id=?")
        .bind(&id)
        .fetch_one(&app.store.pool)
        .await?;
    if exists == 0 {
        return Err(missing());
    }
    if input.tags.as_ref().is_some_and(|tags| {
        tags.len() > 20
            || tags.iter().any(|t| {
                t.trim().is_empty() || t.chars().count() > 30 || t.contains([',', '\n', '\r'])
            })
    }) {
        return Err(bad(
            "最多 20 个标签，每个标签为 1 至 30 个字符，不能包含逗号或换行",
        ));
    }
    let mut tx = app.store.pool.begin().await?;
    if let Some(favorite) = input.favorite {
        sqlx::query("UPDATE media SET favorite=? WHERE id=?")
            .bind(favorite)
            .bind(&id)
            .execute(&mut *tx)
            .await?;
    }
    if let Some(tags) = input.tags {
        sqlx::query("DELETE FROM media_tags WHERE media_id=?")
            .bind(&id)
            .execute(&mut *tx)
            .await?;
        for tag in tags {
            let tag = tag.trim();
            sqlx::query("INSERT INTO tags(name) VALUES(?) ON CONFLICT DO NOTHING")
                .bind(tag)
                .execute(&mut *tx)
                .await?;
            sqlx::query("INSERT INTO media_tags(media_id,tag_id) SELECT ?,id FROM tags WHERE name=? ON CONFLICT DO NOTHING").bind(&id).bind(tag).execute(&mut *tx).await?;
        }
    }
    tx.commit().await?;
    Ok(Json(json!({"ok":true})))
}
async fn tags(State(app): State<App>) -> Result<Json<Vec<String>>> {
    Ok(Json(
        sqlx::query_scalar(
            "SELECT name FROM tags WHERE id IN (SELECT tag_id FROM media_tags) ORDER BY name",
        )
        .fetch_all(&app.store.pool)
        .await?,
    ))
}
async fn redownload(State(app): State<App>, Path(id): Path<String>) -> Result<Json<Task>> {
    let row: Option<(String, String)> = sqlx::query_as(
        "SELECT m.status,t.author FROM media m JOIN tweets t ON t.id=m.tweet_id WHERE m.id=?",
    )
    .bind(&id)
    .fetch_optional(&app.store.pool)
    .await?;
    let (status, author) = row.ok_or_else(missing)?;
    if status == "downloading" {
        return Err(bad("媒体正在下载，请稍后再试"));
    }
    let task = app
        .store
        .queue_download(&id, &format!("下载 @{author} 的媒体"))
        .await?;
    Ok(Json(task))
}
async fn serve_media(
    app: App,
    id: String,
    thumbnail: bool,
    request: axum::extract::Request,
) -> Result<Response> {
    let field = if thumbnail {
        "thumbnail_path"
    } else {
        "file_path"
    };
    let path: Option<Option<String>> =
        sqlx::query_scalar(&format!("SELECT {field} FROM media WHERE id=?"))
            .bind(id)
            .fetch_optional(&app.store.pool)
            .await?;
    let path = path.flatten().ok_or_else(missing)?;
    let path = safe_path(&app.data, &path).await?;
    Ok(ServeFile::new(path)
        .oneshot(request)
        .await
        .unwrap()
        .into_response())
}
async fn media_file(
    State(app): State<App>,
    Path(id): Path<String>,
    request: axum::extract::Request,
) -> Result<Response> {
    serve_media(app, id, false, request).await
}
async fn thumbnail(
    State(app): State<App>,
    Path(id): Path<String>,
    request: axum::extract::Request,
) -> Result<Response> {
    serve_media(app, id, true, request).await
}

#[derive(Serialize, sqlx::FromRow)]
struct TaskGroup {
    #[serde(flatten)]
    #[sqlx(flatten)]
    task: Task,
    download_total: i64,
    download_completed: i64,
    download_failed: i64,
    download_cancelled: i64,
    download_size: i64,
    download_error: Option<String>,
    #[serde(skip)]
    group_status: String,
    #[serde(skip)]
    group_progress: i64,
}

const TASK_GROUPS: &str = "
WITH latest_downloads AS (
    SELECT *, ROW_NUMBER() OVER (
        PARTITION BY parent_id,target ORDER BY created_at DESC,rowid DESC
    ) AS attempt_order
    FROM tasks WHERE parent_id IS NOT NULL
), downloads AS (
    SELECT parent_id, COUNT(*) AS download_total,
        SUM(status='succeeded') AS download_completed,
        SUM(status='failed') AS download_failed,
        SUM(status='cancelled') AS download_cancelled,
        SUM(status IN ('queued','running')) AS active,
        SUM(status='cancelling') AS cancelling,
        SUM(CASE WHEN status='succeeded' THEN result_size ELSE 0 END) AS download_size,
        CAST(AVG(progress) AS INTEGER) AS download_progress,
        MIN(CASE WHEN status!='succeeded' THEN error END) AS download_error
    FROM latest_downloads WHERE attempt_order=1 GROUP BY parent_id
), task_groups AS (
    SELECT t.*,
        COALESCE(d.download_total,0) AS download_total,
        COALESCE(d.download_completed,0) AS download_completed,
        COALESCE(d.download_failed,0) AS download_failed,
        COALESCE(d.download_cancelled,0) AS download_cancelled,
        COALESCE(d.download_size,t.result_size) AS download_size,
        d.download_error,
        CASE
            WHEN t.status='cancelling' OR d.cancelling>0 THEN 'cancelling'
            WHEN t.status IN ('queued','running') THEN t.status
            WHEN d.active>0 THEN 'running'
            WHEN t.status='failed' OR d.download_failed>0 THEN 'failed'
            WHEN t.status='cancelled' OR d.download_cancelled>0 THEN 'cancelled'
            ELSE t.status
        END AS group_status,
        CASE WHEN t.status='succeeded' AND d.download_total>0
            THEN d.download_progress ELSE t.progress END AS group_progress
    FROM tasks t LEFT JOIN downloads d ON d.parent_id=t.id
    WHERE t.parent_id IS NULL
)
";

async fn tasks(State(app): State<App>, Query(f): Query<Filters>) -> Result<Json<Value>> {
    let (limit, offset) = pagination(&f);
    if f.grouped == Some(true) {
        let total: i64 = sqlx::query_scalar(&format!(
            "{TASK_GROUPS} SELECT COUNT(*) FROM task_groups WHERE (? IS NULL OR group_status=?)"
        ))
        .bind(&f.status)
        .bind(&f.status)
        .fetch_one(&app.store.pool)
        .await?;
        let mut items: Vec<TaskGroup> = sqlx::query_as(&format!(
            "{TASK_GROUPS} SELECT * FROM task_groups WHERE (? IS NULL OR group_status=?) ORDER BY created_at DESC,id DESC LIMIT ? OFFSET ?"
        ))
        .bind(&f.status)
        .bind(&f.status)
        .bind(limit)
        .bind(offset)
        .fetch_all(&app.store.pool)
        .await?;
        for item in &mut items {
            item.task.status = item.group_status.clone();
            item.task.progress = item.group_progress;
        }
        return Ok(Json(
            json!({"items":items,"total":total,"page":offset/limit+1,"limit":limit}),
        ));
    }
    let total: i64 = sqlx::query_scalar("SELECT COUNT(*) FROM tasks WHERE (? IS NULL OR status=?)")
        .bind(&f.status)
        .bind(&f.status)
        .fetch_one(&app.store.pool)
        .await?;
    let items:Vec<Task>=sqlx::query_as("SELECT * FROM tasks WHERE (? IS NULL OR status=?) ORDER BY created_at DESC,id DESC LIMIT ? OFFSET ?").bind(&f.status).bind(&f.status).bind(limit).bind(offset).fetch_all(&app.store.pool).await?;
    Ok(Json(
        json!({"items":items,"total":total,"page":offset/limit+1,"limit":limit}),
    ))
}
async fn retry_task(State(app): State<App>, Path(id): Path<String>) -> Result<Json<Task>> {
    let task: Task = sqlx::query_as("SELECT * FROM tasks WHERE id=?")
        .bind(&id)
        .fetch_optional(&app.store.pool)
        .await?
        .ok_or_else(missing)?;
    if task.status == "succeeded" {
        let mut tx = app.store.pool.begin_with("BEGIN IMMEDIATE").await?;
        let children: Vec<Task> = sqlx::query_as("SELECT d.* FROM tasks d WHERE d.parent_id=? AND d.status IN ('failed','cancelled') AND NOT EXISTS (SELECT 1 FROM tasks newer WHERE newer.parent_id=d.parent_id AND newer.target=d.target AND (newer.created_at>d.created_at OR (newer.created_at=d.created_at AND newer.rowid>d.rowid)))")
            .bind(&id).fetch_all(&mut *tx).await?;
        if children.is_empty() {
            return Err(bad("只有失败或取消的任务可以重试"));
        }
        for child in children {
            let queued = sqlx::query("INSERT INTO tasks(id,kind,target,label,parent_id) SELECT ?,'download',?,?,? WHERE NOT EXISTS (SELECT 1 FROM tasks WHERE kind='download' AND target=? AND status IN ('queued','running','cancelling'))")
                .bind(uuid::Uuid::new_v4().to_string()).bind(&child.target).bind(&child.label).bind(&id).bind(&child.target)
                .execute(&mut *tx).await?;
            if queued.rows_affected() == 0 {
                return Err(bad("媒体正在下载，请稍后再重试任务"));
            }
            sqlx::query(
                "UPDATE media SET status='pending',error=NULL WHERE id=? AND status='failed'",
            )
            .bind(&child.target)
            .execute(&mut *tx)
            .await?;
        }
        tx.commit().await?;
        return Ok(Json(task));
    }
    if !["failed", "cancelled"].contains(&task.status.as_str()) {
        return Err(bad("只有失败或取消的任务可以重试"));
    }
    let new_task = if task.kind == "download" {
        app.store
            .queue_child_download(&task.target, &task.label, task.parent_id.as_deref())
            .await?
    } else if task.kind == "cookie_check" {
        require_cookie(&app).await?;
        app.store
            .enqueue(
                "cookie_check",
                &app.cookie.read().await.revision,
                &task.label,
            )
            .await?
    } else {
        app.store
            .enqueue(&task.kind, &task.target, &task.label)
            .await?
    };
    sqlx::query("UPDATE failed_tweets SET task_id=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE kind=? AND target=?")
        .bind(&new_task.id).bind(&new_task.kind).bind(&new_task.target).execute(&app.store.pool).await?;
    Ok(Json(new_task))
}
async fn cancel_task(State(app): State<App>, Path(id): Path<String>) -> Result<Json<Value>> {
    let mut tx = app.store.pool.begin_with("BEGIN IMMEDIATE").await?;
    sqlx::query("UPDATE media SET status='failed',error='用户取消了下载，可手动重试' WHERE status!='ready' AND id IN (SELECT target FROM tasks WHERE (id=? OR parent_id=?) AND kind='download' AND status='queued')").bind(&id).bind(&id).execute(&mut *tx).await?;
    let result=sqlx::query("UPDATE tasks SET status=CASE WHEN status='running' THEN 'cancelling' ELSE 'cancelled' END,error='用户取消了任务',updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE (id=? OR parent_id=?) AND status IN ('queued','running')").bind(&id).bind(&id).execute(&mut *tx).await?;
    if result.rows_affected() == 0 {
        return Err(bad("任务不存在或已结束"));
    }
    tx.commit().await?;
    Ok(Json(json!({"ok":true})))
}

#[derive(Deserialize)]
struct Direction {
    direction: Option<String>,
}
#[derive(Serialize, sqlx::FromRow)]
struct Snapshot {
    id: String,
    user_id: String,
    direction: String,
    status: String,
    expected_count: Option<i64>,
    fetched_count: i64,
    error: Option<String>,
    created_at: String,
}
fn direction(value: Option<String>) -> Result<String> {
    let d = value.unwrap_or_else(|| "following".into());
    if !["following", "followers"].contains(&d.as_str()) {
        return Err(bad("关注方向无效"));
    }
    Ok(d)
}
async fn snapshots(
    State(app): State<App>,
    Path(id): Path<String>,
    Query(input): Query<Direction>,
) -> Result<Json<Vec<Snapshot>>> {
    let d = direction(input.direction)?;
    Ok(Json(sqlx::query_as("SELECT * FROM snapshots WHERE user_id=? AND direction=? ORDER BY created_at DESC LIMIT 200").bind(id).bind(d).fetch_all(&app.store.pool).await?))
}
async fn create_snapshot(
    State(app): State<App>,
    Path(id): Path<String>,
    Json(input): Json<Direction>,
) -> Result<Json<Task>> {
    require_cookie(&app).await?;
    let d = direction(input.direction)?;
    let username: Option<String> =
        sqlx::query_scalar("SELECT username FROM users WHERE id=? AND enabled=1")
            .bind(&id)
            .fetch_optional(&app.store.pool)
            .await?;
    let username = username.ok_or_else(|| bad("用户不存在或已暂停"))?;
    Ok(Json(
        app.store
            .enqueue(
                "relationships",
                &format!("{id}:{d}"),
                &format!("@{username} {d} 快照"),
            )
            .await?,
    ))
}
async fn snapshot_members(
    State(app): State<App>,
    Path(id): Path<String>,
    Query(f): Query<Filters>,
) -> Result<Json<Value>> {
    let snapshot: Snapshot = sqlx::query_as("SELECT * FROM snapshots WHERE id=?")
        .bind(&id)
        .fetch_optional(&app.store.pool)
        .await?
        .ok_or_else(missing)?;
    let (limit, offset) = pagination(&f);
    let items:Vec<(String,String,String)>=sqlx::query_as("SELECT user_id,username,name FROM relationships WHERE snapshot_id=? ORDER BY username LIMIT ? OFFSET ?").bind(&id).bind(limit).bind(offset).fetch_all(&app.store.pool).await?;
    Ok(Json(
        json!({"snapshot":snapshot,"items":items.into_iter().map(|(id,username,name)|json!({"id":id,"username":username,"name":name})).collect::<Vec<_>>(),"total":snapshot.fetched_count,"page":offset/limit+1,"limit":limit}),
    ))
}
#[derive(Deserialize)]
struct DiffQuery {
    base: String,
}
async fn snapshot_diff(
    State(app): State<App>,
    Path(id): Path<String>,
    Query(input): Query<DiffQuery>,
) -> Result<Json<Value>> {
    let snapshots: Vec<Snapshot> = sqlx::query_as("SELECT * FROM snapshots WHERE id IN (?,?)")
        .bind(&id)
        .bind(&input.base)
        .fetch_all(&app.store.pool)
        .await?;
    if snapshots.len() != 2 {
        return Err(bad("请选择两个不同的快照"));
    }
    if snapshots[0].user_id != snapshots[1].user_id
        || snapshots[0].direction != snapshots[1].direction
    {
        return Err(bad("只能对比同一用户、同一方向的快照"));
    }
    if snapshots.iter().any(|s| s.status != "complete") {
        return Err(bad(
            "只有两个完整快照才能判定新增或取消关系；部分或失败快照不参与差异判断",
        ));
    }
    let current = snapshots.iter().find(|s| s.id == id).ok_or_else(missing)?;
    let base = snapshots
        .iter()
        .find(|s| s.id == input.base)
        .ok_or_else(missing)?;
    if base.created_at > current.created_at {
        return Err(bad("基准快照必须早于当前快照"));
    }
    let mut results = Vec::new();
    for (a, b) in [(&id, &input.base), (&input.base, &id)] {
        let rows:Vec<(String,String,String)>=sqlx::query_as("SELECT user_id,username,name FROM relationships r WHERE snapshot_id=? AND NOT EXISTS (SELECT 1 FROM relationships b WHERE b.snapshot_id=? AND b.user_id=r.user_id) ORDER BY username").bind(a).bind(b).fetch_all(&app.store.pool).await?;
        results.push(
            rows.into_iter()
                .map(|(id, username, name)| json!({"id":id,"username":username,"name":name}))
                .collect::<Vec<_>>(),
        );
    }
    Ok(Json(
        json!({"added":results[0],"removed":results[1],"base":input.base,"current":id}),
    ))
}

#[cfg(not(debug_assertions))]
#[derive(rust_embed::RustEmbed)]
#[folder = "frontend/dist/"]
struct Assets;
async fn static_file(uri: axum::http::Uri) -> Response {
    if uri.path().starts_with("/api/") {
        return missing().into_response();
    }
    let path = uri.path().trim_start_matches('/');
    if path.contains("..") || path.contains('\\') {
        return missing().into_response();
    }
    let path = if path.is_empty() { "index.html" } else { path };
    #[cfg(not(debug_assertions))]
    let asset = Assets::get(path)
        .map(|a| (a.data.into_owned(), path.to_string()))
        .or_else(|| Assets::get("index.html").map(|a| (a.data.into_owned(), "index.html".into())));
    #[cfg(debug_assertions)]
    let asset = {
        let root = std::path::Path::new(env!("CARGO_MANIFEST_DIR")).join("frontend/dist");
        match tokio::fs::read(root.join(path)).await {
            Ok(bytes) => Some((bytes, path.to_string())),
            Err(_) => tokio::fs::read(root.join("index.html"))
                .await
                .ok()
                .map(|bytes| (bytes, "index.html".into())),
        }
    };
    match asset {
        Some((bytes, path)) => (
            [(
                header::CONTENT_TYPE,
                mime_guess::from_path(path)
                    .first_or_octet_stream()
                    .to_string(),
            )],
            Body::from(bytes),
        )
            .into_response(),
        None => (StatusCode::SERVICE_UNAVAILABLE, "请先运行前端构建").into_response(),
    }
}
