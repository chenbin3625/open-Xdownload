package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/jobs"
	"github.com/chenbin3625/open-Xdownload/internal/parser"
	"github.com/chenbin3625/open-Xdownload/internal/storage"
	"github.com/chenbin3625/open-Xdownload/internal/xapi"
)

type authFallbackSource struct {
	xapi.Source
	tweet parser.TweetData
	calls int
}

func (s *authFallbackSource) Diagnostics() xapi.PoolDiagnostics {
	return xapi.PoolDiagnostics{Total: 1, Available: 1}
}

func (s *authFallbackSource) FetchTweet(_ context.Context, id string, _ parser.ParseOptions) (parser.TweetData, error) {
	s.calls++
	t := s.tweet
	t.ID = id
	return t, nil
}

func TestParseTweetLinkFallsBackToAuthenticatedGraphQL(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "parse-fallback.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	// Route syndication through a local HTTP proxy returning 404 (simulating protected/NSFW tweet).
	syndication := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer syndication.Close()
	cfg, err := store.GetConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cfg.ProxyURL = syndication.URL
	if _, err := store.UpdateConfig(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}

	bus := jobs.NewEventBus()
	svc := parser.NewService()
	mgr := jobs.NewManager(store, svc, bus)
	fakeSrc := &authFallbackSource{
		tweet: parser.TweetData{
			Text:   "protected post",
			Author: parser.Author{ID: "42", ScreenName: "alice"},
			Media:  []parser.Media{{ID: "m1", Type: parser.MediaPhoto, URL: "https://pbs.twimg.com/media/p1.jpg"}},
		},
	}
	mgr.SetXSourceFactory(func(context.Context, config.AppConfig) (xapi.Source, error) {
		return fakeSrc, nil
	})

	server := NewServer(store, svc, mgr, bus)
	body := bytes.NewBufferString(`{"url":"https://x.com/alice/status/1234567890"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/tweets/parse", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.parseTweetLink(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s, want 200 via authenticated GraphQL fallback", rec.Code, rec.Body.String())
	}
	if fakeSrc.calls != 1 {
		t.Fatalf("FetchTweet calls = %d, want 1", fakeSrc.calls)
	}
	var got parser.TweetData
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if got.ID != "1234567890" || got.Text != "protected post" || len(got.Media) != 1 {
		t.Fatalf("got tweet = %+v", got)
	}
}
