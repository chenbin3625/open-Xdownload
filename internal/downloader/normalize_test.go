package downloader

import "testing"

func TestNormalizeMediaURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "video tag stripped",
			in:   "https://video.twimg.com/ext_tw_video/123/pu/vid/1280x720/abc.mp4?tag=12",
			want: "https://video.twimg.com/ext_tw_video/123/pu/vid/1280x720/abc.mp4",
		},
		{
			name: "tag stripped but format kept",
			in:   "https://video.twimg.com/ext_tw_video/123/pu/vid/1280x720/abc.mp4?tag=14&format=mp4",
			want: "https://video.twimg.com/ext_tw_video/123/pu/vid/1280x720/abc.mp4?format=mp4",
		},
		{
			name: "photo url with format and name unchanged",
			in:   "https://pbs.twimg.com/media/abc?format=jpg&name=small",
			want: "https://pbs.twimg.com/media/abc?format=jpg&name=small",
		},
		{
			name: "photo url without query unchanged",
			in:   "https://pbs.twimg.com/media/abc.jpg",
			want: "https://pbs.twimg.com/media/abc.jpg",
		},
		{
			name: "no query unchanged",
			in:   "https://example.com/video.mp4",
			want: "https://example.com/video.mp4",
		},
		{
			name: "non-twitter tag not stripped",
			in:   "https://example.com/video.mp4?tag=secret",
			want: "https://example.com/video.mp4?tag=secret",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NormalizeMediaURL(tt.in)
			if got != tt.want {
				t.Fatalf("NormalizeMediaURL(%q) = %q, want %q", tt.in, got, tt.want)
			}
			// 幂等：再规范化一次结果不变
			if second := NormalizeMediaURL(got); second != got {
				t.Fatalf("not idempotent: NormalizeMediaURL(%q) = %q, want %q", got, second, got)
			}
		})
	}
}

func TestNormalizeMediaURLEmpty(t *testing.T) {
	if got := NormalizeMediaURL(""); got != "" {
		t.Fatalf(`NormalizeMediaURL("") = %q, want empty`, got)
	}
}

func TestNormalizeMediaURLRejectsLookalikeHosts(t *testing.T) {
	for _, raw := range []string{
		"https://notpbs.twimg.com.evil/video.mp4?tag=1",
		"https://pbs.twimg.com.evil/media.jpg?tag=1",
		"file://twimg.com/video.mp4?tag=1",
	} {
		if got := NormalizeMediaURL(raw); got != raw {
			t.Fatalf("NormalizeMediaURL(%q) = %q, want unchanged", raw, got)
		}
	}
}
