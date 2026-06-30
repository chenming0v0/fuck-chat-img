package model

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/fuck-chat-img/fci/internal/config"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var DB *gorm.DB

// Init 初始化数据库
func Init() error {
	cfg := config.Get()
	if dir := filepath.Dir(cfg.DBPath); dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
	}
	db, err := gorm.Open(sqlite.Open(cfg.DBPath), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
	})
	if err != nil {
		return err
	}
	if err := db.AutoMigrate(&User{}, &ModelGroup{}, &History{}); err != nil {
		return err
	}
	DB = db

	// 不再创建默认密码账户; 首次启动时由用户在 Web 设置页输入管理密码
	// 若通过 FCI_ADMIN_USER / FCI_ADMIN_PASS 环境变量预置, 则在 initAdminFromEnv 中创建
	if err := initAdminFromEnv(); err != nil {
		return err
	}
	return nil
}

// initAdminFromEnv 若设置了 FCI_ADMIN_USER + FCI_ADMIN_PASS 环境变量,
// 并且当前没有任何用户, 则按环境变量创建管理员(便于自动化部署/容器化场景)
func initAdminFromEnv() error {
	cfg := config.Get()
	if cfg.InitAdminPass == "" {
		return nil
	}
	// 与 Setup / ChangePassword 保持一致的密码强度校验, 避免环境变量误设弱密码
	if err := config.ValidatePasswordStrength(cfg.InitAdminPass); err != nil {
		return fmt.Errorf("FCI_ADMIN_PASS %w", err)
	}
	// 与 SetupAdmin 保持一致的用户名规范化与校验(Low-8): 此前直接用 cfg.AdminUser,
	// 未校验空白, 可能创建出 username 为空白/默认值 "root" 的管理员, 与 Setup 流程行为不一致
	adminUser := strings.TrimSpace(cfg.AdminUser)
	if adminUser == "" {
		return fmt.Errorf("FCI_ADMIN_USER 不能为空")
	}
	var count int64
	DB.Model(&User{}).Count(&count)
	if count > 0 {
		return nil
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(cfg.InitAdminPass), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	u := User{
		Username:     adminUser,
		PasswordHash: string(hash),
		Role:         "admin",
		Status:       1,
	}
	if err := DB.Create(&u).Error; err != nil {
		// Low-9: 唯一约束冲突不应阻断启动(并发初始化/旧数据残留都可能触发).
		// 与 SetupAdmin 一致地视为"已存在, 跳过"而非致命错误
		if errors.Is(err, gorm.ErrDuplicatedKey) || isUniqueConstraintErr(err) {
			log.Printf("[fci] 管理员账户 %s 已存在, 跳过创建", adminUser)
			return nil
		}
		return err
	}
	log.Printf("[fci] 已通过环境变量创建管理员账户: %s", adminUser)
	// Low-6: 创建完成后清零内存中的明文密码引用, 减少常驻内存暴露面
	// (Go 字符串不可变无法真正擦除底层字节, 但消除 cfg.InitAdminPass 这一稳定引用)
	cfg.InitAdminPass = ""
	return nil
}

// IsSetupRequired 判断是否需要进行首次管理员设置(没有任何用户时返回 true)
func IsSetupRequired() bool {
	var count int64
	DB.Model(&User{}).Count(&count)
	return count == 0
}

// SetupAdmin 首次设置管理员账户(仅在没有任何用户时可用)
// username / password 由用户在前端设置页输入
func SetupAdmin(username, password string) error {
	if !IsSetupRequired() {
		return fmt.Errorf("管理员账户已存在, 无需再次设置")
	}
	username = strings.TrimSpace(username)
	if username == "" {
		return fmt.Errorf("用户名不能为空")
	}
	if err := config.ValidatePasswordStrength(password); err != nil {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	u := User{
		Username:     username,
		PasswordHash: string(hash),
		Role:         "admin",
		Status:       1,
	}
	if err := DB.Create(&u).Error; err != nil {
		// 用户名唯一约束冲突时返回友好提示
		if errors.Is(err, gorm.ErrDuplicatedKey) || isUniqueConstraintErr(err) {
			return fmt.Errorf("用户名 %s 已存在", username)
		}
		return err
	}
	log.Printf("[fci] 首次设置完成, 管理员账户已创建: %s", username)
	return nil
}

// isUniqueConstraintErr 兜底识别 SQLite 的 UNIQUE 约束错误(GORM v2 对 SQLite 的 DuplicatedKey 识别不全)
func isUniqueConstraintErr(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "UNIQUE constraint failed") || strings.Contains(s, "Duplicate entry")
}

// VerifyPassword 校验密码
func VerifyPassword(username, password string) (*User, bool) {
	var u User
	if err := DB.Where("username = ? AND status = 1", username).First(&u).Error; err != nil {
		return nil, false
	}
	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)); err != nil {
		return nil, false
	}
	return &u, true
}

// UpdatePassword 更新密码
func UpdatePassword(userID uint, newPlain string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(newPlain), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	return DB.Model(&User{}).Where("id = ?", userID).Update("password_hash", string(hash)).Error
}
