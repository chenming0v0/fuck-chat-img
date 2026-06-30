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
