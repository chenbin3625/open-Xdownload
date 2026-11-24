package config

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ArchiveFilter 定义按任务或全局生效的归档过滤规则。
// 空值 / 零值字段表示不做限制，完全向后兼容既有任务和配置。
type ArchiveFilter struct {
	// MediaTypes 限制下载的媒体类型，可选值："photo"、"video"、"animated_gif"。空切片表示全部允许。
	MediaTypes []string `json:"mediaTypes,omitempty"`
	// ExcludeRetweets 为 true 时跳过转推（retweet）。
	ExcludeRetweets bool `json:"excludeRetweets,omitempty"`
	// ExcludeReplies 为 true 时跳过回复推文（in_reply_to 非空）。
	ExcludeReplies bool `json:"excludeReplies,omitempty"`
	// MinBytes 跳过小于该字节数的媒体文件（0 表示不限）。
	MinBytes int64 `json:"minBytes,omitempty"`
	// MinWidth / MinHeight 跳过分辨率低于该值的图片/视频（0 表示不限；元数据缺失时不误杀）。
	MinWidth  int `json:"minWidth,omitempty"`
	MinHeight int `json:"minHeight,omitempty"`
	// SinceDate / UntilDate 限制推文发布日期范围（YYYY-MM-DD，UTC，闭区间）。
	SinceDate string `json:"sinceDate,omitempty"`
	UntilDate string `json:"untilDate,omitempty"`
}

// IsEmpty 报告过滤规则是否未启用任何条件。
func (f *ArchiveFilter) IsEmpty() bool {
	if f == nil {
		return true
	}
	return len(f.MediaTypes) == 0 &&
		!f.ExcludeRetweets &&
		!f.ExcludeReplies &&
		f.MinBytes <= 0 &&
		f.MinWidth <= 0 &&
		f.MinHeight <= 0 &&
		strings.TrimSpace(f.SinceDate) == "" &&
		strings.TrimSpace(f.UntilDate) == ""
}

// NormalizeAndValidate 规范化并校验过滤规则。
func (f *ArchiveFilter) NormalizeAndValidate() (*ArchiveFilter, error) {
	if f == nil || f.IsEmpty() {
		return nil, nil
	}
	out := &ArchiveFilter{
		ExcludeRetweets: f.ExcludeRetweets,
		ExcludeReplies:  f.ExcludeReplies,
		MinBytes:        f.MinBytes,
		MinWidth:        f.MinWidth,
		MinHeight:       f.MinHeight,
		SinceDate:       strings.TrimSpace(f.SinceDate),
		UntilDate:       strings.TrimSpace(f.UntilDate),
	}
	if out.MinBytes < 0 {
		return nil, fmt.Errorf("最小文件体积不能为负数")
	}
	if out.MinWidth < 0 || out.MinHeight < 0 {
		return nil, fmt.Errorf("最小分辨率不能为负数")
	}
	seenTypes := map[string]bool{}
	for _, raw := range f.MediaTypes {
		kind := strings.ToLower(strings.TrimSpace(raw))
		switch kind {
		case "photo", "image":
			kind = "photo"
		case "video":
		case "animated_gif", "gif":
			kind = "animated_gif"
		case "":
			continue
		default:
			return nil, fmt.Errorf("不支持的媒体过滤类型: %s", raw)
		}
		if !seenTypes[kind] {
			seenTypes[kind] = true
			out.MediaTypes = append(out.MediaTypes, kind)
		}
	}
	var sinceTime, untilTime time.Time
	if out.SinceDate != "" {
		t, err := time.Parse("2006-01-02", out.SinceDate)
		if err != nil {
			return nil, fmt.Errorf("开始日期格式无效（应为 YYYY-MM-DD）")
		}
		sinceTime = t
	}
	if out.UntilDate != "" {
		t, err := time.Parse("2006-01-02", out.UntilDate)
		if err != nil {
			return nil, fmt.Errorf("结束日期格式无效（应为 YYYY-MM-DD）")
		}
		untilTime = t
	}
	if !sinceTime.IsZero() && !untilTime.IsZero() && sinceTime.After(untilTime) {
		return nil, fmt.Errorf("开始日期不能晚于结束日期")
	}
	if out.IsEmpty() {
		return nil, nil
	}
	return out, nil
}

// ParseArchiveFilterJSON 解析并校验持久化 JSON；空串返回 (nil, nil)。
func ParseArchiveFilterJSON(raw string) (*ArchiveFilter, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" || raw == "{}" {
		return nil, nil
	}
	var f ArchiveFilter
	if err := json.Unmarshal([]byte(raw), &f); err != nil {
		return nil, fmt.Errorf("归档过滤规则格式无效")
	}
	return f.NormalizeAndValidate()
}

// MarshalArchiveFilter 序列化规范化后的过滤规则；空规则返回 ""。
func MarshalArchiveFilter(f *ArchiveFilter) (string, error) {
	normalized, err := f.NormalizeAndValidate()
	if err != nil {
		return "", err
	}
	if normalized == nil {
		return "", nil
	}
	data, err := json.Marshal(normalized)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// EffectiveArchiveFilter 合并全局规则与任务级规则：任务显式配置了非空过滤时优先使用任务规则，
// 否则回退到全局规则。
func EffectiveArchiveFilter(global, job *ArchiveFilter) *ArchiveFilter {
	if job != nil && !job.IsEmpty() {
		return job
	}
	if global != nil && !global.IsEmpty() {
		return global
	}
	return nil
}

// AllowsMediaType 判断媒体类型是否被允许。
func (f *ArchiveFilter) AllowsMediaType(mediaType string) bool {
	if f == nil || len(f.MediaTypes) == 0 {
		return true
	}
	norm := strings.ToLower(strings.TrimSpace(mediaType))
	if norm == "image" {
		norm = "photo"
	} else if norm == "gif" {
		norm = "animated_gif"
	}
	for _, allowed := range f.MediaTypes {
		if allowed == norm {
			return true
		}
	}
	return false
}

// AllowsDimensions 判断媒体宽高是否满足最小分辨率要求（未知尺寸 0 时放行，避免误杀）。
func (f *ArchiveFilter) AllowsDimensions(width, height int) bool {
	if f == nil {
		return true
	}
	if f.MinWidth > 0 && width > 0 && width < f.MinWidth {
		return false
	}
	if f.MinHeight > 0 && height > 0 && height < f.MinHeight {
		return false
	}
	return true
}

// AllowsPublishedAt 判断推文发布时间是否落在日期过滤范围内。
func (f *ArchiveFilter) AllowsPublishedAt(publishedAt time.Time) bool {
	if f == nil || publishedAt.IsZero() {
		return true
	}
	utc := publishedAt.UTC()
	if f.SinceDate != "" {
		if since, err := time.Parse("2006-01-02", f.SinceDate); err == nil {
			if utc.Before(since) {
				return false
			}
		}
	}
	if f.UntilDate != "" {
		if until, err := time.Parse("2006-01-02", f.UntilDate); err == nil {
			if !utc.Before(until.AddDate(0, 0, 1)) {
				return false
			}
		}
	}
	return true
}
