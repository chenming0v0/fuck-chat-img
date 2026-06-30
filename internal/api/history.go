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

	// 用户隔离: 非管理员只能查看自己的历史.
	// 防御性写法: 若 context 中缺少 UserID(中间件不变量被破坏), 直接 403 拒绝,
	// 而不是回落到 user_id=0 暴露匿名代理历史(Low-8 越权风险).
	userID, ok := currentUserID(c)
	if !ok {
		c.JSON(http.StatusForbidden, gin.H{"success": false, "message": "无权访问"})
		return
	}
	isAdmin := isAdminContext(c)

	// applyFilters: 把过滤条件应用到给定 session, 避免复用同一 *gorm.DB 链
	// 导致 Count/Find 的 SELECT 子句相互污染(M1, 与 HistoryStats 一致的做法).
	applyFilters := func(q *gorm.DB) *gorm.DB {
		if !isAdmin {
			q = q.Where("user_id = ?", userID)
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

// GetHistory 单条历史详情(非管理员只能查看自己的)
func GetHistory(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id < 1 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "非法 id"})
		return
	}
	var h model.History
	if err := model.DB.First(&h, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "记录不存在"})
		return
	}
	// 用户隔离: 非管理员不能查看他人记录(包括匿名代理历史 user_id=0)
	if !isAdminContext(c) {
		userID, ok := currentUserID(c)
		if !ok || h.UserID != userID {
			c.JSON(http.StatusForbidden, gin.H{"success": false, "message": "无权查看该记录"})
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": h})
}

// DeleteHistory 删除单条
func DeleteHistory(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id < 1 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "非法 id"})
		return
	}
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
//
// 实现: 每个独立统计都基于 model.DB.Session(&gorm.Session{}) 起一个干净的查询,
// 避免复用同一 *gorm.DB 链导致的 Where/Select 残留污染(GORM v2 经典 footgun,
// 连续 Count/Scan 复用同一链时 SELECT 子句会相互干扰, 导致统计错误).
func HistoryStats(c *gin.Context) {
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())

	// 用户隔离: 缺 UserID 直接 403, 不回落 user_id=0 暴露匿名代理历史
	userID, ok := currentUserID(c)
	if !ok {
		c.JSON(http.StatusForbidden, gin.H{"success": false, "message": "无权访问"})
		return
	}
	isAdmin := isAdminContext(c)

	// baseScope: 仅返回带用户隔离 Where 的查询构造器, 不携带任何 SELECT/额外条件
	baseScope := func() *gorm.DB {
		q := model.DB.Session(&gorm.Session{}).Model(&model.History{})
		if !isAdmin {
			return q.Where("user_id = ?", userID)
		}
		return q
	}

	var total, successCount, failCount, cacheHitCount, todayCount int64
	baseScope().Count(&total)
	baseScope().Where("success = ?", true).Count(&successCount)
	baseScope().Where("success = ?", false).Count(&failCount)
	baseScope().Where("cache_hit = ?", true).Count(&cacheHitCount)
	baseScope().Where("created_at >= ?", today).Count(&todayCount)

	var avgLatency float64
	type avgRes struct{ Avg float64 }
	var ar avgRes
	_ = baseScope().Select("COALESCE(AVG(latency_ms),0) as avg").Scan(&ar).Error
	avgLatency = ar.Avg

	var totalTokens int64
	type tokRes struct{ Sum int64 }
	var tr tokRes
	_ = baseScope().Select("COALESCE(SUM(total_tokens),0) as sum").Scan(&tr).Error
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
