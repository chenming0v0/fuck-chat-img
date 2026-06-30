package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/fuck-chat-img/fci/internal/auth"
	"github.com/fuck-chat-img/fci/internal/config"
	"github.com/fuck-chat-img/fci/internal/model"
	"github.com/gin-gonic/gin"
)

// Login 登录
func Login(c *gin.Context) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "参数错误"})
		return
	}
	u, ok := model.VerifyPassword(strings.TrimSpace(req.Username), req.Password)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "用户名或密码错误"})
		return
	}
	token, expiresAt, err := auth.GenerateToken(u)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "生成令牌失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"token":      token,
			"username":   u.Username,
			"role":       u.Role,
			"expires_at": expiresAt.Unix(),
		},
	})
}

// UserInfo 当前用户信息
func UserInfo(c *gin.Context) {
	uid, ok := c.Get(auth.ContextKeyUserID)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "未登录"})
		return
	}
	username, ok := c.Get(auth.ContextKeyUsername)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "未登录"})
		return
	}
	role, _ := c.Get(auth.ContextKeyRole)
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"id":       uid,
			"username": username,
			"role":     role,
		},
	})
}

// ChangePassword 修改密码
func ChangePassword(c *gin.Context) {
	var req struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "参数错误"})
		return
	}
	uid, ok := currentUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "未登录"})
		return
	}
	if _, ok := model.VerifyPasswordByID(uid, req.OldPassword); !ok {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "原密码错误"})
		return
	}
	if err := config.ValidatePasswordStrength(req.NewPassword); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	if err := model.UpdatePassword(uid, req.NewPassword); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "密码已修改"})
}

// Status 公开状态(前端启动检查)
// need_setup=true 表示当前没有任何用户, 前端应跳转到 /setup 引导用户设置管理密码
func Status(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"version":      "1.0.0",
			"name":         "fuck-chat-img",
			"require_auth": true,
			"need_setup":   model.IsSetupRequired(),
			"server_time":  time.Now().Unix(),
		},
	})
}

// currentUserID 从 gin.Context 提取已登录用户 ID.
// 返回 (0, false) 表示 context 中缺少 UserID(中间件不变量被破坏), 调用方应 403 拒绝.
// 不再回落到 user_id=0 查询, 避免暴露匿名代理历史(Low-8 越权风险).
func currentUserID(c *gin.Context) (uint, bool) {
	v, exists := c.Get(auth.ContextKeyUserID)
	if !exists {
		return 0, false
	}
	uid, ok := v.(uint)
	return uid, ok
}

// isAdminContext 判断当前请求是否为管理员
func isAdminContext(c *gin.Context) bool {
	v, exists := c.Get(auth.ContextKeyAdmin)
	if !exists {
		return false
	}
	isAdmin, ok := v.(bool)
	return ok && isAdmin
}
