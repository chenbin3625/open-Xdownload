package jobs

import (
	"strings"
	"testing"

	"github.com/chenbin3625/open-Xdownload/internal/storage"
)

func TestEventMarshalSSEIncludesMetaOnce(t *testing.T) {
	t.Parallel()
	event := Event{
		Type:    "job.updated",
		JobID:   7,
		Payload: storage.Job{ID: 7, Status: storage.JobCompleted},
		Meta: storage.DashboardMetaView{
			Stats:            storage.JobStats{Total: 3, Completed: 1},
			FailedTweetCount: 2,
		},
		Timestamp: "2024-01-01T00:00:00Z",
	}
	got := string(event.MarshalSSE())
	if !strings.HasPrefix(got, "data: ") || !strings.HasSuffix(got, "\n\n") {
		t.Fatalf("frame = %q", got)
	}
	if strings.Count(got, `"failedTweetCount":2`) != 1 {
		t.Fatalf("expected meta once: %s", got)
	}
	if strings.Contains(got, "data: data:") {
		t.Fatalf("double prefix: %s", got)
	}
}

func TestEventBusPublishNilReceiver(t *testing.T) {
	t.Parallel()
	var bus *EventBus
	bus.Publish(Event{Type: "job.created"})
}

func TestEventBusPublishCachesSSEFrame(t *testing.T) {
	bus := NewEventBus()
	channel, ok := bus.Subscribe()
	if !ok {
		t.Fatal("subscribe failed")
	}
	defer bus.Unsubscribe(channel)

	bus.Publish(Event{Type: "job.updated", JobID: 7})
	published := <-channel
	if len(published.sse) == 0 {
		t.Fatal("publish should cache the encoded SSE frame")
	}
	frame := published.MarshalSSE()
	if len(frame) == 0 || &frame[0] != &published.sse[0] {
		t.Fatal("MarshalSSE should reuse the cached frame")
	}
}
