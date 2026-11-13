package xclient

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// 接口返回 401 但 /home 校验通过：说明是该接口/目标的问题（例如 X 停用旧路径），
// 不能把仍然有效的 Cookie 禁用。
func TestEndpointUnauthorizedWithValidCookieDoesNotDisable(t *testing.T) {
	var homeChecks atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/home" {
			homeChecks.Add(1)
			_, _ = w.Write([]byte(`{"screen_name":"owner"}`))
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	client := newHeaderTestClient(server)
	for i := 0; i < 3; i++ {
		if _, err := getUserByInput(context.Background(), client, "alice"); !IsUnauthorized(err) {
			t.Fatalf("error = %v, want 401", err)
		}
		if client.isDisabled() {
			t.Fatal("valid cookie was disabled after an endpoint-level 401")
		}
	}
	// 短时间内重复 401 不应每次都去撞 /home。
	if got := homeChecks.Load(); got != 1 {
		t.Fatalf("home checks = %d, want 1", got)
	}
}

// /home 也 401：Cookie 确实失效，禁用；冷却期不应再长达 30 分钟。
func TestUnauthorizedConfirmedByHomeDisablesWithModerateCooldown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	client := newHeaderTestClient(server)
	if _, err := getUserByInput(context.Background(), client, "alice"); err == nil {
		t.Fatal("expected 401 error")
	}
	client.mu.Lock()
	remaining := time.Until(client.disabledUntil)
	client.mu.Unlock()
	if remaining < 9*time.Minute || remaining > 11*time.Minute {
		t.Fatalf("confirmed 401 cooldown = %s, want about 10m", remaining)
	}
}

// /home 无法给出结论（网络错误/挑战页）时按短冷却处理，不锁 30 分钟。
func TestUnauthorizedWithInconclusiveHomeUsesShortCooldown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/home" {
			_, _ = w.Write([]byte(`<html>challenge</html>`))
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	client := newHeaderTestClient(server)
	if _, err := getUserByInput(context.Background(), client, "alice"); err == nil {
		t.Fatal("expected 401 error")
	}
	client.mu.Lock()
	remaining := time.Until(client.disabledUntil)
	client.mu.Unlock()
	if remaining < 4*time.Minute || remaining > 6*time.Minute {
		t.Fatalf("inconclusive 401 cooldown = %s, want about 5m", remaining)
	}
}

// 守卫：所有指向 X API 的路径字面量都必须以 /i/api/ 开头。X 已对裸 /graphql/ 返回 401，
// 这类回归只会在线上表现为"Cookie 失效"，必须在测试阶段拦住。
func TestAllAPIPathLiteralsUseIAPIPrefix(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		source, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, name, source, 0)
		if err != nil {
			t.Fatal(err)
		}
		// strings.Contains/HasPrefix 等的参数是子串匹配，不是请求路径。
		matchers := map[*ast.BasicLit]bool{}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "strings" {
					for _, arg := range call.Args {
						if lit, ok := arg.(*ast.BasicLit); ok {
							matchers[lit] = true
						}
					}
				}
			}
			return true
		})
		ast.Inspect(file, func(node ast.Node) bool {
			lit, ok := node.(*ast.BasicLit)
			if ok && matchers[lit] {
				return true
			}
			if !ok || lit.Kind != token.STRING {
				return true
			}
			value, err := strconv.Unquote(lit.Value)
			if err != nil || !strings.HasPrefix(value, "/") {
				return true
			}
			if strings.Contains(value, "graphql/") || strings.Contains(value, "/1.1/") || strings.Contains(value, "/2/") {
				checked++
				if !strings.HasPrefix(value, "/i/api/") {
					t.Errorf("%s: API path %q must start with /i/api/", fset.Position(lit.Pos()), value)
				}
			}
			return true
		})
	}
	if checked == 0 {
		t.Fatal("guard found no API path literals; scanner is broken")
	}
}
