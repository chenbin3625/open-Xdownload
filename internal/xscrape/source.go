package xscrape

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/parser"
	"github.com/chenbin3625/open-Xdownload/internal/storage"
	"github.com/chenbin3625/open-Xdownload/internal/xapi"
)

const (
	// checkAllConcurrency / checkAllAccountTimeout 沿用 legacy 的账号检测上限。
	checkAllConcurrency    = 4
	checkAllAccountTimeout = 20 * time.Second
	// diagnosticsTimeout 限制 Diagnostics 的只读查询耗时。
	diagnosticsTimeout = 5 * time.Second
)

// source 是绑定到一次 Source(ctx, cfg) 调用的 xapi.Source。账号表每次请求时读取，
// 账号变化无需重建；全局代理等配置由 Engine 共享。
type source struct {
	engine *Engine
	proxy  string
}

// accountRequester 把请求固定到某个账号（主账号、viewer 绑定、成员分页）。
type accountRequester struct {
	source  *source
	account storage.XAccount
}

func (r accountRequester) GraphQL(ctx context.Context, path string, values url.Values) ([]byte, error) {
	return r.source.doPinned(ctx, r.account, http.MethodGet, path, values, nil)
}

func (s *source) doPinned(ctx context.Context, account storage.XAccount, method, path string, query, form url.Values) ([]byte, error) {
	_, release, _, err := s.engine.slots.acquire(ctx, account.ID)
	if err != nil {
		return nil, err
	}
	return s.executeAccountRequest(ctx, account, release, queueForPath(path), method, path, query, form, true)
}

func (s *source) GetUserByInput(ctx context.Context, input string) (xapi.User, error) {
	return xapi.GetUserByInput(ctx, s, input, s.lookupFetcher())
}

func (s *source) GetListByID(ctx context.Context, id string) (xapi.List, error) {
	return xapi.GetListByID(ctx, s, id, s.listFetcher())
}

func (s *source) GetUserByIDAsPrimary(ctx context.Context, id string) (xapi.User, error) {
	account, err := s.primaryAccount(ctx)
	if err != nil {
		return xapi.User{}, err
	}
	return xapi.GetUserByID(ctx, accountRequester{source: s, account: account}, id, nil)
}

func (s *source) FollowUserAsPrimary(ctx context.Context, user xapi.User) error {
	account, err := s.primaryAccount(ctx)
	if err != nil {
		return err
	}
	form := url.Values{}
	form.Set("user_id", user.ID)
	_, err = s.doPinned(ctx, account, http.MethodPost, "/i/api/1.1/friendships/create.json", nil, form)
	return err
}

func (s *source) FetchTweet(ctx context.Context, id string, options parser.ParseOptions) (parser.TweetData, error) {
	active, err := s.engine.activeAccountCount(ctx)
	if err != nil {
		return parser.TweetData{}, err
	}
	if active == 0 {
		return xapi.FetchTweet(ctx, guestRequester{source: s}, id, false, options)
	}
	return xapi.FetchTweet(ctx, s, id, true, options)
}

func (s *source) WalkUserMedia(ctx context.Context, user xapi.User, options xapi.UserMediaTimelineOptions, save func(xapi.UserMediaTimelinePage) error) (xapi.MediaFetchStats, error) {
	var requester xapi.Requester = s
	if xapi.UserMediaNeedsPrimaryClient(user) {
		account, err := s.primaryAccount(ctx)
		if err != nil {
			return xapi.MediaFetchStats{}, err
		}
		requester = accountRequester{source: s, account: account}
	}
	return xapi.WalkUserMediaPages(ctx, requester, user, options, save)
}

func (s *source) WalkUserTweets(ctx context.Context, user xapi.User, options xapi.TweetTimelineOptions, save func(xapi.TweetTimelinePage) error) error {
	var requester xapi.Requester = s
	if xapi.UserMediaNeedsPrimaryClient(user) {
		account, err := s.primaryAccount(ctx)
		if err != nil {
			return err
		}
		requester = accountRequester{source: s, account: account}
	}
	return xapi.WalkUserTweets(ctx, requester, user, options, save)
}

func (s *source) GetListMembersPage(ctx context.Context, list xapi.List, cursor, viewerKey string) (xapi.UserPage, error) {
	viewer := viewerKey
	if viewer == "" {
		viewer = list.ViewerKey
	}
	return s.userPage(ctx, viewer, xapi.ListMembersPath, "data.list.members_timeline.timeline.instructions",
		map[string]any{"listId": list.ID, "count": 200, "withSafetyModeUserFields": true}, cursor)
}

func (s *source) GetFollowingPage(ctx context.Context, user xapi.User, cursor, viewerKey string) (xapi.UserPage, error) {
	var preferred storage.XAccount
	if user.Protected && viewerKey == "" {
		account, err := s.primaryAccount(ctx)
		if err != nil {
			return xapi.UserPage{}, err
		}
		preferred = account
	}
	requester, account, err := s.pageRequester(ctx, viewerKey, preferred, xapi.FollowingPath)
	if err != nil {
		return xapi.UserPage{}, err
	}
	page, err := xapi.FetchUserPage(ctx, requester, xapi.FollowingPath, "data.user.result.timeline.timeline.instructions",
		map[string]any{"userId": user.ID, "count": 200, "includePromotedContent": false}, cursor)
	if err != nil {
		return xapi.UserPage{}, err
	}
	page.ViewerKey = xapi.ViewerKey(account.AuthToken)
	return page, nil
}

func (s *source) userPage(ctx context.Context, viewerKey, path, instructions string, variables map[string]any, cursor string) (xapi.UserPage, error) {
	requester, account, err := s.pageRequester(ctx, viewerKey, storage.XAccount{}, path)
	if err != nil {
		return xapi.UserPage{}, err
	}
	page, err := xapi.FetchUserPage(ctx, requester, path, instructions, variables, cursor)
	if err != nil {
		return xapi.UserPage{}, err
	}
	page.ViewerKey = xapi.ViewerKey(account.AuthToken)
	return page, nil
}

// pageRequester 解析分页请求使用的账号：viewerKey 优先，其次 preferred（主账号），
// 否则从账号池选一个。
func (s *source) pageRequester(ctx context.Context, viewerKey string, preferred storage.XAccount, path string) (xapi.Requester, storage.XAccount, error) {
	if viewerKey != "" {
		account, err := s.accountByViewerKey(ctx, viewerKey)
		if err != nil {
			return nil, storage.XAccount{}, err
		}
		return accountRequester{source: s, account: account}, account, nil
	}
	if preferred.ID != 0 {
		return accountRequester{source: s, account: preferred}, preferred, nil
	}
	account, release, err := s.engine.nextAccount(ctx, queueForPath(path), map[int64]bool{})
	if err != nil {
		return nil, storage.XAccount{}, err
	}
	release()
	return accountRequester{source: s, account: account}, account, nil
}

func (s *source) accountByViewerKey(ctx context.Context, viewerKey string) (storage.XAccount, error) {
	accounts, err := s.engine.store.ListXAccounts(ctx)
	if err != nil {
		return storage.XAccount{}, err
	}
	for _, account := range accounts {
		if xapi.ViewerKey(account.AuthToken) != viewerKey {
			continue
		}
		if !account.Active {
			return storage.XAccount{}, errors.New("成员分页使用的 X 账号已停用，请重新创建归档任务")
		}
		return account, nil
	}
	return storage.XAccount{}, errors.New("成员分页使用的 X 账号已移除，请重新创建归档任务")
}

func (s *source) primaryAccount(ctx context.Context) (storage.XAccount, error) {
	accounts, err := s.engine.store.ListXAccounts(ctx)
	if err != nil {
		return storage.XAccount{}, err
	}
	for _, account := range accounts {
		if account.IsPrimary && account.Active {
			return account, nil
		}
	}
	return storage.XAccount{}, errors.New("没有可用 X 主账号")
}

// lookupFetcher 在目标账号受年龄限制时累积排除当前账号并换号重试。
func (s *source) lookupFetcher() xapi.LookupFetcher {
	excluded := map[int64]bool{}
	return func(ctx context.Context, path string, values url.Values, _ []byte) ([]byte, bool, error) {
		payload, account, err := s.graphQLWithAccount(ctx, path, values, excluded)
		if err != nil {
			return nil, false, err
		}
		if !xapi.AgeRestrictedPayload(payload) {
			return payload, false, nil
		}
		excluded[account.ID] = true
		active, err := s.engine.activeAccountCount(ctx)
		if err != nil || len(excluded) >= active {
			return payload, false, nil
		}
		return payload, true, nil
	}
}

// listFetcher 返回读取列表元信息的账号的 viewerKey，供成员分页固定视角。
func (s *source) listFetcher() xapi.ListFetcher {
	return func(ctx context.Context, path string, values url.Values) ([]byte, string, error) {
		payload, account, err := s.graphQLWithAccount(ctx, path, values, nil)
		if err != nil {
			return nil, "", err
		}
		return payload, xapi.ViewerKey(account.AuthToken), nil
	}
}

func (s *source) RecheckDisabledClients(ctx context.Context) bool {
	accounts, err := s.engine.store.ListXAccounts(ctx)
	if err != nil {
		return false
	}
	candidates := make([]storage.XAccount, 0, len(accounts))
	for _, account := range accounts {
		if !account.Active {
			candidates = append(candidates, account)
		}
	}
	if len(candidates) > 0 {
		s.checkAccounts(ctx, candidates)
	}
	active, err := s.engine.activeAccountCount(ctx)
	return err == nil && active > 0
}

func (s *source) CheckAll(ctx context.Context) xapi.PoolDiagnostics {
	accounts, err := s.engine.store.ListXAccounts(ctx)
	if err != nil {
		return s.Diagnostics()
	}
	s.checkAccounts(ctx, accounts)
	return s.Diagnostics()
}

// checkAccounts 并发（上限 4、单账号 20 秒）探测账号并把结论写回账号表。
func (s *source) checkAccounts(ctx context.Context, accounts []storage.XAccount) {
	sem := make(chan struct{}, checkAllConcurrency)
	var wait sync.WaitGroup
	for _, account := range accounts {
		wait.Add(1)
		go func(account storage.XAccount) {
			defer wait.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}
			accountCtx, cancel := context.WithTimeout(ctx, checkAllAccountTimeout)
			defer cancel()
			s.checkAccount(accountCtx, account)
		}(account)
	}
	wait.Wait()
}

func (s *source) checkAccount(ctx context.Context, account storage.XAccount) {
	result := s.engine.probe(ctx, s.accountCredentials(account))
	if ctx.Err() != nil {
		return
	}
	check := storage.XAccountCheck{}
	switch {
	case result.Unauthorized:
		check.Unauthorized = true
		check.Err = "X 拒绝该账号"
		if result.Err != nil {
			check.Err = result.Err.Error()
		}
		s.engine.probes.forget(account.ID)
	case result.Err != nil:
		check.Err = result.Err.Error()
	default:
		check.ScreenName = result.ScreenName
		s.engine.probes.mark(account.ID, s.engine.now())
	}
	_, _ = s.engine.store.ApplyXAccountCheck(ctx, account.ID, check, s.engine.now())
}

func (s *source) accountCredentials(account storage.XAccount) xapi.AccountCredentials {
	extra, _ := account.ExtraCookieMap()
	return xapi.AccountCredentials{
		AuthToken:    account.AuthToken,
		CSRFToken:    account.CSRFToken,
		ExtraCookies: extra,
		UserAgent:    account.UserAgent,
		Proxy:        s.engine.proxyFor(account.Proxy),
	}
}

func (s *source) Diagnostics() xapi.PoolDiagnostics {
	ctx, cancel := context.WithTimeout(context.Background(), diagnosticsTimeout)
	defer cancel()
	e := s.engine
	accounts, err := e.store.ListXAccounts(ctx)
	if err != nil {
		return xapi.PoolDiagnostics{}
	}
	locks, _ := e.store.ListXAccountLocks(ctx, e.now())
	stats, _ := e.store.ListXAccountStats(ctx, e.now().UTC().Format("2006-01-02"))

	locksByAccount := map[int64][]xapi.RateLimitSnapshot{}
	for _, lock := range locks {
		locksByAccount[lock.AccountID] = append(locksByAccount[lock.AccountID], xapi.RateLimitSnapshot{
			Path:    lock.Queue,
			Reset:   lock.Until,
			Blocked: true,
		})
	}
	statsByAccount := map[int64]int64{}
	for _, stat := range stats {
		statsByAccount[stat.AccountID] += stat.Requests
	}

	diagnostics := xapi.PoolDiagnostics{Total: len(accounts), Clients: make([]xapi.ClientStatus, 0, len(accounts))}
	for index, account := range accounts {
		status := xapi.ClientStatus{
			Index:        index,
			Primary:      account.IsPrimary,
			ScreenName:   account.ScreenName,
			OK:           account.Active,
			Disabled:     !account.Active,
			Error:        account.ErrorMsg,
			RequestCount: statsByAccount[account.ID],
			RateLimits:   locksByAccount[account.ID],
		}
		if status.OK {
			diagnostics.Available++
		}
		diagnostics.Clients = append(diagnostics.Clients, status)
	}
	return diagnostics
}

func (s *source) HasPrimary() bool {
	ctx, cancel := context.WithTimeout(context.Background(), diagnosticsTimeout)
	defer cancel()
	accounts, err := s.engine.store.ListXAccounts(ctx)
	if err != nil {
		return false
	}
	for _, account := range accounts {
		if account.IsPrimary && account.Active {
			return true
		}
	}
	return false
}

var _ xapi.Source = (*source)(nil)
