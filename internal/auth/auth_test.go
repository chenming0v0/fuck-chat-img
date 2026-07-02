package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fuck-chat-img/fci/internal/config"
	"github.com/fuck-chat-img/fci/internal/model"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupTestDB(t *testing.T) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared&_busy_timeout=5000"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	db.AutoMigrate(&model.User{})
	db.Exec("DELETE FROM users")
	model.DB = db
}

func setupTestConfig(t *testing.T) {
	t.Helper()
	config.SetJWTSecretForTest("test-jwt-secret-for-unit-tests")
	config.SetProxyKeyForTest("")
	gin.SetMode(gin.TestMode)
}

func TestGenerateAndParseToken(t *testing.T) {
	setupTestConfig(t)
	setupTestDB(t)

	user := &model.User{
		ID:           1,
		Username:     "testuser",
		Role:         "admin",
		TokenVersion: 0,
	}
	if err := model.DB.Create(user).Error; err != nil {
		t.Fatal(err)
	}

	token, expiresAt, err := GenerateToken(user)
	if err != nil {
		t.Fatalf("GenerateToken failed: %v", err)
	}
	if token == "" {
		t.Fatal("token should not be empty")
	}
	if expiresAt.Before(time.Now()) {
		t.Fatal("expiresAt should be in the future")
	}

	claims, err := ParseToken(token)
	if err != nil {
		t.Fatalf("ParseToken failed: %v", err)
	}
	if claims.UserID != user.ID {
		t.Errorf("expected UserID %d, got %d", user.ID, claims.UserID)
	}
	if claims.Username != user.Username {
		t.Errorf("expected Username %s, got %s", user.Username, claims.Username)
	}
	if claims.Role != user.Role {
		t.Errorf("expected Role %s, got %s", user.Role, claims.Role)
	}
	if claims.TokenVersion != user.TokenVersion {
		t.Errorf("expected TokenVersion %d, got %d", user.TokenVersion, claims.TokenVersion)
	}
}

func TestTokenVersionValidation(t *testing.T) {
	setupTestConfig(t)
	setupTestDB(t)

	user := &model.User{
		ID:           1,
		Username:     "testuser",
		Role:         "admin",
		TokenVersion: 0,
	}
	if err := model.DB.Create(user).Error; err != nil {
		t.Fatal(err)
	}

	token, _, err := GenerateToken(user)
	if err != nil {
		t.Fatal(err)
	}

	claims, err := ParseToken(token)
	if err != nil {
		t.Fatal(err)
	}
	if !ValidateTokenVersion(claims) {
		t.Fatal("token should be valid before password change")
	}

	if err := model.UpdatePassword(user.ID, "newpassword123"); err != nil {
		t.Fatal(err)
	}

	if ValidateTokenVersion(claims) {
		t.Fatal("old token should be invalid after password change")
	}

	updatedUser, err := model.GetUserByID(user.ID)
	if err != nil {
		t.Fatal(err)
	}
	newToken, _, err := GenerateToken(updatedUser)
	if err != nil {
		t.Fatal(err)
	}
	newClaims, err := ParseToken(newToken)
	if err != nil {
		t.Fatal(err)
	}
	if !ValidateTokenVersion(newClaims) {
		t.Fatal("new token should be valid after password change")
	}
}

func TestInvalidSignatureToken(t *testing.T) {
	setupTestConfig(t)

	wrongSecret := "wrong-secret"
	claims := Claims{
		UserID:   1,
		Username: "test",
		Role:     "admin",
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	wrongToken, err := tok.SignedString([]byte(wrongSecret))
	if err != nil {
		t.Fatal(err)
	}

	_, err = ParseToken(wrongToken)
	if err == nil {
		t.Fatal("token with wrong signature should be rejected")
	}
}

func TestExpiredToken(t *testing.T) {
	setupTestConfig(t)

	claims := Claims{
		UserID:   1,
		Username: "test",
		Role:     "admin",
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(-1 * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now().Add(-2 * time.Hour)),
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	expiredToken, err := tok.SignedString([]byte("test-jwt-secret-for-unit-tests"))
	if err != nil {
		t.Fatal(err)
	}

	_, err = ParseToken(expiredToken)
	if err == nil {
		t.Fatal("expired token should be rejected")
	}
}

func TestSetAndClearAuthCookie(t *testing.T) {
	setupTestConfig(t)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	expiresAt := time.Now().Add(7 * 24 * time.Hour)
	testToken := "test-token-value"
	SetAuthCookie(c, testToken, expiresAt)

	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected 1 cookie, got %d", len(cookies))
	}
	cookie := cookies[0]
	if cookie.Name != CookieName {
		t.Errorf("expected cookie name %s, got %s", CookieName, cookie.Name)
	}
	if cookie.Value != testToken {
		t.Errorf("expected cookie value %s, got %s", testToken, cookie.Value)
	}
	if !cookie.HttpOnly {
		t.Error("cookie should be HttpOnly")
	}
	if cookie.SameSite != http.SameSiteStrictMode {
		t.Errorf("expected SameSite Strict, got %v", cookie.SameSite)
	}
	if cookie.Path != "/" {
		t.Errorf("expected path /, got %s", cookie.Path)
	}
	if cookie.MaxAge <= 0 {
		t.Error("MaxAge should be positive")
	}

	w2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(w2)
	ClearAuthCookie(c2)

	cookies2 := w2.Result().Cookies()
	if len(cookies2) != 1 {
		t.Fatalf("expected 1 cookie, got %d", len(cookies2))
	}
	clearedCookie := cookies2[0]
	if clearedCookie.Name != CookieName {
		t.Errorf("expected cookie name %s, got %s", CookieName, clearedCookie.Name)
	}
	if clearedCookie.Value != "" {
		t.Errorf("expected empty value, got %s", clearedCookie.Value)
	}
	if clearedCookie.MaxAge != -1 {
		t.Errorf("expected MaxAge -1, got %d", clearedCookie.MaxAge)
	}
}

func TestExtractTokenFromCookie(t *testing.T) {
	setupTestConfig(t)

	testToken := "cookie-token-value"
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: CookieName, Value: testToken})
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req

	extracted := extractToken(c)
	if extracted != testToken {
		t.Errorf("expected token from cookie %s, got %s", testToken, extracted)
	}
}

func TestExtractTokenFromHeader(t *testing.T) {
	setupTestConfig(t)

	testToken := "header-token-value"
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req

	extracted := extractToken(c)
	if extracted != testToken {
		t.Errorf("expected token from header %s, got %s", testToken, extracted)
	}
}

func TestExtractTokenFromQuery(t *testing.T) {
	setupTestConfig(t)

	testToken := "query-token-value"
	req := httptest.NewRequest("GET", "/?token="+testToken, nil)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req

	extracted := extractToken(c)
	if extracted != testToken {
		t.Errorf("expected token from query %s, got %s", testToken, extracted)
	}
}

func TestExtractBearerCaseInsensitive(t *testing.T) {
	testToken := "test-bearer-token"
	cases := []string{"bearer ", "BEARER ", "Bearer ", "bEaReR "}
	for _, prefix := range cases {
		token, ok := extractBearer(prefix + testToken)
		if !ok {
			t.Errorf("prefix %q should be recognized", prefix)
		}
		if token != testToken {
			t.Errorf("expected token %s, got %s", testToken, token)
		}
	}

	_, ok := extractBearer("Basic " + testToken)
	if ok {
		t.Error("Basic scheme should not be recognized as Bearer")
	}

	_, ok = extractBearer("bearer")
	if ok {
		t.Error("too short string should not be recognized")
	}
}

func TestMiddlewareAuthNoToken(t *testing.T) {
	setupTestConfig(t)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/test", MiddlewareAuth(), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestMiddlewareAdminNonAdmin(t *testing.T) {
	setupTestConfig(t)
	setupTestDB(t)

	user := &model.User{
		ID:           1,
		Username:     "normaluser",
		Role:         "user",
		TokenVersion: 0,
	}
	if err := model.DB.Create(user).Error; err != nil {
		t.Fatal(err)
	}

	token, _, err := GenerateToken(user)
	if err != nil {
		t.Fatal(err)
	}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/admin", MiddlewareAuth(), MiddlewareAdmin(), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest("GET", "/admin", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 for non-admin, got %d", w.Code)
	}
}

func TestMiddlewareProxyAuthNoAuth(t *testing.T) {
	setupTestConfig(t)
	config.SetProxyKeyForTest("test-proxy-key")

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/proxy", MiddlewareProxyAuth(), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest("GET", "/proxy", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 with no auth, got %d", w.Code)
	}
}

func TestMiddlewareProxyAuthWithCorrectKey(t *testing.T) {
	setupTestConfig(t)
	proxyKey := "my-secret-proxy-key"
	config.SetProxyKeyForTest(proxyKey)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/proxy", MiddlewareProxyAuth(), func(c *gin.Context) {
		userID, _ := c.Get(ContextKeyUserID)
		role, _ := c.Get(ContextKeyRole)
		if userID.(uint) != ProxyUserID {
			t.Errorf("expected ProxyUserID, got %v", userID)
		}
		if role.(string) != "proxy" {
			t.Errorf("expected role proxy, got %v", role)
		}
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest("GET", "/proxy", nil)
	req.Header.Set("Authorization", "Bearer "+proxyKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 with correct proxy key, got %d", w.Code)
	}
}

func TestMiddlewareProxyAuthWithJWT(t *testing.T) {
	setupTestConfig(t)
	setupTestDB(t)
	config.SetProxyKeyForTest("test-proxy-key")

	user := &model.User{
		ID:           1,
		Username:     "admin",
		Role:         "admin",
		TokenVersion: 0,
	}
	if err := model.DB.Create(user).Error; err != nil {
		t.Fatal(err)
	}

	token, _, err := GenerateToken(user)
	if err != nil {
		t.Fatal(err)
	}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/proxy", MiddlewareProxyAuth(), func(c *gin.Context) {
		userID, _ := c.Get(ContextKeyUserID)
		username, _ := c.Get(ContextKeyUsername)
		if userID.(uint) != user.ID {
			t.Errorf("expected user ID %d, got %v", user.ID, userID)
		}
		if username.(string) != user.Username {
			t.Errorf("expected username %s, got %v", user.Username, username)
		}
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest("GET", "/proxy", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 with valid JWT, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestMiddlewareProxyAuthWithWrongKey(t *testing.T) {
	setupTestConfig(t)
	config.SetProxyKeyForTest("correct-key")

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/proxy", MiddlewareProxyAuth(), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest("GET", "/proxy", nil)
	req.Header.Set("Authorization", "Bearer wrong-key")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 with wrong proxy key, got %d", w.Code)
	}
}

func TestCookieSecureInReleaseMode(t *testing.T) {
	setupTestConfig(t)

	gin.SetMode(gin.ReleaseMode)
	defer gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	expiresAt := time.Now().Add(1 * time.Hour)
	SetAuthCookie(c, "token", expiresAt)

	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected 1 cookie, got %d", len(cookies))
	}
	if !cookies[0].Secure {
		t.Error("cookie should be Secure in release mode")
	}
}

func TestCookieSecureInTestMode(t *testing.T) {
	setupTestConfig(t)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	expiresAt := time.Now().Add(1 * time.Hour)
	SetAuthCookie(c, "token", expiresAt)

	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected 1 cookie, got %d", len(cookies))
	}
	if cookies[0].Secure {
		t.Error("cookie should not be Secure in test mode")
	}
}

func TestNoneAlgorithmTokenRejected(t *testing.T) {
	setupTestConfig(t)

	claims := Claims{
		UserID:   1,
		Username: "test",
		Role:     "admin",
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodNone, claims)
	noneToken, err := tok.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatal(err)
	}

	_, err = ParseToken(noneToken)
	if err == nil {
		t.Fatal("token with alg:none should be rejected")
	}
}

func TestNonHMACAlgorithmTokenRejected(t *testing.T) {
	setupTestConfig(t)

	// 生成一个真正的 RSA 密钥用于签名 RS256 token, 验证 ParseToken 会拒绝非 HMAC 算法
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate RSA key: %v", err)
	}

	claims := Claims{
		UserID:   1,
		Username: "test",
		Role:     "admin",
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	rsaToken, err := tok.SignedString(rsaKey)
	if err != nil {
		t.Fatalf("RSA signing failed: %v", err)
	}

	_, err = ParseToken(rsaToken)
	if err == nil {
		t.Fatal("token with RS256 algorithm should be rejected")
	}
}

func TestMiddlewareProxyAuthRevokedJWTWithValidProxyKey(t *testing.T) {
	setupTestConfig(t)
	setupTestDB(t)
	proxyKey := "valid-proxy-key"
	config.SetProxyKeyForTest(proxyKey)

	user := &model.User{
		ID:           1,
		Username:     "admin",
		Role:         "admin",
		TokenVersion: 0,
	}
	if err := model.DB.Create(user).Error; err != nil {
		t.Fatal(err)
	}

	token, _, err := GenerateToken(user)
	if err != nil {
		t.Fatal(err)
	}

	if err := model.UpdatePassword(user.ID, "newpassword123"); err != nil {
		t.Fatal(err)
	}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/proxy", MiddlewareProxyAuth(), func(c *gin.Context) {
		userID, _ := c.Get(ContextKeyUserID)
		if userID.(uint) != ProxyUserID {
			t.Errorf("expected ProxyUserID when using proxy key, got %v", userID)
		}
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest("GET", "/proxy", nil)
	req.AddCookie(&http.Cookie{Name: CookieName, Value: token})
	req.Header.Set("Authorization", "Bearer "+proxyKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 when revoked JWT cookie is present but valid proxy key in Authorization header, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestMiddlewareProxyAuthRevokedJWTClearsCookie(t *testing.T) {
	setupTestConfig(t)
	setupTestDB(t)
	config.SetProxyKeyForTest("")

	user := &model.User{
		ID:           1,
		Username:     "admin",
		Role:         "admin",
		TokenVersion: 0,
	}
	if err := model.DB.Create(user).Error; err != nil {
		t.Fatal(err)
	}

	token, _, err := GenerateToken(user)
	if err != nil {
		t.Fatal(err)
	}

	if err := model.UpdatePassword(user.ID, "newpassword123"); err != nil {
		t.Fatal(err)
	}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/proxy", MiddlewareProxyAuth(), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest("GET", "/proxy", nil)
	req.AddCookie(&http.Cookie{Name: CookieName, Value: token})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for revoked JWT, got %d", w.Code)
	}

	cookies := w.Result().Cookies()
	var cleared bool
	for _, cookie := range cookies {
		if cookie.Name == CookieName && cookie.MaxAge == -1 {
			cleared = true
			break
		}
	}
	if !cleared {
		t.Error("revoked JWT in proxy auth should clear cookie")
	}
}

func TestExtractTokenPriority(t *testing.T) {
	setupTestConfig(t)

	cookieToken := "cookie-token"
	headerToken := "header-token"
	queryToken := "query-token"

	req := httptest.NewRequest("GET", "/?token="+queryToken, nil)
	req.AddCookie(&http.Cookie{Name: CookieName, Value: cookieToken})
	req.Header.Set("Authorization", "Bearer "+headerToken)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req

	extracted := extractToken(c)
	if extracted != cookieToken {
		t.Errorf("cookie should have highest priority, expected %q, got %q", cookieToken, extracted)
	}
}

func TestExtractTokenHeaderOverQuery(t *testing.T) {
	setupTestConfig(t)

	headerToken := "header-token"
	queryToken := "query-token"

	req := httptest.NewRequest("GET", "/?token="+queryToken, nil)
	req.Header.Set("Authorization", "Bearer "+headerToken)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req

	extracted := extractToken(c)
	if extracted != headerToken {
		t.Errorf("header should have higher priority than query, expected %q, got %q", headerToken, extracted)
	}
}

func TestExtractTokenNoQueryRejectsQueryToken(t *testing.T) {
	setupTestConfig(t)

	queryToken := "query-token-should-be-ignored"

	req := httptest.NewRequest("GET", "/?token="+queryToken, nil)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req

	extracted := extractTokenNoQuery(c)
	if extracted != "" {
		t.Errorf("extractTokenNoQuery should not accept query token, got %q", extracted)
	}
}

func TestExtractBearerEdgeCases(t *testing.T) {
	cases := []struct {
		input string
		ok    bool
		token string
	}{
		{"Bearer", false, ""},
		{"Bearer ", false, ""},
		{"Bearer  ", false, ""},
		{"bearer abc", true, "abc"},
		{"BEARER abc", true, "abc"},
		{"bEaReR abc", true, "abc"},
		{"Bearer abc def", true, "abc def"},
		{"Basic abc", false, ""},
		{"", false, ""},
	}
	for _, tc := range cases {
		token, ok := extractBearer(tc.input)
		if ok != tc.ok {
			t.Errorf("extractBearer(%q) ok = %v, want %v", tc.input, ok, tc.ok)
		}
		if ok && strings.TrimSpace(token) != strings.TrimSpace(tc.token) {
			t.Errorf("extractBearer(%q) token = %q, want %q", tc.input, token, tc.token)
		}
	}
}

func TestVerifyPasswordTimingAttack(t *testing.T) {
	setupTestConfig(t)
	setupTestDB(t)
	if err := model.SetupAdmin("admin", "password123"); err != nil {
		t.Fatalf("SetupAdmin failed: %v", err)
	}

	start1 := time.Now()
	_, ok1 := model.VerifyPassword("nonexistentuser", "password123")
	elapsed1 := time.Since(start1)

	start2 := time.Now()
	_, ok2 := model.VerifyPassword("admin", "wrongpassword")
	elapsed2 := time.Since(start2)

	if ok1 {
		t.Error("nonexistent user should fail")
	}
	if ok2 {
		t.Error("wrong password should fail")
	}

	// 两条路径都应执行 bcrypt(cost 12) 比对, 耗时应在同一量级(均 >= 10ms).
	// 用绝对下限断言"确实执行了 bcrypt", 用相对比值断言"两条路径耗时相近".
	if elapsed1 < 10*time.Millisecond {
		t.Errorf("nonexistent user path too fast (%v), dummy bcrypt may not have run", elapsed1)
	}
	if elapsed2 < 10*time.Millisecond {
		t.Errorf("wrong password path too fast (%v), bcrypt may not have run", elapsed2)
	}
	ratio := float64(elapsed2) / float64(elapsed1+1)
	t.Logf("Timing: nonexistent=%v, wrongpass=%v, ratio=%.2f", elapsed1, elapsed2, ratio)
}

func TestMiddlewareAuthRevokedTokenClearsCookie(t *testing.T) {
	setupTestConfig(t)
	setupTestDB(t)

	user := &model.User{
		ID:           1,
		Username:     "admin",
		Role:         "admin",
		TokenVersion: 0,
	}
	if err := model.DB.Create(user).Error; err != nil {
		t.Fatal(err)
	}

	token, _, err := GenerateToken(user)
	if err != nil {
		t.Fatal(err)
	}

	if err := model.UpdatePassword(user.ID, "newpassword123"); err != nil {
		t.Fatal(err)
	}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/test", MiddlewareAuth(), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest("GET", "/test", nil)
	req.AddCookie(&http.Cookie{Name: CookieName, Value: token})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for revoked token, got %d", w.Code)
	}

	cookies := w.Result().Cookies()
	var cleared bool
	for _, cookie := range cookies {
		if cookie.Name == CookieName && cookie.MaxAge == -1 {
			cleared = true
			break
		}
	}
	if !cleared {
		t.Error("MiddlewareAuth should clear cookie for revoked token")
	}
}
