package config

import (
	"os"
	"strconv"
	"strings"
)

// Config 全局配置
type Config struct {
	ListenAddr     string
	DBPath         string
	WebDir         string // 前端静态资源目录; 留空使用嵌入前端
	JWTSecret      string
	AdminUser      string
	AdminPass      string // bcrypt 哈希后的密码，若为空则在首次启动时使用 InitAdminPass 并写入
	InitAdminPass  string
	CacheEnabled   bool
	CacheMaxItems  int
	RequestTimeout int // 上游请求超时(秒)
}

var cfg = Config{
	ListenAddr:     ":8080",
	DBPath:         "./data/fci.db",
	WebDir:         "", // 默认空: 使用嵌入前端; 开发时通过环境变量指向 web/dist
	JWTSecret:      "fuck-chat-img-default-secret-change-me",
	AdminUser:      "root",
	CacheEnabled:   true,
	CacheMaxItems:  10000,
	RequestTimeout: 300,
}

// Get 返回全局配置
func Get() *Config { return &cfg }

// Load 从环境变量加载配置
func Load() {
	if v := os.Getenv("FCI_LISTEN"); v != "" {
		cfg.ListenAddr = v
	}
	if v := os.Getenv("FCI_DB_PATH"); v != "" {
		cfg.DBPath = v
	}
	if v := os.Getenv("FCI_WEB_DIR"); v != "" {
		cfg.WebDir = v
	}
	if v := os.Getenv("FCI_JWT_SECRET"); v != "" {
		cfg.JWTSecret = v
	}
	if v := os.Getenv("FCI_ADMIN_USER"); v != "" {
		cfg.AdminUser = v
	}
	if v := os.Getenv("FCI_ADMIN_PASS"); v != "" {
		// 若已像 bcrypt 哈希则直接用；否则在初始化时会被哈希
		cfg.InitAdminPass = v
	}
	if v := os.Getenv("FCI_CACHE_ENABLED"); v != "" {
		cfg.CacheEnabled = strings.EqualFold(v, "true") || v == "1"
	}
	if v := os.Getenv("FCI_CACHE_MAX"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.CacheMaxItems = n
		}
	}
	if v := os.Getenv("FCI_REQUEST_TIMEOUT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.RequestTimeout = n
		}
	}
}
