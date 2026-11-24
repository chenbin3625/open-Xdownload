package httpapi

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/storage"
)

const (
	maxExportZIPFiles = 500
	maxExportZIPBytes = 2 << 30 // 2 GiB
	maxExportTweets   = 2000
)

func (s *Server) exportMediaZIP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs          []int64 `json:"ids"`
		User         string  `json:"user"`
		Kind         string  `json:"kind"`
		Search       string  `json:"search"`
		Starred      *bool   `json:"starred"`
		TagID        int64   `json:"tagId"`
		CollectionID int64   `json:"collectionId"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	records := []storage.DownloadRecord{}
	if len(req.IDs) > 0 {
		limit := min(len(req.IDs), maxExportZIPFiles)
		for _, id := range req.IDs[:limit] {
			rec, err := s.store.GetDownload(r.Context(), id)
			if err == nil && rec != nil {
				records = append(records, *rec)
			}
		}
	} else {
		page, err := s.store.ListLibraryDownloadsPage(r.Context(), storage.LibraryPageOptions{
			Page:     1,
			PageSize: min(storage.MaxLibraryPageSize, maxExportZIPFiles),
			Filter: storage.LibraryFilter{
				Kind:         req.Kind,
				User:         req.User,
				Search:       req.Search,
				Starred:      req.Starred,
				TagID:        req.TagID,
				CollectionID: req.CollectionID,
			},
			Sort: "date_desc",
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		records = page.Items
	}
	if len(records) == 0 {
		writeError(w, http.StatusNotFound, storage.ErrNotFound("没有可导出的媒体文件", nil))
		return
	}
	cfg, err := s.store.GetConfig(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	roots, _ := s.store.DownloadRoots(r.Context())
	if cfg.DownloadDir != "" {
		roots = append([]string{cfg.DownloadDir}, roots...)
	}

	filename := fmt.Sprintf("open-xdownload-media-%s.zip", time.Now().UTC().Format("2006-01-02"))
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	zw := zip.NewWriter(w)
	defer zw.Close()

	usedNames := map[string]int{}
	var totalWritten int64
	for _, rec := range records {
		if totalWritten >= maxExportZIPBytes {
			break
		}
		resolved, ok := resolvePathWithinRoots(rec.FilePath, roots)
		if !ok {
			continue
		}
		f, err := os.Open(resolved)
		if err != nil {
			continue
		}
		info, err := f.Stat()
		if err != nil || info.IsDir() {
			_ = f.Close()
			continue
		}
		if totalWritten+info.Size() > maxExportZIPBytes && totalWritten > 0 {
			_ = f.Close()
			break
		}
		base := filepath.Base(resolved)
		if rec.UserScreenName != "" {
			base = rec.UserScreenName + "/" + base
		}
		entryName := uniqueZIPEntryName(base, usedNames)
		hdr, err := zip.FileInfoHeader(info)
		if err != nil {
			_ = f.Close()
			continue
		}
		hdr.Name = entryName
		hdr.Method = zip.Store // 图片/视频已压缩，Store 零 CPU 开销且更快
		entryWriter, err := zw.CreateHeader(hdr)
		if err != nil {
			_ = f.Close()
			continue
		}
		n, _ := io.Copy(entryWriter, f)
		totalWritten += n
		_ = f.Close()
	}
}

func uniqueZIPEntryName(name string, used map[string]int) string {
	lower := strings.ToLower(name)
	count := used[lower]
	used[lower] = count + 1
	if count == 0 {
		return name
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	return fmt.Sprintf("%s_%d%s", stem, count, ext)
}

func resolvePathWithinRoots(target string, roots []string) (string, bool) {
	if strings.TrimSpace(target) == "" {
		return "", false
	}
	absTarget, err := filepath.Abs(target)
	if err != nil {
		return "", false
	}
	cleanTarget := filepath.Clean(absTarget)
	for _, root := range roots {
		if strings.TrimSpace(root) == "" {
			continue
		}
		absRoot, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(filepath.Clean(absRoot), cleanTarget)
		if err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			return cleanTarget, true
		}
	}
	return "", false
}

func (s *Server) exportTweets(w http.ResponseWriter, r *http.Request) {
	filter, err := parseTweetFilterFromRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	if format == "" {
		format = "markdown"
	}
	if format != "markdown" && format != "html" && format != "json" {
		writeError(w, http.StatusBadRequest, storage.ErrValidation("导出格式仅支持 markdown、html 或 json"))
		return
	}

	allItems := []storage.ArchivedTweet{}
	filter.PageSize = 100
	for pageNum := 1; len(allItems) < maxExportTweets; pageNum++ {
		filter.Page = pageNum
		page, err := s.store.ListTweets(r.Context(), filter)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		allItems = append(allItems, page.Items...)
		if pageNum >= page.Pagination.TotalPages || len(page.Items) == 0 {
			break
		}
	}
	if len(allItems) > maxExportTweets {
		allItems = allItems[:maxExportTweets]
	}
	s.enrichTweetsDownloads(r, allItems)
	stamp := time.Now().UTC().Format("2006-01-02")

	switch format {
	case "json":
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="open-xdownload-tweets-%s.json"`, stamp))
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(allItems)
	case "markdown":
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="open-xdownload-tweets-%s.md"`, stamp))
		_, _ = io.WriteString(w, renderTweetsMarkdown(allItems))
	case "html":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="open-xdownload-tweets-%s.html"`, stamp))
		_, _ = io.WriteString(w, renderTweetsHTML(allItems))
	}
}

func renderTweetsMarkdown(tweets []storage.ArchivedTweet) string {
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("generator: open-Xdownload\n")
	b.WriteString("exported_at: " + time.Now().UTC().Format(time.RFC3339) + "\n")
	b.WriteString("count: " + strconv.Itoa(len(tweets)) + "\n")
	b.WriteString("---\n\n")
	b.WriteString("# X 推文归档导出\n\n")
	for _, tw := range tweets {
		b.WriteString(fmt.Sprintf("## %s (@%s)\n\n", tw.Author.Name, tw.Author.ScreenName))
		b.WriteString(fmt.Sprintf("- **时间**: %s\n", tw.CreatedAt.UTC().Format("2006-01-02 15:04:05 UTC")))
		b.WriteString(fmt.Sprintf("- **链接**: %s\n", tw.URL))
		if tw.Metrics != nil {
			b.WriteString(fmt.Sprintf("- **互动**: 赞 %d · 转发 %d · 回复 %d · 浏览 %d\n", tw.Metrics.Likes, tw.Metrics.Retweets, tw.Metrics.Replies, tw.Metrics.Views))
		}
		if len(tw.Tags) > 0 {
			names := make([]string, len(tw.Tags))
			for i, tg := range tw.Tags {
				names[i] = "#" + tg.Name
			}
			b.WriteString("- **标签**: " + strings.Join(names, " ") + "\n")
		}
		b.WriteString("\n")
		if tw.Text != "" {
			b.WriteString(tw.Text + "\n\n")
		}
		if tw.Article != nil {
			b.WriteString("### " + tw.Article.Title + "\n\n")
			if tw.Article.PlainText != "" {
				b.WriteString(tw.Article.PlainText + "\n\n")
			}
		}
		if tw.Poll != nil && len(tw.Poll.Choices) > 0 {
			b.WriteString("**投票快照**:\n")
			for _, ch := range tw.Poll.Choices {
				b.WriteString(fmt.Sprintf("- %s: %d 票\n", ch.Label, ch.Votes))
			}
			b.WriteString("\n")
		}
		for _, dl := range tw.Downloads {
			b.WriteString(fmt.Sprintf("- 媒体文件: `%s`\n", dl.FilePath))
		}
		b.WriteString("\n---\n\n")
	}
	return b.String()
}

func renderTweetsHTML(tweets []storage.ArchivedTweet) string {
	var b strings.Builder
	b.WriteString(`<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>open-Xdownload 推文归档</title><style>
:root{color-scheme:light dark}body{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif;max-width:780px;margin:0 auto;padding:24px 16px;line-height:1.6}
article{border:1px solid color-mix(in srgb,currentColor 16%,transparent);border-radius:12px;padding:16px 20px;margin-bottom:16px}
header{display:flex;justify-content:space-between;gap:12px;margin-bottom:8px;font-size:14px;opacity:.85}
.body{white-space:pre-wrap;word-break:break-word;margin:8px 0}.meta{font-size:12px;opacity:.7;margin-top:8px}.pill{display:inline-block;padding:2px 8px;border-radius:999px;font-size:12px;border:1px solid color-mix(in srgb,currentColor 20%,transparent);margin-right:6px}
</style></head><body><h1>open-Xdownload 推文归档</h1>`)
	for _, tw := range tweets {
		b.WriteString("<article><header><strong>")
		b.WriteString(html.EscapeString(tw.Author.Name))
		b.WriteString(" @")
		b.WriteString(html.EscapeString(tw.Author.ScreenName))
		b.WriteString("</strong><time>")
		b.WriteString(html.EscapeString(tw.CreatedAt.UTC().Format("2006-01-02 15:04 UTC")))
		b.WriteString("</time></header><div class=\"body\">")
		b.WriteString(html.EscapeString(tw.Text))
		b.WriteString("</div>")
		if tw.Article != nil && (tw.Article.Title != "" || tw.Article.PlainText != "") {
			b.WriteString("<h3>" + html.EscapeString(tw.Article.Title) + "</h3><div class=\"body\">" + html.EscapeString(tw.Article.PlainText) + "</div>")
		}
		if tw.Poll != nil && len(tw.Poll.Choices) > 0 {
			b.WriteString("<ul>")
			for _, ch := range tw.Poll.Choices {
				b.WriteString(fmt.Sprintf("<li>%s (%d)</li>", html.EscapeString(ch.Label), ch.Votes))
			}
			b.WriteString("</ul>")
		}
		b.WriteString("<div class=\"meta\">")
		if tw.Metrics != nil {
			b.WriteString(fmt.Sprintf("❤ %d · ↻ %d · 💬 %d · 👁 %d · ", tw.Metrics.Likes, tw.Metrics.Retweets, tw.Metrics.Replies, tw.Metrics.Views))
		}
		if tw.URL != "" {
			b.WriteString(fmt.Sprintf(`<a href="%s" target="_blank" rel="noreferrer">原推文</a>`, html.EscapeString(tw.URL)))
		}
		b.WriteString("</div></article>")
	}
	b.WriteString("</body></html>")
	return b.String()
}
