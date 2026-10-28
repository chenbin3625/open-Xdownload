package jobs

import (
	"encoding/json"
	"log"
	"sync"
	"sync/atomic"
	"time"
)

type Event struct {
	Type      string `json:"type"`
	JobID     int64  `json:"jobId,omitempty"`
	Payload   any    `json:"payload,omitempty"`
	Meta      any    `json:"meta,omitempty"`
	Timestamp string `json:"timestamp"`
	sse       []byte
}

type EventBus struct {
	mu          sync.Mutex
	subscribers map[chan Event]struct{}
	closed      bool
	// dropped 统计因订阅者缓冲满而被丢弃的事件数（前端有轮询对账兜底，这里只做可观测）。
	dropped atomic.Int64
}

// maxEventSubscribers bounds concurrent SSE subscribers so an unauthenticated
// client cannot exhaust memory/goroutines by opening unlimited long-lived
// /api/events connections.
const maxEventSubscribers = 64

func NewEventBus() *EventBus {
	return &EventBus{subscribers: make(map[chan Event]struct{})}
}

// subscriberBuffer 是每个订阅者的 SSE 事件缓冲深度。Manager 以 ≥400ms/活跃任务 的频率
// 发布 job.updated；缓冲溢出时 Publish 丢弃事件（非阻塞发送），前端依赖轮询对账兜底。
// 64 槽把丢弃概率压到只有真正的慢消费者才会触发。
const subscriberBuffer = 64

func (bus *EventBus) Subscribe() (chan Event, bool) {
	bus.mu.Lock()
	defer bus.mu.Unlock()
	if bus.closed || len(bus.subscribers) >= maxEventSubscribers {
		return nil, false
	}
	channel := make(chan Event, subscriberBuffer)
	bus.subscribers[channel] = struct{}{}
	return channel, true
}

func (bus *EventBus) Unsubscribe(channel chan Event) {
	bus.mu.Lock()
	defer bus.mu.Unlock()
	// 只关闭仍登记在册的 channel：CloseAll 可能已经关过它，重复 close 会 panic。
	if _, ok := bus.subscribers[channel]; ok {
		delete(bus.subscribers, channel)
		close(channel)
	}
}

// CloseAll 关闭全部订阅者并拒绝新的订阅，用于进程关停。http.Server.Shutdown 不会
// 取消处理中请求的 ctx，SSE 处理器只能靠 channel 关闭退出；否则只要浏览器开着页面，
// Shutdown 就会阻塞到超时，容器在任务收尾前就被强杀。可重复调用。
func (bus *EventBus) CloseAll() {
	if bus == nil {
		return
	}
	bus.mu.Lock()
	defer bus.mu.Unlock()
	bus.closed = true
	for channel := range bus.subscribers {
		delete(bus.subscribers, channel)
		close(channel)
	}
}

func (bus *EventBus) Publish(event Event) {
	if bus == nil {
		return
	}
	if event.Timestamp == "" {
		event.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	}
	event.sse = marshalSSE(event)
	bus.mu.Lock()
	defer bus.mu.Unlock()
	for channel := range bus.subscribers {
		select {
		case channel <- event:
		default:
			// 慢消费者（SSE 客户端）丢事件是设计内的：前端靠轮询对账。但静默丢弃会让
			// "首尾事件都丢了"这类问题无法排查，因此计数 + 每 1000 条限频日志。
			if dropped := bus.dropped.Add(1); dropped%1000 == 1 {
				log.Printf("event bus: dropped %d event(s) for slow subscriber(s) (latest type=%s)", dropped, event.Type)
			}
		}
	}
}

// DroppedEvents 返回累计丢弃的事件数（用于测试与诊断）。
func (bus *EventBus) DroppedEvents() int64 {
	if bus == nil {
		return 0
	}
	return bus.dropped.Load()
}

func (event Event) MarshalSSE() []byte {
	if len(event.sse) > 0 {
		return event.sse
	}
	return marshalSSE(event)
}

func marshalSSE(event Event) []byte {
	payload, err := json.Marshal(event)
	if err != nil {
		return []byte(": encode-error\n\n")
	}
	out := make([]byte, 0, 8+len(payload))
	out = append(out, "data: "...)
	out = append(out, payload...)
	out = append(out, '\n', '\n')
	return out
}
