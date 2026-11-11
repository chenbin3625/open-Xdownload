package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRemotePosterTemporaryFailuresUseShortCache(t *testing.T) {
	for _, upstream := range []int{http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusServiceUnavailable, http.StatusGatewayTimeout} {
		t.Run(fmt.Sprint(upstream), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(upstream)
			}))
			defer server.Close()
			target := filepath.Join(t.TempDir(), "poster.jpg")
			err := fetchRemotePoster(context.Background(), server.Client(), make(countingSemaphore, 1), server.URL, target, maxPosterBytes)
			status, message := posterFailureStatus(err)
			if err == nil || status < http.StatusInternalServerError {
				t.Fatalf("upstream %d: error=%v status=%d, want transient failure", upstream, err, status)
			}
			cache := &posterFailureCache{}
			cache.store(target, status, message)
			entry, ok := cache.lookup(target)
			if !ok || time.Until(entry.expiresAt) > posterTransientFailureTTL+time.Second {
				t.Fatalf("transient cache entry = %+v, found=%v", entry, ok)
			}
			if _, err := os.Stat(target); !os.IsNotExist(err) {
				t.Fatalf("failed poster was published: %v", err)
			}
		})
	}
}
