package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log"
	"os"
	"strconv"
	"strings"
)

// MinPasswordLength 全项目统一的密码最小长度(Setup / ChangePassword / initAdminFromEnv 共用)
const MinPasswordLength = 6

// Config 全局配置
type Config struct {
	ListenAddr     string
	DBPath         string
	WebDir         string // 前端静态资源目录
	JWTSecret      string
	AdminUser      string
	InitAdminPass  string // 通过 FCI_ADMIN_PASS 预置的明文密码(仅首次启动创建管理员时使用)
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
	// FCI_WEB_DIR: 使用 LookupEnv 区分"未设置"(用默认嵌入)与"显式设为空"(强制用嵌入)
	if v, ok := os.LookupEnv("FCI_WEB_DIR"); ok {
		cfg.WebDir = v
	}
	if v := os.Getenv("FCI_JWT_SECRET"); v != "" {
		cfg.JWTSecret = v
	}
	if v := os.Getenv("FCI_ADMIN_USER"); v != "" {
		cfg.AdminUser = v
	}
	if v := os.Getenv("FCI_ADMIN_PASS"); v != "" {
		// 明文传入, 服务端在 initAdminFromEnv 中会 bcrypt 哈希后存储
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
	// 安全: 若未配置 JWT 密钥, 用 crypto/rand 生成随机密钥并警告(每次重启会变化, 导致旧 token 失效)
	if cfg.JWTSecret == "" {
		cfg.JWTSecret = randomSecret(32)
		log.Printf("[fci] 警告: 未设置 FCI_JWT_SECRET, 已生成临时密钥。请配置 FCI_JWT_SECRET 环境变量以保持登录态稳定")
	}
}

// randomSecret 用 crypto/rand 生成密码学安全的随机十六进制密钥
func randomSecret(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand 失败属于无法安全运行的致命情况, 直接 panic
		panic("[fci] crypto/rand 失败: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// ValidatePasswordStrength 统一密码强度校验(供 Setup / ChangePassword / initAdminFromEnv 复用)
func ValidatePasswordStrength(password string) error {
	if len(password) < MinPasswordLength {
		return errors.New("密码至少 " + strconv.Itoa(MinPasswordLength) + " 位")
	}
	return nil
}
