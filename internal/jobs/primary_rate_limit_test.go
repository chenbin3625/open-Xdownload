package jobs

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/storage"
	"github.com/chenbin3625/open-Xdownload/internal/xapi"
)

type limitedPrimarySource struct {
	xapi.Source
	err error
}

func (s limitedPrimarySource) HasPrimary() bool { return true }
func (s limitedPrimarySource) GetUserByIDAsPrimary(context.Context, string) (xapi.User, error) {
	return xapi.User{}, s.err
}

func TestProtectedUserPrimaryRateLimitRequeuesWithoutSkipping(t *testing.T) {
	for _, pacing := range []bool{false, true} {
		name := "rate-limit"
		if pacing {
			name = "pacing"
		}
		t.Run(name, func(t *testing.T) {
			store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			ctx := context.Background()
			job, err := store.CreateJob(ctx, storage.JobKindUser, "alice", "alice")
			if err != nil {
				t.Fatal(err)
			}
			claimed, err := store.ClaimPendingJobs(ctx, 1)
			if err != nil || len(claimed) != 1 {
				t.Fatalf("claim = %+v, %v", claimed, err)
			}
			limited := &xapi.RateLimitedError{
				RetryAt: time.Now().Add(time.Minute), Pacing: pacing,
				Cause: &xapi.HTTPError{StatusCode: http.StatusTooManyRequests},
			}
			manager := NewManager(store, nil, NewEventBus())
			stats, err := manager.archiveUsers(ctx, ctx, claimed[0], config.AppConfig{},
				limitedPrimarySource{err: limited}, []xapi.User{{ID: "42", ScreenName: "alice", Protected: true}}, nil, 0, 1)
			if !errors.Is(err, limited) || stats.Failed != 0 {
				t.Fatalf("stats=%+v error=%v, want a yield without failing the user", stats, err)
			}
			manager.fail(ctx, claimed[0], "", err)
			updated, err := store.GetJob(ctx, job.ID)
			if err != nil || updated.Status != storage.JobPending || updated.AvailableAt == nil {
				t.Fatalf("requeued job = %+v, %v", updated, err)
			}
			if pacing && updated.DeferredSeconds != 0 {
				t.Fatalf("pacing accumulated %d deferred seconds", updated.DeferredSeconds)
			}
		})
	}
}
