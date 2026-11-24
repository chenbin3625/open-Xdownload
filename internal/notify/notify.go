package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/httpx"
	"github.com/chenbin3625/open-Xdownload/internal/storage"
)

// Event 表示一次可推送给外部 Webhook 通道的通知事件。
type Event struct {
	Type      string         `json:"event"`
	Title     string         `json:"title"`
	Message   string         `json:"message"`
	Severity  string         `json:"severity"` // info | success | warning | error
	Timestamp time.Time      `json:"timestamp"`
	Data      map[string]any `json:"data,omitempty"`
}

// Dispatcher 异步调度并投递 Webhook 通知，保证不阻塞任务执行。
type Dispatcher struct {
	store  *storage.Store
	client *http.Client
	queue  chan Event
	mu     sync.Mutex
	closed bool
	wg     sync.WaitGroup
}

func NewDispatcher(store *storage.Store) *Dispatcher {
	d := &Dispatcher{
		store: store,
		queue: make(chan Event, 128),
	}
	d.wg.Add(1)
	go d.worker()
	return d
}

// SetHTTPClient 用于测试替换 HTTP 客户端。
func (d *Dispatcher) SetHTTPClient(client *http.Client) {
	d.mu.Lock()
	d.client = client
	d.mu.Unlock()
}

// Emit 非阻塞入队一条通知事件；队列满或已关闭时静默丢弃。
func (d *Dispatcher) Emit(ev Event) {
	if d == nil || d.store == nil {
		return
	}
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now().UTC()
	}
	if ev.Severity == "" {
		ev.Severity = "info"
	}
	d.mu.Lock()
	closed := d.closed
	d.mu.Unlock()
	if closed {
		return
	}
	select {
	case d.queue <- ev:
	default:
		log.Printf("notify: queue full, dropping event %s", ev.Type)
	}
}

func (d *Dispatcher) Close() {
	if d == nil {
		return
	}
	d.mu.Lock()
	if !d.closed {
		d.closed = true
		close(d.queue)
	}
	d.mu.Unlock()
	d.wg.Wait()
}

func (d *Dispatcher) worker() {
	defer d.wg.Done()
	for ev := range d.queue {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		d.dispatch(ctx, ev)
		cancel()
	}
}

func (d *Dispatcher) dispatch(ctx context.Context, ev Event) {
	webhooks, err := d.store.ListWebhooks(ctx)
	if err != nil || len(webhooks) == 0 {
		return
	}
	proxyURL := ""
	if cfg, err := d.store.GetConfig(ctx); err == nil {
		proxyURL = cfg.ProxyURL
	}
	for _, wh := range webhooks {
		if !wh.Enabled || !webhookSubscribes(wh, ev.Type) {
			continue
		}
		d.deliverOne(ctx, wh, ev, proxyURL)
	}
}

// SendTest 同步向单条 Webhook 发送测试通知，并返回投递结果。
func (d *Dispatcher) SendTest(ctx context.Context, wh storage.Webhook) error {
	proxyURL := ""
	if d.store != nil {
		if cfg, err := d.store.GetConfig(ctx); err == nil {
			proxyURL = cfg.ProxyURL
		}
	}
	ev := Event{
		Type:      "test",
		Title:     "open-Xdownload 测试通知",
		Message:   fmt.Sprintf("通知通道「%s」连通性验证成功。", wh.Name),
		Severity:  "info",
		Timestamp: time.Now().UTC(),
		Data:      map[string]any{"webhookId": wh.ID, "kind": wh.Kind},
	}
	return d.deliverOne(ctx, wh, ev, proxyURL)
}

func webhookSubscribes(wh storage.Webhook, eventType string) bool {
	if eventType == "test" {
		return true
	}
	for _, ev := range wh.Events {
		if ev == eventType || ev == "*" {
			return true
		}
	}
	return false
}

func (d *Dispatcher) httpClient(proxyURL string) *http.Client {
	d.mu.Lock()
	custom := d.client
	d.mu.Unlock()
	if custom != nil {
		return custom
	}
	return httpx.Client(proxyURL, 10*time.Second)
}

func (d *Dispatcher) deliverOne(ctx context.Context, wh storage.Webhook, ev Event, proxyURL string) error {
	req, err := buildWebhookRequest(ctx, wh, ev)
	if err != nil {
		if d.store != nil && wh.ID > 0 {
			_ = d.store.RecordWebhookDelivery(ctx, wh.ID, "failed", err.Error(), time.Now().UTC())
		}
		return err
	}
	client := d.httpClient(proxyURL)
	resp, err := client.Do(req)
	if err != nil {
		if d.store != nil && wh.ID > 0 {
			_ = d.store.RecordWebhookDelivery(ctx, wh.ID, "failed", err.Error(), time.Now().UTC())
		}
		return err
	}
	defer resp.Body.Close()
	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 300 {
		errMsg := fmt.Sprintf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(bodyBytes)))
		if d.store != nil && wh.ID > 0 {
			_ = d.store.RecordWebhookDelivery(ctx, wh.ID, "failed", errMsg, time.Now().UTC())
		}
		return fmt.Errorf("%s", errMsg)
	}
	if d.store != nil && wh.ID > 0 {
		_ = d.store.RecordWebhookDelivery(ctx, wh.ID, "ok", "", time.Now().UTC())
	}
	return nil
}

func buildWebhookRequest(ctx context.Context, wh storage.Webhook, ev Event) (*http.Request, error) {
	targetURL := wh.URL
	var body any
	headers := map[string]string{
		"Content-Type": "application/json; charset=utf-8",
		"User-Agent":   "open-Xdownload-Webhook/1.0",
	}
	summaryText := fmt.Sprintf("[%s] %s\n%s", ev.Type, ev.Title, ev.Message)
	switch wh.Kind {
	case "telegram":
		// 支持 URL 中携带 ?chat_id=...，同时兼容直接写标准 sendMessage 地址
		parsed, err := url.Parse(targetURL)
		if err != nil {
			return nil, err
		}
		chatID := parsed.Query().Get("chat_id")
		payload := map[string]any{
			"text":                     summaryText,
			"disable_web_page_preview": true,
		}
		if chatID != "" {
			payload["chat_id"] = chatID
		}
		body = payload
	case "discord":
		color := 0x2563EB
		switch ev.Severity {
		case "success":
			color = 0x16A34A
		case "warning":
			color = 0xD97706
		case "error":
			color = 0xDC2626
		}
		body = map[string]any{
			"embeds": []map[string]any{{
				"title":       ev.Title,
				"description": ev.Message,
				"color":       color,
				"timestamp":   ev.Timestamp.UTC().Format(time.RFC3339),
				"footer":      map[string]any{"text": "open-Xdownload · " + ev.Type},
			}},
		}
	case "bark":
		body = map[string]any{
			"title": ev.Title,
			"body":  ev.Message,
			"group": "open-Xdownload",
		}
	case "feishu":
		payload := map[string]any{
			"msg_type": "text",
			"content":  map[string]string{"text": summaryText},
		}
		if wh.Secret != "" {
			ts := strconv.FormatInt(time.Now().Unix(), 10)
			mac := hmac.New(sha256.New, []byte(ts+"\n"+wh.Secret))
			payload["timestamp"] = ts
			payload["sign"] = base64.StdEncoding.EncodeToString(mac.Sum(nil))
		}
		body = payload
	case "wecom":
		body = map[string]any{
			"msgtype": "text",
			"text":    map[string]string{"content": summaryText},
		}
	case "dingtalk":
		if wh.Secret != "" {
			ts := strconv.FormatInt(time.Now().UnixMilli(), 10)
			mac := hmac.New(sha256.New, []byte(wh.Secret))
			_, _ = mac.Write([]byte(ts + "\n" + wh.Secret))
			sign := base64.StdEncoding.EncodeToString(mac.Sum(nil))
			if parsed, err := url.Parse(targetURL); err == nil {
				q := parsed.Query()
				q.Set("timestamp", ts)
				q.Set("sign", sign)
				parsed.RawQuery = q.Encode()
				targetURL = parsed.String()
			}
		}
		body = map[string]any{
			"msgtype": "text",
			"text":    map[string]string{"content": summaryText},
		}
	default:
		body = ev
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	if wh.Kind == "generic" && wh.Secret != "" {
		mac := hmac.New(sha256.New, []byte(wh.Secret))
		_, _ = mac.Write(encoded)
		headers["X-XDownload-Signature-256"] = "sha256=" + hex.EncodeToString(mac.Sum(nil))
	}
	headers["X-XDownload-Event"] = ev.Type
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return req, nil
}
