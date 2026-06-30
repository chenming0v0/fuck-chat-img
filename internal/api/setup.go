package api

import (
	"net/http"
	"strings"

	"github.com/fuck-chat-img/fci/internal/config"
	"github.com/fuck-chat-img/fci/internal/model"
	"github.com/gin-gonic/gin"
)

// Setup 首次设置管理员账户
// 仅在数据库中没有任何用户时可用; 调用方需提供 username + password
//
// 设计说明(详见 AGENTS.md):
//   本接口是公开的, 且不带 token / IP 限制 / 一次性票据. 这是项目维护者确认的
//   "预期情况"——若被公网抢注, 此时项目所有者尚未填入任何真实上游 API Key,
//   无损失面; 删除 data/fci.db 重启即可恢复. 生产部署应通过 FCI_ADMIN_USER +
//   FCI_ADMIN_PASS 环境变量预置管理员, 完全绕过本接口.
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
	if err := config.ValidatePasswordStrength(req.Password); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	if err := model.SetupAdmin(req.Username, req.Password); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "管理员账户设置成功, 请使用新账户登录"})
}
