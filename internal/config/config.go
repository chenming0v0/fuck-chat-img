package config

import (
	"encoding/hex"
	"log"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config 全局配置
type Config struct {
	ListenAddr     string
	DBPath         string
	WebDir         string // 前端静态资源目录
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
	WebDir:         "./web/dist",
	JWTSecret:      "",
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
	// 安全: 若未配置 JWT 密钥, 生成随机密钥并警告(每次重启会变化, 导致旧 token 失效)
	if cfg.JWTSecret == "" {
		cfg.JWTSecret = randomSecret(32)
		log.Printf("[fci] 警告: 未设置 FCI_JWT_SECRET, 已生成临时密钥。请配置 FCI_JWT_SECRET 环境变量以保持登录态稳定")
	}
}

// randomSecret 生成随机十六进制密钥
func randomSecret(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(randInt(0, 256))
	}
	return hex.EncodeToString(b)
}

func randInt(min, max int) int {
	return min + int(randUint32())%(max-min)
}

var randCounter uint64

func randUint32() uint32 {
	// 简单的伪随机(基于纳秒), 仅用于未配置密钥时的临时生成
	now := time.Now().UnixNano()
	randCounter++
	return uint32(now) ^ uint32(randCounter<<16)
}
