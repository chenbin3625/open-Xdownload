use anyhow::{Result, bail, ensure};
use url::Url;

fn x_url(input: &str) -> Result<Url> {
    let url = Url::parse(input.trim())?;
    ensure!(
        url.scheme() == "https"
            && url.username().is_empty()
            && url.password().is_none()
            && url.port().is_none(),
        "请输入 HTTPS 的 X 链接"
    );
    ensure!(
        matches!(
            url.host_str(),
            Some("x.com" | "www.x.com" | "twitter.com" | "www.twitter.com")
        ),
        "只支持 x.com 或 twitter.com 链接"
    );
    Ok(url)
}

pub fn username(input: &str) -> Result<String> {
    let input = input.trim();
    let value = if input.contains("://") {
        let url = x_url(input)?;
        let parts: Vec<_> = url.path().trim_matches('/').split('/').collect();
        ensure!(parts.len() == 1, "请输入用户主页链接");
        parts[0].to_string()
    } else {
        input.trim_start_matches('@').to_string()
    };
    ensure!(
        !value.is_empty()
            && value.len() <= 15
            && value
                .bytes()
                .all(|c| c.is_ascii_alphanumeric() || c == b'_'),
        "用户名应为 1 至 15 位字母、数字或下划线"
    );
    let value = value.to_ascii_lowercase();
    ensure!(
        ![
            "home",
            "i",
            "settings",
            "search",
            "explore",
            "intent",
            "messages",
            "notifications",
            "compose"
        ]
        .contains(&value.as_str()),
        "请输入用户的用户名"
    );
    Ok(value)
}

pub fn tweet_id(input: &str) -> Result<String> {
    let url = x_url(input)?;
    let parts: Vec<_> = url.path().trim_matches('/').split('/').collect();
    let id = if parts.len() == 3 && parts[1] == "status" {
        username(parts[0])?;
        parts[2]
    } else if parts.len() == 4 && parts[..3] == ["i", "web", "status"] {
        parts[3]
    } else {
        bail!("请输入完整的推文链接")
    };
    ensure!(
        !id.is_empty() && id.len() <= 20 && id.bytes().all(|c| c.is_ascii_digit()),
        "推文 ID 无效"
    );
    Ok(id.to_string())
}

pub fn list_id(input: &str) -> Result<String> {
    let value = if input.contains("://") {
        let url = x_url(input)?;
        let parts: Vec<_> = url.path().trim_matches('/').split('/').collect();
        ensure!(
            parts.len() == 3 && parts[..2] == ["i", "lists"],
            "请输入 X 列表链接"
        );
        parts[2].to_string()
    } else {
        input.trim().to_string()
    };
    ensure!(
        !value.is_empty() && value.len() <= 20 && value.bytes().all(|c| c.is_ascii_digit()),
        "列表 ID 无效"
    );
    let id = value
        .parse::<u64>()
        .map_err(|_| anyhow::anyhow!("列表 ID 无效"))?;
    ensure!(id > 0, "列表 ID 无效");
    Ok(id.to_string())
}

pub fn cookies_from_header(input: &str) -> Result<String> {
    ensure!(input.len() <= 1024 * 1024, "Cookie 内容不能超过 1 MB");
    ensure!(
        input.bytes().all(|c| c.is_ascii() && !c.is_ascii_control()),
        "Cookie 内容包含无效字符"
    );
    let input = input.trim();
    let input = if input
        .get(..7)
        .is_some_and(|prefix| prefix.eq_ignore_ascii_case("cookie:"))
    {
        input[7..].trim()
    } else {
        input
    };
    ensure!(!input.is_empty(), "请输入 X 登录 Cookie");
    let mut auth = None;
    let mut csrf = None;
    for pair in input.trim_end_matches(';').split(';') {
        let (name, value) = pair.split_once('=').ok_or_else(|| {
            anyhow::anyhow!("Cookie 格式不正确，请输入 name=value; name=value 格式")
        })?;
        let name = name.trim();
        let value = value.trim();
        let value = value
            .strip_prefix('"')
            .and_then(|value| value.strip_suffix('"'))
            .unwrap_or(value);
        ensure!(
            !name.is_empty()
                && name
                    .bytes()
                    .all(|c| c.is_ascii_alphanumeric() || b"!#$%&'*+-.^_`|~".contains(&c))
                && value.bytes().all(
                    |c| matches!(c, 0x21 | 0x23..=0x2b | 0x2d..=0x3a | 0x3c..=0x5b | 0x5d..=0x7e)
                ),
            "Cookie 格式不正确，请输入 name=value; name=value 格式"
        );
        let slot = match name {
            "auth_token" => &mut auth,
            "ct0" => &mut csrf,
            _ => continue,
        };
        ensure!(slot.is_none(), "auth_token 和 ct0 不能重复填写");
        ensure!(!value.is_empty(), "auth_token 和 ct0 不能为空");
        *slot = Some(value);
    }
    let (Some(auth), Some(csrf)) = (auth, csrf) else {
        bail!("Cookie 必须包含 auth_token 和 ct0")
    };
    // Tool sessions require Netscape format; only the X authentication pair is retained.
    Ok(format!(
        "# Netscape HTTP Cookie File\n.x.com\tTRUE\t/\tTRUE\t0\tauth_token\t{auth}\n.x.com\tTRUE\t/\tTRUE\t0\tct0\t{csrf}\n"
    ))
}

pub fn validate_cookies(input: &str) -> Result<()> {
    ensure!(
        input.len() <= 1024 * 1024 && !input.contains('\0'),
        "Cookie 内容无效或超过 1 MB"
    );
    let mut auth = false;
    let mut csrf = false;
    for line in input.lines() {
        let line = line.trim_end_matches('\r');
        let line = if let Some(value) = line.strip_prefix("#HttpOnly_") {
            value
        } else if line.starts_with('#') || line.is_empty() {
            continue;
        } else {
            line
        };
        let fields: Vec<_> = line.split('\t').collect();
        ensure!(fields.len() == 7, "Cookie 存储格式不正确");
        ensure!(
            matches!(fields[1], "TRUE" | "FALSE")
                && matches!(fields[3], "TRUE" | "FALSE")
                && fields[4].parse::<u64>().is_ok(),
            "Cookie 文件格式不正确"
        );
        if matches!(
            fields[0].trim_start_matches('.'),
            "x.com" | "twitter.com" | "www.x.com" | "www.twitter.com"
        ) {
            let expires = fields[4].parse::<i64>().unwrap_or(0);
            let current = expires == 0 || expires > chrono::Utc::now().timestamp();
            if fields[5] == "auth_token" && !fields[6].is_empty() && current {
                auth = true;
            }
            if fields[5] == "ct0" && !fields[6].is_empty() && current {
                csrf = true;
            }
        }
    }
    ensure!(
        auth && csrf,
        "未找到未过期的 X auth_token 和 ct0，请更新登录 Cookie"
    );
    Ok(())
}

pub fn safe_tool_error(input: &str) -> &'static str {
    let text = input.to_ascii_lowercase();
    if text.contains("429") || text.contains("rate limit") || text.contains("too many requests") {
        "触发 X 请求频率限制，请稍后重试或延长同步间隔"
    } else if [
        "401",
        "unauthorized",
        "login",
        "authenticate",
        "authentication",
        "cookies",
        "csrf",
    ]
    .iter()
    .any(|s| text.contains(s))
    {
        "Cookie 无效或会话已过期，请在设置中更新 Cookie 并重新检测"
    } else if text.contains("403") || text.contains("protected") || text.contains("not authorized")
    {
        "当前会话无权访问此内容，或 X 暂时限制了访问"
    } else if text.contains("404") || text.contains("not found") || text.contains("unavailable") {
        "内容不存在、已删除，或当前会话不可访问"
    } else if text.contains("timeout")
        || text.contains("timed out")
        || text.contains("connection")
        || text.contains("resolve")
    {
        "连接 X 失败或超时，请检查网络后重试"
    } else {
        "下载工具未能完成操作，请检查工具版本、网络和账号权限后重试"
    }
}

pub fn complete_snapshot(
    exit_success: bool,
    expected: Option<u64>,
    observed: u64,
    limited: bool,
) -> bool {
    exit_success && !limited && expected == Some(observed)
}
