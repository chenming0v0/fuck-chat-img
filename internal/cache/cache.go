package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fuck-chat-img/fci/internal/config"
)

// Entry 缓存条目
//
// 不可变契约(M9): Get 返回的 *Entry 中所有字段(Value/StreamEvents 等)必须视为只读.
// 调用方若对 slice 做原地修改会污染缓存中所有后续读者(共享底层数组).
// 当前调用方(replayCacheEntry/cacheHitOutputSummary/usageFromCacheEntry)均只读.
type Entry struct {
	Key       string
	Value     []byte // 完整响应体(非流式)或合并后的流式事件序列(用于回放)
	StreamEvents [][]byte // 若为流式响应, 存储事件列表用于回放
	IsStream  bool
	ModelName string
	CreatedAt time.Time
	HitCount  int64
	// 写入缓存时记录的请求元数据, 命中回放时用于回填历史记录, 避免硬编码失真
	HasImage      bool
	ImageCount    int
	ImageModelUsed string
}

// Store 内存 LRU 缓存
type Store struct {
	mu       sync.RWMutex
	items    map[string]*Entry
	order    []string // LRU 顺序(头部=最久未用, 尾部=最近使用)
	maxItems int
	ttl      time.Duration // 条目生存期, 0 表示不限
	enabled  bool
}

var store *Store

// 默认缓存 TTL: 上游模型若升级/重训/调整行为, 旧响应不应被无限期回放(M8)
const defaultCacheTTL = 24 * time.Hour

// Init 初始化缓存
func Init() {
	cfg := config.Get()
	store = &Store{
		items:    make(map[string]*Entry),
		maxItems: cfg.CacheMaxItems,
		ttl:      defaultCacheTTL,
		enabled:  cfg.CacheEnabled,
	}
}

// Enabled 是否启用
func Enabled() bool {
	return store != nil && store.enabled
}

// Key 根据模型组名与规范化后的输入计算稳定缓存键
// 输入应当是规范化后的 canonical JSON 字节
func Key(modelGroup string, canonicalInput []byte) string {
	h := sha256.New()
	h.Write([]byte(modelGroup))
	h.Write([]byte{0})
	h.Write(canonicalInput)
	return hex.EncodeToString(h.Sum(nil))
}

// Get 读取缓存(LRU: 命中时将条目移到最近使用位置)
// TTL: 命中但已过期的条目视为未命中并删除, 避免无限期返回陈旧响应(M8)
func Get(key string) (*Entry, bool) {
	if store == nil || !store.enabled {
		return nil, false
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	e, ok := store.items[key]
	if !ok {
		return nil, false
	}
	// 过期检查: 命中但超过 TTL 视为未命中, 删除条目并返回 miss
	if store.ttl > 0 && time.Since(e.CreatedAt) > store.ttl {
		store.deleteLocked(key)
		return nil, false
	}
	e.HitCount++
	// LRU: 将命中的 key 移到末尾(最近使用)
	store.touchLocked(key)
	return e, true
}

// touchLocked 将 key 移到 order 末尾(调用方持锁)
func (s *Store) touchLocked(key string) {
	for i, k := range s.order {
		if k == key {
			s.order = append(s.order[:i], s.order[i+1:]...)
			s.order = append(s.order, key)
			return
		}
	}
}

// deleteLocked 删除指定 key(调用方持锁), 同步清理 order 切片
func (s *Store) deleteLocked(key string) {
	delete(s.items, key)
	for i, k := range s.order {
		if k == key {
			s.order = append(s.order[:i], s.order[i+1:]...)
			return
		}
	}
}

// PutWithMeta 写入缓存(非流式, 携带请求元数据用于命中回放时的历史回填)
func PutWithMeta(key, modelName string, value []byte, hasImage bool, imgCount int, imgModelUsed string) {
	putEntry(key, &Entry{
		Key:            key,
		Value:          value,
		IsStream:       false,
		ModelName:      modelName,
		CreatedAt:      time.Now(),
		HasImage:       hasImage,
		ImageCount:     imgCount,
		ImageModelUsed: imgModelUsed,
	})
}

// PutStreamWithMeta 写入缓存(流式, 携带请求元数据)
func PutStreamWithMeta(key, modelName string, events [][]byte, hasImage bool, imgCount int, imgModelUsed string) {
	putEntry(key, &Entry{
		Key:            key,
		StreamEvents:   events,
		IsStream:       true,
		ModelName:      modelName,
		CreatedAt:      time.Now(),
		HasImage:       hasImage,
		ImageCount:     imgCount,
		ImageModelUsed: imgModelUsed,
	})
}

// putEntry 实际写入逻辑(统一处理 LRU 顺序与淘汰)
func putEntry(key string, e *Entry) {
	if store == nil || !store.enabled {
		return
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, exists := store.items[key]; !exists {
		store.order = append(store.order, key)
		store.evictLocked()
	} else {
		// 已存在: 移到末尾(LRU)
		store.touchLocked(key)
	}
	store.items[key] = e
}

// evictLocked 淘汰超出上限的条目(LRU: 从 order 头部淘汰最久未用, 调用方持锁)
func (s *Store) evictLocked() {
	for len(s.order) > s.maxItems && len(s.order) > 0 {
		k := s.order[0]
		s.order = s.order[1:]
		delete(s.items, k)
	}
}

// Stats 缓存统计
type Stats struct {
	Enabled  bool  `json:"enabled"`
	Items    int   `json:"items"`
	MaxItems int   `json:"max_items"`
	Hits     int64 `json:"hits"`
	Misses   int64 `json:"misses"`
}

var (
	hits   int64
	misses int64
)

// RecordHit 记录命中(原子操作, 并发安全)
func RecordHit() { atomic.AddInt64(&hits, 1) }

// RecordMiss 记录未命中(原子操作, 并发安全)
func RecordMiss() { atomic.AddInt64(&misses, 1) }

// Stats 返回统计
func GetStats() Stats {
	if store == nil {
		return Stats{}
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	return Stats{
		Enabled:  store.enabled,
		Items:    len(store.items),
		MaxItems: store.maxItems,
		Hits:     atomic.LoadInt64(&hits),
		Misses:   atomic.LoadInt64(&misses),
	}
}

// Clear 清空缓存
func Clear() int {
	if store == nil {
		return 0
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	n := len(store.items)
	store.items = make(map[string]*Entry)
	store.order = nil
	return n
}
