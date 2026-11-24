package httpapi

import (
	"encoding/xml"
	"fmt"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
	"time"
)

type rssFeed struct {
	XMLName xml.Name   `xml:"rss"`
	Version string     `xml:"version,attr"`
	Channel rssChannel `xml:"channel"`
}

type rssChannel struct {
	Title         string    `xml:"title"`
	Link          string    `xml:"link"`
	Description   string    `xml:"description"`
	LastBuildDate string    `xml:"lastBuildDate"`
	Items         []rssItem `xml:"item"`
}

type rssItem struct {
	Title       string        `xml:"title"`
	Link        string        `xml:"link"`
	GUID        string        `xml:"guid"`
	PubDate     string        `xml:"pubDate"`
	Author      string        `xml:"author,omitempty"`
	Description string        `xml:"description"`
	Enclosure   *rssEnclosure `xml:"enclosure,omitempty"`
}

type rssEnclosure struct {
	URL    string `xml:"url,attr"`
	Length int64  `xml:"length,attr"`
	Type   string `xml:"type,attr"`
}

func (s *Server) serveTweetRSS(w http.ResponseWriter, r *http.Request) {
	filter, err := parseTweetFilterFromRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	limit := parsePositiveInt(r, "limit", 50, 1, 100)
	filter.Page = 1
	filter.PageSize = limit
	page, err := s.store.ListTweets(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.enrichTweetsDownloads(r, page.Items)

	scheme := "http"
	if r.TLS != nil || strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https") {
		scheme = "https"
	}
	origin := fmt.Sprintf("%s://%s", scheme, r.Host)
	title := "open-Xdownload 推文归档"
	if filter.Author != "" {
		title = fmt.Sprintf("@%s · open-Xdownload", strings.TrimPrefix(filter.Author, "@"))
	}
	items := make([]rssItem, 0, len(page.Items))
	for _, tw := range page.Items {
		summary := tw.Text
		if tw.Article != nil && tw.Article.Title != "" {
			summary = tw.Article.Title + "\n\n" + tw.Article.PlainText
		}
		itemTitle := fmt.Sprintf("@%s: %s", tw.Author.ScreenName, truncateRunes(strings.ReplaceAll(summary, "\n", " "), 80))
		link := tw.URL
		if link == "" {
			link = origin + "/tweets?id=" + tw.ID
		}
		item := rssItem{
			Title:       itemTitle,
			Link:        link,
			GUID:        "open-xdownload:tweet:" + tw.ID,
			PubDate:     tw.CreatedAt.UTC().Format(time.RFC1123Z),
			Author:      tw.Author.ScreenName,
			Description: summary,
		}
		if len(tw.Downloads) > 0 {
			dl := tw.Downloads[0]
			ct := mime.TypeByExtension(strings.ToLower(filepath.Ext(dl.FilePath)))
			if ct == "" {
				ct = "application/octet-stream"
			}
			item.Enclosure = &rssEnclosure{
				URL:    origin + "/api/library/downloads/" + fmt.Sprintf("%d", dl.ID) + "/file",
				Length: dl.Bytes,
				Type:   ct,
			}
		}
		items = append(items, item)
	}
	feed := rssFeed{
		Version: "2.0",
		Channel: rssChannel{
			Title:         title,
			Link:          origin + "/tweets",
			Description:   "Local-first X / Twitter archive feed from open-Xdownload",
			LastBuildDate: time.Now().UTC().Format(time.RFC1123Z),
			Items:         items,
		},
	}
	w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
	_, _ = w.Write([]byte(xml.Header))
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	_ = enc.Encode(feed)
}

func truncateRunes(s string, maxLen int) string {
	runes := []rune(strings.TrimSpace(s))
	if len(runes) <= maxLen {
		return string(runes)
	}
	return string(runes[:maxLen]) + "…"
}
