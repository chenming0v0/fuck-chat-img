package api

import (
	"net/http"
	"strings"

	"github.com/fuck-chat-img/fci/internal/model"
	"github.com/gin-gonic/gin"
)

// Setup 首次设置管理员账户
// 仅在数据库中没有任何用户时可用; 调用方需提供 username + password
//
// POST /api/setup
// Body: {"username":"...","password":"..."}
func Setup(c *gin.Context) {
	if !model.IsSetupRequired() {
		c.JSON(http.StatusConflict, gin.H{"success": false, "message": "管理员账户已存在, 无需再次设置"})
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "参数错误: " + err.Error()})
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if req.Username == "" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "用户名不能为空"})
		return
	}
	if len(req.Password) < 6 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "密码至少 6 位"})
		return
	}
	if err := model.SetupAdmin(req.Username, req.Password); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "管理员账户设置成功, 请使用新账户登录"})
}
