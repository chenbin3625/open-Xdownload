package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/chenbin3625/open-Xdownload/internal/storage"
)

func TestDispatcherDeliversSignedGenericAndDiscordWebhooks(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	var mu sync.Mutex
	var received []struct {
		event string
		sig   string
		body  string
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		received = append(received, struct {
			event string
			sig   string
			body  string
		}{r.Header.Get("X-XDownload-Event"), r.Header.Get("X-XDownload-Signature-256"), string(raw)})
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	wh1, err := store.CreateWebhook(ctx, storage.Webhook{
		Name: "Generic", Kind: "generic", URL: srv.URL + "/generic", Secret: "topsecret",
		Events: []string{"account.disabled"}, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	wh2, err := store.CreateWebhook(ctx, storage.Webhook{
		Name: "Discord", Kind: "discord", URL: srv.URL + "/discord",
		Events: []string{"account.disabled"}, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	disp := NewDispatcher(store)
	disp.SetHTTPClient(srv.Client())
	if err := disp.SendTest(ctx, wh1); err != nil {
		t.Fatalf("SendTest failed: %v", err)
	}
	disp.Emit(Event{Type: "account.disabled", Title: "Cookie 失效", Message: "@alice 已停用", Severity: "error"})
	disp.Close()

	mu.Lock()
	defer mu.Unlock()
	if len(received) != 3 {
		t.Fatalf("received %d requests, want 3", len(received))
	}
	if !strings.HasPrefix(received[0].sig, "sha256=") {
		t.Fatalf("missing generic signature: %+v", received[0])
	}
	var discordBody map[string]any
	if err := json.Unmarshal([]byte(received[2].body), &discordBody); err != nil || discordBody["embeds"] == nil {
		t.Fatalf("invalid discord payload: %s", received[2].body)
	}
	updated, err := store.GetWebhook(ctx, wh2.ID)
	if err != nil || updated.LastStatus != "ok" {
		t.Fatalf("webhook delivery not recorded: %+v err=%v", updated, err)
	}
}
