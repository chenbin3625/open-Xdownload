package xapi

import (
	"os"
	"strings"
)

// defaultGuestBearer 是 X 网页端公开的 Bearer（公开已知常量），与 xclient 使用同一个值。
const defaultGuestBearer = "AAAAAAAAAAAAAAAAAAAAANRILgAAAAAAnNwIzUejRCOuH5E6I8xnZz4puTs%3D1Zv7ttfk8LF81IUq16cHjhLTvJu4FA33AGWWjCpTnA"

// GuestBearer 返回请求 X 时使用的 Bearer。环境变量 OPEN_XDOWNLOAD_GUEST_BEARER 可覆盖，
// 官方 token 变更时无需改代码即可轮换。
func GuestBearer() string {
	if value := strings.TrimSpace(os.Getenv("OPEN_XDOWNLOAD_GUEST_BEARER")); value != "" {
		return value
	}
	return defaultGuestBearer
}
