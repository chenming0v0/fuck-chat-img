package api

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/fuck-chat-img/fci/internal/model"
	"github.com/fuck-chat-img/fci/internal/proxy"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
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

	applyFilters := func(q *gorm.DB) *gorm.DB {
		if keyword != "" {
			escaped := escapeLike(keyword)
			q = q.Where("name LIKE ? ESCAPE '\\' OR description LIKE ? ESCAPE '\\'", "%"+escaped+"%", "%"+escaped+"%")
		}
		return q
	}

	var total int64
	if err := applyFilters(model.DB.Session(&gorm.Session{}).Model(&model.ModelGroup{})).Count(&total).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": err.Error()})
		return
	}
	var groups []model.ModelGroup
	if err := applyFilters(model.DB.Session(&gorm.Session{}).Model(&model.ModelGroup{})).
		Order("id DESC").Offset((page - 1) * size).Limit(size).Find(&groups).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": err.Error()})
		return
	}

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

// GetGroup 获取单个
func GetGroup(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id < 1 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "非法 id"})
		return
	}
	var g model.ModelGroup
	if err := model.DB.First(&g, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "模型组不存在"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": groupToDTO(g, true)})
}

// GetGroupPlain 获取单个
func GetGroupPlain(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id < 1 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "非法 id"})
		return
	}
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
	mainJSON, err := json.Marshal(req.MainTextModel)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "序列化失败: " + err.Error()})
		return
	}
	imgJSON, err := json.Marshal(req.ImageModels)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "序列化失败: " + err.Error()})
		return
	}
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
		if errors.Is(err, gorm.ErrDuplicatedKey) || isUniqueConstraintErr(err) {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "模型组名称已存在"})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "创建失败: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": groupToDTO(g, false), "message": "创建成功"})
}

// UpdateGroup 更新
func UpdateGroup(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id < 1 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "非法 id"})
		return
	}
	var g model.ModelGroup
	if err := model.DB.First(&g, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "模型组不存在"})
		return
	}
	oldName := g.Name
	var req createGroupReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "参数错误: " + err.Error()})
		return
	}
	if err := validateGroupReq(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	mainJSON, err := json.Marshal(req.MainTextModel)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "序列化失败: " + err.Error()})
		return
	}
	imgJSON, err := json.Marshal(req.ImageModels)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "序列化失败: " + err.Error()})
		return
	}
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
	if oldName != req.Name {
		proxy.DeleteRRIndex(oldName)
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": groupToDTO(g, false), "message": "更新成功"})
}

// DeleteGroup 删除
func DeleteGroup(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id < 1 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "非法 id"})
		return
	}
	var g model.ModelGroup
	if err := model.DB.First(&g, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "模型组不存在"})
		return
	}
	if err := model.DB.Delete(&model.ModelGroup{}, id).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": err.Error()})
		return
	}
	proxy.DeleteRRIndex(g.Name)
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "已删除"})
}

// ToggleGroup 启用/禁用
func ToggleGroup(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id < 1 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "非法 id"})
		return
	}
	result := model.DB.Model(&model.ModelGroup{}).Where("id = ?", id).UpdateColumn("enabled", gorm.Expr("NOT enabled"))
	if result.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "更新失败: " + result.Error.Error()})
		return
	}
	if result.RowsAffected == 0 {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "模型组不存在"})
		return
	}
	var g model.ModelGroup
	if err := model.DB.First(&g, id).Error; err == nil {
		c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"enabled": g.Enabled}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "状态已切换"})
}

// TestGroup 测试模型组
func TestGroup(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id < 1 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "非法 id"})
		return
	}
	var g model.ModelGroup
	if err := model.DB.First(&g, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "模型组不存在"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "测试接口(请通过 /v1/responses 实测)", "data": groupToDTO(g, true)})
}

type createGroupReq struct {
	Name          string               `json:"name"`
	Description   string               `json:"description"`
	MainTextModel model.UpstreamModel  `json:"main_text_model"`
	ImageModels   []model.UpstreamModel `json:"image_models"`
	ImageStrategy string               `json:"image_strategy"`
	ImagePrompt   string               `json:"image_prompt"`
	ReplaceImage  bool                 `json:"replace_image"`
	Enabled       bool                 `json:"enabled"`
}

const (
	maxNameLength        = 128
	maxDescriptionLength = 512
)

func validateUpstreamModel(m *model.UpstreamModel, label string) error {
	if m.APIType == "" {
		m.APIType = "openai"
	}
	valid := false
	switch m.APIType {
	case "openai", "azure", "anthropic":
		valid = true
	}
	if !valid {
		return errStr(label + " api_type 仅支持 openai/azure/anthropic")
	}
	if m.MaxRetries < 0 {
		return errStr(label + " max_retries 不能小于 0")
	}
	if m.MaxRetries == 0 {
		m.MaxRetries = 1
	}
	if m.Weight < 0 {
		return errStr(label + " weight 不能为负数")
	}
	if m.Weight == 0 {
		m.Weight = 1
	}
	m.BaseURL = strings.TrimSpace(m.BaseURL)
	m.Model = strings.TrimSpace(m.Model)
	return nil
}

func validateGroupReq(r *createGroupReq) error {
	r.Name = strings.TrimSpace(r.Name)
	if r.Name == "" {
		return errStr("模型组名称不能为空")
	}
	if len(r.Name) > maxNameLength {
		return errStr("模型组名称不能超过 " + strconv.Itoa(maxNameLength) + " 字符")
	}
	r.Description = strings.TrimSpace(r.Description)
	if len(r.Description) > maxDescriptionLength {
		return errStr("描述不能超过 " + strconv.Itoa(maxDescriptionLength) + " 字符")
	}
	r.ImagePrompt = strings.TrimSpace(r.ImagePrompt)
	// 先 trim 再做空值校验, 防止纯空白字符串绕过
	r.MainTextModel.BaseURL = strings.TrimSpace(r.MainTextModel.BaseURL)
	r.MainTextModel.Model = strings.TrimSpace(r.MainTextModel.Model)
	r.MainTextModel.APIKey = strings.TrimSpace(r.MainTextModel.APIKey)
	if r.MainTextModel.BaseURL == "" || r.MainTextModel.APIKey == "" || r.MainTextModel.Model == "" {
		return errStr("主对话模型需填写 base_url/api_key/model")
	}
	if err := validateUpstreamModel(&r.MainTextModel, "主对话模型"); err != nil {
		return err
	}
	if r.ImageStrategy == "" {
		r.ImageStrategy = "round_robin"
	}
	if r.ImageStrategy != "round_robin" && r.ImageStrategy != "failover" {
		return errStr("图片策略仅支持 round_robin / failover")
	}
	for i := range r.ImageModels {
		r.ImageModels[i].BaseURL = strings.TrimSpace(r.ImageModels[i].BaseURL)
		r.ImageModels[i].Model = strings.TrimSpace(r.ImageModels[i].Model)
		r.ImageModels[i].APIKey = strings.TrimSpace(r.ImageModels[i].APIKey)
		if r.ImageModels[i].BaseURL == "" {
			return errStr("图片模型 base_url 不能为空")
		}
		if r.ImageModels[i].APIKey == "" {
			return errStr("图片模型 api_key 不能为空")
		}
		if r.ImageModels[i].Model == "" {
			return errStr("图片模型 model 不能为空")
		}
		if err := validateUpstreamModel(&r.ImageModels[i], "图片模型"); err != nil {
			return err
		}
	}
	return nil
}

func errStr(s string) error { return &validateErr{msg: s} }

type validateErr struct{ msg string }

func (e *validateErr) Error() string { return e.msg }

func isUniqueConstraintErr(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "UNIQUE constraint failed") || strings.Contains(s, "Duplicate entry")
}

func groupToDTO(g model.ModelGroup, maskKey bool) gin.H {
	var main model.UpstreamModel
	if err := json.Unmarshal([]byte(g.MainTextModel), &main); err != nil {
		log.Printf("[fci] warn: unmarshal main_text_model for group %d failed: %v", g.ID, err)
	}
	var imgs []model.UpstreamModel
	if err := json.Unmarshal([]byte(g.ImageModels), &imgs); err != nil {
		log.Printf("[fci] warn: unmarshal image_models for group %d failed: %v", g.ID, err)
	}
	if maskKey {
		main.APIKey = maskKeyStr(main.APIKey)
		for i := range imgs {
			imgs[i].APIKey = maskKeyStr(imgs[i].APIKey)
		}
	}
	return gin.H{
		"id":              g.ID,
		"name":            g.Name,
		"description":     g.Description,
		"main_text_model": main,
		"image_models":    imgs,
		"image_strategy":  g.ImageStrategy,
		"image_prompt":    g.ImagePrompt,
		"replace_image":   g.ReplaceImage,
		"enabled":         g.Enabled,
		"created_at":      g.CreatedAt,
		"updated_at":      g.UpdatedAt,
	}
}

func maskKeyStr(k string) string {
	if k == "" {
		return ""
	}
	if len(k) <= 4 {
		return strings.Repeat("*", len(k))
	}
	return strings.Repeat("*", len(k)-4) + k[len(k)-4:]
}
