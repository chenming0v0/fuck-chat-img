package api

import (
	"errors"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fuck-chat-img/fci/internal/auth"
	"github.com/fuck-chat-img/fci/internal/config"
	"github.com/fuck-chat-img/fci/internal/proxy"
	"github.com/fuck-chat-img/fci/web"
	"github.com/gin-gonic/gin"
)

const maxProxyBodyBytes = 32 << 20
const maxAPIBodyBytes = 1 << 20

func SetupRouter() *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Logger())
	r.Use(customRecovery())
	// 显式传入空切片: 不信任任何代理, c.ClientIP() 直接返回 RemoteAddr.
	// 避免攻击者伪造 X-Forwarded-For 绕过 rateLimit 的每 IP 限流.
	_ = r.SetTrustedProxies([]string{})
	r.Use(corsMiddleware())
	r.Use(func(c *gin.Context) {
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "DENY")
		c.Header("Referrer-Policy", "strict-origin-when-cross-origin")
		c.Next()
	})
	r.Use(func(c *gin.Context) {
		method := c.Request.Method
		if method == http.MethodPost || method == http.MethodPut || method == http.MethodPatch || method == http.MethodDelete {
			if strings.HasPrefix(c.Request.URL.Path, "/v1/") {
				c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxProxyBodyBytes)
			} else {
				c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxAPIBodyBytes)
			}
		}
		c.Next()
	})

	api := r.Group("/api")
	api.GET("/status", Status)
	api.POST("/login", rateLimit("login", 10, time.Minute), Login)
	api.POST("/setup", rateLimit("setup", 10, time.Minute), Setup)
	api.POST("/logout", Logout)

	v1 := r.Group("/v1")
	v1.Use(auth.MiddlewareProxyAuth())
	v1.GET("/models", proxy.HandleModels)
	v1.GET("/models/", proxy.HandleModels)
	v1.POST("/responses", proxy.HandleResponses)
	v1.POST("/chat/completions", proxy.HandleChat)
	v1.POST("/messages", proxy.HandleMessages)
	v1.POST("/responses/", proxy.HandleResponses)
	v1.POST("/chat/completions/", proxy.HandleChat)
	v1.POST("/messages/", proxy.HandleMessages)

	authed := r.Group("/api")
	authed.Use(auth.MiddlewareAuth())
	{
		authed.GET("/user", UserInfo)
		authed.POST("/user/password", ChangePassword)

		authed.GET("/groups", ListGroups)
		authed.GET("/groups/:id", GetGroup)
		authed.GET("/groups/:id/plain", auth.MiddlewareAdmin(), GetGroupPlain)
		authed.POST("/groups", auth.MiddlewareAdmin(), CreateGroup)
		authed.PUT("/groups/:id", auth.MiddlewareAdmin(), UpdateGroup)
		authed.DELETE("/groups/:id", auth.MiddlewareAdmin(), DeleteGroup)
		authed.POST("/groups/:id/toggle", auth.MiddlewareAdmin(), ToggleGroup)
		authed.GET("/groups/:id/test", auth.MiddlewareAdmin(), TestGroup)

		authed.GET("/history", ListHistory)
		authed.GET("/history/:id", GetHistory)
		authed.DELETE("/history/:id", auth.MiddlewareAdmin(), DeleteHistory)
		authed.DELETE("/history", auth.MiddlewareAdmin(), ClearHistory)
		authed.GET("/history/stats", HistoryStats)

		authed.GET("/cache/stats", CacheStats)
		authed.DELETE("/cache", auth.MiddlewareAdmin(), CacheClear)
	}

	registerWebStatic(r)

	return r
}

var sameOriginHosts atomicP

type atomicP struct {
	p *[]string
	mu sync.RWMutex
}

func (a *atomicP) Load() *[]string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.p
}

func (a *atomicP) Store(p *[]string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.p = p
}

// hostsMatch 判断 Origin 是否与请求同源(同 host + 同 port + 同 scheme)
// 请求 scheme 由 c.Request.TLS 决定, 不从 Origin 头推断, 避免跨 scheme 同源误判.
func hostsMatch(originURL *url.URL, requestHost string, requestTLS bool) bool {
	originHostname, originPort, err := net.SplitHostPort(originURL.Host)
	if err != nil {
		originHostname = originURL.Host
		originPort = ""
	}
	reqHostname, reqPort, err := net.SplitHostPort(requestHost)
	if err != nil {
		reqHostname = requestHost
		reqPort = ""
	}
	if originHostname != reqHostname {
		return false
	}
	originScheme := originURL.Scheme
	reqScheme := "http"
	if requestTLS {
		reqScheme = "https"
	}
	if originPort == "" {
		if originScheme == "https" {
			originPort = "443"
		} else {
			originPort = "80"
		}
	}
	if reqPort == "" {
		if reqScheme == "https" {
			reqPort = "443"
		} else {
			reqPort = "80"
		}
	}
	if originScheme != reqScheme {
		return false
	}
	return originPort == reqPort
}

func corsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Vary", "Origin")
		origin := c.GetHeader("Origin")
		if origin != "" {
			allow := false
			if u, err := url.Parse(origin); err == nil && u.Scheme != "" && u.Host != "" {
				if hostsMatch(u, c.Request.Host, c.Request.TLS != nil) {
					allow = true
				} else {
					p := sameOriginHosts.Load()
					if p != nil {
						for _, h := range *p {
							if origin == h {
								allow = true
								break
							}
						}
					}
				}
			}
			if allow {
				c.Header("Access-Control-Allow-Origin", origin)
				c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
				c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization, Accept, Cache-Control")
				c.Header("Access-Control-Expose-Headers", "Content-Length, Content-Type")
				c.Header("Access-Control-Allow-Credentials", "true")
			}
		}
		if c.Request.Method == http.MethodOptions {
			if c.GetHeader("Origin") != "" && c.Writer.Header().Get("Access-Control-Allow-Origin") == "" {
				c.AbortWithStatus(http.StatusForbidden)
				return
			}
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

func SetSameOriginHosts(hosts []string) {
	cp := append([]string(nil), hosts...)
	sameOriginHosts.Store(&cp)
}

func rateLimit(name string, limit int, window time.Duration) gin.HandlerFunc {
	type bucket struct {
		count   int
		resetAt time.Time
	}
	var mu sync.Mutex
	buckets := make(map[string]*bucket)
	return func(c *gin.Context) {
		ip := c.ClientIP()
		key := name + ":" + ip
		mu.Lock()
		b, ok := buckets[key]
		now := time.Now()
		if !ok || now.After(b.resetAt) {
			b = &bucket{count: 0, resetAt: now.Add(window)}
			buckets[key] = b
		}
		b.count++
		allowed := b.count <= limit
		// 惰性清理过期桶: 桶数过多时顺带清理, 避免内存无限增长(替代独立清理协程).
		if len(buckets) > 10000 {
			for k, bb := range buckets {
				if now.After(bb.resetAt) {
					delete(buckets, k)
				}
			}
		}
		mu.Unlock()
		if !allowed {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"success": false,
				"message": "请求过于频繁, 请稍后再试",
			})
			return
		}
		c.Next()
	}
}

func customRecovery() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if err := recover(); err != nil {
				log.Printf("[fci] panic recovered: %v", err)
				c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
					"success": false,
					"message": "服务器内部错误",
				})
			}
		}()
		c.Next()
	}
}

func registerWebStatic(r *gin.Engine) {
	cfg := config.Get()
	var rootFS fs.FS
	if cfg.WebDir != "" {
		indexPath := filepath.Join(cfg.WebDir, "index.html")
		if _, err := os.Stat(indexPath); err == nil {
			rootFS = os.DirFS(cfg.WebDir)
		} else if !errors.Is(err, os.ErrNotExist) {
			log.Printf("[fci] warn: stat WebDir index.html failed: %v", err)
		}
	}
	if rootFS == nil {
		emb, err := fs.Sub(web.DistFS, "dist")
		if err != nil {
			log.Printf("[fci] warn: embedded dist FS not available: %v", err)
		} else {
			if _, err := fs.Stat(emb, "index.html"); err == nil {
				rootFS = emb
			}
		}
	}

	r.GET("/static/*filepath", func(c *gin.Context) {
		if rootFS == nil {
			c.Status(http.StatusNotFound)
			return
		}
		fp := strings.TrimPrefix(c.Param("filepath"), "/")
		if fp == "" {
			c.Status(http.StatusNotFound)
			return
		}
		decoded, err := url.PathUnescape(fp)
		if err != nil {
			c.Status(http.StatusNotFound)
			return
		}
		fp = decoded
		if strings.Contains(fp, "..") {
			c.Status(http.StatusNotFound)
			return
		}
		cleaned := path.Clean("static/" + fp)
		if !strings.HasPrefix(cleaned, "static/") {
			c.Status(http.StatusNotFound)
			return
		}
		if !fs.ValidPath(cleaned) {
			c.Status(http.StatusNotFound)
			return
		}
		serveStaticFile(c, rootFS, cleaned)
	})

	r.GET("/", func(c *gin.Context) {
		serveIndex(c, rootFS)
	})

	r.NoRoute(func(c *gin.Context) {
		p := c.Request.URL.Path
		if strings.HasPrefix(p, "/api/") || p == "/api" {
			c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "not found"})
			return
		}
		if strings.HasPrefix(p, "/v1/") || p == "/v1" {
			c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "not found"})
			return
		}
		if strings.HasPrefix(p, "/static/") || p == "/static" {
			c.Status(http.StatusNotFound)
			return
		}
		serveIndex(c, rootFS)
	})
}

func serveIndex(c *gin.Context, rootFS fs.FS) {
	if rootFS == nil {
		c.String(http.StatusOK, "fuck-chat-img backend is running. Web UI not built. Run `cd web && bun run build` then rebuild.")
		return
	}
	f, err := rootFS.Open("index.html")
	if err != nil {
		log.Printf("[fci] warn: open index.html failed: %v", err)
		c.String(http.StatusOK, "fuck-chat-img backend is running. Web UI not built. Run `cd web && bun run build` then rebuild.")
		return
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 1<<20))
	if err != nil {
		log.Printf("[fci] warn: read index.html failed: %v", err)
		c.String(http.StatusOK, "fuck-chat-img backend is running. Web UI not built. Run `cd web && bun run build` then rebuild.")
		return
	}
	c.Header("Cache-Control", "no-cache, no-store, must-revalidate")
	c.Data(http.StatusOK, "text/html; charset=utf-8", data)
}

func serveStaticFile(c *gin.Context, rootFS fs.FS, p string) {
	f, err := rootFS.Open(p)
	if err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil || stat.IsDir() {
		c.Status(http.StatusNotFound)
		return
	}
	data, err := io.ReadAll(io.LimitReader(f, 50<<20))
	if err != nil {
		log.Printf("[fci] warn: read static file %s failed: %v", p, err)
		c.Status(http.StatusInternalServerError)
		return
	}
	ext := path.Ext(p)
	if isHashedAsset(p, ext) {
		c.Header("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		c.Header("Cache-Control", "no-cache, no-store, must-revalidate")
	}
	c.Data(http.StatusOK, contentTypeFor(p), data)
}

// hashedAssetExt 可享受长缓存的资源扩展名(rsbuild 默认对这类文件加内容哈希)
var hashedAssetExt = map[string]bool{
	".js": true, ".css": true, ".woff2": true, ".woff": true, ".ttf": true, ".eot": true,
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".svg": true,
}

// isHashedAsset 判断静态资源是否可设为 immutable 长缓存.
// 仅当扩展名属于已知 hashed 资源类型, 且文件名含内容哈希段(如 index.a1b2c3d4.js)时才返回 true;
// 否则回落到 no-cache, 避免非哈希资源(如 favicon.ico)被原地更新后浏览器仍命中旧缓存.
func isHashedAsset(p, ext string) bool {
	if !hashedAssetExt[ext] {
		return false
	}
	base := path.Base(p)
	// 去掉扩展名后检查是否含 8 位以上十六进制哈希段(常见形式: name.[hash].ext 或 name-[hash].ext)
	stem := strings.TrimSuffix(base, ext)
	idx := strings.LastIndexAny(stem, ".-")
	if idx < 0 || idx == len(stem)-1 {
		return false
	}
	tail := stem[idx+1:]
	if len(tail) < 8 {
		return false
	}
	for _, c := range tail {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

func contentTypeFor(p string) string {
	ext := strings.ToLower(path.Ext(p))
	switch ext {
	case ".js":
		return "application/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".html":
		return "text/html; charset=utf-8"
	case ".json":
		return "application/json; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".ico":
		return "image/x-icon"
	case ".woff2":
		return "font/woff2"
	case ".woff":
		return "font/woff"
	case ".ttf":
		return "font/ttf"
	case ".eot":
		return "application/vnd.ms-fontobject"
	default:
		ct := mimeFromExt(ext)
		if ct != "" {
			return ct
		}
		return "application/octet-stream"
	}
}

func mimeFromExt(ext string) string {
	m := map[string]string{
		".mp3": "audio/mpeg",
		".mp4": "video/mp4",
		".webm": "video/webm",
		".pdf": "application/pdf",
	}
	if ct, ok := m[ext]; ok {
		return ct
	}
	return ""
}
