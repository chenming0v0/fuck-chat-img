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

// GenerateToken 生成 JWT, 同时返回过期时间(供调用方与响应 expires_at 字段共用同一来源,
// 避免响应字段与 JWT 实际 exp 各自调用 time.Now() 产生微小漂移, Low-10)
func GenerateToken(u *model.User) (string, time.Time, error) {
	cfg := config.Get()
	expiresAt := time.Now().Add(7 * 24 * time.Hour)
	claims := Claims{
		UserID:   u.ID,
		Username: u.Username,
		Role:     u.Role,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			Issuer:    "fuck-chat-img",
		},
	}
	t := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	s, err := t.SignedString([]byte(cfg.JWTSecret))
	return s, expiresAt, err
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
// HTTP/1.1 规范中 Authorization scheme 大小写不敏感, 部分客户端发 bearer/BEARER, 需兼容(Low-12).
func extractToken(c *gin.Context) string {
	if h := c.GetHeader("Authorization"); h != "" {
		if t, ok := extractBearer(h); ok {
			return t
		}
	}
	if t := c.Query("token"); t != "" {
		return t
	}
	return ""
}

// extractTokenHeaderOnly 仅从 Header 提取 token(用于 Web UI 鉴权, 避免 JWT 进日志)
func extractTokenHeaderOnly(c *gin.Context) string {
	if h := c.GetHeader("Authorization"); h != "" {
		if t, ok := extractBearer(h); ok {
			return t
		}
	}
	return ""
}

// extractBearer 从 Authorization 头提取 Bearer token(scheme 大小写不敏感)
func extractBearer(h string) (string, bool) {
	const scheme = "bearer "
	if len(h) < len(scheme) {
		return "", false
	}
	if !strings.EqualFold(h[:len(scheme)], scheme) {
		return "", false
	}
	return strings.TrimSpace(h[len(scheme):]), true
}

// extractProxyKey 仅从 Header 提取 FCI_PROXY_KEY(不允许 query)
// 安全: query 参数会进入访问日志/Referer/浏览器历史, 长期 proxy key 不应通过 query 传递(Low-13).
// query token 仅接受短效 JWT(供 SSE/EventSource 使用).
func extractProxyKey(c *gin.Context) string {
	if h := c.GetHeader("Authorization"); h != "" {
		if t, ok := extractBearer(h); ok {
			return t
		}
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
// 安全策略(实现必须与文档承诺一致, H6 修复点):
//  1. 若配置了 FCI_PROXY_KEY: 客户端用该 key(仅 Header, 不允许 query 避免日志泄漏)访问, 或用有效 JWT
//  2. 若未配置 FCI_PROXY_KEY: 仅允许管理员 JWT 访问(避免任意登录态用户白嫖管理员上游额度)
//  3. JWT 可走 Header 或 query(后者用于 SSE/EventSource); query 不接受 proxy key
func MiddlewareProxyAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		token := extractToken(c)
		proxyKey := os.Getenv("FCI_PROXY_KEY")
		// 1. 先尝试解析为 Web UI 的 JWT(query 或 header 均可, JWT 短效)
		if token != "" {
			if claims, err := ParseToken(token); err == nil {
				isAdmin := claims.Role == "admin"
				// 未配置 proxy key 时, 仅管理员 JWT 放行(H6: 此前任意有效 JWT 都放行, 违反文档承诺)
				if !isAdmin && proxyKey == "" {
					c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
						"error": gin.H{"message": "non-admin JWT not allowed without FCI_PROXY_KEY", "type": "auth_error", "code": 403},
					})
					return
				}
				c.Set(ContextKeyUserID, claims.UserID)
				c.Set(ContextKeyUsername, claims.Username)
				c.Set(ContextKeyRole, claims.Role)
				c.Set(ContextKeyAdmin, isAdmin)
				c.Next()
				return
			}
		}
		// 2. 校验代理访问密钥(常量时间比较, 防时序侧信道; 仅从 Header 取, 避免 query 泄漏)
		if proxyKey != "" {
			if pk := extractProxyKey(c); pk != "" && subtle.ConstantTimeCompare([]byte(pk), []byte(proxyKey)) == 1 {
				c.Set(ContextKeyAdmin, false)
				c.Next()
				return
			}
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": gin.H{"message": "invalid proxy key", "type": "auth_error", "code": 401},
			})
			return
		}
		// 3. 未配置代理密钥且无有效 JWT: 拒绝
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
			"error": gin.H{"message": "authentication required (configure FCI_PROXY_KEY or login as admin via Web UI)", "type": "auth_error", "code": 401},
		})
	}
}
