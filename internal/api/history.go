package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/fuck-chat-img/fci/internal/cache"
	"github.com/fuck-chat-img/fci/internal/model"
	"github.com/gin-gonic/gin"
)

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

	q := model.DB.Model(&model.History{})
	if keyword != "" {
		q = q.Where("request_id LIKE ? OR input_summary LIKE ? OR output_summary LIKE ? OR error_message LIKE ?",
			"%"+keyword+"%", "%"+keyword+"%", "%"+keyword+"%", "%"+keyword+"%")
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
	}

	var total int64
	q.Count(&total)
	var list []model.History
	q.Order("id DESC").Offset((page - 1) * size).Limit(size).Find(&list)
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
	id, _ := strconv.Atoi(c.Param("id"))
	var h model.History
	if err := model.DB.First(&h, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "记录不存在"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": h})
}

// DeleteHistory 删除单条
func DeleteHistory(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	model.DB.Delete(&model.History{}, id)
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "已删除"})
}

// ClearHistory 清空历史
func ClearHistory(c *gin.Context) {
	model.DB.Where("1 = 1").Delete(&model.History{})
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "已清空"})
}

// HistoryStats 历史统计
func HistoryStats(c *gin.Context) {
	var total, successCount, failCount, cacheHitCount int64
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())

	model.DB.Model(&model.History{}).Count(&total)
	model.DB.Model(&model.History{}).Where("success = ?", true).Count(&successCount)
	model.DB.Model(&model.History{}).Where("success = ?", false).Count(&failCount)
	model.DB.Model(&model.History{}).Where("cache_hit = ?", true).Count(&cacheHitCount)

	var todayCount int64
	model.DB.Model(&model.History{}).Where("created_at >= ?", today).Count(&todayCount)

	var avgLatency float64
	type avgRes struct{ Avg float64 }
	var ar avgRes
	model.DB.Model(&model.History{}).Select("COALESCE(AVG(latency_ms),0) as avg").Scan(&ar)
	avgLatency = ar.Avg

	var totalTokens int64
	type tokRes struct{ Sum int64 }
	var tr tokRes
	model.DB.Model(&model.History{}).Select("COALESCE(SUM(total_tokens),0) as sum").Scan(&tr)
	totalTokens = tr.Sum

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"total":         total,
			"success":       successCount,
			"fail":          failCount,
			"cache_hit":     cacheHitCount,
			"today":         todayCount,
			"avg_latency":   avgLatency,
			"total_tokens":  totalTokens,
			"cache_stats":   cache.GetStats(),
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
