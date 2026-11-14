package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/httpx"
	"github.com/chenbin3625/open-Xdownload/internal/jobs"
	"github.com/chenbin3625/open-Xdownload/internal/storage"
	"github.com/chenbin3625/open-Xdownload/internal/xapi"
)

const (
	// xAccountCheckTimeout 是检测单个账号的超时。
	xAccountCheckTimeout = 20 * time.Second
	// xAccountCheckConcurrency 是全部检测时同时探测的账号数上限。
	xAccountCheckConcurrency = 4
	// maxXAccountUserAgentRunes 限制账号级 User-Agent 的长度。
	maxXAccountUserAgentRunes = 512
	// maxXAccountCheckErrorRunes 限制写进 error_msg 的检测错误长度。
	maxXAccountCheckErrorRunes = 300
	// xAccountStatsDayLayout 与 storage 的 x_account_stats.day（UTC）格式一致。
	xAccountStatsDayLayout = "2006-01-02"
)

// errXAccountProberUnavailable 表示 cmd/server 还没有注入账号探测函数。
var errXAccountProberUnavailable = &storage.UserFacingError{
	Status:  http.StatusServiceUnavailable,
	Message: "账号检测功能还没有初始化",
}

// xAccountView 是账号的对外视图，字段与前端 XAccountView 一一对应。Cookie 只给出打码值，
// 代理去掉 userinfo。
type xAccountView struct {
	ID              int64                   `json:"id"`
	ScreenName      string                  `json:"screenName"`
	AuthToken       string                  `json:"authToken"`
	CSRFToken       string                  `json:"csrfToken"`
	HasExtraCookies bool                    `json:"hasExtraCookies"`
	UserAgent       string                  `json:"userAgent"`
	Proxy           string                  `json:"proxy"`
	IsPrimary       bool                    `json:"isPrimary"`
	Source          string                  `json:"source"`
	Active          bool                    `json:"active"`
	ErrorMsg        string                  `json:"errorMsg"`
	LastUsed        *string                 `json:"lastUsed"`
	VerifiedAt      *string                 `json:"verifiedAt"`
	CreatedAt       string                  `json:"createdAt"`
	Locks           []xAccountLockView      `json:"locks"`
	RequestsToday   int64                   `json:"requestsToday"`
	RequestsByQueue []xAccountQueueRequests `json:"requestsByQueue"`
}

type xAccountLockView struct {
	Queue  string `json:"queue"`
	Until  string `json:"until"`
	Reason string `json:"reason"`
}

type xAccountQueueRequests struct {
	Queue    string `json:"queue"`
	Requests int64  `json:"requests"`
}

type xAccountsResponse struct {
	Scraper  string         `json:"scraper"`
	Accounts []xAccountView `json:"accounts"`
}

type xAccountCreateRequest struct {
	Cookie    string `json:"cookie"`
	UserAgent string `json:"userAgent"`
	Proxy     string `json:"proxy"`
	IsPrimary bool   `json:"isPrimary"`
}

type xAccountImportRequest struct {
	Text string `json:"text"`
}

type xAccountImportIssue struct {
	Index  int    `json:"index"`
	Reason string `json:"reason"`
}

type xAccountImportResponse struct {
	Created []xAccountView        `json:"created"`
	Issues  []xAccountImportIssue `json:"issues"`
}

// xAccountPatchRequest 用指针区分"未提交"与零值。
type xAccountPatchRequest struct {
	Active    *bool   `json:"active"`
	IsPrimary *bool   `json:"isPrimary"`
	UserAgent *string `json:"userAgent"`
	Proxy     *string `json:"proxy"`
	Cookie    *string `json:"cookie"`
}

type xAccountCheckResponse struct {
	OK         bool         `json:"ok"`
	ScreenName string       `json:"screenName,omitempty"`
	Error      string       `json:"error,omitempty"`
	Account    xAccountView `json:"account"`
}

type xAccountCheckAllItem struct {
	ID         int64  `json:"id"`
	OK         bool   `json:"ok"`
	ScreenName string `json:"screenName,omitempty"`
	Error      string `json:"error,omitempty"`
}

type xAccountCheckAllResponse struct {
	Results  []xAccountCheckAllItem `json:"results"`
	Accounts []xAccountView         `json:"accounts"`
}

func (s *Server) listXAccounts(w http.ResponseWriter, r *http.Request) {
	accounts, err := s.store.ListXAccounts(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	views, err := s.xAccountViews(r.Context(), accounts)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, xAccountsResponse{Scraper: jobs.ScraperMode(), Accounts: views})
}

func (s *Server) createXAccount(w http.ResponseWriter, r *http.Request) {
	var req xAccountCreateRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	input, err := parseSingleXAccountCookie(req.Cookie)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	userAgent, err := normalizeXAccountUserAgent(req.UserAgent)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	// 新账号没有库内代理可还原：打码占位符会被去掉，不会被当成真实凭据保存。
	proxy, err := normalizeXAccountProxy(req.Proxy, "")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	account, err := xAccountFromCookieInput(input)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	account.UserAgent = userAgent
	account.Proxy = proxy
	account.IsPrimary = req.IsPrimary
	created, err := s.store.CreateXAccount(r.Context(), account)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.invalidateXSource()
	s.writeXAccountView(w, r, http.StatusCreated, created)
}

func (s *Server) importXAccounts(w http.ResponseWriter, r *http.Request) {
	var req xAccountImportRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	inputs, parseIssues := config.ParseCookieInput(req.Text)
	if len(inputs) == 0 && len(parseIssues) == 0 {
		writeError(w, http.StatusBadRequest, storage.ErrValidation("没有识别到要导入的账号"))
		return
	}
	issues := make([]xAccountImportIssue, 0, len(parseIssues))
	for _, issue := range parseIssues {
		issues = append(issues, xAccountImportIssue{Index: issue.Index, Reason: issue.Reason})
	}
	created := make([]storage.XAccount, 0, len(inputs))
	for _, input := range inputs {
		account, err := xAccountFromCookieInput(input)
		if err == nil {
			account, err = s.store.CreateXAccount(r.Context(), account)
		}
		if err != nil {
			if r.Context().Err() != nil {
				return
			}
			// 重复账号等单条失败不影响其他条目，按输入序号报告。
			issues = append(issues, xAccountImportIssue{Index: input.Index, Reason: xAccountEntryError(err)})
			continue
		}
		created = append(created, account)
	}
	sort.SliceStable(issues, func(i, j int) bool { return issues[i].Index < issues[j].Index })
	if len(created) > 0 {
		s.invalidateXSource()
	}
	views, err := s.xAccountViews(r.Context(), created)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, xAccountImportResponse{Created: views, Issues: issues})
}

func (s *Server) updateXAccount(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var req xAccountPatchRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.IsPrimary != nil && !*req.IsPrimary {
		writeError(w, http.StatusBadRequest, storage.ErrValidation("不能直接取消主账号，请把另一个账号设为主账号"))
		return
	}
	current, err := s.store.GetXAccount(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	patch := storage.XAccountPatch{Active: req.Active, IsPrimary: req.IsPrimary}
	if req.UserAgent != nil {
		userAgent, err := normalizeXAccountUserAgent(*req.UserAgent)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		patch.UserAgent = &userAgent
	}
	if req.Proxy != nil {
		// 前端把打码后的代理原样带回来：同一主机时还原库内的用户名密码。
		proxy, err := normalizeXAccountProxy(*req.Proxy, current.Proxy)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		patch.Proxy = &proxy
	}
	// 留空表示不替换 Cookie，与编辑框"留空则不修改"一致。
	if req.Cookie != nil && strings.TrimSpace(*req.Cookie) != "" {
		input, err := parseSingleXAccountCookie(*req.Cookie)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		extra, err := encodeXAccountExtraCookies(input.Extra)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		// 新 Cookie 整组替换旧值，包括额外 Cookie；env 账号由 storage 拒绝。
		patch.AuthToken = &input.AuthToken
		patch.CSRFToken = &input.CSRFToken
		patch.ExtraCookies = &extra
	}
	updated, err := s.store.UpdateXAccount(r.Context(), id, patch)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.invalidateXSource()
	s.writeXAccountView(w, r, http.StatusOK, updated)
}

func (s *Server) deleteXAccount(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.store.DeleteXAccount(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.invalidateXSource()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) checkXAccount(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if s.homeProber == nil {
		writeError(w, http.StatusServiceUnavailable, errXAccountProberUnavailable)
		return
	}
	account, err := s.store.GetXAccount(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	globalProxy, err := s.effectiveProxyURL(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	check := s.probeXAccount(r.Context(), account, globalProxy)
	if r.Context().Err() != nil {
		// 客户端已断开：探测结果多半是"已取消"，不能据此改写账号状态。
		return
	}
	updated, err := s.store.ApplyXAccountCheck(r.Context(), id, check, time.Now())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.invalidateXSource()
	view, err := s.xAccountView(r.Context(), updated)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, xAccountCheckResponse{
		OK:         check.Err == "",
		ScreenName: check.ScreenName,
		Error:      check.Err,
		Account:    view,
	})
}

func (s *Server) unlockXAccount(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	// 先解锁再读：响应里的 locks 必须已经是解锁后的状态，否则前端拿到的仍是旧锁。
	if err := s.store.UnlockXAccount(r.Context(), id, ""); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	account, err := s.store.GetXAccount(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.invalidateXSource()
	s.writeXAccountView(w, r, http.StatusOK, account)
}

func (s *Server) checkAllXAccounts(w http.ResponseWriter, r *http.Request) {
	if s.homeProber == nil {
		writeError(w, http.StatusServiceUnavailable, errXAccountProberUnavailable)
		return
	}
	ctx := r.Context()
	accounts, err := s.store.ListXAccounts(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	globalProxy, err := s.effectiveProxyURL(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	results := make([]xAccountCheckAllItem, len(accounts))
	next := make(chan int)
	var wg sync.WaitGroup
	for range min(xAccountCheckConcurrency, len(accounts)) {
		wg.Go(func() {
			for index := range next {
				results[index] = s.checkXAccountInBatch(ctx, accounts[index], globalProxy)
			}
		})
	}
	for index := range accounts {
		next <- index
	}
	close(next)
	wg.Wait()
	if ctx.Err() != nil {
		return
	}
	s.invalidateXSource()
	refreshed, err := s.store.ListXAccounts(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	views, err := s.xAccountViews(ctx, refreshed)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, xAccountCheckAllResponse{Results: results, Accounts: views})
}

// checkXAccountInBatch 检测一个账号并写回结果，供全部检测使用。
func (s *Server) checkXAccountInBatch(ctx context.Context, account storage.XAccount, globalProxy string) xAccountCheckAllItem {
	item := xAccountCheckAllItem{ID: account.ID}
	check := s.probeXAccount(ctx, account, globalProxy)
	if ctx.Err() != nil {
		item.Error = "检测已取消"
		return item
	}
	if _, err := s.store.ApplyXAccountCheck(ctx, account.ID, check, time.Now()); err != nil {
		item.Error = xAccountEntryError(err)
		return item
	}
	item.OK = check.Err == ""
	item.ScreenName = check.ScreenName
	item.Error = check.Err
	return item
}

// probeXAccount 用注入的探测函数检测账号（最多 xAccountCheckTimeout），并把结果转换成
// storage 的检测结论。探测本身不读写账号表。
func (s *Server) probeXAccount(ctx context.Context, account storage.XAccount, globalProxy string) storage.XAccountCheck {
	creds := xAccountCredentials(account, globalProxy)
	probeCtx, cancel := context.WithTimeout(ctx, xAccountCheckTimeout)
	defer cancel()
	return xAccountCheckFromProbe(s.homeProber(probeCtx, creds), creds)
}

// xAccountCredentials 组装探测凭据：账号 Cookie 加上额外 Cookie（格式损坏时忽略），
// 账号没有单独配置代理时使用全局生效代理。
func xAccountCredentials(account storage.XAccount, globalProxy string) xapi.AccountCredentials {
	extra, err := account.ExtraCookieMap()
	if err != nil {
		// 不记录 err：JSON 语法错误会带出原文里的字符。
		log.Printf("httpapi: 账号 %d 的额外 Cookie 格式损坏，检测时已忽略", account.ID)
		extra = nil
	}
	proxy := strings.TrimSpace(account.Proxy)
	if proxy == "" {
		proxy = globalProxy
	}
	return xapi.AccountCredentials{
		AuthToken:    account.AuthToken,
		CSRFToken:    account.CSRFToken,
		ExtraCookies: extra,
		UserAgent:    account.UserAgent,
		Proxy:        proxy,
	}
}

// xAccountCheckFromProbe 按 xapi.HomeProbeResult 的约定转换探测结果。storage 以 Err 为空
// 判定成功，所以确认失效时必须带上原因；成功但没有拿到登录账号时按无法判断处理，避免
// 在没有证据时重新启用被系统停用的账号。
func xAccountCheckFromProbe(result xapi.HomeProbeResult, creds xapi.AccountCredentials) storage.XAccountCheck {
	switch {
	case result.Unauthorized:
		return storage.XAccountCheck{Unauthorized: true, Err: xAccountProbeMessage(result.Err, "X 拒绝了这个账号（Cookie 已失效或账号受限）", creds)}
	case result.Err != nil:
		if errors.Is(result.Err, context.DeadlineExceeded) {
			return storage.XAccountCheck{Err: "检测超时，没有在 20 秒内得到结果"}
		}
		return storage.XAccountCheck{Err: xAccountProbeMessage(result.Err, "检测失败", creds)}
	case strings.TrimSpace(result.ScreenName) == "":
		return storage.XAccountCheck{Err: "检测没有返回登录账号，无法确认 Cookie 是否有效"}
	default:
		return storage.XAccountCheck{ScreenName: strings.TrimSpace(result.ScreenName)}
	}
}

// xAccountProbeMessage 把探测错误转成可写入 error_msg 的文本：替换掉可能混进来的 Cookie
// 与代理密码，并限制长度。
func xAccountProbeMessage(err error, fallback string, creds xapi.AccountCredentials) string {
	if err == nil {
		return fallback
	}
	message := strings.TrimSpace(err.Error())
	if message == "" {
		return fallback
	}
	secrets := []string{creds.AuthToken, creds.CSRFToken}
	for _, value := range creds.ExtraCookies {
		secrets = append(secrets, value)
	}
	if proxy, parseErr := url.Parse(creds.Proxy); parseErr == nil && proxy.User != nil {
		if password, ok := proxy.User.Password(); ok {
			secrets = append(secrets, password)
		}
	}
	for _, secret := range secrets {
		// 太短的值（如 lang=en）替换掉会破坏正常文字，也不是凭据。
		if len(secret) >= 8 {
			message = strings.ReplaceAll(message, secret, config.SecretPlaceholder)
		}
	}
	if utf8.RuneCountInString(message) > maxXAccountCheckErrorRunes {
		message = string([]rune(message)[:maxXAccountCheckErrorRunes]) + "…"
	}
	return message
}

// effectiveProxyURL 返回全局生效代理（已套用环境变量覆盖）。
func (s *Server) effectiveProxyURL(ctx context.Context) (string, error) {
	cfg, err := s.store.GetConfig(ctx)
	if err != nil {
		return "", err
	}
	return cfg.ProxyURL, nil
}

func (s *Server) invalidateXSource() {
	if s.manager != nil {
		s.manager.InvalidateXSource()
	}
}

func (s *Server) writeXAccountView(w http.ResponseWriter, r *http.Request, status int, account storage.XAccount) {
	view, err := s.xAccountView(r.Context(), account)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, status, view)
}

func (s *Server) xAccountView(ctx context.Context, account storage.XAccount) (xAccountView, error) {
	views, err := s.xAccountViews(ctx, []storage.XAccount{account})
	if err != nil {
		return xAccountView{}, err
	}
	return views[0], nil
}

// xAccountViews 为账号附上未过期的锁和今天（UTC）的请求统计。
func (s *Server) xAccountViews(ctx context.Context, accounts []storage.XAccount) ([]xAccountView, error) {
	views := make([]xAccountView, 0, len(accounts))
	if len(accounts) == 0 {
		return views, nil
	}
	now := time.Now().UTC()
	locks, err := s.store.ListXAccountLocks(ctx, now)
	if err != nil {
		return nil, err
	}
	stats, err := s.store.ListXAccountStats(ctx, now.Format(xAccountStatsDayLayout))
	if err != nil {
		return nil, err
	}
	locksByAccount := make(map[int64][]xAccountLockView)
	for _, lock := range locks {
		locksByAccount[lock.AccountID] = append(locksByAccount[lock.AccountID], xAccountLockView{
			Queue:  lock.Queue,
			Until:  formatXAccountTime(lock.Until),
			Reason: lock.Reason,
		})
	}
	statsByAccount := make(map[int64][]xAccountQueueRequests)
	for _, stat := range stats {
		statsByAccount[stat.AccountID] = append(statsByAccount[stat.AccountID], xAccountQueueRequests{
			Queue:    stat.Queue,
			Requests: stat.Requests,
		})
	}
	for _, account := range accounts {
		views = append(views, newXAccountView(account, locksByAccount[account.ID], statsByAccount[account.ID]))
	}
	return views, nil
}

func newXAccountView(account storage.XAccount, locks []xAccountLockView, stats []xAccountQueueRequests) xAccountView {
	extra, err := account.ExtraCookieMap()
	if locks == nil {
		locks = []xAccountLockView{}
	}
	if stats == nil {
		stats = []xAccountQueueRequests{}
	}
	view := xAccountView{
		ID:              account.ID,
		ScreenName:      account.ScreenName,
		AuthToken:       maskXAccountSecret(account.AuthToken),
		CSRFToken:       maskXAccountSecret(account.CSRFToken),
		HasExtraCookies: err == nil && len(extra) > 0,
		UserAgent:       account.UserAgent,
		Proxy:           config.RedactURLUserinfo(account.Proxy),
		IsPrimary:       account.IsPrimary,
		Source:          account.Source,
		Active:          account.Active,
		ErrorMsg:        account.ErrorMsg,
		LastUsed:        formatXAccountNullTime(account.LastUsed.Time, account.LastUsed.Valid),
		VerifiedAt:      formatXAccountNullTime(account.VerifiedAt.Time, account.VerifiedAt.Valid),
		CreatedAt:       formatXAccountTime(account.CreatedAt),
		Locks:           locks,
		RequestsByQueue: stats,
	}
	for _, item := range stats {
		view.RequestsToday += item.Requests
	}
	return view
}

// maskXAccountSecret 把 Cookie 值打码成"前 4 位…后 4 位"；8 个字符及以下整段替换为
// 占位符。空值保持为空，便于看出缺少该 Cookie。
func maskXAccountSecret(value string) string {
	if value == "" {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= 8 {
		return config.SecretPlaceholder
	}
	return string(runes[:4]) + "…" + string(runes[len(runes)-4:])
}

func formatXAccountTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339)
}

func formatXAccountNullTime(value time.Time, valid bool) *string {
	if !valid {
		return nil
	}
	formatted := formatXAccountTime(value)
	return &formatted
}

// parseSingleXAccountCookie 要求输入恰好是一个账号的 Cookie。
func parseSingleXAccountCookie(raw string) (config.CookieInput, error) {
	inputs, issues := config.ParseCookieInput(raw)
	switch {
	case len(inputs) == 1 && len(issues) == 0:
		return inputs[0], nil
	case len(inputs)+len(issues) > 1:
		return config.CookieInput{}, storage.ErrValidation("一次只能添加一个账号的 Cookie，多个账号请使用导入")
	case len(issues) == 1:
		return config.CookieInput{}, storage.ErrValidation("Cookie 无法识别：" + issues[0].Reason)
	default:
		return config.CookieInput{}, storage.ErrValidation("Cookie 不能为空")
	}
}

func xAccountFromCookieInput(input config.CookieInput) (storage.XAccount, error) {
	extra, err := encodeXAccountExtraCookies(input.Extra)
	if err != nil {
		return storage.XAccount{}, err
	}
	return storage.XAccount{AuthToken: input.AuthToken, CSRFToken: input.CSRFToken, ExtraCookies: extra}, nil
}

// encodeXAccountExtraCookies 把额外 Cookie 编码成 extra_cookies 列的 JSON 对象；没有时为空串。
func encodeXAccountExtraCookies(extra map[string]string) (string, error) {
	if len(extra) == 0 {
		return "", nil
	}
	payload, err := json.Marshal(extra)
	if err != nil {
		return "", err
	}
	return string(payload), nil
}

// normalizeXAccountProxy 还原打码的代理（同一 scheme/主机时换回库内的 userinfo）并校验格式。
// 解析错误的原文会带出整段地址（可能含密码），因此只返回通用提示。
func normalizeXAccountProxy(submitted, stored string) (string, error) {
	proxy := config.RestoreURLUserinfo(strings.TrimSpace(submitted), stored)
	if err := httpx.ValidateProxyURL(proxy); err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			return "", storage.ErrValidation("代理地址无效")
		}
		return "", storage.ErrValidation(err.Error())
	}
	return proxy, nil
}

// normalizeXAccountUserAgent 去掉首尾空白并拒绝控制字符：User-Agent 会原样写进请求头，
// 含换行的值会让该账号的每个请求都失败。
func normalizeXAccountUserAgent(raw string) (string, error) {
	userAgent := strings.TrimSpace(raw)
	if utf8.RuneCountInString(userAgent) > maxXAccountUserAgentRunes {
		return "", storage.ErrValidation("User-Agent 太长")
	}
	if strings.ContainsFunc(userAgent, unicode.IsControl) {
		return "", storage.ErrValidation("User-Agent 不能包含换行或控制字符")
	}
	return userAgent, nil
}

// xAccountEntryError 把单个账号的保存错误转成可展示的原因；内部错误只记日志。
func xAccountEntryError(err error) string {
	var userFacing *storage.UserFacingError
	if errors.As(err, &userFacing) {
		return userFacing.Message
	}
	log.Printf("httpapi x-accounts: %v", err)
	return "保存账号失败"
}
