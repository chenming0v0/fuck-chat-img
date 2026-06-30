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
	token, err := auth.GenerateToken(u)
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
			"expires_at": time.Now().Add(7 * 24 * time.Hour).Unix(),
		},
	})
}

// UserInfo 当前用户信息
func UserInfo(c *gin.Context) {
	uid, _ := c.Get(auth.ContextKeyUserID)
	username, _ := c.Get(auth.ContextKeyUsername)
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
	uidVal, _ := c.Get(auth.ContextKeyUserID)
	usernameVal, _ := c.Get(auth.ContextKeyUsername)
	uid, ok := uidVal.(uint)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "未登录"})
		return
	}
	username, ok := usernameVal.(string)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "未登录"})
		return
	}
	if _, ok := model.VerifyPassword(username, req.OldPassword); !ok {
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
