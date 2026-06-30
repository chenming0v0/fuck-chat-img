package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fuck-chat-img/fci/internal/cache"
	"github.com/fuck-chat-img/fci/internal/config"
	"github.com/fuck-chat-img/fci/internal/model"
	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func bcryptGenerate(plain string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	return string(b), err
}

func setupTestServer(t *testing.T) *gin.Engine {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared&_busy_timeout=5000"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	db.AutoMigrate(&model.User{}, &model.ModelGroup{}, &model.History{})
	db.Exec("DELETE FROM users")
	model.DB = db
	cache.Init()
	config.Get().WebDir = "" // 强制使用嵌入前端
	return SetupRouter()
}

func TestEmbeddedSPA(t *testing.T) {
	r := setupTestServer(t)
	// 根路径应返回 200(可能返回真实 index.html 或占位提示)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("根路径应返回200, 实际 %d", w.Code)
	}
	// 未知前端路由应回退到 index.html(SPA history 模式)
	req3 := httptest.NewRequest(http.MethodGet, "/console/groups", nil)
	w3 := httptest.NewRecorder()
	r.ServeHTTP(w3, req3)
	if w3.Code != http.StatusOK {
		t.Errorf("前端路由应回退 index.html, 实际 %d", w3.Code)
	}
}

func TestLoginFlow(t *testing.T) {
	r := setupTestServer(t)
	// 用 bcrypt 现生成 "123456" 的哈希
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

// TestSetupFlow 验证首次启动设置管理员流程
// 1. 无用户时 /api/status 返回 need_setup=true
// 2. /api/setup 可创建管理员
// 3. 设置后 /api/status 返回 need_setup=false
// 4. 已有用户时 /api/setup 拒绝(409)
func TestSetupFlow(t *testing.T) {
	r := setupTestServer(t)
	// 1. 初始无用户: need_setup 应为 true
	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status 应返回 200, 实际 %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"need_setup":true`) {
		t.Errorf("无用户时 need_setup 应为 true, body=%s", w.Body.String())
	}
	// 2. 首次设置管理员
	body := `{"username":"admin","password":"mypassword"}`
	req2 := httptest.NewRequest(http.MethodPost, "/api/setup", strings.NewReader(body))
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("setup 应返回 200, 实际 %d body=%s", w2.Code, w2.Body.String())
	}
	// 3. 设置后 need_setup 应为 false
	req3 := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	w3 := httptest.NewRecorder()
	r.ServeHTTP(w3, req3)
	if !strings.Contains(w3.Body.String(), `"need_setup":false`) {
		t.Errorf("设置后 need_setup 应为 false, body=%s", w3.Body.String())
	}
	// 4. 已有用户时再次 setup 应被拒绝(409)
	req4 := httptest.NewRequest(http.MethodPost, "/api/setup", strings.NewReader(body))
	req4.Header.Set("Content-Type", "application/json")
	w4 := httptest.NewRecorder()
	r.ServeHTTP(w4, req4)
	if w4.Code != http.StatusConflict {
		t.Errorf("已有用户时 setup 应返回 409, 实际 %d body=%s", w4.Code, w4.Body.String())
	}
	// 5. 用新设置的密码应能登录
	req5 := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(body))
	req5.Header.Set("Content-Type", "application/json")
	w5 := httptest.NewRecorder()
	r.ServeHTTP(w5, req5)
	if w5.Code != http.StatusOK {
		t.Errorf("新管理员登录应成功, 实际 %d body=%s", w5.Code, w5.Body.String())
	}
}

// TestSetupValidation 验证 setup 接口的参数校验
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
