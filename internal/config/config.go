package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
)

const (
	// MinPasswordLength 全项目统一的密码最小长度(Setup / ChangePassword / initAdminFromEnv 共用)
	MinPasswordLength = 6
	// MaxPasswordLength 全项目统一的密码最大长度.
	// bcrypt 会对超过 72 字节的密码静默截断只取前 72 字节, 两个前 72 位相同的长密码哈希一致,
	// 削弱安全边际. 这里在入口处统一拒绝超长密码, 避免静默截断(Low-7).
	MaxPasswordLength     = 72
	defaultCacheMaxItems  = 10000
	defaultRequestTimeout = 300
)

// Config 全局配置
type Config struct {
	ListenAddr     string
	DBPath         string
	WebDir         string // 前端静态资源目录
	JWTSecret      string
	AdminUser      string
	InitAdminPass  string // 通过 FCI_ADMIN_PASS 预置的明文密码(仅首次启动创建管理员时使用)
	ProxyKey       string
	CacheEnabled   bool
	CacheMaxItems  int
	RequestTimeout int // 上游请求超时(秒)
}

var (
	cfgMu sync.RWMutex
	cfg = Config{
		ListenAddr:     ":8080",
		DBPath:         "./data/fci.db",
		WebDir:         "./web/dist",
		JWTSecret:      "",
		AdminUser:      "root",
		CacheEnabled:   true,
		CacheMaxItems:  defaultCacheMaxItems,
		RequestTimeout: defaultRequestTimeout,
	}
)

// Get 返回全局配置的副本(防止外部篡改全局状态)
func Get() Config {
	cfgMu.RLock()
	defer cfgMu.RUnlock()
	return cfg
}

// ClearInitAdminPass 清零内存中的明文管理员密码(初始化完成后调用)
func ClearInitAdminPass() {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	cfg.InitAdminPass = ""
}

// SetWebDirForTest 仅用于测试: 覆盖前端静态资源目录
func SetWebDirForTest(dir string) {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	cfg.WebDir = dir
}

// SetJWTSecretForTest 仅用于测试: 覆盖JWT密钥
func SetJWTSecretForTest(secret string) {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	cfg.JWTSecret = secret
}

// SetProxyKeyForTest 仅用于测试: 覆盖代理密钥
func SetProxyKeyForTest(key string) {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	cfg.ProxyKey = key
}

// Load 从环境变量加载配置
func Load() {
	cfgMu.Lock()
	defer cfgMu.Unlock()
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
	if v := os.Getenv("FCI_PROXY_KEY"); v != "" {
		cfg.ProxyKey = v
	}
	if v := os.Getenv("FCI_CACHE_ENABLED"); v != "" {
		vLower := strings.ToLower(v)
		switch vLower {
		case "true", "1", "yes", "on":
			cfg.CacheEnabled = true
		case "false", "0", "no", "off":
			cfg.CacheEnabled = false
		default:
			log.Printf("[fci] 警告: FCI_CACHE_ENABLED=%s 无法识别，使用默认值 true", v)
		}
	}
	if v := os.Getenv("FCI_CACHE_MAX"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.CacheMaxItems = n
		} else {
			log.Printf("[fci] 警告: FCI_CACHE_MAX=%s 无效，使用默认值 %d", v, defaultCacheMaxItems)
		}
	}
	if v := os.Getenv("FCI_REQUEST_TIMEOUT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.RequestTimeout = n
		} else {
			log.Printf("[fci] 警告: FCI_REQUEST_TIMEOUT=%s 无效，使用默认值 %d", v, defaultRequestTimeout)
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
	if len(password) > MaxPasswordLength {
		return errors.New("密码不能超过 " + strconv.Itoa(MaxPasswordLength) + " 位(bcrypt 限制)")
	}
	return nil
}
