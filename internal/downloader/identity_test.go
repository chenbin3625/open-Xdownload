package downloader

import (
	"net/url"
	"testing"
)

func TestMediaIdentityGroupsEquivalentMediaURLs(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "video tag stripped",
			in:   "https://video.twimg.com/ext_tw_video/123/pu/vid/1280x720/abc.mp4?tag=12",
			want: "https://video.twimg.com/ext_tw_video/123/pu/vid/1280x720/abc?format=mp4",
		},
		{
			name: "host and scheme normalized",
			in:   "http://VIDEO.twimg.com/ext_tw_video/123/pu/vid/1280x720/abc.mp4",
			want: "https://video.twimg.com/ext_tw_video/123/pu/vid/1280x720/abc?format=mp4",
		},
		{
			name: "photo extension stripped",
			in:   "https://pbs.twimg.com/media/abc.jpg",
			want: "https://pbs.twimg.com/media/abc?format=jpg",
		},
		{
			name: "photo format param normalized",
			in:   "https://pbs.twimg.com/media/abc?format=jpg",
			want: "https://pbs.twimg.com/media/abc?format=jpg",
		},
		{
			// name 只是尺寸指令：同一张图片的各分辨率写法必须收敛到同一身份，
			// 否则同一张照片会按缩略图/原图各存一份。
			name: "photo size variant drops name parameter",
			in:   "https://pbs.twimg.com/media/abc?format=jpg&name=small",
			want: "https://pbs.twimg.com/media/abc?format=jpg",
		},
		{
			name: "video host keeps name parameter",
			in:   "https://video.twimg.com/ext_tw_video/123/pu/vid/abc.mp4?name=xyz",
			want: "https://video.twimg.com/ext_tw_video/123/pu/vid/abc?format=mp4&name=xyz",
		},
		{
			name: "different photo format remains distinct",
			in:   "https://pbs.twimg.com/media/abc?format=png",
			want: "https://pbs.twimg.com/media/abc?format=png",
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

func TestMediaIdentityKeepsDifferentFormatsSeparate(t *testing.T) {
	jpg := MediaIdentity("https://pbs.twimg.com/media/abc.jpg")
	png := MediaIdentity("https://pbs.twimg.com/media/abc?format=png")
	if jpg == png {
		t.Fatalf("different formats share identity %q", jpg)
	}
}

func TestMediaIdentityCollapsesDuplicateURLForms(t *testing.T) {
	// 同一份媒体的常见写法差异必须收敛到同一个身份键：归档时据此复用已有文件。
	groups := [][]string{
		{
			"https://pbs.twimg.com/media/abc.jpg",
			"https://pbs.twimg.com/media/abc?format=jpg",
			"https://PBS.twimg.com/media/abc.jpeg",
			// 尺寸变体同属一张照片：卡片图片与 syndication 常给出 name=small/large。
			"https://pbs.twimg.com/media/abc?format=jpg&name=small",
			"https://pbs.twimg.com/media/abc?format=jpg&name=large",
			"https://pbs.twimg.com/media/abc.jpg?name=4096x4096",
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

// largePhotoURL 必须无条件把图片请求改写到最大尺寸。这是跨尺寸去重的另一半：身份键
// 忽略 name 之后，同一张照片的任一写法都可能是先到的那一个，若沿用请求里的 name=small
// 就会把缩略图当成该照片的归档件存下来，原图再也不会被取回。
func TestLargePhotoURLAlwaysRequestsLargestSize(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "missing name is filled",
			in:   "https://pbs.twimg.com/media/abc.jpg",
			want: "https://pbs.twimg.com/media/abc.jpg?name=4096x4096",
		},
		{
			name: "small thumbnail is upgraded",
			in:   "https://pbs.twimg.com/media/abc?format=jpg&name=small",
			want: "https://pbs.twimg.com/media/abc?format=jpg&name=4096x4096",
		},
		{
			name: "large is upgraded",
			in:   "https://pbs.twimg.com/media/abc?format=jpg&name=large",
			want: "https://pbs.twimg.com/media/abc?format=jpg&name=4096x4096",
		},
		{
			// orig 是原始上传件，改成 4096x4096 反而降级。
			name: "orig is preserved",
			in:   "https://pbs.twimg.com/media/abc?format=jpg&name=orig",
			want: "https://pbs.twimg.com/media/abc?format=jpg&name=orig",
		},
		{
			name: "video host untouched",
			in:   "https://video.twimg.com/ext_tw_video/123/pu/vid/abc.mp4",
			want: "https://video.twimg.com/ext_tw_video/123/pu/vid/abc.mp4",
		},
		{
			name: "non twimg host untouched",
			in:   "https://example.com/media/abc.jpg",
			want: "https://example.com/media/abc.jpg",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := largePhotoURL(tt.in); got != tt.want {
				t.Fatalf("largePhotoURL(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// 去重正确性依赖两个函数配合：身份键忽略尺寸参数（同一张照片只归档一份），而请求 URL
// 被改写到同一个最大尺寸（那一份必定是最大尺寸的字节）。只做前者会让先到的缩略图冒充
// 原图；只做后者会让同一张照片按不同写法各存一份。这里断言二者对同一张照片的各种写法
// 同时成立。
func TestPhotoSizeVariantsShareIdentityAndFetchSameBytes(t *testing.T) {
	variants := []string{
		"https://pbs.twimg.com/media/abc.jpg",
		"https://pbs.twimg.com/media/abc?format=jpg",
		"https://pbs.twimg.com/media/abc?format=jpg&name=small",
		"https://pbs.twimg.com/media/abc?format=jpg&name=medium",
		"https://pbs.twimg.com/media/abc?format=jpg&name=large",
		"https://pbs.twimg.com/media/abc?format=jpg&name=4096x4096",
		"https://pbs.twimg.com/media/abc.jpg?tag=3&name=small",
	}
	wantIdentity := MediaIdentity(variants[0])
	wantRequest := largePhotoURL(variants[0])
	for _, raw := range variants[1:] {
		if got := MediaIdentity(raw); got != wantIdentity {
			t.Errorf("MediaIdentity(%q) = %q, want %q (same photo)", raw, got, wantIdentity)
		}
		if got := requestedSize(t, largePhotoURL(raw)); got != requestedSize(t, wantRequest) {
			t.Errorf("largePhotoURL(%q) requests size %q, want %q", raw, got, requestedSize(t, wantRequest))
		}
	}
}

func requestedSize(t *testing.T, rawURL string) string {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse %q: %v", rawURL, err)
	}
	return parsed.Query().Get("name")
}
