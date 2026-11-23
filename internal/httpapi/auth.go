package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/chenbin3625/open-Xdownload/internal/storage"
	"golang.org/x/crypto/bcrypt"
)

const sessionCookieName = "open_xdownload_session"
const sessionCookieMaxAge = int(storage.SessionLifetime / time.Second)

type authContextKey struct{}

type authenticatedSession struct {
	account *storage.Account
	revoked <-chan struct{}
}

func requestToken(r *http.Request) (string, bool) {
	if header := r.Header.Get("Authorization"); header != "" {
		parts := strings.Fields(header)
		if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
			return parts[1], true
		}
		return "", true
	}
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		return cookie.Value, false
	}
	return "", false
}

func signSession(key []byte) string {
	nonce := base64.RawURLEncoding.EncodeToString(randomSessionKey())
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(nonce))
	return nonce + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func randomSessionKey() []byte {
	key := make([]byte, 32)
	// crypto/rand.Read always fills the buffer or terminates the process.
	rand.Read(key)
	return key
}

func validSession(token string, key []byte) bool {
	nonce, signature, ok := strings.Cut(token, ".")
	if !ok || len(nonce) != 43 || len(signature) != 43 {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(nonce))
	return hmac.Equal(decoded, mac.Sum(nil))
}

func (s *Server) requestAccount(r *http.Request) (*storage.Account, error) {
	return s.requestAccountMode(r, true)
}

func (s *Server) requestAccountReadOnly(r *http.Request) (*storage.Account, error) {
	return s.requestAccountMode(r, false)
}

func (s *Server) requestAccountMode(r *http.Request, touch bool) (*storage.Account, error) {
	account, err := s.store.GetAccount(r.Context())
	if err != nil {
		return nil, err
	}
	token, _ := requestToken(r)
	if account == nil || !validSession(token, account.SessionKey) {
		return nil, nil
	}
	var active bool
	if touch {
		active, err = s.store.SessionValid(r.Context(), token, time.Now())
	} else {
		active, err = s.store.SessionValidReadOnly(r.Context(), token, time.Now())
	}
	if err != nil || !active {
		return nil, err
	}
	return account, nil
}

func (s *Server) acquireBcryptSem(ctx context.Context) bool {
	select {
	case s.bcryptSem <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

func setSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	maxAge := sessionCookieMaxAge
	if token == "" {
		maxAge = -1
	}
	proto, _, _ := strings.Cut(r.Header.Get("X-Forwarded-Proto"), ",")
	secure := r.TLS != nil || strings.EqualFold(strings.TrimSpace(proto), "https")
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: token, Path: "/", MaxAge: maxAge,
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode,
	})
}

func (s *Server) requireLogin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		switch r.URL.Path {
		case "/api/health", "/api/auth/session", "/api/auth/setup", "/api/auth/login":
			next.ServeHTTP(w, r)
			return
		}
		s.authMu.RLock()
		account, err := s.requestAccount(r)
		revoked := s.authChanged
		s.authMu.RUnlock()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if account == nil {
			writeError(w, http.StatusUnauthorized, errors.New("请先登录"))
			return
		}
		_, bearer := requestToken(r)
		// Cookie-authenticated mutations require a non-simple header. No host or
		// origin comparison is needed, including behind reverse proxies.
		if !bearer && r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions && r.Header.Get("X-XDownload-Request") != "1" {
			writeError(w, http.StatusForbidden, errors.New("请求缺少校验标记"))
			return
		}
		ctx := context.WithValue(r.Context(), authContextKey{}, authenticatedSession{account, revoked})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) sessionStatus(w http.ResponseWriter, r *http.Request) {
	s.authMu.RLock()
	account, err := s.store.GetAccount(r.Context())
	var authenticated bool
	if err == nil && account != nil {
		var current *storage.Account
		current, err = s.requestAccount(r)
		authenticated = current != nil && err == nil
	}
	s.authMu.RUnlock()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	result := map[string]any{"configured": account != nil, "authenticated": authenticated}
	if authenticated {
		result["username"] = account.Username
	}
	writeJSON(w, http.StatusOK, result)
}

func validatePassword(password string) error {
	if utf8.RuneCountInString(password) < 8 || len(password) > 72 {
		return storage.ErrValidation("密码至少 8 个字符，最多 72 字节")
	}
	return nil
}

func (s *Server) setupAccount(w http.ResponseWriter, r *http.Request) {
	if !s.checkLoginRate(w, r) {
		return
	}
	succeeded := false
	defer func() {
		if succeeded {
			s.clearLoginFailures(r)
		}
	}()
	if account, err := s.store.GetAccount(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	} else if account != nil {
		writeError(w, http.StatusConflict, errors.New("账号已创建，请登录"))
		return
	}
	var req struct{ Username, Password string }
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if req.Username == "" || utf8.RuneCountInString(req.Username) > 64 || strings.ContainsFunc(req.Username, unicode.IsControl) {
		writeError(w, http.StatusBadRequest, storage.ErrValidation("用户名应为 1 至 64 个字符，不能包含控制字符"))
		return
	}
	if err := validatePassword(req.Password); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	s.authMu.Lock()
	defer s.authMu.Unlock()
	if account, err := s.store.GetAccount(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	} else if account != nil {
		writeError(w, http.StatusConflict, errors.New("账号已创建，请登录"))
		return
	}
	if !s.acquireBcryptSem(r.Context()) {
		writeError(w, http.StatusRequestTimeout, r.Context().Err())
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	<-s.bcryptSem
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	account := storage.Account{Username: req.Username, PasswordHash: hash, SessionKey: randomSessionKey()}
	if err := s.store.CreateAccount(r.Context(), account); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	succeeded = s.respondLogin(w, r, account, http.StatusCreated)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !s.checkLoginRate(w, r) {
		return
	}
	succeeded := false
	defer func() {
		if succeeded {
			s.clearLoginFailures(r)
		}
	}()
	var req struct{ Username, Password string }
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	account, err := s.store.GetAccount(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	// 用户名不匹配时仍跑一次 bcrypt，防止时序枚举。
	if !s.acquireBcryptSem(r.Context()) {
		writeError(w, http.StatusRequestTimeout, r.Context().Err())
		return
	}
	if account == nil || strings.TrimSpace(req.Username) != account.Username {
		_ = bcrypt.CompareHashAndPassword(dummyBcryptHash, []byte(req.Password))
		<-s.bcryptSem
		writeError(w, http.StatusUnauthorized, errors.New("用户名或密码错误"))
		return
	}
	err = bcrypt.CompareHashAndPassword(account.PasswordHash, []byte(req.Password))
	<-s.bcryptSem
	if err != nil {
		writeError(w, http.StatusUnauthorized, errors.New("用户名或密码错误"))
		return
	}
	s.authMu.RLock()
	current, err := s.store.GetAccount(r.Context())
	if err != nil {
		s.authMu.RUnlock()
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if current == nil || !hmac.Equal(current.SessionKey, account.SessionKey) {
		s.authMu.RUnlock()
		writeError(w, http.StatusUnauthorized, errors.New("用户名或密码错误"))
		return
	}
	succeeded = s.respondLogin(w, r, *current, http.StatusOK)
	s.authMu.RUnlock()
}

func (s *Server) respondLogin(w http.ResponseWriter, r *http.Request, account storage.Account, status int) bool {
	token := signSession(account.SessionKey)
	if err := s.store.CreateSession(r.Context(), token, time.Now()); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return false
	}
	setSessionCookie(w, r, token)
	result := map[string]any{"username": account.Username}
	if r.Header.Get("X-XDownload-Request") != "1" {
		result["token"] = token
	}
	writeJSON(w, status, result)
	return true
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	token, _ := requestToken(r)
	s.authMu.Lock()
	err := s.store.DeleteSession(r.Context(), token)
	if err == nil {
		close(s.authChanged)
		s.authChanged = make(chan struct{})
	}
	s.authMu.Unlock()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	setSessionCookie(w, r, "")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	if !s.checkLoginRate(w, r) {
		return
	}
	succeeded := false
	defer func() {
		if succeeded {
			s.clearLoginFailures(r)
		}
	}()
	var req struct{ CurrentPassword, NewPassword string }
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := validatePassword(req.NewPassword); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	session := r.Context().Value(authContextKey{}).(authenticatedSession)
	if !s.acquireBcryptSem(r.Context()) {
		writeError(w, http.StatusRequestTimeout, r.Context().Err())
		return
	}
	err := bcrypt.CompareHashAndPassword(session.account.PasswordHash, []byte(req.CurrentPassword))
	<-s.bcryptSem
	if err != nil {
		writeError(w, http.StatusBadRequest, storage.ErrValidation("当前密码错误"))
		return
	}
	if !s.acquireBcryptSem(r.Context()) {
		writeError(w, http.StatusRequestTimeout, r.Context().Err())
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
	<-s.bcryptSem
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.authMu.Lock()
	err = s.store.UpdateAccountPassword(r.Context(), session.account.SessionKey, hash, randomSessionKey())
	if err == nil {
		close(s.authChanged)
		s.authChanged = make(chan struct{})
	}
	s.authMu.Unlock()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	setSessionCookie(w, r, "")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	succeeded = true
}

// dummyBcryptHash 用于用户名不匹配时跑一次假 hash，防止时序差异泄漏"用户名是否存在"。
var dummyBcryptHash, _ = bcrypt.GenerateFromPassword([]byte("dummy-timing-defense"), bcrypt.DefaultCost)

func extractRemoteHost(remoteAddr string) string {
	remote := strings.TrimSpace(remoteAddr)
	if remote == "" {
		return ""
	}
	host, _, err := net.SplitHostPort(remote)
	if err == nil && host != "" {
		return host
	}
	return strings.Trim(remote, "[]")
}

func isTrustedLoginProxy(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	raw := strings.TrimSpace(os.Getenv("OPEN_XDOWNLOAD_TRUSTED_PROXIES"))
	if raw == "" {
		return false
	}
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if _, cidr, err := net.ParseCIDR(entry); err == nil {
			if cidr.Contains(ip) {
				return true
			}
			continue
		}
		if trusted := net.ParseIP(strings.Trim(entry, "[]")); trusted != nil && trusted.Equal(ip) {
			return true
		}
	}
	return false
}

func parseForwardedIP(raw string) net.IP {
	candidate := strings.TrimSpace(raw)
	if candidate == "" {
		return nil
	}
	if h, _, err := net.SplitHostPort(candidate); err == nil && h != "" {
		candidate = h
	}
	candidate = strings.Trim(candidate, "[]")
	return net.ParseIP(candidate)
}

func loginClientKey(r *http.Request) string {
	if r == nil {
		return "unknown"
	}
	host := extractRemoteHost(r.RemoteAddr)
	if host == "" {
		return "unknown"
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return host
	}
	// 仅在连接来自受信任反向代理（本地回环或 OPEN_XDOWNLOAD_TRUSTED_PROXIES 配置的代理）时
	// 采信 X-Forwarded-For / X-Real-IP，并从右向左跳过受信任代理跳点取真实对端地址，
	// 避免客户端伪造左侧头绕过限速，同时防止反代后所有用户共享同一个代理桶而相互锁死或误清他人失败记录。
	if isTrustedLoginProxy(ip) {
		if forwarded := forwardedClientIP(r.Header.Values("X-Forwarded-For")); forwarded != "" {
			return forwarded
		}
		if realIP := parseForwardedIP(r.Header.Get("X-Real-IP")); realIP != nil {
			return realIP.String()
		}
	}
	return ip.String()
}

func forwardedClientIP(headers []string) string {
	var fallback string
	for i := len(headers) - 1; i >= 0; i-- {
		parts := strings.Split(headers[i], ",")
		for j := len(parts) - 1; j >= 0; j-- {
			ip := parseForwardedIP(parts[j])
			if ip == nil {
				continue
			}
			if !isTrustedLoginProxy(ip) {
				return ip.String()
			}
			if fallback == "" {
				fallback = ip.String()
			}
		}
	}
	return fallback
}

// evictLoginClientBucket 在活跃桶达到上限时淘汰一个旧桶，避免攻击者塞满 4096 个
// 来源地址造成全局登录 DoS，同时保持内存有界。优先淘汰尚未触发限速的最旧桶，
// 防止用大量单次探测顶掉已被封禁来源的记录；若全部都已触发限速，再淘汰最旧的桶。
func evictLoginClientBucket(buckets map[string][]time.Time, cutoff time.Time) {
	var oldestUnlockedKey string
	var oldestUnlockedTime time.Time
	hasOldestUnlocked := false
	var oldestKey string
	var oldestTime time.Time
	hasOldest := false
	for client, attempts := range buckets {
		if len(attempts) == 0 {
			delete(buckets, client)
			return
		}
		last := attempts[len(attempts)-1]
		if !hasOldest || last.Before(oldestTime) {
			oldestKey = client
			oldestTime = last
			hasOldest = true
		}
		active := 0
		for _, t := range attempts {
			if t.After(cutoff) {
				active++
			}
		}
		if active < maxLoginAttempts && (!hasOldestUnlocked || last.Before(oldestUnlockedTime)) {
			oldestUnlockedKey = client
			oldestUnlockedTime = last
			hasOldestUnlocked = true
		}
	}
	if hasOldestUnlocked {
		delete(buckets, oldestUnlockedKey)
		return
	}
	if hasOldest {
		delete(buckets, oldestKey)
	}
}

// checkLoginRate 在检查时预留额度，失败及尚未完成的请求都计入来源地址限额。
func (s *Server) checkLoginRate(w http.ResponseWriter, r *http.Request) bool {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	if s.loginAttempts == nil {
		s.loginAttempts = make(map[string][]time.Time)
	}
	key := loginClientKey(r)
	now := time.Now()
	cutoff := now.Add(-loginRateWindow)
	for client, attempts := range s.loginAttempts {
		if len(attempts) == 0 || !attempts[len(attempts)-1].After(cutoff) {
			delete(s.loginAttempts, client)
		}
	}
	attempts := s.loginAttempts[key]
	valid := 0
	for _, t := range attempts {
		if t.After(cutoff) {
			attempts[valid] = t
			valid++
		}
	}
	attempts = attempts[:valid]
	if len(attempts) >= maxLoginAttempts {
		s.loginAttempts[key] = attempts
		writeError(w, http.StatusTooManyRequests, errors.New("登录尝试过于频繁，请稍后再试"))
		return false
	}
	if len(attempts) == 0 {
		for len(s.loginAttempts) >= maxLoginClients {
			evictLoginClientBucket(s.loginAttempts, cutoff)
		}
	}
	s.loginAttempts[key] = append(attempts, now)
	return true
}

func (s *Server) clearLoginFailures(r *http.Request) {
	s.loginMu.Lock()
	delete(s.loginAttempts, loginClientKey(r))
	s.loginMu.Unlock()
}
