package config

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// CronSchedule 表示一个标准 5 字段 Cron 表达式（分 时 日 月 周）。
// 支持 *、*/n、a-b、a-b/n、逗号列表以及 @hourly/@daily/@weekly/@monthly 宏。
type CronSchedule struct {
	minutes  [60]bool
	hours    [24]bool
	days     [32]bool // 1..31
	months   [13]bool // 1..12
	weekdays [7]bool  // 0..6 (0=Sunday, 7 normalized to 0)
	domWild  bool
	dowWild  bool
}

var cronMacros = map[string]string{
	"@hourly":  "0 * * * *",
	"@daily":   "0 0 * * *",
	"@weekly":  "0 0 * * 0",
	"@monthly": "0 0 1 * *",
}

// ParseCron 解析并校验 5 字段 Cron 表达式。
func ParseCron(expr string) (*CronSchedule, error) {
	trimmed := strings.TrimSpace(expr)
	if trimmed == "" {
		return nil, fmt.Errorf("Cron 表达式不能为空")
	}
	if expanded, ok := cronMacros[strings.ToLower(trimmed)]; ok {
		trimmed = expanded
	}
	fields := strings.Fields(trimmed)
	if len(fields) != 5 {
		return nil, fmt.Errorf("Cron 表达式必须包含 5 个字段（分 时 日 月 周），当前为 %d 个", len(fields))
	}
	s := &CronSchedule{
		domWild: fields[2] == "*",
		dowWild: fields[4] == "*",
	}
	if err := parseCronField(fields[0], 0, 59, false, s.minutes[:]); err != nil {
		return nil, fmt.Errorf("分钟字段无效: %w", err)
	}
	if err := parseCronField(fields[1], 0, 23, false, s.hours[:]); err != nil {
		return nil, fmt.Errorf("小时字段无效: %w", err)
	}
	if err := parseCronField(fields[2], 1, 31, false, s.days[:]); err != nil {
		return nil, fmt.Errorf("日期字段无效: %w", err)
	}
	if err := parseCronField(fields[3], 1, 12, false, s.months[:]); err != nil {
		return nil, fmt.Errorf("月份字段无效: %w", err)
	}
	if err := parseCronField(fields[4], 0, 7, true, s.weekdays[:]); err != nil {
		return nil, fmt.Errorf("星期字段无效: %w", err)
	}
	return s, nil
}

// ValidateCron 校验 Cron 表达式和可选时区。
func ValidateCron(expr, tz string) error {
	if _, err := ParseCron(expr); err != nil {
		return err
	}
	if tz = strings.TrimSpace(tz); tz != "" {
		if _, err := time.LoadLocation(tz); err != nil {
			return fmt.Errorf("无效的时区: %s", tz)
		}
	}
	return nil
}

// NextCronRun 返回 from 之后（严格晚于 from）的下一个触发时间（UTC）。
func NextCronRun(expr, tz string, from time.Time) (time.Time, error) {
	sched, err := ParseCron(expr)
	if err != nil {
		return time.Time{}, err
	}
	loc := time.UTC
	if tz = strings.TrimSpace(tz); tz != "" {
		loaded, err := time.LoadLocation(tz)
		if err != nil {
			return time.Time{}, fmt.Errorf("无效的时区: %s", tz)
		}
		loc = loaded
	}
	next, ok := sched.Next(from.In(loc))
	if !ok {
		return time.Time{}, fmt.Errorf("Cron 表达式在未来 5 年内没有可触发的时间")
	}
	return next.UTC(), nil
}

// Next 返回严格晚于 from 的下一次触发时刻（保持 from 的时区）。
func (s *CronSchedule) Next(from time.Time) (time.Time, bool) {
	loc := from.Location()
	t := from.Truncate(time.Minute).Add(time.Minute)
	limit := t.AddDate(5, 0, 0)
	for !t.After(limit) {
		if !s.months[int(t.Month())] {
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, loc)
			continue
		}
		if !s.matchesDay(t) {
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, loc)
			continue
		}
		if !s.hours[t.Hour()] {
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, loc)
			continue
		}
		if !s.minutes[t.Minute()] {
			t = t.Add(time.Minute)
			continue
		}
		return t, true
	}
	return time.Time{}, false
}

func (s *CronSchedule) matchesDay(t time.Time) bool {
	domMatch := s.days[t.Day()]
	dowMatch := s.weekdays[int(t.Weekday())]
	// 标准 Vixie cron 语义：当日与周同时非通配时取并集，否则取交集。
	if !s.domWild && !s.dowWild {
		return domMatch || dowMatch
	}
	return domMatch && dowMatch
}

func parseCronField(raw string, minVal, maxVal int, wrapSunday bool, target []bool) error {
	if raw == "" {
		return fmt.Errorf("不能为空")
	}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return fmt.Errorf("包含空项")
		}
		step := 1
		rangePart := part
		if strings.Contains(part, "/") {
			pieces := strings.Split(part, "/")
			if len(pieces) != 2 || pieces[1] == "" {
				return fmt.Errorf("步长语法无效: %s", part)
			}
			parsedStep, err := strconv.Atoi(pieces[1])
			if err != nil || parsedStep <= 0 {
				return fmt.Errorf("步长必须为正整数: %s", part)
			}
			step = parsedStep
			rangePart = pieces[0]
		}
		var start, end int
		switch {
		case rangePart == "*":
			start = minVal
			end = maxVal
			if wrapSunday && end == 7 {
				end = 6
			}
		case strings.Contains(rangePart, "-"):
			bounds := strings.Split(rangePart, "-")
			if len(bounds) != 2 {
				return fmt.Errorf("区间语法无效: %s", part)
			}
			a, err := strconv.Atoi(bounds[0])
			if err != nil {
				return fmt.Errorf("起始值无效: %s", part)
			}
			b, err := strconv.Atoi(bounds[1])
			if err != nil {
				return fmt.Errorf("结束值无效: %s", part)
			}
			if a < minVal || b > maxVal || a > b {
				return fmt.Errorf("区间超出范围 [%d-%d]: %s", minVal, maxVal, part)
			}
			start, end = a, b
		default:
			v, err := strconv.Atoi(rangePart)
			if err != nil || v < minVal || v > maxVal {
				return fmt.Errorf("数值超出范围 [%d-%d]: %s", minVal, maxVal, part)
			}
			start = v
			if strings.Contains(part, "/") {
				end = maxVal
				if wrapSunday && end == 7 {
					end = 6
				}
			} else {
				end = v
			}
		}
		for v := start; v <= end; v += step {
			idx := v
			if wrapSunday && idx == 7 {
				idx = 0
			}
			if idx >= 0 && idx < len(target) {
				target[idx] = true
			}
		}
	}
	anySet := false
	for _, set := range target {
		if set {
			anySet = true
			break
		}
	}
	if !anySet {
		return fmt.Errorf("未匹配任何有效值")
	}
	return nil
}
