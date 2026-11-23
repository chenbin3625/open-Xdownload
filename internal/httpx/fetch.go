package httpx

import (
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
)

// ErrBodyTooLarge 表示响应体超过调用方给定的上限。
var ErrBodyTooLarge = errors.New("响应体超过大小上限")

// CopyLimited 最多从 src 复制 limit 字节到 dst；src 超过 limit 时返回 ErrBodyTooLarge。
// 与 io.LimitReader 不同，超限不会被静默截断成"成功"：截断的图片若被当成完整文件发布，
// 之后会因文件已存在而被永久沿用。
func CopyLimited(dst io.Writer, src io.Reader, limit int64) (int64, error) {
	if limit < 0 {
		return 0, ErrBodyTooLarge
	}
	readLimit := limit + 1
	if limit == math.MaxInt64 {
		readLimit = math.MaxInt64
	}
	n, err := io.Copy(dst, io.LimitReader(src, readLimit))
	if err != nil {
		return n, err
	}
	if n > limit {
		return n, ErrBodyTooLarge
	}
	return n, nil
}

// RestrictRedirects 让 client 只跟随到 allowedHost 接受的主机的重定向。初始 URL 的主机
// 由调用方自行校验；不加这层限制时，一次 302 就能把请求带到任意主机（链路本地地址由
// 拨号防护兜底，但内网与第三方主机不在其列）。
func RestrictRedirects(client *http.Client, allowedHost func(host string) bool) {
	if client == nil {
		return
	}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("重定向次数过多")
		}
		if req == nil || req.URL == nil {
			return errors.New("重定向目标无效")
		}
		if !strings.EqualFold(req.URL.Scheme, "http") && !strings.EqualFold(req.URL.Scheme, "https") {
			return fmt.Errorf("拒绝非 HTTP 重定向: %s", req.URL.Scheme)
		}
		if strings.EqualFold(req.URL.Scheme, "http") {
			for _, prev := range via {
				if prev != nil && prev.URL != nil && strings.EqualFold(prev.URL.Scheme, "https") {
					return fmt.Errorf("拒绝 HTTPS 降级重定向: %s", req.URL)
				}
			}
		}
		if allowedHost == nil || !allowedHost(req.URL.Hostname()) {
			return fmt.Errorf("拒绝重定向到 %s", req.URL.Hostname())
		}
		return nil
	}
}
