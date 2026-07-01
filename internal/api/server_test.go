package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fuck-chat-img/fci/internal/auth"
	"github.com/fuck-chat-img/fci/internal/cache"
	"github.com/fuck-chat-img/fci/internal/config"
	"github.com/fuck-chat-img/fci/internal/model"
	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
)

func bcryptGenerate(plain string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	return string(b), err
}

func setupTestServer(t *testing.T) *gin.Engine {
	t.Helper()
	if err := model.InitTestDB("file::memory:?cache=shared"); err != nil {
		t.Fatal(err)
	}
	db := model.DB
	db.Exec("DELETE FROM users")
	db.Exec("DELETE FROM model_groups")
	db.Exec("DELETE FROM histories")
	cache.Init()
	config.SetWebDirForTest("")
	config.SetJWTSecretForTest("test-jwt-secret-for-unit-tests")
	config.SetProxyKeyForTest("")
	sameOriginHosts.Store(nil)
	return SetupRouter()
}

func loginAsUser(t *testing.T, r *gin.Engine, username, password, role string) *http.Cookie {
	t.Helper()
	hash, err := bcryptGenerate(password)
	if err != nil {
		t.Fatal(err)
	}
	u := model.User{Username: username, PasswordHash: hash, Role: role, Status: 1}
	if err := model.DB.Create(&u).Error; err != nil {
		t.Fatal(err)
	}
	body := `{"username":"` + username + `","password":"` + password + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("登录失败: %d body=%s", w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	for _, c := range cookies {
		if c.Name == auth.CookieName {
			return c
		}
	}
	t.Fatal("登录响应中未找到 auth cookie")
	return nil
}

func TestEmbeddedSPA(t *testing.T) {
	r := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("根路径应返回200, 实际 %d", w.Code)
	}
	req3 := httptest.NewRequest(http.MethodGet, "/console/groups", nil)
	w3 := httptest.NewRecorder()
	r.ServeHTTP(w3, req3)
	if w3.Code != http.StatusOK {
		t.Errorf("前端路由应回退 index.html, 实际 %d", w3.Code)
	}
}

func TestLoginFlow(t *testing.T) {
	r := setupTestServer(t)
	hash, err := bcryptGenerate("123456")
	if err != nil {
		t.Fatal(err)
	}
	u := model.User{Username: "admin", PasswordHash: hash, Role: "admin", Status: 1}
	if err := model.DB.Create(&u).Error; err != nil {
		t.Fatal(err)
	}
	body := `{"username":"admin","password":"123456"}`
	req := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("登录应成功, 实际 %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "token") {
		t.Errorf("登录响应应包含 token, body=%s", w.Body.String())
	}
}

func TestSetupFlow(t *testing.T) {
	r := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status 应返回 200, 实际 %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"need_setup":true`) {
		t.Errorf("无用户时 need_setup 应为 true, body=%s", w.Body.String())
	}
	body := `{"username":"admin","password":"mypassword"}`
	req2 := httptest.NewRequest(http.MethodPost, "/api/setup", strings.NewReader(body))
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("setup 应返回 200, 实际 %d body=%s", w2.Code, w2.Body.String())
	}
	req3 := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	w3 := httptest.NewRecorder()
	r.ServeHTTP(w3, req3)
	if !strings.Contains(w3.Body.String(), `"need_setup":false`) {
		t.Errorf("设置后 need_setup 应为 false, body=%s", w3.Body.String())
	}
	req4 := httptest.NewRequest(http.MethodPost, "/api/setup", strings.NewReader(body))
	req4.Header.Set("Content-Type", "application/json")
	w4 := httptest.NewRecorder()
	r.ServeHTTP(w4, req4)
	if w4.Code != http.StatusConflict {
		t.Errorf("已有用户时 setup 应返回 409, 实际 %d body=%s", w4.Code, w4.Body.String())
	}
	req5 := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(body))
	req5.Header.Set("Content-Type", "application/json")
	w5 := httptest.NewRecorder()
	r.ServeHTTP(w5, req5)
	if w5.Code != http.StatusOK {
		t.Errorf("新管理员登录应成功, 实际 %d body=%s", w5.Code, w5.Body.String())
	}
}

func TestSetupValidation(t *testing.T) {
	r := setupTestServer(t)
	cases := []struct {
		name string
		body string
		want int
	}{
		{"空用户名", `{"username":"","password":"mypassword"}`, http.StatusBadRequest},
		{"短密码", `{"username":"admin","password":"123"}`, http.StatusBadRequest},
		{"无密码", `{"username":"admin","password":""}`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/setup", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != tc.want {
				t.Errorf("%s: 期望 %d, 实际 %d body=%s", tc.name, tc.want, w.Code, w.Body.String())
			}
		})
	}
}

func TestCORSSameOrigin(t *testing.T) {
	r := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	req.Host = "localhost"
	req.Header.Set("Origin", "http://localhost")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Header().Get("Access-Control-Allow-Origin") != "http://localhost" {
		t.Errorf("同源请求应返回 Access-Control-Allow-Origin, headers=%v", w.Header())
	}
	if w.Header().Get("Vary") != "Origin" {
		t.Errorf("应设置 Vary: Origin, got %q", w.Header().Get("Vary"))
	}
}

func TestCORSCrossOriginRejected(t *testing.T) {
	r := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	req.Host = "localhost"
	req.Header.Set("Origin", "http://evil.com")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("跨域请求不应返回 Access-Control-Allow-Origin, got %q", w.Header().Get("Access-Control-Allow-Origin"))
	}
	if w.Header().Get("Vary") != "Origin" {
		t.Errorf("即使跨域拒绝也应设置 Vary: Origin, got %q", w.Header().Get("Vary"))
	}
}

func TestCORSPrefixBypass(t *testing.T) {
	r := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	req.Host = "localhost"
	req.Header.Set("Origin", "http://localhost.evil.com")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("前缀绕过攻击应被拒绝, got Access-Control-Allow-Origin=%q", w.Header().Get("Access-Control-Allow-Origin"))
	}
}

func TestCORSWhiteList(t *testing.T) {
	r := setupTestServer(t)
	SetSameOriginHosts([]string{"http://trusted.example.com"})
	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	req.Host = "localhost"
	req.Header.Set("Origin", "http://trusted.example.com")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Header().Get("Access-Control-Allow-Origin") != "http://trusted.example.com" {
		t.Errorf("白名单 Origin 应被允许, got %q", w.Header().Get("Access-Control-Allow-Origin"))
	}
}

func TestCorsOptions(t *testing.T) {
	r := setupTestServer(t)
	req := httptest.NewRequest(http.MethodOptions, "/api/status", nil)
	req.Host = "localhost"
	req.Header.Set("Origin", "http://localhost")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Errorf("OPTIONS 请求应返回 204, 实际 %d", w.Code)
	}
}

func TestSecurityHeaders(t *testing.T) {
	r := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	expected := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "strict-origin-when-cross-origin",
	}
	for k, v := range expected {
		if got := w.Header().Get(k); got != v {
			t.Errorf("Header %s: 期望 %q, 实际 %q", k, v, got)
		}
	}
}

func TestNoRouteAPI404(t *testing.T) {
	r := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/nonexistent", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("/api/nonexistent 应返回 404, 实际 %d", w.Code)
	}
	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("/api 404 应返回 JSON, Content-Type=%q", ct)
	}
}

func TestNoRouteV1404(t *testing.T) {
	r := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/nonexistent", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("/v1/nonexistent 应返回 404, 实际 %d", w.Code)
	}
	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("/v1 404 应返回 JSON, Content-Type=%q", ct)
	}
}

func TestNoRouteFrontend(t *testing.T) {
	r := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/console/somepage", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("前端路由应返回 SPA(200), 实际 %d", w.Code)
	}
}

func TestStaticPathTraversal(t *testing.T) {
	r := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/static/../../../etc/passwd", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("path traversal 应返回 404, 实际 %d", w.Code)
	}
}

func TestRateLimit(t *testing.T) {
	r := setupTestServer(t)
	var lastCode int
	for i := 0; i < 11; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/login", nil)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		lastCode = w.Code
	}
	if lastCode != http.StatusTooManyRequests {
		t.Errorf("第11次请求应返回 429, 实际 %d", lastCode)
	}
}

func TestAdminMiddlewareRejects(t *testing.T) {
	r := setupTestServer(t)
	cookie := loginAsUser(t, r, "normaluser", "password123", "user")

	req := httptest.NewRequest(http.MethodPost, "/api/groups", strings.NewReader(`{"name":"test"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("非 admin 创建 group 应返回 403, 实际 %d body=%s", w.Code, w.Body.String())
	}
}

func TestAuthRequired(t *testing.T) {
	r := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/user", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("未登录访问 /api/user 应返回 401, 实际 %d", w.Code)
	}
}

func TestLogoutClearsCookie(t *testing.T) {
	r := setupTestServer(t)
	cookie := loginAsUser(t, r, "admin", "password123", "admin")

	req := httptest.NewRequest(http.MethodPost, "/api/logout", nil)
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("logout 应返回 200, 实际 %d", w.Code)
	}
	cookies := w.Result().Cookies()
	var cleared bool
	for _, c := range cookies {
		if c.Name == auth.CookieName {
			if c.MaxAge < 0 {
				cleared = true
			}
		}
	}
	if !cleared {
		t.Errorf("logout 应清除 cookie (MaxAge=-1), cookies=%v", cookies)
	}
}

func TestChangePassword(t *testing.T) {
	r := setupTestServer(t)
	cookie := loginAsUser(t, r, "admin", "oldpassword", "admin")

	body := `{"old_password":"oldpassword","new_password":"newpassword123"}`
	req := httptest.NewRequest(http.MethodPost, "/api/user/password", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("修改密码应成功, 实际 %d body=%s", w.Code, w.Body.String())
	}

	req2 := httptest.NewRequest(http.MethodGet, "/api/user", nil)
	req2.AddCookie(cookie)
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)
	if w2.Code != http.StatusUnauthorized {
		t.Errorf("旧 token 应失效, 实际 %d body=%s", w2.Code, w2.Body.String())
	}
}

func TestTrailingSlash(t *testing.T) {
	r := setupTestServer(t)
	cookie := loginAsUser(t, r, "admin", "password123", "admin")

	paths := []string{"/v1/chat/completions", "/v1/chat/completions/"}
	for _, p := range paths {
		req := httptest.NewRequest(http.MethodPost, p, strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(cookie)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code == http.StatusNotFound {
			t.Errorf("路径 %s 应匹配路由而非 404", p)
		}
	}
}
