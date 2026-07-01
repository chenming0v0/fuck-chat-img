package api

import (
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fuck-chat-img/fci/internal/auth"
	"github.com/fuck-chat-img/fci/internal/config"
	"github.com/fuck-chat-img/fci/internal/proxy"
	"github.com/fuck-chat-img/fci/web"
	"github.com/gin-gonic/gin"
)

// maxProxyBodyBytes 代理请求体上限(32MiB). /v1/messages 等支持 base64 图片,
// 请求体天然较大, 但仍需设上限防止内存耗尽 DoS.
const maxProxyBodyBytes = 32 << 20

// maxAPIBodyBytes 管理接口请求体上限(1MiB), 管理接口不处理图片, body 很小
const maxAPIBodyBytes = 1 << 20

// SetupRouter 装配路由
func SetupRouter() *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Logger())
	r.Use(gin.Recovery())
	// 安全: 不信任任何 X-Forwarded-For / X-Real-IP 头(默认直连部署形态).
	// 否则攻击者可伪造 XFF 头使 c.ClientIP() 返回任意 IP, 绕过 /api/login /api/setup
	// 的速率限制. 反向代理部署时, 部署方应通过 SetTrustedProxies 显式列出可信代理 CIDR.
	_ = r.SetTrustedProxies(nil)
	// 安全: CORS 仅允许同源. 现代浏览器对同源 POST/PUT/DELETE 也会带 Origin 头,
	// 因此不能只放行空 Origin——会误拒 SPA 自身的写请求. 这里:
	//   - 空 Origin 放行(非浏览器客户端 / 同源 GET)
	//   - 比对请求 Host 头, http(s)://<host> 形式视为同源
	//   - 显式 Origin 通过 SetSameOriginHosts 白名单放行(部署方按需配置)
	//   - 其余跨域拒绝
	r.Use(corsMiddleware())
	// 安全: 基础安全响应头
	r.Use(func(c *gin.Context) {
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "DENY")
		c.Header("Referrer-Policy", "strict-origin-when-cross-origin")
		c.Next()
	})
	// 全局请求体大小限制: /v1/ 代理接口32MiB, /api/ 管理接口1MiB, 其他默认1MiB
	r.Use(func(c *gin.Context) {
		if strings.HasPrefix(c.Request.URL.Path, "/v1/") {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxProxyBodyBytes)
		} else {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxAPIBodyBytes)
		}
		c.Next()
	})

	// ===== 公开接口(带速率限制, 防爆破/抢注轮询) =====
	api := r.Group("/api")
	api.GET("/status", Status)
	api.POST("/login", rateLimit("login", 10, time.Minute), Login)
	api.POST("/setup", rateLimit("setup", 10, time.Minute), Setup) // 首次设置管理员(仅在无任何用户时可用)

	// ===== OpenAI 兼容代理接口 (使用模型组名作为访问凭证, /v1/models 需鉴权) =====
	v1 := r.Group("/v1")
	v1.Use(auth.MiddlewareProxyAuth())
	v1.GET("/models", proxy.HandleModels)
	v1.POST("/responses", proxy.HandleResponses)
	v1.POST("/chat/completions", proxy.HandleChat)
	// Anthropic Claude 兼容代理
	v1.POST("/messages", proxy.HandleMessages)
	// 兼容别名
	v1.POST("/responses/", proxy.HandleResponses)
	v1.POST("/chat/completions/", proxy.HandleChat)
	v1.POST("/messages/", proxy.HandleMessages)

	// ===== 需登录的管理接口 =====
	authed := api.Group("")
	authed.Use(auth.MiddlewareAuth())
	{
		authed.GET("/user", UserInfo)
		authed.POST("/user/password", ChangePassword)
		authed.POST("/logout", Logout)

		// 模型组管理(普通用户只读, 管理员可写)
		authed.GET("/groups", ListGroups)
		authed.GET("/groups/:id", GetGroup)
		// 明文 API Key 仅管理员可获取(用于编辑回填)
		authed.GET("/groups/:id/plain", auth.MiddlewareAdmin(), GetGroupPlain)
		authed.POST("/groups", auth.MiddlewareAdmin(), CreateGroup)
		authed.PUT("/groups/:id", auth.MiddlewareAdmin(), UpdateGroup)
		authed.DELETE("/groups/:id", auth.MiddlewareAdmin(), DeleteGroup)
		authed.POST("/groups/:id/toggle", auth.MiddlewareAdmin(), ToggleGroup)
		// 安全: TestGroup 返回的 group DTO 也含 Key, 必须管理员才能调
		authed.GET("/groups/:id/test", auth.MiddlewareAdmin(), TestGroup)

		// 历史记录(管理员可查看全部, 普通用户仅查看自己的)
		authed.GET("/history", ListHistory)
		authed.GET("/history/:id", GetHistory)
		authed.DELETE("/history/:id", auth.MiddlewareAdmin(), DeleteHistory)
		authed.DELETE("/history", auth.MiddlewareAdmin(), ClearHistory)
		authed.GET("/history/stats", HistoryStats)

		// 缓存
		authed.GET("/cache/stats", CacheStats)
		authed.DELETE("/cache", auth.MiddlewareAdmin(), CacheClear)
	}

	// ===== 前端静态资源 =====
	registerWebStatic(r)

	return r
}

// sameOriginHosts 运行期可被覆盖的同源白名单(默认为空, 仅靠请求 Host 推断)
// 用 atomic.Pointer 做整体替换, 避免运行期配置变更与请求处理路径的数据竞争.
var sameOriginHosts atomic.Pointer[[]string]

// corsMiddleware 返回自定义 CORS 中间件, 能够访问请求 Host 头进行动态同源判断.
// 这解决了 gin-contrib/cors 的 AllowOriginFunc 无法访问请求上下文的问题.
func corsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if origin != "" {
			allow := false
			host := c.Request.Host
			// 去除端口号进行比对(Host 可能含端口, Origin 也含端口, 直接前缀匹配即可)
			if strings.HasPrefix(origin, "http://"+host) || strings.HasPrefix(origin, "https://"+host) {
				allow = true
			} else {
				// 检查显式白名单
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
			if allow {
				c.Header("Access-Control-Allow-Origin", origin)
				c.Header("Vary", "Origin")
				c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
				c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization")
				c.Header("Access-Control-Expose-Headers", "Content-Length, Content-Type")
				c.Header("Access-Control-Allow-Credentials", "true")
			}
		}
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

// isSameOrigin 判断 origin 是否在显式白名单中(辅助函数, 主要逻辑在 corsMiddleware)
func isSameOrigin(origin string) bool {
	p := sameOriginHosts.Load()
	if p == nil {
		return false
	}
	for _, h := range *p {
		if origin == h {
			return true
		}
	}
	return false
}

// SetSameOriginHosts 设置允许的显式 Origin 白名单(供部署方按需放开跨域)
// 整体替换语义, 线程安全; 应在 r.Run 之前调用一次完成初始化.
func SetSameOriginHosts(hosts []string) {
	cp := append([]string(nil), hosts...)
	sameOriginHosts.Store(&cp)
}

// rateLimit 简易每 IP 令牌桶速率限制中间件(无外部依赖).
// limit 次 / window; 超限返回 429. 用于 /api/login /api/setup 防爆破与抢注轮询.
func rateLimit(name string, limit int, window time.Duration) gin.HandlerFunc {
	type bucket struct {
		count   int
		resetAt time.Time
	}
	var mu sync.Mutex
	buckets := make(map[string]*bucket)
	// 后台定期清理过期 bucket, 防止内存无限增长
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[fci] rate-limit cleanup goroutine panic: %v", r)
			}
		}()
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			mu.Lock()
			now := time.Now()
			for k, b := range buckets {
				if now.After(b.resetAt) {
					delete(buckets, k)
				}
			}
			mu.Unlock()
		}
	}()
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

// registerWebStatic 注册前端 SPA(支持 history 路由回退到 index.html)
// 优先使用磁盘 WebDir(便于开发热替换), 否则使用嵌入的 DistFS
func registerWebStatic(r *gin.Engine) {
	cfg := config.Get()
	var rootFS fs.FS
	if cfg.WebDir != "" {
		if _, err := os.Stat(filepath.Join(cfg.WebDir, "index.html")); err == nil {
			rootFS = os.DirFS(cfg.WebDir)
		}
	}
	if rootFS == nil {
		// 使用嵌入的前端产物
		emb, err := fs.Sub(web.DistFS, "dist")
		if err == nil {
			rootFS = emb
		}
	}

	// 预读 index.html 用于 SPA 回退(避免 http.FileServer 的 301 重定向)
	var indexBytes []byte
	if rootFS != nil {
		if f, err := rootFS.Open("index.html"); err == nil {
			indexBytes, _ = io.ReadAll(f)
			f.Close()
		}
	}

	// 静态资源路由(请求 /static/js/x.js → FS 中 static/js/x.js)
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
		// 安全: 防 path traversal. os.DirFS 不阻止 .. 越界, 攻击者构造
		// /static/../../etc/passwd 可读取任意文件. 显式拒绝 .. 并校验 Clean 后仍位于 static/ 下.
		if strings.Contains(fp, "..") {
			c.Status(http.StatusNotFound)
			return
		}
		cleaned := path.Clean("static/" + fp)
		if !strings.HasPrefix(cleaned, "static/") {
			c.Status(http.StatusNotFound)
			return
		}
		serveStaticFile(c, rootFS, cleaned)
	})

	// 根路径 → index.html
	r.GET("/", func(c *gin.Context) {
		serveIndex(c, indexBytes)
	})

	// SPA 深链回退
	r.NoRoute(func(c *gin.Context) {
		if strings.HasPrefix(c.Request.URL.Path, "/api") || strings.HasPrefix(c.Request.URL.Path, "/v1") {
			c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"message": "not found", "type": "not_found"}})
			return
		}
		if strings.HasPrefix(c.Request.URL.Path, "/static") {
			c.Status(http.StatusNotFound)
			return
		}
		serveIndex(c, indexBytes)
	})
}

func serveIndex(c *gin.Context, indexBytes []byte) {
	if len(indexBytes) == 0 {
		c.String(http.StatusOK, "fuck-chat-img backend is running. Web UI not built. Run `cd web && bun run build` then rebuild.")
		return
	}
	c.Data(http.StatusOK, "text/html; charset=utf-8", indexBytes)
}

// serveStaticFile 从 FS 读取并写入静态文件(带 Content-Type 推断与缓存头)
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
	data, err := io.ReadAll(f)
	if err != nil {
		c.Status(http.StatusInternalServerError)
		return
	}
	// 静态资源(带 hash 文件名的 JS/CSS)长期缓存, 其他文件不缓存
	if strings.HasSuffix(p, ".js") || strings.HasSuffix(p, ".css") || strings.HasSuffix(p, ".woff2") || strings.HasSuffix(p, ".woff") {
		c.Header("Cache-Control", "public, max-age=31536000, immutable")
	}
	c.Data(http.StatusOK, contentTypeFor(p), data)
}

func contentTypeFor(p string) string {
	switch {
	case strings.HasSuffix(p, ".js"):
		return "application/javascript; charset=utf-8"
	case strings.HasSuffix(p, ".css"):
		return "text/css; charset=utf-8"
	case strings.HasSuffix(p, ".html"):
		return "text/html; charset=utf-8"
	case strings.HasSuffix(p, ".json"):
		return "application/json; charset=utf-8"
	case strings.HasSuffix(p, ".svg"):
		return "image/svg+xml"
	case strings.HasSuffix(p, ".png"):
		return "image/png"
	case strings.HasSuffix(p, ".woff2"):
		return "font/woff2"
	case strings.HasSuffix(p, ".woff"):
		return "font/woff"
	default:
		return "application/octet-stream"
	}
}
