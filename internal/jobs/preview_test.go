package jobs

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestFetchPosterImageRejectsOversizedBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(bytes.Repeat([]byte{'j'}, 64))
	}))
	defer server.Close()
	dest := filepath.Join(t.TempDir(), "v.mp4.preview.jpg")

	// 超过上限的海报必须报错且不发布：截断的 jpg 一旦落盘，之后会因文件已存在被永久沿用。
	err := fetchPosterImage(context.Background(), server.Client(), server.URL+"/p.jpg", dest, 32)
	if err == nil {
		t.Fatal("oversized poster should fail")
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Fatalf("truncated poster was published: %v", statErr)
	}
	entries, _ := os.ReadDir(filepath.Dir(dest))
	if len(entries) != 0 {
		t.Fatalf("temporary files left behind: %v", entries)
	}

	if err := fetchPosterImage(context.Background(), server.Client(), server.URL+"/p.jpg", dest, 64); err != nil {
		t.Fatalf("poster within limit: %v", err)
	}
	if data, err := os.ReadFile(dest); err != nil || len(data) != 64 {
		t.Fatalf("published poster = %d bytes, err %v", len(data), err)
	}
}
