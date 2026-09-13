package downloader

import "testing"

func TestMediaIdentityGroupsEquivalentMediaURLs(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "video tag stripped",
			in:   "https://video.twimg.com/ext_tw_video/123/pu/vid/1280x720/abc.mp4?tag=12",
			want: "https://video.twimg.com/ext_tw_video/123/pu/vid/1280x720/abc",
		},
		{
			name: "host and scheme normalized",
			in:   "http://VIDEO.twimg.com/ext_tw_video/123/pu/vid/1280x720/abc.mp4",
			want: "https://video.twimg.com/ext_tw_video/123/pu/vid/1280x720/abc",
		},
		{
			name: "photo extension stripped",
			in:   "https://pbs.twimg.com/media/abc.jpg",
			want: "https://pbs.twimg.com/media/abc",
		},
		{
			name: "photo format param dropped",
			in:   "https://pbs.twimg.com/media/abc?format=jpg",
			want: "https://pbs.twimg.com/media/abc",
		},
		{
			name: "size variant keeps name parameter",
			in:   "https://pbs.twimg.com/media/abc?format=jpg&name=small",
			want: "https://pbs.twimg.com/media/abc?name=small",
		},
		{
			name: "non-twimg url unchanged",
			in:   "https://example.com/media/abc.jpg?tag=1",
			want: "https://example.com/media/abc.jpg?tag=1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MediaIdentity(tt.in)
			if got != tt.want {
				t.Fatalf("MediaIdentity(%q) = %q, want %q", tt.in, got, tt.want)
			}
			if second := MediaIdentity(got); second != got {
				t.Fatalf("not idempotent: MediaIdentity(%q) = %q, want %q", got, second, got)
			}
		})
	}
}

func TestMediaIdentityCollapsesDuplicateURLForms(t *testing.T) {
	// 同一份媒体的常见写法差异必须收敛到同一个身份键：归档时据此复用已有文件。
	groups := [][]string{
		{
			"https://pbs.twimg.com/media/abc.jpg",
			"https://pbs.twimg.com/media/abc?format=jpg",
			"https://PBS.twimg.com/media/abc.jpeg",
		},
		{
			"https://video.twimg.com/ext_tw_video/123/pu/vid/1280x720/abc.mp4?tag=12",
			"https://video.twimg.com/ext_tw_video/123/pu/vid/1280x720/abc.mp4?tag=14",
		},
	}
	for _, group := range groups {
		want := MediaIdentity(group[0])
		for _, raw := range group[1:] {
			if got := MediaIdentity(raw); got != want {
				t.Fatalf("MediaIdentity(%q) = %q, want %q (same media as %q)", raw, got, want, group[0])
			}
		}
	}
}
