package downloader

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

type Result struct {
	Path  string
	Bytes int64
}

type Downloader struct {
	client *http.Client
}

func New() *Downloader {
	return &Downloader{
		client: &http.Client{Timeout: 10 * time.Minute},
	}
}

func (d *Downloader) Download(ctx context.Context, rawURL string, dir string, filenameHint string) (Result, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Result{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return Result{}, err
	}
	response, err := d.client.Do(request)
	if err != nil {
		return Result{}, err
	}
	defer response.Body.Close()
	if response.StatusCode >= 400 {
		return Result{}, fmt.Errorf("download failed: HTTP %d", response.StatusCode)
	}

	path, err := uniquePath(filepath.Join(dir, filenameFromURL(rawURL, filenameHint)))
	if err != nil {
		return Result{}, err
	}
	file, err := os.Create(path)
	if err != nil {
		return Result{}, err
	}
	defer file.Close()

	bytes, err := io.Copy(file, response.Body)
	if err != nil {
		return Result{}, err
	}
	return Result{Path: path, Bytes: bytes}, nil
}

var unsupportedFilenameChars = regexp.MustCompile(`[/\\:*?"<>\|]`)

func filenameFromURL(rawURL string, hint string) string {
	ext := ".bin"
	if parsed, err := url.Parse(rawURL); err == nil {
		if value := filepath.Ext(parsed.Path); value != "" {
			ext = value
		}
	}
	base := sanitizeFilename(hint)
	if base == "" {
		base = "media"
	}
	if filepath.Ext(base) != "" {
		return base
	}
	return base + ext
}

func sanitizeFilename(name string) string {
	name = strings.TrimSpace(name)
	name = unsupportedFilenameChars.ReplaceAllString(name, "")
	name = strings.ReplaceAll(name, "\r", " ")
	name = strings.ReplaceAll(name, "\n", " ")
	if name == "" {
		return ""
	}
	const maxBytes = 180
	var builder strings.Builder
	for _, ch := range name {
		if builder.Len()+utf8.RuneLen(ch) > maxBytes {
			break
		}
		builder.WriteRune(ch)
	}
	return strings.TrimSpace(builder.String())
}

func uniquePath(path string) (string, error) {
	for {
		_, err := os.Lstat(path)
		if os.IsNotExist(err) {
			return path, nil
		}
		if err != nil {
			return "", err
		}
		dir := filepath.Dir(path)
		ext := filepath.Ext(path)
		stem := strings.TrimSuffix(filepath.Base(path), ext)
		for index := 1; ; index++ {
			candidate := filepath.Join(dir, fmt.Sprintf("%s(%d)%s", stem, index, ext))
			_, err := os.Lstat(candidate)
			if os.IsNotExist(err) {
				return candidate, nil
			}
			if err != nil {
				return "", err
			}
		}
	}
}
