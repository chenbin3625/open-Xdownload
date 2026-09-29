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

func TestEventBusCloseAllEndsSubscribersAndRejectsNewOnes(t *testing.T) {
	bus := NewEventBus()
	first, ok := bus.Subscribe()
	if !ok {
		t.Fatal("subscribe")
	}
	second, ok := bus.Subscribe()
	if !ok {
		t.Fatal("subscribe")
	}

	// 关停时 http.Server.Shutdown 不会取消处理中请求的 ctx：SSE 处理器只能靠 channel
	// 关闭退出，否则 Shutdown 必然阻塞到超时。
	bus.CloseAll()
	for _, channel := range []chan Event{first, second} {
		if _, open := <-channel; open {
			t.Fatal("subscriber channel should be closed by CloseAll")
		}
	}
	// 处理器退出时的 defer Unsubscribe 不能对已关闭的 channel 再 close 一次（panic）。
	bus.Unsubscribe(first)
	bus.Unsubscribe(second)
	bus.CloseAll()
	bus.Publish(Event{Type: "job.updated"})

	if _, ok := bus.Subscribe(); ok {
		t.Fatal("Subscribe after CloseAll should be rejected")
	}
}
