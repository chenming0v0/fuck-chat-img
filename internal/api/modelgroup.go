package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/fuck-chat-img/fci/internal/model"
	"github.com/fuck-chat-img/fci/internal/proxy"
	"github.com/gin-gonic/gin"
)

// ListGroups 列出模型组
func ListGroups(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("p", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("size", "20"))
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 100 {
		size = 20
	}
	keyword := strings.TrimSpace(c.Query("keyword"))

	var total int64
	q := model.DB.Model(&model.ModelGroup{})
	if keyword != "" {
		q = q.Where("name LIKE ? OR description LIKE ?", "%"+keyword+"%", "%"+keyword+"%")
	}
	q.Count(&total)
	var groups []model.ModelGroup
	q.Order("id DESC").Offset((page - 1) * size).Limit(size).Find(&groups)

	// 脱敏 api_key
	out := make([]gin.H, 0, len(groups))
	for _, g := range groups {
		out = append(out, groupToDTO(g, true))
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    out,
		"total":   total,
		"page":    page,
		"size":    size,
	})
}

// GetGroup 获取单个(始终脱敏 API Key, 编辑时通过 GetGroupPlain 获取明文)
func GetGroup(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var g model.ModelGroup
	if err := model.DB.First(&g, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "模型组不存在"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": groupToDTO(g, true)})
}

// GetGroupPlain 获取单个(明文 API Key, 仅管理员可用, 用于编辑表单回填)
func GetGroupPlain(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var g model.ModelGroup
	if err := model.DB.First(&g, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "模型组不存在"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": groupToDTO(g, false)})
}

// CreateGroup 创建
func CreateGroup(c *gin.Context) {
	var req createGroupReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "参数错误: " + err.Error()})
		return
	}
	if err := validateGroupReq(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	mainJSON, _ := json.Marshal(req.MainTextModel)
	imgJSON, _ := json.Marshal(req.ImageModels)
	g := model.ModelGroup{
		Name:          req.Name,
		Description:   req.Description,
		MainTextModel: string(mainJSON),
		ImageModels:   string(imgJSON),
		ImageStrategy: req.ImageStrategy,
		ImagePrompt:   req.ImagePrompt,
		ReplaceImage:  req.ReplaceImage,
		Enabled:       req.Enabled,
	}
	if err := model.DB.Create(&g).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "创建失败(可能名称已存在): " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": groupToDTO(g, false), "message": "创建成功"})
}

// UpdateGroup 更新
func UpdateGroup(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var g model.ModelGroup
	if err := model.DB.First(&g, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "模型组不存在"})
		return
	}
	var req createGroupReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "参数错误: " + err.Error()})
		return
	}
	if err := validateGroupReq(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	mainJSON, _ := json.Marshal(req.MainTextModel)
	imgJSON, _ := json.Marshal(req.ImageModels)
	g.Name = req.Name
	g.Description = req.Description
	g.MainTextModel = string(mainJSON)
	g.ImageModels = string(imgJSON)
	g.ImageStrategy = req.ImageStrategy
	g.ImagePrompt = req.ImagePrompt
	g.ReplaceImage = req.ReplaceImage
	g.Enabled = req.Enabled
	if err := model.DB.Save(&g).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "更新失败: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": groupToDTO(g, false), "message": "更新成功"})
}

// DeleteGroup 删除
func DeleteGroup(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	// 先查出名称, 用于清理轮询游标
	var g model.ModelGroup
	if err := model.DB.First(&g, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "模型组不存在"})
		return
	}
	if err := model.DB.Delete(&model.ModelGroup{}, id).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	// 清理轮询游标, 防止内存泄漏
	proxy.CleanupRRIndex(map[string]bool{g.Name: false})
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "已删除"})
}

// ToggleGroup 启用/禁用
func ToggleGroup(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var g model.ModelGroup
	if err := model.DB.First(&g, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "模型组不存在"})
		return
	}
	g.Enabled = !g.Enabled
	if err := model.DB.Save(&g).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "更新失败: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"enabled": g.Enabled}})
}

// TestGroup 测试模型组(可选: 简单 ping 主模型)
func TestGroup(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var g model.ModelGroup
	if err := model.DB.First(&g, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "模型组不存在"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "测试接口(请通过 /v1/responses 实测)", "data": groupToDTO(g, false)})
}

type createGroupReq struct {
	Name          string             `json:"name"`
	Description   string             `json:"description"`
	MainTextModel model.UpstreamModel `json:"main_text_model"`
	ImageModels   []model.UpstreamModel `json:"image_models"`
	ImageStrategy string             `json:"image_strategy"`
	ImagePrompt   string             `json:"image_prompt"`
	ReplaceImage  bool               `json:"replace_image"`
	Enabled       bool               `json:"enabled"`
}

func validateGroupReq(r *createGroupReq) error {
	r.Name = strings.TrimSpace(r.Name)
	if r.Name == "" {
		return errStr("模型组名称不能为空")
	}
	if r.MainTextModel.BaseURL == "" || r.MainTextModel.APIKey == "" || r.MainTextModel.Model == "" {
		return errStr("主对话模型需填写 base_url/api_key/model")
	}
	if r.ImageStrategy == "" {
		r.ImageStrategy = "round_robin"
	}
	if r.ImageStrategy != "round_robin" && r.ImageStrategy != "failover" {
		return errStr("图片策略仅支持 round_robin / failover")
	}
	for i := range r.ImageModels {
		if r.ImageModels[i].BaseURL == "" {
			return errStr("图片模型 base_url 不能为空")
		}
		if r.ImageModels[i].APIKey == "" {
			return errStr("图片模型 api_key 不能为空")
		}
		if r.ImageModels[i].Model == "" {
			return errStr("图片模型 model 不能为空")
		}
	}
	return nil
}

func errStr(s string) error { return &validateErr{msg: s} }

type validateErr struct{ msg string }

func (e *validateErr) Error() string { return e.msg }

// groupToDTO 转为前端 DTO, maskKey=true 时对 api_key 脱敏
func groupToDTO(g model.ModelGroup, maskKey bool) gin.H {
	var main model.UpstreamModel
	_ = json.Unmarshal([]byte(g.MainTextModel), &main)
	var imgs []model.UpstreamModel
	_ = json.Unmarshal([]byte(g.ImageModels), &imgs)
	if maskKey {
		main.APIKey = maskKeyStr(main.APIKey)
		for i := range imgs {
			imgs[i].APIKey = maskKeyStr(imgs[i].APIKey)
		}
	}
	return gin.H{
		"id":             g.ID,
		"name":           g.Name,
		"description":    g.Description,
		"main_text_model": main,
		"image_models":   imgs,
		"image_strategy": g.ImageStrategy,
		"image_prompt":   g.ImagePrompt,
		"replace_image":  g.ReplaceImage,
		"enabled":        g.Enabled,
		"created_at":     g.CreatedAt,
		"updated_at":     g.UpdatedAt,
	}
}

func maskKeyStr(k string) string {
	if k == "" {
		return ""
	}
	if len(k) <= 8 {
		return strings.Repeat("*", len(k))
	}
	return k[:4] + strings.Repeat("*", len(k)-8) + k[len(k)-4:]
}
