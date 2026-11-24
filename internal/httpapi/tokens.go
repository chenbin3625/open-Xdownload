package httpapi

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/chenbin3625/open-Xdownload/internal/storage"
)

func (s *Server) listAPITokens(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListAPITokens(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) createAPIToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name          string `json:"name"`
		Scopes        string `json:"scopes"`
		ExpiresInDays int    `json:"expiresInDays"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var expiresAt *time.Time
	if req.ExpiresInDays > 0 {
		t := time.Now().UTC().AddDate(0, 0, min(req.ExpiresInDays, 3650))
		expiresAt = &t
	}
	token, plaintext, err := s.store.CreateAPIToken(r.Context(), req.Name, req.Scopes, expiresAt)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"token":     token,
		"plaintext": plaintext,
	})
}

func (s *Server) deleteAPIToken(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.store.DeleteAPIToken(r.Context(), id); err != nil {
		writeStorageError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) serveUserscript(w http.ResponseWriter, r *http.Request) {
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https") {
		scheme = "https"
	}
	origin := fmt.Sprintf("%s://%s", scheme, r.Host)
	script := fmt.Sprintf(`// ==UserScript==
// @name         open-Xdownload Companion
// @namespace    open-xdownload
// @version      1.0.0
// @description  One-click archive X tweets, threads, users & sync cookies to open-Xdownload
// @match        https://x.com/*
// @match        https://twitter.com/*
// @grant        GM_xmlhttpRequest
// @grant        GM_getValue
// @grant        GM_setValue
// @grant        GM_registerMenuCommand
// @grant        GM_cookie
// @connect      *
// ==/UserScript==

(function () {
  "use strict";
  const DEFAULT_BASE = %q;

  function getBase() {
    return (GM_getValue("oxd_base_url", DEFAULT_BASE) || DEFAULT_BASE).replace(/\/+$/, "");
  }
  function getToken() {
    return GM_getValue("oxd_api_token", "");
  }
  function ensureConfig() {
    let base = getBase();
    let token = getToken();
    if (!token) {
      base = window.prompt("open-Xdownload Server URL:", base) || base;
      token = window.prompt("open-Xdownload API Token (oxd_...):", token) || "";
      GM_setValue("oxd_base_url", base.trim());
      GM_setValue("oxd_api_token", token.trim());
    }
    return { base: getBase(), token: getToken() };
  }

  function apiCall(path, method, payload, onDone) {
    const { base, token } = ensureConfig();
    if (!token) {
      window.alert("请先在 open-Xdownload 系统配置中创建 API Token");
      return;
    }
    GM_xmlhttpRequest({
      method,
      url: base + path,
      headers: {
        "Content-Type": "application/json",
        "Authorization": "Bearer " + token,
      },
      data: payload ? JSON.stringify(payload) : undefined,
      onload(res) {
        if (res.status >= 200 && res.status < 300) {
          onDone(null, res.responseText);
        } else {
          onDone(new Error("HTTP " + res.status + ": " + res.responseText));
        }
      },
      onerror(err) {
        onDone(new Error("网络请求失败: " + JSON.stringify(err)));
      },
    });
  }

  function toast(msg) {
    const el = document.createElement("div");
    el.textContent = msg;
    Object.assign(el.style, {
      position: "fixed", right: "20px", bottom: "20px", zIndex: "999999",
      background: "#111827", color: "#fff", padding: "10px 14px", borderRadius: "8px",
      fontSize: "13px", boxShadow: "0 6px 20px rgba(0,0,0,0.3)",
    });
    document.body.appendChild(el);
    setTimeout(() => el.remove(), 3000);
  }

  function submitCurrentPage(kindOverride) {
    const url = window.location.href;
    let kind = kindOverride || "tweet_link";
    let input = url;
    if (!kindOverride) {
      if (/\/status\/\d+/.test(url)) {
        kind = "tweet_link";
      } else if (/\/i\/lists\/\d+/.test(url)) {
        kind = "list";
      } else {
        kind = "user";
      }
    }
    apiCall("/api/jobs", "POST", { kind, input, archiveMode: "both" }, (err) => {
      if (err) toast("归档失败: " + err.message);
      else toast("已提交到 open-Xdownload (" + kind + ")");
    });
  }

  function syncCookies() {
    if (typeof GM_cookie === "undefined" || !GM_cookie.list) {
      window.alert("当前脚本管理器不支持 GM_cookie（需 Tampermonkey Beta 或支持 GM_cookie 的管理器）。请从开发者工具复制 auth_token 和 ct0。");
      return;
    }
    GM_cookie.list({ domain: ".x.com" }, (cookies, err) => {
      if (err || !cookies) {
        toast("读取 Cookie 失败");
        return;
      }
      const auth = cookies.find((c) => c.name === "auth_token");
      const ct0 = cookies.find((c) => c.name === "ct0");
      if (!auth || !ct0) {
        toast("未找到 auth_token 或 ct0，请确认已登录 X");
        return;
      }
      const cookieStr = "auth_token=" + auth.value + "; ct0=" + ct0.value;
      apiCall("/api/x-accounts", "POST", { cookie: cookieStr }, (apiErr) => {
        if (apiErr) toast("同步 Cookie 失败: " + apiErr.message);
        else toast("X Cookie 已同步到 open-Xdownload 账号池");
      });
    });
  }

  GM_registerMenuCommand("配置服务器与 API Token", () => {
    const base = window.prompt("open-Xdownload Server URL:", getBase());
    if (base !== null) GM_setValue("oxd_base_url", base.trim());
    const token = window.prompt("open-Xdownload API Token (oxd_...):", getToken());
    if (token !== null) GM_setValue("oxd_api_token", token.trim());
  });
  GM_registerMenuCommand("归档当前页面到 open-Xdownload", () => submitCurrentPage());
  GM_registerMenuCommand("归档当前长串 (Thread)", () => submitCurrentPage("thread"));
  GM_registerMenuCommand("同步当前 X Cookie 到账号池", syncCookies);

  // 在每条推文底部注入快捷归档按钮
  const observer = new MutationObserver(() => {
    document.querySelectorAll('article[data-testid="tweet"]').forEach((article) => {
      if (article.dataset.oxdBound) return;
      article.dataset.oxdBound = "1";
      const link = article.querySelector('a[href*="/status/"]');
      const group = article.querySelector('[role="group"]');
      if (!link || !group) return;
      const btn = document.createElement("button");
      btn.type = "button";
      btn.textContent = "归档";
      btn.title = "发送到 open-Xdownload（按住 Shift 归档完整长串）";
      Object.assign(btn.style, {
        marginLeft: "8px", padding: "2px 8px", borderRadius: "999px",
        border: "1px solid currentColor", background: "transparent",
        color: "inherit", fontSize: "12px", cursor: "pointer", opacity: "0.75",
      });
      btn.addEventListener("click", (e) => {
        e.preventDefault();
        e.stopPropagation();
        const tweetURL = new URL(link.getAttribute("href"), window.location.origin).toString();
        const kind = e.shiftKey ? "thread" : "tweet_link";
        apiCall("/api/jobs", "POST", { kind, input: tweetURL, archiveMode: "both" }, (err) => {
          if (err) toast("归档失败: " + err.message);
          else toast(e.shiftKey ? "长串归档任务已入队" : "推文归档任务已入队");
        });
      });
      group.appendChild(btn);
    });
  });
  observer.observe(document.documentElement, { childList: true, subtree: true });
})();
`, origin)

	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="open-xdownload.user.js"`)
	_, _ = w.Write([]byte(script))
}

func writeStorageError(w http.ResponseWriter, err error) {
	var uf *storage.UserFacingError
	if err != nil {
		if ok := asUserFacingError(err, &uf); ok {
			writeError(w, uf.Status, err)
			return
		}
	}
	writeError(w, http.StatusInternalServerError, err)
}

func asUserFacingError(err error, target **storage.UserFacingError) bool {
	for err != nil {
		if uf, ok := err.(*storage.UserFacingError); ok {
			*target = uf
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			break
		}
		err = u.Unwrap()
	}
	return false
}
