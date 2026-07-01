package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/fuck-chat-img/fci/internal/cache"
	"github.com/fuck-chat-img/fci/internal/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func escapeLike(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "%", "\\%")
	s = strings.ReplaceAll(s, "_", "\\_")
	return s
}

// ListHistory 历史记录列表
func ListHistory(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("p", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("size", "20"))
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 100 {
		size = 20
	}
	keyword := strings.TrimSpace(c.Query("keyword"))
	group := strings.TrimSpace(c.Query("group"))
	success := c.Query("success")
	cacheHit := c.Query("cache_hit")

	userID, ok := currentUserID(c)
	if !ok {
		c.JSON(http.StatusForbidden, gin.H{"success": false, "message": "无权访问"})
		return
	}
	isAdmin := isAdminContext(c)

	applyFilters := func(q *gorm.DB) *gorm.DB {
		if !isAdmin {
			q = q.Where("user_id = ?", userID)
		}
		if keyword != "" {
			escaped := escapeLike(keyword)
			q = q.Where("request_id LIKE ? ESCAPE '\\' OR input_summary LIKE ? ESCAPE '\\' OR output_summary LIKE ? ESCAPE '\\' OR error_message LIKE ? ESCAPE '\\'",
				"%"+escaped+"%", "%"+escaped+"%", "%"+escaped+"%", "%"+escaped+"%")
		}
		if group != "" {
			q = q.Where("model_group = ?", group)
		}
		if success == "true" {
			q = q.Where("success = ?", true)
		} else if success == "false" {
			q = q.Where("success = ?", false)
		}
		if cacheHit == "true" {
			q = q.Where("cache_hit = ?", true)
		} else if cacheHit == "false" {
			q = q.Where("cache_hit = ?", false)
		}
		return q
	}

	var total int64
	if err := applyFilters(model.DB.Session(&gorm.Session{}).Model(&model.History{})).Count(&total).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": err.Error()})
		return
	}
	var list []model.History
	if err := applyFilters(model.DB.Session(&gorm.Session{}).Model(&model.History{})).
		Order("id DESC").Offset((page - 1) * size).Limit(size).Find(&list).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    list,
		"total":   total,
		"page":    page,
		"size":    size,
	})
}

// GetHistory 单条历史详情
func GetHistory(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id < 1 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "非法 id"})
		return
	}
	userID, ok := currentUserID(c)
	if !ok {
		c.JSON(http.StatusForbidden, gin.H{"success": false, "message": "无权访问"})
		return
	}
	isAdmin := isAdminContext(c)

	var h model.History
	q := model.DB.Model(&model.History{})
	if !isAdmin {
		q = q.Where("user_id = ?", userID)
	}
	if err := q.First(&h, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "记录不存在"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": h})
}

// DeleteHistory 删除单条
func DeleteHistory(c *gin.Context) {
	if !isAdminContext(c) {
		c.JSON(http.StatusForbidden, gin.H{"success": false, "message": "无权访问"})
		return
	}
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id < 1 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "非法 id"})
		return
	}
	if err := model.DB.Delete(&model.History{}, id).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "已删除"})
}

// ClearHistory 清空历史
func ClearHistory(c *gin.Context) {
	if !isAdminContext(c) {
		c.JSON(http.StatusForbidden, gin.H{"success": false, "message": "无权访问"})
		return
	}
	if err := model.DB.Where("1 = 1").Delete(&model.History{}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "已清空"})
}

// HistoryStats 历史统计
func HistoryStats(c *gin.Context) {
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())

	userID, ok := currentUserID(c)
	if !ok {
		c.JSON(http.StatusForbidden, gin.H{"success": false, "message": "无权访问"})
		return
	}
	isAdmin := isAdminContext(c)

	baseScope := func() *gorm.DB {
		q := model.DB.Session(&gorm.Session{}).Model(&model.History{})
		if !isAdmin {
			return q.Where("user_id = ?", userID)
		}
		return q
	}

	var total, successCount, failCount, cacheHitCount, todayCount int64
	if err := baseScope().Count(&total).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": err.Error()})
		return
	}
	if err := baseScope().Where("success = ?", true).Count(&successCount).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": err.Error()})
		return
	}
	if err := baseScope().Where("success = ?", false).Count(&failCount).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": err.Error()})
		return
	}
	if err := baseScope().Where("cache_hit = ?", true).Count(&cacheHitCount).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": err.Error()})
		return
	}
	if err := baseScope().Where("created_at >= ?", today).Count(&todayCount).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": err.Error()})
		return
	}

	var avgLatency float64
	type avgRes struct{ Avg float64 }
	var ar avgRes
	if err := baseScope().Select("COALESCE(AVG(latency_ms),0) as avg").Scan(&ar).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": err.Error()})
		return
	}
	avgLatency = ar.Avg

	var totalTokens int64
	type tokRes struct{ Sum int64 }
	var tr tokRes
	if err := baseScope().Select("COALESCE(SUM(total_tokens),0) as sum").Scan(&tr).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": err.Error()})
		return
	}
	totalTokens = tr.Sum

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"total":        total,
			"success":      successCount,
			"fail":         failCount,
			"cache_hit":    cacheHitCount,
			"today":        todayCount,
			"avg_latency":  avgLatency,
			"total_tokens": totalTokens,
			"cache_stats":  cache.GetStats(),
		},
	})
}

// CacheStats 缓存统计
func CacheStats(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"success": true, "data": cache.GetStats()})
}

// CacheClear 清空缓存
func CacheClear(c *gin.Context) {
	n := cache.Clear()
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "已清空", "cleared": n})
}
