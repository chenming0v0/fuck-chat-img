package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/fuck-chat-img/fci/internal/auth"
	"github.com/fuck-chat-img/fci/internal/cache"
	"github.com/fuck-chat-img/fci/internal/model"
	"github.com/gin-gonic/gin"
)

// ListHistory 历史记录列表(管理员看全部, 普通用户只看自己的)
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
	// 用户隔离: 非管理员只能查看自己的历史
	if isAdmin, _ := c.Get(auth.ContextKeyAdmin); isAdmin != true {
		uid, _ := c.Get(auth.ContextKeyUserID)
		if userID, ok := uid.(uint); ok {
			q = q.Where("user_id = ?", userID)
		} else {
			q = q.Where("user_id = ?", 0)
		}
	}
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

// GetHistory 单条历史详情(非管理员只能查看自己的)
func GetHistory(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var h model.History
	if err := model.DB.First(&h, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "记录不存在"})
		return
	}
	// 用户隔离: 非管理员不能查看他人记录
	if isAdmin, _ := c.Get(auth.ContextKeyAdmin); isAdmin != true {
		uid, _ := c.Get(auth.ContextKeyUserID)
		userID, _ := uid.(uint)
		if h.UserID != userID {
			c.JSON(http.StatusForbidden, gin.H{"success": false, "message": "无权查看该记录"})
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": h})
}

// DeleteHistory 删除单条
func DeleteHistory(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	if err := model.DB.Delete(&model.History{}, id).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "已删除"})
}

// ClearHistory 清空历史
func ClearHistory(c *gin.Context) {
	if err := model.DB.Where("1 = 1").Delete(&model.History{}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "已清空"})
}

// HistoryStats 历史统计(管理员看全部, 普通用户只看自己的)
func HistoryStats(c *gin.Context) {
	var total, successCount, failCount, cacheHitCount int64
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())

	q := model.DB.Model(&model.History{})
	// 用户隔离
	if isAdmin, _ := c.Get(auth.ContextKeyAdmin); isAdmin != true {
		uid, _ := c.Get(auth.ContextKeyUserID)
		if userID, ok := uid.(uint); ok {
			q = q.Where("user_id = ?", userID)
		} else {
			q = q.Where("user_id = ?", 0)
		}
	}

	q.Count(&total)
	q.Where("success = ?", true).Count(&successCount)
	q.Where("success = ?", false).Count(&failCount)
	q.Where("cache_hit = ?", true).Count(&cacheHitCount)

	var todayCount int64
	q.Where("created_at >= ?", today).Count(&todayCount)

	var avgLatency float64
	type avgRes struct{ Avg float64 }
	var ar avgRes
	q.Select("COALESCE(AVG(latency_ms),0) as avg").Scan(&ar)
	avgLatency = ar.Avg

	var totalTokens int64
	type tokRes struct{ Sum int64 }
	var tr tokRes
	q.Select("COALESCE(SUM(total_tokens),0) as sum").Scan(&tr)
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
