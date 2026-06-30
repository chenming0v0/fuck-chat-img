package auth

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/fuck-chat-img/fci/internal/config"
	"github.com/fuck-chat-img/fci/internal/model"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

const (
	ContextKeyUserID   = "user_id"
	ContextKeyUsername = "username"
	ContextKeyRole     = "role"
	ContextKeyAdmin    = "is_admin"
)

// Claims JWT 载荷
type Claims struct {
	UserID   uint   `json:"uid"`
	Username string `json:"usr"`
	Role     string `json:"rol"`
	jwt.RegisteredClaims
}

// GenerateToken 生成 JWT
func GenerateToken(u *model.User) (string, error) {
	cfg := config.Get()
	claims := Claims{
		UserID:   u.ID,
		Username: u.Username,
		Role:     u.Role,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(7 * 24 * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			Issuer:    "fuck-chat-img",
		},
	}
	t := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return t.SignedString([]byte(cfg.JWTSecret))
}

// ParseToken 解析 JWT
func ParseToken(tokenStr string) (*Claims, error) {
	cfg := config.Get()
	claims := &Claims{}
	t, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (interface{}, error) {
		// 安全: 必须使用 HS256, 防止算法混淆攻击
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return []byte(cfg.JWTSecret), nil
	})
	if err != nil || !t.Valid {
		return nil, errors.New("invalid token")
	}
	return claims, nil
}

// extractToken 从请求中提取 token (Authorization: Bearer xxx 或 query token)
func extractToken(c *gin.Context) string {
	auth := c.GetHeader("Authorization")
	if strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimPrefix(auth, "Bearer ")
	}
	if t := c.Query("token"); t != "" {
		return t
	}
	return ""
}

// MiddlewareAuth 要求登录(用于 Web UI 控制台)
func MiddlewareAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		token := extractToken(c)
		if token == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"success": false, "message": "未登录"})
			return
		}
		claims, err := ParseToken(token)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"success": false, "message": "登录已过期"})
			return
		}
		c.Set(ContextKeyUserID, claims.UserID)
		c.Set(ContextKeyUsername, claims.Username)
		c.Set(ContextKeyRole, claims.Role)
		c.Set(ContextKeyAdmin, claims.Role == "admin")
		c.Next()
	}
}

// MiddlewareAdmin 要求管理员
func MiddlewareAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		isAdmin, _ := c.Get(ContextKeyAdmin)
		if isAdmin != true {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"success": false, "message": "需要管理员权限"})
			return
		}
		c.Next()
	}
}

// MiddlewareProxyAuth 代理接口鉴权
// 优先使用 Web UI 的 JWT(管理员/用户), 其次校验 FCI_PROXY_KEY(OpenAI 兼容客户端场景)
// 若 FCI_PROXY_KEY 未配置, 则允许携带有效 JWT 的已登录用户访问
func MiddlewareProxyAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		token := extractToken(c)
		// 1. 先尝试解析为 Web UI 的 JWT
		if token != "" {
			if claims, err := ParseToken(token); err == nil {
				c.Set(ContextKeyUserID, claims.UserID)
				c.Set(ContextKeyUsername, claims.Username)
				c.Set(ContextKeyRole, claims.Role)
				c.Set(ContextKeyAdmin, claims.Role == "admin")
				c.Next()
				return
			}
		}
		// 2. 校验代理访问密钥
		proxyKey := os.Getenv("FCI_PROXY_KEY")
		if proxyKey != "" {
			// 客户端通过 Authorization: Bearer <proxyKey> 或 query ?token=<proxyKey> 传入
			if token == proxyKey {
				c.Set(ContextKeyAdmin, false)
				c.Next()
				return
			}
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": gin.H{"message": "invalid proxy key", "type": "auth_error", "code": 401},
			})
			return
		}
		// 3. 未配置代理密钥且无有效 JWT: 拒绝(防止匿名调用)
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
			"error": gin.H{"message": "authentication required (configure FCI_PROXY_KEY or login via Web UI)", "type": "auth_error", "code": 401},
		})
	}
}
