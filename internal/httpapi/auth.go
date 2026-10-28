package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/chenbin3625/open-Xdownload/internal/storage"
	"golang.org/x/crypto/bcrypt"
)

const sessionCookieName = "open_xdownload_session"
const sessionCookieMaxAge = 400 * 24 * 60 * 60

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
	account, err := s.store.GetAccount(r.Context())
	if err != nil {
		return nil, err
	}
	token, _ := requestToken(r)
	if account == nil || !validSession(token, account.SessionKey) {
		return nil, nil
	}
	return account, nil
}

func setSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	maxAge := sessionCookieMaxAge
	if token == "" {
		maxAge = -1
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: token, Path: "/", MaxAge: maxAge,
		HttpOnly: true, Secure: r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https"), SameSite: http.SameSiteStrictMode,
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
	token, _ := requestToken(r)
	authenticated := err == nil && account != nil && validSession(token, account.SessionKey)
	s.authMu.RUnlock()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	result := map[string]any{"configured": account != nil, "authenticated": authenticated}
	if authenticated {
		result["username"] = account.Username
		// Renew browser cookie storage while the underlying token has no expiry.
		setSessionCookie(w, r, token)
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
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	account := storage.Account{Username: req.Username, PasswordHash: hash, SessionKey: randomSessionKey()}
	if err := s.store.CreateAccount(r.Context(), account); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.respondLogin(w, r, account, http.StatusCreated)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
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
	if account == nil || strings.TrimSpace(req.Username) != account.Username || bcrypt.CompareHashAndPassword(account.PasswordHash, []byte(req.Password)) != nil {
		writeError(w, http.StatusUnauthorized, errors.New("用户名或密码错误"))
		return
	}
	s.respondLogin(w, r, *account, http.StatusOK)
}

func (s *Server) respondLogin(w http.ResponseWriter, r *http.Request, account storage.Account, status int) {
	token := signSession(account.SessionKey)
	setSessionCookie(w, r, token)
	writeJSON(w, status, map[string]any{"username": account.Username, "token": token})
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
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
	if bcrypt.CompareHashAndPassword(session.account.PasswordHash, []byte(req.CurrentPassword)) != nil {
		writeError(w, http.StatusBadRequest, storage.ErrValidation("当前密码错误"))
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
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
}
