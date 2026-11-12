package parser

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestArchiveRelationsKeepBodiesWithoutIncludingNestedMedia(t *testing.T) {
	result := gjson.Parse(`{
"rest_id":"100",
"legacy":{"full_text":"short body","created_at":"Tue Nov 10 12:00:00 +0000 2026","conversation_id_str":"99",
 "retweeted_status_results":{"result":{"rest_id":"103","legacy":{"full_text":"Retweeted body"}}}},
"note_tweet":{"note_tweet_results":{"result":{"text":"Complete body &amp; original text"}}},
"quoted_tweet_results":{"result":{"__typename":"TweetWithVisibilityResults","tweet":{"rest_id":"101",
 "legacy":{"full_text":"Quoted body","extended_entities":{"media":[{"id_str":"photo","type":"photo","media_url_https":"https://pbs.twimg.com/media/quoted.jpg"}]}}}}},
"reply_to_results":{"result":{"rest_id":"102","legacy":{"full_text":"Reply body"}}}
}`)
	tweet, err := TweetFromGraphQLResultWithOptions("", "author", "100", result, ParseOptions{RequireRelated: true})
	if err != nil {
		t.Fatal(err)
	}
	if tweet.Text != "Complete body & original text" || tweet.ConversationID != "99" || tweet.QuotedTweetID != "101" || tweet.ReplyToID != "102" || tweet.RetweetedTweetID != "103" {
		t.Fatalf("archive relationships: %+v", tweet)
	}
	if len(tweet.Media) != 0 || len(tweet.Related) != 3 {
		t.Fatalf("nested media option changed related bodies: %+v", tweet)
	}
	bodies := map[string]string{}
	for _, related := range tweet.Related {
		bodies[related.ID] = related.Text
		if related.ID == "101" && len(related.Media) != 1 {
			t.Fatal("related tweet lost its own media")
		}
	}
	for id, body := range map[string]string{"101": "Quoted body", "102": "Reply body", "103": "Retweeted body"} {
		if bodies[id] != body {
			t.Fatalf("relation %s body = %q", id, bodies[id])
		}
	}
}

func TestArchiveUnavailableRelationKeepsIDAndMalformedAvailableBodyFails(t *testing.T) {
	result := gjson.Parse(`{"rest_id":"100","legacy":{"full_text":"Body","quoted_status_id_str":"99"},"quoted_tweet_results":{"result":{"__typename":"TweetUnavailable","rest_id":"99"}}}`)
	tweet, err := TweetFromGraphQLResultWithOptions("", "author", "100", result, ParseOptions{RequireRelated: true})
	if err != nil || tweet.QuotedTweetID != "99" || len(tweet.Related) != 0 || tweet.Text != "Body" {
		t.Fatalf("unavailable relation: %+v %v", tweet, err)
	}
	malformed := gjson.Parse(`{"rest_id":"100","legacy":{"full_text":"Body"},"quoted_tweet_results":{"result":{"rest_id":"99","legacy":{"created_at":"broken"}}}}`)
	if _, err := TweetFromGraphQLResultWithOptions("", "author", "100", malformed, ParseOptions{RequireRelated: true}); err == nil || !strings.Contains(err.Error(), "99") {
		t.Fatalf("malformed related body was silently discarded: %v", err)
	}
}

func TestSyndicationRelationsKeepIDsAndAvailableBodies(t *testing.T) {
	result := gjson.Parse(`{
"id_str":"100","text":"Body","created_at":"2026-11-10T12:00:00.000Z",
"conversation_id_str":"90","in_reply_to_status_id_str":"102","quoted_status_id_str":"101","retweeted_status_id_str":"103",
"user":{"id_str":"42","name":"Author","screen_name":"author"},
"quoted_tweet":{"id_str":"101","text":"Quoted","created_at":"2026-11-10T11:00:00.000Z","user":{"id_str":"7","name":"Quoted","screen_name":"quoted"}},
"retweeted_status":{"id_str":"103","text":"Reposted","created_at":"2026-11-10T10:00:00.000Z","user":{"id_str":"8","name":"Reposted","screen_name":"reposted"}},
"in_reply_to_status":{"__typename":"TweetUnavailable","id_str":"102"}
}`)
	tweet, err := tweetFromSyndication("", "author", "100", result, ParseOptions{RequireRelated: true})
	if err != nil {
		t.Fatal(err)
	}
	if tweet.ConversationID != "90" || tweet.ReplyToID != "102" || tweet.QuotedTweetID != "101" || tweet.RetweetedTweetID != "103" {
		t.Fatalf("syndication relations: %+v", tweet)
	}
	bodies := map[string]string{}
	for _, related := range tweet.Related {
		bodies[related.ID] = related.Text
	}
	if len(tweet.Related) != 2 || bodies["101"] != "Quoted" || bodies["103"] != "Reposted" {
		t.Fatalf("syndication related bodies: %+v", tweet.Related)
	}
}
