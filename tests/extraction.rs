use serde_json::json;
use x_archive::tools::parse_tweets;

#[test]
fn gallery_messages_preserve_text_only_tweets_and_classify_animated_video() {
    let data = json!([
        [2, {"tweet_id": 123, "user": {"name":"jack", "nick":"Jack"}, "content":"hello", "date":"2026-11-10 00:00:00", "count":1}],
        [3, "https://video.twimg.com/tweet_video/test.mp4", {"tweet_id":123, "user":{"name":"jack","nick":"Jack"}, "content":"hello", "date":"2026-11-10 00:00:00", "num":1, "type":"animated_gif", "extension":"mp4"}],
        [2, {"tweet_id": 124, "user":{"name":"jack"}, "content":"text only", "date":"2026-11-10 01:00:00", "count":0}]
    ]);
    let tweets = parse_tweets(&serde_json::to_vec(&data).unwrap()).unwrap();
    assert_eq!(tweets.len(), 2);
    assert_eq!(tweets[0].media[0].kind, "gif");
    assert_eq!(tweets[1].text, "text only");
    assert!(tweets[1].media.is_empty());
}

#[test]
fn extractor_does_not_accept_off_platform_media_or_invalid_metadata() {
    let data = json!([[3,"http://127.0.0.1/private",{"tweet_id":123,"user":{"name":"jack"},"num":1,"extension":"jpg"}]]);
    assert!(parse_tweets(&serde_json::to_vec(&data).unwrap()).is_err());
}

#[test]
fn gallery_authentication_errors_are_classified_regardless_of_exit_status() {
    for success in [true, false] {
        let output = x_archive::tools::ToolOutput {
            success,
            stdout: serde_json::to_vec(
                &json!([[-1,{"error":"AuthenticationError","message":"auth_token=DO_NOT_EXPOSE"}]]),
            )
            .unwrap(),
            stderr: String::new(),
        };
        let error = output.failure().unwrap();
        assert!(error.contains("Cookie"));
        assert!(!error.contains("DO_NOT_EXPOSE"));
    }
}
