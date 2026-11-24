package config

import (
	"testing"
	"time"
)

func TestArchiveFilterValidationAndMatching(t *testing.T) {
	raw, err := MarshalArchiveFilter(&ArchiveFilter{
		MediaTypes:      []string{"image", "VIDEO", "photo"},
		ExcludeRetweets: true,
		MinBytes:        1024,
		MinWidth:        800,
		SinceDate:       "2026-01-01",
		UntilDate:       "2026-12-31",
	})
	if err != nil {
		t.Fatalf("MarshalArchiveFilter: %v", err)
	}
	parsed, err := ParseArchiveFilterJSON(raw)
	if err != nil {
		t.Fatalf("ParseArchiveFilterJSON: %v", err)
	}
	if len(parsed.MediaTypes) != 2 || !parsed.AllowsMediaType("photo") || !parsed.AllowsMediaType("video") || parsed.AllowsMediaType("animated_gif") {
		t.Fatalf("unexpected media types: %+v", parsed.MediaTypes)
	}
	if !parsed.AllowsDimensions(1000, 500) || parsed.AllowsDimensions(640, 480) || !parsed.AllowsDimensions(0, 0) {
		t.Fatal("dimension filtering mismatch")
	}
	if !parsed.AllowsPublishedAt(time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatal("expected date in range")
	}
	if parsed.AllowsPublishedAt(time.Date(2025, 12, 31, 23, 59, 0, 0, time.UTC)) {
		t.Fatal("expected date before since to be rejected")
	}
	if _, err := (&ArchiveFilter{SinceDate: "2026-12-31", UntilDate: "2026-01-01"}).NormalizeAndValidate(); err == nil {
		t.Fatal("expected invalid range error")
	}
}
