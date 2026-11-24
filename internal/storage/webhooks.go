package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/config"
)

// Webhook 定义一条外部推送通知通道。
type Webhook struct {
	ID         int64        `json:"id" db:"id"`
	Name       string       `json:"name" db:"name"`
	Kind       string       `json:"kind" db:"kind"`
	URL        string       `json:"url" db:"url"`
	Secret     string       `json:"secret,omitempty" db:"secret"`
	EventsJSON string       `json:"-" db:"events_json"`
	Events     []string     `json:"events" db:"-"`
	Enabled    bool         `json:"enabled" db:"enabled"`
	LastStatus string       `json:"lastStatus,omitempty" db:"last_status"`
	LastError  string       `json:"lastError,omitempty" db:"last_error"`
	LastSentAt sql.NullTime `json:"-" db:"last_sent_at"`
	LastSent   *time.Time   `json:"lastSentAt,omitempty" db:"-"`
	CreatedAt  time.Time    `json:"createdAt" db:"created_at"`
	UpdatedAt  time.Time    `json:"updatedAt" db:"updated_at"`
}

var ValidWebhookKinds = map[string]bool{
	"generic":  true,
	"telegram": true,
	"discord":  true,
	"bark":     true,
	"feishu":   true,
	"wecom":    true,
	"dingtalk": true,
}

var ValidWebhookEvents = map[string]bool{
	"job.completed":             true,
	"job.failed":                true,
	"job.completed_with_errors": true,
	"schedule.ran":              true,
	"account.disabled":          true,
	"account.rate_limited":      true,
}

func (w Webhook) Redacted() Webhook {
	if strings.TrimSpace(w.Secret) != "" {
		w.Secret = config.SecretPlaceholder
	}
	w.URL = config.RedactURLUserinfo(w.URL)
	return w
}

func hydrateWebhook(w Webhook) Webhook {
	if strings.TrimSpace(w.EventsJSON) != "" {
		_ = json.Unmarshal([]byte(w.EventsJSON), &w.Events)
	}
	if w.Events == nil {
		w.Events = []string{}
	}
	if w.LastSentAt.Valid {
		v := w.LastSentAt.Time.UTC()
		w.LastSent = &v
	}
	return w
}

func validateWebhook(w Webhook) (Webhook, error) {
	w.Name = strings.TrimSpace(w.Name)
	if w.Name == "" {
		return Webhook{}, ErrValidation("通知通道名称不能为空")
	}
	w.Kind = strings.ToLower(strings.TrimSpace(w.Kind))
	if w.Kind == "" {
		w.Kind = "generic"
	}
	if !ValidWebhookKinds[w.Kind] {
		return Webhook{}, ErrValidation(fmt.Sprintf("不支持的通知通道类型: %s", w.Kind))
	}
	w.URL = strings.TrimSpace(w.URL)
	if w.URL == "" {
		return Webhook{}, ErrValidation("Webhook 地址不能为空")
	}
	parsed, err := url.Parse(w.URL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
		return Webhook{}, ErrValidation("Webhook 地址必须为有效的 http(s) URL")
	}
	events := make([]string, 0, len(w.Events))
	seen := map[string]bool{}
	for _, ev := range w.Events {
		ev = strings.TrimSpace(ev)
		if ev == "" || seen[ev] {
			continue
		}
		if !ValidWebhookEvents[ev] {
			return Webhook{}, ErrValidation(fmt.Sprintf("不支持的通知事件: %s", ev))
		}
		seen[ev] = true
		events = append(events, ev)
	}
	if len(events) == 0 {
		events = []string{"job.failed", "account.disabled", "account.rate_limited"}
	}
	rawEvents, err := json.Marshal(events)
	if err != nil {
		return Webhook{}, err
	}
	w.Events = events
	w.EventsJSON = string(rawEvents)
	return w, nil
}

func (s *Store) migrateWebhooks() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS webhooks (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  kind TEXT NOT NULL DEFAULT 'generic',
  url TEXT NOT NULL,
  secret TEXT NOT NULL DEFAULT '',
  events_json TEXT NOT NULL DEFAULT '[]',
  enabled INTEGER NOT NULL DEFAULT 1,
  last_status TEXT NOT NULL DEFAULT '',
  last_error TEXT NOT NULL DEFAULT '',
  last_sent_at DATETIME,
  created_at DATETIME NOT NULL,
  updated_at DATETIME NOT NULL
);
`)
	return err
}

func (s *Store) ListWebhooks(ctx context.Context) ([]Webhook, error) {
	items := []Webhook{}
	if err := s.db.SelectContext(ctx, &items, `SELECT * FROM webhooks ORDER BY id ASC`); err != nil {
		return nil, err
	}
	for i := range items {
		items[i] = hydrateWebhook(items[i])
	}
	return items, nil
}

func (s *Store) GetWebhook(ctx context.Context, id int64) (Webhook, error) {
	var item Webhook
	err := s.db.GetContext(ctx, &item, `SELECT * FROM webhooks WHERE id = ?`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return Webhook{}, ErrNotFound("通知通道不存在", err)
	}
	if err != nil {
		return Webhook{}, err
	}
	return hydrateWebhook(item), nil
}

func (s *Store) CreateWebhook(ctx context.Context, input Webhook) (Webhook, error) {
	validated, err := validateWebhook(input)
	if err != nil {
		return Webhook{}, err
	}
	now := formatSortableTime(time.Now().UTC())
	var created Webhook
	err = s.db.GetContext(ctx, &created, `
INSERT INTO webhooks(name, kind, url, secret, events_json, enabled, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?) RETURNING *`,
		validated.Name, validated.Kind, validated.URL, validated.Secret, validated.EventsJSON, validated.Enabled, now, now)
	if err != nil {
		return Webhook{}, err
	}
	return hydrateWebhook(created), nil
}

func (s *Store) UpdateWebhook(ctx context.Context, id int64, input Webhook) (Webhook, error) {
	current, err := s.GetWebhook(ctx, id)
	if err != nil {
		return Webhook{}, err
	}
	// 占位符还原：保留既有密钥或 URL 凭据
	input.URL = config.RestoreURLUserinfo(input.URL, current.URL)
	switch input.Secret {
	case "", config.SecretPlaceholder:
		input.Secret = current.Secret
	case config.ClearSentinel:
		input.Secret = ""
	}
	validated, err := validateWebhook(input)
	if err != nil {
		return Webhook{}, err
	}
	now := formatSortableTime(time.Now().UTC())
	var updated Webhook
	err = s.db.GetContext(ctx, &updated, `
UPDATE webhooks SET name = ?, kind = ?, url = ?, secret = ?, events_json = ?, enabled = ?, updated_at = ?
WHERE id = ? RETURNING *`,
		validated.Name, validated.Kind, validated.URL, validated.Secret, validated.EventsJSON, validated.Enabled, now, id)
	if err != nil {
		return Webhook{}, err
	}
	return hydrateWebhook(updated), nil
}

func (s *Store) DeleteWebhook(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM webhooks WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound("通知通道不存在", sql.ErrNoRows)
	}
	return nil
}

func (s *Store) RecordWebhookDelivery(ctx context.Context, id int64, status, errMsg string, sentAt time.Time) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE webhooks SET last_status = ?, last_error = ?, last_sent_at = ?, updated_at = ? WHERE id = ?`,
		status, errMsg, formatSortableTime(sentAt.UTC()), formatSortableTime(time.Now().UTC()), id)
	return err
}
