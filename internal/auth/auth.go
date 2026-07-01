package auth

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
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
	// CookieName JWT存储的HttpOnly Cookie名称
	CookieName = "fci_token"
	// ProxyUserID 代理密钥认证请求使用的固定用户ID(非零, 确保历史记录归属可追溯).
	// 使用 MaxUint 避免与正常自增用户ID冲突.
	ProxyUserID uint = ^uint(0)
)

// Claims JWT 载荷
type Claims struct {
	UserID       uint   `json:"uid"`
	Username     string `json:"usr"`
	Role         string `json:"rol"`
	TokenVersion int    `json:"tv"`
	jwt.RegisteredClaims
}

// GenerateToken 生成 JWT, 同时返回过期时间(供调用方与响应 expires_at 字段共用同一来源,
// 避免响应字段与 JWT 实际 exp 各自调用 time.Now() 产生微小漂移, Low-10)
func GenerateToken(u *model.User) (string, time.Time, error) {
	cfg := config.Get()
	expiresAt := time.Now().Add(7 * 24 * time.Hour)
	claims := Claims{
		UserID:       u.ID,
		Username:     u.Username,
		Role:         u.Role,
		TokenVersion: u.TokenVersion,
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

// ParseToken 解析 JWT(仅做签名和过期校验, token_version 校验由 ValidateTokenVersion 负责)
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

// ValidateTokenVersion 校验JWT中的token_version是否与数据库中用户当前版本一致
// 修改密码时会递增token_version, 使所有旧JWT立即失效
func ValidateTokenVersion(claims *Claims) bool {
	// ProxyKey认证的请求不经过JWT, 不会调用此函数
	// ProxyUserID是特殊ID, 跳过版本校验(它不用JWT)
	if claims.UserID == ProxyUserID {
		return true
	}
	currentVersion, err := model.GetUserTokenVersion(claims.UserID)
	if err != nil {
		return false
	}
	return currentVersion == claims.TokenVersion
}

// SetAuthCookie 设置HttpOnly认证Cookie(JWT存储), 防止XSS窃取token
func SetAuthCookie(c *gin.Context, token string, expiresAt time.Time) {
	secure := gin.Mode() == gin.ReleaseMode
	cookie := &http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		Expires:  expiresAt,
		MaxAge:   int(time.Until(expiresAt).Seconds()),
	}
	http.SetCookie(c.Writer, cookie)
}

// ClearAuthCookie 清除认证Cookie(登出时调用)
func ClearAuthCookie(c *gin.Context) {
	secure := gin.Mode() == gin.ReleaseMode
	cookie := &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
		Expires:  time.Unix(0, 0),
	}
	http.SetCookie(c.Writer, cookie)
}

// extractToken 从请求中提取 token (优先Cookie, 其次Authorization: Bearer header, 最后query token)
// query token 仅用于代理接口(SSE/EventSource 无法自定义 Header 场景).
// HTTP/1.1 规范中 Authorization scheme 大小写不敏感, 部分客户端发 bearer/BEARER, 需兼容.
func extractToken(c *gin.Context) string {
	// 1. 优先从HttpOnly Cookie读取(Web UI主要方式, 防XSS)
	if cookie, err := c.Cookie(CookieName); err == nil && cookie != "" {
		return cookie
	}
	// 2. 其次从Authorization header读取(API客户端/programmatic access)
	if h := c.GetHeader("Authorization"); h != "" {
		if t, ok := extractBearer(h); ok {
			return t
		}
	}
	// 3. 最后从query读取(仅用于SSE/EventSource)
	if t := c.Query("token"); t != "" {
		return t
	}
	return ""
}

// extractTokenNoQuery 从Cookie或Header提取token, 不允许query参数(Web UI鉴权用)
func extractTokenNoQuery(c *gin.Context) string {
	if cookie, err := c.Cookie(CookieName); err == nil && cookie != "" {
		return cookie
	}
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
		token := extractTokenNoQuery(c)
		if token == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"success": false, "message": "未登录"})
			return
		}
		claims, err := ParseToken(token)
		if err != nil {
			ClearAuthCookie(c)
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"success": false, "message": "登录已过期"})
			return
		}
		if !ValidateTokenVersion(claims) {
			ClearAuthCookie(c)
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"success": false, "message": "登录已失效，请重新登录"})
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
		v, exists := c.Get(ContextKeyAdmin)
		isAdmin, ok := v.(bool)
		if !exists || !ok || !isAdmin {
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
		cfg := config.Get()
		token := extractToken(c)
		proxyKey := cfg.ProxyKey
		if token != "" {
			if claims, err := ParseToken(token); err == nil {
				if !ValidateTokenVersion(claims) {
					c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
						"error": gin.H{"message": "token revoked", "type": "auth_error", "code": 401},
					})
					return
				}
				isAdmin := claims.Role == "admin"
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
		if proxyKey != "" {
			pk := extractProxyKey(c)
			if pk != "" && subtle.ConstantTimeCompare([]byte(pk), []byte(proxyKey)) == 1 {
				c.Set(ContextKeyUserID, ProxyUserID)
				c.Set(ContextKeyUsername, "__proxy__")
				c.Set(ContextKeyRole, "proxy")
				c.Set(ContextKeyAdmin, false)
				c.Next()
				return
			}
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": gin.H{"message": "invalid proxy key", "type": "auth_error", "code": 401},
			})
			return
		}
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
			"error": gin.H{"message": "authentication required (configure FCI_PROXY_KEY or login as admin via Web UI)", "type": "auth_error", "code": 401},
		})
	}
}
