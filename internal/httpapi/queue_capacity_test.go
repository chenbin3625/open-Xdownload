package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chenbin3625/open-Xdownload/internal/jobs"
	"github.com/chenbin3625/open-Xdownload/internal/storage"
)

func TestFullJobQueueReturns429WithRetryAfter(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "queue.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	drafts := make([]storage.JobDraft, storage.MaxQueuedJobs)
	for i := range drafts {
		drafts[i] = storage.JobDraft{Kind: storage.JobKindUser, Input: fmt.Sprintf("user-%d", i)}
	}
	if _, err := store.CreateJobs(context.Background(), drafts); err != nil {
		t.Fatal(err)
	}
	manager := jobs.NewManager(store, nil, nil)
	handler := authenticatedRoutes(t, NewServer(store, nil, manager, nil))
	for _, target := range []struct{ path, body string }{
		{"/api/jobs", `{"kind":"tweet_link","input":"https://x.com/user/status/123"}`},
		{"/api/jobs/batch", `{"items":[{"kind":"tweet_link","input":"https://x.com/user/status/124"}]}`},
	} {
		request := newLoopbackRequest(http.MethodPost, target.path, strings.NewReader(target.body))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusTooManyRequests || !strings.Contains(response.Body.String(), "队列已满") {
			t.Fatalf("%s: status=%d body=%s", target.path, response.Code, response.Body.String())
		}
		if response.Header().Get("Retry-After") != "60" {
			t.Fatalf("%s: missing queue retry guidance", target.path)
		}
	}
}
