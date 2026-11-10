use x_archive::domain::*;

#[test]
fn accepts_only_canonical_x_targets() {
    assert_eq!(username("@jack").unwrap(), "jack");
    assert_eq!(username("https://twitter.com/Jack/").unwrap(), "jack");
    assert_eq!(
        tweet_id("https://x.com/jack/status/123456?s=20").unwrap(),
        "123456"
    );
    for bad in [
        "https://evil.test/jack",
        "https://x.com@evil.test/jack",
        "../root",
        "home",
        "ab/cd",
    ] {
        assert!(username(bad).is_err(), "{bad}");
    }
    assert!(tweet_id("https://x.com/jack/status/12/evil").is_err());
    assert!(tweet_id("file:///etc/passwd").is_err());
}

#[test]
fn cookie_validation_requires_x_session_without_echoing_secrets() {
    let good = "# Netscape HTTP Cookie File\n.x.com\tTRUE\t/\tTRUE\t0\tauth_token\tSECRET_A\n#HttpOnly_.x.com\tTRUE\t/\tTRUE\t0\tct0\tSECRET_B\n";
    assert!(validate_cookies(good).is_ok());
    assert!(validate_cookies("auth_token=SECRET_A").is_err());
    assert!(validate_cookies(&good.replace(".x.com", ".evil.com")).is_err());
    let error = validate_cookies(&good.replace("ct0", "missing"))
        .unwrap_err()
        .to_string();
    assert!(!error.contains("SECRET"));
}

#[test]
fn incomplete_relationship_fetch_never_proves_a_removal() {
    assert!(complete_snapshot(true, Some(2), 2, false));
    assert!(!complete_snapshot(true, Some(10), 2, false));
    assert!(!complete_snapshot(false, Some(2), 2, false));
    assert!(!complete_snapshot(true, None, 2, false));
    assert!(!complete_snapshot(true, Some(2), 2, true));
    assert!(complete_snapshot(true, Some(0), 0, false));
}

#[test]
fn tool_errors_are_classified_without_exposing_output() {
    let message = safe_tool_error("auth_token=SECRET HTTP 401 Unauthorized");
    assert!(!message.contains("SECRET"));
    assert!(message.contains("Cookie"));
    assert!(safe_tool_error("429 Too Many Requests").contains("限制"));
}
