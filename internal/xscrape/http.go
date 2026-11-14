package xscrape

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/httpx"
)

// requestTimeout 是单个 X 请求的总超时，与 legacy 一致。
const requestTimeout = 90 * time.Second

// maxResponseBytes 是响应体上限，与 legacy 一致。
const maxResponseBytes = 32 << 20

var errFetchGuestToken = errors.New("获取 X guest token 失败")

func newHTTPClient(transport http.RoundTripper) *http.Client {
	return &http.Client{Transport: transport, Timeout: requestTimeout, CheckRedirect: checkRedirect}
}

// sameOrigin 比较 scheme、主机名和实际端口，判断两个 URL 是否同源。
func sameOrigin(a, b *url.URL) bool {
	if a == nil || b == nil || !strings.EqualFold(a.Scheme, b.Scheme) || !strings.EqualFold(a.Hostname(), b.Hostname()) {
		return false
	}
	port := func(u *url.URL) string {
		if p := u.Port(); p != "" {
			return p
		}
		if strings.EqualFold(u.Scheme, "https") {
			return "443"
		}
		if strings.EqualFold(u.Scheme, "http") {
			return "80"
		}
		return ""
	}
	return port(a) == port(b)
}

// checkRedirect 与 legacy 相同：拒绝 HTTPS 降级、跨源重定向和超过 10 次跳转。
// X-Csrf-Token 是自定义头，标准库不会在跨域重定向时剥离，必须在这里拦下。
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("重定向次数过多")
	}
	if len(via) > 0 && via[0].URL.Scheme == "https" && req.URL.Scheme == "http" {
		return fmt.Errorf("拒绝 HTTPS 降级重定向: %s", req.URL.Scheme)
	}
	if len(via) > 0 && !sameOrigin(req.URL, via[0].URL) {
		return fmt.Errorf("拒绝跨域重定向: %s", req.URL.Host)
	}
	return nil
}

// response 是一次 HTTP 往返的结果。网络错误、超时与响应体超限通过 err 返回；
// HTTP 4xx/5xx 仍走 status/payload，由调用方按响应表处置。
type response struct {
	status  int
	header  http.Header
	payload []byte
	err     error
}

func (r *response) contentType() string { return r.header.Get("Content-Type") }

// bodyTooLarge 报告 err 是否是响应体超限。
func bodyTooLarge(err error) bool { return errors.Is(err, httpx.ErrBodyTooLarge) }

// applyTransactionID 为请求附加 x-client-transaction-id。生成失败只记录限频日志并
// 省略该头：不能让一次 xtid 抓取失败阻断所有请求。
func (e *Engine) applyTransactionID(ctx context.Context, request *http.Request, generator TransactionIDGenerator, method, path string) {
	if generator == nil {
		return
	}
	transactionID, err := generator.Generate(ctx, method, path)
	if err != nil {
		e.logs.printf("xtid", 5*time.Minute, "xscrape: 生成 x-client-transaction-id 失败，本次请求省略该头: %v", err)
		return
	}
	if transactionID != "" {
		request.Header.Set("x-client-transaction-id", transactionID)
	}
}

// invalidateTransactionIDs 让下次请求重新抓取 xtid 脚本（GraphQL 404 时）。
func (e *Engine) invalidateTransactionIDs() {
	if generator := e.currentGenerator(); generator != nil {
		generator.Invalidate()
	}
}

func doRequest(client *http.Client, request *http.Request) *response {
	reply, err := client.Do(request)
	if err != nil {
		return &response{err: err}
	}
	defer reply.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(reply.Body, maxResponseBytes+1))
	if err != nil {
		return &response{err: err}
	}
	if len(payload) > maxResponseBytes {
		return &response{err: httpx.ErrBodyTooLarge}
	}
	return &response{status: reply.StatusCode, header: reply.Header, payload: payload}
}
