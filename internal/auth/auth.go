package auth

import (
	"crypto/subtle"
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
// 注意: query token 仅用于代理接口(SSE/EventSource 无法自定义 Header 场景),
// Web UI 鉴权(MiddlewareAuth)不允许 query token, 避免长期 JWT 泄露到访问日志.
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

// extractTokenHeaderOnly 仅从 Header 提取 token(用于 Web UI 鉴权, 避免 JWT 进日志)
func extractTokenHeaderOnly(c *gin.Context) string {
	auth := c.GetHeader("Authorization")
	if strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimPrefix(auth, "Bearer ")
	}
	return ""
}

// MiddlewareAuth 要求登录(用于 Web UI 控制台)
func MiddlewareAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		token := extractTokenHeaderOnly(c)
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
// 安全策略:
//  1. 若配置了 FCI_PROXY_KEY: 客户端用该 key(Header 或 query, 后者用于 SSE)访问, 或用有效 JWT
//  2. 若未配置 FCI_PROXY_KEY: 仅允许管理员 JWT 访问(避免任意登录态用户白嫖管理员上游额度)
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
		// 2. 校验代理访问密钥(常量时间比较, 防时序侧信道)
		proxyKey := os.Getenv("FCI_PROXY_KEY")
		if proxyKey != "" {
			if token != "" && subtle.ConstantTimeCompare([]byte(token), []byte(proxyKey)) == 1 {
				c.Set(ContextKeyAdmin, false)
				c.Next()
				return
			}
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": gin.H{"message": "invalid proxy key", "type": "auth_error", "code": 401},
			})
			return
		}
		// 3. 未配置代理密钥: 拒绝(此时 JWT 路径已在第 1 步处理, 走到这里说明无有效 JWT)
		//    不再允许"任意有效 JWT 即放行", 收紧到必须配置 FCI_PROXY_KEY 或管理员 JWT
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
			"error": gin.H{"message": "authentication required (configure FCI_PROXY_KEY or login as admin via Web UI)", "type": "auth_error", "code": 401},
		})
	}
}
