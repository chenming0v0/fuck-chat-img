package api

import (
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/fuck-chat-img/fci/internal/auth"
	"github.com/fuck-chat-img/fci/internal/config"
	"github.com/fuck-chat-img/fci/internal/proxy"
	"github.com/fuck-chat-img/fci/web"
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
)

// SetupRouter 装配路由
func SetupRouter() *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())
	// 安全: CORS 不能同时允许任意 Origin 和 Credentials
	r.Use(cors.New(cors.Config{
		AllowOriginFunc: func(origin string) bool {
			// 同源请求 Origin 为空, 直接放行; 否则只允许配置的来源(此处同源部署, 不允许跨域)
			return origin == ""
		},
		AllowMethods:     []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Content-Type", "Authorization"},
		ExposeHeaders:    []string{"Content-Length", "Content-Type"},
		AllowCredentials: true,
	}))
	// 安全: 基础安全响应头
	r.Use(func(c *gin.Context) {
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "DENY")
		c.Next()
	})

	// ===== 公开接口 =====
	api := r.Group("/api")
	api.GET("/status", Status)
	api.POST("/login", Login)
	api.POST("/setup", Setup) // 首次设置管理员(仅在无任何用户时可用)

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
	v1.POST("/messages/", proxy.HandleMessages)

	// ===== 需登录的管理接口 =====
	authed := api.Group("")
	authed.Use(auth.MiddlewareAuth())
	{
		authed.GET("/user", UserInfo)
		authed.POST("/user/password", ChangePassword)

		// 模型组管理(普通用户只读, 管理员可写)
		authed.GET("/groups", ListGroups)
		authed.GET("/groups/:id", GetGroup)
		// 明文 API Key 仅管理员可获取(用于编辑回填)
		authed.GET("/groups/:id/plain", auth.MiddlewareAdmin(), GetGroupPlain)
		authed.POST("/groups", auth.MiddlewareAdmin(), CreateGroup)
		authed.PUT("/groups/:id", auth.MiddlewareAdmin(), UpdateGroup)
		authed.DELETE("/groups/:id", auth.MiddlewareAdmin(), DeleteGroup)
		authed.POST("/groups/:id/toggle", auth.MiddlewareAdmin(), ToggleGroup)
		authed.GET("/groups/:id/test", TestGroup)

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
		serveStaticFile(c, rootFS, "static/"+fp)
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
		if strings.HasPrefix(c.Request.URL.Path, "/static/") {
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
