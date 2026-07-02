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
	if err := model.DB.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&model.History{}).Error; err != nil {
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

	q := model.DB.Session(&gorm.Session{}).Model(&model.History{})
	if !isAdmin {
		q = q.Where("user_id = ?", userID)
	}

	type statsRes struct {
		Total       int64   `gorm:"column:total"`
		Success     int64   `gorm:"column:success_count"`
		Fail        int64   `gorm:"column:fail_count"`
		CacheHit    int64   `gorm:"column:cache_hit_count"`
		Today       int64   `gorm:"column:today_count"`
		AvgLatency  float64 `gorm:"column:avg_latency"`
		TotalTokens int64   `gorm:"column:total_tokens"`
	}
	var res statsRes
	selectSQL := `
		COUNT(*) as total,
		COALESCE(SUM(CASE WHEN success THEN 1 ELSE 0 END), 0) as success_count,
		COALESCE(SUM(CASE WHEN NOT success THEN 1 ELSE 0 END), 0) as fail_count,
		COALESCE(SUM(CASE WHEN cache_hit THEN 1 ELSE 0 END), 0) as cache_hit_count,
		COALESCE(SUM(CASE WHEN created_at >= ? THEN 1 ELSE 0 END), 0) as today_count,
		COALESCE(AVG(latency_ms), 0) as avg_latency,
		COALESCE(SUM(total_tokens), 0) as total_tokens
	`
	if err := q.Select(selectSQL, today).Scan(&res).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"total":        res.Total,
			"success":      res.Success,
			"fail":         res.Fail,
			"cache_hit":    res.CacheHit,
			"today":        res.Today,
			"avg_latency":  res.AvgLatency,
			"total_tokens": res.TotalTokens,
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
