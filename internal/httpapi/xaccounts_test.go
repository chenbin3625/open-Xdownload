package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/config"
	"github.com/chenbin3625/open-Xdownload/internal/jobs"
	"github.com/chenbin3625/open-Xdownload/internal/storage"
	"github.com/chenbin3625/open-Xdownload/internal/xapi"
)

// newXAccountsTestServer 建一个只带账号池路由的服务器。探测函数默认未注入，
// 检测相关用例自己用 setHomeProber 覆盖。
func newXAccountsTestServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	server := NewServer(store, nil, nil, nil)
	httpServer := httptest.NewServer(authenticatedRoutes(t, server))
	t.Cleanup(httpServer.Close)
	return server, httpServer
}

func setHomeProber(t *testing.T, server *Server, prober xapi.HomeProber) {
	t.Helper()
	server.SetHomeProber(prober)
}

// xAccountsRequest 发一次账号池请求。body 为 nil 时不带请求体。
func xAccountsRequest(t *testing.T, httpServer *httptest.Server, method, path string, body any) *http.Response {
	t.Helper()
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(payload)
	}
	request, err := http.NewRequest(method, httpServer.URL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := httpServer.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { response.Body.Close() })
	return response
}

func decodeXAccountsBody[T any](t *testing.T, response *http.Response) T {
	t.Helper()
	var payload T
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("decode %s %s: %v", response.Request.Method, response.Request.URL.Path, err)
	}
	return payload
}

func mustXAccount[T any](t *testing.T, response *http.Response) T {
	t.Helper()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		t.Fatalf("%s %s: status %d: %s", response.Request.Method, response.Request.URL.Path, response.StatusCode, readBodyString(t, response))
	}
	return decodeXAccountsBody[T](t, response)
}

// readBodyString 读尽响应体（错误断言时用于输出服务端提示）。
func readBodyString(t *testing.T, response *http.Response) string {
	t.Helper()
	payload := new(bytes.Buffer)
	if _, err := payload.ReadFrom(response.Body); err != nil {
		t.Fatalf("read body: %v", err)
	}
	return strings.TrimSpace(payload.String())
}

func cookieLine(authToken, csrfToken string) string {
	return "auth_token=" + authToken + "; ct0=" + csrfToken
}

func itoa(id int64) string {
	return strconv.FormatInt(id, 10)
}

func TestXAccountsCreateMasksSecrets(t *testing.T) {
	_, httpServer := newXAccountsTestServer(t)
	response := xAccountsRequest(t, httpServer, http.MethodPost, "/api/x-accounts", map[string]any{
		"cookie": cookieLine("created-auth-token-0123456789", "created-ct0-0123456789"),
		"proxy":  "http://proxy-user:proxy-secret@127.0.0.1:7890",
	})
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", response.StatusCode, readBodyString(t, response))
	}
	raw := readBodyString(t, response)
	for _, secret := range []string{"created-auth-token-0123456789", "created-ct0-0123456789", "proxy-secret"} {
		if strings.Contains(raw, secret) {
			t.Fatalf("response leaks %q: %s", secret, raw)
		}
	}
	var view xAccountView
	if err := json.Unmarshal([]byte(raw), &view); err != nil {
		t.Fatal(err)
	}
	if view.AuthToken != "crea…6789" || view.CSRFToken != "crea…6789" {
		t.Fatalf("masked view = %+v", view)
	}
	// RedactURLUserinfo 整段替换 userinfo（用户名也算凭据），与全局代理的显示一致。
	if view.Proxy != "http://"+config.SecretPlaceholder+"@127.0.0.1:7890" {
		t.Fatalf("proxy = %q, want userinfo replaced by placeholder", view.Proxy)
	}
	if !view.Active || view.Source != "db" || view.ErrorMsg != "" {
		t.Fatalf("new account should be an active db account: %+v", view)
	}
}

func TestXAccountsImportReportsIssuesByLine(t *testing.T) {
	_, httpServer := newXAccountsTestServer(t)
	text := strings.Join([]string{
		cookieLine("import-auth-token-aaaa1111", "import-ct0-aaaa1111"),
		"auth_token=missing-ct0-token-bbbb2222",
		cookieLine("import-auth-token-aaaa1111", "import-ct0-aaaa1111"),
	}, "\n")
	response := xAccountsRequest(t, httpServer, http.MethodPost, "/api/x-accounts/import", map[string]any{"text": text})
	payload := mustXAccount[xAccountImportResponse](t, response)
	if len(payload.Created) != 1 {
		t.Fatalf("created = %d, want 1: %+v", len(payload.Created), payload)
	}
	reasons := map[int]string{}
	for _, issue := range payload.Issues {
		reasons[issue.Index] = issue.Reason
	}
	if len(reasons) != 2 || reasons[1] == "" || reasons[2] == "" {
		t.Fatalf("issues = %+v, want entries for line 2 and 3", payload.Issues)
	}
}

func TestXAccountsPatchRejectsPrimaryRemovalAndEnvCookie(t *testing.T) {
	server, httpServer := newXAccountsTestServer(t)
	created := mustXAccount[xAccountView](t, xAccountsRequest(t, httpServer, http.MethodPost, "/api/x-accounts", map[string]any{
		"cookie": cookieLine("patch-auth-token-cccc3333", "patch-ct0-cccc3333"),
	}))

	falseValue := false
	response := xAccountsRequest(t, httpServer, http.MethodPatch, "/api/x-accounts/"+itoa(created.ID), map[string]any{"isPrimary": falseValue})
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("unset primary status = %d, want 400: %s", response.StatusCode, readBodyString(t, response))
	}

	envAccount, err := server.store.CreateXAccount(context.Background(), storage.XAccount{
		AuthToken: "env-auth-token-dddd4444", CSRFToken: "env-ct0-dddd4444", Source: "env",
	})
	if err != nil {
		t.Fatal(err)
	}
	response = xAccountsRequest(t, httpServer, http.MethodPatch, "/api/x-accounts/"+itoa(envAccount.ID), map[string]any{
		"cookie": cookieLine("renewed-auth-token-eeee5555", "renewed-ct0-eeee5555"),
	})
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("env cookie patch status = %d, want 400: %s", response.StatusCode, readBodyString(t, response))
	}
}

func TestXAccountsUnlockReturnsUnlockedView(t *testing.T) {
	server, httpServer := newXAccountsTestServer(t)
	created := mustXAccount[xAccountView](t, xAccountsRequest(t, httpServer, http.MethodPost, "/api/x-accounts", map[string]any{
		"cookie": cookieLine("unlock-auth-token-ffff6666", "unlock-ct0-ffff6666"),
	}))
	ctx := context.Background()
	now := time.Now()
	for _, queue := range []string{"*", "UserMedia"} {
		if err := server.store.LockXAccount(ctx, created.ID, queue, now.Add(time.Hour), "test"); err != nil {
			t.Fatal(err)
		}
	}

	payload := mustXAccount[xAccountView](t, xAccountsRequest(t, httpServer, http.MethodPost, "/api/x-accounts/"+itoa(created.ID)+"/unlock", nil))
	if len(payload.Locks) != 0 {
		t.Fatalf("unlock response still carries locks: %+v", payload.Locks)
	}
	stored, err := server.store.ListXAccountLocks(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 0 {
		t.Fatalf("locks remain in storage: %+v", stored)
	}
}

func TestXAccountsCheckAppliesProbeResult(t *testing.T) {
	t.Run("success re-enables a system disabled account", func(t *testing.T) {
		server, httpServer := newXAccountsTestServer(t)
		created := mustXAccount[xAccountView](t, xAccountsRequest(t, httpServer, http.MethodPost, "/api/x-accounts", map[string]any{
			"cookie": cookieLine("check-auth-token-gggg7777", "check-ct0-gggg7777"),
		}))
		ctx := context.Background()
		if err := server.store.DeactivateXAccount(ctx, created.ID, "Cookie 已失效"); err != nil {
			t.Fatal(err)
		}
		if err := server.store.LockXAccount(ctx, created.ID, "*", time.Now().Add(time.Hour), "test"); err != nil {
			t.Fatal(err)
		}
		setHomeProber(t, server, func(context.Context, xapi.AccountCredentials) xapi.HomeProbeResult {
			return xapi.HomeProbeResult{ScreenName: "alice"}
		})

		payload := mustXAccount[xAccountCheckResponse](t, xAccountsRequest(t, httpServer, http.MethodPost, "/api/x-accounts/"+itoa(created.ID)+"/check", nil))
		if !payload.OK || payload.ScreenName != "alice" {
			t.Fatalf("check = %+v, want ok with screen name", payload)
		}
		if !payload.Account.Active || payload.Account.ScreenName != "alice" || payload.Account.ErrorMsg != "" {
			t.Fatalf("account after successful check = %+v", payload.Account)
		}
		if payload.Account.VerifiedAt == nil {
			t.Fatal("successful check must stamp verifiedAt")
		}
		if len(payload.Account.Locks) != 0 {
			t.Fatalf("successful check keeps locks: %+v", payload.Account.Locks)
		}
	})

	t.Run("unauthorized disables and keeps user disabled account disabled", func(t *testing.T) {
		server, httpServer := newXAccountsTestServer(t)
		created := mustXAccount[xAccountView](t, xAccountsRequest(t, httpServer, http.MethodPost, "/api/x-accounts", map[string]any{
			"cookie": cookieLine("unauth-auth-token-hhhh8888", "unauth-ct0-hhhh8888"),
		}))
		setHomeProber(t, server, func(context.Context, xapi.AccountCredentials) xapi.HomeProbeResult {
			return xapi.HomeProbeResult{Unauthorized: true}
		})

		payload := mustXAccount[xAccountCheckResponse](t, xAccountsRequest(t, httpServer, http.MethodPost, "/api/x-accounts/"+itoa(created.ID)+"/check", nil))
		if payload.OK {
			t.Fatalf("unauthorized check must not be ok: %+v", payload)
		}
		if payload.Error == "" {
			t.Fatal("unauthorized check must report a reason")
		}
		if payload.Account.Active || payload.Account.ErrorMsg == "" {
			t.Fatalf("account after unauthorized check = %+v, want disabled with reason", payload.Account)
		}

		// 用户手动停用的账号：探测确认失效也不能把它变成"系统停用"（否则下次检测成功会自动启用）。
		manual := mustXAccount[xAccountView](t, xAccountsRequest(t, httpServer, http.MethodPost, "/api/x-accounts", map[string]any{
			"cookie": cookieLine("manual-auth-token-iiii9999", "manual-ct0-iiii9999"),
		}))
		disabled := false
		if _, err := server.store.UpdateXAccount(context.Background(), manual.ID, storage.XAccountPatch{Active: &disabled}); err != nil {
			t.Fatal(err)
		}
		payload = mustXAccount[xAccountCheckResponse](t, xAccountsRequest(t, httpServer, http.MethodPost, "/api/x-accounts/"+itoa(manual.ID)+"/check", nil))
		if payload.Account.Active || payload.Account.ErrorMsg != "" {
			t.Fatalf("manually disabled account after failed check = %+v, want still disabled without system error", payload.Account)
		}
	})

	t.Run("inconclusive error keeps account active", func(t *testing.T) {
		server, httpServer := newXAccountsTestServer(t)
		created := mustXAccount[xAccountView](t, xAccountsRequest(t, httpServer, http.MethodPost, "/api/x-accounts", map[string]any{
			"cookie": cookieLine("flaky-auth-token-jjjj0000", "flaky-ct0-jjjj0000"),
		}))
		setHomeProber(t, server, func(context.Context, xapi.AccountCredentials) xapi.HomeProbeResult {
			return xapi.HomeProbeResult{Err: errors.New("network is unreachable")}
		})

		payload := mustXAccount[xAccountCheckResponse](t, xAccountsRequest(t, httpServer, http.MethodPost, "/api/x-accounts/"+itoa(created.ID)+"/check", nil))
		if payload.OK || payload.Error == "" {
			t.Fatalf("inconclusive check = %+v, want not ok with reason", payload)
		}
		if !payload.Account.Active || payload.Account.ErrorMsg == "" {
			t.Fatalf("account after inconclusive check = %+v, want active with error note", payload.Account)
		}
	})

	t.Run("probe error redacts credentials", func(t *testing.T) {
		server, httpServer := newXAccountsTestServer(t)
		const authToken = "redact-auth-token-kkkk1234"
		const csrfToken = "redact-ct0-kkkk1234"
		created := mustXAccount[xAccountView](t, xAccountsRequest(t, httpServer, http.MethodPost, "/api/x-accounts", map[string]any{
			"cookie": cookieLine(authToken, csrfToken),
			"proxy":  "http://proxy-user:proxy-password@127.0.0.1:7890",
		}))
		setHomeProber(t, server, func(context.Context, xapi.AccountCredentials) xapi.HomeProbeResult {
			return xapi.HomeProbeResult{Err: errors.New("dial failed: " + authToken + " " + csrfToken + " proxy-password")}
		})

		payload := mustXAccount[xAccountCheckResponse](t, xAccountsRequest(t, httpServer, http.MethodPost, "/api/x-accounts/"+itoa(created.ID)+"/check", nil))
		for _, secret := range []string{authToken, csrfToken, "proxy-password"} {
			if strings.Contains(payload.Error, secret) || strings.Contains(payload.Account.ErrorMsg, secret) {
				t.Fatalf("check error leaks %q: %q / %q", secret, payload.Error, payload.Account.ErrorMsg)
			}
		}
	})

	t.Run("missing prober reports unavailable", func(t *testing.T) {
		_, httpServer := newXAccountsTestServer(t)
		created := mustXAccount[xAccountView](t, xAccountsRequest(t, httpServer, http.MethodPost, "/api/x-accounts", map[string]any{
			"cookie": cookieLine("noprobe-auth-token-llll5678", "noprobe-ct0-llll5678"),
		}))
		response := xAccountsRequest(t, httpServer, http.MethodPost, "/api/x-accounts/"+itoa(created.ID)+"/check", nil)
		if response.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503: %s", response.StatusCode, readBodyString(t, response))
		}
	})
}

func TestXAccountsCheckAllProbesEveryAccount(t *testing.T) {
	server, httpServer := newXAccountsTestServer(t)
	for index, token := range []string{"batch-auth-token-mmmm1111", "batch-auth-token-nnnn2222", "batch-auth-token-oooo3333"} {
		mustXAccount[xAccountView](t, xAccountsRequest(t, httpServer, http.MethodPost, "/api/x-accounts", map[string]any{
			"cookie": cookieLine(token, "batch-ct0-1111"+strconv.Itoa(index)+"0000"),
		}))
	}
	var calls atomic.Int32
	setHomeProber(t, server, func(context.Context, xapi.AccountCredentials) xapi.HomeProbeResult {
		calls.Add(1)
		return xapi.HomeProbeResult{ScreenName: "batch"}
	})

	payload := mustXAccount[xAccountCheckAllResponse](t, xAccountsRequest(t, httpServer, http.MethodPost, "/api/x-accounts/check-all", nil))
	if calls.Load() != 3 || len(payload.Results) != 3 || len(payload.Accounts) != 3 {
		t.Fatalf("probe calls = %d, results = %+v, want 3 each", calls.Load(), payload.Results)
	}
	for _, item := range payload.Results {
		if !item.OK || item.ScreenName != "batch" {
			t.Fatalf("check-all item = %+v, want ok", item)
		}
	}
	// 全部检测成功后 env/db 账号都应该带着 verifiedAt 出现在最终视图里。
	for _, account := range payload.Accounts {
		if account.VerifiedAt == nil {
			t.Fatalf("account %d missing verifiedAt after check-all", account.ID)
		}
	}
}

func TestListXAccountsReportsScraperMode(t *testing.T) {
	t.Setenv("OPEN_XDOWNLOAD_SCRAPER", jobs.ScraperXScrape)
	_, httpServer := newXAccountsTestServer(t)
	payload := mustXAccount[xAccountsResponse](t, xAccountsRequest(t, httpServer, http.MethodGet, "/api/x-accounts", nil))
	if payload.Scraper != jobs.ScraperXScrape {
		t.Fatalf("scraper = %q, want %q", payload.Scraper, jobs.ScraperXScrape)
	}
	if payload.Accounts == nil {
		t.Fatal("accounts must be an empty array, not null")
	}
}

func TestUpdateConfigRejectsLegacyCookieFields(t *testing.T) {
	server, httpServer := newXAccountsTestServer(t)
	ctx := context.Background()
	// 删除和打码占位符是旧前端原样回传的内容，静默忽略。
	for name, payload := range map[string]map[string]any{
		"placeholder": {"authToken": "********", "csrfToken": ""},
		"clear":       {"authToken": config.ClearSentinel, "additionalCookies": nil},
	} {
		t.Run(name, func(t *testing.T) {
			response := xAccountsRequest(t, httpServer, http.MethodPut, "/api/config", payload)
			if response.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", response.StatusCode, readBodyString(t, response))
			}
		})
	}
	response := xAccountsRequest(t, httpServer, http.MethodPut, "/api/config", map[string]any{
		"csrfToken": "real-cookie-value",
	})
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("real cookie status = %d, want 400: %s", response.StatusCode, readBodyString(t, response))
	}
	if body := readBodyString(t, response); !strings.Contains(body, "账号池") {
		t.Fatalf("error should point at the account pool: %s", body)
	}
	stored, err := server.store.GetStoredConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stored.CSRFToken != "" || stored.AuthToken != "" {
		t.Fatalf("legacy cookie columns were written: %+v", stored)
	}
}
