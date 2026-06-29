package model

import (
	"log"
	"os"
	"path/filepath"

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

	// 初始化管理员账户
	if err := initAdmin(); err != nil {
		return err
	}
	return nil
}

func initAdmin() error {
	cfg := config.Get()
	var count int64
	DB.Model(&User{}).Count(&count)
	if count > 0 {
		// 已存在账户: 若设置了初始密码环境变量则不覆盖
		return nil
	}
	plain := cfg.InitAdminPass
	if plain == "" {
		plain = "123456"
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	u := User{
		Username:     cfg.AdminUser,
		PasswordHash: string(hash),
		Role:         "admin",
		Status:       1,
	}
	if err := DB.Create(&u).Error; err != nil {
		return err
	}
	log.Printf("[fci] 初始管理员账户已创建: %s / %s (请尽快修改密码)", cfg.AdminUser, plain)
	return nil
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
