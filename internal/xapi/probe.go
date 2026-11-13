package xapi

import "context"

// HomeProbeResult 是一次账号登录校验的结果。
//
// 名字沿用早期设计里的"/home 校验"。X 现在对失效 Cookie 的 /home 返回 307 跳回
// 首页并清掉 auth_token，而不是 401，无法据此确认失效；xscrape 的实现改为请求
// /i/api/1.1/account/settings.json：失效 Cookie 返回 HTTP 401 + 错误码 32，有效
// Cookie 返回带 screen_name 的 JSON。
//
// 调用方按以下顺序判断：
//   - Unauthorized 为 true：X 明确拒绝这个账号（HTTP 401，或错误码 32/64/89/215/326：
//     Cookie 失效、账号被封禁或锁定），Err 说明原因（可直接写入 error_msg）；
//   - 否则 Err 非 nil：无法判断（网络、WAF、限流、响应格式变化），不能据此停用账号；
//   - 否则校验成功，ScreenName 为当前登录账号。
type HomeProbeResult struct {
	ScreenName   string
	Unauthorized bool
	Err          error
}

// AccountCredentials 是一次探测所需的凭据。Proxy 与 UserAgent 由调用方解析好：
// 账号没有单独配置代理时传入全局代理；UserAgent 为空时实现使用默认 UA。
type AccountCredentials struct {
	AuthToken, CSRFToken string
	ExtraCookies         map[string]string
	UserAgent, Proxy     string
}

// HomeProber 校验一组账号凭据。实现只发起探测请求，不修改账号表或账号池状态；
// 根据结果更新账号状态是调用方的责任。
type HomeProber func(ctx context.Context, creds AccountCredentials) HomeProbeResult
