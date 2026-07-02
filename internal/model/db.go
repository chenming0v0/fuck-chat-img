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

func buildDSN(path string) string {
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	if strings.Contains(path, ":memory:") {
		return path + sep + "_busy_timeout=5000&_foreign_keys=ON"
	}
	return path + sep + "_journal_mode=WAL&_busy_timeout=5000&_synchronous=NORMAL&_foreign_keys=ON"
}

func initDB(dsn string, needEnv bool) error {
	db, err := gorm.Open(sqlite.Open(buildDSN(dsn)), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
	})
	if err != nil {
		return err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&User{}, &ModelGroup{}, &History{}); err != nil {
		sqlDB.Close()
		return err
	}
	DB = db
	if needEnv {
		if err := initAdminFromEnv(); err != nil {
			sqlDB.Close()
			DB = nil
			return err
		}
	}
	return nil
}

// Init 初始化数据库
func Init() error {
	cfg := config.Get()
	if dir := filepath.Dir(cfg.DBPath); dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
	}
	return initDB(cfg.DBPath, true)
}

func InitTestDB(dsn string) error {
	return initDB(dsn, false)
}

// initAdminFromEnv 若设置了 FCI_ADMIN_USER + FCI_ADMIN_PASS 环境变量,
// 并且当前没有任何用户, 则按环境变量创建管理员(便于自动化部署/容器化场景)
func initAdminFromEnv() error {
	cfg := config.Get()
	if cfg.InitAdminPass == "" {
		return nil
	}
	// 创建完成后(无论成功失败)清零内存中的明文密码引用, 减少常驻内存暴露面
	defer config.ClearInitAdminPass()
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
	if err := DB.Model(&User{}).Count(&count).Error; err != nil {
		return fmt.Errorf("查询用户数量失败: %w", err)
	}
	if count > 0 {
		log.Printf("[fci] 已存在 %d 个用户, 跳过环境变量管理员创建", count)
		return nil
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(cfg.InitAdminPass), config.BcryptCost)
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
		// 唯一约束冲突不应阻断启动(并发初始化/旧数据残留都可能触发).
		// 与 SetupAdmin 一致地视为"已存在, 跳过"而非致命错误
		if errors.Is(err, gorm.ErrDuplicatedKey) || isUniqueConstraintErr(err) {
			log.Printf("[fci] 管理员账户 %s 已存在, 跳过创建", adminUser)
			return nil
		}
		return err
	}
	log.Printf("[fci] 已通过环境变量创建管理员账户: %s", adminUser)
	return nil
}

// IsSetupRequired 判断是否需要进行首次管理员设置(没有任何用户时返回 true)
// 查询失败时 fail-open 返回 true: 引导走 setup 流程更易恢复, 避免把首次安装锁死.
func IsSetupRequired() bool {
	var count int64
	if err := DB.Model(&User{}).Count(&count).Error; err != nil {
		log.Printf("[fci] IsSetupRequired 查询失败: %v", err)
		return true
	}
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
	hash, err := bcrypt.GenerateFromPassword([]byte(password), config.BcryptCost)
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

// dummyHash 用 cost 12 生成, 与 config.BcryptCost 一致, 确保"用户不存在"路径
// 与"用户存在但密码错"路径的 bcrypt 比对耗时相近, 防止时序侧信道枚举用户名.
// 若生成失败则 panic: 输入合法且 cost 来自 config, 失败意味着 bcrypt 库/配置异常,
// 此时 dummyHash 为空会导致时序防护失效, 应硬失败而非静默降级.
var dummyHash = func() string {
	h, err := bcrypt.GenerateFromPassword([]byte("dummy-fixed-nonsecret-do-not-login"), config.BcryptCost)
	if err != nil {
		panic("failed to generate dummyHash: " + err.Error())
	}
	return string(h)
}()

func verifyPasswordWithHash(hash string, password string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	return err == nil
}

// VerifyPassword 校验密码
func VerifyPassword(username, password string) (*User, bool) {
	var u User
	if err := DB.Where("username = ? AND status = 1", username).First(&u).Error; err != nil {
		verifyPasswordWithHash(dummyHash, password)
		return nil, false
	}
	if !verifyPasswordWithHash(u.PasswordHash, password) {
		return nil, false
	}
	return &u, true
}

// VerifyPasswordByID 通过用户ID校验密码
func VerifyPasswordByID(userID uint, password string) (*User, bool) {
	var u User
	if err := DB.Where("id = ? AND status = 1", userID).First(&u).Error; err != nil {
		verifyPasswordWithHash(dummyHash, password)
		return nil, false
	}
	if !verifyPasswordWithHash(u.PasswordHash, password) {
		return nil, false
	}
	return &u, true
}

// UpdatePassword 更新密码并递增token_version, 立即使所有旧JWT失效
func UpdatePassword(userID uint, newPlain string) error {
	if userID == 0 {
		return errors.New("invalid user id")
	}
	if err := config.ValidatePasswordStrength(newPlain); err != nil {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPlain), config.BcryptCost)
	if err != nil {
		return err
	}
	result := DB.Model(&User{}).Where("id = ? AND status = 1", userID).Updates(map[string]interface{}{
		"password_hash": string(hash),
		"token_version": gorm.Expr("token_version + 1"),
	})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return errors.New("用户不存在")
	}
	return nil
}

// GetUserTokenVersion 获取用户当前token版本
func GetUserTokenVersion(userID uint) (int, error) {
	var u User
	if err := DB.Select("token_version").Where("id = ? AND status = 1", userID).First(&u).Error; err != nil {
		return 0, err
	}
	return u.TokenVersion, nil
}

// GetUserByID 根据ID获取用户
func GetUserByID(userID uint) (*User, error) {
	var u User
	if err := DB.Where("id = ? AND status = 1", userID).First(&u).Error; err != nil {
		return nil, err
	}
	return &u, nil
}
